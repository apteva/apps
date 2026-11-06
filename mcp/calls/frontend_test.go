package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestFrontendManifestMatchesServedBundle(t *testing.T) {
	rec := httptest.NewRecorder()
	handleFrontendAsset(rec, httptest.NewRequest(http.MethodGet, "/ui/frontend.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("frontend manifest: %d", rec.Code)
	}
	var spec struct {
		Schema, App, Version string
		Client               struct{ Path, SHA256 string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Schema != "apteva-app-frontend/v1" || spec.App != "calls" || spec.Version != (&App{}).Manifest().Version {
		t.Fatalf("invalid manifest: %+v", spec)
	}
	bundle := httptest.NewRecorder()
	handleFrontendAsset(bundle, httptest.NewRequest(http.MethodGet, spec.Client.Path, nil))
	if bundle.Code != http.StatusOK || spec.Client.SHA256 != fmt.Sprintf("%x", sha256.Sum256(bundle.Body.Bytes())) {
		t.Fatalf("served bundle does not match manifest: %d", bundle.Code)
	}
}

func TestEmbedJoinOriginAndProject(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithConfig(map[string]string{
		"allowed_embed_origins": "https://app.example, https://other.example",
	}))
	app := &App{}
	globalCtx = ctx
	room := createRoom(t, app, ctx)
	out, err := app.toolCreateJoinToken(ctx, map[string]any{"room_id": room.ID})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"token": out.(map[string]any)["token"]})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("APTEVA_PROJECT_ID", "") // Exercise a global installation.
	for _, tc := range []struct {
		origin, project, contentType string
		status                       int
	}{
		{"https://evil.example", "test-proj", "application/json", http.StatusForbidden},
		{"https://app.example.attacker.example", "test-proj", "application/json", http.StatusForbidden},
		{"https://app.example", "test-proj", "application/x-www-form-urlencoded", http.StatusForbidden},
		{"https://app.example", "wrong-project", "application/json", http.StatusBadRequest},
		{"https://app.example", "test-proj", "application/json", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/join?project_id="+tc.project, strings.NewReader(string(body)))
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Content-Type", tc.contentType)
		rec := httptest.NewRecorder()
		app.handleAPIJoin(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s (%s): %d want %d", tc.origin, tc.project, rec.Code, tc.status)
		}
	}
}

func TestSharedCallsBrowser(t *testing.T) {
	if os.Getenv("CALLS_BROWSER_TEST") != "1" {
		t.Skip("set CALLS_BROWSER_TEST=1 to run the Chromium call")
	}
	bundle := filepath.Join(t.TempDir(), "host.js")
	build := exec.CommandContext(t.Context(), "bun", "build", "frontend/tests/host.tsx", "--target=browser", "--outfile", bundle)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build browser host: %v\n%s", err, output)
	}

	host := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/host.js" {
			http.ServeFile(w, r, bundle)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><div id="root"></div><script type="module" src="/host.js"></script>`)
	})
	external := httptest.NewServer(host)
	t.Cleanup(external.Close)
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithConfig(map[string]string{
		"allowed_embed_origins": external.URL,
		"ice_servers_json":      "[]",
	}))
	app := &App{}
	globalCtx = ctx
	room := createRoom(t, app, ctx)
	createInvite := func() map[string]any {
		out, err := app.toolCreateJoinToken(ctx, map[string]any{"room_id": room.ID, "role": "guest"})
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}
	hostedInvite, externalInvite := createInvite(), createInvite()

	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == external.URL {
			w.Header().Set("Access-Control-Allow-Origin", external.URL)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/apps/calls/") {
			http.StripPrefix("/api/apps/calls", mux).ServeHTTP(w, r)
			return
		}
		host.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	run := exec.CommandContext(t.Context(), "bun", "frontend/tests/browser.ts", server.URL, external.URL,
		strconv.FormatInt(room.ID, 10), hostedInvite["join_url"].(string), externalInvite["token"].(string))
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("browser call: %v\n%s", err, output)
	}
}
