package main

import (
	"encoding/json"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type fleetIngressPlatform struct {
	tk.BasePlatformClient
	exposed   []sdk.IngressExposeRequest
	unexposed []string
	appCalls  []string
}

func (p *fleetIngressPlatform) ExposeIngress(req sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	p.exposed = append(p.exposed, req)
	return &sdk.IngressRoute{
		Hostname:  req.Hostname,
		Target:    req.Target,
		OwnerKind: req.OwnerKind,
		CertFQDN:  req.CertFQDN,
	}, nil
}

func (p *fleetIngressPlatform) UnexposeIngress(hostname string) error {
	p.unexposed = append(p.unexposed, hostname)
	return nil
}

func (p *fleetIngressPlatform) CallApp(appName, method string, input map[string]any) (json.RawMessage, error) {
	p.appCalls = append(p.appCalls, appName+"."+method)
	return json.RawMessage(`{"ok":true}`), nil
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
