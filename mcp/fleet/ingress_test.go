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
	exposed     []sdk.IngressExposeRequest
	unexposed   []string
	appCalls    []string
	exposeErr   error
	unexposeErr error
	bindings    map[string]any
}

func (p *fleetIngressPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: p.bindings}, nil
}

func (p *fleetIngressPlatform) CallApp(appName, method string, input map[string]any) (json.RawMessage, error) {
	p.appCalls = append(p.appCalls, appName+"."+method)
	if appName == "instances" && method == "instance_open_tunnel" {
		return json.RawMessage(`{"result":{"content":[{"type":"text","text":"{\"local_host\":\"127.0.0.1\",\"local_port\":43123}"}]}}`), nil
	}
	if appName == "domains" && method == "domain_list" {
		return json.RawMessage(`{"result":{"content":[{"type":"text","text":"{\"domains\":[{\"name\":\"example.com\"}]}"}]}}`), nil
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
	return p.unexposeErr
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

func TestNormaliseExactHostnameRejectsWildcardsAndURLs(t *testing.T) {
	for _, input := range []string{"*.example.com", "https://app.example.com", "127.0.0.1", "localhost", "bad_label.example.com"} {
		if _, err := normaliseExactHostname(input); err == nil {
			t.Errorf("normaliseExactHostname(%q) accepted an invalid exact hostname", input)
		}
	}
	got, err := normaliseExactHostname("Commerciaux.Test.Flexylead.COM.")
	if err != nil || got != "commerciaux.test.flexylead.com" {
		t.Fatalf("normalise exact hostname = %q, %v", got, err)
	}
}

func TestAttachTenantHostPersistsExactIngressAndInfersGrant(t *testing.T) {
	pf := &fleetIngressPlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	tenantID := seedTenant(t, app, "acme", StatusActive)
	tenant, _, _ := app.store.get(tenantID)
	grant := &DomainGrant{TenantID: tenantID, Domain: "test.flexylead.com", Wildcard: true, Status: "active"}
	if err := app.store.upsertDomainGrant(grant); err != nil {
		t.Fatalf("upsert grant: %v", err)
	}

	host, err := app.attachTenantHost(ctx, tenant, "Commerciaux.Test.Flexylead.COM.", 0)
	if err != nil {
		t.Fatalf("attach tenant host: %v", err)
	}
	if host.Hostname != "commerciaux.test.flexylead.com" || host.DomainGrantID != grant.ID || host.Status != "active" {
		t.Fatalf("host = %+v", host)
	}
	if len(pf.exposed) != 1 {
		t.Fatalf("exposed = %+v", pf.exposed)
	}
	if got := pf.exposed[0]; got.Hostname != host.Hostname || got.CertFQDN != host.Hostname || strings.Contains(got.Hostname, "*") {
		t.Fatalf("exact ingress = %+v", got)
	}
}

func TestDomainGrantWritesDNSWithoutRegisteringGrantIngress(t *testing.T) {
	pf := &fleetIngressPlatform{bindings: map[string]any{"domains": float64(1)}}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	tenantID := seedTenant(t, app, "grant-dns-only", StatusActive)
	tenant, _, _ := app.store.get(tenantID)

	grant, err := app.grantDomain(ctx, "proj-1", tenant, attachDomainSpec{
		FQDN: "apps.example.com", Target: "203.0.113.5", Type: "A", ManageDNS: true,
	})
	if err != nil {
		t.Fatalf("grant domain: %v", err)
	}
	if grant.Domain != "apps.example.com" || grant.DomainRecordID == "" || grant.WildcardRecordID == "" {
		t.Fatalf("grant = %+v", grant)
	}
	if len(pf.exposed) != 0 {
		t.Fatalf("domain grant registered ingress: %+v", pf.exposed)
	}
	for _, call := range pf.appCalls {
		if !strings.HasPrefix(call, "domains.") {
			t.Fatalf("domain grant made unexpected app call: %s", call)
		}
	}
}

func TestAttachTenantHostKeepsFailedIntentForReconciliation(t *testing.T) {
	pf := &fleetIngressPlatform{exposeErr: errors.New("temporary ingress failure")}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	tenantID := seedTenant(t, app, "acme", StatusActive)
	tenant, _, _ := app.store.get(tenantID)

	if _, err := app.attachTenantHost(ctx, tenant, "app.client.com", 0); err == nil {
		t.Fatal("attach tenant host succeeded with failing ingress")
	}
	stored, err := app.store.getTenantHost(tenantID, "app.client.com")
	if err != nil || stored == nil {
		t.Fatalf("stored failed intent = %+v, %v", stored, err)
	}
	if stored.Status != "error" || !strings.Contains(stored.LastError, "temporary ingress failure") {
		t.Fatalf("stored failed intent = %+v", stored)
	}
}

func TestTenantHostnameCannotBelongToTwoTenants(t *testing.T) {
	pf := &fleetIngressPlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	firstID := seedTenant(t, app, "first", StatusActive)
	secondID := seedTenant(t, app, "second", StatusActive)
	first, _, _ := app.store.get(firstID)
	second, _, _ := app.store.get(secondID)
	if _, err := app.attachTenantHost(ctx, first, "app.client.com", 0); err != nil {
		t.Fatalf("attach first: %v", err)
	}
	if _, err := app.attachTenantHost(ctx, second, "app.client.com", 0); err == nil || !strings.Contains(err.Error(), "another tenant") {
		t.Fatalf("attach duplicate error = %v", err)
	}
}

func TestRefreshTenantIngressTargetsUsesStoredExactHostsNotGrantWildcard(t *testing.T) {
	pf := &fleetIngressPlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	tenantID := seedTenant(t, app, "acme", StatusActive)
	if err := app.store.setLocation(tenantID, 9, "http://203.0.113.10:7100", remoteFleetRoot+"/acme"); err != nil {
		t.Fatalf("set location: %v", err)
	}
	if err := app.store.setDomain(tenantID, "agents.client.com", "", nowUTC()); err != nil {
		t.Fatalf("set tenant domain: %v", err)
	}
	grant := &DomainGrant{TenantID: tenantID, Domain: "test.flexylead.com", Wildcard: true, Status: "active"}
	if err := app.store.upsertDomainGrant(grant); err != nil {
		t.Fatalf("upsert grant: %v", err)
	}
	for _, hostname := range []string{"commerciaux.test.flexylead.com", "dashboard.test.flexylead.com"} {
		if err := app.store.upsertTenantHost(&TenantHost{
			TenantID: tenantID, Hostname: hostname, DomainGrantID: grant.ID, Status: "pending",
		}); err != nil {
			t.Fatalf("upsert host %s: %v", hostname, err)
		}
	}
	tenant, _, _ := app.store.get(tenantID)
	if err := app.refreshTenantIngressTargets(ctx, tenant, "http://127.0.0.1:43123"); err != nil {
		t.Fatalf("refresh ingress: %v", err)
	}

	want := map[string]bool{
		"agents.client.com":              false,
		"commerciaux.test.flexylead.com": false,
		"dashboard.test.flexylead.com":   false,
	}
	for _, exposed := range pf.exposed {
		if strings.Contains(exposed.Hostname, "*") || exposed.Hostname == grant.Domain {
			t.Fatalf("grant-level ingress was exposed: %+v", exposed)
		}
		if _, ok := want[exposed.Hostname]; !ok {
			t.Fatalf("unexpected ingress: %+v", exposed)
		}
		want[exposed.Hostname] = true
		if exposed.Target != "http://127.0.0.1:43123" {
			t.Fatalf("target = %q", exposed.Target)
		}
	}
	for hostname, seen := range want {
		if !seen {
			t.Errorf("hostname %s was not refreshed", hostname)
		}
	}
}

func TestRefreshTenantIngressSupportsLocalMigrationTargets(t *testing.T) {
	pf := &fleetIngressPlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	tenantID := seedTenant(t, app, "local-target", StatusActive)
	if err := app.store.upsertTenantHost(&TenantHost{
		TenantID: tenantID, Hostname: "app.client.com", Status: "pending",
	}); err != nil {
		t.Fatalf("upsert host: %v", err)
	}
	tenant, _, _ := app.store.get(tenantID)
	if err := app.refreshTenantIngress(ctx, tenant); err != nil {
		t.Fatalf("refresh local ingress: %v", err)
	}
	if len(pf.exposed) != 1 || pf.exposed[0].Hostname != "app.client.com" || pf.exposed[0].Target != "http://127.0.0.1:65535" {
		t.Fatalf("local migration ingress = %+v", pf.exposed)
	}
}

func TestRemoveTenantHostDoesNotTouchDNS(t *testing.T) {
	pf := &fleetIngressPlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(pf))
	tenantID := seedTenant(t, app, "acme", StatusActive)
	tenant, _, _ := app.store.get(tenantID)
	if _, err := app.attachTenantHost(ctx, tenant, "app.client.com", 0); err != nil {
		t.Fatalf("attach host: %v", err)
	}
	pf.appCalls = nil
	if _, err := app.removeTenantHost(ctx, tenant, "app.client.com"); err != nil {
		t.Fatalf("remove host: %v", err)
	}
	if !reflect.DeepEqual(pf.unexposed, []string{"app.client.com"}) {
		t.Fatalf("unexposed = %#v", pf.unexposed)
	}
	if len(pf.appCalls) != 0 {
		t.Fatalf("host removal touched an app dependency: %#v", pf.appCalls)
	}
	stored, err := app.store.getTenantHost(tenantID, "app.client.com")
	if err != nil || stored != nil {
		t.Fatalf("stored host after removal = %+v, %v", stored, err)
	}
}

func TestTenantHardDeleteCascadesHostMappings(t *testing.T) {
	app, _ := newTestApp(t)
	tenantID := seedTenant(t, app, "delete-hosts", StatusStopped)
	if err := app.store.upsertTenantHost(&TenantHost{
		TenantID: tenantID, Hostname: "app.client.com", Status: "active",
	}); err != nil {
		t.Fatalf("upsert host: %v", err)
	}
	if err := app.store.hardDelete(tenantID); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	hosts, err := app.store.listTenantHosts(tenantID)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	if len(hosts) != 0 {
		t.Fatalf("orphaned tenant hosts: %+v", hosts)
	}
}
