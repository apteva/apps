package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

type capturedDelegatedMint struct {
	ProjectID       string          `json:"project_id"`
	OAuthClientID   string          `json:"oauth_client_id"`
	SubjectType     string          `json:"subject_type"`
	SubjectID       string          `json:"subject_id"`
	SubjectEmail    string          `json:"subject_email"`
	OrganizationID  string          `json:"organization_id"`
	AllowedOrigins  []string        `json:"allowed_origins"`
	CallerScopes    json.RawMessage `json:"scopes"`
	CallerExpiresIn json.RawMessage `json:"expires_in"`
}

func TestDelegatedTokenSendsOnlyIdentityOAuthClientAndOrigins(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	if _, err := (&App{}).toolClientsUpdate(ctx, map[string]any{
		"client_id":           clientID,
		"add_allowed_origins": []any{"https://app.example.com", "http://localhost:3000"},
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	captured := make([]capturedDelegatedMint, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/delegated-keys/mint" {
			t.Errorf("callback path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer app-test-token" {
			t.Errorf("Authorization = %q", got)
		}
		var body capturedDelegatedMint
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode callback body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		captured = append(captured, body)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":    "uk_delegated_test",
			"token_type":      "Bearer",
			"expires_in":      900,
			"oauth_client_id": body.OAuthClientID,
		})
	}))
	defer server.Close()
	t.Setenv("APTEVA_APP_TOKEN", "app-test-token")
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)

	app := &App{}
	origin := "https://app.example.com"
	signup := call(app.handleSignup, http.MethodPost, "/signup", map[string]any{
		"email": "delegated@example.com", "password": "VerySafe!Pw#12345", "client_id": clientID,
	}, "Origin", origin, "Content-Type", "application/json")
	if signup.Code != http.StatusCreated {
		t.Fatalf("signup status=%d body=%s", signup.Code, signup.Body.String())
	}
	signupBody := decode(t, signup)
	if signupBody["apteva_access_token"] != "uk_delegated_test" {
		t.Fatalf("signup delegated token = %v", signupBody["apteva_access_token"])
	}
	authorization := signupBody["authorization"].(map[string]any)
	if len(authorization["roles"].([]any)) != 0 || len(authorization["permissions"].([]any)) != 0 {
		t.Fatalf("new user unexpectedly has RBAC grants: %v", authorization)
	}

	login := call(app.handleLogin, http.MethodPost, "/login", map[string]any{
		"email": "delegated@example.com", "password": "VerySafe!Pw#12345", "client_id": clientID,
	}, "Origin", origin, "Content-Type", "application/json")
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	loginBody := decode(t, login)
	refresh := call(app.handleRefresh, http.MethodPost, "/refresh", map[string]any{
		"refresh_token": loginBody["refresh_token"], "client_id": clientID,
	}, "Origin", origin, "Content-Type", "application/json")
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", refresh.Code, refresh.Body.String())
	}
	if got := decode(t, refresh)["apteva_access_token"]; got != "uk_delegated_test" {
		t.Fatalf("refresh delegated token = %v", got)
	}

	mu.Lock()
	requests := append([]capturedDelegatedMint(nil), captured...)
	mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("delegated callback count=%d, want 3", len(requests))
	}
	for i, req := range requests {
		if req.OAuthClientID != clientID {
			t.Errorf("request %d oauth_client_id=%q", i, req.OAuthClientID)
		}
		if !reflect.DeepEqual(req.AllowedOrigins, []string{"https://app.example.com", "http://localhost:3000"}) {
			t.Errorf("request %d allowed_origins=%v", i, req.AllowedOrigins)
		}
		if len(req.CallerScopes) != 0 || len(req.CallerExpiresIn) != 0 {
			t.Errorf("request %d improperly supplied policy: scopes=%s expires_in=%s", i, req.CallerScopes, req.CallerExpiresIn)
		}
	}
}

func TestDelegatedTokenIsOmittedWithoutClientOrigins(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	client, err := dbGetClientByClientID(ctx.AppDB(), "test-proj", clientID)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("APTEVA_APP_TOKEN", "app-test-token")
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	token, err := mintAptevaDelegatedToken("test-proj", &Organization{ID: 1, Slug: "default"}, &User{ID: 2}, client)
	if err != nil || token != nil || called {
		t.Fatalf("missing origins: token=%v called=%v err=%v", token, called, err)
	}
}

func TestAuthSessionSurvivesMissingServerDelegationPolicy(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	if _, err := (&App{}).toolClientsUpdate(ctx, map[string]any{
		"client_id": clientID, "add_allowed_origins": []any{"https://app.example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "delegated access policy not found", http.StatusForbidden)
	}))
	defer server.Close()
	t.Setenv("APTEVA_APP_TOKEN", "app-test-token")
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	rec := call((&App{}).handleSignup, http.MethodPost, "/signup", map[string]any{
		"email": "no-policy@example.com", "password": "VerySafe!Pw#12345", "client_id": clientID,
	}, "Origin", "https://app.example.com", "Content-Type", "application/json")
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["access_token"] == "" || body["refresh_token"] == "" {
		t.Fatalf("identity tokens missing after policy rejection: %v", body)
	}
	if _, exists := body["apteva_access_token"]; exists {
		t.Fatalf("delegated token returned without policy: %v", body)
	}
}

func TestDelegatedTokenRejectsLegacyServerResponseWithoutPolicyBinding(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	client, err := dbGetClientByClientID(ctx.AppDB(), "test-proj", clientID)
	if err != nil {
		t.Fatal(err)
	}
	client.AllowedOrigins = []string{"https://app.example.com"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "uk_legacy_unbound",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer server.Close()
	t.Setenv("APTEVA_APP_TOKEN", "app-test-token")
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	token, err := mintAptevaDelegatedToken("test-proj", &Organization{ID: 1, Slug: "default"}, &User{ID: 2}, client)
	if err == nil || token != nil {
		t.Fatalf("legacy server response accepted: token=%v err=%v", token, err)
	}
}
