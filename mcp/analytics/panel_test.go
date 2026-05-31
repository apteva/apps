package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// The embedded manifest is only parsed at boot (App.Manifest panics on a
// bad string), so a YAML typo in the ui_panels block would ship silently.
// Guard it here.
func TestEmbeddedManifestParses(t *testing.T) {
	if _, err := sdk.ParseManifest([]byte(manifestYAML)); err != nil {
		t.Fatalf("embedded manifest invalid: %v", err)
	}
}

func TestHTTPRoutesRegistered(t *testing.T) {
	got := map[string]bool{}
	for _, r := range (&App{}).HTTPRoutes() {
		got[r.Pattern] = true
	}
	for _, want := range []string{"/summary", "/series", "/top", "/feed", "/dimensions", "/collect", "/keys", "/keys/revoke", "/capture", "/dashboards", "/dashboards/", "/widgets/", "/query-widget"} {
		if !got[want] {
			t.Errorf("missing route %s", want)
		}
	}
}

// The app-sdk mounts its own /health, /manifest, /mcp, /events, /ui/ on
// the same ServeMux; an app route reusing any of those panics at boot
// (this is how v0.3.0's /events route broke the install). Guard it.
func TestHTTPRoutesAvoidReservedSDKRoutes(t *testing.T) {
	reserved := map[string]bool{
		"/health": true, "/manifest": true, "/mcp": true, "/events": true, "/ui/": true,
	}
	for _, r := range (&App{}).HTTPRoutes() {
		if reserved[r.Pattern] {
			t.Errorf("route %q collides with a reserved app-sdk framework route", r.Pattern)
		}
	}
}
