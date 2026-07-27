package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type relayDomainPlatform struct {
	tk.BasePlatformClient
	withDomains bool
	routes      []sdk.IngressRoute
	unexposed   []string
	appCalls    []struct {
		Tool  string
		Input map[string]any
	}
}

func (p *relayDomainPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	bindings := map[string]any{"ios_provider": float64(7)}
	if p.withDomains {
		bindings["domains"] = float64(9)
	}
	return &sdk.InstallIdentity{AppName: "push", Bindings: bindings}, nil
}

func (p *relayDomainPlatform) PlatformInfo() (*sdk.PlatformInfo, error) {
	return &sdk.PlatformInfo{PublicURL: "https://main.apteva.ai"}, nil
}

func (p *relayDomainPlatform) ExposeIngress(req sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	route := sdk.IngressRoute{
		Hostname:  req.Hostname,
		Target:    req.Target,
		ProjectID: req.ProjectID,
		OwnerKind: req.OwnerKind,
		CertFQDN:  req.CertFQDN,
		TLSMode:   req.TLSMode,
		Status:    "active",
		Certificate: &sdk.IngressCertificateStatus{
			FQDN:   req.CertFQDN,
			Status: "not_cached",
		},
	}
	p.routes = []sdk.IngressRoute{route}
	return &p.routes[0], nil
}

func (p *relayDomainPlatform) UnexposeIngress(hostname string) error {
	p.unexposed = append(p.unexposed, hostname)
	p.routes = nil
	return nil
}

func (p *relayDomainPlatform) ListIngressRoutes() ([]sdk.IngressRoute, error) {
	return p.routes, nil
}

func (p *relayDomainPlatform) CallAppResult(_ string, tool string, input map[string]any, output any) error {
	p.appCalls = append(p.appCalls, struct {
		Tool  string
		Input map[string]any
	}{Tool: tool, Input: input})
	var value any = map[string]any{"ok": true}
	if tool == "domain_list" {
		value = map[string]any{
			"domains": []map[string]any{{"name": "apteva.ai"}},
		}
	}
	encoded, _ := json.Marshal(value)
	return json.Unmarshal(encoded, output)
}

func newRelayDomainHarness(t *testing.T, withDomains bool) (*App, *relayDomainPlatform, *http.ServeMux) {
	t.Helper()
	platform := &relayDomainPlatform{withDomains: withDomains}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	app := &App{encryptionKey: "test-only-key-with-at-least-24-characters"}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		mux.HandleFunc(route.Method+" "+route.Pattern, route.Handler)
	}
	return app, platform, mux
}

func relayDomainRequest(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &payload)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestRelayDomainConfiguresIngressDNSAndManagedTLS(t *testing.T) {
	app, platform, mux := newRelayDomainHarness(t, true)
	configured := relayDomainRequest(t, mux, http.MethodPost, "/admin/relay-domain", map[string]any{
		"hostname":   "Push.Apteva.ai.",
		"project_id": "infrastructure",
	})
	if configured.Code != http.StatusCreated {
		t.Fatalf("configure status=%d body=%s", configured.Code, configured.Body)
	}
	state := decodeResponse[relayDomainState](t, configured)
	if !state.Configured || state.RelayURL != "https://push.apteva.ai" || !state.DNS.Managed {
		t.Fatalf("configured state=%+v", state)
	}
	if state.Route == nil || state.Route.Target != "app://push" ||
		state.Route.CertFQDN != "push.apteva.ai" || state.Route.TLSMode != "auto" {
		t.Fatalf("route=%+v", state.Route)
	}
	if state.DNS.Domain != "apteva.ai" || state.DNS.Name != "push" ||
		state.DNS.Type != "CNAME" || state.DNS.Value != "main.apteva.ai" {
		t.Fatalf("dns=%+v", state.DNS)
	}

	var sawSet bool
	for _, call := range platform.appCalls {
		if call.Tool != "domain_records_set" {
			continue
		}
		sawSet = true
		if call.Input["_project_id"] != "infrastructure" {
			t.Fatalf("domain project=%v", call.Input["_project_id"])
		}
	}
	if !sawSet {
		t.Fatal("Domains did not receive domain_records_set")
	}

	refreshed := relayDomainRequest(t, mux, http.MethodGet, "/admin/relay-domain", nil)
	if refreshed.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", refreshed.Code, refreshed.Body)
	}
	state = decodeResponse[relayDomainState](t, refreshed)
	if state.Route == nil || state.Route.Status != "active" {
		t.Fatalf("refreshed route=%+v", state.Route)
	}
	stored, err := app.store.relayDomain()
	if err != nil || stored.Hostname != "push.apteva.ai" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}

	detached := relayDomainRequest(t, mux, http.MethodDelete, "/admin/relay-domain?remove_dns=true", nil)
	if detached.Code != http.StatusOK {
		t.Fatalf("detach status=%d body=%s", detached.Code, detached.Body)
	}
	if len(platform.unexposed) != 1 || platform.unexposed[0] != "push.apteva.ai" {
		t.Fatalf("unexposed=%v", platform.unexposed)
	}
	var sawDelete bool
	for _, call := range platform.appCalls {
		if call.Tool == "domain_records_delete" {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatal("Domains did not receive domain_records_delete")
	}
}

func TestRelayDomainWithoutDomainsReturnsManualRecord(t *testing.T) {
	_, platform, mux := newRelayDomainHarness(t, false)
	initial := relayDomainRequest(t, mux, http.MethodGet, "/admin/relay-domain", nil)
	initialState := decodeResponse[relayDomainState](t, initial)
	if initialState.Hostname != "" {
		t.Fatalf("unconfigured relay must not assume a hostname: %+v", initialState)
	}
	configured := relayDomainRequest(t, mux, http.MethodPost, "/admin/relay-domain", map[string]any{
		"hostname":   "push.example.com",
		"project_id": "infrastructure",
	})
	if configured.Code != http.StatusCreated {
		t.Fatalf("configure status=%d body=%s", configured.Code, configured.Body)
	}
	state := decodeResponse[relayDomainState](t, configured)
	if state.DomainsBound || state.DNS.Managed || state.DNS.Error == "" {
		t.Fatalf("manual DNS state=%+v", state)
	}
	if state.DNS.Name != "push" || state.DNS.Type != "CNAME" || state.DNS.Value != "main.apteva.ai" {
		t.Fatalf("manual DNS=%+v", state.DNS)
	}
	if len(platform.routes) != 1 {
		t.Fatal("ingress should be configured even without Domains")
	}
}

func TestRelayDomainAdminRoutesRemainAuthenticated(t *testing.T) {
	found := 0
	for _, route := range (&App{}).HTTPRoutes() {
		if route.Pattern == "/admin/relay-domain" {
			found++
			if route.NoAuth {
				t.Fatalf("%s %s must not bypass app authentication", route.Method, route.Pattern)
			}
		}
	}
	if found != 3 {
		t.Fatalf("relay admin routes=%d, want 3", found)
	}
}

func TestRelayDomainManifestDeclaresPlatformAndDomainsDependencies(t *testing.T) {
	manifest := (&App{}).Manifest()
	permissions := map[sdk.Permission]bool{}
	for _, permission := range manifest.Requires.Permissions {
		permissions[permission] = true
	}
	if !permissions[sdk.PermIngressWrite] || !permissions[sdk.PermAppsCall] {
		t.Fatalf("relay permissions=%v", manifest.Requires.Permissions)
	}
	for _, dependency := range manifest.Requires.Integrations {
		if dependency.Role == "domains" && dependency.Kind == "app" && !dependency.Required {
			return
		}
	}
	t.Fatal("manifest is missing optional Domains app dependency")
}
