package main

import (
	"database/sql"
	"encoding/json"
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
	calls    []callAppCall
	contacts int64
	entitled bool
}

type callAppCall struct {
	App   string
	Tool  string
	Input map[string]any
}

func (p *platformStub) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, callAppCall{App: appName, Tool: tool, Input: input})
	var body map[string]any
	switch tool {
	case "auth_orgs_create":
		body = map[string]any{"organization": map[string]any{"id": 101, "slug": input["slug"], "name": input["name"]}}
	case "auth_users_create":
		body = map[string]any{"user": map[string]any{"id": 202, "email": input["email"], "organization_id": input["organization_id"]}}
	case "entitlement_grants_create":
		body = map[string]any{"grant": map[string]any{"id": 301, "feature_key": input["feature_key"], "subject_id": input["subject_id"]}}
	case "entitlement_limits_set":
		body = map[string]any{"limit": map[string]any{"id": 401, "feature_key": input["feature_key"], "limit_value": input["limit_value"]}}
	case "entitlements_check":
		body = map[string]any{"allowed": p.entitled}
	case "customers_upsert_by_email":
		body = map[string]any{"customer": map[string]any{"id": 501, "email": input["email"], "name": input["name"]}}
	case "catalog_prices_get":
		body = map[string]any{"price": map[string]any{"id": input["id"], "product_id": 7001, "unit_amount_cents": 2900, "currency": "USD", "interval": "month", "interval_count": 1}}
	case "subscriptions_create":
		body = map[string]any{"subscription": map[string]any{"id": 601, "status": input["status"], "customer_id": input["customer_id"], "items": input["items"]}}
	case "subscriptions_invoice_create":
		body = map[string]any{"invoice": map[string]any{"id": 701, "status": "open", "total_cents": 2900}, "cycle": map[string]any{"id": 801, "invoice_id": 701, "payment_status": "open"}}
	case "payments_record":
		body = map[string]any{"payment": map[string]any{"id": 901, "method": input["method"], "amount_cents": input["amount_cents"]}, "invoice": map[string]any{"id": input["invoice_id"], "status": "paid", "total_cents": input["amount_cents"], "amount_paid_cents": input["amount_cents"]}}
	case "invoices_send_payment_link":
		body = map[string]any{"url": "https://pay.example/session", "stripe_session_id": "cs_test_123", "expires_at": 123}
	case "payment_method_setup_create":
		body = map[string]any{"setup_session": map[string]any{"id": 1001, "customer_id": input["customer_id"], "status": "pending", "url": "https://pay.example/setup"}, "url": "https://pay.example/setup"}
	case "crm_saas_usage_snapshot":
		body = map[string]any{"usage": []map[string]any{{"feature_key": "crm:contacts", "quantity": p.contacts}}}
	case "contacts_search":
		body = map[string]any{"contacts": []any{}, "count": 0, "total": p.contacts, "offset": input["offset"]}
	default:
		body = map[string]any{"ok": true}
	}
	raw, _ := json.Marshal(body)
	return json.Unmarshal(raw, out)
}

func newTestCtx(t *testing.T, pf sdk.PlatformClient) (*sdk.AppCtx, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "saas.db"))
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	app := &App{}
	manifest := app.Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, pf, nil).WithProject("proj-test")
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}

func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "saas" {
		t.Errorf("manifest.Name=%q, want saas", m.Name)
	}
	if m.Version != "0.1.3" {
		t.Errorf("manifest.Version=%q, want 0.1.3", m.Version)
	}
	if !m.Requires.DynamicAppCalls {
		t.Error("manifest should allow dynamic app calls for configured usage sources")
	}
	if m.DB == nil || m.DB.Migrations != "migrations/" {
		t.Fatalf("manifest DB not declared correctly: %+v", m.DB)
	}
	required := map[string]bool{}
	for _, dep := range m.Requires.Apps {
		required[dep.Name] = !dep.Optional
	}
	for _, name := range []string{"catalog", "billing", "subscriptions", "entitlements", "auth"} {
		if !required[name] {
			t.Errorf("manifest should require %s", name)
		}
	}
	var subscriptionEvents []string
	for _, dep := range m.Requires.Apps {
		if dep.Name == "subscriptions" {
			subscriptionEvents = dep.Events
			break
		}
	}
	if len(subscriptionEvents) == 0 {
		t.Fatal("manifest should subscribe to subscription lifecycle events")
	}
	if len(m.Provides.UIPanels) != 1 || m.Provides.UIPanels[0].Entry != "/ui/SaaSPanel.mjs" {
		t.Fatalf("manifest should expose SaaS UI panel, got %+v", m.Provides.UIPanels)
	}
	if required["messaging"] || required["analytics"] {
		t.Error("messaging and analytics should be optional")
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
			t.Errorf("handler implements %q but manifest does not declare it", name)
		}
	}
}

func TestAccountCreate_AppliesAuthAndEntitlements(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}

	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "crm-pro", "name": "CRM Pro", "billing_mode": "paid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanFeatureAdd(ctx, map[string]any{"plan_key": "crm-pro", "feature_key": "crm:access"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanLimitSet(ctx, map[string]any{"plan_key": "crm-pro", "feature_key": "crm:contacts", "limit_value": 10000}); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolAccountCreate(ctx, map[string]any{
		"owner_email":       "owner@example.com",
		"customer_name":     "Acme Inc",
		"slug":              "acme",
		"plan_key":          "crm-pro",
		"create_owner_user": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	acct := out.(map[string]any)["account"].(*Account)
	if acct.Status != StatusActive {
		t.Fatalf("status=%s, want active", acct.Status)
	}
	if acct.AuthOrgID == nil || *acct.AuthOrgID != 101 {
		t.Fatalf("auth_org_id=%v, want 101", acct.AuthOrgID)
	}
	if acct.AuthUserID == nil || *acct.AuthUserID != 202 {
		t.Fatalf("auth_user_id=%v, want 202", acct.AuthUserID)
	}

	got := map[string]bool{}
	for _, call := range pf.calls {
		got[call.App+":"+call.Tool] = true
	}
	for _, key := range []string{
		"auth:auth_orgs_create",
		"auth:auth_users_create",
		"entitlements:entitlement_grants_create",
		"entitlements:entitlement_limits_set",
	} {
		if !got[key] {
			t.Errorf("missing call %s; calls=%+v", key, pf.calls)
		}
	}
}

func TestAccountCreate_StoresBillingCustomerID(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}

	out, err := app.toolAccountCreate(ctx, map[string]any{
		"owner_email":         "owner@example.com",
		"customer_name":       "Acme Inc",
		"billing_customer_id": 77,
		"slug":                "acme",
		"plan_key":            "free",
	})
	if err != nil {
		t.Fatal(err)
	}
	acct := out.(map[string]any)["account"].(*Account)
	customer, err := dbCustomerGet(db, "proj-test", acct.CustomerID)
	if err != nil {
		t.Fatal(err)
	}
	if customer.BillingCustomerID == nil || *customer.BillingCustomerID != 77 {
		t.Fatalf("billing_customer_id=%v, want 77", customer.BillingCustomerID)
	}
}

func TestCheckoutCreate_ManualPaymentActivatesAccountAndStoresBillingRefs(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)

	out, err := app.toolCheckoutCreate(ctx, map[string]any{
		"owner_email":           "buyer@example.com",
		"customer_name":         "Buyer Co",
		"slug":                  "buyer",
		"plan_key":              "crm-pro",
		"payment_mode":          "manual",
		"record_payment":        true,
		"manual_payment_method": "wire",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := out.(map[string]any)
	acct := body["account"].(*Account)
	if acct.Status != StatusActive {
		t.Fatalf("status=%s, want active", acct.Status)
	}
	if acct.SubscriptionID == nil || *acct.SubscriptionID != 601 {
		t.Fatalf("subscription_id=%v, want 601", acct.SubscriptionID)
	}
	customer, err := dbCustomerGet(db, "proj-test", acct.CustomerID)
	if err != nil {
		t.Fatal(err)
	}
	if customer.BillingCustomerID == nil || *customer.BillingCustomerID != 501 {
		t.Fatalf("billing_customer_id=%v, want 501", customer.BillingCustomerID)
	}
	for _, key := range []string{
		"billing:customers_upsert_by_email",
		"catalog:catalog_prices_get",
		"subscriptions:subscriptions_create",
		"subscriptions:subscriptions_invoice_create",
		"billing:payments_record",
		"auth:auth_orgs_create",
		"entitlements:entitlement_grants_create",
		"entitlements:entitlement_limits_set",
	} {
		if !hasCall(pf.calls, key) {
			t.Fatalf("missing call %s; calls=%+v", key, pf.calls)
		}
	}
	payment := findCall(pf.calls, "billing", "payments_record")
	if payment == nil {
		t.Fatal("payments_record was not called")
	}
	if int64FromAny(payment.Input["amount_cents"]) != 2900 || payment.Input["method"] != "wire" {
		t.Fatalf("bad payment input: %+v", payment.Input)
	}
}

func TestCheckoutCreate_PaymentLinkLeavesAccountPastDueAndAccessDenied(t *testing.T) {
	pf := &platformStub{entitled: true, contacts: 0}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)

	out, err := app.toolCheckoutCreate(ctx, map[string]any{
		"owner_email":   "buyer@example.com",
		"customer_name": "Buyer Co",
		"slug":          "buyer",
		"plan_key":      "crm-pro",
		"payment_mode":  "payment_link",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := out.(map[string]any)
	acct := body["account"].(*Account)
	if acct.Status != StatusPastDue || body["status"] != "awaiting_payment" {
		t.Fatalf("checkout should await payment with past_due account: status=%s body=%+v", acct.Status, body)
	}
	link := mapFromAny(body["payment_link"])
	if strArg(link, "url") != "https://pay.example/session" {
		t.Fatalf("payment link missing: %+v", body["payment_link"])
	}
	if hasCall(pf.calls, "billing:payments_record") {
		t.Fatalf("payments_record should not run for payment_link; calls=%+v", pf.calls)
	}
	access, err := app.toolAccessCheck(ctx, map[string]any{"account_id": acct.ID, "feature_key": "crm:contacts"})
	if err != nil {
		t.Fatal(err)
	}
	if access.(map[string]any)["allowed"].(bool) {
		t.Fatalf("unpaid past_due checkout should not allow access: %+v", access)
	}
}

func TestUsageSync_StoresLiveGaugeNotAdditive(t *testing.T) {
	pf := &platformStub{contacts: 7}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}

	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "crm-pro", "name": "CRM Pro"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanLimitSet(ctx, map[string]any{"plan_key": "crm-pro", "feature_key": "crm:contacts", "limit_value": 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanUsageSourceAdd(ctx, map[string]any{"plan_key": "crm-pro", "app_name": "crm", "tool_name": "crm_saas_usage_snapshot"}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "acme", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)

	if _, err := app.toolUsageSync(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	pf.contacts = 4
	if _, err := app.toolUsageSync(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolUsageGet(ctx, map[string]any{"account_id": acct.ID, "feature_key": "crm:contacts"})
	if err != nil {
		t.Fatal(err)
	}
	usage := out.(map[string]any)["usage"].([]UsageTotal)
	if len(usage) != 1 {
		t.Fatalf("usage len=%d, want 1", len(usage))
	}
	if usage[0].Quantity != 4 {
		t.Fatalf("usage quantity=%d, want current gauge 4", usage[0].Quantity)
	}
	if usage[0].OverLimit {
		t.Fatal("usage should not be over limit after gauge dropped below 5")
	}
}

func TestUsageSync_ExtractsGenericToolResponsePath(t *testing.T) {
	pf := &platformStub{contacts: 12}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}

	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "crm-pro", "name": "CRM Pro"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanLimitSet(ctx, map[string]any{"plan_key": "crm-pro", "feature_key": "crm:contacts", "limit_value": 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanUsageSourceAdd(ctx, map[string]any{
		"plan_key":       "crm-pro",
		"app_name":       "crm",
		"tool_name":      "contacts_search",
		"feature_key":    "crm:contacts",
		"read_path":      "total",
		"call_args":      map[string]any{"limit": 1, "offset": 0, "q": "{{account.slug}}"},
		"feature_prefix": "crm",
	}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "acme", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)

	if _, err := app.toolUsageSync(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolUsageGet(ctx, map[string]any{"account_id": acct.ID, "feature_key": "crm:contacts"})
	if err != nil {
		t.Fatal(err)
	}
	usage := out.(map[string]any)["usage"].([]UsageTotal)
	if len(usage) != 1 || usage[0].Quantity != 12 || !usage[0].OverLimit {
		t.Fatalf("generic usage extraction failed: %+v", usage)
	}

	var search callAppCall
	for _, call := range pf.calls {
		if call.App == "crm" && call.Tool == "contacts_search" {
			search = call
		}
	}
	if search.Tool == "" {
		t.Fatalf("contacts_search was not called; calls=%+v", pf.calls)
	}
	if search.Input["_project_id"] != "proj-test" || int64FromAny(search.Input["limit"]) != 1 || search.Input["q"] != "acme" {
		t.Fatalf("usage source input not expanded correctly: %+v", search.Input)
	}
}

func TestUsageSync_DefaultIncludesPastDueAccounts(t *testing.T) {
	pf := &platformStub{contacts: 1}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}

	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "crm-pro", "name": "CRM Pro"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanUsageSourceAdd(ctx, map[string]any{"plan_key": "crm-pro", "app_name": "crm", "tool_name": "crm_saas_usage_snapshot"}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "acme", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"account_id": acct.ID, "subscription_status": "past_due"}); err != nil {
		t.Fatal(err)
	}

	pf.contacts = 3
	if _, err := app.toolUsageSync(ctx, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolUsageGet(ctx, map[string]any{"account_id": acct.ID, "feature_key": "crm:contacts"})
	if err != nil {
		t.Fatal(err)
	}
	usage := out.(map[string]any)["usage"].([]UsageTotal)
	if len(usage) != 1 || usage[0].Quantity != 3 {
		t.Fatalf("past_due account usage should sync by default, got %+v", usage)
	}
}

func TestAccessCheck_RequiresEntitlementAndLiveLimit(t *testing.T) {
	pf := &platformStub{contacts: 7, entitled: true}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}

	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "crm-pro", "name": "CRM Pro"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanFeatureAdd(ctx, map[string]any{"plan_key": "crm-pro", "feature_key": "crm:contacts"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanLimitSet(ctx, map[string]any{"plan_key": "crm-pro", "feature_key": "crm:contacts", "limit_value": 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanUsageSourceAdd(ctx, map[string]any{"plan_key": "crm-pro", "app_name": "crm", "tool_name": "crm_saas_usage_snapshot"}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "acme", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)

	out, err := app.toolAccessCheck(ctx, map[string]any{"account_id": acct.ID, "feature_key": "crm:contacts"})
	if err != nil {
		t.Fatal(err)
	}
	access := out.(map[string]any)
	if access["allowed"].(bool) {
		t.Fatal("access should be denied when live usage is over the plan limit")
	}
	if !access["entitled"].(bool) || !access["over_limit"].(bool) {
		t.Fatalf("access should report entitled=true and over_limit=true: %+v", access)
	}

	pf.contacts = 4
	if _, err := app.toolUsageSync(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	out, err = app.toolAccessCheck(ctx, map[string]any{"account_id": acct.ID, "feature_key": "crm:contacts"})
	if err != nil {
		t.Fatal(err)
	}
	access = out.(map[string]any)
	if !access["allowed"].(bool) {
		t.Fatalf("access should be allowed when entitled and under limit: %+v", access)
	}

	pf.entitled = false
	out, err = app.toolAccessCheck(ctx, map[string]any{"account_id": acct.ID, "feature_key": "crm:contacts"})
	if err != nil {
		t.Fatal(err)
	}
	access = out.(map[string]any)
	if access["allowed"].(bool) || access["entitled"].(bool) {
		t.Fatalf("access should be denied when Entitlements denies the feature: %+v", access)
	}
}

func TestPlanChildrenRequireExistingPlan(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}

	if _, err := app.toolPlanFeatureAdd(ctx, map[string]any{"plan_key": "missing-pro", "feature_key": "crm:contacts"}); err == nil {
		t.Fatal("feature add should fail for a missing plan")
	}
	if _, err := app.toolPlanLimitSet(ctx, map[string]any{"plan_key": "missing-pro", "feature_key": "crm:contacts", "limit_value": 5}); err == nil {
		t.Fatal("limit set should fail for a missing plan")
	}
	if _, err := app.toolPlanUsageSourceAdd(ctx, map[string]any{"plan_key": "missing-pro", "app_name": "crm", "tool_name": "crm_saas_usage_snapshot"}); err == nil {
		t.Fatal("usage source add should fail for a missing plan")
	}
}

func TestAccountCreate_SlugConflict(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}

	first, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "acme", "plan_key": "free"})
	if err != nil {
		t.Fatal(err)
	}
	firstAcct := first.(map[string]any)["account"].(*Account)
	second, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "acme", "plan_key": "free"})
	if err != nil {
		t.Fatal(err)
	}
	secondAcct := second.(map[string]any)["account"].(*Account)
	if secondAcct.ID != firstAcct.ID {
		t.Fatalf("idempotent create returned account %s, want %s", secondAcct.ID, firstAcct.ID)
	}
	if _, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "other@example.com", "slug": "acme", "plan_key": "free"}); err == nil {
		t.Fatal("slug conflict should fail for a different owner")
	}
}

func TestSubscriptionSync_UpdatesLifecycle(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}

	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "acme", "plan_key": "free", "subscription_id": 99})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"account_id": acct.ID, "subscription_status": "past_due"}); err != nil {
		t.Fatal(err)
	}
	got, err := dbAccountGet(db, "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPastDue {
		t.Fatalf("status=%s, want past_due", got.Status)
	}
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"subscription_id": 99, "subscription_status": "cancelled"}); err != nil {
		t.Fatal(err)
	}
	got, err = dbAccountGet(db, "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusSuspended {
		t.Fatalf("status=%s, want suspended", got.Status)
	}
}

func TestSubscriptionLifecycle_UsesEventName(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}

	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "acme", "plan_key": "free", "subscription_id": 77})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	err = app.handleSubscriptionLifecycle(ctx, sdk.Event{
		Event:     "subscription.past_due",
		Topic:     "subscription.active",
		ProjectID: "proj-test",
		Data:      map[string]any{"subscription_id": 77},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := dbAccountGet(db, "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPastDue {
		t.Fatalf("status=%s, want past_due from event name", got.Status)
	}
}

func TestSubscriptionLifecycle_MatchesHostingEventShape(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}

	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "acme", "plan_key": "free", "subscription_id": 88})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)

	err = app.handleSubscriptionLifecycle(ctx, sdk.Event{
		Event:     "subscription.active",
		ProjectID: "proj-test",
		Data:      map[string]any{"id": 88, "status": "cancelled"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := dbAccountGet(db, "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusSuspended {
		t.Fatalf("status=%s, want suspended from data.status override", got.Status)
	}

	if err := app.handleSubscriptionLifecycle(ctx, sdk.Event{Event: "subscription.active", Data: map[string]any{}}); err != nil {
		t.Fatalf("missing subscription id should be ignored, got %v", err)
	}
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"account_id": acct.ID, "subscription_status": "resumed"}); err != nil {
		t.Fatal(err)
	}
	got, err = dbAccountGet(db, "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusActive {
		t.Fatalf("status=%s, want active after resumed", got.Status)
	}
}

func setupPaidCRMPlan(t *testing.T, app *App, ctx *sdk.AppCtx) {
	t.Helper()
	if _, err := app.toolPlanUpsert(ctx, map[string]any{
		"key":                   "crm-pro",
		"name":                  "CRM Pro",
		"billing_mode":          "paid",
		"catalog_product_id":    7001,
		"catalog_price_id":      8001,
		"subscription_required": true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanFeatureAdd(ctx, map[string]any{"plan_key": "crm-pro", "feature_key": "crm:contacts"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanLimitSet(ctx, map[string]any{"plan_key": "crm-pro", "feature_key": "crm:contacts", "limit_value": 3}); err != nil {
		t.Fatal(err)
	}
}

func hasCall(calls []callAppCall, key string) bool {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return false
	}
	return findCall(calls, parts[0], parts[1]) != nil
}

func findCall(calls []callAppCall, app, tool string) *callAppCall {
	for i := range calls {
		if calls[i].App == app && calls[i].Tool == tool {
			return &calls[i]
		}
	}
	return nil
}
