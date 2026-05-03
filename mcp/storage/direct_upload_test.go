package main

// Tier 1 — direct presigned-upload protocol on the disk path
// (returns 501) and on a stub backend that mimics s3 (presigned
// roundtrip without a real bucket).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── stub backend that pretends to be s3 ───────────────────────────
//
// fakeS3Backend keeps an in-memory map of {key → bytes} and reports
// Kind()=="s3" so the direct-upload code path treats it as remote.
// PresignGet/PresignPut return canned URLs the test asserts on.

type fakeS3Backend struct {
	objects   map[string][]byte
	presigned int32 // count of PresignPut calls — assert clients hit it
}

func newFakeS3() *fakeS3Backend { return &fakeS3Backend{objects: map[string][]byte{}} }

func (f *fakeS3Backend) Kind() string { return "s3" }

func (f *fakeS3Backend) Put(_ context.Context, key, _ string, r io.Reader, size int64) error {
	var rd io.Reader = r
	if size > 0 {
		rd = io.LimitReader(r, size)
	}
	body, err := io.ReadAll(rd)
	if err != nil {
		return err
	}
	f.objects[key] = body
	return nil
}

func (f *fakeS3Backend) Delete(_ context.Context, key string) error {
	delete(f.objects, key)
	return nil
}

func (f *fakeS3Backend) Stat(_ context.Context, key string) (int64, error) {
	b, ok := f.objects[key]
	if !ok {
		return 0, ErrNotFound
	}
	return int64(len(b)), nil
}

func (f *fakeS3Backend) LocalPath(string) (string, bool) { return "", false }

func (f *fakeS3Backend) PresignGet(_ context.Context, key, _, _ string, _ time.Duration) (string, error) {
	return "https://fake-s3.example.com/" + key + "?presigned=get", nil
}

func (f *fakeS3Backend) PresignPut(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	atomic.AddInt32(&f.presigned, 1)
	return "https://fake-s3.example.com/" + key + "?presigned=put", nil
}

// ─── tests ─────────────────────────────────────────────────────────

func TestDirectUpload_DiskReturns501(t *testing.T) {
	// Disk-backed install: /files/init must return 501 so clients
	// know to fall back to POST /files.
	ctx := newTestStorageCtx(t)
	_ = ctx
	globalBackend = newDiskBackend(globalCtx) // explicit disk

	body := strings.NewReader(`{"name":"x.mp4","size_bytes":100,"sha256":"` + repeat64Hex("a") + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/files/init?project_id=p1", body)
	rec := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status=%d want 501; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "POST bytes to /files") {
		t.Errorf("expected fallback hint in body: %s", rec.Body.String())
	}
}

func TestDirectUpload_S3InitReturnsPresignedURL(t *testing.T) {
	ctx := newTestStorageCtx(t)
	_ = ctx
	stub := newFakeS3()
	globalBackend = stub

	body := strings.NewReader(`{
		"name":"clip.mp4",
		"folder":"/incoming/",
		"content_type":"video/mp4",
		"size_bytes":1234,
		"sha256":"` + repeat64Hex("b") + `"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/files/init?project_id=p1", body)
	rec := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["mode"] != "presigned" {
		t.Errorf("mode=%v want presigned", resp["mode"])
	}
	if u, _ := resp["upload_url"].(string); !strings.HasPrefix(u, "https://fake-s3.example.com/bb/") {
		t.Errorf("upload_url=%q (want sha-prefixed key)", u)
	}
	if id, _ := resp["upload_id"].(string); len(id) < 16 {
		t.Errorf("upload_id too short: %q", id)
	}
	if atomic.LoadInt32(&stub.presigned) != 1 {
		t.Errorf("PresignPut should have been called once, got %d", stub.presigned)
	}
}

func TestDirectUpload_InitDedupShortCircuit(t *testing.T) {
	ctx := newTestStorageCtx(t)
	stub := newFakeS3()
	globalBackend = stub

	// Pre-seed an existing files row with a known sha. Init for the
	// same sha must skip presigning entirely and return that row.
	sha := repeat64Hex("c")
	if _, err := ctx.AppDB().Exec(`
		INSERT INTO files (project_id, name, folder, storage_key, content_type,
		                   size_bytes, sha256, visibility)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"p1", "existing.mp4", "/", "old-key.mp4", "video/mp4", 999, sha, "private",
	); err != nil {
		t.Fatal(err)
	}

	body := strings.NewReader(`{"name":"clip.mp4","size_bytes":999,"sha256":"` + sha + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/files/init?project_id=p1", body)
	rec := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp["was_existing"] != true {
		t.Errorf("dedup should set was_existing=true: %+v", resp)
	}
	if resp["mode"] != "deduplicated" {
		t.Errorf("mode=%v want deduplicated", resp["mode"])
	}
	if atomic.LoadInt32(&stub.presigned) != 0 {
		t.Error("dedup must NOT presign")
	}
}

func TestDirectUpload_InitRejectsMissingSHA(t *testing.T) {
	ctx := newTestStorageCtx(t)
	_ = ctx
	globalBackend = newFakeS3()

	body := strings.NewReader(`{"name":"x.mp4","size_bytes":1}`)
	req := httptest.NewRequest(http.MethodPost, "/files/init?project_id=p1", body)
	rec := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDirectUpload_FinalizeRoundtrip(t *testing.T) {
	ctx := newTestStorageCtx(t)
	stub := newFakeS3()
	globalBackend = stub

	// 1. init → returns upload_id + URL
	sha := repeat64Hex("d")
	body := []byte(`{"name":"clip.mp4","size_bytes":5,"sha256":"` + sha + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/files/init?project_id=p1", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("init: %d %s", rec.Code, rec.Body.String())
	}
	var initResp map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&initResp)
	uploadID, _ := initResp["upload_id"].(string)

	// 2. simulate the client PUT to s3: write into the fake bucket.
	// The key is whatever we presigned — re-derive from the URL.
	url, _ := initResp["upload_url"].(string)
	key := strings.TrimPrefix(strings.SplitN(url, "?", 2)[0], "https://fake-s3.example.com/")
	stub.objects[key] = []byte("hello")

	// 3. finalize → file row + dedup row gone.
	req2 := httptest.NewRequest(http.MethodPost,
		"/files/"+uploadID+"/finalize?project_id=p1",
		strings.NewReader(`{}`))
	rec2 := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("finalize: %d %s", rec2.Code, rec2.Body.String())
	}
	var finResp struct {
		File        *File `json:"file"`
		WasExisting bool  `json:"was_existing"`
	}
	if err := json.NewDecoder(rec2.Body).Decode(&finResp); err != nil {
		t.Fatal(err)
	}
	if finResp.File == nil {
		t.Fatal("expected file row")
	}
	if finResp.File.SHA256 != sha {
		t.Errorf("sha=%q want %q", finResp.File.SHA256, sha)
	}
	if finResp.WasExisting {
		t.Error("first upload shouldn't be dedup")
	}

	// pending session is cleaned up.
	var n int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM pending_uploads WHERE upload_id = ?`, uploadID,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("pending row not cleaned: %d", n)
	}
}

func TestDirectUpload_FinalizeDetectsSizeMismatch(t *testing.T) {
	ctx := newTestStorageCtx(t)
	_ = ctx
	stub := newFakeS3()
	globalBackend = stub

	sha := repeat64Hex("e")
	req := httptest.NewRequest(http.MethodPost, "/files/init?project_id=p1",
		strings.NewReader(`{"name":"clip.mp4","size_bytes":10,"sha256":"`+sha+`"}`))
	rec := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("init: %d %s", rec.Code, rec.Body.String())
	}
	var initResp map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&initResp)
	uploadID, _ := initResp["upload_id"].(string)
	url, _ := initResp["upload_url"].(string)
	key := strings.TrimPrefix(strings.SplitN(url, "?", 2)[0], "https://fake-s3.example.com/")

	// Upload only 3 bytes when 10 were declared. Finalize must reject
	// AND clean up the orphan object.
	stub.objects[key] = []byte("xyz")

	req2 := httptest.NewRequest(http.MethodPost,
		"/files/"+uploadID+"/finalize?project_id=p1",
		strings.NewReader(`{}`))
	rec2 := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rec2.Code, rec2.Body.String())
	}
	if _, exists := stub.objects[key]; exists {
		t.Error("orphan object should be cleaned up on size mismatch")
	}
}

func TestDirectUpload_FinalizeMissingObject(t *testing.T) {
	stub := newFakeS3()
	globalBackend = stub
	ctx := newTestStorageCtx(t)
	_ = ctx

	sha := repeat64Hex("f")
	req := httptest.NewRequest(http.MethodPost, "/files/init?project_id=p1",
		strings.NewReader(`{"name":"clip.mp4","size_bytes":10,"sha256":"`+sha+`"}`))
	rec := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec, req)
	var initResp map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&initResp)
	uploadID, _ := initResp["upload_id"].(string)

	// Don't upload anything. Finalize must report bad request, not 500.
	req2 := httptest.NewRequest(http.MethodPost,
		"/files/"+uploadID+"/finalize?project_id=p1",
		strings.NewReader(`{}`))
	rec2 := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestSweepStalePendingUploads_DeletesExpired(t *testing.T) {
	ctx := newTestStorageCtx(t)

	// Insert one expired + one fresh row.
	if _, err := ctx.AppDB().Exec(`
		INSERT INTO pending_uploads
		  (upload_id, project_id, storage_key, name, folder,
		   size_bytes, declared_sha256, expires_at)
		VALUES
		  ('STALE', 'p', 'k', 'x', '/', 1, 'sha', ?),
		  ('FRESH', 'p', 'k2', 'y', '/', 1, 'sha', ?)`,
		time.Now().Add(-time.Hour).Unix(),
		time.Now().Add(time.Hour).Unix(),
	); err != nil {
		t.Fatal(err)
	}

	sweepStalePendingUploads(ctx)

	var fresh, stale int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM pending_uploads WHERE upload_id='FRESH'`).Scan(&fresh)
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM pending_uploads WHERE upload_id='STALE'`).Scan(&stale)
	if fresh != 1 {
		t.Error("FRESH should survive sweep")
	}
	if stale != 0 {
		t.Error("STALE should be reaped")
	}
}

// ─── helpers ────────────────────────────────────────────────────────

func repeat64Hex(c string) string {
	if len(c) != 1 {
		panic("c must be one hex char")
	}
	return strings.Repeat(c, 64)
}

// newTestStorageCtx builds a fresh AppCtx wired to a temp blob dir.
// Mirrors emit_test.go's newRecordedCtx but skips the EmitRecorder
// since the direct-upload tests don't assert on emitted events.
// Each test gets a fresh ctx + a fresh fakeS3 + an AppCtx with an
// empty DB so tests are independent.
func newTestStorageCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	dir := t.TempDir()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("p1"),
		tk.WithEnv("STORAGE_BLOBS_DIR", dir),
	)
	globalCtx = ctx
	// reset globalBackend so the helper doesn't pick up state from a
	// previous test that set it to a fakeS3.
	globalBackend = nil
	return ctx
}

// ensureSilenced keeps go vet from flagging unused imports if a test
// is removed in isolation; harmless at runtime.
var _ = errors.New
