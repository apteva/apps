package main

import (
	"encoding/json"
	"reflect"
	"testing"

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
