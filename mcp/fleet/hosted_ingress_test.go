package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type ingressPreparePlatform struct {
	tk.BasePlatformClient
	reached chan struct{}
	unblock chan struct{}
	blocked bool
	calls   int
}

func (p *ingressPreparePlatform) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	if appName != "instances" || tool != "instance_get" {
		return nil, errors.New("unexpected platform call")
	}
	p.calls++
	if !p.blocked {
		p.blocked = true
		close(p.reached)
		<-p.unblock
	}
	return nil, errors.New("instance preflight unavailable")
}

func TestIngressPrepareHoldsOuterLockAndAllowsRetryAfterPreflightFailure(t *testing.T) {
	platform := &ingressPreparePlatform{
		reached: make(chan struct{}),
		unblock: make(chan struct{}),
	}
	app, ctx := newTestApp(t, tk.WithPlatform(platform))
	id := seedTenant(t, app, "hosted-ingress", StatusActive)
	if err := app.store.setLocation(id, 3, "http://203.0.113.8:7100", remoteFleetRoot+"/hosted-ingress"); err != nil {
		t.Fatalf("set hosted location: %v", err)
	}
	if err := app.store.setDomain(id, "tenant.example.com", "", time.Now().UTC()); err != nil {
		t.Fatalf("set tenant domain: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := app.toolIngressPrepareDirect(ctx, map[string]any{"tenant_id": id})
		result <- err
	}()
	select {
	case <-platform.reached:
	case <-time.After(time.Second):
		t.Fatal("direct ingress did not reach Instances preflight")
	}
	if _, err := app.beginTenantOperation(id, "competing operation"); err == nil || !strings.Contains(err.Error(), "ingress cutover") {
		t.Fatalf("competing operation was not rejected: %v", err)
	}
	close(platform.unblock)

	if err := <-result; err == nil || !strings.Contains(err.Error(), "instance preflight unavailable") {
		t.Fatalf("preflight error = %v", err)
	}
	if _, err := app.toolIngressPrepareDirect(ctx, map[string]any{"tenant_id": id}); err == nil || !strings.Contains(err.Error(), "instance preflight unavailable") {
		t.Fatalf("retry error = %v", err)
	}
	if platform.calls != 2 {
		t.Fatalf("instance_get calls = %d, want initial attempt plus retry", platform.calls)
	}
}

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
