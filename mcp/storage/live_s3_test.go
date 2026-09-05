//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// This profile uses a disposable local MinIO container, never a real bucket.
func TestLiveS3IntegrityAndDelivery(t *testing.T) {
	endpoint := os.Getenv("STORAGE_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set STORAGE_TEST_S3_ENDPOINT to run the disposable S3 profile")
	}
	c := context.Background()
	client, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4("storage-test", "storage-test-password", ""), Secure: false, Region: "us-east-1", BucketLookup: minio.BucketLookupPath})
	if err != nil {
		t.Fatal(err)
	}
	bucket := fmt.Sprintf("storage-audit-%d", time.Now().UnixNano())
	if err = client.MakeBucket(c, bucket, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for object := range client.ListObjects(c, bucket, minio.ListObjectsOptions{Recursive: true}) {
			if object.Err == nil {
				_ = client.RemoveObject(c, bucket, object.Key, minio.RemoveObjectOptions{})
			}
		}
		if err := client.RemoveBucket(c, bucket); err != nil {
			t.Error(err)
		}
	})
	appCtx := auditCtx(t)
	globalBackend = &s3Backend{client: client, bucket: bucket, region: "us-east-1", partSize: 5 << 20, uploadThreads: 2}
	app := &App{}
	payload := "verified live S3 bytes"
	w := httptest.NewRecorder()
	app.handleDirectInit(w, httptest.NewRequest("POST", "/files/init", strings.NewReader(fmt.Sprintf(`{"name":"live.txt","content_type":"text/plain","size_bytes":%d,"sha256":"%s"}`, len(payload), shaText(payload)))))
	var init struct {
		UploadID string            `json:"upload_id"`
		URL      string            `json:"upload_url"`
		Headers  map[string]string `json:"headers"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &init); err != nil || init.UploadID == "" {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	put := func(body string) {
		req, _ := http.NewRequest("PUT", init.URL, strings.NewReader(body))
		for k, v := range init.Headers {
			req.Header.Set(k, v)
		}
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatal(resp.StatusCode, string(raw))
		}
	}
	put(payload)
	finish := func() *File {
		w := httptest.NewRecorder()
		app.handleDirectFinalize(w, httptest.NewRequest("POST", "/files/"+init.UploadID+"/finalize", strings.NewReader(`{}`)), init.UploadID)
		var result struct {
			File *File `json:"file"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		if w.Code != 200 || result.File == nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return result.File
	}
	f := finish()
	if next := finish(); next.ID != f.ID {
		t.Fatal("non-idempotent completion")
	}
	put(strings.Repeat("X", len(payload)))
	read, e := backend().OpenObject(c, objectKey(f.SHA256, f.StorageKey), ObjectReadOptions{})
	if e != nil {
		t.Fatal(e)
	}
	actual, _ := io.ReadAll(read.Body)
	read.Body.Close()
	if !bytes.Equal(actual, []byte(payload)) {
		t.Fatal("reused PUT changed published object")
	}
	path := mintProxyPath(t, appCtx, f, DispositionInline, 60)
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Range", "bytes=0-7")
	w = httptest.NewRecorder()
	app.handlePublicFilesItem(w, r)
	if w.Code != 206 || w.Body.String() != payload[:8] || w.Header().Get("Location") != "" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	if _, err = dbUpdate(appCtx.AppDB(), f.ProjectID, f.ID, map[string]any{"visibility": "private"}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	app.handlePublicFilesItem(w, httptest.NewRequest("GET", path, nil))
	if w.Code != 403 {
		t.Fatal("share survived revoke", w.Code)
	}
	hard, err := deleteFile(appCtx, f.ProjectID, f.ID, false)
	if err != nil || !hard {
		t.Fatal(hard, err)
	}
}
