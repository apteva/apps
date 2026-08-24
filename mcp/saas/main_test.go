package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

type platformStub struct {
	tk.BasePlatformClient
	mu                          sync.Mutex
	calls                       []callAppCall
	contacts                    int64
	entitled                    bool
	priceTrialDays              int64
	priceMetadata               map[string]any
	failures                    map[string]int
	failureMessages             map[string]string
	grants                      map[string]bool
	failGrantFeature            string
	failGrantOnce               bool
	emptyUsage                  bool
	existingInvoiceOperationKey string
	invoiceGetStatus            string
	invoiceGetPaidAt            string
	invoiceGetAmountPaid        int64
	invoiceGetTotal             int64
	invoiceGetCurrency          string
	invoiceGetPayments          []map[string]any
	subscriptionDiscountCents   int64
	discountPercentageBPS       int64
}

type callAppCall struct {
	App   string
	Tool  string
	Input map[string]any
}

type paymentEventRaceStub struct {
	*platformStub
	onPaid            func() error
	raceStarted       chan struct{}
	activationStarted chan struct{}
	releaseActivation chan struct{}
	eventDone         chan error
	activationOnce    sync.Once
}

func newPaymentEventRaceStub() *paymentEventRaceStub {
	return &paymentEventRaceStub{
		platformStub:      paidInvoicePlatformStub(),
		raceStarted:       make(chan struct{}),
		activationStarted: make(chan struct{}),
		releaseActivation: make(chan struct{}),
		eventDone:         make(chan error, 1),
	}
}

func (p *paymentEventRaceStub) CallAppResult(appName, tool string, input map[string]any, out any) error {
	if appName == "billing" && tool == "payments_record" {
		if err := p.platformStub.CallAppResult(appName, tool, input, out); err != nil {
			return err
		}
		close(p.raceStarted)
		go func() { p.eventDone <- p.onPaid() }()
		select {
		case <-p.activationStarted:
		case <-time.After(time.Second):
			return errors.New("invoice.paid handler did not claim the payment operation")
		}
		go func() {
			time.Sleep(75 * time.Millisecond)
			close(p.releaseActivation)
		}()
		return nil
	}
	if appName == "subscriptions" && tool == "subscription_cycles_update" {
		select {
		case <-p.raceStarted:
			p.activationOnce.Do(func() { close(p.activationStarted) })
			<-p.releaseActivation
		default:
		}
	}
	return p.platformStub.CallAppResult(appName, tool, input, out)
}

func (p *platformStub) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, callAppCall{App: appName, Tool: tool, Input: input})
	key := appName + ":" + tool
	if p.failures[key] > 0 {
		p.failures[key]--
		return fmt.Errorf("injected failure for %s", key)
	}
	if message := p.failureMessages[key]; message != "" {
		return errors.New(message)
	}
	var body map[string]any
	switch tool {
	case "auth_orgs_create":
		body = map[string]any{"organization": map[string]any{"id": 101, "slug": input["slug"], "name": input["name"]}}
	case "auth_users_create":
		body = map[string]any{"user": map[string]any{"id": 202, "email": input["email"], "organization_id": input["organization_id"]}}
	case "contacts_upsert_by_channel":
		body = map[string]any{"contact": map[string]any{"id": 303, "display_name": input["defaults"].(map[string]any)["display_name"]}, "was_created": true}
	case "entitlement_grants_upsert":
		if p.failGrantOnce && strFromAny(input["feature_key"]) == p.failGrantFeature {
			p.failGrantOnce = false
			return fmt.Errorf("injected grant failure for %s", p.failGrantFeature)
		}
		if p.grants == nil {
			p.grants = map[string]bool{}
		}
		p.grants[strFromAny(input["feature_key"])] = true
		body = map[string]any{"grant": map[string]any{"id": 301, "feature_key": input["feature_key"], "subject_id": input["subject_id"], "metadata": input["metadata"]}, "created": true}
	case "entitlement_grants_list":
		var grants []any
		if p.grants[strFromAny(input["feature_key"])] {
			grants = append(grants, map[string]any{"id": 301, "feature_key": input["feature_key"], "subject_id": input["subject_id"], "source_type": "saas", "source_id": input["subject_id"], "status": "active"})
		}
		body = map[string]any{"grants": grants}
	case "entitlement_limits_set":
		body = map[string]any{"limit": map[string]any{"id": 401, "feature_key": input["feature_key"], "limit_value": input["limit_value"]}}
	case "entitlements_check":
		body = map[string]any{"allowed": p.entitled}
	case "customers_upsert_by_email":
		defaults := mapFromAny(input["defaults"])
		body = map[string]any{"customer": map[string]any{"id": 501, "email": input["email"], "name": defaults["name"], "metadata": defaults["metadata"]}}
	case "catalog_prices_get":
		price := map[string]any{"id": input["id"], "product_id": 7001, "unit_amount_cents": 2900, "currency": "USD", "interval": "month", "interval_count": 1}
		if p.priceTrialDays > 0 {
			price["trial_days"] = p.priceTrialDays
		}
		if p.priceMetadata != nil {
			price["metadata"] = p.priceMetadata
		}
		body = map[string]any{"price": price}
	case "catalog_products_list":
		body = map[string]any{"products": []map[string]any{
			{"id": 7001, "name": "CRM", "slug": "crm", "type": "recurring", "category": "business", "color": "#2563eb"},
		}}
	case "catalog_discounts_reserve":
		discountID := firstNonZero(int64FromAny(input["discount_id"]), 9001)
		code := normalizeDiscountCode(strFromAny(input["code"]))
		subtotal := int64FromAny(input["subtotal_cents"])
		percentageBPS := firstNonZero(p.discountPercentageBPS, 5000)
		discount := subtotal * percentageBPS / 10000
		p.subscriptionDiscountCents = discount
		body = map[string]any{"reservation": map[string]any{
			"reservation_id": "dres_123", "idempotency_key": input["idempotency_key"], "discount_id": discountID,
			"code_id": int64(9002), "code": code, "customer_ref": input["customer_ref"], "context_ref": input["context_ref"],
			"product_id": input["product_id"], "price_id": input["price_id"], "quantity": int64(1), "currency": input["currency"],
			"subtotal_cents": subtotal, "discount_cents": discount, "total_cents": subtotal - discount,
			"status": "reserved", "expires_at": "2027-01-01T00:00:00Z",
			"application": map[string]any{"discount_id": discountID, "code_id": int64(9002), "code": code, "name": "Promotion", "discount_type": "percentage", "percentage_bps": percentageBPS, "duration": "repeating", "duration_cycles": int64(3)},
		}}
	case "catalog_discounts_redeem":
		body = map[string]any{"redemption": map[string]any{"reservation_id": input["reservation_id"], "status": "redeemed", "redeemed_at": "2026-07-14T10:00:00Z"}}
	case "subscriptions_create":
		body = map[string]any{"subscription": map[string]any{
			"id":                   601,
			"status":               input["status"],
			"customer_id":          input["customer_id"],
			"items":                input["items"],
			"trial_start":          input["trial_start"],
			"trial_end":            input["trial_end"],
			"current_period_start": input["current_period_start"],
			"current_period_end":   input["current_period_end"],
			"next_renewal_at":      input["next_renewal_at"],
			"metadata":             input["metadata"],
		}}
	case "subscriptions_search":
		body = map[string]any{"subscriptions": []any{}, "count": 0}
	case "subscription_cycles_create":
		body = map[string]any{"cycle": map[string]any{"id": 801, "subscription_id": input["subscription_id"], "period_start": input["period_start"], "period_end": input["period_end"], "payment_status": input["payment_status"]}}
	case "subscriptions_invoice_create":
		body = map[string]any{"invoice": map[string]any{"id": 701, "status": "open", "total_cents": 2900}, "cycle": map[string]any{"id": 801, "invoice_id": 701, "payment_status": "open"}}
	case "subscriptions_invoice_prepare":
		net := int64(2900) - p.subscriptionDiscountCents
		body = map[string]any{
			"subscription": map[string]any{"id": input["subscription_id"], "currency": "USD"},
			"currency":     "USD", "period_start": input["period_start"], "period_end": input["period_end"], "cycle_id": input["cycle_id"],
			"base_subtotal_cents": int64(2900), "discount_cents": p.subscriptionDiscountCents, "total_cents": net,
			"line_items": []any{map[string]any{"description": "CRM Pro", "quantity": 1, "unit_price_cents": net}},
		}
	case "invoices_create_from_prepared_lines":
		body = map[string]any{"invoice": map[string]any{"id": 702, "customer_id": input["customer_id"], "status": "open", "total_cents": 2900}}
	case "invoices_search":
		invoices := []any{}
		if p.existingInvoiceOperationKey != "" {
			invoices = append(invoices, map[string]any{"id": 799, "customer_id": input["customer_id"], "status": "open", "metadata": map[string]any{"operation_key": p.existingInvoiceOperationKey}})
		}
		body = map[string]any{"invoices": invoices, "count": len(invoices)}
	case "invoices_get":
		status := firstNonEmpty(p.invoiceGetStatus, "open")
		body = map[string]any{"invoice": map[string]any{
			"id": input["id"], "status": status, "customer_id": 501,
			"currency":    firstNonEmpty(p.invoiceGetCurrency, "USD"),
			"total_cents": p.invoiceGetTotal, "amount_paid_cents": p.invoiceGetAmountPaid,
			"paid_at": p.invoiceGetPaidAt, "created_at": "2026-05-01T10:00:00Z", "updated_at": "2026-07-01T10:00:00Z",
			"payments": p.invoiceGetPayments,
		}, "found": true}
	case "subscription_cycles_update":
		body = map[string]any{"cycle": map[string]any{"id": input["id"], "invoice_id": input["invoice_id"], "payment_status": input["payment_status"]}}
	case "subscriptions_update_status":
		body = map[string]any{"subscription": map[string]any{"id": input["id"], "status": input["status"], "current_period_start": input["current_period_start"], "current_period_end": input["current_period_end"]}}
	case "subscriptions_update_metadata":
		body = map[string]any{"subscription": map[string]any{"id": input["id"], "metadata": input["metadata_patch"]}}
	case "payments_record":
		body = map[string]any{"payment": map[string]any{"id": 901, "method": input["method"], "amount_cents": input["amount_cents"]}, "invoice": map[string]any{"id": input["invoice_id"], "status": "paid", "total_cents": input["amount_cents"], "amount_paid_cents": input["amount_cents"]}}
	case "invoices_send_payment_link":
		body = map[string]any{"url": "https://pay.example/session", "stripe_session_id": "cs_test_123", "expires_at": 123}
	case "invoices_collect":
		body = map[string]any{"collection_attempt": map[string]any{"id": 1101, "invoice_id": input["invoice_id"], "status": "succeeded", "idempotency_key": input["idempotency_key"]}}
	case "payment_method_setup_create":
		body = map[string]any{"setup_session": map[string]any{"id": 1001, "provider_session_id": "cs_setup_123", "customer_id": input["customer_id"], "status": "pending", "url": "https://pay.example/setup", "expires_at": 1785000000, "metadata": input["metadata"]}, "url": "https://pay.example/setup"}
	case "containers_create":
		body = map[string]any{"workload": map[string]any{"id": "wrk_123", "name": input["name"], "image": input["image"], "status": "running"}}
	case "containers_stop":
		body = map[string]any{"workload": map[string]any{"id": input["workload_id"], "status": "stopped"}}
	case "containers_start":
		body = map[string]any{"workload": map[string]any{"id": input["workload_id"], "status": "running"}}
	case "containers_destroy":
		body = map[string]any{"workload": map[string]any{"id": input["workload_id"], "status": "deleted"}}
	case "credentials_create":
		body = map[string]any{
			"token":    "customer-secret-token",
			"token_id": "tok_123",
			"session":  map[string]any{"exchange_code": "custom-secret-code", "expires_at": "2026-08-01T00:00:00Z"},
		}
	case "crm_saas_usage_snapshot":
		if p.emptyUsage {
			body = map[string]any{"usage": []map[string]any{}}
		} else {
			body = map[string]any{"usage": []map[string]any{{"feature_key": "crm:contacts", "quantity": p.contacts}}}
		}
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
	// Match the app-sdk runtime, which serializes SQLite access so app
	// events and tool calls queue instead of competing as separate writers.
	db.SetMaxOpenConns(1)
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
	if m.Version != "0.9.1" {
		t.Errorf("manifest.Version=%q, want 0.9.1", m.Version)
	}
	if !m.Requires.DynamicAppCalls {
		t.Error("manifest should allow dynamic app calls for configured usage sources")
	}
	if m.DB == nil || m.DB.Migrations != "migrations/" {
		t.Fatalf("manifest DB not declared correctly: %+v", m.DB)
	}
	required := map[string]bool{}
	present := map[string]bool{}
	versions := map[string]string{}
	for _, dep := range m.Requires.Apps {
		present[dep.Name] = true
		required[dep.Name] = !dep.Optional
		versions[dep.Name] = dep.Version
	}
	if versions["catalog"] != ">=0.2.0" || versions["billing"] != ">=0.10.0" || versions["subscriptions"] != ">=0.7.0" || versions["entitlements"] != ">=0.2.0" {
		t.Fatalf("dependency versions not enforced: %+v", versions)
	}
	permissions := map[string]bool{}
	for _, permission := range m.Provides.ProvidedPermissions {
		permissions[permission.Name] = true
	}
	for _, name := range []string{"saas.read", "saas.checkout", "saas.admin"} {
		if !permissions[name] {
			t.Errorf("manifest missing permission %s", name)
		}
	}
	toolRequires := map[string]string{}
	for _, tool := range m.Provides.MCPTools {
		toolRequires[tool.Name] = tool.Requires
	}
	if toolRequires["saas_checkout_create"] != "saas.checkout" || toolRequires["saas_account_create"] != "saas.admin" || toolRequires["saas_account_reconcile"] != "saas.admin" || toolRequires["saas_plan_action_add"] != "saas.admin" || toolRequires["saas_billing_sync"] != "saas.admin" || toolRequires["saas_account_change_plan"] != "saas.admin" || toolRequires["saas_plan_change_get"] != "saas.read" {
		t.Fatalf("sensitive tools are not permission-gated: %+v", toolRequires)
	}
	for _, name := range []string{"catalog", "billing", "subscriptions", "entitlements", "auth"} {
		if !required[name] {
			t.Errorf("manifest should require %s", name)
		}
	}
	var subscriptionEvents, billingEvents []string
	for _, dep := range m.Requires.Apps {
		if dep.Name == "subscriptions" {
			subscriptionEvents = dep.Events
		}
		if dep.Name == "billing" {
			billingEvents = dep.Events
		}
	}
	if !containsString(subscriptionEvents, "subscription.cycle_due") || !containsString(subscriptionEvents, "subscription.change.applied") {
		t.Fatalf("manifest is missing subscription orchestration events: %v", subscriptionEvents)
	}
	if !containsString(billingEvents, "invoice.paid") ||
		!containsString(billingEvents, "invoice.payment_failed") ||
		!containsString(billingEvents, "invoice.payment_action_required") {
		t.Fatalf("manifest should subscribe to billing collection events: %v", billingEvents)
	}
	if len(m.Provides.UIPanels) != 1 || m.Provides.UIPanels[0].Entry != "/ui/SaaSPanel.mjs" {
		t.Fatalf("manifest should expose SaaS UI panel, got %+v", m.Provides.UIPanels)
	}
	if !present["crm"] || required["crm"] || required["messaging"] || required["analytics"] {
		t.Error("crm, messaging, and analytics should be optional")
	}
	publishes := map[string]bool{}
	for _, event := range m.Provides.Publishes {
		publishes[event.Name] = true
	}
	for _, name := range []string{"saas.quota.approaching", "saas.quota.reached", "saas.quota.exceeded", "saas.quota.recovered", "saas.account.plan_changed"} {
		if !publishes[name] {
			t.Errorf("manifest missing published event %s", name)
		}
	}
}

func TestPlanList_ProjectsCatalogProductIdentity(t *testing.T) {
	platform := &platformStub{}
	ctx, db := newTestCtx(t, platform)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{
		"key": "crm-basic", "name": "Basic", "billing_mode": "paid", "catalog_product_id": 7001,
	}); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolPlanList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	plans := out.(map[string]any)["plans"].([]*Plan)
	var crm *Plan
	for _, plan := range plans {
		if plan.Key == "crm-basic" {
			crm = plan
			break
		}
	}
	if crm == nil || crm.CatalogProduct == nil {
		t.Fatalf("catalog product was not projected onto plan: %+v", crm)
	}
	if crm.CatalogProduct.ID != 7001 || crm.CatalogProduct.Name != "CRM" || crm.CatalogProduct.Slug != "crm" {
		t.Fatalf("unexpected catalog product projection: %+v", crm.CatalogProduct)
	}
	catalogCalls := 0
	for _, call := range platform.calls {
		if call.App == "catalog" && call.Tool == "catalog_products_list" {
			catalogCalls++
		}
	}
	if catalogCalls != 1 {
		t.Fatalf("plan list should hydrate products with one Catalog call: %+v", platform.calls)
	}
}

func TestPlanUpsert_DefaultsNewPaidPlansAndPreservesExplicitManualCollection(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}

	createdOut, err := app.toolPlanUpsert(ctx, map[string]any{
		"key": "automatic", "name": "Automatic", "billing_mode": "paid",
	})
	if err != nil {
		t.Fatal(err)
	}
	created := createdOut.(map[string]any)["plan"].(*Plan)
	if got := strArg(mapFromAny(created.Metadata), "collection_method"); got != "charge_automatically" {
		t.Fatalf("new paid plan collection_method=%q, want charge_automatically", got)
	}

	if _, err := app.toolPlanUpsert(ctx, map[string]any{
		"key": "manual", "name": "Manual", "billing_mode": "paid",
		"metadata": map[string]any{"collection_method": "send_invoice"},
	}); err != nil {
		t.Fatal(err)
	}
	updatedOut, err := app.toolPlanUpsert(ctx, map[string]any{
		"key": "manual", "name": "Manual renamed", "billing_mode": "paid",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated := updatedOut.(map[string]any)["plan"].(*Plan)
	if got := strArg(mapFromAny(updated.Metadata), "collection_method"); got != "send_invoice" {
		t.Fatalf("existing manual plan collection_method=%q, want send_invoice", got)
	}
}

func TestPlanList_CatalogProjectionFailureIsNonBlocking(t *testing.T) {
	platform := &platformStub{failures: map[string]int{"catalog:catalog_products_list": 1}}
	ctx, db := newTestCtx(t, platform)
	defer db.Close()
	app := &App{}

	out, err := app.toolPlanList(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("plan list should survive Catalog display-data failure: %v", err)
	}
	plans := out.(map[string]any)["plans"].([]*Plan)
	if len(plans) == 0 {
		t.Fatal("plan list lost its local SaaS plans")
	}
}

func TestAccountList_FiltersByCatalogProduct(t *testing.T) {
	platform := &platformStub{}
	ctx, db := newTestCtx(t, platform)
	defer db.Close()
	app := &App{}
	for _, plan := range []map[string]any{
		{"key": "crm-free", "name": "CRM Free", "billing_mode": "free", "catalog_product_id": 7001},
		{"key": "storage-free", "name": "Storage Free", "billing_mode": "free", "catalog_product_id": 7002},
	} {
		if _, err := app.toolPlanUpsert(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	for _, account := range []map[string]any{
		{"owner_email": "crm@example.com", "slug": "crm-account", "plan_key": "crm-free"},
		{"owner_email": "storage@example.com", "slug": "storage-account", "plan_key": "storage-free"},
		{"owner_email": "legacy@example.com", "slug": "legacy-account", "plan_key": "free"},
	} {
		if _, err := app.toolAccountCreate(ctx, account); err != nil {
			t.Fatal(err)
		}
	}

	crmOut, err := app.toolAccountList(ctx, map[string]any{"catalog_product_id": 7001})
	if err != nil {
		t.Fatal(err)
	}
	crmAccounts := crmOut.(map[string]any)["accounts"].([]*Account)
	if len(crmAccounts) != 1 || crmAccounts[0].PlanKey != "crm-free" {
		t.Fatalf("CRM product filter returned wrong accounts: %+v", crmAccounts)
	}

	unlinkedOut, err := app.toolAccountList(ctx, map[string]any{"catalog_product_id": 0})
	if err != nil {
		t.Fatal(err)
	}
	unlinkedAccounts := unlinkedOut.(map[string]any)["accounts"].([]*Account)
	if len(unlinkedAccounts) != 1 || unlinkedAccounts[0].PlanKey != "free" {
		t.Fatalf("unlinked product filter returned wrong accounts: %+v", unlinkedAccounts)
	}
}

func TestReliabilityMigration_PreservesExistingRows(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, path := range []string{"migrations/001_init.sql", "migrations/002_fulfillment_actions.sql"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO saas_usage_snapshots (project_id, account_id, customer_id, source_app, feature_key, quantity) VALUES ('p','a',1,'crm','contacts',3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO saas_fulfillment_runs (project_id, account_id, plan_action_id, event, app_name, tool_name, status) VALUES ('p','a',9,'account_active','containers','containers_create','succeeded')`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/003_reliability.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	var sourceKey, generation, transitionID string
	if err := db.QueryRow(`SELECT source_key, generation_id FROM saas_usage_snapshots`).Scan(&sourceKey, &generation); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT transition_id FROM saas_fulfillment_runs`).Scan(&transitionID); err != nil {
		t.Fatal(err)
	}
	if sourceKey != "crm" || generation != "legacy" || transitionID != "legacy:1" {
		t.Fatalf("legacy rows not preserved: source=%q generation=%q transition=%q", sourceKey, generation, transitionID)
	}
}

func TestFulfillmentPersistenceMigration_ScrubsExistingRowsOnMount(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	matches, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range matches {
		if strings.HasSuffix(path, "010_fulfillment_persistence.sql") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO saas_plans (project_id,key,name) VALUES ('proj-test','legacy','Legacy')`); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`
		INSERT INTO saas_plan_actions (project_id,plan_key,event,app_name,tool_name)
		VALUES ('proj-test','legacy','account_active','credentials','credentials_create')`)
	if err != nil {
		t.Fatal(err)
	}
	actionID, _ := result.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO saas_fulfillment_runs
			(project_id,account_id,plan_action_id,transition_id,event,app_name,tool_name,status,input_json,output_json)
		VALUES ('proj-test','legacy-account',?,'legacy-transition','account_active','credentials','credentials_create','succeeded',
		        '{"api_key":"legacy-input-secret"}','{"token":"legacy-output-secret","token_id":"tok_legacy"}')`, actionID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/010_fulfillment_persistence.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	manifest := app.Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, &platformStub{}, nil).WithProject("proj-test")
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	var inputJSON, outputJSON string
	if err := db.QueryRow(`SELECT input_json,output_json FROM saas_fulfillment_runs WHERE account_id='legacy-account'`).Scan(&inputJSON, &outputJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inputJSON, "legacy-input-secret") || strings.Contains(outputJSON, "legacy-output-secret") {
		t.Fatalf("upgrade retained historical secrets: input=%s output=%s", inputJSON, outputJSON)
	}
	if !strings.Contains(outputJSON, `"token_id":"tok_legacy"`) {
		t.Fatalf("upgrade removed safe fulfillment output: %s", outputJSON)
	}
}

func TestAutomaticCollectionDefaultMigration_PreservesExistingPaidPlans(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	matches, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range matches {
		if strings.HasSuffix(path, "011_automatic_collection_default.sql") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO saas_plans (project_id,key,name,billing_mode,metadata_json)
		VALUES
		  ('proj-test','legacy','Legacy','paid','{"note":"keep"}'),
		  ('proj-test','explicit-auto','Explicit Auto','paid','{"collection_method":"charge_automatically"}'),
		  ('proj-test','free','Free','free','{}')`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/011_automatic_collection_default.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"legacy":        "send_invoice",
		"explicit-auto": "charge_automatically",
		"free":          "",
	} {
		var metadata string
		if err := db.QueryRow(`SELECT metadata_json FROM saas_plans WHERE project_id='proj-test' AND key=?`, key).Scan(&metadata); err != nil {
			t.Fatal(err)
		}
		if got := strArg(mapFromAny(json.RawMessage(metadata)), "collection_method"); got != want {
			t.Fatalf("%s collection_method=%q, want %q (metadata=%s)", key, got, want, metadata)
		}
	}
}

func TestPlanActionUpdate_PartialPatch(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}

	created, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key":     "free",
		"event":        "account_active",
		"app_name":     "containers",
		"tool_name":    "containers_create",
		"failure_mode": "fail_account",
		"args":         map[string]any{"name": "old", "image": "nginx"},
		"store":        map[string]any{"metadata.workload_id": "workload.id"},
		"metadata":     map[string]any{"source": "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	action := created.(map[string]any)["action"].(*PlanAction)

	updated, err := app.toolPlanActionUpdate(ctx, map[string]any{
		"id":           action.ID,
		"enabled":      false,
		"failure_mode": "mark_degraded",
		"args": map[string]any{
			"name":  "new",
			"image": "ghcr.io/apteva/apteva:latest",
			"files": []any{map[string]any{
				"path":   "/data/apteva.yaml",
				"mode":   "0600",
				"secret": true,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := updated.(map[string]any)["action"].(*PlanAction)
	if got.ID != action.ID || got.PlanKey != "free" || got.Event != "account_active" || got.AppName != "containers" || got.ToolName != "containers_create" {
		t.Fatalf("identity fields changed unexpectedly: %+v", got)
	}
	if got.Enabled {
		t.Fatalf("enabled=%v, want false", got.Enabled)
	}
	if got.FailureMode != "mark_degraded" {
		t.Fatalf("failure_mode=%q", got.FailureMode)
	}
	if got.PersistInput != persistenceRedacted || got.PersistOutput != persistenceRedacted {
		t.Fatalf("persistence defaults changed: input=%q output=%q", got.PersistInput, got.PersistOutput)
	}
	args := mapFromAny(got.Args)
	if args["name"] != "new" || args["image"] != "ghcr.io/apteva/apteva:latest" {
		t.Fatalf("args not updated: %+v", args)
	}
	files, ok := args["files"].([]any)
	if !ok || len(files) != 1 || mapFromAny(files[0])["path"] != "/data/apteva.yaml" {
		t.Fatalf("files not preserved in args: %#v", args["files"])
	}
	store := mapFromAny(got.Store)
	if store["metadata.workload_id"] != "workload.id" {
		t.Fatalf("store should be unchanged: %+v", store)
	}
	meta := mapFromAny(got.Metadata)
	if meta["source"] != "test" {
		t.Fatalf("metadata should be unchanged: %+v", meta)
	}
}

func TestPlanActionUpdate_RejectsUnknownAndBadFailureMode(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}

	if _, err := app.toolPlanActionUpdate(ctx, map[string]any{"id": 999, "enabled": false}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err=%v, want sql.ErrNoRows", err)
	}

	created, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key":  "free",
		"event":     "account_active",
		"app_name":  "containers",
		"tool_name": "containers_create",
	})
	if err != nil {
		t.Fatal(err)
	}
	action := created.(map[string]any)["action"].(*PlanAction)
	if _, err := app.toolPlanActionUpdate(ctx, map[string]any{"id": action.ID, "failure_mode": "explode"}); err == nil || !strings.Contains(err.Error(), "unsupported failure_mode") {
		t.Fatalf("expected unsupported failure mode, got %v", err)
	}
}

func TestFulfillmentPersistence_RedactsSecretsByDefault(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "api", "name": "API"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "api", "event": "account_active", "app_name": "credentials", "tool_name": "credentials_create",
		"args":                   map[string]any{"provider_api_key": "provider-secret-value"},
		"sensitive_output_paths": []any{"session.exchange_code"},
	}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "secure", "plan_key": "api"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	var inputJSON, outputJSON string
	if err := db.QueryRow(`SELECT input_json, output_json FROM saas_fulfillment_runs WHERE account_id=?`, acct.ID).Scan(&inputJSON, &outputJSON); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"provider-secret-value", "customer-secret-token", "custom-secret-code"} {
		if strings.Contains(inputJSON, secret) || strings.Contains(outputJSON, secret) {
			t.Fatalf("persisted fulfillment leaked %q: input=%s output=%s", secret, inputJSON, outputJSON)
		}
	}
	var input, output map[string]any
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(outputJSON), &output); err != nil {
		t.Fatal(err)
	}
	if input["provider_api_key"] != redactedValue || output["token"] != redactedValue || output["token_id"] != "tok_123" {
		t.Fatalf("unexpected persisted values: input=%+v output=%+v", input, output)
	}
	if mapFromAny(output["session"])["exchange_code"] != redactedValue {
		t.Fatalf("configured path was not redacted: %+v", output)
	}
	var provisioningJSON string
	if err := db.QueryRow(`SELECT output_json FROM saas_provisioning_steps WHERE account_id=? AND step='fulfillment'`, acct.ID).Scan(&provisioningJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(provisioningJSON, "customer-secret-token") || strings.Contains(provisioningJSON, "custom-secret-code") {
		t.Fatalf("provisioning output leaked fulfillment secret: %s", provisioningJSON)
	}
}

func TestFulfillmentPersistence_NoneStoresNoPayload(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "api", "name": "API"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "api", "event": "account_active", "app_name": "credentials", "tool_name": "credentials_create",
		"args": map[string]any{"api_key": "input-secret"}, "persist_input": "none", "persist_output": "none",
	}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "no-payload", "plan_key": "api"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	var inputJSON, outputJSON string
	if err := db.QueryRow(`SELECT input_json, output_json FROM saas_fulfillment_runs WHERE account_id=?`, acct.ID).Scan(&inputJSON, &outputJSON); err != nil {
		t.Fatal(err)
	}
	if inputJSON != "{}" || outputJSON != "{}" {
		t.Fatalf("none persistence stored payloads: input=%s output=%s", inputJSON, outputJSON)
	}
}

func TestFulfillmentPersistence_RedactsErrorsAndHistoricalRuns(t *testing.T) {
	pf := &platformStub{failureMessages: map[string]string{
		"credentials:credentials_create": "upstream rejected api_key=provider-secret-value",
	}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "api", "name": "API"}); err != nil {
		t.Fatal(err)
	}
	added, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "api", "event": "account_active", "app_name": "credentials", "tool_name": "credentials_create",
		"args": map[string]any{"api_key": "provider-secret-value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	action := added.(map[string]any)["action"].(*PlanAction)
	if _, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "failed-secret", "plan_key": "api"}); err == nil || strings.Contains(err.Error(), "provider-secret-value") {
		t.Fatalf("caller received an unredacted fulfillment error: %v", err)
	}
	var storedError string
	if err := db.QueryRow(`SELECT error FROM saas_fulfillment_runs WHERE plan_action_id=?`, action.ID).Scan(&storedError); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedError, "provider-secret-value") || !strings.Contains(storedError, redactedValue) {
		t.Fatalf("stored error was not redacted: %q", storedError)
	}

	if _, err := db.Exec(`
		INSERT INTO saas_fulfillment_runs
			(project_id, account_id, plan_action_id, transition_id, event, app_name, tool_name, status, input_json, output_json, error)
		VALUES ('proj-test', 'legacy', ?, 'legacy-secret', 'account_active', 'credentials', 'credentials_create', 'succeeded',
		        '{"api_key":"old-input-secret","safe":"kept"}',
		        '{"token":"old-output-secret","token_id":"tok_old"}',
		        'provider rejected token=old-output-secret')`, action.ID); err != nil {
		t.Fatal(err)
	}
	if err := scrubFulfillmentHistory(db); err != nil {
		t.Fatal(err)
	}
	var inputJSON, outputJSON, errorText string
	if err := db.QueryRow(`SELECT input_json, output_json, error FROM saas_fulfillment_runs WHERE account_id='legacy'`).Scan(&inputJSON, &outputJSON, &errorText); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"old-input-secret", "old-output-secret"} {
		if strings.Contains(inputJSON, secret) || strings.Contains(outputJSON, secret) || strings.Contains(errorText, secret) {
			t.Fatalf("historical scrub retained %q: input=%s output=%s error=%s", secret, inputJSON, outputJSON, errorText)
		}
	}
	if !strings.Contains(inputJSON, `"safe":"kept"`) || !strings.Contains(outputJSON, `"token_id":"tok_old"`) {
		t.Fatalf("historical scrub removed safe fields: input=%s output=%s", inputJSON, outputJSON)
	}
}

func TestFulfillmentPersistence_RejectsSensitiveStoreMapping(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "api", "name": "API"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "api", "event": "account_active", "app_name": "credentials", "tool_name": "credentials_create",
		"store": map[string]any{"metadata.customer_token": "token"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "unsafe-store", "plan_key": "api"}); err == nil || !strings.Contains(err.Error(), "contains sensitive data") {
		t.Fatalf("expected sensitive store rejection, got %v", err)
	}
	acct, err := dbAccountBySlug(db, "proj-test", "unsafe-store")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(acct.Metadata), "customer-secret-token") {
		t.Fatalf("account metadata leaked token: %s", acct.Metadata)
	}
}

func TestPlanActionPersistence_ValidatesConfiguration(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "free", "event": "account_active", "app_name": "credentials", "tool_name": "credentials_create",
		"persist_output": "none", "store": map[string]any{"metadata.token_id": "token_id"},
	}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("expected none/store validation error, got %v", err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "free", "event": "account_active", "app_name": "credentials", "tool_name": "credentials_create",
		"persist_output": "sometimes",
	}); err == nil || !strings.Contains(err.Error(), "unsupported persistence mode") {
		t.Fatalf("expected persistence mode validation error, got %v", err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "free", "event": "account_active", "app_name": "credentials", "tool_name": "credentials_create",
		"sensitive_output_paths": "token",
	}); err == nil || !strings.Contains(err.Error(), "array") {
		t.Fatalf("expected sensitive path validation error, got %v", err)
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

	acct, _, err := app.createAccount(ctx, map[string]any{
		"owner_email":       "owner@example.com",
		"customer_name":     "Acme Inc",
		"slug":              "acme",
		"plan_key":          "crm-pro",
		"create_owner_user": true,
	})
	if err != nil {
		t.Fatal(err)
	}
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
		"crm:contacts_upsert_by_channel",
		"auth:auth_orgs_create",
		"auth:auth_users_create",
		"entitlements:entitlement_grants_upsert",
		"entitlements:entitlement_limits_set",
	} {
		if !got[key] {
			t.Errorf("missing call %s; calls=%+v", key, pf.calls)
		}
	}
	customer, err := dbCustomerGet(db, "proj-test", acct.CustomerID)
	if err != nil {
		t.Fatal(err)
	}
	if customer.CRMContactID == nil || *customer.CRMContactID != 303 || customer.CRMSyncStatus != "synced" {
		t.Fatalf("customer CRM link was not persisted: %+v", customer)
	}
}

func TestCustomerCreate_CRMSyncFailureIsNonBlockingAndRetryable(t *testing.T) {
	pf := &platformStub{failures: map[string]int{"crm:contacts_upsert_by_channel": 1}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}

	out, err := app.toolCustomerCreate(ctx, map[string]any{
		"email": "buyer@example.com",
		"name":  "Buyer Example",
	})
	if err != nil {
		t.Fatalf("optional CRM failure blocked customer creation: %v", err)
	}
	customer := out.(map[string]any)["customer"].(*Customer)
	if customer.CRMContactID != nil || customer.CRMSyncStatus != "failed" || customer.CRMSyncError == "" {
		t.Fatalf("CRM failure state was not persisted: %+v", customer)
	}
	if _, err := db.Exec(`UPDATE saas_customers SET crm_sync_attempted_at=datetime('now','-7 hours') WHERE id=?`, customer.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.retryPendingCRMCustomers(ctx); err != nil {
		t.Fatal(err)
	}
	customer, err = dbCustomerGet(db, "proj-test", customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if customer.CRMContactID == nil || *customer.CRMContactID != 303 || customer.CRMSyncStatus != "synced" || customer.CRMSyncError != "" {
		t.Fatalf("CRM retry did not persist the contact link: %+v", customer)
	}
	if got := countCalls(pf.calls, "crm", "contacts_upsert_by_channel"); got != 2 {
		t.Fatalf("CRM upsert calls=%d, want 2", got)
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

func TestCheckoutCreate_FreePlanActivatesWithoutCommerce(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}

	out, err := app.toolCheckoutCreate(ctx, map[string]any{
		"owner_email":   "free@example.com",
		"customer_name": "Free Co",
		"slug":          "free-co",
		"plan_key":      "free",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := out.(map[string]any)
	acct := body["account"].(*Account)
	if acct.Status != StatusActive || body["status"] != "active" || body["requires_payment"] != false {
		t.Fatalf("free checkout should activate without payment: account=%+v body=%+v", acct, body)
	}
	for _, key := range []string{
		"billing:customers_upsert_by_email",
		"catalog:catalog_prices_get",
		"subscriptions:subscriptions_create",
		"subscriptions:subscriptions_invoice_create",
		"billing:invoices_send_payment_link",
	} {
		if hasCall(pf.calls, key) {
			t.Fatalf("%s should not run for free checkout; calls=%+v", key, pf.calls)
		}
	}
}

func TestCheckoutCreate_AdminPaymentActivatesAccountAndStoresBillingRefs(t *testing.T) {
	pf := paidInvoicePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key":     "crm-pro",
		"event":        "account_active",
		"app_name":     "containers",
		"tool_name":    "containers_create",
		"failure_mode": "fail_account",
		"args": map[string]any{
			"name":  "saas-{{account.slug}}",
			"image": "example/crm:latest",
			"env":   map[string]any{"SAAS_ACCOUNT_ID": "{{account.id}}", "CUSTOMER_ID": "{{customer.id}}"},
		},
		"store": map[string]any{"metadata.workload_id": "workload.id"},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolCheckoutCreate(ctx, map[string]any{
		"owner_email": "buyer@example.com", "customer_name": "Buyer Co",
		"slug": "buyer", "plan_key": "crm-pro",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := out.(map[string]any)
	checkout := body["checkout"].(*Checkout)
	if body["status"] != "awaiting_payment" {
		t.Fatalf("initial checkout status=%v, want awaiting_payment", body["status"])
	}
	paid, err := app.toolCheckoutMarkPaid(ctx, map[string]any{"checkout_id": checkout.ID, "amount_cents": 2900, "method": "wire"})
	if err != nil {
		t.Fatal(err)
	}
	acct := paid.(map[string]any)["account"].(*Account)
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
		"subscriptions:subscriptions_invoice_prepare",
		"subscriptions:subscription_cycles_create",
		"billing:invoices_create_from_prepared_lines",
		"billing:payments_record",
		"auth:auth_orgs_create",
		"entitlements:entitlement_grants_upsert",
		"entitlements:entitlement_limits_set",
		"containers:containers_create",
	} {
		if !hasCall(pf.calls, key) {
			t.Fatalf("missing call %s; calls=%+v", key, pf.calls)
		}
	}
	container := findCall(pf.calls, "containers", "containers_create")
	if container.Input["name"] != "saas-buyer" || container.Input["image"] != "example/crm:latest" {
		t.Fatalf("container args not expanded: %+v", container.Input)
	}
	env := mapFromAny(container.Input["env"])
	if env["SAAS_ACCOUNT_ID"] != acct.ID || int64FromAny(env["CUSTOMER_ID"]) != acct.CustomerID {
		t.Fatalf("container env not expanded: %+v", env)
	}
	gotAcct, err := dbAccountGet(db, "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strArg(mapFromAny(gotAcct.Metadata), "workload_id") != "wrk_123" {
		t.Fatalf("workload_id not stored in account metadata: %s", gotAcct.Metadata)
	}
	payment := findCall(pf.calls, "billing", "payments_record")
	if payment == nil {
		t.Fatal("payments_record was not called")
	}
	if int64FromAny(payment.Input["amount_cents"]) != 2900 || payment.Input["method"] != "wire" {
		t.Fatalf("bad payment input: %+v", payment.Input)
	}
}

func TestCheckoutMarkPaid_WaitsForConcurrentInvoicePaidEvent(t *testing.T) {
	pf := newPaymentEventRaceStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	pf.onPaid = func() error {
		return app.handleInvoicePaid(ctx, sdk.Event{
			Event: "invoice.paid", ProjectID: "proj-test", SourceApp: "billing",
			Data: map[string]any{"id": 702, "status": "paid"},
		})
	}
	setupPaidCRMPlan(t, app, ctx)

	out, err := app.toolCheckoutCreate(ctx, map[string]any{
		"owner_email": "race@example.com", "customer_name": "Race Co",
		"slug": "race-co", "plan_key": "crm-pro",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkout := out.(map[string]any)["checkout"].(*Checkout)
	paid, err := app.toolCheckoutMarkPaid(ctx, map[string]any{
		"checkout_id": checkout.ID, "amount_cents": 2900, "method": "wire",
	})
	if err != nil {
		t.Fatalf("concurrent invoice.paid event produced a false payment error: %v", err)
	}
	select {
	case err := <-pf.eventDone:
		if err != nil {
			t.Fatalf("invoice.paid event failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("invoice.paid event did not finish")
	}

	body := paid.(map[string]any)
	if body["status"] != "active" || body["payment_processing"] == true {
		t.Fatalf("payment response did not return the completed checkout: %+v", body)
	}
	op, err := dbCommerceOperationByInvoice(db, "proj-test", 702)
	if err != nil {
		t.Fatal(err)
	}
	if op == nil || op.Status != "paid" || op.Stage != "subscription_activated" {
		t.Fatalf("payment operation not completed: %+v", op)
	}
	if got := countCalls(pf.calls, "billing", "payments_record"); got != 1 {
		t.Fatalf("payments_record calls=%d, want 1", got)
	}
	if got := countCalls(pf.calls, "subscriptions", "subscriptions_update_status"); got != 1 {
		t.Fatalf("subscription activation calls=%d, want 1", got)
	}
}

func TestCheckoutCreate_NoCardTrialActivatesAndFulfillsWithoutBilling(t *testing.T) {
	pf := &platformStub{
		entitled:       true,
		priceTrialDays: 7,
		priceMetadata:  map[string]any{"trial_requires_payment_method": false},
	}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key":  "crm-pro",
		"event":     "account_active",
		"app_name":  "containers",
		"tool_name": "containers_create",
		"args":      map[string]any{"name": "saas-{{account.slug}}", "image": "example/crm:latest"},
		"store":     map[string]any{"metadata.workload_id": "workload.id"},
	}); err != nil {
		t.Fatal(err)
	}

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
	if acct.Status != StatusActive || body["status"] != "trialing" || body["requires_payment"] != false {
		t.Fatalf("no-card trial should activate as trialing without payment: account=%+v body=%+v", acct, body)
	}
	if acct.SubscriptionID == nil || *acct.SubscriptionID != 601 {
		t.Fatalf("subscription_id=%v, want 601", acct.SubscriptionID)
	}
	if _, ok := body["payment_link"]; ok {
		t.Fatalf("no-card trial returned payment_link: %+v", body["payment_link"])
	}
	if _, ok := body["url"]; ok {
		t.Fatalf("no-card trial returned url: %+v", body["url"])
	}
	for _, key := range []string{
		"billing:customers_upsert_by_email",
		"subscriptions:subscriptions_invoice_create",
		"billing:invoices_send_payment_link",
		"billing:payment_method_setup_create",
	} {
		if hasCall(pf.calls, key) {
			t.Fatalf("%s should not run for no-card trial; calls=%+v", key, pf.calls)
		}
	}
	for _, key := range []string{
		"catalog:catalog_prices_get",
		"subscriptions:subscriptions_create",
		"containers:containers_create",
	} {
		if !hasCall(pf.calls, key) {
			t.Fatalf("missing call %s; calls=%+v", key, pf.calls)
		}
	}
	subCreate := findCall(pf.calls, "subscriptions", "subscriptions_create")
	if subCreate == nil {
		t.Fatal("subscriptions_create was not called")
	}
	trialStart, startOK := parseTimestamp(strFromAny(subCreate.Input["trial_start"]))
	trialEnd, endOK := parseTimestamp(strFromAny(subCreate.Input["trial_end"]))
	if subCreate.Input["status"] != "trialing" || !startOK || !endOK || trialEnd.Sub(trialStart) != 7*24*time.Hour {
		t.Fatalf("bad trial subscription input: %+v", subCreate.Input)
	}
	subMeta := mapFromAny(subCreate.Input["metadata"])
	if subMeta["payment_method_missing"] != true || subMeta["trial_requires_payment_method"] != false || subMeta["payment_required_at"] != subCreate.Input["trial_end"] {
		t.Fatalf("trial metadata missing on subscription: %+v", subMeta)
	}
	gotAcct, err := dbAccountGet(db, "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta := mapFromAny(gotAcct.Metadata)
	if meta["checkout_status"] != "trialing" || meta["payment_method_missing"] != true || meta["payment_required_at"] != subCreate.Input["trial_end"] {
		t.Fatalf("trial metadata missing on account: %+v", meta)
	}
	if strArg(meta, "workload_id") != "wrk_123" {
		t.Fatalf("fulfillment did not store workload_id: %+v", meta)
	}
	access, err := app.toolAccessCheck(ctx, map[string]any{"account_id": acct.ID, "feature_key": "crm:contacts"})
	if err != nil {
		t.Fatal(err)
	}
	if !access.(map[string]any)["allowed"].(bool) {
		t.Fatalf("trial account should allow access: %+v", access)
	}
}

func TestCheckoutCreate_CardRequiredTrialWaitsForSetup(t *testing.T) {
	pf := &platformStub{entitled: true, contacts: 0, priceTrialDays: 7, priceMetadata: map[string]any{"trial_requires_payment_method": true}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key":  "crm-pro",
		"event":     "account_active",
		"app_name":  "containers",
		"tool_name": "containers_create",
		"args":      map[string]any{"name": "saas-{{account.slug}}", "image": "example/crm:latest"},
		"store":     map[string]any{"metadata.workload_id": "workload.id"},
	}); err != nil {
		t.Fatal(err)
	}

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
	if acct.Status != StatusSuspended || body["status"] != "awaiting_payment_method" {
		t.Fatalf("checkout should await payment method with suspended account: status=%s body=%+v", acct.Status, body)
	}
	setup := mapFromAny(body["setup_session"])
	if strArg(setup, "url") != "https://pay.example/setup" {
		t.Fatalf("setup link missing: %+v", body["setup_session"])
	}
	if hasCall(pf.calls, "billing:payments_record") {
		t.Fatalf("payments_record should not run for payment_link; calls=%+v", pf.calls)
	}
	if hasCall(pf.calls, "containers:containers_create") {
		t.Fatalf("containers_create should not run before payment method setup; calls=%+v", pf.calls)
	}
	access, err := app.toolAccessCheck(ctx, map[string]any{"account_id": acct.ID, "feature_key": "crm:contacts"})
	if err != nil {
		t.Fatal(err)
	}
	if access.(map[string]any)["allowed"].(bool) {
		t.Fatalf("unpaid past_due checkout should not allow access: %+v", access)
	}
}

func TestAccountCreate_RejectsPaidPlanBypass(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"}); err == nil || !strings.Contains(err.Error(), "saas_checkout_create") {
		t.Fatalf("paid-plan direct creation should be rejected, got %v", err)
	}
}

func TestCheckoutCreate_RejectsPublicPaymentOverrides(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	for _, args := range []map[string]any{
		{"activate_without_payment": true},
		{"record_payment": true},
		{"manual_payment_method": "wire"},
		{"payment_mode": "manual"},
		{"payment_mode": "none"},
	} {
		args["owner_email"] = "buyer@example.com"
		args["slug"] = "buyer"
		args["plan_key"] = "crm-pro"
		if _, err := app.toolCheckoutCreate(ctx, args); err == nil {
			t.Fatalf("unsafe checkout args should be rejected: %+v", args)
		}
	}
}

func TestCheckoutCreate_IsIdempotentAcrossCompletedRetries(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	args := map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro", "idempotency_key": "checkout-123"}
	first, err := app.toolCheckoutCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.toolCheckoutCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["checkout"].(*Checkout).ID != second.(map[string]any)["checkout"].(*Checkout).ID {
		t.Fatal("idempotent checkout returned different IDs")
	}
	for _, call := range []struct{ app, tool string }{
		{"subscriptions", "subscriptions_create"},
		{"subscriptions", "subscription_cycles_create"},
		{"billing", "invoices_create_from_prepared_lines"},
		{"billing", "invoices_send_payment_link"},
	} {
		if got := countCalls(pf.calls, call.app, call.tool); got != 1 {
			t.Fatalf("%s.%s calls=%d, want 1", call.app, call.tool, got)
		}
	}
	conflict := map[string]any{"owner_email": "other@example.com", "slug": "other", "plan_key": "crm-pro", "idempotency_key": "checkout-123"}
	if _, err := app.toolCheckoutCreate(ctx, conflict); err == nil || !strings.Contains(err.Error(), "different checkout") {
		t.Fatalf("idempotency conflict should be rejected, got %v", err)
	}
}

func TestCheckoutCreate_DiscountCodeReservesPersistsRedeemsAndBillsNetLines(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)

	out, err := app.toolCheckoutCreate(ctx, map[string]any{
		"owner_email": "discount@example.com", "slug": "discount", "plan_key": "crm-pro",
		"discount_code": " half3 ", "idempotency_key": "discount-checkout-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := out.(map[string]any)
	discount := body["discount"].(*CheckoutDiscount)
	if discount.Status != "redeemed" || discount.DiscountCode != "HALF3" || discount.SubtotalCents != 2900 || discount.DiscountCents != 1450 || discount.TotalCents != 1450 {
		t.Fatalf("unexpected checkout discount: %+v", discount)
	}
	pricing := mapFromAny(body["pricing"])
	if int64FromAny(pricing["total_cents"]) != 1450 || int64FromAny(pricing["discount_cents"]) != 1450 {
		t.Fatalf("unexpected pricing: %+v", pricing)
	}
	reserve := findCall(pf.calls, "catalog", "catalog_discounts_reserve")
	if reserve == nil || reserve.Input["code"] != "HALF3" || reserve.Input["context_ref"] != body["checkout"].(*Checkout).ID || int64FromAny(reserve.Input["expires_in_seconds"]) != 86400 {
		t.Fatalf("bad Catalog reservation call: %+v", reserve)
	}
	subCreate := findCall(pf.calls, "subscriptions", "subscriptions_create")
	if subCreate == nil {
		t.Fatal("subscriptions_create missing")
	}
	discounts := sliceFromAny(subCreate.Input["discounts"])
	if len(discounts) != 1 {
		t.Fatalf("subscription discounts=%+v", discounts)
	}
	snapshot := mapFromAny(discounts[0])
	if snapshot["source_app"] != "catalog" || snapshot["source_ref"] != "dres_123" || int64FromAny(snapshot["catalog_price_id"]) != 8001 || int64FromAny(mapFromAny(snapshot["application"])["percentage_bps"]) != 5000 {
		t.Fatalf("bad immutable subscription snapshot: %+v", snapshot)
	}
	prepare := findCall(pf.calls, "subscriptions", "subscriptions_invoice_prepare")
	if prepare == nil || int64FromAny(prepare.Input["cycle_id"]) != 801 {
		t.Fatalf("invoice preparation was not tied to its cycle: %+v", prepare)
	}
	invoice := findCall(pf.calls, "billing", "invoices_create_from_prepared_lines")
	line := mapFromAny(sliceFromAny(invoice.Input["line_items"])[0])
	if int64FromAny(line["unit_price_cents"]) != 1450 {
		t.Fatalf("Billing did not receive Subscriptions net line: %+v", line)
	}
	if countCalls(pf.calls, "catalog", "catalog_discounts_reserve") != 1 || countCalls(pf.calls, "catalog", "catalog_discounts_redeem") != 1 {
		t.Fatalf("discount calls were not exactly once: %+v", pf.calls)
	}
}

func TestCheckoutCreate_ZeroTotalDiscountActivatesWithoutBilling(t *testing.T) {
	pf := &platformStub{discountPercentageBPS: 10000}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	out, err := app.toolCheckoutCreate(ctx, map[string]any{
		"owner_email": "free-cycle@example.com", "slug": "free-cycle", "plan_key": "crm-pro", "discount_code": "FREE100",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := out.(map[string]any)
	if body["status"] != "active" || body["requires_payment"] != false || body["account"].(*Account).Status != StatusActive {
		t.Fatalf("zero-total checkout did not activate: %+v", body)
	}
	if int64FromAny(mapFromAny(body["pricing"])["total_cents"]) != 0 {
		t.Fatalf("zero-total pricing missing: %+v", body["pricing"])
	}
	for _, key := range []string{"billing:customers_upsert_by_email", "billing:invoices_create_from_prepared_lines", "billing:invoices_send_payment_link"} {
		if hasCall(pf.calls, key) {
			t.Fatalf("%s should not run for a zero-total cycle: %+v", key, pf.calls)
		}
	}
	cycleUpdate := findCall(pf.calls, "subscriptions", "subscription_cycles_update")
	if cycleUpdate == nil || cycleUpdate.Input["payment_status"] != "paid" {
		t.Fatalf("zero-total cycle was not marked paid: %+v", cycleUpdate)
	}
	subCreate := findCall(pf.calls, "subscriptions", "subscriptions_create")
	if subCreate == nil || subCreate.Input["status"] != "active" {
		t.Fatalf("zero-total subscription was not active: %+v", subCreate)
	}
}

func TestCheckoutCreate_AutomaticPlanDiscountAndCodeDoNotStack(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolPlanUpsert(ctx, map[string]any{
		"key": "crm-pro", "name": "CRM Pro", "billing_mode": "paid", "catalog_product_id": 7001,
		"catalog_price_id": 8001, "catalog_discount_id": 9100, "subscription_required": true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "stack@example.com", "slug": "stack", "plan_key": "crm-pro", "discount_code": "EXTRA"}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("expected stacking rejection, got %v", err)
	}
	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "auto@example.com", "slug": "auto", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	reserve := findCall(pf.calls, "catalog", "catalog_discounts_reserve")
	if reserve == nil || int64FromAny(reserve.Input["discount_id"]) != 9100 {
		t.Fatalf("automatic plan discount was not reserved: %+v", reserve)
	}
	if out.(map[string]any)["discount"].(*CheckoutDiscount).CatalogDiscountID != 9100 {
		t.Fatalf("automatic discount missing from checkout: %+v", out)
	}
}

func TestDiscountConfigurationRejectsInvalidTargetsAndCodes(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "bad", "name": "Bad", "billing_mode": "paid", "catalog_discount_id": 42, "subscription_required": true}); err == nil || !strings.Contains(err.Error(), "requires catalog_price_id") {
		t.Fatalf("expected price requirement, got %v", err)
	}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "bad@example.com", "slug": "bad", "plan_key": "crm-pro", "discount_code": "not valid"}); err == nil || !strings.Contains(err.Error(), "letters, numbers") {
		t.Fatalf("expected code validation, got %v", err)
	}
	if hasCall(pf.calls, "catalog:catalog_discounts_reserve") {
		t.Fatalf("invalid code reached Catalog: %+v", pf.calls)
	}
}

func TestCheckoutCreate_DiscountReserveRetryUsesSameCatalogIdempotencyKey(t *testing.T) {
	pf := &platformStub{failures: map[string]int{"catalog:catalog_discounts_reserve": 1}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	args := map[string]any{"owner_email": "retry@example.com", "slug": "retry", "plan_key": "crm-pro", "discount_code": "HALF3", "idempotency_key": "retry-discount"}
	if _, err := app.toolCheckoutCreate(ctx, args); err == nil {
		t.Fatal("expected injected reserve failure")
	}
	if _, err := app.toolCheckoutCreate(ctx, args); err != nil {
		t.Fatalf("checkout retry failed: %v", err)
	}
	var keys []string
	for _, call := range pf.calls {
		if call.App == "catalog" && call.Tool == "catalog_discounts_reserve" {
			keys = append(keys, strFromAny(call.Input["idempotency_key"]))
		}
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("Catalog idempotency keys=%v", keys)
	}
	if countCalls(pf.calls, "subscriptions", "subscriptions_create") != 1 {
		t.Fatalf("subscription creation was duplicated: %+v", pf.calls)
	}
}

func TestCheckoutCreate_ExpiredUnusedDiscountGetsANewReservationAttempt(t *testing.T) {
	pf := &platformStub{failures: map[string]int{"subscriptions:subscriptions_create": 1}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	args := map[string]any{"owner_email": "expired@example.com", "slug": "expired", "plan_key": "crm-pro", "discount_code": "HALF3"}
	if _, err := app.toolCheckoutCreate(ctx, args); err == nil {
		t.Fatal("expected injected subscription failure")
	}
	if _, err := db.Exec(`UPDATE saas_checkout_discounts SET expires_at='2020-01-01T00:00:00Z' WHERE project_id='proj-test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolCheckoutCreate(ctx, args); err != nil {
		t.Fatalf("retry after expired unused reservation: %v", err)
	}
	var keys []string
	for _, call := range pf.calls {
		if call.App == "catalog" && call.Tool == "catalog_discounts_reserve" {
			keys = append(keys, strFromAny(call.Input["idempotency_key"]))
		}
	}
	if len(keys) != 2 || keys[0] == keys[1] || !strings.HasSuffix(keys[1], ":2") {
		t.Fatalf("reservation attempt keys=%v", keys)
	}
	checkout, _ := dbCheckoutLookup(db, "proj-test", map[string]any{"slug": "expired"})
	discount, _ := dbCheckoutDiscountGet(db, "proj-test", checkout.ID)
	if discount.AttemptCount != 2 || discount.Status != "redeemed" {
		t.Fatalf("replacement reservation was not committed: %+v", discount)
	}
}

func TestCheckoutRecovery_RedeemsDiscountAfterInterruptedResponse(t *testing.T) {
	pf := &platformStub{failures: map[string]int{"catalog:catalog_discounts_redeem": 1}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	args := map[string]any{"owner_email": "recover@example.com", "slug": "recover", "plan_key": "crm-pro", "discount_code": "HALF3"}
	if _, err := app.toolCheckoutCreate(ctx, args); err == nil {
		t.Fatal("expected injected redemption failure")
	}
	checkout, err := dbCheckoutLookup(db, "proj-test", map[string]any{"slug": "recover"})
	if err != nil || checkout == nil || int64PtrValue(checkout.SubscriptionID) == 0 {
		t.Fatalf("durable subscription state missing: checkout=%+v err=%v", checkout, err)
	}
	discount, err := dbCheckoutDiscountGet(db, "proj-test", checkout.ID)
	if err != nil || discount == nil || discount.Status != "reserved" || discount.LastError == "" {
		t.Fatalf("durable discount state missing: discount=%+v err=%v", discount, err)
	}
	if err := app.recoverExpiredCheckouts(ctx); err != nil {
		t.Fatal(err)
	}
	discount, err = dbCheckoutDiscountGet(db, "proj-test", checkout.ID)
	if err != nil || discount.Status != "redeemed" || discount.LastError != "" {
		t.Fatalf("discount was not reconciled: discount=%+v err=%v", discount, err)
	}
	if countCalls(pf.calls, "catalog", "catalog_discounts_reserve") != 1 || countCalls(pf.calls, "catalog", "catalog_discounts_redeem") != 2 || countCalls(pf.calls, "subscriptions", "subscriptions_create") != 1 {
		t.Fatalf("unexpected recovery calls: %+v", pf.calls)
	}
}

func TestPaymentMethodAttached_ActivatesCardRequiredTrial(t *testing.T) {
	pf := &platformStub{priceTrialDays: 7, priceMetadata: map[string]any{"trial_requires_payment_method": true}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "crm-pro", "event": "account_active", "app_name": "containers", "tool_name": "containers_create",
		"args": map[string]any{"name": "saas-{{account.slug}}", "image": "example/crm:latest"},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	checkout := out.(map[string]any)["checkout"].(*Checkout)
	if err := app.handlePaymentMethodAttached(ctx, sdk.Event{Event: "payment_method.attached", ProjectID: "proj-test", Data: map[string]any{
		"id": 1201, "customer_id": 501, "status": "active", "metadata": map[string]any{"saas_checkout_id": checkout.ID},
	}}); err != nil {
		t.Fatal(err)
	}
	checkout, err = dbCheckoutGet(db, "proj-test", checkout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Status != "trialing" || int64PtrValue(checkout.PaymentMethodID) != 1201 {
		t.Fatalf("checkout not activated after setup: %+v", checkout)
	}
	account, _ := dbAccountGet(db, "proj-test", checkout.AccountID)
	if account.Status != StatusActive || countCalls(pf.calls, "containers", "containers_create") != 1 {
		t.Fatalf("card-required trial was not fulfilled: account=%+v calls=%+v", account, pf.calls)
	}
	if hasCall(pf.calls, "billing:invoices_create_from_prepared_lines") {
		t.Fatal("card-required trial must not create an invoice at signup")
	}
}

func TestTrialCycleDue_UpdatesCheckoutAndPaidEventActivates(t *testing.T) {
	pf := paidInvoicePlatformStub()
	pf.priceTrialDays = 7
	pf.priceMetadata = map[string]any{"trial_requires_payment_method": false}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	checkout := out.(map[string]any)["checkout"].(*Checkout)
	if err := app.handleSubscriptionLifecycle(ctx, sdk.Event{Event: "subscription.past_due", ProjectID: "proj-test", Data: map[string]any{"subscription_id": 601, "status": "past_due"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleSubscriptionCycleDue(ctx, sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 601, "cycle_id": 803, "currency": "USD", "period_start": "2026-07-12T00:00:00Z", "period_end": "2026-08-12T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	checkout, _ = dbCheckoutGet(db, "proj-test", checkout.ID)
	if checkout.Status != "awaiting_payment" || checkout.PaymentURL != "" || int64PtrValue(checkout.InvoiceID) != 702 {
		t.Fatalf("trial checkout not updated for automatic collection: %+v", checkout)
	}
	if !hasCall(pf.calls, "billing:invoices_collect") || hasCall(pf.calls, "billing:invoices_send_payment_link") {
		t.Fatalf("default paid plan should collect automatically: %+v", pf.calls)
	}
	if err := app.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: "proj-test", Data: map[string]any{"id": 702, "status": "paid"}}); err != nil {
		t.Fatal(err)
	}
	checkout, _ = dbCheckoutGet(db, "proj-test", checkout.ID)
	if checkout.Status != "active" {
		t.Fatalf("paid trial checkout status=%s, want active", checkout.Status)
	}
}

func TestTrialCycleDue_ZeroTotalDiscountActivatesWithoutBilling(t *testing.T) {
	pf := &platformStub{
		priceTrialDays: 7, priceMetadata: map[string]any{"trial_requires_payment_method": false}, discountPercentageBPS: 10000,
	}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "trial-free@example.com", "slug": "trial-free", "plan_key": "crm-pro", "discount_code": "FREE100"})
	if err != nil {
		t.Fatal(err)
	}
	checkout := out.(map[string]any)["checkout"].(*Checkout)
	if err := app.handleSubscriptionLifecycle(ctx, sdk.Event{Event: "subscription.past_due", ProjectID: "proj-test", Data: map[string]any{"subscription_id": 601, "status": "past_due"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleSubscriptionCycleDue(ctx, sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 601, "cycle_id": 804, "currency": "USD", "period_start": "2026-07-12T00:00:00Z", "period_end": "2026-08-12T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	checkout, _ = dbCheckoutGet(db, "proj-test", checkout.ID)
	if checkout.Status != "active" || int64PtrValue(checkout.InvoiceID) != 0 {
		t.Fatalf("zero-total trial cycle did not activate cleanly: %+v", checkout)
	}
	for _, key := range []string{"billing:customers_upsert_by_email", "billing:invoices_create_from_prepared_lines", "billing:invoices_send_payment_link"} {
		if hasCall(pf.calls, key) {
			t.Fatalf("%s should not run for a zero-total trial cycle: %+v", key, pf.calls)
		}
	}
	statusCall := findCall(pf.calls, "subscriptions", "subscriptions_update_status")
	if statusCall == nil || statusCall.Input["status"] != "active" {
		t.Fatalf("zero-total trial subscription was not activated: %+v", statusCall)
	}
}

func TestCycleDue_ReconcilesExistingBillingInvoice(t *testing.T) {
	pf := &platformStub{existingInvoiceOperationKey: "subscription:77:cycle:88"}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	created, _, err := app.createAccount(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "acme", "plan_key": "free", "subscription_id": 77})
	if err != nil {
		t.Fatal(err)
	}
	_ = created
	if err := app.handleSubscriptionCycleDue(ctx, sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 77, "cycle_id": 88, "currency": "USD", "period_start": "2026-07-01T00:00:00Z", "period_end": "2026-08-01T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	if hasCall(pf.calls, "billing:invoices_create_from_prepared_lines") {
		t.Fatal("reconciled invoice should not be created again")
	}
	op, _ := dbCommerceOperationByInvoice(db, "proj-test", 799)
	if op == nil || op.Status != "awaiting_payment" {
		t.Fatalf("existing invoice was not adopted: %+v", op)
	}
}

func TestPastDueGraceKeepsAccountActiveUntilSubscriptionEnds(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}
	created, _, err := app.createAccount(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "grace", "plan_key": "free", "subscription_id": 77})
	if err != nil {
		t.Fatal(err)
	}
	account := created
	pastDueSince := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := app.handleSubscriptionLifecycle(ctx, sdk.Event{Event: "subscription.past_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 77, "status": "past_due", "metadata": map[string]any{"past_due_since": pastDueSince, "unpaid_grace_days": 3},
	}}); err != nil {
		t.Fatal(err)
	}
	account, _ = dbAccountGet(db, "proj-test", account.ID)
	if account.Status != StatusActive || strArg(mapFromAny(account.Metadata), "unpaid_grace_until") == "" {
		t.Fatalf("grace should retain active access: %+v", account)
	}
}

func TestExpiredSetupSessionIsPaused(t *testing.T) {
	pf := &platformStub{priceTrialDays: 7, priceMetadata: map[string]any{"trial_requires_payment_method": true}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	checkout := out.(map[string]any)["checkout"].(*Checkout)
	if _, err := db.Exec(`UPDATE saas_checkouts SET session_expires_at='2020-01-01T00:00:00Z' WHERE id=?`, checkout.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.recoverExpiredCheckouts(ctx); err != nil {
		t.Fatal(err)
	}
	checkout, _ = dbCheckoutGet(db, "proj-test", checkout.ID)
	if checkout.Status != "setup_expired" {
		t.Fatalf("expired checkout status=%s", checkout.Status)
	}
	statusCall := findCall(pf.calls, "subscriptions", "subscriptions_update_status")
	if statusCall == nil || statusCall.Input["status"] != "paused" {
		t.Fatalf("expired setup did not pause subscription: %+v", statusCall)
	}
}

func TestInvoiceRefundMovesPaidCheckoutPastDue(t *testing.T) {
	pf := paidInvoicePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	checkout := out.(map[string]any)["checkout"].(*Checkout)
	if _, err := app.toolCheckoutMarkPaid(ctx, map[string]any{"checkout_id": checkout.ID, "amount_cents": 2900, "method": "wire"}); err != nil {
		t.Fatal(err)
	}
	pf.invoiceGetStatus = "open"
	pf.invoiceGetPaidAt = ""
	pf.invoiceGetAmountPaid = 0
	pf.invoiceGetPayments = []map[string]any{{"id": 901, "amount_cents": 2900, "currency": "USD", "method": "wire", "received_at": "2026-06-01T10:00:00Z"}, {"id": 902, "amount_cents": -2900, "currency": "USD", "method": "wire", "received_at": "2026-07-01T10:00:00Z"}}
	if err := app.handleInvoiceCollectionFailed(ctx, sdk.Event{Event: "invoice.refunded", ProjectID: "proj-test", Data: map[string]any{"id": 702, "status": "paid"}}); err != nil {
		t.Fatal(err)
	}
	checkout, _ = dbCheckoutGet(db, "proj-test", checkout.ID)
	account, _ := dbAccountGet(db, "proj-test", checkout.AccountID)
	if checkout.Status != "payment_failed" || account.Status != StatusPastDue {
		t.Fatalf("refund policy not applied: checkout=%+v account=%+v", checkout, account)
	}
	var refunded bool
	for _, call := range pf.calls {
		if call.App == "subscriptions" && call.Tool == "subscription_cycles_update" && call.Input["payment_status"] == "refunded" {
			refunded = true
		}
	}
	if !refunded {
		t.Fatal("refund did not update subscription cycle")
	}
	accountOut, err := app.toolAccountGet(ctx, map[string]any{"account_id": account.ID})
	if err != nil {
		t.Fatal(err)
	}
	billing := accountOut.(map[string]any)["account"].(*Account).Billing
	if billing == nil || !billing.HasPaid || billing.PaidInvoiceCount != 0 || billing.NetPaidByCurrency["USD"] != 0 {
		t.Fatalf("refund was not reflected in Billing summary: %+v", billing)
	}
}

func TestPendingInvoiceReconciliationRecoversMissedPaidEvent(t *testing.T) {
	pf := &platformStub{invoiceGetStatus: "paid"}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	setupPaidCRMPlan(t, app, ctx)
	out, err := app.toolCheckoutCreate(ctx, map[string]any{"owner_email": "buyer@example.com", "slug": "buyer", "plan_key": "crm-pro"})
	if err != nil {
		t.Fatal(err)
	}
	checkout := out.(map[string]any)["checkout"].(*Checkout)
	if _, err := db.Exec(`UPDATE saas_commerce_operations SET updated_at='2020-01-01 00:00:00' WHERE invoice_id=702`); err != nil {
		t.Fatal(err)
	}
	if err := app.reconcilePendingInvoices(ctx, "proj-test"); err != nil {
		t.Fatal(err)
	}
	checkout, _ = dbCheckoutGet(db, "proj-test", checkout.ID)
	account, _ := dbAccountGet(db, "proj-test", checkout.AccountID)
	if checkout.Status != "active" || account.Status != StatusActive {
		t.Fatalf("missed payment was not recovered: checkout=%+v account=%+v", checkout, account)
	}
}

func TestFulfillmentLifecycleActionsUseStoredMetadata(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}

	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "container-pro", "name": "Container Pro"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key":  "container-pro",
		"event":     "account_active",
		"app_name":  "containers",
		"tool_name": "containers_create",
		"args":      map[string]any{"name": "saas-{{account.slug}}", "image": "nginx:alpine"},
		"store":     map[string]any{"metadata.workload_id": "workload.id"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key":  "container-pro",
		"event":     "account_past_due",
		"app_name":  "containers",
		"tool_name": "containers_stop",
		"args":      map[string]any{"workload_id": "{{account.metadata.workload_id}}"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key":  "container-pro",
		"event":     "account_resumed",
		"app_name":  "containers",
		"tool_name": "containers_start",
		"args":      map[string]any{"workload_id": "{{account.metadata.workload_id}}"},
	}); err != nil {
		t.Fatal(err)
	}

	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "container-acme", "plan_key": "container-pro", "subscription_id": 99})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	stored, err := dbAccountGet(db, "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strArg(mapFromAny(stored.Metadata), "workload_id") != "wrk_123" {
		t.Fatalf("active fulfillment did not store workload id: %s", stored.Metadata)
	}
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"subscription_id": 99, "subscription_status": "past_due"}); err != nil {
		t.Fatal(err)
	}
	stop := findCall(pf.calls, "containers", "containers_stop")
	if stop == nil || stop.Input["workload_id"] != "wrk_123" {
		t.Fatalf("past_due fulfillment did not use stored workload id: %+v calls=%+v", stop, pf.calls)
	}
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"subscription_id": 99, "subscription_status": "resumed"}); err != nil {
		t.Fatal(err)
	}
	start := findCall(pf.calls, "containers", "containers_start")
	if start == nil || start.Input["workload_id"] != "wrk_123" {
		t.Fatalf("resumed fulfillment did not use stored workload id: %+v calls=%+v", start, pf.calls)
	}
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"subscription_id": 99, "subscription_status": "past_due"}); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(pf.calls, "containers", "containers_stop"); got != 2 {
		t.Fatalf("a later past_due transition should run again; calls=%d", got)
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

func TestUsageSync_EmitsQuotaTransitionsWithoutDuplicates(t *testing.T) {
	pf := &platformStub{contacts: 79}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	recorder := tk.NewEmitRecorder()
	ctx.SetEmitter(recorder)
	app := &App{}

	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "free", "name": "Free"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanLimitSet(ctx, map[string]any{
		"plan_key": "free", "feature_key": "crm:contacts", "limit_value": 100,
		"metadata": map[string]any{"warning_threshold_percent": 80},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanUsageSourceAdd(ctx, map[string]any{"plan_key": "free", "app_name": "crm", "tool_name": "crm_saas_usage_snapshot"}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "quota-events", "plan_key": "free"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	recorder.Reset()

	syncUsage := func() {
		t.Helper()
		if _, err := app.toolUsageSync(ctx, map[string]any{"account_id": acct.ID}); err != nil {
			t.Fatal(err)
		}
	}
	syncUsage() // 79%, below
	pf.contacts = 80
	syncUsage() // approaching
	pf.contacts = 90
	syncUsage() // still approaching: no duplicate
	pf.contacts = 100
	syncUsage() // reached
	pf.contacts = 101
	syncUsage() // exceeded
	pf.contacts = 120
	syncUsage() // still exceeded: no duplicate
	pf.contacts = 95
	syncUsage() // recovered to approaching
	syncUsage() // no duplicate recovery
	pf.emptyUsage = true
	syncUsage() // complete empty snapshot recovers to below

	events := recorder.Events()
	wantTopics := []string{
		"saas.quota.approaching",
		"saas.quota.reached",
		"saas.quota.exceeded",
		"saas.quota.recovered",
		"saas.quota.recovered",
	}
	if len(events) != len(wantTopics) {
		t.Fatalf("emitted events=%d, want %d: %+v", len(events), len(wantTopics), events)
	}
	for i, want := range wantTopics {
		if events[i].Topic != want || events[i].ProjectID != "proj-test" {
			t.Fatalf("event[%d]=%+v, want topic=%s project=proj-test", i, events[i], want)
		}
	}
	payload := events[0].Data.(map[string]any)
	if payload["account_id"] != acct.ID || payload["plan_key"] != "free" || payload["feature_key"] != "crm:contacts" {
		t.Fatalf("approaching payload identity is incomplete: %+v", payload)
	}
	if payload["quantity"] != int64(80) || payload["limit"] != int64(100) || payload["threshold_percent"] != int64(80) || payload["state"] != quotaStateApproaching {
		t.Fatalf("approaching payload has wrong quota values: %+v", payload)
	}
	lastPayload := events[len(events)-1].Data.(map[string]any)
	if lastPayload["previous_state"] != quotaStateApproaching || lastPayload["state"] != quotaStateBelow || lastPayload["quantity"] != int64(0) {
		t.Fatalf("empty-snapshot recovery payload is wrong: %+v", lastPayload)
	}
	var state string
	var quantity int64
	if err := db.QueryRow(`SELECT state, quantity FROM saas_quota_states WHERE project_id=? AND account_id=? AND feature_key=?`, "proj-test", acct.ID, "crm:contacts").Scan(&state, &quantity); err != nil {
		t.Fatal(err)
	}
	if state != quotaStateBelow || quantity != 0 {
		t.Fatalf("persisted quota state=%s quantity=%d, want below/0", state, quantity)
	}
}

func TestUsageRecord_EmitsQuotaTransition(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	recorder := tk.NewEmitRecorder()
	ctx.SetEmitter(recorder)
	app := &App{}

	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "free", "name": "Free"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanLimitSet(ctx, map[string]any{"plan_key": "free", "feature_key": "storage:bytes", "limit_value": 10}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "manual-quota", "plan_key": "free"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	recorder.Reset()
	if _, err := app.toolUsageRecord(ctx, map[string]any{"account_id": acct.ID, "feature_key": "storage:bytes", "quantity": 8}); err != nil {
		t.Fatal(err)
	}
	events := recorder.EventsByTopic("saas.quota.approaching")
	if len(events) != 1 {
		t.Fatalf("manual usage approaching events=%d, want 1: %+v", len(events), recorder.Events())
	}
}

func TestPlanLimitSet_ValidatesWarningThreshold(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "free", "name": "Free"}); err != nil {
		t.Fatal(err)
	}
	for _, threshold := range []int{0, 100} {
		_, err := app.toolPlanLimitSet(ctx, map[string]any{
			"plan_key": "free", "feature_key": "crm:contacts", "limit_value": 100,
			"metadata": map[string]any{"warning_threshold_percent": threshold},
		})
		if err == nil || !strings.Contains(err.Error(), "between 1 and 99") {
			t.Fatalf("threshold %d error=%v, want validation error", threshold, err)
		}
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

func TestPlanListDoesNotHoldRowsWhileHydrating(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	db.SetMaxOpenConns(1)
	app := &App{}

	if _, err := app.toolPlanLimitSet(ctx, map[string]any{"plan_key": "free", "feature_key": "hosting.tenants", "limit_value": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key":  "free",
		"event":     "account_active",
		"app_name":  "containers",
		"tool_name": "containers_create",
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		out, err := app.toolPlanList(ctx, map[string]any{})
		if err == nil {
			plans := out.(map[string]any)["plans"].([]*Plan)
			if len(plans) != 1 || len(plans[0].Limits) != 1 || len(plans[0].Actions) != 1 {
				err = fmt.Errorf("plan hydration mismatch: %+v", plans)
			}
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("plan list hung while hydrating child rows")
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
	if got.Status != StatusCancelled {
		t.Fatalf("status=%s, want cancelled", got.Status)
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
	if got.Status != StatusCancelled {
		t.Fatalf("status=%s, want cancelled from data.status override", got.Status)
	}

	if err := app.handleSubscriptionLifecycle(ctx, sdk.Event{Event: "subscription.active", Data: map[string]any{}}); err != nil {
		t.Fatalf("missing subscription id should be ignored, got %v", err)
	}
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"account_id": acct.ID, "subscription_status": "resumed", "allow_cancelled_reactivation": true}); err != nil {
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

func TestFulfillment_DuplicateLifecycleEventRunsOnce(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "container", "name": "Container"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{"plan_key": "container", "event": "account_active", "app_name": "containers", "tool_name": "containers_create", "args": map[string]any{"name": "{{account.slug}}", "image": "nginx"}}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "once", "plan_key": "container", "subscription_id": 501})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	for i := 0; i < 2; i++ {
		if _, err := app.toolSubscriptionSync(ctx, map[string]any{"account_id": acct.ID, "subscription_id": 501, "subscription_status": "active"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := countCalls(pf.calls, "containers", "containers_create"); got != 1 {
		t.Fatalf("containers_create calls=%d, want 1", got)
	}
}

func TestProvisioning_RetriesFailedFulfillmentWithoutDuplicateRun(t *testing.T) {
	pf := &platformStub{failures: map[string]int{"containers:containers_create": 1}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "container", "name": "Container"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{"plan_key": "container", "event": "account_active", "app_name": "containers", "tool_name": "containers_create", "args": map[string]any{"name": "{{account.slug}}", "image": "nginx"}}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"owner_email": "owner@example.com", "slug": "recover", "plan_key": "container"}
	if _, err := app.toolAccountCreate(ctx, args); err == nil {
		t.Fatal("first provisioning attempt should fail")
	}
	failed, err := dbAccountBySlug(db, "proj-test", "recover")
	if err != nil || failed.Status != StatusFailed {
		t.Fatalf("failed account=%+v err=%v", failed, err)
	}
	out, err := app.toolAccountCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	acct := out.(map[string]any)["account"].(*Account)
	if acct.Status != StatusActive {
		t.Fatalf("status=%s, want active", acct.Status)
	}
	if got := countCalls(pf.calls, "containers", "containers_create"); got != 2 {
		t.Fatalf("containers_create calls=%d, want failed call plus one retry", got)
	}
	var runs, attempts int
	if err := db.QueryRow(`SELECT COUNT(*), MAX(attempt_count) FROM saas_fulfillment_runs WHERE account_id=?`, acct.ID).Scan(&runs, &attempts); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || attempts != 2 {
		t.Fatalf("runs=%d attempts=%d, want 1 run and 2 attempts", runs, attempts)
	}
}

func TestProvisioning_RetryIdempotentlyRefreshesEntitlementGrant(t *testing.T) {
	pf := &platformStub{failGrantFeature: "feature:b", failGrantOnce: true}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "pro", "name": "Pro"}); err != nil {
		t.Fatal(err)
	}
	for _, feature := range []string{"feature:a", "feature:b"} {
		if _, err := app.toolPlanFeatureAdd(ctx, map[string]any{"plan_key": "pro", "feature_key": feature}); err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]any{"owner_email": "owner@example.com", "slug": "grants", "plan_key": "pro"}
	if _, err := app.toolAccountCreate(ctx, args); err == nil {
		t.Fatal("first grant attempt should fail")
	}
	if _, err := app.toolAccountCreate(ctx, args); err != nil {
		t.Fatal(err)
	}
	if got := countCallsWithFeature(pf.calls, "entitlements", "entitlement_grants_upsert", "feature:a"); got != 2 {
		t.Fatalf("feature:a upsert calls=%d, want initial call plus retry", got)
	}
	if got := countCallsWithFeature(pf.calls, "entitlements", "entitlement_grants_upsert", "feature:b"); got != 2 {
		t.Fatalf("feature:b upsert calls=%d, want failed call plus retry", got)
	}
}

func TestUsageSync_SourceFailureDoesNotBlockOtherSources(t *testing.T) {
	pf := &platformStub{contacts: 9}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "crm", "name": "CRM"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanUsageSourceAdd(ctx, map[string]any{"plan_key": "crm", "app_name": "crm", "tool_name": "crm_saas_usage_snapshot"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanUsageSourceAdd(ctx, map[string]any{"plan_key": "crm", "app_name": "crm", "tool_name": "contacts_search", "feature_key": "crm:search", "read_path": "total"}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "usage-errors", "plan_key": "crm"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	pf.failures = map[string]int{"crm:crm_saas_usage_snapshot": 1}
	out, err := app.toolUsageSync(ctx, map[string]any{"account_id": acct.ID})
	if err != nil {
		t.Fatal(err)
	}
	body := out.(map[string]any)
	if body["failed_sources"] != 1 || body["records"] != 1 {
		t.Fatalf("unexpected sync summary: %+v", body)
	}
	if got := countCalls(pf.calls, "crm", "contacts_search"); got < 2 {
		t.Fatalf("contacts_search should run despite the other source failure; calls=%d", got)
	}
	var failures int
	if err := db.QueryRow(`SELECT failure_count FROM saas_usage_source_state st JOIN saas_usage_sources s ON s.id=st.usage_source_id WHERE st.account_id=? AND s.tool_name='crm_saas_usage_snapshot'`, acct.ID).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("failure_count=%d, want 1", failures)
	}
}

func TestUsageSync_CompleteGenerationRemovesMissingGauge(t *testing.T) {
	pf := &platformStub{contacts: 4}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "crm", "name": "CRM"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanUsageSourceAdd(ctx, map[string]any{"plan_key": "crm", "app_name": "crm", "tool_name": "crm_saas_usage_snapshot"}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "generation", "plan_key": "crm"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	pf.emptyUsage = true
	if _, err := app.toolUsageSync(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolUsageGet(ctx, map[string]any{"account_id": acct.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(out.(map[string]any)["usage"].([]UsageTotal)); got != 0 {
		t.Fatalf("usage rows=%d, want stale gauge removed", got)
	}
}

func TestFulfillment_AlwaysPolicyRunsEveryInvocation(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "repeatable", "name": "Repeatable"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{"plan_key": "repeatable", "event": "account_active", "app_name": "containers", "tool_name": "containers_start", "execution_policy": "always", "args": map[string]any{"workload_id": "wrk_fixed"}}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "repeatable", "plan_key": "repeatable"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	for i := 0; i < 2; i++ {
		if _, err := app.toolFulfillmentRun(ctx, map[string]any{"account_id": acct.ID, "event": "account_active"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := countCalls(pf.calls, "containers", "containers_start"); got != 3 {
		t.Fatalf("containers_start calls=%d, want initial plus two explicit runs", got)
	}
}

func TestAccessCheck_ReportsStaleUsageAsUnknown(t *testing.T) {
	pf := &platformStub{contacts: 1, entitled: true}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "crm", "name": "CRM"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanFeatureAdd(ctx, map[string]any{"plan_key": "crm", "feature_key": "crm:contacts"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanUsageSourceAdd(ctx, map[string]any{"plan_key": "crm", "app_name": "crm", "tool_name": "crm_saas_usage_snapshot", "metadata": map[string]any{"freshness_seconds": 60}}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "stale", "plan_key": "crm"})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	if _, err := db.Exec(`UPDATE saas_usage_source_state SET last_success_at='2020-01-01T00:00:00Z' WHERE account_id=?`, acct.ID); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolAccessCheck(ctx, map[string]any{"account_id": acct.ID, "feature_key": "crm:contacts"})
	if err != nil {
		t.Fatal(err)
	}
	access := out.(map[string]any)
	if access["allowed"].(bool) || !access["usage_unknown"].(bool) {
		t.Fatalf("stale usage should be explicitly unknown and denied: %+v", access)
	}
}

func TestCancelledAccountRequiresExplicitReactivation(t *testing.T) {
	ctx, db := newTestCtx(t, &platformStub{})
	defer db.Close()
	app := &App{}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "owner@example.com", "slug": "cancelled", "plan_key": "free", "subscription_id": 909})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"account_id": acct.ID, "subscription_id": 909, "subscription_status": "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolAccountResume(ctx, map[string]any{"account_id": acct.ID}); err == nil {
		t.Fatal("ordinary resume should reject a cancelled account")
	}
	out, err := app.toolSubscriptionSync(ctx, map[string]any{"account_id": acct.ID, "subscription_id": 909, "subscription_status": "active", "allow_cancelled_reactivation": true})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["account"].(*Account).Status != StatusActive {
		t.Fatalf("explicit reactivation failed: %+v", out)
	}
}

func TestSubscriptionCycleDue_RetryReusesInvoiceAndDuplicateIsIgnored(t *testing.T) {
	pf := &platformStub{failures: map[string]int{"subscriptions:subscription_cycles_update": 1}}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	created, err := app.toolAccountCreate(ctx, map[string]any{
		"owner_email": "trial@example.com", "customer_name": "Trial Co", "slug": "trial-co",
		"plan_key": "free", "subscription_id": 601,
	})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	event := sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 601, "cycle_id": 801, "currency": "USD",
		"period_start": "2026-07-12T00:00:00Z", "period_end": "2026-08-12T00:00:00Z",
	}}

	if err := app.handleSubscriptionCycleDue(ctx, event); err == nil || !strings.Contains(err.Error(), "link invoice") {
		t.Fatalf("first attempt should fail after invoice creation, got %v", err)
	}
	if err := app.handleSubscriptionCycleDue(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := app.handleSubscriptionCycleDue(ctx, event); err != nil {
		t.Fatalf("duplicate completed event should be ignored: %v", err)
	}

	if got := countCalls(pf.calls, "subscriptions", "subscriptions_invoice_prepare"); got != 1 {
		t.Fatalf("prepare calls=%d, want 1", got)
	}
	if got := countCalls(pf.calls, "billing", "invoices_create_from_prepared_lines"); got != 1 {
		t.Fatalf("invoice create calls=%d, want 1", got)
	}
	if got := countCalls(pf.calls, "billing", "invoices_send_payment_link"); got != 1 {
		t.Fatalf("payment link calls=%d, want 1", got)
	}
	invoiceCall := findCall(pf.calls, "billing", "invoices_create_from_prepared_lines")
	if invoiceCall == nil || int64FromAny(invoiceCall.Input["customer_id"]) != 501 || invoiceCall.Input["finalize"] != true {
		t.Fatalf("bad prepared invoice call: %+v", invoiceCall)
	}
	meta := mapFromAny(invoiceCall.Input["metadata"])
	if meta["saas_account_id"] != acct.ID || int64FromAny(meta["subscription_id"]) != 601 || int64FromAny(meta["cycle_id"]) != 801 {
		t.Fatalf("invoice correlation metadata missing: %+v", meta)
	}
	customerCall := findCall(pf.calls, "billing", "customers_upsert_by_email")
	defaults := mapFromAny(customerCall.Input["defaults"])
	if defaults["name"] != "Trial Co" || int64FromAny(mapFromAny(defaults["metadata"])["saas_customer_id"]) != acct.CustomerID {
		t.Fatalf("billing customer defaults are wrong: %+v", customerCall.Input)
	}
	op, err := dbCommerceOperationByInvoice(db, "proj-test", 702)
	if err != nil {
		t.Fatal(err)
	}
	if op == nil || op.Status != "awaiting_payment" || op.Stage != "payment_link_created" || op.AttemptCount != 2 {
		t.Fatalf("unexpected commerce operation: %+v", op)
	}
}

func TestAutomaticCollectionCheckoutSavesMethodAndRenewalCollects(t *testing.T) {
	t.Run("initial checkout", func(t *testing.T) {
		pf := &platformStub{}
		ctx, db := newTestCtx(t, pf)
		defer db.Close()
		app := &App{}
		setupPaidCRMPlan(t, app, ctx)
		if _, err := app.toolPlanUpsert(ctx, map[string]any{
			"key": "crm-pro", "name": "CRM Pro", "billing_mode": "paid",
			"catalog_product_id": 7001, "catalog_price_id": 8001,
			"subscription_required": true,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := app.toolCheckoutCreate(ctx, map[string]any{
			"owner_email": "auto@example.com", "slug": "auto", "plan_key": "crm-pro",
		}); err != nil {
			t.Fatal(err)
		}
		subCall := findCall(pf.calls, "subscriptions", "subscriptions_create")
		if subCall == nil || strArg(mapFromAny(subCall.Input["metadata"]), "collection_method") != "charge_automatically" {
			t.Fatalf("subscription collection policy missing: %+v", subCall)
		}
		linkCall := findCall(pf.calls, "billing", "invoices_send_payment_link")
		if linkCall == nil ||
			linkCall.Input["save_payment_method"] != true ||
			linkCall.Input["set_default_payment_method"] != true {
			t.Fatalf("initial checkout must collect recurring consent: %+v", linkCall)
		}
		if hasCall(pf.calls, "billing:invoices_collect") {
			t.Fatal("first checkout cannot collect before the customer enters a payment method")
		}
	})

	t.Run("renewal", func(t *testing.T) {
		pf := &platformStub{}
		ctx, db := newTestCtx(t, pf)
		defer db.Close()
		app := &App{}
		setupPaidCRMPlan(t, app, ctx)
		acct, _, err := app.createAccount(ctx, map[string]any{
			"owner_email": "renewal@example.com", "slug": "renewal",
			"plan_key": "crm-pro", "subscription_id": 77,
		})
		if err != nil {
			t.Fatal(err)
		}
		event := sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
			"subscription_id": 77, "cycle_id": 88, "currency": "USD",
			"period_start": "2026-08-01T00:00:00Z", "period_end": "2026-09-01T00:00:00Z",
			"metadata": map[string]any{"collection_method": "charge_automatically"},
		}}
		if err := app.handleSubscriptionCycleDue(ctx, event); err != nil {
			t.Fatal(err)
		}
		collectCall := findCall(pf.calls, "billing", "invoices_collect")
		if collectCall == nil ||
			collectCall.Input["idempotency_key"] != "subscription:77:cycle:88:collect" {
			t.Fatalf("automatic collection call=%+v", collectCall)
		}
		if hasCall(pf.calls, "billing:invoices_send_payment_link") {
			t.Fatal("renewal with automatic collection must not create a payment link")
		}
		op, err := dbCommerceOperationByInvoice(db, "proj-test", 702)
		if err != nil || op == nil || op.AccountID != acct.ID {
			t.Fatalf("commerce operation=%+v err=%v", op, err)
		}
	})
}

func TestInvoicePaid_MarksCycleAndSubscriptionActiveOnce(t *testing.T) {
	pf := paidInvoicePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanActionAdd(ctx, map[string]any{
		"plan_key": "free", "event": "account_active", "app_name": "containers", "tool_name": "containers_create",
		"args": map[string]any{"name": "saas-{{account.slug}}", "image": "example/crm:latest"},
	}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{
		"owner_email": "paid@example.com", "slug": "paid-co", "plan_key": "free", "subscription_id": 602,
	})
	if err != nil {
		t.Fatal(err)
	}
	acct := created.(map[string]any)["account"].(*Account)
	cycleEvent := sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 602, "cycle_id": 802, "currency": "USD",
		"period_start": "2026-07-12T00:00:00Z", "period_end": "2026-08-12T00:00:00Z",
	}}
	if err := app.handleSubscriptionCycleDue(ctx, cycleEvent); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"account_id": acct.ID, "subscription_id": 602, "subscription_status": "past_due"}); err != nil {
		t.Fatal(err)
	}
	paidEvent := sdk.Event{Event: "invoice.paid", ProjectID: "proj-test", Data: map[string]any{"id": 702, "status": "paid"}}
	if err := app.handleInvoicePaid(ctx, paidEvent); err != nil {
		t.Fatal(err)
	}
	if err := app.handleInvoicePaid(ctx, paidEvent); err != nil {
		t.Fatalf("duplicate paid event should be ignored: %v", err)
	}
	if got := countCalls(pf.calls, "subscriptions", "subscriptions_update_status"); got != 1 {
		t.Fatalf("subscription activation calls=%d, want 1", got)
	}
	if got := countCalls(pf.calls, "subscriptions", "subscription_cycles_update"); got != 2 {
		t.Fatalf("cycle update calls=%d, want pending+paid", got)
	}
	statusCall := findCall(pf.calls, "subscriptions", "subscriptions_update_status")
	if statusCall.Input["status"] != "active" || statusCall.Input["current_period_start"] != "2026-07-12T00:00:00Z" || statusCall.Input["next_renewal_at"] != "2026-08-12T00:00:00Z" {
		t.Fatalf("bad subscription activation input: %+v", statusCall.Input)
	}
	op, err := dbCommerceOperationByInvoice(db, "proj-test", 702)
	if err != nil {
		t.Fatal(err)
	}
	if op == nil || op.Status != "paid" || op.Stage != "subscription_activated" {
		t.Fatalf("payment operation not completed: %+v", op)
	}

	if err := app.handleSubscriptionLifecycle(ctx, sdk.Event{Event: "subscription.active", ProjectID: "proj-test", Data: map[string]any{"subscription_id": 602, "status": "active"}}); err != nil {
		t.Fatal(err)
	}
	gotAcct, err := dbAccountGet(db, "proj-test", acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAcct.Status != StatusActive {
		t.Fatalf("account status=%s, want active", gotAcct.Status)
	}
	if got := countCalls(pf.calls, "containers", "containers_create"); got != 2 {
		t.Fatalf("account_active fulfillment calls=%d, want initial plus paid activation", got)
	}
}

func TestInvoicePaid_PartialPaymentProjectsButDoesNotActivate(t *testing.T) {
	pf := &platformStub{
		invoiceGetStatus: "open", invoiceGetAmountPaid: 1000, invoiceGetTotal: 2900, invoiceGetCurrency: "USD",
		invoiceGetPayments: []map[string]any{{
			"id": 911, "amount_cents": 1000, "currency": "USD", "method": "wire",
			"received_at": "2026-07-02T10:00:00Z", "created_at": "2026-07-02T10:00:01Z",
		}},
	}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	created, err := app.toolAccountCreate(ctx, map[string]any{
		"owner_email": "partial@example.com", "slug": "partial-co", "plan_key": "free", "subscription_id": 612,
	})
	if err != nil {
		t.Fatal(err)
	}
	account := created.(map[string]any)["account"].(*Account)
	if err := app.handleSubscriptionCycleDue(ctx, sdk.Event{Event: "subscription.cycle_due", ProjectID: "proj-test", Data: map[string]any{
		"subscription_id": 612, "cycle_id": 812, "currency": "USD",
		"period_start": "2026-07-01T00:00:00Z", "period_end": "2026-08-01T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolSubscriptionSync(ctx, map[string]any{"account_id": account.ID, "subscription_id": 612, "subscription_status": "past_due"}); err != nil {
		t.Fatal(err)
	}
	activationCalls := countCalls(pf.calls, "subscriptions", "subscriptions_update_status")
	if err := app.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: "proj-test", Data: map[string]any{"id": 702, "status": "open"}}); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(pf.calls, "subscriptions", "subscriptions_update_status"); got != activationCalls {
		t.Fatalf("partial payment activated subscription: calls before=%d after=%d", activationCalls, got)
	}
	account, err = dbAccountGet(db, "proj-test", account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Status != StatusPastDue {
		t.Fatalf("partial payment account status=%s, want past_due", account.Status)
	}
	op, err := dbCommerceOperationByInvoice(db, "proj-test", 702)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != "awaiting_payment" {
		t.Fatalf("partial payment operation status=%s, want awaiting_payment", op.Status)
	}
	var amount int64
	if err := db.QueryRow(`SELECT amount_cents FROM saas_billing_payments WHERE project_id='proj-test' AND payment_id=911`).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if amount != 1000 {
		t.Fatalf("projected payment amount=%d, want 1000", amount)
	}
}

func TestAccountList_ReturnsCustomerBillingAndDateFilters(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	oldOut, err := app.toolAccountCreate(ctx, map[string]any{
		"owner_email": "old@example.com", "customer_name": "Old Co", "billing_customer_id": 701,
		"slug": "old-co", "plan_key": "free",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldAccount := oldOut.(map[string]any)["account"].(*Account)
	newOut, err := app.toolAccountCreate(ctx, map[string]any{
		"owner_email": "new@example.com", "customer_name": "New Co", "billing_customer_id": 702,
		"slug": "new-co", "plan_key": "free",
	})
	if err != nil {
		t.Fatal(err)
	}
	newAccount := newOut.(map[string]any)["account"].(*Account)
	unknownOut, err := app.toolAccountCreate(ctx, map[string]any{
		"owner_email": "unknown@example.com", "customer_name": "Unknown Co", "billing_customer_id": 703,
		"slug": "unknown-co", "plan_key": "free", "subscription_id": 53,
	})
	if err != nil {
		t.Fatal(err)
	}
	unknownAccount := unknownOut.(map[string]any)["account"].(*Account)
	if _, err := db.Exec(`INSERT INTO saas_commerce_operations
		(project_id, operation_key, account_id, subscription_id, cycle_id, billing_customer_id, invoice_id, status)
		VALUES ('proj-test','subscription:53:cycle:63',?,53,63,703,1703,'awaiting_payment')`, unknownAccount.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE saas_accounts SET created_at='2026-03-01T09:00:00Z' WHERE id=?`, oldAccount.ID); err != nil {
		t.Fatal(err)
	}
	if err := dbBillingProjectionReplace(db, "proj-test", &billingInvoiceProjection{
		InvoiceID: 1701, AccountID: oldAccount.ID, BillingCustomerID: 701, SubscriptionID: 51, CycleID: 61,
		Status: "paid", Currency: "EUR", TotalCents: 2900, AmountPaidCents: 2900,
		PaidAt: "2026-05-02T10:00:00Z", SourceCreatedAt: "2026-05-01T10:00:00Z", SourceUpdatedAt: "2026-05-02T10:00:00Z",
		Payments: []billingPaymentProjection{{PaymentID: 1801, AmountCents: 2900, Currency: "EUR", Method: "stripe", ReceivedAt: "2026-05-02T10:00:00Z"}},
	}); err != nil {
		t.Fatal(err)
	}

	callsBeforeList := len(pf.calls)
	out, err := app.toolAccountList(ctx, map[string]any{
		"created_before": "2026-05-01T00:00:00Z", "has_paid": true,
		"paid_since": "2026-05-01T00:00:00Z", "paid_until": "2026-06-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := out.(map[string]any)
	accounts := body["accounts"].([]*Account)
	if len(accounts) != 1 || body["total"] != 1 || accounts[0].ID != oldAccount.ID {
		t.Fatalf("filtered accounts are wrong: %+v", body)
	}
	account := accounts[0]
	if account.CreatedAt != "2026-03-01T09:00:00Z" || account.Customer == nil || account.Customer.Email != "old@example.com" || account.Customer.Name != "Old Co" {
		t.Fatalf("account customer/creation data missing: %+v", account)
	}
	if account.Billing == nil || !account.Billing.HasPaid || account.Billing.FirstPaidAt != "2026-05-02T10:00:00Z" || account.Billing.LastPaidAt != "2026-05-02T10:00:00Z" {
		t.Fatalf("billing summary is wrong: %+v", account.Billing)
	}
	if account.Billing.NetPaidByCurrency["EUR"] != 2900 || !account.Billing.DataComplete {
		t.Fatalf("billing totals/completeness are wrong: %+v", account.Billing)
	}
	if body["billing_sync_pending"] != int64(1) {
		t.Fatalf("billing_sync_pending=%v, want 1", body["billing_sync_pending"])
	}

	unpaidOut, err := app.toolAccountList(ctx, map[string]any{"has_paid": false, "limit": 10})
	if err != nil {
		t.Fatal(err)
	}
	unpaid := unpaidOut.(map[string]any)["accounts"].([]*Account)
	if len(unpaid) != 1 || unpaid[0].ID != newAccount.ID || unpaid[0].Billing.HasPaid {
		t.Fatalf("unpaid customer filter is wrong: %+v", unpaidOut)
	}
	emailOut, err := app.toolAccountList(ctx, map[string]any{"customer_email": "new@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	byEmail := emailOut.(map[string]any)["accounts"].([]*Account)
	if len(byEmail) != 1 || byEmail[0].ID != newAccount.ID {
		t.Fatalf("customer_email filter is wrong: %+v", emailOut)
	}
	if len(pf.calls) != callsBeforeList {
		t.Fatalf("account listing made remote app calls: before=%d after=%d", callsBeforeList, len(pf.calls))
	}
	if _, err := app.toolAccountList(ctx, map[string]any{"created_before": "two months ago"}); err == nil {
		t.Fatal("invalid date filter should fail")
	}
}

func TestBillingSync_BackfillsLinkedInvoice(t *testing.T) {
	pf := paidInvoicePlatformStub()
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "backfill@example.com", "slug": "backfill", "plan_key": "free", "subscription_id": 620})
	if err != nil {
		t.Fatal(err)
	}
	account := created.(map[string]any)["account"].(*Account)
	if _, err := db.Exec(`INSERT INTO saas_commerce_operations
		(project_id, operation_key, account_id, subscription_id, cycle_id, billing_customer_id, invoice_id, status)
		VALUES ('proj-test','subscription:620:cycle:820',?,620,820,501,702,'paid')`, account.ID); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolBillingSync(ctx, map[string]any{"account_id": account.ID})
	if err != nil {
		t.Fatal(err)
	}
	body := out.(map[string]any)
	if body["synced"] != 1 || body["failed"] != 0 || body["pending"] != int64(0) {
		t.Fatalf("unexpected billing sync result: %+v", body)
	}
	getOut, err := app.toolAccountGet(ctx, map[string]any{"account_id": account.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := getOut.(map[string]any)["account"].(*Account)
	if got.Customer == nil || int64PtrValue(got.Customer.BillingCustomerID) != 501 || got.Billing == nil || !got.Billing.HasPaid || !got.Billing.DataComplete {
		t.Fatalf("backfilled account is incomplete: %+v", got)
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

func paidInvoicePlatformStub() *platformStub {
	return &platformStub{
		invoiceGetStatus: "paid", invoiceGetPaidAt: "2026-06-01T10:00:00Z",
		invoiceGetAmountPaid: 2900, invoiceGetTotal: 2900, invoiceGetCurrency: "USD",
		invoiceGetPayments: []map[string]any{{
			"id": 901, "amount_cents": 2900, "currency": "USD", "method": "wire",
			"received_at": "2026-06-01T10:00:00Z", "created_at": "2026-06-01T10:00:01Z",
		}},
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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

func countCalls(calls []callAppCall, app, tool string) int {
	count := 0
	for _, call := range calls {
		if call.App == app && call.Tool == tool {
			count++
		}
	}
	return count
}

func countCallsWithFeature(calls []callAppCall, app, tool, feature string) int {
	count := 0
	for _, call := range calls {
		if call.App == app && call.Tool == tool && call.Input["feature_key"] == feature {
			count++
		}
	}
	return count
}
