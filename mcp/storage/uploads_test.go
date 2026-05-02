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
	"strconv"
	"strings"
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

func patchChunk(t *testing.T, app *App, id string, offset int64, chunk []byte, userID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/uploads/"+id+"?project_id=test-proj",
		bytes.NewReader(chunk))
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
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

// Whole-file resumable upload: 3 chunks → complete → file row exists
// with the right sha256.
func TestUploads_HappyPath(t *testing.T) {
	ctx := newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	app := &App{}

	body := bytes.Repeat([]byte("apteva-storage-resumable\n"), 4096) // ~100 KB
	wantSHA := hex.EncodeToString(sha256SumOf(body))

	init := startUpload(t, app, map[string]any{
		"filename":     "demo.bin",
		"size":         len(body),
		"content_type": "application/octet-stream",
		"folder":       "/",
	})
	id := init["upload_id"].(string)
	if id == "" {
		t.Fatal("no upload_id")
	}
	// Send three chunks.
	chunkSize := len(body) / 3
	off := int64(0)
	for off < int64(len(body)) {
		end := off + int64(chunkSize)
		if end > int64(len(body)) {
			end = int64(len(body))
		}
		rec := patchChunk(t, app, id, off, body[off:end], "1")
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH @%d: %d %s", off, rec.Code, rec.Body.String())
		}
		var rsp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &rsp)
		off = int64(rsp["offset"].(float64))
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
	// Session dir must be cleaned up.
	if _, err := os.Stat(uploadSessionDir(ctx, id)); !os.IsNotExist(err) {
		t.Error("session dir lingered after complete")
	}
}

// Offset mismatch must 409 with the actual offset so the client can
// reconcile after a network drop.
func TestUploads_OffsetMismatch_409(t *testing.T) {
	_ = newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	app := &App{}

	init := startUpload(t, app, map[string]any{
		"filename": "x.bin", "size": 1024,
	})
	id := init["upload_id"].(string)
	// Write 100 bytes at offset 0.
	first := bytes.Repeat([]byte("a"), 100)
	if rec := patchChunk(t, app, id, 0, first, "1"); rec.Code != http.StatusOK {
		t.Fatalf("first chunk: %d %s", rec.Code, rec.Body.String())
	}
	// Now try to resume from a stale offset (100 is correct; 50 is a
	// classic "client lost track" scenario).
	rec := patchChunk(t, app, id, 50, []byte("x"), "1")
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d %s", rec.Code, rec.Body.String())
	}
	var rsp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &rsp)
	if int64(rsp["offset"].(float64)) != 100 {
		t.Errorf("expected actual offset 100, got %v", rsp["offset"])
	}
}

// Restart safety: write some bytes, simulate a sidecar restart by
// constructing a fresh App, finish the upload. The hash.bin sidecar
// must let us complete without re-hashing the prefix.
func TestUploads_HashStateSurvivesRestart(t *testing.T) {
	uploadsDir := t.TempDir()
	body := bytes.Repeat([]byte{0x42}, 8192)
	wantSHA := hex.EncodeToString(sha256SumOf(body))

	// Phase 1.
	_ = newTestCtx(t,
		tk.WithEnv("STORAGE_UPLOADS_DIR", uploadsDir),
		tk.WithEnv("STORAGE_BLOBS_DIR", t.TempDir()),
	)
	app := &App{}
	init := startUpload(t, app, map[string]any{
		"filename": "y.bin", "size": len(body),
	})
	id := init["upload_id"].(string)
	half := len(body) / 2
	if rec := patchChunk(t, app, id, 0, body[:half], "1"); rec.Code != http.StatusOK {
		t.Fatalf("first half: %d", rec.Code)
	}

	// Phase 2: pretend the sidecar restarted — new ctx pointing at
	// the same on-disk uploads dir. (We share the uploads dir by
	// env; blobs dir can be different — a finalize is going to land
	// the bytes there fresh.)
	blobs2 := t.TempDir()
	_ = newTestCtx(t,
		tk.WithEnv("STORAGE_UPLOADS_DIR", uploadsDir),
		tk.WithEnv("STORAGE_BLOBS_DIR", blobs2),
	)
	app2 := &App{}
	if rec := patchChunk(t, app2, id, int64(half), body[half:], "1"); rec.Code != http.StatusOK {
		t.Fatalf("second half: %d %s", rec.Code, rec.Body.String())
	}
	rec := completeUpload(t, app2, id)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	var rsp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &rsp)
	got := rsp["file"].(map[string]any)["sha256"].(string)
	if got != wantSHA {
		t.Errorf("sha drift after restart: got %s want %s", got, wantSHA)
	}
}

// Cross-user PATCH must 403. meta.json holds the owner; X-User-ID
// from a different user gets rejected.
func TestUploads_CrossUserPatch_403(t *testing.T) {
	_ = newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	app := &App{}

	init := startUpload(t, app, map[string]any{"filename": "z.bin", "size": 100})
	id := init["upload_id"].(string)
	// Different X-User-ID.
	rec := patchChunk(t, app, id, 0, []byte("hello"), "999")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d %s", rec.Code, rec.Body.String())
	}
}

// Pre-dedup: client passes sha256 of bytes that already exist in the
// project. Init must short-circuit and return the existing row,
// skipping the entire upload protocol.
func TestUploads_PreDedupShortCircuit(t *testing.T) {
	ctx := newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))

	// Pre-seed a file via the existing single-shot path.
	existing := mustUpload(t, ctx, "preseed.bin", "/", "the same bytes")
	wantSHA := existing.SHA256

	app := &App{}
	bj, _ := json.Marshal(map[string]any{
		"filename": "redundant.bin",
		"size":     1, // bogus — won't matter, server short-circuits
		"sha256":   wantSHA,
	})
	req := httptest.NewRequest("POST", "/uploads?project_id=test-proj",
		bytes.NewReader(bj))
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

// Sweeper must remove session dirs older than uploadIdleTTL and
// leave fresh ones alone.
func TestUploads_SweepStaleRemovesOldDirs(t *testing.T) {
	upDir := t.TempDir()
	ctx := newTestCtx(t, tk.WithEnv("STORAGE_UPLOADS_DIR", upDir))
	app := &App{}

	// Fresh session (untouched).
	init := startUpload(t, app, map[string]any{"filename": "fresh.bin", "size": 10})
	freshID := init["upload_id"].(string)

	// Stale: hand-craft a session dir + meta and backdate its mtime.
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

// validUploadID must reject path-traversal attempts so the routing
// can't be tricked into reading outside <data>/uploads/.
func TestUploads_ValidID(t *testing.T) {
	for _, bad := range []string{"", "../etc", "a/b", `a\b`, "lower-case-id-not-base32", strings.Repeat("A", 100)} {
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

// helper.
func sha256SumOf(b []byte) []byte {
	h := sha256.New()
	_, _ = io.Copy(h, bytes.NewReader(b))
	return h.Sum(nil)
}

// Silence unused-import linter when individual tests are built in
// isolation — fmt.Sprint is genuinely used via t.Fatalf paths above
// in error messages, but the linter doesn't always agree.
var _ = fmt.Sprint
