package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type proxyPlatformStub struct {
	tk.BasePlatformClient
	connectionID int64
	toolCalls    int
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
	return &sdk.PlatformConnection{ID: id, AppSlug: "dataimpulse", Name: "DataImpulse production"}, nil
}

func (s *proxyPlatformStub) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	s.toolCalls++
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
