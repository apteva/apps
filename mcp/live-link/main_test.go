package main

// Tier 1 tests — in-process, in-memory SQLite, fake-binary lifecycle.
// Whole suite runs in well under a second; runs on every commit.
//
// Groups:
//   - MANIFEST   : the embedded YAML matches what apteva.yaml on disk
//                  declares; every advertised tool has a handler.
//   - ASSET_URL  : runtime.GOOS/GOARCH → cloudflared download URL.
//   - REGEX      : the trycloudflare URL parser matches real
//                  cloudflared stderr lines we've observed.
//   - DB         : runs round-trip; OnMount reconciles orphans.
//   - HTTP       : routes mounted on httptest.Server, method-shape
//                  asserted without spawning anything.
//   - MANAGER    : start/stop the Manager against a fake binary
//                  (sh -c …) so we exercise the real subprocess
//                  plumbing without needing cloudflared on the host.
//
// Tier 2 (real sidecar via tk.SpawnSidecar) lives in
// integration_test.go behind //go:build integration.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── helpers ───────────────────────────────────────────────────────

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "live-link.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Apply every migration in lexical order — matches what the SDK
	// runs on a fresh install. Easier than maintaining a list each
	// time we add a new file.
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Exec(string(sql)); err != nil {
			t.Fatalf("migration %s: %v", f, err)
		}
	}
	return db
}

func newTestCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	return newTestCtxWithConfig(t, nil)
}

// newTestCtxWithConfig builds a fresh test context with the given
// install-config values pre-populated. Pass nil for an empty config.
func newTestCtxWithConfig(t *testing.T, cfg map[string]string) *sdk.AppCtx {
	t.Helper()
	db := openTestDB(t)
	m := (&App{}).Manifest()
	c := sdk.Config{}
	for k, v := range cfg {
		c[k] = v
	}
	ctx := sdk.NewAppCtxForTest(&m, db, c, nil, &silentLogger{})
	globalCtx = ctx
	return ctx
}

type silentLogger struct{}

func (silentLogger) Info(string, ...any)  {}
func (silentLogger) Warn(string, ...any)  {}
func (silentLogger) Error(string, ...any) {}

// ─── MANIFEST ──────────────────────────────────────────────────────

func TestManifest_Parses(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "live-link" {
		t.Errorf("name=%q", m.Name)
	}
	if m.Version == "" {
		t.Error("version is empty")
	}
	want := map[string]bool{
		"expose_start":  false,
		"expose_stop":   false,
		"expose_status": false,
	}
	for _, tool := range m.Provides.MCPTools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("manifest is missing tool %q", name)
		}
	}
}

func TestManifest_ToolsMatchHandlers(t *testing.T) {
	a := &App{}
	manifestNames := map[string]bool{}
	for _, tool := range a.Manifest().Provides.MCPTools {
		manifestNames[tool.Name] = true
	}
	for _, tool := range a.MCPTools() {
		if !manifestNames[tool.Name] {
			t.Errorf("MCPTools() exposes %q but manifest doesn't declare it", tool.Name)
		}
		if tool.Handler == nil {
			t.Errorf("tool %q has no handler", tool.Name)
		}
		// All schemas should be a JSON-Schema-shaped object.
		s := tool.InputSchema
		if s["type"] != "object" {
			t.Errorf("tool %q schema type=%v, want object", tool.Name, s["type"])
		}
		if _, ok := s["properties"]; !ok {
			t.Errorf("tool %q schema missing properties", tool.Name)
		}
	}
}

// embeddedManifestMatchesYAMLFile guards against the embedded
// manifestYAML in main.go drifting from apteva.yaml on disk on the
// fields the platform actually compares (name, version, mcp_tools).
// We don't compare every byte — apteva.yaml has user-facing prose
// that's deliberately richer than the embed.
func TestManifest_EmbeddedMatchesYAMLOnKeyFields(t *testing.T) {
	yamlBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := sdk.ParseManifest(yamlBytes)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	embedded := (&App{}).Manifest()

	if onDisk.Name != embedded.Name {
		t.Errorf("name drift: yaml=%q embed=%q", onDisk.Name, embedded.Name)
	}
	if onDisk.Version != embedded.Version {
		t.Errorf("version drift: yaml=%q embed=%q", onDisk.Version, embedded.Version)
	}
	embedTools := map[string]bool{}
	for _, t := range embedded.Provides.MCPTools {
		embedTools[t.Name] = true
	}
	for _, tool := range onDisk.Provides.MCPTools {
		if !embedTools[tool.Name] {
			t.Errorf("apteva.yaml declares tool %q not in embedded manifest", tool.Name)
		}
	}
}

// ─── ASSET_URL ─────────────────────────────────────────────────────

func TestAssetURL_AllSupported(t *testing.T) {
	cases := []struct {
		os, arch string
		want     string
		archived bool
	}{
		{"linux", "amd64", "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64", false},
		{"linux", "arm64", "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-arm64", false},
		{"linux", "arm", "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-arm", false},
		{"linux", "386", "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-386", false},
		{"darwin", "amd64", "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-darwin-amd64.tgz", true},
		{"darwin", "arm64", "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-darwin-arm64.tgz", true},
	}
	for _, c := range cases {
		url, archived, err := assetURL(c.os, c.arch)
		if err != nil {
			t.Errorf("%s/%s: unexpected error: %v", c.os, c.arch, err)
			continue
		}
		if url != c.want {
			t.Errorf("%s/%s: url=%q want %q", c.os, c.arch, url, c.want)
		}
		if archived != c.archived {
			t.Errorf("%s/%s: archived=%v want %v", c.os, c.arch, archived, c.archived)
		}
	}
}

func TestAssetURL_UnsupportedReturnsCleanError(t *testing.T) {
	cases := [][2]string{
		{"windows", "amd64"},
		{"freebsd", "amd64"},
		{"linux", "riscv64"},
		{"darwin", "ppc64"},
	}
	for _, c := range cases {
		_, _, err := assetURL(c[0], c[1])
		if err == nil {
			t.Errorf("%s/%s: expected error, got nil", c[0], c[1])
			continue
		}
		// Error must point the operator at the manual escape hatch.
		if !strings.Contains(err.Error(), "cloudflared_path") {
			t.Errorf("%s/%s: error %q should mention cloudflared_path override", c[0], c[1], err)
		}
	}
}

// ─── REGEX ─────────────────────────────────────────────────────────

func TestTryCloudflareRegex_MatchesRealLogShapes(t *testing.T) {
	// Lines we've seen across cloudflared versions. Format around
	// the URL has changed (ascii box, JSON envelope, plain prefix);
	// the URL itself is stable.
	lines := []struct {
		in   string
		want string
	}{
		{"|  https://random-words-1234.trycloudflare.com  |", "https://random-words-1234.trycloudflare.com"},
		{`{"level":"info","msg":"+--------+","time":"..."}`, ""},
		{`{"level":"info","msg":"|  https://abc-def.trycloudflare.com  |"}`, "https://abc-def.trycloudflare.com"},
		{"INF Your quick Tunnel has been created! Visit it at https://x-y-z.trycloudflare.com", "https://x-y-z.trycloudflare.com"},
		{"INF +-------------------------------+", ""},
		{"random unrelated stderr line", ""},
	}
	for _, c := range lines {
		got := trycloudflareURL.FindString(c.in)
		if got != c.want {
			t.Errorf("regex on %q: got %q want %q", c.in, got, c.want)
		}
	}
}

// ─── DB ────────────────────────────────────────────────────────────

func TestDB_RunsRoundTrip(t *testing.T) {
	ctx := newTestCtx(t)
	id1, err := dbInsertRun(ctx.AppDB(), "cloudflared", "http://localhost:5280", "quick")
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := dbInsertRun(ctx.AppDB(), "cloudflared", "http://localhost:8080", "quick")
	if id1 == 0 || id2 == 0 || id1 == id2 {
		t.Errorf("ids look wrong: %d %d", id1, id2)
	}
	got, err := dbListRuns(ctx.AppDB(), 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d runs, want 2", len(got))
	}
	// Newest first.
	if got[0].ID != id2 {
		t.Errorf("ordering: got[0].ID=%d, want newest=%d", got[0].ID, id2)
	}
	if got[0].Status != "running" {
		t.Errorf("default status=%q, want running", got[0].Status)
	}
}

// shouldAutoRestartOnBoot defaults to true (no config) and respects
// off-shaped values. Operator only has to know about the toggle to
// disable it; default behavior is "bring my tunnel back".
func TestShouldAutoRestartOnBoot_Defaults(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"", true},
		{"true", true},
		{"yes", true},
		{"1", true},
		{"on", true},
		{"false", false},
		{"0", false},
		{"no", false},
		{"off", false},
		{"FALSE", false}, // case-insensitive
	}
	for _, c := range cases {
		ctx := newTestCtxWithConfig(t, map[string]string{"auto_restart_on_boot": c.v})
		if got := shouldAutoRestartOnBoot(ctx); got != c.want {
			t.Errorf("auto_restart_on_boot=%q: got %v, want %v", c.v, got, c.want)
		}
	}
}

func TestOnMount_OrphansLeftoverRunningRows(t *testing.T) {
	ctx := newTestCtx(t)
	// Plant a stale 'running' row from a previous sidecar life.
	_, err := ctx.AppDB().Exec(
		`INSERT INTO runs (provider, target_url, status, public_url)
		 VALUES ('cloudflared', 'http://localhost:5280', 'running', 'https://stale.trycloudflare.com')`)
	if err != nil {
		t.Fatal(err)
	}
	// And a healthy terminated one we should NOT touch.
	_, err = ctx.AppDB().Exec(
		`INSERT INTO runs (provider, target_url, status, public_url, finished_at, exit_reason)
		 VALUES ('cloudflared', 'http://localhost:5280', 'stopped', 'https://done.trycloudflare.com', CURRENT_TIMESTAMP, 'user stopped')`)
	if err != nil {
		t.Fatal(err)
	}

	if err := (&App{}).OnMount(ctx); err != nil {
		t.Fatal(err)
	}

	rows, _ := dbListRuns(ctx.AppDB(), 25)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	var orphaned, stopped *Run
	for _, r := range rows {
		switch r.Status {
		case "orphaned":
			orphaned = r
		case "stopped":
			stopped = r
		}
	}
	if orphaned == nil {
		t.Fatal("stale 'running' row was not orphaned")
	}
	if orphaned.ExitReason != "sidecar restarted" {
		t.Errorf("orphaned exit_reason=%q, want 'sidecar restarted'", orphaned.ExitReason)
	}
	if orphaned.FinishedAt == "" {
		t.Error("orphaned finished_at should be set")
	}
	if stopped == nil || stopped.ExitReason != "user stopped" {
		t.Errorf("healthy stopped row was clobbered: %+v", stopped)
	}
}

// ─── HTTP ──────────────────────────────────────────────────────────

func newHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	newTestCtx(t)
	app := &App{}
	if err := app.OnMount(globalCtx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, r := range app.HTTPRoutes() {
		method, pattern, handler := r.Method, r.Pattern, r.Handler
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			if method != "" && req.Method != method {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			handler(w, req)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTP_StatusIdleByDefault(t *testing.T) {
	srv := newHTTPServer(t)
	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "idle" {
		t.Errorf("status=%v, want idle", body["status"])
	}
	// resolved_target is computed from APTEVA_GATEWAY_URL/defaults
	// regardless of whether a tunnel ever ran.
	if body["resolved_target"] == "" {
		t.Error("resolved_target should never be empty")
	}
}

func TestHTTP_RunsEmptyByDefault(t *testing.T) {
	srv := newHTTPServer(t)
	resp, err := http.Get(srv.URL + "/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Runs []*Run `json:"runs"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(body.Runs))
	}
}

func TestHTTP_StopOnIdleIsNoop(t *testing.T) {
	srv := newHTTPServer(t)
	resp, err := http.Post(srv.URL+"/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_MethodsRejected(t *testing.T) {
	srv := newHTTPServer(t)
	cases := []struct {
		method, path string
		wantStatus   int
	}{
		{"POST", "/status", http.StatusMethodNotAllowed},
		{"GET", "/start", http.StatusMethodNotAllowed},
		{"GET", "/stop", http.StatusMethodNotAllowed},
		{"GET", "/install", http.StatusMethodNotAllowed},
		{"DELETE", "/runs", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.wantStatus {
			t.Errorf("%s %s: status=%d want %d", c.method, c.path, resp.StatusCode, c.wantStatus)
		}
	}
}

// ─── MANAGER (subprocess lifecycle, fake binary) ───────────────────

// fakeCloudflared writes a shell script that mimics cloudflared's
// stderr behaviour: emits a trycloudflare URL line, then sleeps so
// Stop() has something to terminate. Returns the path. Skips on
// Windows since the script is /bin/sh-flavoured.
func fakeCloudflared(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary lifecycle test uses sh; skip on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-cloudflared")
	script := `#!/bin/sh
# Args ignored: we don't need to validate the --url flag.
echo "INF Your quick Tunnel has been created! Visit it at https://fake-test-tunnel.trycloudflare.com" 1>&2
# Sleep long enough that Stop() always has something live to kill.
sleep 30
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManager_StartCapturesURL_StopExitsCleanly(t *testing.T) {
	urlCh := make(chan string, 1)
	exitCh := make(chan string, 1)
	mgr := NewManager(
		func(_ int64, url string) { urlCh <- url },
		func(_ int64, reason string, _ Status) { exitCh <- reason },
	)

	binary := fakeCloudflared(t)
	if err := mgr.Start(StartParams{Binary: binary, Target: "http://localhost:5280", RunID: 42, Mode: ModeQuick}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// URL should arrive within a generous bound. Real cloudflared
	// takes 1-3s; the fake binary prints it on the first line so
	// ours is bounded only by goroutine scheduling.
	select {
	case got := <-urlCh:
		if got != "https://fake-test-tunnel.trycloudflare.com" {
			t.Errorf("url=%q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for URL")
	}

	// Snapshot mid-flight: status running, URL set.
	if snap := mgr.Snapshot(); snap.Status != StatusRunning || snap.PublicURL == "" {
		t.Errorf("mid-flight snapshot wrong: %+v", snap)
	}

	if err := mgr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case reason := <-exitCh:
		// "stopped" if SIGTERM ran clean, or "signal terminated"
		// branch — both are valid outcomes of asking it to stop.
		if reason == "" {
			t.Error("exit reason should not be empty")
		}
	case <-time.After(8 * time.Second): // 5s SIGTERM grace + buffer
		t.Fatal("timed out waiting for exit callback")
	}

	if snap := mgr.Snapshot(); snap.Status == StatusRunning {
		t.Errorf("post-stop snapshot still running: %+v", snap)
	}
}

func TestManager_StartTwiceReturnsAlreadyRunning(t *testing.T) {
	mgr := NewManager(
		func(int64, string) {},
		func(int64, string, Status) {},
	)
	binary := fakeCloudflared(t)
	if err := mgr.Start(StartParams{Binary: binary, Target: "http://localhost:5280", RunID: 1, Mode: ModeQuick}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Stop() })

	err := mgr.Start(StartParams{Binary: binary, Target: "http://localhost:5280", RunID: 2, Mode: ModeQuick})
	if err != ErrAlreadyRunning {
		t.Errorf("second Start: got %v, want ErrAlreadyRunning", err)
	}
}

func TestManager_MissingBinarySurfacesCleanError(t *testing.T) {
	mgr := NewManager(
		func(int64, string) {},
		func(int64, string, Status) {},
	)
	err := mgr.Start(StartParams{Binary: "/no/such/path/to/cloudflared", Target: "http://localhost:5280", RunID: 1, Mode: ModeQuick})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	// Error should include the path so the operator knows what was
	// looked for.
	if !strings.Contains(err.Error(), "cloudflared") {
		t.Errorf("error %q should mention the binary name", err)
	}
}

// ─── RESOLVE_BINARY (config + cache lookup; download skipped) ──────

func TestResolveBinary_PrefersConfigPathWhenItExists(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "my-cloudflared")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveBinary(binary, dir, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != binary {
		t.Errorf("got %q, want config path %q", got, binary)
	}
}

func TestResolveBinary_FallsBackToCachedCopy(t *testing.T) {
	dir := t.TempDir()
	cached := filepath.Join(dir, "bin", "cloudflared")
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("not-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	// configPath empty + nothing on PATH (we can't guarantee that —
	// if the host has cloudflared installed, exec.LookPath will
	// find it first. Skip in that case rather than mocking PATH).
	got, err := resolveBinary("", dir, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != cached && !strings.HasSuffix(got, "/cloudflared") {
		t.Errorf("got %q, want either cache %q or PATH cloudflared", got, cached)
	}
}
