package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// startUpload posts /uploads with a project_id query param + user
// header, returning the parsed init response.
func startUpload(t *testing.T, app *App, body map[string]any) map[string]any {
	t.Helper()
	bj, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/uploads?project_id=test-proj",
		bytes.NewReader(bj))
	req.Header.Set("X-User-ID", "1")
	rec := httptest.NewRecorder()
	app.handleUploadsCollection(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("init: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func putPart(t *testing.T, app *App, id string, n int, chunk []byte, userID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", fmt.Sprintf("/uploads/%s/parts/%d?project_id=test-proj", id, n),
		bytes.NewReader(chunk))
	req.Header.Set("X-User-ID", userID)
	rec := httptest.NewRecorder()
	app.handleUploadsItem(rec, req)
	return rec
}

func completeUpload(t *testing.T, app *App, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/uploads/"+id+"/complete?project_id=test-proj",
		strings.NewReader(`{}`))
	req.Header.Set("X-User-ID", "1")
	rec := httptest.NewRecorder()
	app.handleUploadsItem(rec, req)
	return rec
}

// Whole-file resumable upload: 4 parts uploaded sequentially →
// complete → file row exists with the right sha256.
func TestUploads_HappyPath(t *testing.T) {
	ctx := newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	app := &App{}

	body := bytes.Repeat([]byte("apteva-storage-resumable\n"), 4096) // ~100 KB
	wantSHA := hex.EncodeToString(sha256SumOf(body))

	startUpload(t, app, map[string]any{
		"filename":     "demo.bin",
		"size":         len(body),
		"content_type": "application/octet-stream",
		"folder":       "/",
	})
	id := lastUploadID(t, app, body, "demo.bin")
	// Send 4 parts.
	chunkSize := len(body) / 4
	for n := 1; n <= 4; n++ {
		start := (n - 1) * chunkSize
		end := start + chunkSize
		if n == 4 {
			end = len(body)
		}
		rec := putPart(t, app, id, n, body[start:end], "1")
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT part %d: %d %s", n, rec.Code, rec.Body.String())
		}
	}
	rec := completeUpload(t, app, id)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	var rsp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &rsp)
	file := rsp["file"].(map[string]any)
	if file["sha256"].(string) != wantSHA {
		t.Errorf("sha mismatch: got %s want %s", file["sha256"], wantSHA)
	}
	if int64(file["size_bytes"].(float64)) != int64(len(body)) {
		t.Errorf("size mismatch: %v", file["size_bytes"])
	}
	if _, err := os.Stat(uploadSessionDir(ctx, id)); !os.IsNotExist(err) {
		t.Error("session dir lingered after complete")
	}
}

// lastUploadID returns the most recent upload session id by reading
// the uploads dir. We use this because the init helper above
// already advanced the test, but we want to address parts by id;
// avoiding a re-init here keeps the tests clean.
func lastUploadID(t *testing.T, _ *App, _ []byte, _ string) string {
	t.Helper()
	root := os.Getenv("STORAGE_UPLOADS_DIR")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var newest string
	var newestTime time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest = e.Name()
			newestTime = info.ModTime()
		}
	}
	if newest == "" {
		t.Fatal("no upload sessions on disk")
	}
	return newest
}

// PARALLEL: parts arrive out of order (the whole point of S3-shaped
// upload). Complete must concatenate in part_number order regardless.
func TestUploads_ParallelOutOfOrder(t *testing.T) {
	_ = newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	app := &App{}

	// Distinguishable parts so a wrong-order concat would obviously
	// produce the wrong hash.
	parts := [][]byte{
		bytes.Repeat([]byte("A"), 1024),
		bytes.Repeat([]byte("B"), 1024),
		bytes.Repeat([]byte("C"), 1024),
		bytes.Repeat([]byte("D"), 1024),
	}
	full := bytes.Join(parts, nil)
	wantSHA := hex.EncodeToString(sha256SumOf(full))

	startUpload(t, app, map[string]any{
		"filename": "ooo.bin",
		"size":     len(full),
	})
	id := lastUploadID(t, app, nil, "")

	// Race: upload all 4 parts concurrently, in arbitrary order.
	var wg sync.WaitGroup
	for _, n := range []int{3, 1, 4, 2} {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			rec := putPart(t, app, id, n, parts[n-1], "1")
			if rec.Code != http.StatusOK {
				t.Errorf("part %d: %d %s", n, rec.Code, rec.Body.String())
			}
		}(n)
	}
	wg.Wait()

	rec := completeUpload(t, app, id)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	var rsp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &rsp)
	if got := rsp["file"].(map[string]any)["sha256"].(string); got != wantSHA {
		t.Errorf("hash drift after parallel upload: got %s want %s", got, wantSHA)
	}
}

// Re-uploading the same part_number replaces the bytes (last write
// wins). Otherwise a transient PUT failure followed by a retry
// would corrupt the file with concatenated retries. We use rename
// from a temp file specifically to make this safe.
func TestUploads_PartReupload_LastWriteWins(t *testing.T) {
	_ = newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	app := &App{}

	good := bytes.Repeat([]byte("X"), 512)
	wantSHA := hex.EncodeToString(sha256SumOf(good))

	startUpload(t, app, map[string]any{"filename": "retry.bin", "size": len(good)})
	id := lastUploadID(t, app, nil, "")

	// First (truncated/wrong) attempt.
	if rec := putPart(t, app, id, 1, []byte("WRONG"), "1"); rec.Code != http.StatusOK {
		t.Fatalf("first put: %d", rec.Code)
	}
	// Retry with the correct bytes — should overwrite.
	if rec := putPart(t, app, id, 1, good, "1"); rec.Code != http.StatusOK {
		t.Fatalf("retry put: %d", rec.Code)
	}
	rec := completeUpload(t, app, id)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	var rsp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &rsp)
	if got := rsp["file"].(map[string]any)["sha256"].(string); got != wantSHA {
		t.Errorf("retry didn't overwrite: got %s want %s", got, wantSHA)
	}
}

// Complete must refuse if any part is missing — half-uploaded files
// shouldn't materialise.
func TestUploads_GapInPartsRejected(t *testing.T) {
	_ = newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	app := &App{}

	startUpload(t, app, map[string]any{"filename": "gap.bin", "size": 200})
	id := lastUploadID(t, app, nil, "")

	// Upload 1 and 3, skip 2.
	putPart(t, app, id, 1, bytes.Repeat([]byte("A"), 100), "1")
	putPart(t, app, id, 3, bytes.Repeat([]byte("C"), 100), "1")

	rec := completeUpload(t, app, id)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on gap, got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "missing part 2") {
		t.Errorf("error should name the missing part: %s", rec.Body.String())
	}
}

// Total declared size must match the sum of parts.
func TestUploads_SizeMismatchRejected(t *testing.T) {
	_ = newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	app := &App{}

	startUpload(t, app, map[string]any{"filename": "sz.bin", "size": 1000})
	id := lastUploadID(t, app, nil, "")
	putPart(t, app, id, 1, []byte("only-100"), "1")

	rec := completeUpload(t, app, id)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "size mismatch") {
		t.Fatalf("expected size-mismatch 400, got %d %s", rec.Code, rec.Body.String())
	}
}

// Cross-user PUT must 403. meta.json holds the owner; X-User-ID
// from a different user gets rejected.
func TestUploads_CrossUserPart_403(t *testing.T) {
	_ = newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	app := &App{}

	startUpload(t, app, map[string]any{"filename": "z.bin", "size": 100})
	id := lastUploadID(t, app, nil, "")
	rec := putPart(t, app, id, 1, []byte("hello"), "999")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d %s", rec.Code, rec.Body.String())
	}
}

// Pre-dedup: client passes sha256 that already exists → init
// short-circuits, no upload session created.
func TestUploads_PreDedupShortCircuit(t *testing.T) {
	ctx := newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	existing := mustUpload(t, ctx, "preseed.bin", "/", "the same bytes")
	wantSHA := existing.SHA256

	app := &App{}
	bj, _ := json.Marshal(map[string]any{
		"filename": "redundant.bin",
		"size":     1,
		"sha256":   wantSHA,
	})
	req := httptest.NewRequest("POST", "/uploads?project_id=test-proj", bytes.NewReader(bj))
	req.Header.Set("X-User-ID", "1")
	rec := httptest.NewRecorder()
	app.handleUploadsCollection(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("init: %d %s", rec.Code, rec.Body.String())
	}
	var rsp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &rsp)
	if rsp["was_existing"] != true {
		t.Fatalf("expected was_existing=true, got %+v", rsp)
	}
	if rsp["upload_id"] != nil {
		t.Errorf("expected no upload_id (short-circuit), got %v", rsp["upload_id"])
	}
}

// Sweeper removes session dirs older than uploadIdleTTL.
func TestUploads_SweepStaleRemovesOldDirs(t *testing.T) {
	upDir := t.TempDir()
	ctx := newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", upDir))
	app := &App{}

	startUpload(t, app, map[string]any{"filename": "fresh.bin", "size": 10})
	freshID := lastUploadID(t, app, nil, "")

	// Fake stale session.
	staleID := "01HXXXXXXXXXXXXXXXXXXXXXXX"
	staleDir := filepath.Join(upDir, staleID)
	_ = os.MkdirAll(staleDir, 0755)
	_ = os.WriteFile(filepath.Join(staleDir, "meta.json"), []byte(`{"user_id":1}`), 0644)
	old := time.Now().Add(-2 * uploadIdleTTL)
	_ = os.Chtimes(staleDir, old, old)

	sweepStaleUploads(ctx)

	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Error("sweeper kept stale dir")
	}
	if _, err := os.Stat(uploadSessionDir(ctx, freshID)); err != nil {
		t.Errorf("sweeper removed fresh dir: %v", err)
	}
}

// validUploadID rejects path-traversal attempts.
func TestUploads_ValidID(t *testing.T) {
	for _, bad := range []string{"", "../etc", "a/b", `a\b`, "lower-not-base32", strings.Repeat("A", 100)} {
		if validUploadID(bad) {
			t.Errorf("expected reject %q", bad)
		}
	}
	for _, ok := range []string{"01HABCDEFGHJKMNPQRSTVWXYZ0", "ABCDEFGH"} {
		if !validUploadID(ok) {
			t.Errorf("expected accept %q", ok)
		}
	}
}

// Bad part numbers (0, negative, > maxPartNumber) are rejected at
// the routing layer.
func TestUploads_InvalidPartNumberRejected(t *testing.T) {
	_ = newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	app := &App{}
	startUpload(t, app, map[string]any{"filename": "x.bin", "size": 10})
	id := lastUploadID(t, app, nil, "")

	for _, bad := range []string{"0", "-1", "10001", "abc"} {
		req := httptest.NewRequest("PUT", fmt.Sprintf("/uploads/%s/parts/%s?project_id=test-proj", id, bad),
			bytes.NewReader([]byte("x")))
		req.Header.Set("X-User-ID", "1")
		rec := httptest.NewRecorder()
		app.handleUploadsItem(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("part %q: expected 400, got %d", bad, rec.Code)
		}
	}
}

// helper.
func sha256SumOf(b []byte) []byte {
	h := sha256.New()
	_, _ = io.Copy(h, bytes.NewReader(b))
	return h.Sum(nil)
}
