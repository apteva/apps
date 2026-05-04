package main

// Tier 1 — every HTTP handler and MCP tool exercised against an
// in-memory SQLite, real argon2id hashing, real EdDSA signing. The
// only thing mocked is the HTTP transport (httptest.NewRecorder).
// Fast (whole suite ~1s); runs on every commit.
//
// Tier 2 (real binary, real HTTP via tk.SpawnSidecar) lives in
// integration_test.go behind //go:build integration.
// Tier 3 (live agent + real LLM) lives in scenarios/*.yaml.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// newAuthCtx returns a ready-to-use AppCtx with:
//   - migrations applied
//   - APTEVA_PROJECT_ID = "test-proj"
//   - email_verification_required = false (so signup auto-logs-in)
//   - one signing key seeded
//   - one client registered
//   - globalCtx wired up so HTTP handlers find their AppCtx
//
// Returns the ctx and the test client_id.
func newAuthCtx(t *testing.T) (*sdk.AppCtx, string) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"email_verification_required": "false",
			"app_url":                     "http://localhost:8080",
		}))
	if err := ensureSigningKey(ctx.AppDB(), "test-proj"); err != nil {
		t.Fatalf("seed signing key: %v", err)
	}
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil })

	app := &App{}
	out, err := app.toolClientsCreate(ctx, map[string]any{
		"name":          "test-client",
		"type":          "spa",
		"redirect_uris": []any{"http://localhost:3000/callback"},
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	clientID, _ := out.(map[string]any)["client_id"].(string)
	if clientID == "" {
		t.Fatal("client_id empty after create")
	}
	return ctx, clientID
}

func TestGoldenPath_SignupLoginRefreshMeLogout(t *testing.T) {
	_, clientID := newAuthCtx(t)
	app := &App{}

	// ─── Signup ──────────────────────────────────────────────────
	body := map[string]any{
		"email":        "alice@example.com",
		"password":     "VerySafe!Pw#12345",
		"display_name": "Alice",
		"client_id":    clientID,
	}
	rec := callJSON(app.handleSignup, "POST", "/signup", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup status=%d body=%s", rec.Code, rec.Body.String())
	}
	signupResp := decode(t, rec)
	access1, _ := signupResp["access_token"].(string)
	refresh1, _ := signupResp["refresh_token"].(string)
	if access1 == "" || refresh1 == "" {
		t.Fatalf("signup did not return tokens: %v", signupResp)
	}

	// ─── /me with the access token ───────────────────────────────
	rec = call(app.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+access1)
	if rec.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%s", rec.Code, rec.Body.String())
	}
	meResp := decode(t, rec)
	user := meResp["user"].(map[string]any)
	if user["email"] != "alice@example.com" {
		t.Errorf("me.user.email = %v", user["email"])
	}

	// ─── /login with the same credentials ────────────────────────
	rec = callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email":     "alice@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", rec.Code, rec.Body.String())
	}
	loginResp := decode(t, rec)
	access2, _ := loginResp["access_token"].(string)
	refresh2, _ := loginResp["refresh_token"].(string)
	if access2 == "" || refresh2 == "" {
		t.Fatalf("login did not return tokens")
	}
	if refresh2 == refresh1 {
		t.Error("login returned the same refresh token as signup — should be distinct")
	}

	// ─── /refresh rotates the refresh token ──────────────────────
	rec = callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{
		"refresh_token": refresh2,
		"client_id":     clientID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rec.Code, rec.Body.String())
	}
	refreshResp := decode(t, rec)
	refresh3, _ := refreshResp["refresh_token"].(string)
	if refresh3 == "" || refresh3 == refresh2 {
		t.Errorf("refresh did not rotate: old=%q new=%q", refresh2, refresh3)
	}

	// ─── Replay of the rotated refresh token must be rejected ────
	rec = callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{
		"refresh_token": refresh2,
		"client_id":     clientID,
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("replay of rotated refresh expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	// ─── /logout revokes ─────────────────────────────────────────
	rec = callJSON(app.handleLogout, "POST", "/logout", map[string]any{
		"refresh_token": refresh3,
	})
	if rec.Code != http.StatusNoContent {
		t.Errorf("logout status=%d", rec.Code)
	}
	rec = callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{
		"refresh_token": refresh3,
		"client_id":     clientID,
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("refresh after logout expected 401, got %d", rec.Code)
	}
}

func TestLogin_BadPasswordIs401(t *testing.T) {
	_, clientID := newAuthCtx(t)
	app := &App{}
	// Signup first (auto-logs-in).
	rec := callJSON(app.handleSignup, "POST", "/signup", map[string]any{
		"email":     "bob@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", rec.Code, rec.Body.String())
	}
	rec = callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email":     "bob@example.com",
		"password":  "WRONG-password!1",
		"client_id": clientID,
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on bad password, got %d", rec.Code)
	}
}

func TestSignup_ConflictOnDuplicateEmail(t *testing.T) {
	_, clientID := newAuthCtx(t)
	app := &App{}
	for i, code := range []int{http.StatusCreated, http.StatusConflict} {
		rec := callJSON(app.handleSignup, "POST", "/signup", map[string]any{
			"email":     "carol@example.com",
			"password":  "VerySafe!Pw#12345",
			"client_id": clientID,
		})
		if rec.Code != code {
			t.Errorf("attempt %d: status=%d, want %d, body=%s",
				i+1, rec.Code, code, rec.Body.String())
		}
	}
}

func TestJWKS_PublishesActiveKey(t *testing.T) {
	_, _ = newAuthCtx(t)
	app := &App{}
	rec := call(app.handleJWKS, "GET", "/.well-known/jwks.json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("jwks status=%d", rec.Code)
	}
	body := decode(t, rec)
	keys, _ := body["keys"].([]any)
	if len(keys) < 1 {
		t.Fatalf("expected at least one key, got %d", len(keys))
	}
	k := keys[0].(map[string]any)
	if k["kty"] != "OKP" || k["crv"] != "Ed25519" || k["alg"] != "EdDSA" {
		t.Errorf("unexpected key fields: %+v", k)
	}
}

func TestMCP_StatsAndAudit(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}
	// Drive a signup + login to seed audit + stats.
	_ = callJSON(app.handleSignup, "POST", "/signup", map[string]any{
		"email":     "dan@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	})
	_ = callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email":     "dan@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	})

	statsOut, err := app.toolStats(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	stats := statsOut.(Stats)
	if stats.Active < 1 {
		t.Errorf("stats.Active = %d, want >=1", stats.Active)
	}
	if stats.Logins24h < 1 {
		t.Errorf("stats.Logins24h = %d, want >=1", stats.Logins24h)
	}

	auditOut, err := app.toolAuditSearch(ctx, map[string]any{"event": "login"})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	events := auditOut.(map[string]any)["events"].([]AuditEvent)
	if len(events) < 1 {
		t.Errorf("audit search returned %d login events, want >=1", len(events))
	}
}

// ─── HTTP test helpers ───────────────────────────────────────────────

func callJSON(h http.HandlerFunc, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func call(h http.HandlerFunc, method, path string, body any, headerKV ...string) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rdr)
	for i := 0; i+1 < len(headerKV); i += 2 {
		r.Header.Set(headerKV[i], headerKV[i+1])
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(strings.NewReader(rec.Body.String())).Decode(&out); err != nil {
		t.Fatalf("decode body=%s: %v", rec.Body.String(), err)
	}
	return out
}
