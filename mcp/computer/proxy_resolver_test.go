package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type proxyPlatformStub struct {
	tk.BasePlatformClient
	connectionID int64
	appSlug      string
	toolCalls    int
	lastTool     string
	lastInput    map[string]any
	toolResult   *sdk.ExecuteResult
	toolErr      error
}

func (s *proxyPlatformStub) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{
		proxyProviderRole: map[string]any{
			"ids":        []any{float64(s.connectionID)},
			"default_id": float64(s.connectionID),
		},
	}}, nil
}

func (s *proxyPlatformStub) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	slug := s.appSlug
	if slug == "" {
		slug = "dataimpulse"
	}
	return &sdk.PlatformConnection{ID: id, AppSlug: slug, Name: slug + " production"}, nil
}

func (s *proxyPlatformStub) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	s.toolCalls++
	s.lastTool = tool
	s.lastInput = input
	if s.toolErr != nil || s.toolResult != nil {
		return s.toolResult, s.toolErr
	}
	if id != s.connectionID || tool != "get_sub_user" {
		return nil, tk.ErrNotImplemented
	}
	if got, _ := input["subuser_id"].(int64); got != 321 {
		return nil, tk.ErrNotImplemented
	}
	return &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"data":{"proxy_login":"base-user","proxy_password":"super-secret"}}`),
	}, nil
}

func TestResolveSessionProxyUsesIPRoyalIntegrationAPI(t *testing.T) {
	platform := &proxyPlatformStub{
		connectionID: 91,
		appSlug:      "iproyal",
		toolResult: &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(
			`["geo.iproyal.com:0:royal-user:royal-secret_session-random_lifetime-24h"]`,
		)},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	profile, err := dbCreateProxyProfile(appDB(ctx), ProxyProfile{
		Name: "IPRoyal DE", ProviderSlug: "iproyal", ConnectionID: 91,
		ExternalRef: "subuser-hash", Protocol: "http", DefaultCountry: "DE",
		StickyScope: "context", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := (&App{}).resolveSessionProxy(ctx, map[string]any{
		"proxy_mode": "profile", "proxy_profile": profile.ID,
	}, "browserbase", "ctx_customer")
	if err != nil {
		t.Fatalf("resolve IPRoyal: %v", err)
	}
	if platform.lastTool != "generate_proxy_list" {
		t.Fatalf("tool = %q", platform.lastTool)
	}
	if platform.lastInput["subuser_hash"] != "subuser-hash" || platform.lastInput["location"] != "_country-de" || platform.lastInput["rotation"] != "sticky" {
		t.Fatalf("input = %#v", platform.lastInput)
	}
	wantSession := "_session-" + stableProxyIdentity(profile.ID, "ctx_customer")[:8]
	if policy.External == nil || policy.External.Server != "http://geo.iproyal.com:12321" || policy.External.Username != "royal-user" ||
		!strings.Contains(policy.External.Password, wantSession) || strings.Contains(policy.External.Password, "session-random") {
		t.Fatalf("external proxy = %#v", policy.External)
	}
	assertSafeProxyState(t, policy.State, "royal-secret", "royal-user", "geo.iproyal.com")
}

func TestResolveSessionProxyUsesProxyCheapIntegrationAPI(t *testing.T) {
	platform := &proxyPlatformStub{
		connectionID: 92,
		appSlug:      "proxy-cheap",
		toolResult: &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(
			`{"data":{"proxies":[{"host":"proxy.proxy-cheap.com","port":31112,"username":"cheap-user","password":"cheap-secret_country-US_session-old"}]}}`,
		)},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	profile, err := dbCreateProxyProfile(appDB(ctx), ProxyProfile{
		Name: "Proxy-Cheap FR", ProviderSlug: "proxy-cheap", ConnectionID: 92,
		ExternalRef: "order-123", Protocol: "http", DefaultCountry: "FR",
		StickyScope: "session", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := (&App{}).resolveSessionProxy(ctx, map[string]any{
		"proxy_mode": "profile", "proxy_profile": profile.ID,
	}, "local", "")
	if err != nil {
		t.Fatalf("resolve Proxy-Cheap: %v", err)
	}
	if platform.lastTool != "get_order_proxies" || platform.lastInput["order_id"] != "order-123" {
		t.Fatalf("tool=%q input=%#v", platform.lastTool, platform.lastInput)
	}
	if policy.External == nil || policy.External.Server != "http://proxy.proxy-cheap.com:31112" || policy.External.Username != "cheap-user" ||
		!strings.Contains(policy.External.Password, "_country-FR") || strings.Contains(policy.External.Password, "_country-US") ||
		strings.Contains(policy.External.Password, "_session-old") {
		t.Fatalf("external proxy = %#v", policy.External)
	}
	assertSafeProxyState(t, policy.State, "cheap-secret", "cheap-user", "proxy.proxy-cheap.com")
}

func TestResolveSessionProxyRemovesProxyCheapRoutingTokensForRotatingGlobalProfile(t *testing.T) {
	platform := &proxyPlatformStub{
		connectionID: 94,
		appSlug:      "proxy-cheap",
		toolResult: &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(
			`["http://cheap-user:cheap-secret_country-Germany_session-old@proxy.proxy-cheap.com:31112"]`,
		)},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	profile, err := dbCreateProxyProfile(appDB(ctx), ProxyProfile{
		Name: "Proxy-Cheap global", ProviderSlug: "proxy-cheap", ConnectionID: 94,
		ExternalRef: "order-456", Protocol: "http", StickyScope: "rotating", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := (&App{}).resolveSessionProxy(ctx, map[string]any{
		"proxy_mode": "profile", "proxy_profile": profile.ID,
	}, "browserbase", "")
	if err != nil {
		t.Fatalf("resolve Proxy-Cheap rotating profile: %v", err)
	}
	if policy.External == nil || policy.External.Password != "cheap-secret" {
		t.Fatalf("routing tokens were not removed: %#v", policy.External)
	}
	assertSafeProxyState(t, policy.State, "cheap-secret", "cheap-user", "proxy.proxy-cheap.com")
}

func TestProxyProviderErrorsNeverExposeUpstreamSecrets(t *testing.T) {
	platform := &proxyPlatformStub{connectionID: 93, appSlug: "iproyal", toolErr: errors.New("upstream echoed royal-secret")}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	profile, err := dbCreateProxyProfile(appDB(ctx), ProxyProfile{
		Name: "Broken", ProviderSlug: "iproyal", ConnectionID: 93,
		ExternalRef: "hash", Protocol: "http", StickyScope: "rotating", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&App{}).resolveSessionProxy(ctx, map[string]any{
		"proxy_mode": "profile", "proxy_profile": profile.ID,
	}, "local", "")
	if err == nil || strings.Contains(err.Error(), "royal-secret") || !strings.Contains(err.Error(), "credential lookup failed") {
		t.Fatalf("safe error = %v", err)
	}
}

func assertSafeProxyState(t *testing.T, state SessionProxyState, forbidden ...string) {
	t.Helper()
	encoded, _ := json.Marshal(state)
	for _, value := range forbidden {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("safe state leaked %q: %s", value, encoded)
		}
	}
}

func TestResolveSessionProxyPrefersModeOverLegacyBoolean(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{}
	for _, test := range []struct {
		name        string
		args        map[string]any
		wantMode    string
		wantManaged *bool
	}{
		{name: "serializer false with auto", args: map[string]any{"proxy": false, "proxy_mode": "auto"}, wantMode: "auto"},
		{name: "legacy true cannot override direct", args: map[string]any{"proxy": true, "proxy_mode": "direct"}, wantMode: "direct", wantManaged: boolPointer(false)},
		{name: "legacy true alone remains managed", args: map[string]any{"proxy": true}, wantMode: "managed", wantManaged: boolPointer(true)},
		{name: "legacy false alone remains direct", args: map[string]any{"proxy": false}, wantMode: "direct", wantManaged: boolPointer(false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, err := app.resolveSessionProxy(ctx, test.args, "local", "")
			if err != nil {
				t.Fatal(err)
			}
			if policy.State.Mode != test.wantMode {
				t.Fatalf("mode=%q want %q", policy.State.Mode, test.wantMode)
			}
			if test.wantManaged == nil {
				if policy.Managed != nil {
					t.Fatalf("managed=%v want nil", *policy.Managed)
				}
			} else if policy.Managed == nil || *policy.Managed != *test.wantManaged {
				t.Fatalf("managed=%v want %v", policy.Managed, *test.wantManaged)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

func TestResolveSessionProxyUsesBoundDataImpulseProfile(t *testing.T) {
	platform := &proxyPlatformStub{connectionID: 77}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	profile, err := dbCreateProxyProfile(appDB(ctx), ProxyProfile{
		Name:           "Germany research",
		ProviderSlug:   "dataimpulse",
		ConnectionID:   77,
		ExternalRef:    "321",
		PoolType:       "residential",
		Protocol:       "http",
		DefaultCountry: "DE",
		StickyScope:    "session",
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	policy, err := (&App{}).resolveSessionProxy(ctx, map[string]any{
		"proxy_mode":    "profile",
		"proxy_profile": profile.ID,
	}, "browserbase", "")
	if err != nil {
		t.Fatalf("resolve proxy: %v", err)
	}
	if platform.toolCalls != 1 {
		t.Fatalf("credential lookups = %d, want 1", platform.toolCalls)
	}
	if policy.External == nil {
		t.Fatal("external proxy missing")
	}
	if policy.External.Server != "http://gw.dataimpulse.com:823" {
		t.Fatalf("server = %q", policy.External.Server)
	}
	if !strings.HasPrefix(policy.External.Username, "base-user__cr.de;sessid.") {
		t.Fatalf("username = %q", policy.External.Username)
	}
	if policy.External.Password != "super-secret" {
		t.Fatalf("password was not resolved")
	}
	if policy.State.ProfileName != "Germany research" || policy.State.Country != "DE" || policy.State.StickyScope != "session" {
		t.Fatalf("safe state = %#v", policy.State)
	}
	encoded, _ := json.Marshal(policy.State)
	if strings.Contains(string(encoded), "super-secret") || strings.Contains(string(encoded), "base-user") || strings.Contains(string(encoded), "gw.dataimpulse.com") {
		t.Fatalf("safe state leaked credentials or endpoint: %s", encoded)
	}
}

func TestResolveSessionProxyContextStickinessRequiresManagedContext(t *testing.T) {
	platform := &proxyPlatformStub{connectionID: 77}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	profile, err := dbCreateProxyProfile(appDB(ctx), ProxyProfile{
		Name: "Sticky", ProviderSlug: "dataimpulse", ConnectionID: 77,
		ExternalRef: "321", Protocol: "http", StickyScope: "context", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	_, err = (&App{}).resolveSessionProxy(ctx, map[string]any{
		"proxy_mode": "profile", "proxy_profile": profile.ID,
	}, "local", "")
	if err == nil || !strings.Contains(err.Error(), "requires an app-managed context") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveSessionProxyRejectsUnsupportedManagedCountry(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	_, err := (&App{}).resolveSessionProxy(ctx, map[string]any{
		"proxy_mode": "managed", "proxy_country": "DE",
	}, "steel", "")
	if err == nil || !strings.Contains(err.Error(), "does not support country selection") {
		t.Fatalf("error = %v", err)
	}
}

func TestProxyProfileCRUDAndDefaultSettings(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	db := appDB(ctx)
	profile, err := dbCreateProxyProfile(db, ProxyProfile{
		Name: "US QA", ProviderSlug: "dataimpulse", ConnectionID: 9,
		ExternalRef: "42", Protocol: "http", DefaultCountry: "us", StickyScope: "session", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if profile.DefaultCountry != "US" || profile.PoolType != "residential" {
		t.Fatalf("normalized profile = %#v", profile)
	}
	settings, err := dbUpdateSettings(db, map[string]any{
		"default_proxy_mode":       "profile",
		"default_proxy_profile_id": profile.ID,
		"lock_proxy_policy":        true,
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if settings.DefaultProxyProfile != profile.ID || !settings.LockProxyPolicy {
		t.Fatalf("settings = %#v", settings)
	}
	updated, err := dbUpdateProxyProfile(db, profile.ID, map[string]any{"enabled": false})
	if err != nil {
		t.Fatalf("disable profile: %v", err)
	}
	if updated.Enabled {
		t.Fatal("profile remained enabled")
	}
	enabled, err := dbListProxyProfiles(db, true)
	if err != nil || len(enabled) != 0 {
		t.Fatalf("enabled profiles = %#v, err=%v", enabled, err)
	}
}

func TestSafeProxyResourcesExcludeCredentials(t *testing.T) {
	resources := safeProxyResources(map[string]any{"data": []any{
		map[string]any{
			"subuser_id": float64(321), "label": "Research pool",
			"proxy_login": "credential-user", "proxy_password": "credential-pass",
		},
	}})
	if len(resources) != 1 || resources[0].ID != "321" || resources[0].Name != "Research pool" {
		t.Fatalf("resources = %#v", resources)
	}
	encoded, _ := json.Marshal(resources)
	if strings.Contains(string(encoded), "credential-user") || strings.Contains(string(encoded), "credential-pass") {
		t.Fatalf("safe resources leaked credentials: %s", encoded)
	}
}

func TestSafeProxyResourcesUsesOnlyProviderResourceIdentifiers(t *testing.T) {
	resources := safeProxyResourcesFor(map[string]any{"data": []any{
		map[string]any{"id": "account-id", "name": "Account"},
		map[string]any{"subuser_hash": "subuser-hash", "username": "royal-user", "password": "royal-secret"},
	}}, "subuser_hash", "hash")
	if len(resources) != 1 || resources[0].ID != "subuser-hash" {
		t.Fatalf("resources = %#v", resources)
	}
	encoded, _ := json.Marshal(resources)
	if strings.Contains(string(encoded), "account-id") || strings.Contains(string(encoded), "royal-secret") || strings.Contains(string(encoded), "royal-user") {
		t.Fatalf("safe resources exposed unrelated ids or credentials: %s", encoded)
	}
}
