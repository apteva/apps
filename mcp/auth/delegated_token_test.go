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
	ProjectID      string   `json:"project_id"`
	SubjectType    string   `json:"subject_type"`
	SubjectID      string   `json:"subject_id"`
	SubjectEmail   string   `json:"subject_email"`
	OrganizationID string   `json:"organization_id"`
	AllowedOrigins []string `json:"allowed_origins"`
	Scopes         []struct {
		Type     string   `json:"type"`
		App      string   `json:"app"`
		Actions  []string `json:"actions"`
		AgentIDs []int64  `json:"agent_ids"`
	} `json:"scopes"`
}

func TestDelegatedTokenUsesClientOriginsAndLeastPrivilegeChatScope(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	ctx.Config()["apteva_chat_agent_ids"] = "566, 42,566"
	ctx.Config()["apteva_token_ttl_seconds"] = "1800"
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
			"access_token": "uk_delegated_test",
			"token_type":   "Bearer",
			"expires_in":   1800,
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
	if loginBody["apteva_access_token"] != "uk_delegated_test" {
		t.Fatalf("login delegated token = %v", loginBody["apteva_access_token"])
	}

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
		if !reflect.DeepEqual(req.AllowedOrigins, []string{"https://app.example.com", "http://localhost:3000"}) {
			t.Errorf("request %d allowed_origins=%v", i, req.AllowedOrigins)
		}
		if len(req.Scopes) != 1 {
			t.Fatalf("request %d scopes=%v", i, req.Scopes)
		}
		scope := req.Scopes[0]
		if scope.Type != "app_user" || scope.App != "channel-chat" {
			t.Errorf("request %d scope type/app=%q/%q", i, scope.Type, scope.App)
		}
		if !reflect.DeepEqual(scope.Actions, delegatedChatActions) {
			t.Errorf("request %d actions=%v", i, scope.Actions)
		}
		if !reflect.DeepEqual(scope.AgentIDs, []int64{566, 42}) {
			t.Errorf("request %d agent_ids=%v", i, scope.AgentIDs)
		}
		for _, action := range scope.Actions {
			if action == "*" {
				t.Errorf("request %d contains wildcard action", i)
			}
		}
	}
}

func TestDelegatedTokenIsOmittedWhenPolicyIsNotConfigured(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	client, err := dbGetClientByClientID(ctx.AppDB(), "test-proj", clientID)
	if err != nil {
		t.Fatal(err)
	}
	client.AllowedOrigins = []string{"https://app.example.com"}

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("APTEVA_APP_TOKEN", "app-test-token")
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)

	token, err := mintAptevaDelegatedToken(ctx, "test-proj", &Organization{ID: 1, Slug: "default"}, &User{ID: 2}, client)
	if err != nil {
		t.Fatal(err)
	}
	if token != nil || called {
		t.Fatalf("unconfigured delegation minted token=%v called=%v", token, called)
	}
}

func TestDelegatedTokenFailsClosedForMissingOriginsOrInvalidAgentIDs(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	client, err := dbGetClientByClientID(ctx.AppDB(), "test-proj", clientID)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("APTEVA_APP_TOKEN", "app-test-token")
	ctx.Config()["apteva_chat_agent_ids"] = "566"

	token, err := mintAptevaDelegatedToken(ctx, "test-proj", &Organization{ID: 1}, &User{ID: 2}, client)
	if err != nil || token != nil {
		t.Fatalf("missing origins: token=%v err=%v", token, err)
	}

	client.AllowedOrigins = []string{"https://app.example.com"}
	ctx.Config()["apteva_chat_agent_ids"] = "566,not-an-id"
	if token, err = mintAptevaDelegatedToken(ctx, "test-proj", &Organization{ID: 1}, &User{ID: 2}, client); err == nil || token != nil {
		t.Fatalf("invalid agent IDs: token=%v err=%v", token, err)
	}
}
