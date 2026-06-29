package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

type platformStub struct {
	tk.BasePlatformClient
	callAppCalls  []callAppCall
	ingressCalls  []sdk.IngressExposeRequest
	unexposeHosts []string
}

type callAppCall struct {
	App   string
	Tool  string
	Input map[string]any
}

func (p *platformStub) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.callAppCalls = append(p.callAppCalls, callAppCall{App: appName, Tool: tool, Input: input})
	switch tool {
	case "containers_run", "containers_start", "containers_stop", "containers_restart", "containers_health":
		b, _ := json.Marshal(map[string]any{
			"workload": map[string]any{
				"id":            "wrk_test",
				"name":          input["name"],
				"status":        "running",
				"public_url":    "http://127.0.0.1:53123",
				"health_status": "ok",
				"ports": []map[string]any{{
					"container_port": 5280,
					"host_port":      53123,
					"bind_addr":      "127.0.0.1",
					"protocol":       "tcp",
				}},
			},
		})
		return json.Unmarshal(b, out)
	case "containers_logs":
		b, _ := json.Marshal(map[string]any{"workload_id": input["workload_id"], "logs": "hello"})
		return json.Unmarshal(b, out)
	case "containers_destroy":
		b, _ := json.Marshal(map[string]any{"destroyed": true})
		return json.Unmarshal(b, out)
	default:
		return nil
	}
}

func (p *platformStub) ExposeIngress(req sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	p.ingressCalls = append(p.ingressCalls, req)
	return &sdk.IngressRoute{ID: 1, Hostname: req.Hostname, Target: req.Target, Status: "active"}, nil
}

func (p *platformStub) UnexposeIngress(hostname string) error {
	p.unexposeHosts = append(p.unexposeHosts, hostname)
	return nil
}

func newTestCtx(t *testing.T, pf sdk.PlatformClient) (*sdk.AppCtx, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "hosting.db"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	manifest := app.Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{
		"default_base_domain": "hosted.example.com",
		"default_image":       "apteva:test",
	}, pf, nil)
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}

func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "hosting" {
		t.Errorf("manifest.Name=%q, want hosting", m.Name)
	}
	if m.Version != "1.0.1" {
		t.Errorf("manifest.Version=%q, want 1.0.1", m.Version)
	}
	if m.DB == nil {
		t.Fatal("manifest.DB missing")
	}
	if m.DB.Migrations != "migrations/" {
		t.Errorf("manifest.DB.Migrations=%q, want migrations/", m.DB.Migrations)
	}
	if len(m.Provides.UIPanels) != 1 || m.Provides.UIPanels[0].Entry != "/ui/HostingPanel.mjs" {
		t.Fatalf("hosting panel not declared correctly: %+v", m.Provides.UIPanels)
	}
	gotScopes := map[string]bool{}
	for _, s := range m.Scopes {
		gotScopes[string(s)] = true
	}
	if !gotScopes["global"] {
		t.Error("manifest missing global scope")
	}
	if !gotScopes["project"] {
		t.Error("manifest missing project scope")
	}
	gotRequired := map[string]bool{}
	for _, dep := range m.Requires.Apps {
		gotRequired[dep.Name] = !dep.Optional
	}
	if !gotRequired["containers"] {
		t.Error("manifest should require containers")
	}
}

func TestMCPTools_ManifestMatchesHandlers(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	declared := map[string]bool{}
	for _, tool := range m.Provides.MCPTools {
		declared[tool.Name] = true
	}
	implemented := map[string]bool{}
	for _, tool := range app.MCPTools() {
		implemented[tool.Name] = true
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares tool %q but no handler implements it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", name)
		}
	}
}

func TestSeedPlansIncludeFreePlanLimits(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()

	app := &App{}
	out, err := app.toolPlanList(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	plans := out.(map[string]any)["plans"].([]*Plan)
	byKey := map[string]*Plan{}
	for _, p := range plans {
		byKey[p.Key] = p
	}
	free := byKey["free"]
	if free == nil {
		t.Fatalf("free plan missing: %+v", plans)
	}
	if free.BillingMode != "free" {
		t.Errorf("free billing_mode=%q, want free", free.BillingMode)
	}
	limits := map[string]int64{}
	for _, l := range free.Limits {
		limits[l.FeatureKey] = l.LimitValue
	}
	if limits["hosting.tenants"] != 1 {
		t.Errorf("free hosting.tenants limit=%d, want 1", limits["hosting.tenants"])
	}
	if limits["containers.storage_mb"] != 512 {
		t.Errorf("free containers.storage_mb limit=%d, want 512", limits["containers.storage_mb"])
	}
}

func TestFreePlanLimit(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()

	c, err := dbCustomerUpsert(db, map[string]any{"email": "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := enforceTenantLimit(db, c.ID, "free"); err != nil {
		t.Fatalf("first tenant should be allowed: %v", err)
	}
	if err := dbTenantInsert(db, &Tenant{
		ID:              "htn_1",
		CustomerID:      c.ID,
		Slug:            "one",
		DefaultHostname: "one.hosted.example.com",
		OwnerEmail:      "owner@example.com",
		PlanKey:         "free",
		Status:          StatusActive,
		Image:           "apteva:test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := enforceTenantLimit(ctx.AppDB(), c.ID, "free"); err == nil {
		t.Fatal("second free tenant should be refused")
	}
}

func TestTenantCreateProvisionsContainerAndIngress(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	got, err := app.toolTenantCreate(ctx, map[string]any{
		"owner_email": "owner@example.com",
		"slug":        "acme",
		"plan_key":    "free",
	})
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	tenant := got.(map[string]any)["tenant"].(*Tenant)
	if tenant.Status != StatusActive {
		t.Fatalf("status = %s, want active", tenant.Status)
	}
	if tenant.WorkloadID != "wrk_test" {
		t.Fatalf("workload_id = %s", tenant.WorkloadID)
	}
	if tenant.DefaultHostname != "acme.hosted.example.com" {
		t.Fatalf("hostname = %s", tenant.DefaultHostname)
	}
	if len(pf.callAppCalls) == 0 || pf.callAppCalls[0].Tool != "containers_run" {
		t.Fatalf("expected containers_run call, got %+v", pf.callAppCalls)
	}
	if len(pf.ingressCalls) != 1 {
		t.Fatalf("expected one ingress call, got %d", len(pf.ingressCalls))
	}
	if pf.ingressCalls[0].Target != "http://127.0.0.1:53123" {
		t.Fatalf("ingress target = %s", pf.ingressCalls[0].Target)
	}
	usage, err := dbUsageTotals(db, map[string]any{"tenant_id": tenant.ID, "feature_key": "hosting.tenants"})
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].Quantity != 1 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestTenantLifecycleToolsCallContainers(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	created, err := app.toolTenantCreate(ctx, map[string]any{
		"owner_email": "owner@example.com",
		"slug":        "lifecycle",
		"plan_key":    "free",
	})
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	tenant := created.(map[string]any)["tenant"].(*Tenant)

	out, err := app.toolTenantSuspend(ctx, map[string]any{"tenant_id": tenant.ID})
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if got := out.(map[string]any)["tenant"].(*Tenant).Status; got != StatusSuspended {
		t.Fatalf("suspend status=%q, want %q", got, StatusSuspended)
	}

	out, err = app.toolTenantResume(ctx, map[string]any{"tenant_id": tenant.ID})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := out.(map[string]any)["tenant"].(*Tenant).Status; got != StatusActive {
		t.Fatalf("resume status=%q, want %q", got, StatusActive)
	}

	if _, err := app.toolTenantRestart(ctx, map[string]any{"tenant_id": tenant.ID}); err != nil {
		t.Fatalf("restart: %v", err)
	}
	out, err = app.toolTenantHealth(ctx, map[string]any{"tenant_id": tenant.ID})
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if got := out.(map[string]any)["tenant"].(*Tenant).LastHealthStatus; got != "ok" {
		t.Fatalf("health=%q, want ok", got)
	}
	logs, err := app.toolTenantLogs(ctx, map[string]any{"tenant_id": tenant.ID, "tail": 10})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if logs.(map[string]any)["logs"] != "hello" {
		t.Fatalf("logs response=%+v", logs)
	}
	out, err = app.toolTenantDelete(ctx, map[string]any{"tenant_id": tenant.ID, "delete_volumes": true})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := out.(map[string]any)["tenant"].(*Tenant).Status; got != StatusDeleted {
		t.Fatalf("delete status=%q, want %q", got, StatusDeleted)
	}
	if len(pf.unexposeHosts) != 1 || pf.unexposeHosts[0] != "lifecycle.hosted.example.com" {
		t.Fatalf("unexpose calls=%+v", pf.unexposeHosts)
	}

	tools := []string{}
	for _, c := range pf.callAppCalls {
		tools = append(tools, c.Tool)
	}
	for _, want := range []string{
		"containers_run",
		"containers_stop",
		"containers_start",
		"containers_restart",
		"containers_health",
		"containers_logs",
		"containers_destroy",
	} {
		if !containsString(tools, want) {
			t.Fatalf("expected %s in container calls, got %v", want, tools)
		}
	}
}

func TestHTTPHandlersServePanelData(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	created, err := app.toolTenantCreate(ctx, map[string]any{
		"owner_email": "owner@example.com",
		"slug":        "panel",
		"plan_key":    "free",
	})
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	tenant := created.(map[string]any)["tenant"].(*Tenant)

	req := httptest.NewRequest(http.MethodGet, "/plans", nil)
	rec := httptest.NewRecorder()
	app.handlePlans(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"free"`) {
		t.Fatalf("plans response code=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/tenants", nil)
	rec = httptest.NewRecorder()
	app.handleTenants(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tenant.ID) {
		t.Fatalf("tenants response code=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/usage?tenant_id="+tenant.ID, nil)
	rec = httptest.NewRecorder()
	app.handleUsage(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"hosting.tenants"`) {
		t.Fatalf("usage response code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func containsString(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}
