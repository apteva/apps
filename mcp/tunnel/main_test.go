package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	tunnelclient "github.com/apteva/apps/mcp/tunnel/client"
)

func newTestApp(t *testing.T) (*App, *http.ServeMux) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{
		"max_tunnels_per_project":            "2",
		"max_request_bytes":                  "1048576",
		"request_timeout_seconds":            "5",
		"max_concurrent_requests_per_tunnel": "2",
	}), tk.WithPlatform(testPlatform{}))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.OnUnmount(ctx); err != nil {
			t.Errorf("unmount: %v", err)
		}
	})
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		pattern := route.Pattern
		if route.Method != "" {
			pattern = route.Method + " " + pattern
		}
		mux.HandleFunc(pattern, route.Handler)
	}
	return app, mux
}

type testPlatform struct {
	tk.BasePlatformClient
}

func (testPlatform) ExposeIngress(request sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	return &sdk.IngressRoute{
		Hostname: request.Hostname,
		Target:   request.Target,
		Status:   "active",
	}, nil
}

func (testPlatform) UnexposeIngress(string) error { return nil }

func TestManifestIsSelfHostedAndDomainNeutral(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Name != "tunnel" || manifest.Version != "0.1.0" {
		t.Fatalf("unexpected manifest identity: %s %s", manifest.Name, manifest.Version)
	}
	for _, route := range manifest.Provides.HTTPRoutes {
		if route.Prefix == "/" && route.NoAuth {
			t.Fatal("catch-all app route must never bypass sidecar authentication")
		}
	}
	raw := strings.ToLower(string(manifestYAML))
	for _, forbidden := range []string{"ngrok.com", "zrok.io", "tunnel.apteva.ai"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("manifest hardcodes hosted provider/domain %q", forbidden)
		}
	}
}

func TestNameAndDomainValidation(t *testing.T) {
	for _, name := range []string{"demo", "shop-42", "a23"} {
		if _, err := normalizeTunnelName(name); err != nil {
			t.Errorf("valid name %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"ab", "www", "-demo", "Demo_Name", "a.b"} {
		if _, err := normalizeTunnelName(name); err == nil {
			t.Errorf("invalid name %q accepted", name)
		}
	}
	domain, err := normalizeDomain("*.Tunnel.Example.com.")
	if err != nil || domain != "tunnel.example.com" {
		t.Fatalf("normalized domain=%q err=%v", domain, err)
	}
	if !hostBelongsToBase("demo.tunnel.example.com", domain) {
		t.Fatal("expected exact child hostname to belong to base")
	}
	for _, host := range []string{
		"tunnel.example.com",
		"nested.demo.tunnel.example.com",
		"demo.tunnel.example.com.attacker.test",
	} {
		if hostBelongsToBase(host, domain) {
			t.Fatalf("unsafe host %q matched base", host)
		}
	}
}

func TestTokenIsStoredOnlyAsDigest(t *testing.T) {
	app, _ := newTestApp(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := app.store.saveConfig(&tunnelConfig{
		BaseDomain: "tunnel.example.com",
		ProjectID:  "project-a",
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := app.createTunnel("project-a", "demo")
	if err != nil {
		t.Fatal(err)
	}
	token := result["connector_token"].(string)
	item := result["tunnel"].(*tunnel)
	stored, err := app.store.tunnelByID(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash == token || stored.TokenHash != tokenDigest(token) {
		t.Fatal("connector credential was not stored as a digest")
	}
}

func TestQuotaAndReleasedNameReuse(t *testing.T) {
	app, _ := newTestApp(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := app.store.saveConfig(&tunnelConfig{
		BaseDomain: "tunnel.example.com",
		ProjectID:  "project-a",
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := app.createTunnel("project-a", "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.createTunnel("project-a", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.createTunnel("project-a", "third"); err == nil {
		t.Fatal("project quota was not enforced")
	}
	item := first["tunnel"].(*tunnel)
	if _, err := app.store.revokeTunnel(item.ID, "project-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.createTunnel("project-a", "first"); err != nil {
		t.Fatalf("released stable name was not reusable: %v", err)
	}
}

func TestOnlyOperatorProjectCanReconfigureSharedDomain(t *testing.T) {
	app, _ := newTestApp(t)
	autoDNS := false
	if _, _, err := app.configureDomain("operator-project", configureDomainInput{
		BaseDomain: "tunnel.example.com",
		AutoDNS:    &autoDNS,
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err := app.configureDomain("customer-project", configureDomainInput{
		BaseDomain: "attacker.example",
		AutoDNS:    &autoDNS,
	})
	var typed *domainError
	if !errors.As(err, &typed) || typed.status != http.StatusForbidden {
		t.Fatalf("non-operator reconfigure error=%v", err)
	}
	cfg, err := app.store.config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseDomain != "tunnel.example.com" {
		t.Fatalf("base domain changed to %q", cfg.BaseDomain)
	}
}

func TestEndToEndPublicHTTPRoundTrip(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hello/world" || r.URL.Query().Get("source") != "test" {
			t.Errorf("origin request URI=%s", r.URL.RequestURI())
		}
		if r.Header.Get("X-Apteva-Tunnel") != "1" {
			t.Error("origin request missing tunnel marker")
		}
		if r.Header.Get("Authorization") != "Bearer visitor-token" {
			t.Errorf("origin visitor Authorization=%q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Apteva-Original-Authorization") != "" {
			t.Error("origin received an internal platform header")
		}
		w.Header().Set("X-Origin", "local")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("proxied:" + r.Method))
	}))
	defer origin.Close()

	app, mux := newTestApp(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := app.store.saveConfig(&tunnelConfig{
		BaseDomain: "tunnel.example.com",
		ProjectID:  "project-a",
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := app.createTunnel("project-a", "demo")
	if err != nil {
		t.Fatal(err)
	}
	token := result["connector_token"].(string)

	edge := httptest.NewServer(mux)
	defer edge.Close()
	connector, err := tunnelclient.New(edge.URL, token, origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connectorDone := make(chan error, 1)
	go func() { connectorDone <- connector.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for !app.connectors.connected(result["tunnel"].(*tunnel).ID) {
		if time.Now().After(deadline) {
			t.Fatal("connector did not come online")
		}
		time.Sleep(10 * time.Millisecond)
	}

	request, err := http.NewRequest(http.MethodPost, edge.URL+"/hello/world?source=test", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "demo.tunnel.example.com"
	request.Header.Set("Authorization", "Bearer sidecar-app-token")
	request.Header.Set("X-Apteva-Original-Authorization", "Bearer visitor-token")
	request.Header.Set("X-Apteva-App-Install-ID", "99")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("public status=%d", response.StatusCode)
	}
	if response.Header.Get("X-Origin") != "local" || response.Header.Get("Via") != "Apteva-Tunnel/1" {
		t.Fatalf("public response headers=%v", response.Header)
	}
	if err := app.usage.flush(); err != nil {
		t.Fatal(err)
	}
	stored, err := app.store.tunnelByID(result["tunnel"].(*tunnel).ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RequestCount != 1 || stored.BytesIn != 4 || stored.BytesOut != int64(len("proxied:POST")) {
		t.Fatalf("batched usage was not persisted: %+v", stored)
	}

	cancel()
	select {
	case <-connectorDone:
	case <-time.After(2 * time.Second):
		t.Fatal("connector did not stop")
	}
}

func TestPublicHostCannotReachAnotherTunnel(t *testing.T) {
	app, mux := newTestApp(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := app.store.saveConfig(&tunnelConfig{
		BaseDomain: "tunnel.example.com",
		ProjectID:  "project-a",
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.createTunnel("project-a", "demo", "demo.tunnel.example.com", tokenDigest("secret")); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://evil.test/", nil)
	request.Host = "demo.tunnel.example.com.attacker.test"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("spoofed host status=%d body=%s", response.Code, response.Body)
	}
}

// Compile-time guard: the test platform SDK used by this app must include the
// ingress methods required by tunnel reservation lifecycle.
var _ sdk.PlatformClient = tk.BasePlatformClient{}
