package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── CF client (httptest-mocked) ───────────────────────────────────

// mockCF spins up an httptest server that records every call and lets
// each test plug in handlers per (method, path-prefix). Tests assert on
// requests + canned responses without needing real Cloudflare creds.
type mockCF struct {
	t        *testing.T
	srv      *httptest.Server
	handlers map[string]http.HandlerFunc // key: METHOD path-prefix
	calls    []mockCall
}

type mockCall struct {
	Method string
	Path   string
	Body   string
	Auth   string
}

func newMockCF(t *testing.T) *mockCF {
	m := &mockCF{t: t, handlers: map[string]http.HandlerFunc{}}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Replay bytes back into r.Body so the registered handler can
		// decode them too — io.ReadAll consumes the original.
		r.Body = io.NopCloser(bytes.NewReader(body))
		m.calls = append(m.calls, mockCall{
			Method: r.Method,
			Path:   r.URL.Path + (func() string {
				if r.URL.RawQuery != "" {
					return "?" + r.URL.RawQuery
				}
				return ""
			})(),
			Body: string(body),
			Auth: r.Header.Get("Authorization"),
		})
		// Match the longest registered prefix.
		var bestKey string
		for key := range m.handlers {
			parts := strings.SplitN(key, " ", 2)
			if len(parts) != 2 || parts[0] != r.Method {
				continue
			}
			if strings.HasPrefix(r.URL.Path, parts[1]) && len(parts[1]) > len(bestKey) {
				bestKey = key
			}
		}
		if bestKey != "" {
			m.handlers[bestKey](w, r)
			return
		}
		t.Errorf("mockCF: unhandled %s %s", r.Method, r.URL.Path)
		http.Error(w, "no handler", http.StatusNotImplemented)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockCF) on(method, prefix string, h http.HandlerFunc) {
	m.handlers[method+" "+prefix] = h
}

func (m *mockCF) client() *cfClient {
	return &cfClient{apiToken: "test-token", baseURL: m.srv.URL, hc: m.srv.Client()}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestCF_CreateTunnel_PostsBodyAndExtractsToken(t *testing.T) {
	m := newMockCF(t)
	m.on("POST", "/accounts/acct-1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "live-link-test" {
			t.Errorf("body name=%v", body["name"])
		}
		if body["config_src"] != "cloudflare" {
			t.Errorf("body config_src=%v, want cloudflare (so ingress is API-managed)", body["config_src"])
		}
		writeJSON(w, 200, map[string]any{"result": map[string]any{
			"id": "TUN-UUID-1", "name": "live-link-test", "token": "TOK-1",
		}})
	})
	tun, err := m.client().createTunnel("acct-1", "live-link-test")
	if err != nil {
		t.Fatal(err)
	}
	if tun.ID != "TUN-UUID-1" || tun.Token != "TOK-1" {
		t.Errorf("tun=%+v", tun)
	}
	if !strings.HasPrefix(m.calls[0].Auth, "Bearer ") {
		t.Errorf("missing bearer auth: %q", m.calls[0].Auth)
	}
}

func TestCF_CreateTunnel_SurfacesAPIError(t *testing.T) {
	m := newMockCF(t)
	m.on("POST", "/accounts/acct-1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 403, map[string]any{
			"errors": []map[string]any{{"code": 10000, "message": "Authentication error"}},
		})
	})
	_, err := m.client().createTunnel("acct-1", "n")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "Authentication error") || !strings.Contains(err.Error(), "10000") {
		t.Errorf("error %q should include CF code + message", err)
	}
}

func TestCF_FindTunnelByName_FilteringAndEmptyMatch(t *testing.T) {
	m := newMockCF(t)
	m.on("GET", "/accounts/acct-1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": []map[string]any{
			{"id": "wrong", "name": "other-tunnel"},
			{"id": "right", "name": "live-link-x"},
		}})
	})
	tun, err := m.client().findTunnelByName("acct-1", "live-link-x")
	if err != nil {
		t.Fatal(err)
	}
	if tun == nil || tun.ID != "right" {
		t.Errorf("got %+v", tun)
	}

	// Same call but no match should return (nil, nil), not an error.
	tun, err = m.client().findTunnelByName("acct-1", "no-such-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if tun != nil {
		t.Errorf("got %+v, want nil", tun)
	}
}

func TestCF_PutTunnelConfig_SendsIngressWithCatchAll(t *testing.T) {
	m := newMockCF(t)
	m.on("PUT", "/accounts/acct-1/cfd_tunnel/T/configurations", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Config struct {
				Ingress []map[string]any `json:"ingress"`
			} `json:"config"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Config.Ingress) != 2 {
			t.Errorf("ingress len=%d, want 2 (rule + catch-all)", len(body.Config.Ingress))
		}
		if body.Config.Ingress[0]["hostname"] != "h.example.com" {
			t.Errorf("rule hostname=%v", body.Config.Ingress[0]["hostname"])
		}
		if body.Config.Ingress[0]["service"] != "http://localhost:5280" {
			t.Errorf("rule service=%v", body.Config.Ingress[0]["service"])
		}
		if body.Config.Ingress[1]["service"] != "http_status:404" {
			t.Errorf("catch-all=%v", body.Config.Ingress[1]["service"])
		}
		writeJSON(w, 200, map[string]any{"result": map[string]any{}})
	})
	if err := m.client().putTunnelConfig("acct-1", "T", "h.example.com", "http://localhost:5280"); err != nil {
		t.Fatal(err)
	}
}

func TestCF_UpsertDNSCNAME_CreatesWhenAbsent(t *testing.T) {
	m := newMockCF(t)
	m.on("GET", "/zones/Z/dns_records", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": []map[string]any{}})
	})
	m.on("POST", "/zones/Z/dns_records", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["type"] != "CNAME" || body["name"] != "h.example.com" {
			t.Errorf("create body=%+v", body)
		}
		if body["content"] != "T-UUID.cfargotunnel.com" {
			t.Errorf("create content=%v", body["content"])
		}
		if body["proxied"] != true {
			t.Errorf("proxied should be true (otherwise the tunnel doesn't get traffic)")
		}
		writeJSON(w, 200, map[string]any{"result": map[string]any{"id": "REC-1"}})
	})
	id, err := m.client().upsertDNSCNAME("Z", "h.example.com", "T-UUID")
	if err != nil {
		t.Fatal(err)
	}
	if id != "REC-1" {
		t.Errorf("id=%q", id)
	}
}

func TestCF_UpsertDNSCNAME_UpdatesWhenPresent(t *testing.T) {
	m := newMockCF(t)
	m.on("GET", "/zones/Z/dns_records", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": []map[string]any{
			{"id": "REC-EXISTING", "type": "CNAME", "name": "h.example.com", "content": "stale.cfargotunnel.com"},
		}})
	})
	m.on("PUT", "/zones/Z/dns_records/REC-EXISTING", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": map[string]any{"id": "REC-EXISTING"}})
	})
	// If the test failed it would call POST and the mock would error,
	// so the absence of a POST handler is itself an assertion.
	id, err := m.client().upsertDNSCNAME("Z", "h.example.com", "NEW-UUID")
	if err != nil {
		t.Fatal(err)
	}
	if id != "REC-EXISTING" {
		t.Errorf("should reuse existing record id; got %q", id)
	}
}

// ─── resolveMode ───────────────────────────────────────────────────

func TestResolveMode_DefaultsToQuickAndAcceptsNamed(t *testing.T) {
	cases := []struct {
		in   string
		want Mode
	}{
		{"", ModeQuick},
		{"quick", ModeQuick},
		{"QUICK", ModeQuick},
		{"named", ModeNamed},
		{"  Named  ", ModeNamed},
		{"bogus", ModeQuick}, // unknown values fall back rather than 500ing
	}
	for _, c := range cases {
		ctx := newTestCtxWithConfig(t, map[string]string{"mode": c.in})
		got := (&App{}).resolveMode(ctx)
		if got != c.want {
			t.Errorf("mode=%q: got %q want %q", c.in, got, c.want)
		}
	}
}

// ─── ensureNamedTunnel + destroyNamedTunnel (end-to-end with mockCF) ─

func TestEnsureNamedTunnel_CreatesPersistsAndIsIdempotent(t *testing.T) {
	cf := newMockCF(t)
	created := 0
	cf.on("GET", "/accounts/A/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": []any{}})
	})
	cf.on("POST", "/accounts/A/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		created++
		writeJSON(w, 200, map[string]any{"result": map[string]any{
			"id": "TUN-UUID", "token": "TOK", "name": "irrelevant",
		}})
	})
	cf.on("PUT", "/accounts/A/cfd_tunnel/TUN-UUID/configurations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": map[string]any{}})
	})
	cf.on("GET", "/zones/Z/dns_records", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": []any{}})
	})
	cf.on("POST", "/zones/Z/dns_records", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": map[string]any{"id": "REC"}})
	})

	// Swap in the mock CF base URL via a small patch: we can't inject
	// it into ensureNamedTunnel directly, so we wrap the test by
	// pointing newCFClient at the mock via the package-level cfAPIBase
	// variable. (Done at the bottom of this file via testCFBaseURL.)
	t.Setenv("LIVE_LINK_CF_API_BASE", cf.srv.URL)

	ctx := newTestCtxWithConfig(t, map[string]string{
		"mode":          "named",
		"hostname":      "h.example.com",
		"cf_api_token":  "tok",
		"cf_account_id": "A",
		"cf_zone_id":    "Z",
		"target_url":    "http://localhost:5280",
	})

	app := &App{}
	nt, err := app.ensureNamedTunnel(ctx)
	if err != nil {
		t.Fatalf("ensureNamedTunnel: %v", err)
	}
	if nt.TunnelID != "TUN-UUID" || nt.TunnelToken != "TOK" || nt.DNSRecordID != "REC" {
		t.Errorf("nt=%+v", nt)
	}

	// Second call: should hit the local DB cache and not create again.
	nt2, err := app.ensureNamedTunnel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if nt2.TunnelID != "TUN-UUID" {
		t.Errorf("second call returned different tunnel: %+v", nt2)
	}
	if created != 1 {
		t.Errorf("createTunnel called %d times; want exactly 1 (idempotent)", created)
	}
}

func TestEnsureNamedTunnel_RejectsMissingConfig(t *testing.T) {
	ctx := newTestCtxWithConfig(t, map[string]string{
		"mode":     "named",
		"hostname": "h.example.com",
		// no cf_api_token / account / zone
	})
	_, err := (&App{}).ensureNamedTunnel(ctx)
	if err == nil {
		t.Fatal("expected error for missing CF credentials")
	}
	if !strings.Contains(err.Error(), "cf_api_token") {
		t.Errorf("error should name the missing field: %q", err)
	}
}

func TestDestroyNamedTunnel_HitsCFAndDropsRow(t *testing.T) {
	cf := newMockCF(t)
	cf.on("GET", "/accounts/A/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": []any{}})
	})
	cf.on("POST", "/accounts/A/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": map[string]any{"id": "TUN", "token": "TOK"}})
	})
	cf.on("PUT", "/accounts/A/cfd_tunnel/TUN/configurations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": map[string]any{}})
	})
	cf.on("GET", "/zones/Z/dns_records", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": []any{}})
	})
	cf.on("POST", "/zones/Z/dns_records", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"result": map[string]any{"id": "REC"}})
	})
	dnsDeleted := false
	tunDeleted := false
	cf.on("DELETE", "/zones/Z/dns_records/REC", func(w http.ResponseWriter, r *http.Request) {
		dnsDeleted = true
		writeJSON(w, 200, map[string]any{"result": map[string]any{}})
	})
	cf.on("DELETE", "/accounts/A/cfd_tunnel/TUN", func(w http.ResponseWriter, r *http.Request) {
		tunDeleted = true
		writeJSON(w, 200, map[string]any{"result": map[string]any{}})
	})

	t.Setenv("LIVE_LINK_CF_API_BASE", cf.srv.URL)

	ctx := newTestCtxWithConfig(t, map[string]string{
		"mode":          "named",
		"hostname":      "h.example.com",
		"cf_api_token":  "tok",
		"cf_account_id": "A",
		"cf_zone_id":    "Z",
	})
	app := &App{}
	if _, err := app.ensureNamedTunnel(ctx); err != nil {
		t.Fatal(err)
	}
	destroyed, err := app.destroyNamedTunnel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !destroyed {
		t.Error("destroyed=false")
	}
	if !dnsDeleted || !tunDeleted {
		t.Errorf("CF deletes: dns=%v tunnel=%v", dnsDeleted, tunDeleted)
	}
	// And the local row is gone.
	if got, _ := dbGetNamedTunnel(ctx.AppDB(), "h.example.com"); got != nil {
		t.Errorf("named_tunnels row still present: %+v", got)
	}
}
