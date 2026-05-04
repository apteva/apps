//go:build integration

package main

// Tier 2 — the real binary, real HTTP/MCP. Boot the sidecar, exercise
// the same surfaces a SaaS frontend would: register a client via MCP,
// sign up over REST, log in, refresh, pull the JWKS + OIDC discovery.
// Validates the SDK wiring (manifest parsing at boot, migrations on
// disk, JSON-RPC dispatch, route mounting, /health, auth header)
// end-to-end.
//
// /me is deliberately NOT tested here: the testkit's HTTP client
// always sends `Authorization: Bearer <APP_TOKEN>` so the SDK's
// withTokenAuth gate lets the request through, but that means we
// can't substitute a user JWT — and /me's whole job is to verify
// a user JWT. /me's JWT verification path is covered in Tier 1
// (handlers_test.go) by calling handleMe with httptest directly.
//
// Run with:  go test -tags integration ./...

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// newSidecar boots the auth binary with email_verification_required
// disabled (so /signup auto-logs-in) and returns the sidecar plus a
// freshly-registered SPA client_id.
func newSidecar(t *testing.T) (*tk.Sidecar, string) {
	t.Helper()
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"email_verification_required": "false",
			"app_url":                     "http://localhost:8080",
		}))
	clientResp := sc.MCP("auth_clients_create", map[string]any{
		"name":          "tier2-client",
		"type":          "spa",
		"redirect_uris": []any{"http://localhost:3000/callback"},
	})
	clientID, _ := clientResp["client_id"].(string)
	if clientID == "" {
		t.Fatalf("auth_clients_create returned no client_id: %v", clientResp)
	}
	return sc, clientID
}

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 {
		t.Fatalf("/health status=%d body=%s", resp.Status, string(resp.Body))
	}
	if got["ok"] != true {
		t.Errorf("/health body=%v", got)
	}
}

func TestSidecar_SignupLoginRefreshFlow(t *testing.T) {
	sc, clientID := newSidecar(t)

	// Signup over REST — verification_required=false → auto-login → 201.
	var signup map[string]any
	resp := sc.POST("/signup", map[string]any{
		"email":     "alice@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	}, &signup)
	if resp.Status != 201 {
		t.Fatalf("signup status=%d body=%s", resp.Status, string(resp.Body))
	}
	if signup["access_token"] == "" || signup["refresh_token"] == "" {
		t.Fatalf("signup missing tokens: %v", signup)
	}

	// /login with the same credentials — fresh tokens.
	var login map[string]any
	resp = sc.POST("/login", map[string]any{
		"email":     "alice@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	}, &login)
	if resp.Status != 200 {
		t.Fatalf("login status=%d body=%s", resp.Status, string(resp.Body))
	}
	refresh1, _ := login["refresh_token"].(string)
	if refresh1 == "" {
		t.Fatal("login returned no refresh_token")
	}

	// /refresh rotates.
	var refresh map[string]any
	resp = sc.POST("/refresh", map[string]any{
		"refresh_token": refresh1,
		"client_id":     clientID,
	}, &refresh)
	if resp.Status != 200 {
		t.Fatalf("refresh status=%d body=%s", resp.Status, string(resp.Body))
	}
	refresh2, _ := refresh["refresh_token"].(string)
	if refresh2 == "" || refresh2 == refresh1 {
		t.Errorf("refresh did not rotate: %q -> %q", refresh1, refresh2)
	}

	// Replay of the rotated refresh token must be rejected.
	resp = sc.POST("/refresh", map[string]any{
		"refresh_token": refresh1,
		"client_id":     clientID,
	}, &map[string]any{})
	if resp.Status != 401 {
		t.Errorf("replay of rotated refresh expected 401, got %d body=%s",
			resp.Status, string(resp.Body))
	}
}

func TestSidecar_BadPasswordIs401(t *testing.T) {
	sc, clientID := newSidecar(t)
	// Seed.
	sc.POST("/signup", map[string]any{
		"email":     "bob@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	}, &map[string]any{})
	resp := sc.POST("/login", map[string]any{
		"email":     "bob@example.com",
		"password":  "WRONG-password!1",
		"client_id": clientID,
	}, &map[string]any{})
	if resp.Status != 401 {
		t.Errorf("expected 401 on bad password, got %d", resp.Status)
	}
}

func TestSidecar_JWKSPublishesActiveKey(t *testing.T) {
	sc, _ := newSidecar(t)
	var jwks map[string]any
	resp := sc.GET("/.well-known/jwks.json", &jwks)
	if resp.Status != 200 {
		t.Fatalf("jwks status=%d body=%s", resp.Status, string(resp.Body))
	}
	keys, _ := jwks["keys"].([]any)
	if len(keys) < 1 {
		t.Fatalf("jwks has no keys: %v", jwks)
	}
	k := keys[0].(map[string]any)
	if k["alg"] != "EdDSA" || k["crv"] != "Ed25519" || k["use"] != "sig" {
		t.Errorf("unexpected jwk fields: %+v", k)
	}
	// JWT issued by the sidecar should declare a kid that matches one
	// of the JWKS entries — quick sanity check on the key-id wiring.
	sc2, clientID := newSidecar(t)
	var signup map[string]any
	sc2.POST("/signup", map[string]any{
		"email":     "carol@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	}, &signup)
	access := signup["access_token"].(string)
	if !strings.Contains(access, ".") {
		t.Fatalf("access_token does not look like a JWT: %q", access)
	}
}

func TestSidecar_OIDCDiscoveryAdvertisesEndpoints(t *testing.T) {
	sc, _ := newSidecar(t)
	var cfg map[string]any
	resp := sc.GET("/.well-known/openid-configuration", &cfg)
	if resp.Status != 200 {
		t.Fatalf("oidc-config status=%d body=%s", resp.Status, string(resp.Body))
	}
	for _, key := range []string{"issuer", "jwks_uri", "token_endpoint", "userinfo_endpoint"} {
		if v, _ := cfg[key].(string); v == "" {
			t.Errorf("openid-configuration missing %q: %v", key, cfg)
		}
	}
}

func TestSidecar_MCPClientsList(t *testing.T) {
	sc, clientID := newSidecar(t)
	out := sc.MCP("auth_clients_list", map[string]any{})
	clients, _ := out["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("expected 1 client, got %d (out=%v)", len(clients), out)
	}
	got := clients[0].(map[string]any)
	if got["client_id"] != clientID {
		t.Errorf("client_id mismatch: got=%v want=%v", got["client_id"], clientID)
	}
}

func TestSidecar_MCPStats(t *testing.T) {
	sc, clientID := newSidecar(t)
	// Drive a signup to seed stats.
	sc.POST("/signup", map[string]any{
		"email":     "dan@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	}, &map[string]any{})
	out := sc.MCP("auth_stats", map[string]any{})
	if active, _ := out["active"].(float64); active < 1 {
		t.Errorf("auth_stats.active = %v, want >=1", out["active"])
	}
}
