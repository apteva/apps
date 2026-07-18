package main

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestHostedProcessEnvIsPrivateByDefault(t *testing.T) {
	env := hostedProcessEnv(hostedSpawnSpec{IngressMode: IngressParent})
	if !strings.Contains(env, "APTEVA_INGRESS_ENABLED=0") || strings.Contains(env, ":443") {
		t.Fatalf("parent environment = %q", env)
	}
}

func TestHostedProcessEnvEnablesDirectIngress(t *testing.T) {
	env := hostedProcessEnv(hostedSpawnSpec{
		IngressMode: IngressDirectPending,
		PrimaryHost: "agents.example.com",
		ACMEEmail:   "ops@example.com",
	})
	for _, want := range []string{
		"APTEVA_INGRESS_ENABLED=1",
		"APTEVA_HTTP_LISTEN_ADDR=:80",
		"APTEVA_HTTPS_LISTEN_ADDR=:443",
		"APTEVA_PRIMARY_HOST='agents.example.com'",
		"APTEVA_ACME_EMAIL='ops@example.com'",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("direct environment missing %q: %s", want, env)
		}
	}
}

func TestHostedProcessEnvKeepsQuarantinePrivate(t *testing.T) {
	env := hostedProcessEnv(hostedSpawnSpec{IngressMode: IngressDirect, Quarantine: true})
	if !strings.Contains(env, "APTEVA_INGRESS_ENABLED=0") {
		t.Fatalf("quarantine environment = %q", env)
	}
}

func TestStoreIngressModeRejectsDirectForParentTenant(t *testing.T) {
	app, _ := newTestApp(t)
	id := seedTenant(t, app, "parent-only", StatusActive)
	if err := app.store.setIngressMode(id, IngressDirectPending, ""); err == nil {
		t.Fatal("parent-host tenant accepted direct ingress")
	}
}

func TestRefreshDirectIngressDoesNotRegisterParentRoute(t *testing.T) {
	platform := &fleetIngressPlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(platform))
	tenant := &Tenant{
		ID:          "direct-1",
		Slug:        "direct",
		Kind:        KindLocal,
		InstanceID:  3,
		BaseURL:     "http://203.0.113.10:7100",
		Domain:      "agents.example.com",
		IngressMode: IngressDirect,
	}
	if err := app.refreshTenantIngressTargets(ctx, tenant, "http://127.0.0.1:43123"); err != nil {
		t.Fatalf("refresh direct ingress: %v", err)
	}
	if len(platform.exposed) != 0 {
		t.Fatalf("direct tenant registered parent routes: %+v", platform.exposed)
	}

	tenant.IngressMode = IngressDirectPending
	if err := app.refreshTenantIngressTargets(ctx, tenant, "http://127.0.0.1:43123"); err != nil {
		t.Fatalf("refresh pending ingress: %v", err)
	}
	if len(platform.exposed) != 1 || platform.exposed[0].Hostname != tenant.Domain {
		t.Fatalf("pending tenant did not retain parent route: %+v", platform.exposed)
	}
}

var _ sdk.PlatformClient = (*fleetIngressPlatform)(nil)
