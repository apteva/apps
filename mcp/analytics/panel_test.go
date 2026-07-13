package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	for _, want := range []string{"/summary", "/series", "/top", "/feed", "/dimensions", "/ui/tag.js", "/collect", "/keys", "/keys/revoke", "/capture", "/dashboards", "/dashboards/", "/widgets/", "/query-widget", "/query-dashboard", "/dashboard-filter-options", "/event-specs", "/event-specs/", "/event-spec-violations"} {
		if !got[want] {
			t.Errorf("missing route %s", want)
		}
	}
}

func TestOnlyTrackingRoutesAllowAnonymousAccess(t *testing.T) {
	want := map[string]string{
		"/ui/tag.js": http.MethodGet,
		"/collect":   http.MethodGet,
	}

	gotSDK := map[string]string{}
	for _, route := range (&App{}).HTTPRoutes() {
		if route.NoAuth {
			gotSDK[route.Pattern] = route.Method
		}
	}
	if len(gotSDK) != len(want) {
		t.Fatalf("SDK public routes = %v, want %v", gotSDK, want)
	}
	for path, method := range want {
		if gotSDK[path] != method {
			t.Errorf("SDK public route %s method = %q, want %q", path, gotSDK[path], method)
		}
	}

	gotManifest := map[string]string{}
	for _, route := range (&App{}).Manifest().Provides.HTTPRoutes {
		if route.NoAuth {
			gotManifest[route.Prefix] = route.Method
		}
	}
	if len(gotManifest) != len(want) {
		t.Fatalf("manifest public routes = %v, want %v", gotManifest, want)
	}
	for path, method := range want {
		if gotManifest[path] != method {
			t.Errorf("manifest public route %s method = %q, want %q", path, gotManifest[path], method)
		}
	}
}

func TestDiskAndEmbeddedManifestsAgreeOnPublicRoutes(t *testing.T) {
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(body)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	embedded := (&App{}).Manifest()
	if disk.Version != embedded.Version {
		t.Fatalf("manifest versions differ: disk=%q embedded=%q", disk.Version, embedded.Version)
	}

	publicRoutes := func(manifest sdk.Manifest) map[string]string {
		got := map[string]string{}
		for _, route := range manifest.Provides.HTTPRoutes {
			if route.NoAuth {
				got[route.Prefix] = route.Method
			}
		}
		return got
	}
	diskRoutes := publicRoutes(*disk)
	embeddedRoutes := publicRoutes(embedded)
	if len(diskRoutes) != len(embeddedRoutes) {
		t.Fatalf("public routes differ: disk=%v embedded=%v", diskRoutes, embeddedRoutes)
	}
	for path, method := range embeddedRoutes {
		if diskRoutes[path] != method {
			t.Errorf("disk public route %s method = %q, embedded = %q", path, diskRoutes[path], method)
		}
	}
}

func TestLegacyTrackingTagRouteServesEmbeddedScript(t *testing.T) {
	rec := httptest.NewRecorder()
	(&App{}).handleTrackingTag(rec, httptest.NewRequest(http.MethodGet, "/ui/tag.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if rec.Body.String() != string(trackingTagJS) {
		t.Fatal("handler did not serve the embedded tracking script")
	}
	if !strings.Contains(rec.Body.String(), `.replace(/\/ui\/tag\.js$/, "")`) {
		t.Fatal("legacy tag no longer derives the /collect endpoint from /ui/tag.js")
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
