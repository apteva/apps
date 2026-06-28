package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

type platformStub struct {
	tk.BasePlatformClient
	callAppCalls []callAppCall
	ingressCalls []sdk.IngressExposeRequest
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

func (p *platformStub) UnexposeIngress(string) error { return nil }

func newTestCtx(t *testing.T, pf sdk.PlatformClient) (*sdk.AppCtx, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
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
