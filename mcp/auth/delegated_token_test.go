package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestIndependentDelegatedTokensAreNotIssued(t *testing.T) {
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
