package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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
	db.SetMaxOpenConns(1)
	migrations, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(migrations)
	for _, migration := range migrations {
		raw, err := os.ReadFile(migration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply %s: %v", migration, err)
		}
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
	if m.Version != "1.2.0" {
		t.Errorf("manifest.Version=%q, want 1.2.0", m.Version)
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
	if !gotRequired["catalog"] {
		t.Error("manifest should require catalog")
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
	if free.ProductKey != "apteva" {
		t.Errorf("free product_key=%q, want apteva", free.ProductKey)
	}
	if byKey["docker-free"] == nil || byKey["docker-free"].ProductKey != "custom-docker" {
		t.Fatalf("docker-free plan missing or unbound: %+v", byKey["docker-free"])
	}
	if byKey["wordpress-free"] == nil || byKey["wordpress-free"].ProductKey != "wordpress-single" {
		t.Fatalf("wordpress-free plan missing or unbound: %+v", byKey["wordpress-free"])
	}
}

func TestProductListSeedsCatalog(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()

	app := &App{}
	out, err := app.toolProductList(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	products := out.(map[string]any)["products"].([]*Product)
	byKey := map[string]*Product{}
	for _, p := range products {
		byKey[p.Key] = p
	}
	for _, key := range []string{"apteva", "custom-docker", "wordpress-single"} {
		if byKey[key] == nil {
			t.Fatalf("product %q missing from %+v", key, products)
		}
		if len(byKey[key].Versions) == 0 {
			t.Fatalf("product %q has no versions", key)
		}
		if len(byKey[key].Plans) == 0 {
			t.Fatalf("product %q has no plans", key)
		}
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
	if got := pf.callAppCalls[0].Input["blueprint_slug"]; got != "apteva" {
		t.Fatalf("blueprint_slug=%v, want apteva", got)
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

func TestFulfillmentProvisionReportsTenantToOrders(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	got, err := app.toolFulfillmentProvision(ctx, map[string]any{
		"order_id":       int64(10),
		"fulfillment_id": int64(20),
		"owner_email":    "owner@example.com",
		"slug":           "paid-wp",
		"plan_key":       "wordpress-free",
		"product_key":    "wordpress-single",
	})
	if err != nil {
		t.Fatalf("fulfillment provision: %v", err)
	}
	tenant := got.(map[string]any)["tenant"].(*Tenant)
	if tenant.Status != StatusActive {
		t.Fatalf("status=%s, want active", tenant.Status)
	}
	if !strings.Contains(string(tenant.Metadata), `"order_id":10`) || !strings.Contains(string(tenant.Metadata), `"fulfillment_id":20`) {
		t.Fatalf("metadata missing order trace: %s", tenant.Metadata)
	}
	var ordersCall *callAppCall
	for i := range pf.callAppCalls {
		c := &pf.callAppCalls[i]
		if c.App == "orders" && c.Tool == "fulfillments_update" {
			ordersCall = c
			break
		}
	}
	if ordersCall == nil {
		t.Fatalf("expected orders fulfillments_update call, got %+v", pf.callAppCalls)
	}
	if ordersCall.Input["id"] != int64(20) || ordersCall.Input["status"] != "succeeded" || ordersCall.Input["external_ref"] != tenant.ID {
		t.Fatalf("unexpected orders update input: %+v", ordersCall.Input)
	}
}

func TestTenantCreateCustomDockerProduct(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	got, err := app.toolTenantCreate(ctx, map[string]any{
		"owner_email": "owner@example.com",
		"slug":        "docker",
		"plan_key":    "docker-free",
		"product_key": "custom-docker",
		"runtime_config": map[string]any{
			"image":       "nginx:alpine",
			"port":        8080,
			"health_path": "/healthz",
			"env": map[string]any{
				"FOO": "bar",
			},
		},
	})
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	tenant := got.(map[string]any)["tenant"].(*Tenant)
	if tenant.Image != "nginx:alpine" {
		t.Fatalf("tenant image=%q, want nginx:alpine", tenant.Image)
	}
	run := pf.callAppCalls[0].Input
	if run["blueprint_slug"] != "custom-image" {
		t.Fatalf("blueprint_slug=%v, want custom-image", run["blueprint_slug"])
	}
	if run["image"] != "nginx:alpine" {
		t.Fatalf("image=%v, want nginx:alpine", run["image"])
	}
	ports := run["ports"].([]map[string]any)
	if ports[0]["container_port"] != 8080 {
		t.Fatalf("container_port=%v, want 8080", ports[0]["container_port"])
	}
	if run["health_path"] != "/healthz" {
		t.Fatalf("health_path=%v, want /healthz", run["health_path"])
	}
	env := run["env"].(map[string]any)
	if env["FOO"] != "bar" {
		t.Fatalf("env=%+v, want FOO=bar", env)
	}
	if !strings.Contains(string(tenant.Metadata), `"product_key":"custom-docker"`) {
		t.Fatalf("metadata=%s", tenant.Metadata)
	}
}

func TestTenantCreateRejectsPlanProductMismatch(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()

	app := &App{}
	_, err := app.toolTenantCreate(ctx, map[string]any{
		"owner_email": "owner@example.com",
		"slug":        "mismatch",
		"plan_key":    "free",
		"product_key": "custom-docker",
		"image":       "nginx:alpine",
	})
	if err == nil || !strings.Contains(err.Error(), "bound to product") {
		t.Fatalf("expected product mismatch error, got %v", err)
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
