package main

import (
	"net"
	"os"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type deployIngressPlatform struct {
	tk.BasePlatformClient
	exposed   []sdk.IngressExposeRequest
	unexposed []string
	list      []sdk.IngressRoute
}

func (p *deployIngressPlatform) ExposeIngress(req sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	p.exposed = append(p.exposed, req)
	return &sdk.IngressRoute{
		Hostname:  req.Hostname,
		Target:    req.Target,
		ProjectID: req.ProjectID,
		OwnerKind: req.OwnerKind,
		CertFQDN:  req.CertFQDN,
	}, nil
}

func (p *deployIngressPlatform) UnexposeIngress(hostname string) error {
	p.unexposed = append(p.unexposed, hostname)
	return nil
}

func (p *deployIngressPlatform) ListIngressRoutes() ([]sdk.IngressRoute, error) {
	return p.list, nil
}

func TestRegisterRouteForDeploymentUsesPlatformIngress(t *testing.T) {
	pf := &deployIngressPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-1"), tk.WithPlatform(pf))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	d := seedLiveDeploymentForIngress(t, ctx, "api", "api.example.com", port, os.Getpid())
	registerRouteForDeployment(ctx, &App{}, d)

	if len(pf.exposed) != 1 {
		t.Fatalf("ExposeIngress calls = %d, want 1", len(pf.exposed))
	}
	got := pf.exposed[0]
	if got.Hostname != "api.example.com" || got.Target != "http://127.0.0.1:"+itoa(port) ||
		got.OwnerKind != "deploy" || got.CertFQDN != "api.example.com" || got.ProjectID != "proj-1" {
		t.Fatalf("unexpected ExposeIngress request: %+v", got)
	}
}

func TestUnregisterRouteForDeploymentUsesPlatformIngress(t *testing.T) {
	pf := &deployIngressPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(pf))

	unregisterRouteForDeployment(ctx, &App{}, "api.example.com")

	if !reflect.DeepEqual(pf.unexposed, []string{"api.example.com"}) {
		t.Fatalf("unexposed = %#v", pf.unexposed)
	}
}

func TestSweepPhantomRoutesUsesPlatformIngress(t *testing.T) {
	pf := &deployIngressPlatform{
		list: []sdk.IngressRoute{
			{Hostname: "keep.example.com", OwnerKind: "deploy"},
			{Hostname: "stale.example.com", OwnerKind: "deploy"},
			{Hostname: "fleet.example.com", OwnerKind: "fleet"},
		},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(pf))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	seedLiveDeploymentForIngress(t, ctx, "keep", "keep.example.com", 4567, os.Getpid())

	dropped := (&App{}).sweepPhantomRoutes(ctx)
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if !reflect.DeepEqual(pf.unexposed, []string{"stale.example.com"}) {
		t.Fatalf("unexposed = %#v", pf.unexposed)
	}
}

func seedLiveDeploymentForIngress(t *testing.T, ctx *sdk.AppCtx, name, domain string, port, pid int) *Deployment {
	t.Helper()
	d, err := dbCreateDeployment(ctx.AppDB(), "proj-1", CreateDeploymentInput{
		Name:       name,
		SourceKind: "local",
		SourceRef:  "/tmp/src",
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if err := dbSetDeploymentDomain(ctx.AppDB(), d.ID, domain, "", nowUTC()); err != nil {
		t.Fatalf("set domain: %v", err)
	}
	b, err := dbCreateBuild(ctx.AppDB(), d.ID, "go", "")
	if err != nil {
		t.Fatalf("create build: %v", err)
	}
	rel, err := dbCreateRelease(ctx.AppDB(), d.ID, b.ID)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := dbUpdateRelease(ctx.AppDB(), rel.ID, map[string]any{
		"status":     "live",
		"port":       port,
		"pid":        pid,
		"started_at": nowUTC(),
	}); err != nil {
		t.Fatalf("update release: %v", err)
	}
	if err := dbSetCurrentRelease(ctx.AppDB(), d.ID, &rel.ID); err != nil {
		t.Fatalf("set current release: %v", err)
	}
	d, err = dbGetDeployment(ctx.AppDB(), "proj-1", d.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	return d
}
