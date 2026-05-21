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
	for _, want := range []string{"/summary", "/series", "/top", "/events", "/dimensions"} {
		if !got[want] {
			t.Errorf("missing route %s", want)
		}
	}
}
