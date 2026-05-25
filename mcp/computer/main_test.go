package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

// TestEmbeddedManifestMatchesYAML guards the dual-source-of-truth
// hazard: apteva.yaml is what the platform reads at install time,
// manifestYAML is what a built sidecar binary self-reports. They MUST
// agree on the load-bearing fields (name, version, scope, the tool
// list, declared permissions) or installs will succeed against a yaml
// that promises tools the binary doesn't expose.
func TestEmbeddedManifestMatchesYAML(t *testing.T) {
	yamlBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	fromFile, err := sdk.ParseManifest(yamlBytes)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	fromEmbed, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}

	if fromFile.Name != fromEmbed.Name {
		t.Errorf("name drift: yaml=%q embed=%q", fromFile.Name, fromEmbed.Name)
	}
	if fromFile.Version != fromEmbed.Version {
		t.Errorf("version drift: yaml=%q embed=%q", fromFile.Version, fromEmbed.Version)
	}
	if !sameScopes(fromFile.Scopes, fromEmbed.Scopes) {
		t.Errorf("scopes drift: yaml=%v embed=%v", fromFile.Scopes, fromEmbed.Scopes)
	}
	if !samePermissions(fromFile.Requires.Permissions, fromEmbed.Requires.Permissions) {
		t.Errorf("permissions drift: yaml=%v embed=%v",
			fromFile.Requires.Permissions, fromEmbed.Requires.Permissions)
	}

	yamlTools := toolNames(fromFile.Provides.MCPTools)
	embedTools := toolNames(fromEmbed.Provides.MCPTools)
	if len(yamlTools) != len(embedTools) {
		t.Errorf("tool-count drift: yaml=%d embed=%d (yaml=%v embed=%v)",
			len(yamlTools), len(embedTools), yamlTools, embedTools)
	}
	for i, name := range yamlTools {
		if i >= len(embedTools) || embedTools[i] != name {
			t.Errorf("tool names differ: yaml=%v embed=%v", yamlTools, embedTools)
			break
		}
	}
}

// TestRegistry covers the registry's contract: put adds, get refreshes
// lastUsed, remove is idempotent on unknown ids, reapIdle returns only
// stale ids and closes them.
func TestRegistry(t *testing.T) {
	r := &registry{m: map[string]*session{}}

	// put + get
	now := time.Now()
	fake1 := &fakeComp{}
	r.put("a", &session{comp: fake1, backend: "local", openedAt: now, lastUsed: now})
	got, ok := r.get("a")
	if !ok || got.comp != fake1 {
		t.Fatalf("get(a): want fake1, got=%v ok=%v", got, ok)
	}

	// get refreshes lastUsed
	r.put("b", &session{comp: &fakeComp{}, backend: "local", openedAt: now, lastUsed: now.Add(-2 * time.Hour)})
	beforeGet := time.Now()
	_, _ = r.get("b")
	got2, _ := r.get("b")
	if !got2.lastUsed.After(beforeGet.Add(-time.Second)) {
		t.Errorf("get did not refresh lastUsed; got %v want >= %v", got2.lastUsed, beforeGet)
	}

	// remove on unknown returns ok=false, doesn't panic
	if _, ok := r.remove("does-not-exist"); ok {
		t.Errorf("remove of unknown returned ok=true")
	}

	// reapIdle closes only stale entries
	r.put("stale", &session{comp: &fakeComp{}, backend: "local", openedAt: now, lastUsed: now.Add(-2 * time.Hour)})
	r.put("fresh", &session{comp: &fakeComp{}, backend: "local", openedAt: now, lastUsed: time.Now()})
	reaped := r.reapIdle(30 * time.Minute)
	sort.Strings(reaped)
	if len(reaped) != 1 || reaped[0] != "stale" {
		t.Errorf("reapIdle: want [stale], got %v", reaped)
	}
	if _, ok := r.get("fresh"); !ok {
		t.Errorf("fresh session was reaped")
	}
}

// TestBrowserSessionComputerUseClose drives the generic MCP surface
// end-to-end against a fake backend. The fake records OpenSession +
// Screenshot + Close calls so we can verify the registry actually
// closes the underlying Computer on browser_session(close).
func TestBrowserSessionComputerUseClose(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		if cfg.Type != "local" {
			t.Errorf("backend type: want local, got %q", cfg.Type)
		}
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")

	// open
	openOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":  "open",
		"backend": "local",
		"url":     "https://example.com",
	})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	openMap, ok := openOut.(map[string]any)
	if !ok {
		t.Fatalf("open returned %T, want map", openOut)
	}
	sessionID, _ := openMap["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned empty session_id; out=%v", openMap)
	}
	if got := openMap["backend"]; got != "local" {
		t.Errorf("open backend: want local, got %v", got)
	}
	if got := openMap["width"]; got != 1024 {
		t.Errorf("open width: want 1024, got %v", got)
	}
	if fake.openSessionURL != "https://example.com" {
		t.Errorf("OpenSession URL: want example.com, got %q", fake.openSessionURL)
	}

	// screenshot
	shotOut, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "screenshot",
	})
	if err != nil {
		t.Fatalf("computer_use screenshot: %v", err)
	}
	shotMap := shotOut.(map[string]any)
	gotPNG, _ := base64.StdEncoding.DecodeString(shotMap["screenshot_b64"].(string))
	if len(gotPNG) != len(fake.png) || gotPNG[0] != 0x89 {
		t.Errorf("screenshot bytes round-trip failed: got %v", gotPNG)
	}
	if shotMap["mime_type"] != "image/png" {
		t.Errorf("screenshot mime: want image/png, got %v", shotMap["mime_type"])
	}
	if shotMap["current_url"] != "https://example.com" {
		t.Errorf("current_url: want example.com, got %v", shotMap["current_url"])
	}
	if fake.screenshotCalls != 1 {
		t.Errorf("screenshot calls: want 1, got %d", fake.screenshotCalls)
	}

	// close
	closeOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "close", "session_id": sessionID})
	if err != nil {
		t.Fatalf("browser_session close: %v", err)
	}
	if closeOut.(map[string]any)["closed"] != true {
		t.Errorf("close closed=true expected; got %v", closeOut)
	}
	if fake.closeCalls != 1 {
		t.Errorf("Close calls: want 1, got %d", fake.closeCalls)
	}

	// close again — idempotent
	closeOut2, err := app.toolBrowserSession(ctx, map[string]any{"action": "close", "session_id": sessionID})
	if err != nil {
		t.Fatalf("browser_session close (2nd): %v", err)
	}
	if closeOut2.(map[string]any)["closed"] != false {
		t.Errorf("2nd close: closed=false expected; got %v", closeOut2)
	}

	// screenshot after close — error, not panic
	if _, err := app.toolComputerUse(ctx, map[string]any{"session_id": sessionID, "action": "screenshot"}); err == nil {
		t.Errorf("screenshot after close: want error, got nil")
	}
}

func TestHTTPRoutesUseCanonicalToolHandlers(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	proxy := true
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1200, Height: 700},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.test/app",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		if cfg.Type != "local" {
			t.Errorf("backend type: want local, got %q", cfg.Type)
		}
		if cfg.Width != 1200 || cfg.Height != 700 {
			t.Errorf("viewport cfg: want 1200x700, got %dx%d", cfg.Width, cfg.Height)
		}
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	globalCtx = tk.NewAppCtx(t, "apteva.yaml")
	t.Cleanup(func() { globalCtx = nil })

	openBody := map[string]any{
		"action":     "open",
		"backend":    "local",
		"url":        "https://example.test/app",
		"context_id": "ctx-login",
		"persist":    false,
		"proxy":      proxy,
		"viewport": map[string]any{
			"width":  1200,
			"height": 700,
		},
	}
	openOut := postJSON(t, app.handleOpenSession, "/sessions", openBody)
	sessionID, _ := openOut["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", openOut)
	}
	if fake.openContextID != "ctx-login" {
		t.Errorf("context_id: want ctx-login, got %q", fake.openContextID)
	}
	if fake.openPersist {
		t.Errorf("persist: want false")
	}
	if fake.openProxy == nil || *fake.openProxy != true {
		t.Errorf("proxy: want true, got %v", fake.openProxy)
	}

	useOut := postJSON(t, app.handleComputerUse, "/sessions/"+sessionID+"/use", map[string]any{
		"action":     "click",
		"coordinate": "42,64",
	})
	if got := useOut["current_url"]; got != "https://example.test/app" {
		t.Errorf("current_url: want example.test, got %v", got)
	}
	if fake.lastAction.Type != "click" || fake.lastAction.X != 42 || fake.lastAction.Y != 64 {
		t.Errorf("last action: want click 42,64 got %+v", fake.lastAction)
	}
}

// ─── fake Computer ─────────────────────────────────────────────────

// fakeComp implements backends.Computer + SessionOpener + SessionInfo
// for handler tests. Mutation is unguarded — tests are single-goroutine.
type fakeComp struct {
	display         backends.DisplaySize
	png             []byte
	url             string
	openSessionURL  string
	openContextID   string
	openPersist     bool
	openProxy       *bool
	lastAction      backends.Action
	screenshotCalls int
	closeCalls      int
	mu              sync.Mutex // for the unlikely concurrent test
}

func (f *fakeComp) Execute(action backends.Action) ([]byte, error) {
	f.mu.Lock()
	f.lastAction = action
	f.mu.Unlock()
	return f.png, nil
}

func (f *fakeComp) Screenshot() ([]byte, error) {
	f.mu.Lock()
	f.screenshotCalls++
	f.mu.Unlock()
	return f.png, nil
}

func (f *fakeComp) DisplaySize() backends.DisplaySize { return f.display }

func (f *fakeComp) Close() error {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
	return nil
}

func (f *fakeComp) OpenSession(opts backends.OpenOptions) error {
	f.openSessionURL = opts.URL
	f.openContextID = opts.ContextID
	f.openPersist = opts.Persist
	f.openProxy = opts.Proxy
	return nil
}

// SessionInfo interface
func (f *fakeComp) SessionType() string { return "fake" }
func (f *fakeComp) SessionID() string   { return "" }
func (f *fakeComp) CurrentURL() string  { return f.url }

// ─── helpers ───────────────────────────────────────────────────────

func toolNames(tools []sdk.MCPToolSpec) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

func sameScopes(a, b []sdk.Scope) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[sdk.Scope]bool{}
	for _, s := range a {
		am[s] = true
	}
	for _, s := range b {
		if !am[s] {
			return false
		}
	}
	return true
}

func samePermissions(a, b []sdk.Permission) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[sdk.Permission]bool{}
	for _, p := range a {
		am[p] = true
	}
	for _, p := range b {
		if !am[p] {
			return false
		}
	}
	return true
}

func postJSON(t *testing.T, handler http.HandlerFunc, path string, body any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code < 200 || w.Code >= 300 {
		t.Fatalf("POST %s: status=%d body=%s", path, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	return out
}
