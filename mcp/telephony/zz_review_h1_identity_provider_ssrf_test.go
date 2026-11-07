package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReviewH1IdentityProviderURLRestrictions(t *testing.T) {
	t.Setenv("APTEVA_GATEWAY_URL", "http://127.0.0.1:5280")
	base := phoneAuthProvider{ID: "p", IssuerApp: "auth", IssuerInstallID: "1", Format: "apteva-auth"}
	cases := map[string]bool{
		"https://auth.example.com/me":                       true,
		"http://127.0.0.1:5280/api/apps/auth/_install/1/me": true,
		"http://127.0.0.1:5281/me":                          false,
		"http://localhost/me":                               false,
		"https://localhost/me":                              false,
		"https://foo.localhost/me":                          false,
		"https://127.0.0.1/me":                              false,
		"https://[::1]/me":                                  false,
		"https://10.0.0.5/me":                               false,
		"https://192.168.1.1/me":                            false,
		"https://169.254.169.254/latest/meta-data":          false,
		"https://0.0.0.0/me":                                false,
		"http://auth.example.com/me":                        false,
	}
	for raw, want := range cases {
		p := base
		p.URL = raw
		if got := validPhoneProvider(p); got != want {
			t.Errorf("validPhoneProvider(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestReviewH1IdentityProviderDialRefusesInternalAddresses(t *testing.T) {
	t.Setenv("APTEVA_GATEWAY_URL", "")
	var hit bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer server.Close()
	// The dialer must refuse loopback even if a URL slipped past static
	// validation (e.g. a public name that later resolves to 127.0.0.1).
	u, _ := url.Parse(server.URL)
	req, _ := http.NewRequest("GET", u.String(), nil)
	if _, err := phoneProviderClient(req.URL).Do(req); err == nil || hit {
		t.Fatalf("expected dial refusal, err=%v hit=%v", err, hit)
	}
}

func TestReviewH1IdentityProviderIssuerAliasesAndConflicts(t *testing.T) {
	base := phoneAuthProvider{IssuerApp: "auth", IssuerInstallID: "38", URL: "https://auth.example.com/api/me", Format: "apteva-auth"}
	adviser, listening := base, base
	adviser.ID, listening.ID = "staging-auth", "staging-listening-auth"
	adviser.Actions = []string{"call.read", "call.answer", "call.attach", "call.dial"}
	listening.Actions = []string{"call.read", "call.listen", "call.coach"}
	if phoneProviderIssuerConflict(phonePolicy{Providers: []phoneAuthProvider{adviser, listening}}, adviser) {
		t.Fatal("alias of the identical endpoint flagged as conflicting")
	}
	for name, change := range map[string]func(*phoneAuthProvider){
		"path":   func(p *phoneAuthProvider) { p.URL = "https://auth.example.com/api/me2" },
		"query":  func(p *phoneAuthProvider) { p.URL = "https://auth.example.com/api/me?x=1" },
		"host":   func(p *phoneAuthProvider) { p.URL = "https://other.example.com/api/me" },
		"format": func(p *phoneAuthProvider) { p.Format = "oidc-userinfo" },
	} {
		other := listening
		change(&other)
		if !phoneProviderIssuerConflict(phonePolicy{Providers: []phoneAuthProvider{adviser, other}}, adviser) {
			t.Errorf("%s: a different endpoint for the same issuer was accepted", name)
		}
	}
	distinct := listening
	distinct.IssuerInstallID, distinct.URL = "39", "https://auth.example.com/other"
	if phoneProviderIssuerConflict(phonePolicy{Providers: []phoneAuthProvider{adviser, distinct}}, adviser) {
		t.Fatal("distinct issuers flagged as conflicting")
	}
}

// The staging shape: two provider IDs alias the same Auth endpoint with
// different action lists. Each alias authenticates on its own and keeps only
// its own actions.
func TestReviewH1IdentityProviderAliasesAuthenticateWithSeparateActions(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	policy := phoneTestPolicy(t, app)
	phoneTestCall(t, app, "incoming", "pending")
	var revoked atomic.Bool
	var requests atomic.Int32
	var during atomic.Pointer[func()]
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if hook := during.Load(); hook != nil {
			(*hook)()
		}
		if revoked.Load() || r.Header.Get("Authorization") != "Bearer verified-login-session" {
			http.Error(w, "invalid session", 401)
			return
		}
		writeJSON(w, map[string]any{"user": map[string]any{"id": "alice", "organization_id": "org-1", "project_id": "project-a"}})
	}))
	defer provider.Close()
	t.Setenv("APTEVA_GATEWAY_URL", provider.URL)
	policy.Providers = []phoneAuthProvider{
		{ID: "staging-auth", IssuerApp: "auth", IssuerInstallID: "11", URL: provider.URL, Format: "apteva-auth", Actions: []string{"call.read", "call.answer", "call.attach", "call.dial"}},
		{ID: "staging-listening-auth", IssuerApp: "auth", IssuerInstallID: "11", URL: provider.URL, Format: "apteva-auth", Actions: []string{"call.read", "call.listen", "call.coach"}},
	}
	w := phoneTestRequest(app, nil, "PUT", "/access/policy", policy)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &policy) != nil {
		t.Fatalf("compatible aliases rejected on save: %d %s", w.Code, w.Body)
	}
	saved := policy
	request := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer verified-login-session")
		rec := httptest.NewRecorder()
		app.handleApplicationSession(rec, r)
		return rec
	}
	for _, alias := range []string{"staging-auth", "staging-listening-auth"} {
		if w := request("GET", "/user/calls?auth_provider="+alias); w.Code != 200 || !strings.Contains(w.Body.String(), "incoming") {
			t.Fatalf("%s: call list %d %s", alias, w.Code, w.Body)
		}
		if w := request("GET", "/user/softphone/access?auth_provider="+alias); w.Code != 200 || !strings.Contains(w.Body.String(), "alice") {
			t.Fatalf("%s: line access %d %s", alias, w.Code, w.Body)
		}
	}
	// Action lists are never merged across aliases.
	before := requests.Load()
	if w := request("POST", "/user/softphone/place?auth_provider=staging-listening-auth"); w.Code != 403 {
		t.Fatalf("dial through the listening alias: %d %s", w.Code, w.Body)
	}
	if w := request("POST", "/user/softphone/listen/incoming?auth_provider=staging-auth"); w.Code != 403 {
		t.Fatalf("listen through the adviser alias: %d %s", w.Code, w.Body)
	}
	if requests.Load() != before {
		t.Fatal("disallowed actions contacted the identity provider")
	}
	// The listening alias still needs an explicit supervisor grant: alice has none.
	if w := request("POST", "/user/softphone/listen/incoming?auth_provider=staging-listening-auth"); w.Code != 403 || !strings.Contains(w.Body.String(), "supervisor") {
		t.Fatalf("listen without supervisor grant: %d %s", w.Code, w.Body)
	}
	if w := request("POST", "/user/softphone/coach/incoming?auth_provider=staging-listening-auth"); w.Code != 403 {
		t.Fatalf("coach without supervisor grant: %d %s", w.Code, w.Body)
	}
	// Wrong project and revoked sessions stay rejected.
	if w := request("GET", "/user/calls?auth_provider=staging-auth&project_id=other"); w.Code < 400 {
		t.Fatalf("wrong project accepted: %d", w.Code)
	}
	revoked.Store(true)
	if w := request("GET", "/user/calls?auth_provider=staging-listening-auth"); w.Code != 401 {
		t.Fatalf("revoked session accepted: %d", w.Code)
	}
	revoked.Store(false)

	// Reading and using the policy never rewrites it.
	w = phoneTestRequest(app, nil, "GET", "/access/policy", nil)
	var current phonePolicy
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &current) != nil {
		t.Fatalf("read policy: %d %s", w.Code, w.Body)
	}
	if current.Revision != saved.Revision || !reflect.DeepEqual(current.Providers, saved.Providers) || !reflect.DeepEqual(current.Users, saved.Users) || !reflect.DeepEqual(current.Groups, saved.Groups) {
		t.Fatalf("existing policy changed: revision %d→%d", saved.Revision, current.Revision)
	}

	// A conflicting authority written while identity validation is in flight
	// fails the request safely instead of authorizing it.
	conflict := func() {
		during.Store(nil)
		next := current
		next.Providers = append([]phoneAuthProvider(nil), current.Providers...)
		next.Providers[1].URL = provider.URL + "/other"
		raw, _ := json.Marshal(next)
		if _, err := app.db().db.Exec(`UPDATE telephony_access_policies SET policy_json=?,revision=revision+1 WHERE project_id='project-a'`, string(raw)); err != nil {
			t.Error(err)
		}
	}
	during.Store(&conflict)
	if w := request("GET", "/user/calls?auth_provider=staging-auth"); w.Code != 403 {
		t.Fatalf("request authorized despite a conflicting provider: %d %s", w.Code, w.Body)
	}
	if w := request("GET", "/user/calls?auth_provider=staging-auth"); w.Code != 401 {
		t.Fatalf("conflicting policy still authenticates: %d %s", w.Code, w.Body)
	}
}

func TestReviewH1IdentityProviderConflictingAuthorityRejectedOnSave(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	policy := phoneTestPolicy(t, app)
	t.Setenv("APTEVA_GATEWAY_URL", "")
	a := phoneAuthProvider{ID: "a", IssuerApp: "auth", IssuerInstallID: "11", URL: "https://auth.example.com/me", Format: "apteva-auth", Actions: []string{"call.read"}}
	for name, b := range map[string]phoneAuthProvider{
		"different url":     {ID: "b", IssuerApp: "auth", IssuerInstallID: "11", URL: "https://auth.example.com/me?alt=1", Format: "apteva-auth", Actions: []string{"call.read"}},
		"different format":  {ID: "b", IssuerApp: "auth", IssuerInstallID: "11", URL: "https://auth.example.com/me", Format: "oidc-userinfo", Actions: []string{"call.read"}},
		"duplicate id":      {ID: "a", IssuerApp: "auth", IssuerInstallID: "11", URL: "https://auth.example.com/me", Format: "apteva-auth", Actions: []string{"call.read"}},
		"prohibited target": {ID: "b", IssuerApp: "auth", IssuerInstallID: "12", URL: "https://10.0.0.5/me", Format: "apteva-auth", Actions: []string{"call.read"}},
	} {
		policy.Providers = []phoneAuthProvider{a, b}
		if w := phoneTestRequest(app, nil, "PUT", "/access/policy", policy); w.Code != 400 {
			t.Errorf("%s: saved with %d %s", name, w.Code, w.Body)
		}
	}
}
