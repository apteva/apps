package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
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
	yamlEvents := eventNames(fromFile.Provides.Publishes)
	embedEvents := eventNames(fromEmbed.Provides.Publishes)
	if !sameStringSlices(yamlEvents, embedEvents) {
		t.Errorf("publish-event drift: yaml=%v embed=%v", yamlEvents, embedEvents)
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
	if got := fake.lastScreenshotAnnotate(); got == nil || *got != true {
		t.Errorf("computer_use screenshot annotate default: want true, got %v", got)
	}

	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
	}); err == nil || !strings.Contains(err.Error(), "requires label") {
		t.Fatalf("computer_use click without label/coordinate: want target error, got %v", err)
	}

	clickOut, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"label":      "7",
	})
	if err != nil {
		t.Fatalf("computer_use click with string label: %v", err)
	}
	if clickOut.(map[string]any)["current_url"] != "https://example.com" {
		t.Errorf("click current_url: want example.com, got %v", clickOut.(map[string]any)["current_url"])
	}
	if fake.lastAction.Type != "click" || fake.lastAction.Label != 7 {
		t.Errorf("click action: want label 7, got %+v", fake.lastAction)
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

func TestComputerUseRejectsExplicitLabelZero(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	_, err = app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"label":      0,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid_target") || !strings.Contains(err.Error(), "label=0") {
		t.Fatalf("click label=0: want invalid_target, got %v", err)
	}
	if fake.lastAction.Type != "" {
		t.Fatalf("invalid click executed action: %+v", fake.lastAction)
	}
}

func TestComputerUseContextCanceledEvictsSession(t *testing.T) {
	fake := &fakeComp{
		display:    backends.DisplaySize{Width: 1024, Height: 768},
		executeErr: context.Canceled,
	}
	app := &App{reg: &registry{m: map[string]*session{}}}
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))
	openedAt := time.Now().Add(-time.Minute)
	app.reg.put("br_dead", &session{
		comp:         fake,
		backend:      "browserbase",
		appContextID: "ctx_login",
		contextName:  "Ashley Login",
		persist:      true,
		openedAt:     openedAt,
		lastUsed:     openedAt,
	})

	_, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_dead",
		"action":     "click",
		"label":      5,
	})
	if err == nil || !strings.Contains(err.Error(), "session_unhealthy") || !strings.Contains(err.Error(), "context_id=ctx_login") {
		t.Fatalf("context canceled: want session_unhealthy with context, got %v", err)
	}
	if _, ok := app.reg.get("br_dead"); ok {
		t.Fatalf("unhealthy session was not removed from registry")
	}
	if fake.closeCalls != 1 {
		t.Fatalf("unhealthy session Close calls: want 1, got %d", fake.closeCalls)
	}
	payload := lastEventData(t, rec, "session.closed")
	if payload["session_id"] != "br_dead" || payload["close_reason"] != "session_unhealthy" {
		t.Fatalf("session.closed payload = %#v", payload)
	}
}

func TestComputerUseDeadlineExceededIsActionTimeout(t *testing.T) {
	fake := &fakeComp{
		display:    backends.DisplaySize{Width: 1024, Height: 768},
		executeErr: context.DeadlineExceeded,
	}
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	now := time.Now()
	app.reg.put("br_slow", &session{comp: fake, backend: "browserbase", openedAt: now, lastUsed: now})

	_, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_slow",
		"action":     "click",
		"label":      5,
	})
	if err == nil || !strings.Contains(err.Error(), "action_timeout") {
		t.Fatalf("deadline exceeded: want action_timeout, got %v", err)
	}
	if _, ok := app.reg.get("br_slow"); !ok {
		t.Fatalf("action timeout should not evict session automatically")
	}
}

func TestBrowserSessionResumeWithLiveAppSessionIDReturnsStatus(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "browserbase"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	resumeOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":     "resume",
		"backend":    "browserbase",
		"session_id": sessionID,
	})
	if err != nil {
		t.Fatalf("resume live app session: %v", err)
	}
	if got := resumeOut.(map[string]any)["session_id"]; got != sessionID {
		t.Fatalf("resume returned session_id=%v, want %s", got, sessionID)
	}
	if fake.openSessionID != "" {
		t.Fatalf("resume with live app session called provider attach with %q", fake.openSessionID)
	}
}

func TestBrowserSessionResumeWithStaleAppSessionIDIsClearError(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })
	called := false
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		called = true
		return &fakeComp{}, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	_, err := app.toolBrowserSession(ctx, map[string]any{
		"action":     "resume",
		"backend":    "browserbase",
		"session_id": "br_d911d6d7aa06cc5a",
	})
	if err == nil || !strings.Contains(err.Error(), "session_not_active") {
		t.Fatalf("resume stale app session: want session_not_active, got %v", err)
	}
	if called {
		t.Fatalf("resume stale app session should not instantiate provider backend")
	}
}

func TestBrowserSessionResumeRequiresExplicitBackendSessionID(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })
	called := false
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		called = true
		return &fakeComp{}, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	_, err := app.toolBrowserSession(ctx, map[string]any{
		"action":  "resume",
		"backend": "browserbase",
	})
	if err == nil || !strings.Contains(err.Error(), "backend_session_id required") {
		t.Fatalf("resume without ids: want backend_session_id required, got %v", err)
	}
	if called {
		t.Fatalf("resume without ids should not instantiate provider backend")
	}
}

func TestBrowserSessionResumeWithBackendSessionIDAttachesProvider(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{display: backends.DisplaySize{Width: 1024, Height: 768}, url: "https://example.com"}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action":             "resume",
		"backend":            "browserbase",
		"backend_session_id": "1733196a-6c7b-480e-b8d2-e9fdaa47bc85",
	})
	if err != nil {
		t.Fatalf("resume provider session: %v", err)
	}
	if fake.openSessionID != "1733196a-6c7b-480e-b8d2-e9fdaa47bc85" {
		t.Fatalf("provider attach id: got %q", fake.openSessionID)
	}
	if out.(map[string]any)["session_id"] == "" {
		t.Fatalf("resume provider session returned no app session: %v", out)
	}
}

func TestBrowserSessionResumeProviderExpiredError(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{openErr: fmt.Errorf("browserbase: lookup session bad: HTTP 400: Invalid Session ID")}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	_, err := app.toolBrowserSession(ctx, map[string]any{
		"action":             "resume",
		"backend":            "browserbase",
		"backend_session_id": "bad",
	})
	if err == nil || !strings.Contains(err.Error(), "provider_session_expired") {
		t.Fatalf("resume invalid provider session: want provider_session_expired, got %v", err)
	}
}

func TestValidateBackendConfiguredReportsFastErrors(t *testing.T) {
	cases := []struct {
		name string
		cfg  backends.Config
		want string
	}{
		{name: "browserbase", cfg: backends.Config{Type: "browserbase"}, want: "backend_not_configured: browserbase api_key"},
		{name: "steel", cfg: backends.Config{Type: "steel"}, want: "backend_not_configured: steel api_key"},
		{name: "browser-engine", cfg: backends.Config{Type: "browser-engine"}, want: "backend_not_configured: browser-engine api_key"},
		{name: "service", cfg: backends.Config{Type: "service"}, want: "backend_not_configured: service backend_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBackendConfigured(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateBackendConfigured: want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestComputerUseAutoFollowsSingleNewTab(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	newTab := backends.TabInfo{ID: "tab_detail", URL: "https://example.com/models/42", Title: "Model detail"}
	fake := &fakeComp{
		display:       backends.DisplaySize{Width: 1024, Height: 768},
		png:           []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:           "https://example.com/results",
		activeTabID:   "tab_results",
		tabs:          []backends.TabInfo{{ID: "tab_results", URL: "https://example.com/results", Title: "Results"}},
		addTabOnClick: &newTab,
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"coordinate": "10,10",
	})
	if err != nil {
		t.Fatalf("computer_use click: %v", err)
	}
	m := out.(map[string]any)
	if got := m["current_url"]; got != newTab.URL {
		t.Fatalf("current_url after new tab click: want %s, got %v", newTab.URL, got)
	}
	if got := m["active_tab_id"]; got != newTab.ID {
		t.Fatalf("active_tab_id: want %s, got %v", newTab.ID, got)
	}
	if got := m["switched_tab"]; got != true {
		t.Fatalf("switched_tab: want true, got %v", got)
	}
	if len(fake.switchCalls) != 1 || fake.switchCalls[0] != newTab.ID {
		t.Fatalf("switch calls: want [%s], got %v", newTab.ID, fake.switchCalls)
	}
	if fake.screenshotCalls != 1 {
		t.Fatalf("auto-follow should rescreenshot switched tab once, got %d calls", fake.screenshotCalls)
	}
}

func TestComputerUseBrowserbaseReturnsPostActionScreenshotByDefault(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "browserbase"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"coordinate": "10,10",
	})
	if err != nil {
		t.Fatalf("computer_use click: %v", err)
	}
	m := out.(map[string]any)
	if _, ok := m["screenshot_b64"]; !ok {
		t.Fatalf("screenshot_b64 should be present by default: %v", m)
	}
	if len(fake.actionOnlyCalls) != 0 {
		t.Fatalf("action-only should not be used by default: %+v", fake.actionOnlyCalls)
	}
	if fake.lastAction.Type != "click" {
		t.Fatalf("execute action: want click, got %+v", fake.lastAction)
	}
}

func TestComputerUseBrowserbaseActionOnlyOptInSkipsPostScreenshot(t *testing.T) {
	t.Setenv("APTEVA_BROWSERBASE_SPLIT_ACTION_SCREENSHOT", "1")
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "browserbase"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"coordinate": "10,10",
	})
	if err != nil {
		t.Fatalf("computer_use click: %v", err)
	}
	m := out.(map[string]any)
	if got := m["screenshot_available"]; got != false {
		t.Fatalf("screenshot_available: want false, got %v", got)
	}
	if _, ok := m["screenshot_b64"]; ok {
		t.Fatalf("screenshot_b64 should be omitted when screenshot is skipped: %v", m)
	}
	if got := m["post_action_screenshot"]; got != "skipped" {
		t.Fatalf("post_action_screenshot: want skipped, got %v", got)
	}
	if len(fake.actionOnlyCalls) != 1 || fake.actionOnlyCalls[0].Type != "click" {
		t.Fatalf("action-only calls: want one click, got %+v", fake.actionOnlyCalls)
	}

	waitOut, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "wait",
		"duration":   25,
	})
	if err != nil {
		t.Fatalf("computer_use wait: %v", err)
	}
	if got := waitOut.(map[string]any)["screenshot_available"]; got != false {
		t.Fatalf("wait screenshot_available: want false, got %v", got)
	}
	if len(fake.actionOnlyCalls) != 2 || fake.actionOnlyCalls[1].Type != "wait" {
		t.Fatalf("action-only calls after wait: %+v", fake.actionOnlyCalls)
	}
}

func TestComputerUseReportsScreenshotRecoveryMetadata(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.com/contact",
		lastRecovery: &backends.ScreenshotRecoveryInfo{
			Recovered:     true,
			Strategy:      "fresh_target_same_url",
			PreviousTabID: "tab_old",
			ActiveTabID:   "tab_new",
			URL:           "https://example.com/contact",
			Cause:         "context deadline exceeded",
		},
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "browserbase"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "screenshot",
	})
	if err != nil {
		t.Fatalf("computer_use screenshot: %v", err)
	}
	m := out.(map[string]any)
	if got := m["screenshot_recovered"]; got != true {
		t.Fatalf("screenshot_recovered: want true, got %v", got)
	}
	if got := m["screenshot_recovery"]; got != "fresh_target_same_url" {
		t.Fatalf("screenshot_recovery: want fresh_target_same_url, got %v", got)
	}
	if got := m["screenshot_recovery_previous_tab_id"]; got != "tab_old" {
		t.Fatalf("previous tab: want tab_old, got %v", got)
	}
	if got := m["screenshot_recovery_active_tab_id"]; got != "tab_new" {
		t.Fatalf("active tab: want tab_new, got %v", got)
	}
	if got := m["screenshot_recovery_cause"]; got != "context deadline exceeded" {
		t.Fatalf("cause: want timeout, got %v", got)
	}
}

func TestBrowserSessionTabActions(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display:     backends.DisplaySize{Width: 1024, Height: 768},
		png:         []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		activeTabID: "tab_a",
		tabs: []backends.TabInfo{
			{ID: "tab_a", URL: "https://example.com/a", Title: "A"},
			{ID: "tab_b", URL: "https://example.com/b", Title: "B"},
		},
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	tabsOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "tabs", "session_id": sessionID})
	if err != nil {
		t.Fatalf("browser_session tabs: %v", err)
	}
	if got := tabsOut.(map[string]any)["tab_count"]; got != 2 {
		t.Fatalf("tab_count: want 2, got %v", got)
	}

	switchOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "switch_tab", "session_id": sessionID, "tab_id": "tab_b"})
	if err != nil {
		t.Fatalf("browser_session switch_tab: %v", err)
	}
	if got := switchOut.(map[string]any)["active_tab_id"]; got != "tab_b" {
		t.Fatalf("active_tab_id after switch: want tab_b, got %v", got)
	}

	closeOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "close_tab", "session_id": sessionID, "tab_id": "tab_a"})
	if err != nil {
		t.Fatalf("browser_session close_tab: %v", err)
	}
	if got := closeOut.(map[string]any)["tab_count"]; got != 1 {
		t.Fatalf("tab_count after close: want 1, got %v", got)
	}
	if len(fake.closeTabCalls) != 1 || fake.closeTabCalls[0] != "tab_a" {
		t.Fatalf("close tab calls: want [tab_a], got %v", fake.closeTabCalls)
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

func TestHTTPHandlersEmitIntoRequestProject(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1200, Height: 700},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.test/app",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	rec := tk.NewEmitRecorder()
	globalCtx = tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))
	t.Cleanup(func() { globalCtx = nil })

	openOut := postJSON(t, app.handleOpenSession, "/sessions?project_id=proj-live", map[string]any{
		"action":  "open",
		"backend": "local",
		"url":     "https://example.test/app",
	})
	sessionID, _ := openOut["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", openOut)
	}
	assertLastEventProject(t, rec, "session.opened", "proj-live")

	_ = postJSON(t, app.handleComputerUse, "/sessions/"+sessionID+"/use?project_id=proj-live", map[string]any{
		"action": "type",
		"text":   "hello",
	})
	assertLastEventProject(t, rec, "session.action", "proj-live")

	req := httptest.NewRequest(http.MethodDelete, "/sessions/"+sessionID+"?project_id=proj-live", nil)
	w := httptest.NewRecorder()
	app.handleCloseSession(w, req)
	if w.Code < 200 || w.Code >= 300 {
		t.Fatalf("DELETE session: status=%d body=%s", w.Code, w.Body.String())
	}
	assertLastEventProject(t, rec, "session.closed", "proj-live")
}

func TestContextCatalogResolvesBrowserSessionContextName(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.test",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if ctx.AppDB() == nil {
		t.Fatal("test context has no AppDB")
	}

	created, err := app.toolContextCreate(ctx, map[string]any{
		"name":            "login",
		"backend":         "local",
		"persist_default": false,
	})
	if err != nil {
		t.Fatalf("context create: %v", err)
	}
	contextRow := created.(map[string]any)["context"].(*ComputerContext)
	if contextRow.ProviderContextID == "" {
		t.Fatalf("provider context id empty: %+v", contextRow)
	}

	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action":       "open",
		"backend":      "local",
		"context_name": "login",
	})
	if err != nil {
		t.Fatalf("browser_session open with context_name: %v", err)
	}
	openMap := out.(map[string]any)
	if openMap["app_context_id"] != contextRow.ID {
		t.Errorf("app_context_id: want %q, got %v", contextRow.ID, openMap["app_context_id"])
	}
	if fake.openContextID != contextRow.ProviderContextID {
		t.Errorf("OpenSession context id: want %q, got %q", contextRow.ProviderContextID, fake.openContextID)
	}
	if fake.openPersist {
		t.Errorf("persist should use context default false")
	}
}

func TestContextListDefaultsAllAndReportsOtherBackends(t *testing.T) {
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if ctx.AppDB() == nil {
		t.Fatal("test context has no AppDB")
	}

	created, err := app.toolContextCreate(ctx, map[string]any{
		"name":                 "patreon-login",
		"backend":              "browserbase",
		"provider_context_id":  "bb_ctx_123",
		"auto_create_provider": false,
	})
	if err != nil {
		t.Fatalf("context create: %v", err)
	}
	contextRow := created.(map[string]any)["context"].(*ComputerContext)

	allOut, err := app.toolContextList(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("context list all: %v", err)
	}
	allMap := allOut.(map[string]any)
	allRows := allMap["contexts"].([]*ComputerContext)
	if len(allRows) != 1 || allRows[0].Backend != "browserbase" {
		t.Fatalf("list all contexts = %+v", allRows)
	}
	if allMap["backend"] != "all" {
		t.Fatalf("list all backend marker = %v", allMap["backend"])
	}

	localOut, err := app.toolContextList(ctx, map[string]any{"backend": "local"})
	if err != nil {
		t.Fatalf("context list local: %v", err)
	}
	localMap := localOut.(map[string]any)
	if got := len(localMap["contexts"].([]*ComputerContext)); got != 0 {
		t.Fatalf("local contexts: want 0, got %d", got)
	}
	otherRows := localMap["other_contexts"].([]*ComputerContext)
	if len(otherRows) != 1 || otherRows[0].ID != contextRow.ID {
		t.Fatalf("other_contexts = %+v", otherRows)
	}
	if got := localMap["available_backends"].([]string); len(got) != 1 || got[0] != "browserbase" {
		t.Fatalf("available_backends = %#v", got)
	}

	if _, err := app.toolSettingsUpdate(ctx, map[string]any{"default_backend": "browserbase"}); err != nil {
		t.Fatalf("settings update: %v", err)
	}
	defaultOut, err := app.toolContextList(ctx, map[string]any{"backend": "default"})
	if err != nil {
		t.Fatalf("context list default: %v", err)
	}
	defaultRows := defaultOut.(map[string]any)["contexts"].([]*ComputerContext)
	if len(defaultRows) != 1 || defaultRows[0].ID != contextRow.ID {
		t.Fatalf("default contexts = %+v", defaultRows)
	}
}

func TestContextGetAndOpenFindUniqueNameAcrossBackends(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	var gotBackend string
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.test",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		gotBackend = cfg.Type
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	created, err := app.toolContextCreate(ctx, map[string]any{
		"name":                 "patreon-login",
		"backend":              "browserbase",
		"provider_context_id":  "bb_ctx_456",
		"auto_create_provider": false,
		"persist_default":      true,
	})
	if err != nil {
		t.Fatalf("context create: %v", err)
	}
	contextRow := created.(map[string]any)["context"].(*ComputerContext)

	got, err := app.toolContextGet(ctx, map[string]any{"name": "patreon-login"})
	if err != nil {
		t.Fatalf("context get by unique name: %v", err)
	}
	gotMap := got.(map[string]any)
	if gotMap["found"] != true || gotMap["context"].(*ComputerContext).ID != contextRow.ID {
		t.Fatalf("context get by unique name = %#v", gotMap)
	}

	openOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":       "open",
		"context_name": "patreon-login",
	})
	if err != nil {
		t.Fatalf("browser_session open by cross-backend context_name: %v", err)
	}
	if gotBackend != "browserbase" {
		t.Fatalf("newBackend type: want browserbase, got %q", gotBackend)
	}
	if openOut.(map[string]any)["app_context_id"] != contextRow.ID {
		t.Fatalf("open output = %#v", openOut)
	}
	if fake.openContextID != "bb_ctx_456" {
		t.Fatalf("OpenSession context id: want bb_ctx_456, got %q", fake.openContextID)
	}
}

func TestBrowserScreenshotDefaultsClean(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")

	openOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":  "open",
		"backend": "local",
		"url":     "https://example.com",
	})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	if _, err := app.toolBrowserScreenshot(ctx, map[string]any{"session_id": sessionID}); err != nil {
		t.Fatalf("browser_screenshot: %v", err)
	}
	if got := fake.lastScreenshotAnnotate(); got == nil || *got != false {
		t.Errorf("browser_screenshot annotate default: want false, got %v", got)
	}

	if _, err := app.toolBrowserScreenshot(ctx, map[string]any{"session_id": sessionID, "annotate": true}); err != nil {
		t.Fatalf("browser_screenshot annotate=true: %v", err)
	}
	if got := fake.lastScreenshotAnnotate(); got == nil || *got != true {
		t.Errorf("browser_screenshot annotate=true: want true, got %v", got)
	}
}

func TestBrowserSessionAutoCreateContextGeneratesName(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if ctx.AppDB() == nil {
		t.Fatal("test context has no AppDB")
	}

	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action":              "open",
		"backend":             "local",
		"url":                 "https://example.com/login",
		"auto_create_context": true,
		"persist":             true,
	})
	if err != nil {
		t.Fatalf("browser_session open with generated context: %v", err)
	}
	openMap := out.(map[string]any)
	contextID, _ := openMap["app_context_id"].(string)
	if contextID == "" {
		t.Fatalf("app_context_id missing: %#v", openMap)
	}
	contextName, _ := openMap["context_name"].(string)
	if !strings.HasPrefix(contextName, "auto-local-example-com-") {
		t.Fatalf("generated context_name = %q", contextName)
	}
	rec, err := dbGetContext(ctx.AppDB(), contextID)
	if err != nil {
		t.Fatalf("get generated context: %v", err)
	}
	if !rec.AutoCreated || rec.Name != contextName {
		t.Fatalf("generated context row = %+v, name from output = %q", rec, contextName)
	}
	if fake.openContextID != rec.ProviderContextID {
		t.Errorf("OpenSession context id: want %q, got %q", rec.ProviderContextID, fake.openContextID)
	}
	if !fake.openPersist {
		t.Errorf("persist should be true")
	}
}

func TestComputerSettingsDriveDefaultBackend(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	var gotBackend string
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.test",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		gotBackend = cfg.Type
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if _, err := app.toolSettingsUpdate(ctx, map[string]any{"default_backend": "browserbase"}); err != nil {
		t.Fatalf("settings update: %v", err)
	}
	out, err := app.toolBrowserSession(ctx, map[string]any{"action": "open"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	if gotBackend != "browserbase" {
		t.Errorf("newBackend type: want browserbase, got %q", gotBackend)
	}
	if out.(map[string]any)["backend"] != "browserbase" {
		t.Errorf("output backend: want browserbase, got %v", out.(map[string]any)["backend"])
	}
}

func TestComputerSettingsLockOverridesExplicitBackend(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	var gotBackend string
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.test",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		gotBackend = cfg.Type
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if _, err := app.toolSettingsUpdate(ctx, map[string]any{
		"default_backend": "browserbase",
		"lock_backend":    true,
	}); err != nil {
		t.Fatalf("settings update: %v", err)
	}
	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action":  "open",
		"backend": "local",
	})
	if err != nil {
		t.Fatalf("browser_session open with locked non-default backend: %v", err)
	}
	if gotBackend != "browserbase" {
		t.Errorf("newBackend type: want browserbase, got %q", gotBackend)
	}
	if out.(map[string]any)["backend"] != "browserbase" {
		t.Errorf("output backend: want browserbase, got %v", out.(map[string]any)["backend"])
	}
}

func TestBrowserbaseOpenHidesAndDisablesKeepAlive(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.test",
	}
	var gotCfg backends.Config
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		gotCfg = cfg
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action":     "open",
		"backend":    "browserbase",
		"timeout":    3600,
		"keep_alive": true,
	})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	if gotCfg.Type != "browserbase" {
		t.Fatalf("backend type: want browserbase, got %q", gotCfg.Type)
	}
	if gotCfg.KeepAlive {
		t.Fatal("browserbase keep_alive arg should be ignored and forced off")
	}
	if gotCfg.Timeout != 3600 {
		t.Fatalf("browserbase timeout config: want 3600, got %d", gotCfg.Timeout)
	}
	if fake.openTimeout != 3600 {
		t.Fatalf("OpenSession timeout: want 3600, got %d", fake.openTimeout)
	}
	outMap := out.(map[string]any)
	if _, ok := outMap["keep_alive"]; ok {
		t.Fatalf("output leaked hidden keep_alive: %#v", outMap)
	}
	if outMap["timeout_seconds"] != 3600 {
		t.Fatalf("output timeout_seconds: want 3600, got %#v", outMap["timeout_seconds"])
	}
	if outMap["provider_expires_at"] == "" {
		t.Fatalf("output provider_expires_at missing: %#v", outMap)
	}
	if _, ok := outMap["app_idle_expires_at"]; ok {
		t.Fatalf("output leaked internal app_idle_expires_at: %#v", outMap)
	}

	for _, toolName := range []string{"browser_session", "browser_open"} {
		tool := findTool(t, app.MCPTools(), toolName)
		props, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema has no properties map: %#v", toolName, tool.InputSchema)
		}
		if _, ok := props["keep_alive"]; ok {
			t.Fatalf("%s schema exposes hidden keep_alive", toolName)
		}
	}
}

func TestComputerUseDescriptionTeachesLabelWorkflow(t *testing.T) {
	app := &App{}
	var desc string
	for _, tool := range app.MCPTools() {
		if tool.Name == "computer_use" {
			desc = tool.Description
			break
		}
	}
	if desc == "" {
		t.Fatal("computer_use tool missing")
	}
	for _, want := range []string{
		"action=screenshot first",
		"Set-of-Mark",
		"label=N",
		"Prefer label over coordinate",
		"browser_session(action=tabs)",
		"browser_session(action=switch_tab",
		"Do not use Ctrl+Tab",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("computer_use description missing %q:\n%s", want, desc)
		}
	}

	yamlBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	manifest, err := sdk.ParseManifest(yamlBytes)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	for _, tool := range manifest.Provides.MCPTools {
		if tool.Name == "computer_use" {
			if !strings.Contains(tool.Description, "label=N") {
				t.Fatalf("manifest computer_use description does not teach label workflow:\n%s", tool.Description)
			}
			if !strings.Contains(tool.Description, "browser_session(action=tabs)") || !strings.Contains(tool.Description, "Do not use Ctrl+Tab") {
				t.Fatalf("manifest computer_use description does not teach explicit tab switching:\n%s", tool.Description)
			}
			return
		}
	}
	t.Fatal("manifest computer_use tool missing")
}

func TestAppBusEventsForComputerLifecycle(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.test",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))

	created, err := app.toolContextCreate(ctx, map[string]any{
		"name":            "login",
		"backend":         "local",
		"persist_default": true,
	})
	if err != nil {
		t.Fatalf("context create: %v", err)
	}
	contextRow := created.(map[string]any)["context"].(*ComputerContext)
	contextCreated := lastEventData(t, rec, "context.created")
	if contextCreated["id"] != contextRow.ID || contextCreated["backend"] != "local" {
		t.Fatalf("context.created payload = %#v", contextCreated)
	}

	if _, err := app.toolSettingsUpdate(ctx, map[string]any{
		"default_backend": "local",
		"lock_backend":    true,
	}); err != nil {
		t.Fatalf("settings update: %v", err)
	}
	settingsUpdated := lastEventData(t, rec, "settings.updated")
	if settingsUpdated["default_backend"] != "local" || settingsUpdated["lock_backend"] != true {
		t.Fatalf("settings.updated payload = %#v", settingsUpdated)
	}

	if _, err := app.toolContextUpdate(ctx, map[string]any{
		"id":              contextRow.ID,
		"persist_default": false,
	}); err != nil {
		t.Fatalf("context update: %v", err)
	}
	contextUpdated := lastEventData(t, rec, "context.updated")
	if contextUpdated["id"] != contextRow.ID || contextUpdated["persist_default"] != false {
		t.Fatalf("context.updated payload = %#v", contextUpdated)
	}

	openOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":     "open",
		"backend":    "local",
		"context_id": contextRow.ID,
		"url":        "https://example.test",
	})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)
	sessionOpened := lastEventData(t, rec, "session.opened")
	if sessionOpened["session_id"] != sessionID || sessionOpened["app_context_id"] != contextRow.ID {
		t.Fatalf("session.opened payload = %#v", sessionOpened)
	}

	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "type",
		"text":       "secret-value",
	}); err != nil {
		t.Fatalf("computer_use type: %v", err)
	}
	sessionAction := lastEventData(t, rec, "session.action")
	if sessionAction["action"] != "type" || sessionAction["text_length"] != len([]rune("secret-value")) {
		t.Fatalf("session.action payload = %#v", sessionAction)
	}
	if _, ok := sessionAction["text"]; ok {
		t.Fatalf("session.action leaked typed text: %#v", sessionAction)
	}

	if _, err := app.toolBrowserClose(ctx, map[string]any{"session_id": sessionID}); err != nil {
		t.Fatalf("browser close: %v", err)
	}
	sessionClosed := lastEventData(t, rec, "session.closed")
	if sessionClosed["session_id"] != sessionID || sessionClosed["backend"] != "local" {
		t.Fatalf("session.closed payload = %#v", sessionClosed)
	}

	if _, err := app.toolContextDelete(ctx, map[string]any{"id": contextRow.ID}); err != nil {
		t.Fatalf("context delete: %v", err)
	}
	contextDeleted := lastEventData(t, rec, "context.deleted")
	if contextDeleted["id"] != contextRow.ID || contextDeleted["provider_deleted"] != false {
		t.Fatalf("context.deleted payload = %#v", contextDeleted)
	}
}

func TestAppBusEventForReapedSession(t *testing.T) {
	app := &App{reg: &registry{m: map[string]*session{}}}
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 800, Height: 600},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://idle.example",
	}
	lastUsed := time.Now().Add(-2 * time.Hour)
	app.reg.put("br_stale", &session{
		comp:         fake,
		backend:      "local",
		appContextID: "ctx_login",
		contextName:  "login",
		persist:      true,
		openedAt:     lastUsed.Add(-time.Minute),
		lastUsed:     lastUsed,
	})

	rows := app.reapIdleSessions(ctx, 30*time.Minute)
	if len(rows) != 1 || rows[0].ID != "br_stale" {
		t.Fatalf("reaped rows = %+v", rows)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("stale session Close calls: want 1, got %d", fake.closeCalls)
	}
	payload := lastEventData(t, rec, "session.reaped")
	if payload["session_id"] != "br_stale" || payload["app_context_id"] != "ctx_login" {
		t.Fatalf("session.reaped payload = %#v", payload)
	}
	if idle, ok := payload["idle_seconds"].(int); !ok || idle < 3600 {
		t.Fatalf("session.reaped idle_seconds = %#v", payload["idle_seconds"])
	}
}

// ─── fake Computer ─────────────────────────────────────────────────

// fakeComp implements backends.Computer + SessionOpener + SessionInfo
// for handler tests. Mutation is unguarded — tests are single-goroutine.
type fakeComp struct {
	display          backends.DisplaySize
	png              []byte
	url              string
	executeErr       error
	screenshotErr    error
	executeActionErr error
	openSessionURL   string
	openSessionID    string
	openContextID    string
	openPersist      bool
	openTimeout      int
	openProxy        *bool
	openErr          error
	lastAction       backends.Action
	screenshotCalls  int
	annotateCalls    []bool
	closeCalls       int
	tabs             []backends.TabInfo
	activeTabID      string
	switchCalls      []string
	closeTabCalls    []string
	addTabOnClick    *backends.TabInfo
	actionOnlyCalls  []backends.Action
	lastRecovery     *backends.ScreenshotRecoveryInfo
	mu               sync.Mutex // for the unlikely concurrent test
}

func (f *fakeComp) Execute(action backends.Action) ([]byte, error) {
	f.mu.Lock()
	f.lastAction = action
	if (action.Type == "click" || action.Type == "double_click") && f.addTabOnClick != nil {
		tab := *f.addTabOnClick
		if tab.ID == "" {
			tab.ID = "tab_new"
		}
		f.tabs = append(f.tabs, tab)
		f.addTabOnClick = nil
	}
	f.mu.Unlock()
	if f.executeErr != nil {
		return nil, f.executeErr
	}
	return f.png, nil
}

func (f *fakeComp) ExecuteAction(action backends.Action) error {
	f.mu.Lock()
	f.lastAction = action
	f.actionOnlyCalls = append(f.actionOnlyCalls, action)
	if (action.Type == "click" || action.Type == "double_click") && f.addTabOnClick != nil {
		tab := *f.addTabOnClick
		if tab.ID == "" {
			tab.ID = "tab_new"
		}
		f.tabs = append(f.tabs, tab)
		f.addTabOnClick = nil
	}
	f.mu.Unlock()
	return f.executeActionErr
}

func (f *fakeComp) Screenshot() ([]byte, error) {
	return f.ScreenshotWithOptions(backends.ScreenshotOptions{Annotate: true})
}

func (f *fakeComp) ScreenshotWithOptions(options backends.ScreenshotOptions) ([]byte, error) {
	f.mu.Lock()
	f.screenshotCalls++
	f.annotateCalls = append(f.annotateCalls, options.Annotate)
	f.mu.Unlock()
	if f.screenshotErr != nil {
		return nil, f.screenshotErr
	}
	return f.png, nil
}

func (f *fakeComp) LastScreenshotRecovery() *backends.ScreenshotRecoveryInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastRecovery == nil {
		return nil
	}
	cp := *f.lastRecovery
	return &cp
}

func (f *fakeComp) lastScreenshotAnnotate() *bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.annotateCalls) == 0 {
		return nil
	}
	v := f.annotateCalls[len(f.annotateCalls)-1]
	return &v
}

func (f *fakeComp) DisplaySize() backends.DisplaySize { return f.display }

func (f *fakeComp) Close() error {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
	return nil
}

func (f *fakeComp) ListTabs() ([]backends.TabInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]backends.TabInfo, len(f.tabs))
	copy(out, f.tabs)
	for i := range out {
		out[i].Active = out[i].ID != "" && out[i].ID == f.activeTabID
	}
	return out, nil
}

func (f *fakeComp) ActiveTabID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeTabID
}

func (f *fakeComp) SwitchTab(tabID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.tabs {
		if f.tabs[i].ID == tabID {
			f.activeTabID = tabID
			f.switchCalls = append(f.switchCalls, tabID)
			return nil
		}
	}
	return fmt.Errorf("tab %s not found", tabID)
}

func (f *fakeComp) CloseTab(tabID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tabs) <= 1 {
		return fmt.Errorf("cannot close last tab")
	}
	for i := range f.tabs {
		if f.tabs[i].ID == tabID {
			f.tabs = append(f.tabs[:i], f.tabs[i+1:]...)
			f.closeTabCalls = append(f.closeTabCalls, tabID)
			if f.activeTabID == tabID && len(f.tabs) > 0 {
				f.activeTabID = f.tabs[0].ID
			}
			return nil
		}
	}
	return fmt.Errorf("tab %s not found", tabID)
}

func (f *fakeComp) OpenSession(opts backends.OpenOptions) error {
	f.openSessionURL = opts.URL
	f.openSessionID = opts.SessionID
	f.openContextID = opts.ContextID
	f.openPersist = opts.Persist
	f.openTimeout = opts.Timeout
	f.openProxy = opts.Proxy
	return f.openErr
}

// SessionInfo interface
func (f *fakeComp) SessionType() string { return "fake" }
func (f *fakeComp) SessionID() string   { return f.openSessionID }
func (f *fakeComp) CurrentURL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tab := range f.tabs {
		if tab.ID == f.activeTabID && tab.URL != "" {
			return tab.URL
		}
	}
	return f.url
}

// ─── helpers ───────────────────────────────────────────────────────

func toolNames(tools []sdk.MCPToolSpec) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

func findTool(t *testing.T, tools []sdk.Tool, name string) sdk.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return sdk.Tool{}
}

func eventNames(events []sdk.EventDecl) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

func sameStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

func lastEventData(t *testing.T, rec *tk.EmitRecorder, topic string) map[string]any {
	t.Helper()
	events := rec.EventsByTopic(topic)
	if len(events) == 0 {
		t.Fatalf("expected event %q, got events %#v", topic, rec.Events())
	}
	data, ok := events[len(events)-1].Data.(map[string]any)
	if !ok {
		t.Fatalf("event %q data type = %T, want map[string]any", topic, events[len(events)-1].Data)
	}
	return data
}

func assertLastEventProject(t *testing.T, rec *tk.EmitRecorder, topic, projectID string) {
	t.Helper()
	events := rec.EventsByTopic(topic)
	if len(events) == 0 {
		t.Fatalf("expected event %q, got events %#v", topic, rec.Events())
	}
	if got := events[len(events)-1].ProjectID; got != projectID {
		t.Fatalf("%s project id: want %q, got %q", topic, projectID, got)
	}
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
