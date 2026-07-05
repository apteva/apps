package main

import (
	"database/sql"
	"errors"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

type ingressPlatformStub struct {
	testkit.BasePlatformClient
	exposed       []sdk.IngressExposeRequest
	unexposed     []string
	routes        []sdk.IngressRoute
	unexposeError error
}

func (p *ingressPlatformStub) ExposeIngress(req sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	p.exposed = append(p.exposed, req)
	route := sdk.IngressRoute{
		ID:        int64(len(p.exposed)),
		Hostname:  req.Hostname,
		Target:    req.Target,
		OwnerKind: req.OwnerKind,
		CertFQDN:  req.CertFQDN,
		AllowHTTP: req.AllowHTTP,
		TLSMode:   req.TLSMode,
		Status:    "active",
	}
	if route.TLSMode == "" {
		route.TLSMode = "auto"
	}
	p.routes = append(p.routes, route)
	return &route, nil
}

func (p *ingressPlatformStub) UnexposeIngress(hostname string) error {
	p.unexposed = append(p.unexposed, hostname)
	if p.unexposeError != nil {
		return p.unexposeError
	}
	return nil
}

func (p *ingressPlatformStub) ListIngressRoutes() ([]sdk.IngressRoute, error) {
	return p.routes, nil
}

func testIngressCtx(t *testing.T, pf *ingressPlatformStub) *sdk.AppCtx {
	t.Helper()
	app := &App{}
	manifest := app.Manifest()
	return sdk.NewAppCtxForTest(&manifest, openTestDB(t), sdk.Config{}, pf, nil).WithProject("proj-test")
}

func TestRoutesRegister_UsesServerNativeIngress(t *testing.T) {
	pf := &ingressPlatformStub{}
	ctx := testIngressCtx(t, pf)
	app := &App{}

	out, err := app.toolRoutesRegister(ctx, map[string]any{
		"hostname":   "acme.cloud.apteva.ai",
		"target":     "http://203.0.113.10:39819",
		"owner_kind": "saas",
		"tls_mode":   "auto",
	})
	if err != nil {
		t.Fatalf("routes_register: %v", err)
	}
	if len(pf.exposed) != 1 {
		t.Fatalf("ExposeIngress calls = %d, want 1", len(pf.exposed))
	}
	got := pf.exposed[0]
	if got.Hostname != "acme.cloud.apteva.ai" || got.Target != "http://203.0.113.10:39819" || got.OwnerKind != "saas" {
		t.Fatalf("unexpected expose request: %+v", got)
	}
	body := out.(map[string]any)
	if body["public_url"] != "https://acme.cloud.apteva.ai" {
		t.Fatalf("public_url = %v, want https://acme.cloud.apteva.ai", body["public_url"])
	}
	route := body["route"].(*sdk.IngressRoute)
	if route.Hostname != "acme.cloud.apteva.ai" {
		t.Fatalf("route hostname = %q", route.Hostname)
	}
}

func TestRoutesRegister_TLSOffPublicURL(t *testing.T) {
	pf := &ingressPlatformStub{}
	ctx := testIngressCtx(t, pf)
	app := &App{}

	out, err := app.toolRoutesRegister(ctx, map[string]any{
		"hostname": "local.example.test",
		"target":   "http://127.0.0.1:8080",
		"tls_mode": "off",
	})
	if err != nil {
		t.Fatalf("routes_register: %v", err)
	}
	body := out.(map[string]any)
	if body["public_url"] != "http://local.example.test" {
		t.Fatalf("public_url = %v, want http://local.example.test", body["public_url"])
	}
}

func TestRoutesUnregister_UsesServerNativeIngress(t *testing.T) {
	pf := &ingressPlatformStub{routes: []sdk.IngressRoute{{Hostname: "acme.cloud.apteva.ai"}}}
	ctx := testIngressCtx(t, pf)
	app := &App{}

	out, err := app.toolRoutesUnregister(ctx, map[string]any{"hostname": "acme.cloud.apteva.ai"})
	if err != nil {
		t.Fatalf("routes_unregister: %v", err)
	}
	if len(pf.unexposed) != 1 || pf.unexposed[0] != "acme.cloud.apteva.ai" {
		t.Fatalf("UnexposeIngress calls = %+v", pf.unexposed)
	}
	if out.(map[string]any)["removed"] != true {
		t.Fatalf("removed response = %+v", out)
	}
}

func TestRoutesUnregister_IsIdempotentWhenRouteMissing(t *testing.T) {
	pf := &ingressPlatformStub{}
	ctx := testIngressCtx(t, pf)
	app := &App{}

	out, err := app.toolRoutesUnregister(ctx, map[string]any{"hostname": "missing.cloud.apteva.ai"})
	if err != nil {
		t.Fatalf("routes_unregister missing route: %v", err)
	}
	if len(pf.unexposed) != 0 {
		t.Fatalf("UnexposeIngress should not be called for missing route: %+v", pf.unexposed)
	}
	if out.(map[string]any)["removed"] != false {
		t.Fatalf("removed response = %+v", out)
	}
}

func TestRoutesUnregister_TreatsConcurrentMissingAsRemovedFalse(t *testing.T) {
	pf := &ingressPlatformStub{
		routes:        []sdk.IngressRoute{{Hostname: "race.cloud.apteva.ai"}},
		unexposeError: sql.ErrNoRows,
	}
	ctx := testIngressCtx(t, pf)
	app := &App{}

	out, err := app.toolRoutesUnregister(ctx, map[string]any{"hostname": "race.cloud.apteva.ai"})
	if err != nil {
		t.Fatalf("routes_unregister concurrent missing route: %v", err)
	}
	if len(pf.unexposed) != 1 {
		t.Fatalf("UnexposeIngress calls = %+v", pf.unexposed)
	}
	if out.(map[string]any)["removed"] != false {
		t.Fatalf("removed response = %+v", out)
	}
}

func TestRoutesUnregister_ReturnsUnexpectedUnexposeError(t *testing.T) {
	pf := &ingressPlatformStub{
		routes:        []sdk.IngressRoute{{Hostname: "broken.cloud.apteva.ai"}},
		unexposeError: errors.New("proxy unavailable"),
	}
	ctx := testIngressCtx(t, pf)
	app := &App{}

	if _, err := app.toolRoutesUnregister(ctx, map[string]any{"hostname": "broken.cloud.apteva.ai"}); err == nil {
		t.Fatal("routes_unregister should return unexpected unexpose errors")
	}
}

func TestRoutesGetAndList_ReadServerNativeIngress(t *testing.T) {
	pf := &ingressPlatformStub{routes: []sdk.IngressRoute{{
		Hostname: "acme.cloud.apteva.ai",
		Target:   "http://203.0.113.10:39819",
		TLSMode:  "auto",
		Status:   "active",
	}}}
	ctx := testIngressCtx(t, pf)
	app := &App{}

	listOut, err := app.toolRoutesList(ctx, nil)
	if err != nil {
		t.Fatalf("routes_list: %v", err)
	}
	if listOut.(map[string]any)["count"] != 1 {
		t.Fatalf("routes_list count = %+v", listOut)
	}

	getOut, err := app.toolRoutesGet(ctx, map[string]any{"hostname": "ACME.cloud.apteva.ai"})
	if err != nil {
		t.Fatalf("routes_get: %v", err)
	}
	if getOut.(map[string]any)["public_url"] != "https://acme.cloud.apteva.ai" {
		t.Fatalf("routes_get response = %+v", getOut)
	}
}
