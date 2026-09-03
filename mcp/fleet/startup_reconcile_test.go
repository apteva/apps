package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type startupReconcilePlatform struct {
	tk.BasePlatformClient

	mu sync.Mutex

	instanceFailures int
	listFailures     int
	tunnelFailures   int
	exposeFailures   int
	permanentExpose  error

	instanceCalls int
	listCalls     int
	tunnelCalls   int
	exposeCalls   int

	routes            []sdk.IngressRoute
	successfulTargets map[string]string
	allRoutesChanged  chan struct{}
	releaseLastRoute  chan struct{}
	allChangedOnce    sync.Once
}

func (p *startupReconcilePlatform) CallApp(appName, method string, _ map[string]any) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if appName != "instances" {
		return nil, fmt.Errorf("unexpected app call %s.%s", appName, method)
	}
	switch method {
	case "instance_run_command":
		p.instanceCalls++
		if p.instanceFailures > 0 {
			p.instanceFailures--
			return nil, errors.New("platform app call: http 503: service unavailable")
		}
		return wrappedToolResult(map[string]any{"output": "listening", "exit_code": 0}), nil
	case "instance_open_tunnel":
		p.tunnelCalls++
		if p.tunnelFailures > 0 {
			p.tunnelFailures--
			return nil, errors.New("platform app call: http 502: bad gateway")
		}
		return wrappedToolResult(map[string]any{"local_host": "127.0.0.1", "local_port": 44733}), nil
	default:
		return nil, fmt.Errorf("unexpected instances tool %q", method)
	}
}

func (p *startupReconcilePlatform) ListIngressRoutes() ([]sdk.IngressRoute, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listCalls++
	if p.listFailures > 0 {
		p.listFailures--
		return nil, errors.New("platform ingress routes: http 503: service unavailable")
	}
	return append([]sdk.IngressRoute(nil), p.routes...), nil
}

func (p *startupReconcilePlatform) ExposeIngress(req sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	p.mu.Lock()
	p.exposeCalls++
	if p.permanentExpose != nil {
		err := p.permanentExpose
		p.mu.Unlock()
		return nil, err
	}
	if p.exposeFailures > 0 {
		p.exposeFailures--
		p.mu.Unlock()
		return nil, errors.New("platform ingress expose: http 502: bad gateway")
	}
	if p.successfulTargets == nil {
		p.successfulTargets = map[string]string{}
	}
	p.successfulTargets[req.Hostname] = req.Target
	allChanged := len(p.successfulTargets) == len(p.routes)
	if allChanged && p.allRoutesChanged != nil {
		p.allChangedOnce.Do(func() { close(p.allRoutesChanged) })
	}
	release := p.releaseLastRoute
	p.mu.Unlock()

	if allChanged && release != nil {
		<-release
	}
	return &sdk.IngressRoute{
		Hostname:  req.Hostname,
		Target:    req.Target,
		OwnerKind: req.OwnerKind,
		CertFQDN:  req.CertFQDN,
		Status:    "active",
	}, nil
}

func seedHostedStartupTenant(t *testing.T, app *App, routeCount int) (*Tenant, []sdk.IngressRoute) {
	t.Helper()
	tenantID := seedTenant(t, app, "hosted-startup", StatusActive)
	if err := app.store.setLocation(tenantID, 3, "http://203.0.113.10:7100", remoteFleetRoot+"/hosted-startup"); err != nil {
		t.Fatal(err)
	}
	if err := app.store.setDomain(tenantID, "agents.client.example", "", nowUTC()); err != nil {
		t.Fatal(err)
	}
	hostnames := []string{"agents.client.example"}
	for i := 1; i < routeCount; i++ {
		hostname := fmt.Sprintf("app-%d.client.example", i)
		if err := app.store.upsertTenantHost(&TenantHost{
			TenantID: tenantID,
			Hostname: hostname,
			Status:   "active",
		}); err != nil {
			t.Fatal(err)
		}
		hostnames = append(hostnames, hostname)
	}
	routes := make([]sdk.IngressRoute, 0, len(hostnames))
	for _, hostname := range hostnames {
		routes = append(routes, sdk.IngressRoute{
			Hostname:  hostname,
			Target:    "http://127.0.0.1:35501",
			OwnerKind: "fleet",
			Status:    "active",
		})
	}
	tenant, _, err := app.store.get(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	return tenant, routes
}

func TestOnMountWaitsForCompleteHostedRouteReconciliation(t *testing.T) {
	platform := &startupReconcilePlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(platform))
	tenant, routes := seedHostedStartupTenant(t, app, 7)

	platform.mu.Lock()
	platform.instanceFailures = 2
	platform.listFailures = 1
	platform.tunnelFailures = 1
	platform.exposeFailures = 1
	platform.routes = routes
	platform.allRoutesChanged = make(chan struct{})
	platform.releaseLastRoute = make(chan struct{})
	platform.mu.Unlock()

	remounted := &App{startupRetryDelays: []time.Duration{0, 0, 0}}
	mountDone := make(chan error, 1)
	go func() {
		mountDone <- remounted.OnMount(ctx)
	}()

	select {
	case <-platform.allRoutesChanged:
	case <-time.After(2 * time.Second):
		t.Fatal("startup did not replace all seven stale routes")
	}
	select {
	case err := <-mountDone:
		t.Fatalf("OnMount returned before the final route replacement completed: %v", err)
	default:
	}
	close(platform.releaseLastRoute)
	if err := <-mountDone; err != nil {
		t.Fatalf("OnMount: %v", err)
	}

	platform.mu.Lock()
	defer platform.mu.Unlock()
	if platform.instanceCalls != 3 {
		t.Fatalf("hosted port checks = %d, want 3", platform.instanceCalls)
	}
	if platform.listCalls != 2 {
		t.Fatalf("ingress route list calls = %d, want 2", platform.listCalls)
	}
	if platform.tunnelCalls != 2 {
		t.Fatalf("tunnel calls = %d, want 2", platform.tunnelCalls)
	}
	if len(platform.successfulTargets) != 7 {
		t.Fatalf("rewritten routes = %d, want 7", len(platform.successfulTargets))
	}
	for hostname, target := range platform.successfulTargets {
		if target != "http://127.0.0.1:44733" {
			t.Fatalf("route %s target = %q", hostname, target)
		}
	}

	events, err := remounted.store.recentEvents(tenant.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	for _, event := range events {
		if event.Kind == "hosted_ingress_reconciled" {
			payload, _ = event.Payload.(map[string]any)
			break
		}
	}
	if payload == nil {
		t.Fatal("missing hosted_ingress_reconciled event")
	}
	if payload["old_tunnel_port"] != float64(35501) ||
		payload["new_tunnel_port"] != float64(44733) ||
		payload["routes_rewritten"] != float64(7) {
		t.Fatalf("unexpected reconciliation payload: %#v", payload)
	}
}

func TestOnMountFailsWhenHostedRoutesCannotBeRewritten(t *testing.T) {
	platform := &startupReconcilePlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(platform))
	_, routes := seedHostedStartupTenant(t, app, 1)

	platform.mu.Lock()
	platform.routes = routes
	platform.permanentExpose = errors.New("platform ingress expose: http 500: internal error")
	platform.mu.Unlock()

	remounted := &App{startupRetryDelays: []time.Duration{0}}
	err := remounted.OnMount(ctx)
	if err == nil || !strings.Contains(err.Error(), "replace hosted ingress routes") {
		t.Fatalf("OnMount error = %v", err)
	}
}

func TestOnMountSkipsStoppedHostedTenantBeforeInstanceLookup(t *testing.T) {
	platform := &startupReconcilePlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(platform))
	tenantID := seedTenant(t, app, "stopped-hosted", StatusStopped)
	if err := app.store.setLocation(tenantID, 5, "http://203.0.113.50:7100", remoteFleetRoot+"/stopped-hosted"); err != nil {
		t.Fatal(err)
	}

	remounted := &App{startupRetryDelays: []time.Duration{0}}
	if err := remounted.OnMount(ctx); err != nil {
		t.Fatalf("OnMount should not look up an intentionally stopped tenant's instance: %v", err)
	}

	platform.mu.Lock()
	defer platform.mu.Unlock()
	if platform.instanceCalls != 0 || platform.tunnelCalls != 0 || platform.listCalls != 0 || platform.exposeCalls != 0 {
		t.Fatalf("stopped hosted tenant triggered reconciliation calls: instance=%d tunnel=%d list=%d expose=%d",
			platform.instanceCalls, platform.tunnelCalls, platform.listCalls, platform.exposeCalls)
	}

	tenant, _, err := remounted.store.get(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if tenant.Status != StatusStopped {
		t.Fatalf("stopped hosted tenant changed status to %q", tenant.Status)
	}
}
