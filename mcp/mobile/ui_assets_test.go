package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedMobileUIWithoutRuntimeDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	mux := http.NewServeMux()
	for _, route := range (&App{}).HTTPRoutes() {
		pattern := route.Pattern
		if route.Method != "" {
			pattern = route.Method + " " + pattern
		}
		mux.HandleFunc(pattern, route.Handler)
	}
	// The SDK may also mount an empty runtime /ui/ directory. Exact assets win.
	mux.Handle("/ui/", http.NotFoundHandler())
	for _, tc := range []struct{ path, kind, content string }{
		{"/ui/MobilePanel.mjs", "javascript", "Stay awake while charging"},
		{"/ui/MobilePanel.mjs.map", "json", "MobilePanel.tsx"},
		{"/ui/icon.svg", "svg", "<svg"},
	} {
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, httptest.NewRequest("GET", tc.path, nil))
		if r.Code != 200 || !strings.Contains(r.Header().Get("Content-Type"), tc.kind) || !strings.Contains(r.Body.String(), tc.content) {
			t.Fatalf("asset %s: status %d, content type %s", tc.path, r.Code, r.Header().Get("Content-Type"))
		}
	}
	r := httptest.NewRecorder()
	mux.ServeHTTP(r, httptest.NewRequest("GET", "/ui/MobilePanel.tsx", nil))
	if r.Code != 404 {
		t.Fatal("unexpected source asset exposed")
	}
}
