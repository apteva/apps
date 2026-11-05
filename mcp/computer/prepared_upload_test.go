package main

import (
	"crypto/sha256"
	"encoding/hex"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPreparedUploadFreezesSourceAndCleansUp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "source.mp4")
	os.WriteFile(p, []byte("original video"), 0600)
	h := sha256.Sum256([]byte("original video"))
	args := map[string]any{"file_path": p, "filename": "replacement.mp4", "expected_sha256": hex.EncodeToString(h[:]), "require_prepared": true}
	s := &session{backend: "local"}
	meta, err := s.prepareUpload(args, "")
	if err != nil {
		t.Fatal(err)
	}
	if meta["uploaded"] != false || meta["prepared"] != true {
		t.Fatal(meta)
	}
	staged := s.preparedUpload.files[0]
	os.WriteFile(p, []byte("changed"), 0600)
	files, _, cleanup, err := s.takeUpload(args)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(files[0])
	if string(raw) != "original video" {
		t.Fatal("source not frozen")
	}
	cleanup()
	if _, err = os.Stat(staged); err != nil {
		t.Fatal("staged file removed before browser finished reading")
	}
	s.clearPreparedUpload()
	if _, err = os.Stat(staged); !os.IsNotExist(err) {
		t.Fatal("staged file leaked")
	}
	if _, _, _, err = s.takeUpload(args); err == nil {
		t.Fatal("prepared upload replayed")
	}
	if raw, _ = os.ReadFile(p); string(raw) != "changed" {
		t.Fatal("caller file removed")
	}
}

func TestPreparedUploadRejectsMismatchExpiryAndUnsupportedBackend(t *testing.T) {
	p := filepath.Join(t.TempDir(), "id.jpeg")
	os.WriteFile(p, []byte("test id fixture"), 0600)
	args := map[string]any{"file_path": p, "filename": "id.jpeg", "require_prepared": true}
	s := &session{backend: "local"}
	defer s.clearPreparedUpload()
	wrong := map[string]any{"file_path": p, "expected_sha256": strings.Repeat("0", 64)}
	if _, err := s.prepareUpload(wrong, ""); err == nil {
		t.Fatal("hash mismatch accepted")
	}
	if _, err := s.prepareUpload(args, ""); err != nil {
		t.Fatal(err)
	}
	other := map[string]any{"file_path": p, "filename": "other.jpeg", "require_prepared": true}
	if _, _, _, err := s.takeUpload(other); err == nil {
		t.Fatal("different source accepted")
	}
	s.preparedUpload.timer.Stop()
	s.preparedUpload.timer = time.AfterFunc(time.Millisecond, s.preparedUpload.cleanup)
	time.Sleep(10 * time.Millisecond)
	if _, _, _, err := s.takeUpload(args); err == nil {
		t.Fatal("expired preparation accepted")
	}
	if _, err := (&session{backend: "steel"}).prepareUpload(args, ""); err == nil {
		t.Fatal("unsupported backend accepted")
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestPreparedUploadProviderLimitAndLargeLocalStreaming(t *testing.T) {
	size := int64(maxUploadBytes + 1)
	if os.Getenv("RUN_LARGE_UPLOAD_TESTS") == "1" {
		size = 1133393792
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmtInt(size))
		io.CopyN(w, zeros{}, size)
	}))
	defer server.Close()
	args := map[string]any{"source_url": server.URL + "/fixture.mp4", "filename": "fixture.mp4", "require_prepared": true}
	cloud := &session{backend: "browserbase"}
	if _, err := cloud.prepareUpload(args, ""); err == nil {
		t.Fatal("cloud cap bypassed")
	}
	if cloud.preparedUpload != nil {
		t.Fatal("failed cloud preparation retained file")
	}
	local := &session{backend: "local"}
	defer local.clearPreparedUpload()
	meta, err := local.prepareUpload(args, "")
	if err != nil {
		t.Fatal(err)
	}
	if meta["size_bytes"] != size {
		t.Fatal(meta)
	}
	files, _, cleanup, err := local.takeUpload(args)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if info, err := os.Stat(files[0]); err != nil || info.Size() != size {
		t.Fatal("large source incomplete", err)
	}
}

func fmtInt(n int64) string { return strconv.FormatInt(n, 10) }

// Exercises the production Computer sidecar and native CDP input selection with
// a 1.13 GB fixture. No website login, real video or identity document is used.
func TestPreparedLargeUploadNativeBrowser(t *testing.T) {
	if os.Getenv("RUN_LARGE_UPLOAD_BROWSER_TESTS") != "1" {
		t.Skip("opt-in isolated native browser test")
	}
	path := filepath.Join(t.TempDir(), "large_fixture.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(1133393792); err != nil {
		t.Fatal(err)
	}
	f.Close()
	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	html := `<html><body><label>Upload Files<input id="file" type="file"></label><p id="receipt">waiting</p><script>document.querySelector('input').onchange=async function(){let f=this.files[0];let r=await fetch('/upload',{method:'POST',body:f});document.getElementById('receipt').textContent=await r.text()}</script></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			io.WriteString(w, html)
			return
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(r.Body, 1133393793))
		if e != nil || n != 1133393792 {
			http.Error(w, "incomplete", 400)
			return
		}
		io.WriteString(w, "received-"+strconv.FormatInt(n, 10)+"-"+hex.EncodeToString(h.Sum(nil)))
	}))
	defer server.Close()
	open := sc.MCP("browser_session", map[string]any{"action": "open", "backend": "local", "url": server.URL})
	id, _ := open["session_id"].(string)
	if id == "" {
		t.Fatal(open)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": id})
	args := map[string]any{"session_id": id, "action": "prepare_upload", "file_path": path, "filename": "large_fixture.mp4", "mime_type": "video/mp4"}
	ready := sc.MCP("computer_use", args)
	if ready["prepared"] != true || ready["uploaded"] != false {
		t.Fatal(ready)
	}
	args["action"] = "upload_file"
	args["require_prepared"] = true
	args["selector"] = "input#file"
	out := sc.MCP("computer_use", args)
	if out["uploaded"] != true {
		t.Fatal(out)
	}
	verified := sc.MCP("computer_use", map[string]any{"session_id": id, "action": "wait_for", "conditions": []any{map[string]any{"type": "text_present", "value": "received-1133393792-" + stringValue(ready["sha256"])}}, "timeout_ms": 10000})
	if verified["matched"] != true {
		t.Fatal(verified)
	}
}

func TestPreparedUploadUsesPrivateCacheAndReapsOnlyExpiredOwnFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "staging")
	t.Setenv("APTEVA_UPLOAD_STAGING_DIR", root)
	if _, err := preparedUploadRoot(""); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("cache not private", err)
	}
	old := filepath.Join(root, "prepared-old")
	other := filepath.Join(root, "keep-other")
	fresh := filepath.Join(root, "prepared-live")
	for _, p := range []string{old, other, fresh} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	ago := time.Now().Add(-2 * time.Hour)
	os.Chtimes(old, ago, ago)
	os.Chtimes(other, ago, ago)
	if _, err := preparedUploadRoot(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expired staging not reaped")
	}
	for _, p := range []string{other, fresh} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal("unrelated or live files removed")
		}
	}
	t.Setenv("APTEVA_UPLOAD_STAGING_DIR", "relative")
	if _, err := preparedUploadRoot(""); err == nil {
		t.Fatal("relative staging accepted")
	}
}

func TestPreparedUploadUsesInstallDirectoryWithoutUserCache(t *testing.T) {
	t.Setenv("APTEVA_UPLOAD_STAGING_DIR", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	dataDir := t.TempDir()
	t.Setenv("APTEVA_DATA_DIR", dataDir)
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	fixture := []byte("upload staging regression fixture")
	hash := sha256.Sum256(fixture)
	expected := hex.EncodeToString(hash[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	sess := &session{backend: "local"}
	defer sess.clearPreparedUpload()
	app := &App{reg: &registry{m: map[string]*session{"fixture": sess}}}
	result, err := app.toolComputerUse(ctx, map[string]any{
		"action":          "prepare_upload",
		"session_id":      "fixture",
		"source_url":      server.URL + "/fixture.txt",
		"expected_sha256": expected,
	})
	if err != nil {
		t.Fatal(err)
	}
	meta := result.(map[string]any)
	if meta["prepared"] != true || meta["uploaded"] != false || meta["sha256"] != expected {
		t.Fatalf("unexpected prepare_upload metadata: %#v", meta)
	}
	root := filepath.Join(dataDir, "computer-uploads")
	if got := filepath.Dir(filepath.Dir(sess.preparedUpload.files[0])); got != root {
		t.Fatalf("staging root = %q, want %q", got, root)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("staging root must be private, got %s", info.Mode())
	}
}
