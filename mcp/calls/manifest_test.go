package main

import "testing"

func TestManifestBasics(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "calls" {
		t.Fatalf("manifest.Name=%q, want calls", m.Name)
	}
	if m.DB == nil || m.DB.Migrations != "migrations/" {
		t.Fatalf("manifest DB migrations not wired: %#v", m.DB)
	}
	if len(m.Requires.Apps) != 0 {
		t.Fatalf("calls should not require other apps, got %#v", m.Requires.Apps)
	}
	names := map[string]bool{}
	for _, tool := range m.Provides.MCPTools {
		names[tool.Name] = true
	}
	for _, name := range []string{
		"calls_create_room",
		"calls_create_join_token",
		"calls_join_room",
		"calls_send_message",
		"calls_append_transcript",
	} {
		if !names[name] {
			t.Fatalf("manifest missing tool %s", name)
		}
	}
}

func TestManifestGuestRoutesMatchRuntime(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()

	manifestRoutes := make(map[string]bool, len(manifest.Provides.HTTPRoutes))
	for _, route := range manifest.Provides.HTTPRoutes {
		manifestRoutes[route.Prefix] = route.NoAuth
	}

	wantPublic := map[string]bool{
		"/join/":      true,
		"/room/":      true,
		"/api/join":   true,
		"/api/rooms/": true,
	}
	for prefix := range wantPublic {
		noAuth, ok := manifestRoutes[prefix]
		if !ok || !noAuth {
			t.Errorf("manifest route %q = (present %v, no_auth %v), want public", prefix, ok, noAuth)
		}
	}
	if noAuth, ok := manifestRoutes["/"]; !ok || noAuth {
		t.Errorf("manifest catch-all = (present %v, no_auth %v), want authenticated", ok, noAuth)
	}

	runtimePublic := map[string]bool{}
	for _, route := range app.HTTPRoutes() {
		if route.NoAuth {
			runtimePublic[route.Pattern] = true
		}
	}
	for prefix := range wantPublic {
		if !runtimePublic[prefix] {
			t.Errorf("runtime route %q is not public", prefix)
		}
	}
	for prefix := range runtimePublic {
		if !wantPublic[prefix] {
			t.Errorf("runtime unexpectedly exposes %q without app auth", prefix)
		}
	}
}
