package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Optional cross-repository contract check; uses real Auth and the Web SDK,
// with a fixture platform mint API and protected app endpoint. No live accounts.
func TestWebSDKSessionIntegration(t *testing.T) {
	dir := os.Getenv("AUTH_WEB_SDK_TEST_DIR")
	if dir == "" {
		t.Skip("set AUTH_WEB_SDK_TEST_DIR to a Web SDK checkout")
	}
	ctx, cid := newAuthCtx(t)
	a := &App{}
	signup := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"email": "sdk@example.com", "password": "GoodPassword123", "client_id": cid}))
	uid := int64(signup["user"].(map[string]any)["id"].(float64))
	oid := dbDefaultOrgID(ctx.AppDB(), "test-proj")
	role, err := dbCreateRole(ctx.AppDB(), "test-proj", oid, "commercial", "Commercial", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dbSetUserRoles(ctx.AppDB(), "test-proj", oid, uid, []int64{role.ID}); err != nil {
		t.Fatal(err)
	}
	ctx.Config()["delegated_token_bindings"] = fmt.Sprintf(`[{"project_id":"test-proj","organization_slug":"default","client_id":%q,"profile":"commercial","policy_client_id":"commercial-policy","roles":["commercial"]}]`, cid)
	var mu sync.Mutex
	issued := map[string]time.Time{}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/apps/callback/delegated-keys/mint":
			if r.Header.Get("Authorization") != "Bearer fixture" {
				w.WriteHeader(401)
				return
			}
			var b map[string]any
			if json.NewDecoder(r.Body).Decode(&b) != nil || b["oauth_client_id"] != "commercial-policy" || b["project_id"] != "test-proj" || b["subject_id"] != uintToStr(uid) {
				w.WriteHeader(403)
				return
			}
			mu.Lock()
			token := fmt.Sprintf("uk_fixture_%d", len(issued))
			expiry := time.Now().UTC().Add(60 * time.Second)
			issued[token] = expiry
			mu.Unlock()
			httpJSON(w, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 60, "expires_at": expiry.Format(time.RFC3339), "project_id": "test-proj", "oauth_client_id": "commercial-policy", "subject": map[string]any{"type": "user", "id": uintToStr(uid), "organization_id": uintToStr(oid), "organization_slug": "default"}})
		case strings.HasPrefix(r.URL.Path, "/api/apps/auth/"):
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/auth")
			switch r.URL.Path {
			case "/login":
				a.handleLogin(w, r)
			case "/refresh":
				a.handleRefresh(w, r)
			case "/delegated-token":
				a.handleDelegatedToken(w, r)
			case "/logout":
				a.handleLogout(w, r)
			case "/me":
				a.handleMe(w, r)
			default:
				w.WriteHeader(404)
			}
		case r.URL.Path == "/fixture/remove-role":
			if _, err := dbSetUserRoles(ctx.AppDB(), "test-proj", oid, uid, nil); err != nil {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
		case strings.HasPrefix(r.URL.Path, "/api/apps/conversations/"):
			mu.Lock()
			expiry := issued[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
			mu.Unlock()
			if !expiry.After(time.Now()) {
				w.WriteHeader(401)
				return
			}
			httpJSON(w, map[string]any{"ok": true})
		default:
			w.WriteHeader(404)
		}
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_APP_TOKEN", "fixture")
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	cmd := exec.Command("bun", filepath.Join(dir, "tests/auth-live.ts"))
	cmd.Env = append(os.Environ(), "AUTH_INTEGRATION_URL="+gateway.URL, "AUTH_INTEGRATION_CLIENT="+cid)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SDK integration: %v\n%s", err, out)
	}
	t.Log(string(out))
}
