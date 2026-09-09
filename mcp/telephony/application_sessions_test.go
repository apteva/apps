package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestApplicationSessionOnlineAuthAndRevocation(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	policy := phoneTestPolicy(t, app)
	phoneTestCall(t, app, "incoming", "pending")
	var revoked atomic.Bool
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if revoked.Load() || r.Header.Get("Authorization") != "Bearer verified-login-session" {
			http.Error(w, "invalid session", 401)
			return
		}
		writeJSON(w, map[string]any{"user": map[string]any{"id": "alice", "organization_id": "org-1", "project_id": "project-a"}})
	}))
	defer provider.Close()
	policy.Providers = []phoneAuthProvider{{ID: "login", IssuerApp: "auth", IssuerInstallID: "11", URL: provider.URL, Format: "apteva-auth", Actions: []string{"call.read", "call.answer", "call.attach"}}}
	if w := phoneTestRequest(app, nil, "PUT", "/access/policy", policy); w.Code != 200 {
		t.Fatal(w.Body)
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer verified-login-session")
		r.Header.Set("X-Apteva-Subject-ID", "boss")
		w := httptest.NewRecorder()
		app.handleApplicationSession(w, r)
		return w
	}
	w := request("/user/calls?auth_provider=login")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "incoming") {
		t.Fatalf("online login: %d %s", w.Code, w.Body)
	}
	w = request("/user/softphone/access?auth_provider=login")
	if w.Code != 200 || strings.Contains(w.Body.String(), "boss") || !strings.Contains(w.Body.String(), "alice") {
		t.Fatalf("forged identity accepted: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/user/access/policy?auth_provider=login", "/user/calls?auth_provider=unknown", "/user/calls?auth_provider=login&project_id=other"} {
		if w := request(path); w.Code < 400 {
			t.Fatalf("unauthorized path %s", path)
		}
	}
	revoked.Store(true)
	if w := request("/user/calls?auth_provider=login"); w.Code != 401 {
		t.Fatalf("revoked Auth session accepted: %d", w.Code)
	}
	if requests.Load() != 3 {
		t.Fatalf("expected online validation for each valid route, got %d", requests.Load())
	}
}
func TestApplicationSessionProviderRedirectAndProjectDenied(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	policy := phoneTestPolicy(t, app)
	var leak atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leak.Store(true) }))
	defer destination.Close()
	var redirect atomic.Bool
	redirect.Store(true)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect.Load() {
			http.Redirect(w, r, destination.URL, 302)
			return
		}
		writeJSON(w, map[string]any{"user": map[string]any{"id": "alice", "organization_id": "org-1", "project_id": "other"}})
	}))
	defer provider.Close()
	policy.Providers = []phoneAuthProvider{{ID: "login", IssuerApp: "auth", IssuerInstallID: "11", URL: provider.URL, Format: "apteva-auth", Actions: []string{"call.read"}}}
	if w := phoneTestRequest(app, nil, "PUT", "/access/policy", policy); w.Code != 200 {
		t.Fatal(w.Body)
	}
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", "/user/calls?auth_provider=login", nil)
		r.Header.Set("Authorization", "Bearer verified-login-session")
		w := httptest.NewRecorder()
		app.handleApplicationSession(w, r)
		if w.Code < 400 {
			t.Fatal("redirect/cross-project identity accepted")
		}
		redirect.Store(false)
	}
	if leak.Load() {
		t.Fatal("bearer sent to redirect destination")
	}
	// Policy responses contain no bearer tokens or independent user credentials.
	w := phoneTestRequest(app, nil, "GET", "/access/policy", nil)
	var value map[string]any
	if json.Unmarshal(w.Body.Bytes(), &value) != nil {
		t.Fatal(w.Body)
	}
}
