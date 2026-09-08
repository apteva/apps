package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in because this runs a second real app and can transfer gigabyte fixtures.
// GIGS_TEST_STORAGE_BIN must be a built Storage >=0.11.3 binary.
func TestBinaryUploadWithRealStorage(t *testing.T) {
	bin := os.Getenv("GIGS_TEST_STORAGE_BIN")
	remote := os.Getenv("GIGS_TEST_REMOTE_GATEWAY")
	if bin == "" && remote == "" {
		t.Skip("set GIGS_TEST_STORAGE_BIN to run the real Storage integration")
	}
	var target *url.URL
	var err error
	if remote != "" {
		if os.Getenv("GIGS_TEST_REMOTE_TOKEN") == "" || os.Getenv("GIGS_TEST_REMOTE_PROJECT") == "" {
			t.Fatal("remote token and project required")
		}
		target, err = url.Parse(remote)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		dir := t.TempDir()
		logfile, err := os.Create(filepath.Join(dir, "storage.log"))
		if err != nil {
			t.Fatal(err)
		}
		defer logfile.Close()
		identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "/whoami") {
				io.WriteString(w, `{"install_id":1,"app_name":"storage","bindings":{}}`)
				return
			}
			io.WriteString(w, `{}`)
		}))
		defer identity.Close()
		cmd := exec.Command(bin)
		cmd.Dir = "../storage"
		cmd.Env = append(os.Environ(), "APTEVA_APP_PORT="+strconv.Itoa(port), "APTEVA_APP_TOKEN=storage-integration-token", "APTEVA_PROJECT_ID=project-a", "APTEVA_GATEWAY_URL="+identity.URL, "DB_PATH="+filepath.Join(dir, "app.db"), "STORAGE_BLOBS_DIR="+filepath.Join(dir, "blobs"), "STORAGE_UPLOADS_DIR="+filepath.Join(dir, "uploads"), `APTEVA_APP_CONFIG={"max_upload_size_mb":"5120"}`)
		cmd.Stdout = logfile
		cmd.Stderr = logfile
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cmd.Process.Kill()
			cmd.Wait()
			if t.Failed() {
				raw, _ := os.ReadFile(logfile.Name())
				t.Log(string(raw))
			}
		})
		target, _ = url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
		healthy := false
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			r, e := http.Get(target.String() + "/health")
			if e == nil {
				r.Body.Close()
				if r.StatusCode == 200 {
					healthy = true
					break
				}
			}
		}
		if !healthy {
			t.Fatal("Storage failed to start")
		}
	}
	var lostComplete atomic.Bool
	var createdFile atomic.Int64
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.Header.Set("Accept-Encoding", "identity")
		if remote != "" {
			r.Header.Set("Authorization", "Bearer "+os.Getenv("GIGS_TEST_REMOTE_TOKEN"))
			q := r.URL.Query()
			q.Set("project_id", os.Getenv("GIGS_TEST_REMOTE_PROJECT"))
			r.URL.RawQuery = q.Encode()
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/callback/apps/storage/proxy")
		r.Header.Set("Authorization", "Bearer storage-integration-token")
		r.Header.Set("X-User-ID", "1")
	}
	proxy.ModifyResponse = func(r *http.Response) error {
		if strings.HasSuffix(r.Request.URL.Path, "/complete") && r.StatusCode == 200 {
			data, e := io.ReadAll(r.Body)
			if e != nil {
				return e
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(data))
			var receipt storageCompletion
			if json.Unmarshal(data, &receipt) == nil && !receipt.WasExisting {
				createdFile.Store(receipt.File.ID)
			}
		}
		if strings.HasSuffix(r.Request.URL.Path, "/complete") && r.StatusCode == 200 && lostComplete.CompareAndSwap(false, true) {
			r.Body.Close()
			data := `{"error":"injected lost completion response"}`
			r.StatusCode = 503
			r.Body = io.NopCloser(strings.NewReader(data))
			r.ContentLength = int64(len(data))
			r.Header.Set("Content-Length", strconv.Itoa(len(data)))
		}
		return nil
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gigs-integration-token" || r.URL.Query().Get("project_id") != "project-a" {
			http.Error(w, "incorrect binding credentials or project", 403)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer gateway.Close()
	if remote != "" {
		defer func() {
			if id := createdFile.Load(); id > 0 {
				if e := storageHTTPJSON(context.Background(), "project-a", "DELETE", fmt.Sprintf("/files/%d", id), nil, nil); e != nil {
					t.Errorf("cleanup test file %d: %v", id, e)
				} else {
					t.Logf("removed production test file %d", id)
				}
			}
		}()
	}
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	t.Setenv("APTEVA_APP_TOKEN", "gigs-integration-token")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
	ctx, gid, aid := auditWorker(t, &auditStorage{})
	_, err = ctx.AppDB().Exec(`INSERT INTO gig_instructions(gig_id,sort_order,instruction_kind,rendered_body_json,result_key) VALUES (?,0,'input_video_recording','{}','clip')`, gid)
	if err != nil {
		t.Fatal(err)
	}
	size := int64(11<<20) + 123
	if raw := os.Getenv("GIGS_TEST_UPLOAD_BYTES"); raw != "" {
		size, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	app := &App{}
	request := func(path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		app.handleWorkerRoot(w, httptest.NewRequest("POST", "/worker/audit-token"+path, bytes.NewReader(raw)))
		return w
	}
	initBody := map[string]any{"instruction_key": "clip", "name": "gigs-release-audit-recording.mov", "content_type": "video/quicktime", "size_bytes": size, "transport": "binary", "client_key": strings.Repeat("a", 64)}
	w := request("/upload/init", initBody)
	if w.Code != 200 {
		t.Fatalf("init %d %s", w.Code, w.Body.String())
	}
	var init workerUpload
	json.Unmarshal(w.Body.Bytes(), &init)
	w = request("/upload/init", initBody)
	var again workerUpload
	json.Unmarshal(w.Body.Bytes(), &again)
	if again.ID != init.ID {
		t.Fatal("resume init created a duplicate session")
	}
	// Stable repeating fixture, constructed one part at a time.
	expectedHash := sha256.New()
	started := time.Now()
	for offset, n := int64(0), 1; offset < size; n++ {
		count := init.PartSize
		if size-offset < count {
			count = size - offset
		}
		data := bytes.Repeat([]byte{byte(n % 251)}, int(count))
		expectedHash.Write(data)
		r := httptest.NewRequest("PUT", fmt.Sprintf("/worker/audit-token/upload/part?upload_id=%s&part_number=%d", init.ID, n), bytes.NewReader(data))
		w = httptest.NewRecorder()
		app.handleWorkerRoot(w, r)
		if w.Code != 200 {
			t.Fatalf("part %d: %d %s", n, w.Code, w.Body.String())
		}
		if n == 1 {
			w = request("/upload/status", map[string]any{"upload_id": init.ID})
			var state storageUploadStatus
			json.Unmarshal(w.Body.Bytes(), &state)
			if w.Code != 200 || state.BytesUploaded != count {
				t.Fatalf("resume status %d %s", w.Code, w.Body.String())
			}
		}
		offset += count
	}
	w = request("/upload/complete", map[string]any{"upload_id": init.ID})
	if w.Code != 202 {
		t.Fatalf("complete %d %s", w.Code, w.Body.String())
	}
	var completed workerUpload
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		w = request("/upload/status", map[string]any{"upload_id": init.ID})
		if w.Code != 200 {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
		json.Unmarshal(w.Body.Bytes(), &completed)
		if completed.Status == "completed" {
			break
		}
		if completed.Status == "uploading" && completed.Stage == "finalize" {
			request("/upload/complete", map[string]any{"upload_id": init.ID})
		}
	}
	if completed.Status != "completed" || completed.FileID == 0 {
		t.Fatalf("not completed: %+v", completed)
	}
	if !lostComplete.Load() {
		t.Fatal("lost response fault was not exercised")
	}
	w = request("/upload/complete", map[string]any{"upload_id": init.ID})
	if w.Code != 200 {
		t.Fatalf("repeat complete: %d %s", w.Code, w.Body.String())
	}
	draft, err := loadWorkerDraft(ctx.AppDB(), aid)
	if err != nil || draft == nil || len(draftAttachmentIDs(draft.Payload)) != 1 {
		t.Fatalf("completed file not auto-saved to draft: %+v %v", draft, err)
	}
	var metadata struct {
		File struct {
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size_bytes"`
		} `json:"file"`
	}
	if err := storageHTTPJSON(context.Background(), "project-a", "GET", fmt.Sprintf("/files/%d", completed.FileID), nil, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.File.Size != size || metadata.File.SHA256 != hex.EncodeToString(expectedHash.Sum(nil)) {
		t.Fatalf("stored size/hash mismatch: %+v", metadata)
	}
	// Verify the committed object's bytes, not only the database checksum.
	req, err := http.NewRequest("GET", gateway.URL+fmt.Sprintf("/api/apps/callback/apps/storage/proxy/files/%d/content?project_id=project-a", completed.FileID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer gigs-integration-token")
	download, err := storageHTTPClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	actualHash := sha256.New()
	actualBytes, copyErr := io.Copy(actualHash, io.LimitReader(download.Body, size+1))
	download.Body.Close()
	if copyErr != nil || download.StatusCode != 200 || actualBytes != size || !bytes.Equal(actualHash.Sum(nil), expectedHash.Sum(nil)) {
		t.Fatalf("stored object mismatch: status=%d bytes=%d err=%v", download.StatusCode, actualBytes, copyErr)
	}
	t.Logf("verified upload and download of %d bytes, Storage SHA256 %s, lost-completion recovery, elapsed %s", size, metadata.File.SHA256, time.Since(started))
}
