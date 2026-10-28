package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestReviewH1IdentityProviderSharedIssuerRejected(t *testing.T) {
	a := phoneAuthProvider{ID: "a", IssuerApp: "auth", IssuerInstallID: "1"}
	b := phoneAuthProvider{ID: "b", IssuerApp: "auth", IssuerInstallID: "1"}
	c := phoneAuthProvider{ID: "c", IssuerApp: "auth", IssuerInstallID: "2"}
	if !phoneProviderIssuerShared(phonePolicy{Providers: []phoneAuthProvider{a, b}}, a) {
		t.Fatal("shared issuer not detected")
	}
	if phoneProviderIssuerShared(phonePolicy{Providers: []phoneAuthProvider{a, c}}, a) {
		t.Fatal("distinct issuers flagged as shared")
	}
}
