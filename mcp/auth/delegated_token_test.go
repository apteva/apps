package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDelegatedTokensDisabledWithoutBindings(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	t.Setenv("APTEVA_APP_TOKEN", "fixture")
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	_, err := (&App{}).toolClientsUpdate(ctx, map[string]any{"client_id": cid, "add_allowed_origins": []string{"https://app.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{}
	signup := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"email": "delegated@example.com", "password": "GoodPassword123", "client_id": cid}))
	login := decode(t, callJSON(a.handleLogin, "POST", "/login", map[string]any{"email": "delegated@example.com", "password": "GoodPassword123", "client_id": cid}))
	refresh := decode(t, callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": login["refresh_token"]}))
	identity := loginIdentity(t, a, ctx, cid, "device", "delegate-test", nil)
	for _, out := range []map[string]any{signup, login, refresh, identity} {
		if out["access_token"] == nil {
			t.Fatalf("missing Auth access token: %v", out)
		}
		if _, ok := out["apteva_access_token"]; ok {
			t.Fatal("independent delegated token returned")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsafe platform mint was called")
	}
}

func TestDelegatedSessionLifecycle(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	signup := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"email": "live@example.com", "password": "GoodPassword123", "client_id": cid}))
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
	var calls atomic.Int32
	var ttl atomic.Int32
	ttl.Store(60)
	var incorrect atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/apps/callback/delegated-keys/mint" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("wrong gateway request")
		}
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
			return
		}
		if b["oauth_client_id"] != "commercial-policy" || b["project_id"] != "test-proj" || b["subject_id"] != uintToStr(uid) || b["scopes"] != nil {
			t.Error("identity or policy override")
		}
		seconds := int(ttl.Load())
		project := "test-proj"
		if incorrect.Load() {
			project = "other"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "uk_fixture", "token_type": "Bearer", "expires_in": seconds, "expires_at": time.Now().UTC().Add(time.Duration(seconds) * time.Second).Format(time.RFC3339), "project_id": project, "oauth_client_id": "commercial-policy", "subject": map[string]any{"type": "user", "id": uintToStr(uid), "organization_id": uintToStr(oid), "organization_slug": "default"}})
	}))
	defer server.Close()
	t.Setenv("APTEVA_APP_TOKEN", "fixture")
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	login := func() map[string]any {
		t.Helper()
		return decode(t, callJSON(a.handleLogin, "POST", "/login", map[string]any{"client_id": cid, "email": "live@example.com", "password": "GoodPassword123"}))
	}
	session := login()
	if session["apteva_access_token"] != "uk_fixture" || session["apteva_expires_at"] == nil {
		t.Fatal("missing platform credential")
	}
	renew := func(token, path string) *httptest.ResponseRecorder {
		return call(a.handleDelegatedToken, "POST", path, nil, "Authorization", "Bearer "+token)
	}
	access := session["access_token"].(string)
	if r := renew(access, "/delegated-token"); r.Code != 200 {
		t.Fatalf("renew: %d %s", r.Code, r.Body.String())
	}
	before := calls.Load()
	if r := renew(access, "/delegated-token?delegated_profile=admin"); r.Code != 403 || calls.Load() != before {
		t.Fatal("profile escalation")
	}
	if r := call(a.handleDelegatedToken, "POST", "/delegated-token", nil, "Authorization", "Bearer "+access, "Origin", "https://evil.test"); r.Code != 403 || calls.Load() != before {
		t.Fatal("origin escalation")
	}
	ttl.Store(3600)
	if r := renew(access, "/delegated-token"); r.Code != 503 || strings.Contains(r.Body.String(), "uk_fixture") {
		t.Fatal("accepted policy lifetime override")
	}
	ttl.Store(60)
	incorrect.Store(true)
	if r := renew(access, "/delegated-token"); r.Code != 503 {
		t.Fatal("accepted wrong project response")
	}
	incorrect.Store(false)
	refreshed := decode(t, callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": session["refresh_token"]}))
	if refreshed["apteva_access_token"] != "uk_fixture" {
		t.Fatal("refresh did not mint")
	}
	if _, err = dbSetUserRoles(ctx.AppDB(), "test-proj", oid, uid, nil); err != nil {
		t.Fatal(err)
	}
	before = calls.Load()
	if r := renew(access, "/delegated-token"); r.Code != 401 || calls.Load() != before {
		t.Fatal("stale authorization minted")
	}
	denied := decode(t, callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": refreshed["refresh_token"]}))
	if denied["access_token"] == nil || denied["apteva_access_token"] != nil || calls.Load() != before {
		t.Fatal("removed role still minted")
	}
	if _, err = dbSetUserRoles(ctx.AppDB(), "test-proj", oid, uid, []int64{role.ID}); err != nil {
		t.Fatal(err)
	}
	session = login()
	access = session["access_token"].(string)
	callJSON(a.handleLogout, "POST", "/logout", map[string]any{"refresh_token": session["refresh_token"]})
	before = calls.Load()
	if r := renew(access, "/delegated-token"); r.Code != 401 || calls.Load() != before {
		t.Fatal("logout still minted")
	}
	session = login()
	access = session["access_token"].(string)
	if err = dbSetUserStatus(ctx.AppDB(), "test-proj", oid, uid, "disabled"); err != nil {
		t.Fatal(err)
	}
	before = calls.Load()
	if r := renew(access, "/delegated-token"); r.Code != 401 || calls.Load() != before {
		t.Fatal("disabled account minted")
	}
}
