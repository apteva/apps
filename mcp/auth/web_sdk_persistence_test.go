package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Browser storage and Web Locks run in Chromium; all Auth handlers and rotating
// credentials are real. Only temporary fixture accounts/databases are used.
func TestWebSDKPersistentBrowserIntegration(t *testing.T) {
	dir := os.Getenv("AUTH_WEB_SDK_TEST_DIR")
	if dir == "" {
		t.Skip("set AUTH_WEB_SDK_TEST_DIR to a Web SDK checkout with Playwright Chromium installed")
	}
	ctx, cid := newAuthCtx(t)
	app := &App{}
	if _, err := ctx.AppDB().Exec(`UPDATE clients SET allowed_origins='["*"]' WHERE client_id=?`, cid); err != nil {
		t.Fatal(err)
	}
	signup := callJSON(app.handleSignup, "POST", "/signup", map[string]any{"email": "browser@example.com", "password": "GoodPassword123", "client_id": cid})
	if signup.Code != 200 && signup.Code != 201 {
		t.Fatalf("signup: %d %s", signup.Code, signup.Body)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture/refresh-outage" {
			var err error
			if r.Method == "POST" {
				_, err = ctx.AppDB().Exec(`CREATE TRIGGER fixture_refresh_outage BEFORE UPDATE OF revoked_at ON sessions BEGIN SELECT RAISE(ABORT,'fixture unavailable'); END`)
			} else {
				_, err = ctx.AppDB().Exec(`DROP TRIGGER fixture_refresh_outage`)
			}
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.WriteHeader(204)
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/auth")
		switch r.URL.Path {
		case "/login":
			app.handleLogin(w, r)
		case "/refresh":
			app.handleRefresh(w, r)
		case "/logout":
			app.handleLogout(w, r)
		case "/me":
			app.handleMe(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	cmd := exec.Command("bun", filepath.Join(dir, "tests/browser-auth-live.ts"))
	cmd.Env = append(os.Environ(), "AUTH_INTEGRATION_URL="+gateway.URL, "AUTH_INTEGRATION_CLIENT="+cid)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser Auth integration: %v\n%s", err, out)
	}
	t.Log(string(out))
}
