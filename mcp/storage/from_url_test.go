package main

// Tests for files_from_url. Spins up a local httptest.Server and
// makes assertions against the inserted row, the saved bytes, and
// the User-Agent we send (since CDNs commonly block Go's default).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// newCtxWithRecorder sets up a fresh AppCtx + recorder + temp blob
// dir so the test exercises the same surface a real install would.
func newFromURLCtx(t *testing.T) (*sdk.AppCtx, *tk.EmitRecorder, string) {
	t.Helper()
	dir := t.TempDir()
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("STORAGE_BLOBS_DIR", dir),
		tk.WithEmitter(rec),
	)
	globalCtx = ctx
	return ctx, rec, dir
}

func TestFromURL_FetchesAndStores(t *testing.T) {
	bytesPayload := []byte("hello from upstream")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(bytesPayload)
	}))
	defer srv.Close()

	ctx, rec, blobDir := newFromURLCtx(t)
	app := &App{}

	out, err := app.toolFromURL(ctx, map[string]any{
		"url":    srv.URL + "/foo.txt",
		"folder": "/inbox/",
	})
	if err != nil {
		t.Fatalf("toolFromURL: %v", err)
	}
	res := out.(map[string]any)
	wantHash := sha256.Sum256(bytesPayload)
	if res["sha256"] != hex.EncodeToString(wantHash[:]) {
		t.Errorf("sha256 = %v, want %s", res["sha256"], hex.EncodeToString(wantHash[:]))
	}
	if res["was_existing"] != false {
		t.Errorf("was_existing = %v on first fetch, want false", res["was_existing"])
	}
	id := res["id"].(int64)

	// Bytes actually landed on disk somewhere under the blob dir.
	var found bool
	_ = filepath.Walk(blobDir, func(p string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && info.Size() == int64(len(bytesPayload)) {
			b, _ := os.ReadFile(p)
			if string(b) == string(bytesPayload) {
				found = true
			}
		}
		return nil
	})
	if !found {
		t.Error("uploaded bytes not found on disk under blob dir")
	}

	// Emit fired.
	if got := rec.EventsByTopic("file.added"); len(got) != 1 {
		t.Errorf("expected 1 file.added emit, got %d", len(got))
	}

	// Metadata: name defaults to URL basename + folder propagated.
	got, _ := app.toolGet(ctx, map[string]any{"id": id})
	f := got.(map[string]any)["file"].(*File)
	if f.Name != "foo.txt" {
		t.Errorf("name = %q, want foo.txt", f.Name)
	}
	if f.Folder != "/inbox/" {
		t.Errorf("folder = %q, want /inbox/", f.Folder)
	}
	if f.ContentType != "text/plain" {
		t.Errorf("content_type = %q, want text/plain", f.ContentType)
	}
}

func TestFromURL_RespectsExplicitName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	ctx, _, _ := newFromURLCtx(t)
	app := &App{}
	out, err := app.toolFromURL(ctx, map[string]any{
		"url":  srv.URL + "/some/long/path/file.bin",
		"name": "renamed.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["id"].(int64)
	got, _ := app.toolGet(ctx, map[string]any{"id": id})
	f := got.(map[string]any)["file"].(*File)
	if f.Name != "renamed.txt" {
		t.Errorf("name = %q, want renamed.txt", f.Name)
	}
}

func TestFromURL_SendsBrowserUserAgent(t *testing.T) {
	// Many CDNs reject Go's default UA. Make sure we send a Mozilla-
	// shaped one so vecteezy / cloudfront / cloudflare-bot-scored
	// hosts don't 403 us.
	var capturedUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	ctx, _, _ := newFromURLCtx(t)
	app := &App{}
	if _, err := app.toolFromURL(ctx, map[string]any{"url": srv.URL + "/x.txt"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capturedUA, "Mozilla/5.0") {
		t.Errorf("User-Agent = %q, expected to contain Mozilla/5.0 (so CDNs don't 403 us)", capturedUA)
	}
	if strings.Contains(capturedUA, "Go-http-client") {
		t.Errorf("User-Agent leaked Go default (%q) — would be blocked by CDNs", capturedUA)
	}
}

func TestFromURL_PropagatesUpstream4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	ctx, _, _ := newFromURLCtx(t)
	app := &App{}
	_, err := app.toolFromURL(ctx, map[string]any{"url": srv.URL})
	if err == nil {
		t.Fatal("expected error on 403, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error doesn't mention status: %v", err)
	}
}

func TestFromURL_DedupesContent(t *testing.T) {
	bytesPayload := []byte("dedupe me")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytesPayload)
	}))
	defer srv.Close()

	ctx, _, _ := newFromURLCtx(t)
	app := &App{}
	args := map[string]any{"url": srv.URL + "/dup.txt"}
	out1, err := app.toolFromURL(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := app.toolFromURL(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if out1.(map[string]any)["was_existing"] != false {
		t.Errorf("first fetch should not be was_existing")
	}
	if out2.(map[string]any)["was_existing"] != true {
		t.Errorf("second identical fetch should report was_existing=true (got %v)", out2.(map[string]any)["was_existing"])
	}
}

func TestFromURL_RequiresURL(t *testing.T) {
	ctx, _, _ := newFromURLCtx(t)
	app := &App{}
	_, err := app.toolFromURL(ctx, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "url") {
		t.Errorf("expected url-required error, got %v", err)
	}
}

// ─── HTTP wrapper (POST /files/from-url) ────────────────────────────

func TestHTTPFromURL_FetchesAndStores(t *testing.T) {
	payload := []byte("server-side fetched bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	newFromURLCtx(t)
	// Force the global-scope code path so the test proves _project_id
	// gets threaded from the query string into toolFromURL's args
	// (mirrors http_upload_test.go's coverage).
	t.Setenv("APTEVA_PROJECT_ID", "")
	app := &App{}

	body, _ := json.Marshal(map[string]any{
		"url":    srv.URL + "/clip.txt",
		"folder": "/.imports/",
	})
	req := httptest.NewRequest(http.MethodPost,
		"/files/from-url?project_id=p-from-query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.httpFromURL(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["id"]; !ok {
		t.Errorf("expected response.id, got %v", resp)
	}
	if resp["was_existing"] != false {
		t.Errorf("was_existing = %v on first fetch, want false", resp["was_existing"])
	}
	if resp["sha256"] == nil || resp["sha256"] == "" {
		t.Errorf("expected response.sha256, got %v", resp)
	}
}

func TestHTTPFromURL_RejectsNonPOST(t *testing.T) {
	newFromURLCtx(t)
	app := &App{}
	req := httptest.NewRequest(http.MethodGet, "/files/from-url?project_id=p", nil)
	rec := httptest.NewRecorder()
	app.httpFromURL(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on GET, got %d", rec.Code)
	}
}

func TestHTTPFromURL_RejectsInvalidJSON(t *testing.T) {
	newFromURLCtx(t)
	app := &App{}
	req := httptest.NewRequest(http.MethodPost,
		"/files/from-url?project_id=p", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	app.httpFromURL(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on bad json, got %d", rec.Code)
	}
}

// quiet io import
var _ = io.Discard
