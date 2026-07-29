package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStorageHTTPClientMultipartUpload(t *testing.T) {
	var initSeen bool
	var parts [][]byte
	var completeSeen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("missing auth header: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("project_id") != "p1" {
			t.Fatalf("missing project_id: %q", r.URL.RawQuery)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == storageBoundProxyPath+"/uploads":
			initSeen = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["filename"] != "video.mp4" {
				t.Fatalf("filename = %v", body["filename"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": "UP1", "part_size": 4})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, storageBoundProxyPath+"/uploads/UP1/parts/"):
			body, _ := io.ReadAll(r.Body)
			parts = append(parts, body)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.Method == http.MethodPost && r.URL.Path == storageBoundProxyPath+"/uploads/UP1/complete":
			completeSeen = true
			_ = json.NewEncoder(w).Encode(map[string]any{"file": map[string]any{"id": 42, "url": "signed"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	c := &storageHTTPClient{base: srv.URL, token: "token", httpClient: srv.Client()}
	got, err := c.uploadMultipart(t.Context(), "p1", bytes.NewReader([]byte("abcdefghi")), "video.mp4", "video/mp4", "/out", "private", []string{"x"}, 9, "sha", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 42 || got.URL != "signed" {
		t.Fatalf("got %+v", got)
	}
	if !initSeen || !completeSeen {
		t.Fatalf("initSeen=%v completeSeen=%v", initSeen, completeSeen)
	}
	if len(parts) != 3 || string(parts[0]) != "abcd" || string(parts[1]) != "efgh" || string(parts[2]) != "i" {
		t.Fatalf("parts = %q", parts)
	}
}

func TestStorageHTTPClientRetriesPartUpload(t *testing.T) {
	partAttempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == storageBoundProxyPath+"/uploads":
			_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": "UP1", "part_size": 4})
		case r.Method == http.MethodPut:
			partAttempts++
			if partAttempts == 1 {
				http.Error(w, "temporary", http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/complete"):
			_ = json.NewEncoder(w).Encode(map[string]any{"file": map[string]any{"id": 7}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c := &storageHTTPClient{base: srv.URL, token: "token", httpClient: srv.Client()}
	if _, err := c.uploadMultipart(t.Context(), "p1", bytes.NewReader([]byte("data")), "a.mp3", "audio/mpeg", "/out", "private", nil, 4, "sha", nil); err != nil {
		t.Fatal(err)
	}
	if partAttempts != 2 {
		t.Fatalf("part attempts = %d, want 2", partAttempts)
	}
}

type cancelingReader struct {
	cancel context.CancelFunc
	read   bool
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	copy(p, "data")
	r.cancel()
	return 4, nil
}

func TestHashFileHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, err := hashFile(ctx, &cancelingReader{cancel: cancel}, 8, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("hash error = %v, want context canceled", err)
	}
}
