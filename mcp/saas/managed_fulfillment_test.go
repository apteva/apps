package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

type integrationToolCall struct {
	ConnectionID int64
	Tool         string
	Input        map[string]any
}

func TestIntegrationFulfillmentMigrationPreservesExistingActions(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{
		"001_init.sql", "002_fulfillment_actions.sql", "003_reliability.sql",
		"004_commerce_orchestration.sql", "005_checkout_orchestration.sql",
		"006_quota_events.sql", "007_billing_read_model.sql", "008_discount_orchestration.sql",
		"009_plan_changes.sql", "010_fulfillment_persistence.sql",
		"011_automatic_collection_default.sql", "012_crm_customer_sync.sql",
	} {
		raw, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO saas_plans(key,project_id,name) VALUES('free','p','Free')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO saas_plan_actions(project_id,plan_key,event,app_name,tool_name) VALUES('p','free','account_active','containers','containers_create')`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/013_integration_fulfillment.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	var executionKind string
	if err := db.QueryRow(`SELECT execution_kind FROM saas_plan_actions`).Scan(&executionKind); err != nil {
		t.Fatal(err)
	}
	if executionKind != "app" {
		t.Fatalf("execution_kind=%q, want app", executionKind)
	}
}

func (p *platformStub) EnsureManagedConnection(req sdk.ManagedConnectionRequest) (*sdk.PlatformConnection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.managedConnectionRequests = append(p.managedConnectionRequests, req)
	return &sdk.PlatformConnection{
		ID:                   481,
		AppSlug:              req.AppSlug,
		Name:                 req.Name,
		Status:               "active",
		ProjectID:            req.ProjectID,
		CredentialManagement: "app",
		ExportPolicy:         sdk.ExportNever,
	}, nil
}

func (p *platformStub) RotateManagedConnection(int64, sdk.ManagedConnectionRotation) (*sdk.PlatformConnection, error) {
	return nil, nil
}

func (p *platformStub) RevokeManagedConnection(int64) error { return nil }

func (p *platformStub) EnsureManagedTenant(req sdk.ManagedTenantRequest) (*sdk.ManagedTenant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.managedTenantRequests = append(p.managedTenantRequests, req)
	return &sdk.ManagedTenant{TenantID: req.TenantID, AccountID: req.AccountID, Status: "active"}, nil
}

func (p *platformStub) CreateManagedTenantEnrollment(req sdk.ManagedTenantEnrollmentRequest) (*sdk.ManagedTenantEnrollment, error) {
	return &sdk.ManagedTenantEnrollment{TenantID: req.TenantID}, nil
}

func (p *platformStub) EnsureManagedConnectionGrant(req sdk.ManagedConnectionGrantRequest) (*sdk.ManagedConnectionGrant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.managedGrantRequests = append(p.managedGrantRequests, req)
	return &sdk.ManagedConnectionGrant{
		TenantID: req.TenantID, GrantID: req.GrantID, ConnectionID: req.ConnectionID,
		AppSlug: req.AppSlug, ProjectID: req.ProjectID, Status: "active", AllowedTools: req.AllowedTools,
	}, nil
}

func (p *platformStub) GetManagedConnectionGrantDelivery(tenantID, grantID string) (*sdk.ManagedConnectionGrantDelivery, error) {
	return &sdk.ManagedConnectionGrantDelivery{
		TenantID: tenantID, GrantID: grantID, ConnectionID: 72, AppSlug: "twilio",
		ControllerToken: "delegated-secret", ControllerExecute: "https://controller.example/api/managed/grants/phone/execute",
		AllowedTools: []string{"calls_create"}, PublicFields: map[string]string{"phone_number": "+15551234567"},
	}, nil
}

func (p *platformStub) RevokeManagedConnectionGrant(string, string) error { return nil }

func (p *platformStub) EnsureManagedTenantBundle(req sdk.ManagedTenantBundleRequest) (*sdk.ManagedTenantBundle, error) {
	return &sdk.ManagedTenantBundle{TenantID: req.TenantID, BundleID: req.BundleID, Apps: req.Apps}, nil
}

func (p *platformStub) GetManagedTenantBundle(tenantID, bundleID string) (*sdk.ManagedTenantBundle, error) {
	return &sdk.ManagedTenantBundle{TenantID: tenantID, BundleID: bundleID}, nil
}

func (p *platformStub) ExecuteIntegrationTool(connectionID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.integrationCalls = append(p.integrationCalls, integrationToolCall{ConnectionID: connectionID, Tool: tool, Input: input})
	if p.integrationResult != nil {
		return p.integrationResult, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"status":"applied"}`)}, nil
}

func TestManagedConnectionSetupAndIntegrationFulfillment(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}

	created, err := app.toolAccountCreate(ctx, map[string]any{
		"owner_email": "owner@example.com", "slug": "phone-acme", "plan_key": "free",
	})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	if _, err := app.toolConnectionEnsure(ctx, map[string]any{
		"account_id": acct.ID,
		"key":        "instance:" + acct.ID,
		"app_slug":   "apteva-instance",
		"name":       "Customer instance",
		"fields": map[string]any{
			"base_url": "https://customer.example",
			"api_key":  "instance-secret",
		},
		"metadata_key": "instance_connection_id",
	}); err != nil {
		t.Fatal(err)
	}
	acct, err = dbAccountGet(db, "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64FromAny(mapFromAny(acct.Metadata)["instance_connection_id"]); got != 481 {
		t.Fatalf("stored connection id=%d, want 481", got)
	}
	if len(pf.managedConnectionRequests) != 1 || pf.managedConnectionRequests[0].Fields["api_key"] != "instance-secret" || pf.managedConnectionRequests[0].Key != "saas:account:"+acct.ID+":instance:"+acct.ID {
		t.Fatalf("managed connection request=%+v", pf.managedConnectionRequests)
	}

	actionOut, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key":       "free",
		"event":          "account_active",
		"execution_kind": "integration_execute",
		"tool_name":      "provision_apply",
		"args": map[string]any{
			"connection_id": "{{account.metadata.instance_connection_id}}",
			"managed": map[string]any{
				"tenant_id": "{{account.id}}",
				"grants": []any{map[string]any{
					"grant_id": "phone", "provider_connection_id": 72, "app_slug": "twilio",
					"allowed_tools": []any{"calls_create"}, "public_fields": map[string]any{"phone_number": "+15551234567"},
				}},
				"bundle": map[string]any{
					"bundle_id": "phone",
					"apps":      []any{map[string]any{"key": "telephony", "manifest_url": "https://apps.example/telephony/apteva.yaml"}},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	action := actionOut.(map[string]any)["action"].(*PlanAction)
	if action.ExecutionKind != "integration_execute" || action.AppName != "" {
		t.Fatalf("action=%+v", action)
	}

	if _, err := app.toolFulfillmentRun(ctx, map[string]any{"account_id": acct.ID, "event": "account_active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolFulfillmentRun(ctx, map[string]any{"account_id": acct.ID, "event": "account_active"}); err != nil {
		t.Fatal(err)
	}
	if len(pf.integrationCalls) != 1 {
		t.Fatalf("integration calls=%d, want one idempotent call", len(pf.integrationCalls))
	}
	call := pf.integrationCalls[0]
	if call.ConnectionID != 481 || call.Tool != "provision_apply" {
		t.Fatalf("integration call=%+v", call)
	}
	if strFromAny(call.Input["tenant_id"]) != acct.ID || !strings.HasPrefix(strFromAny(call.Input["request_id"]), "saas_") {
		t.Fatalf("provisioning identity missing: %+v", call.Input)
	}
	bundle := mapFromAny(call.Input["bundle"])
	if int64FromAny(bundle["revision"]) <= 0 || bundle["bundle_id"] != "phone" {
		t.Fatalf("bundle revision missing: %+v", bundle)
	}
	grants := sliceFromAny(call.Input["grants"])
	if len(grants) != 1 || mapFromAny(grants[0])["controller_token"] != "delegated-secret" {
		t.Fatalf("grant delivery missing from in-memory request: %+v", grants)
	}

	var storedInput, storedOutput string
	if err := db.QueryRow(`SELECT input_json,output_json FROM saas_fulfillment_runs WHERE plan_action_id=?`, action.ID).Scan(&storedInput, &storedOutput); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"instance-secret", "delegated-secret"} {
		if strings.Contains(storedInput, secret) || strings.Contains(storedOutput, secret) {
			t.Fatalf("fulfillment persistence leaked %q: input=%s output=%s", secret, storedInput, storedOutput)
		}
	}
	var accountMetadata, actionArgs, eventPayloads string
	if err := db.QueryRow(`SELECT metadata_json FROM saas_accounts WHERE id=?`, acct.ID).Scan(&accountMetadata); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT args_json FROM saas_plan_actions WHERE id=?`, action.ID).Scan(&actionArgs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(GROUP_CONCAT(payload_json), '') FROM saas_events WHERE account_id=?`, acct.ID).Scan(&eventPayloads); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"instance-secret", "delegated-secret"} {
		if strings.Contains(accountMetadata, secret) || strings.Contains(actionArgs, secret) || strings.Contains(eventPayloads, secret) {
			t.Fatalf("SaaS persistence leaked %q", secret)
		}
	}
}

func TestIntegrationFulfillmentPropagatesNon2xx(t *testing.T) {
	pf := &platformStub{integrationResult: &sdk.ExecuteResult{Success: false, Status: 409, Data: json.RawMessage(`{"error":"conflict"}`)}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "conflict", "plan_key": "free"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	if _, err := db.Exec(`UPDATE saas_accounts SET metadata_json='{"instance_connection_id":481}' WHERE id=?`, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "free", "event": "account_active", "execution_kind": "integration_execute", "tool_name": "health",
		"args": map[string]any{"connection_id": "{{account.metadata.instance_connection_id}}", "input": map[string]any{}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolFulfillmentRun(ctx, map[string]any{"account_id": acct.ID, "event": "account_active"}); err == nil || !strings.Contains(err.Error(), "status 409") {
		t.Fatalf("expected non-2xx fulfillment error, got %v", err)
	}
}
