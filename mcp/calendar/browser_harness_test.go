package main

import (
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBrowserHarness(t *testing.T) {
	if os.Getenv("CALENDAR_BROWSER") != "1" {
		t.Skip("browser harness only")
	}
	newCtx(t)
	app := &App{}
	api := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		api.HandleFunc(route.Pattern, route.Handler)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/calendar/_install/1/", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/calendar/_install/1")
		r.Header.Set("X-Apteva-Project-ID", "test-proj")
		api.ServeHTTP(w, r)
	})
	mux.Handle("/", http.FileServer(http.Dir("browser/dist")))
	listener, err := net.Listen("tcp", "127.0.0.1:5319")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
