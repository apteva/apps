package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestManagedPackageReleaseCapabilitiesMatchRuntime(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	if manifest.Runtime.Package == nil || manifest.Runtime.Package.AppCapabilities == nil {
		t.Fatal("release lacks portable lifecycle metadata")
	}
	t.Setenv("APTEVA_APP_TOKEN", "package-test")
	req := httptest.NewRequest(http.MethodGet, runtimeAppPrefix+"describe", nil)
	req.Header.Set("Authorization", "Bearer package-test")
	w := httptest.NewRecorder()
	app.handleRuntimeLifecycle(w, req)
	var actual sdk.RuntimeAppCapabilities
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &actual) != nil || !reflect.DeepEqual(actual, *manifest.Runtime.Package.AppCapabilities) {
		t.Fatalf("release/runtime capability mismatch: %s", w.Body.String())
	}
}
