package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFinanceIconServedWithoutUIDirectory(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Icon != "/ui/icon.svg" || m.IconStyle != "monochrome" {
		t.Fatal("adaptive app icon missing", m.Icon, m.IconStyle)
	}
	mux := http.NewServeMux()
	found := false
	for _, r := range app.HTTPRoutes() {
		if r.Pattern == m.Icon {
			if !r.NoAuth || r.Method != "GET" {
				t.Fatal("icon must be a public GET asset")
			}
			mux.HandleFunc("GET "+r.Pattern, r.Handler)
			found = true
		}
	}
	if !found {
		t.Fatal("icon route missing")
	}
	t.Chdir(t.TempDir())
	for _, method := range []string{"GET", "HEAD"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/ui/icon.svg?v=0.2.1", nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" {
			t.Fatal(w.Code, w.Header())
		}
		if method == "GET" && (!strings.Contains(w.Body.String(), "<svg") || !strings.Contains(w.Body.String(), "currentColor")) {
			t.Fatal("missing theme-aware SVG")
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD returned a body")
		}
	}
}
