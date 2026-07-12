package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type fleetIngressPlatform struct {
	tk.BasePlatformClient
	exposed   []sdk.IngressExposeRequest
	unexposed []string
	appCalls  []string
	exposeErr error
}

func (p *fleetIngressPlatform) CallApp(appName, method string, input map[string]any) (json.RawMessage, error) {
	p.appCalls = append(p.appCalls, appName+"."+method)
	if appName == "instances" && method == "instance_open_tunnel" {
		return json.RawMessage(`{"result":{"content":[{"type":"text","text":"{\"local_host\":\"127.0.0.1\",\"local_port\":43123}"}]}}`), nil
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (p *fleetIngressPlatform) ExposeIngress(req sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	p.exposed = append(p.exposed, req)
	if p.exposeErr != nil {
		return nil, p.exposeErr
	}
	return &sdk.IngressRoute{
		Hostname:  req.Hostname,
		Target:    req.Target,
		OwnerKind: req.OwnerKind,
		CertFQDN:  req.CertFQDN,
	}, nil
}

func TestAttachDomainDoesNotPersistWhenIngressFails(t *testing.T) {
	pf := &fleetIngressPlatform{exposeErr: errors.New("ingress unavailable")}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	tenantID := seedTenant(t, app, "acme", StatusActive)
	tenant, _, _ := app.store.get(tenantID)
	err := app.attachDomain(ctx, "proj-1", tenant, attachDomainSpec{FQDN: "tenant.example.com", ManageDNS: false})
	if err == nil || !strings.Contains(err.Error(), "ingress unavailable") {
		t.Fatalf("attach error = %v", err)
	}
	updated, _, _ := app.store.get(tenantID)
	if updated.Domain != "" || updated.DomainRecordID != "" {
		t.Fatalf("failed ingress was persisted: %+v", updated)
	}
}

func (p *fleetIngressPlatform) UnexposeIngress(hostname string) error {
	p.unexposed = append(p.unexposed, hostname)
	return nil
}

func TestAttachDomainClientManagedUsesPlatformIngressOnly(t *testing.T) {
	pf := &fleetIngressPlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	tenantID := seedTenant(t, app, "acme", StatusActive)
	tenant, _, err := app.store.get(tenantID)
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}

	if err := app.attachDomain(ctx, "proj-1", tenant, attachDomainSpec{
		FQDN:      "Tenant.Example.COM.",
		ManageDNS: false,
	}); err != nil {
		t.Fatalf("attachDomain: %v", err)
	}

	if len(pf.appCalls) != 0 {
		t.Fatalf("unexpected legacy app calls: %#v", pf.appCalls)
	}
	if len(pf.exposed) != 1 {
		t.Fatalf("ExposeIngress calls = %d, want 1", len(pf.exposed))
	}
	got := pf.exposed[0]
	if got.Hostname != "tenant.example.com" || got.Target != "http://127.0.0.1:65535" ||
		got.OwnerKind != "fleet" || got.CertFQDN != "tenant.example.com" {
		t.Fatalf("unexpected ExposeIngress request: %+v", got)
	}

	updated, _, err := app.store.get(tenantID)
	if err != nil {
		t.Fatalf("get updated tenant: %v", err)
	}
	if updated.Domain != "tenant.example.com" || updated.DomainRecordID != "" {
		t.Fatalf("domain state mismatch: %+v", updated)
	}
}

func TestRegisterHostedRouteUsesSSHTunnel(t *testing.T) {
	pf := &fleetIngressPlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	tenant := &Tenant{ID: "tenant-1", Slug: "acme", Kind: KindLocal, InstanceID: 9, BaseURL: "http://203.0.113.10:7100", ConfigDir: remoteFleetRoot + "/acme"}
	if err := app.registerRouteForTenantHost(ctx, tenant, "acme.example.com", "acme.example.com", "test"); err != nil {
		t.Fatalf("register route: %v", err)
	}
	if len(pf.exposed) != 1 || pf.exposed[0].Target != "http://127.0.0.1:43123" {
		t.Fatalf("hosted ingress target = %+v", pf.exposed)
	}
	if pf.exposed[0].Target == tenant.BaseURL {
		t.Fatal("hosted route exposed the tenant's public HTTP endpoint")
	}
}

func TestDetachDomainClientManagedUsesPlatformIngressOnly(t *testing.T) {
	pf := &fleetIngressPlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	tenantID := seedTenant(t, app, "acme", StatusActive)
	tenant, _, err := app.store.get(tenantID)
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	if err := app.store.setDomain(tenant.ID, "tenant.example.com", "", nowUTC()); err != nil {
		t.Fatalf("set domain: %v", err)
	}
	tenant, _, err = app.store.get(tenantID)
	if err != nil {
		t.Fatalf("get tenant with domain: %v", err)
	}

	if err := app.detachDomain(ctx, "proj-1", tenant); err != nil {
		t.Fatalf("detachDomain: %v", err)
	}

	if len(pf.appCalls) != 0 {
		t.Fatalf("unexpected legacy app calls: %#v", pf.appCalls)
	}
	if !reflect.DeepEqual(pf.unexposed, []string{"tenant.example.com"}) {
		t.Fatalf("unexposed = %#v", pf.unexposed)
	}
	updated, _, err := app.store.get(tenantID)
	if err != nil {
		t.Fatalf("get updated tenant: %v", err)
	}
	if updated.Domain != "" || updated.DomainRecordID != "" {
		t.Fatalf("domain was not cleared: %+v", updated)
	}
}
