package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestUploadRenderFile_UsesChunkedUploadAndVerifiesStoredBytes(t *testing.T) {
	payload := []byte("render-output-large-enough-for-multiple-parts")
	wantSHA := sha256Hex(payload)
	tmp := writeTempPayload(t, payload)

	var got bytes.Buffer
	var initSeen bool
	var completeSeen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/storage/uploads":
			initSeen = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode init: %v", err)
			}
			if body["source"] != "media-render" {
				t.Errorf("source=%v, want media-render", body["source"])
			}
			json.NewEncoder(w).Encode(map[string]any{
				"upload_id":    "UP1",
				"part_size":    7,
				"max_parallel": 1,
				"max_parts":    100,
			})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/apps/storage/uploads/UP1/parts/"):
			b, _ := io.ReadAll(r.Body)
			got.Write(b)
			json.NewEncoder(w).Encode(map[string]any{"size": len(b)})
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/storage/uploads/UP1/complete":
			completeSeen = true
			json.NewEncoder(w).Encode(map[string]any{
				"file": map[string]any{
					"id":           77,
					"name":         "out.mov",
					"folder":       "/renders/",
					"content_type": "video/quicktime",
					"size_bytes":   len(payload),
					"sha256":       wantSHA,
				},
				"was_existing": false,
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &storageClient{base: srv.URL, token: "test", httpClient: srv.Client()}
	id, err := c.UploadRenderFile(context.Background(), "p1", "/renders/", "out.mov", "video/quicktime", tmp)
	if err != nil {
		t.Fatal(err)
	}
	if id != 77 {
		t.Fatalf("id=%d want 77", id)
	}
	if !initSeen || !completeSeen {
		t.Fatalf("initSeen=%v completeSeen=%v", initSeen, completeSeen)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("uploaded bytes mismatch: %q", got.Bytes())
	}
}

func TestUploadRenderFile_RejectsStoredSizeMismatch(t *testing.T) {
	payload := []byte("render-output")
	tmp := writeTempPayload(t, payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/storage/uploads":
			json.NewEncoder(w).Encode(map[string]any{"upload_id": "UP1", "part_size": 1024, "max_parts": 100})
		case r.Method == http.MethodPut:
			json.NewEncoder(w).Encode(map[string]any{"size": 1})
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/storage/uploads/UP1/complete":
			json.NewEncoder(w).Encode(map[string]any{
				"file": map[string]any{
					"id":         78,
					"size_bytes": len(payload) - 1,
					"sha256":     sha256Hex(payload),
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &storageClient{base: srv.URL, token: "test", httpClient: srv.Client()}
	_, err := c.UploadRenderFile(context.Background(), "p1", "/renders/", "out.mov", "video/quicktime", tmp)
	if err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("err=%v, want size mismatch", err)
	}
}

func writeTempPayload(t *testing.T, payload []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "payload-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
