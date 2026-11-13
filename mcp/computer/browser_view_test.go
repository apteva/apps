package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

type browserViewPlatform struct {
	tk.BasePlatformClient
	identity *sdk.InstallIdentity
	calls    int
}

func (p *browserViewPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	p.calls++
	return p.identity, nil
}

func TestComputerInstallIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		identity  *sdk.InstallIdentity
		want      int64
		calls     int
	}{
		{"runtime environment", "55", &sdk.InstallIdentity{InstallID: 165}, 55, 0},
		{"platform fallback", "", &sdk.InstallIdentity{InstallID: 55}, 55, 1},
		{"invalid environment", "-1", &sdk.InstallIdentity{InstallID: 55}, 55, 1},
		{"no identity", "", nil, 0, 1},
		{"invalid platform identity", "", &sdk.InstallIdentity{InstallID: 0}, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APTEVA_INSTALL_ID", tc.env)
			platform := &browserViewPlatform{identity: tc.identity}
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
			if got := computerInstallID(ctx); got != tc.want || platform.calls != tc.calls {
				t.Fatalf("identity=%d calls=%d, want %d/%d", got, platform.calls, tc.want, tc.calls)
			}
			view := (&App{installID: tc.want}).browserViewReference("br_identity")
			if tc.want == 0 {
				if _, ok := view["resource"]; ok {
					t.Fatal("fabricated resource without trusted identity")
				}
			} else {
				ref := view["resource"].(sdk.AppResourceRef)
				if err := ref.Validate(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestBrowserViewResourceAcrossLifecycle(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })
	fake := &fakeComp{display: backends.DisplaySize{Width: 1024, Height: 768}, url: "https://example.com"}
	newBackend = func(backends.Config) (backends.Computer, error) { return fake, nil }
	t.Setenv("APTEVA_INSTALL_ID", "55")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{installID: computerInstallID(ctx), reg: &registry{m: map[string]*session{}}}
	opened, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local", "url": "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	id := opened.(map[string]any)["session_id"].(string)
	t.Cleanup(func() { _, _ = app.toolBrowserSession(ctx, map[string]any{"action": "close", "session_id": id}) })
	expected := app.browserViewReference(id)
	resource := expected["resource"].(sdk.AppResourceRef)
	if resource.InstallID != 55 || resource.ResourceType != "computer.browser_session" || resource.ResourceID != id {
		t.Fatalf("resource=%+v", resource)
	}
	if err := resource.Validate(); err != nil {
		t.Fatal(err)
	}
	check := func(value any) {
		t.Helper()
		if !reflect.DeepEqual(value, expected) {
			t.Fatalf("view changed: %#v, want %#v", value, expected)
		}
	}
	check(opened.(map[string]any)["view"])
	// Caller/tool fields cannot change the owner captured at mount.
	t.Setenv("APTEVA_INSTALL_ID", "165")
	for _, action := range []string{"status", "close", "status", "close"} {
		out, err := app.toolBrowserSession(ctx, map[string]any{"action": action, "session_id": id})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		check(out.(map[string]any)["view"])
	}
	// A restarted sidecar must return the same locator from durable history.
	restarted := &App{installID: 55, reg: &registry{m: map[string]*session{}}}
	out, err := restarted.toolBrowserSession(ctx, map[string]any{"action": "status", "session_id": id})
	if err != nil {
		t.Fatal(err)
	}
	check(out.(map[string]any)["view"])
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Resource sdk.AppResourceRef `json:"resource"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil || wire.Resource != resource {
		t.Fatalf("resource wire=%s err=%v", encoded, err)
	}
}

func TestBrowserMediaURLsKeepComputerInstallRouting(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("one")
	app := &App{installID: 55}
	// The runtime environment cannot replace the identity captured at mount.
	t.Setenv("APTEVA_INSTALL_ID", "165")
	for name, raw := range map[string]string{
		"screenshot":      app.sessionResourceURL(ctx, "br_one", "screenshot"),
		"stream":          app.sessionResourceURL(ctx, "br_one", "stream"),
		"playlist":        app.recordingPlaylistURL(ctx, "br_one", "tab-1"),
		"recording asset": app.recordingAssetURL(ctx, "br_one", "tab-1", "opaque-token"),
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Query().Get("install_id") != "55" || parsed.Query().Get("project_id") != "one" {
			t.Fatalf("%s routing=%s", name, raw)
		}
		if name == "recording asset" && parsed.Query().Get("token") != "opaque-token" {
			t.Fatal("asset token lost")
		}
	}
}

func TestPassiveBrowserViewsDoNotRenewSessionLifetime(t *testing.T) {
	prev := newBackend
	previousCtx := globalCtx
	t.Cleanup(func() { newBackend = prev; globalCtx = previousCtx })
	fake := &fakeComp{display: backends.DisplaySize{Width: 1024, Height: 768}, url: "https://example.com", png: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}}
	newBackend = func(backends.Config) (backends.Computer, error) { return fake, nil }
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("one")
	globalCtx = ctx
	app := &App{installID: 55, reg: &registry{m: map[string]*session{}}}
	opened, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local", "url": "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	id := opened.(map[string]any)["session_id"].(string)
	t.Cleanup(func() { _, _ = app.toolBrowserClose(ctx, map[string]any{"session_id": id}) })
	sess, _ := app.reg.peek(id)
	// Exercise a project-bound session; ordinary browser sessions may be global.
	sess.projectID = "one"
	stale := time.Now().Add(-2 * time.Minute)
	sess.lastUsedMu.Lock()
	sess.lastUsed = stale
	sess.lastUsedMu.Unlock()
	routes := map[string]sdk.Route{}
	for _, route := range app.HTTPRoutes() {
		routes[route.Pattern] = route
	}
	for _, resource := range []string{"presentation", "stream", "screenshot"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/sessions/"+id+"/"+resource+"?project_id=one&install_id=55", nil)
		routes["/sessions/{id}/"+resource].Handler(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", resource, response.Code, response.Body.String())
		}
		if !sess.usedAt().Equal(stale) {
			t.Fatalf("%s extended idle lifetime", resource)
		}
	}
	// Authorization, including a rejected foreign-project view, is passive too.
	response := httptest.NewRecorder()
	routes["/sessions/{id}/presentation"].Handler(response, httptest.NewRequest(http.MethodGet, "/sessions/"+id+"/presentation?project_id=other", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("foreign view status=%d", response.Code)
	}
	if !sess.usedAt().Equal(stale) {
		t.Fatal("foreign view renewed idle lifetime")
	}
	reaped := app.reapIdleSessions(ctx, time.Minute)
	if len(reaped) != 1 || reaped[0].ID != id {
		t.Fatalf("pinned idle session was not reaped: %+v", reaped)
	}
	response = httptest.NewRecorder()
	routes["/sessions/{id}/screenshot"].Handler(response, httptest.NewRequest(http.MethodGet, "/sessions/"+id+"/screenshot?project_id=one&install_id=55", nil))
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), fake.png) {
		t.Fatalf("saved final frame: %d %x", response.Code, response.Body.Bytes())
	}
	if fake.closeCalls != 1 {
		t.Fatal("reaper did not release browser")
	}
}
