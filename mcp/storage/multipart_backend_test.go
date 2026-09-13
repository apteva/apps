package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestS3MultipartWireProtocol(t *testing.T) {
	completed := false
	aborted := false
	lists := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.Method == "PUT" && q.Get("uploadId") == "remote-id":
			body, _ := io.ReadAll(r.Body)
			if string(body) != "abc" || q.Get("partNumber") != "2" || r.Header.Get("Authorization") == "" {
				t.Error("invalid relay request")
			}
			w.Header().Set("ETag", `"etag2"`)
		case r.Method == "POST" && q.Has("uploads"):
			fmt.Fprint(w, `<InitiateMultipartUploadResult><Bucket>test-bucket</Bucket><Key>00/video.mp4</Key><UploadId>remote-id</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == "GET" && q.Get("uploadId") == "remote-id":
			lists++
			if q.Get("part-number-marker") == "0" || q.Get("part-number-marker") == "" {
				fmt.Fprint(w, `<ListPartsResult><IsTruncated>true</IsTruncated><NextPartNumberMarker>1</NextPartNumberMarker><Part><PartNumber>1</PartNumber><ETag>etag1</ETag><Size>5242880</Size></Part></ListPartsResult>`)
			} else {
				fmt.Fprint(w, `<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>2</PartNumber><ETag>etag2</ETag><Size>3</Size></Part></ListPartsResult>`)
			}
		case r.Method == "POST" && q.Get("uploadId") == "remote-id":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "etag1") || !strings.Contains(string(body), "etag2") {
				t.Error("completion omitted authoritative etags")
			}
			completed = true
			fmt.Fprint(w, `<CompleteMultipartUploadResult><Bucket>test-bucket</Bucket><Key>00/video.mp4</Key><ETag>complete-etag</ETag></CompleteMultipartUploadResult>`)
		case r.Method == "DELETE" && q.Get("uploadId") == "remote-id":
			aborted = true
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected S3 request %s %s", r.Method, r.URL)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("test-key", "test-secret", ""), Secure: false, Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	be := &s3Backend{client: client, bucket: "test-bucket"}
	c := context.Background()
	id, err := be.BeginMultipart(c, "00/video.mp4", "video/mp4")
	if err != nil || id != "remote-id" {
		t.Fatalf("begin %s %v", id, err)
	}
	signed, err := be.SignMultipartPart(c, "00/video.mp4", id, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(signed)
	q := u.Query()
	if q.Get("uploadId") != id || q.Get("partNumber") != "2" || !strings.Contains(q.Get("X-Amz-SignedHeaders"), "content-length") {
		t.Fatalf("unbounded part capability: %s", u.RawQuery)
	}
	p, err := be.PutMultipartPart(c, "00/video.mp4", id, 2, strings.NewReader("abc"), 3)
	if err != nil || p.Size != 3 || p.ETag != "etag2" || p.Number != 2 {
		t.Fatalf("relay: %+v %v", p, err)
	}
	parts, err := be.MultipartParts(c, "00/video.mp4", id)
	if err != nil || len(parts) != 2 || lists != 2 {
		t.Fatalf("parts %v %v requests %d", parts, err, lists)
	}
	if err = be.FinishMultipart(c, "00/video.mp4", id, parts); err != nil {
		t.Fatal(err)
	}
	if err = be.AbortMultipart(c, "00/video.mp4", id); err != nil {
		t.Fatal(err)
	}
	if !completed || !aborted {
		t.Fatal("incomplete lifecycle")
	}
}
