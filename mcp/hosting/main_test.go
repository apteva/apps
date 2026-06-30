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
	case "catalog_prices_get":
		trialDays := int64(0)
		if int64Arg(input, "id") == 100 {
			trialDays = 14
		}
		b, _ := json.Marshal(map[string]any{
			"price": map[string]any{
				"id":                input["id"],
				"product_id":        501,
				"nickname":          "Apteva Starter",
				"unit_amount_cents": 1900,
				"currency":          "USD",
				"interval":          "month",
				"interval_count":    1,
				"trial_days":        trialDays,
				"metadata": map[string]any{
					"plan_key":                "starter",
					"on_trial_end_unpaid":     "suspend",
					"unpaid_grace_days":       7,
					"on_unpaid_grace_expired": "delete",
				},
			},
		})
		return json.Unmarshal(b, out)
	case "catalog_products_get":
		b, _ := json.Marshal(map[string]any{
			"product": map[string]any{
				"id":   input["id"],
				"name": "Apteva Hosting",
				"slug": "apteva-hosting",
				"metadata": map[string]any{
					"product_key":  "apteva",
					"product_type": "apteva_hosting",
					"runtime_config": map[string]any{
						"image":       "ghcr.io/apteva/apteva:latest",
						"port":        5280,
						"health_path": "/health",
					},
				},
			},
		})
		return json.Unmarshal(b, out)
	case "customers_upsert_by_email":
		b, _ := json.Marshal(map[string]any{
			"customer": map[string]any{
				"id":    55,
				"email": input["email"],
				"name":  "Checkout Buyer",
			},
			"was_created": true,
		})
		return json.Unmarshal(b, out)
	case "subscriptions_create":
		b, _ := json.Marshal(map[string]any{
			"subscription": map[string]any{
				"id":             66,
				"status":         input["status"],
				"customer_id":    input["customer_id"],
				"customer_email": input["customer_email"],
				"metadata":       input["metadata"],
			},
		})
		return json.Unmarshal(b, out)
	case "subscriptions_update_status":
		b, _ := json.Marshal(map[string]any{
			"subscription": map[string]any{
				"id":     input["id"],
				"status": input["status"],
			},
		})
		return json.Unmarshal(b, out)
	case "invoices_create":
		b, _ := json.Marshal(map[string]any{
			"invoice": map[string]any{
				"id":          77,
				"status":      "draft",
				"customer_id": input["customer_id"],
				"currency":    input["currency"],
				"metadata":    input["metadata"],
				"line_items":  input["line_items"],
			},
		})
		return json.Unmarshal(b, out)
	case "invoices_finalize":
		b, _ := json.Marshal(map[string]any{
			"invoice": map[string]any{
				"id":     input["invoice_id"],
				"status": "open",
			},
		})
		return json.Unmarshal(b, out)
	case "invoices_send_payment_link":
		b, _ := json.Marshal(map[string]any{
			"url":               "https://checkout.stripe.test/session",
			"stripe_session_id": "cs_test_123",
		})
		return json.Unmarshal(b, out)
	case "invoices_get":
		b, _ := json.Marshal(map[string]any{
			"found": true,
			"invoice": map[string]any{
				"id":             input["id"],
				"customer_id":    42,
				"customer_email": "buyer@example.com",
				"customer_name":  "WP Buyer",
				"status":         "paid",
				"currency":       "USD",
				"metadata": map[string]any{
					"product_type":     "wordpress_hosting",
					"product_key":      "wordpress-single",
					"plan_key":         "wordpress-starter",
					"owner_email":      "owner@example.com",
					"slug":             "paid-wp-sale",
					"subscription_id":  int64(66),
					"fulfillment_app":  "hosting",
					"fulfillment_type": "hosting_tenant_provision",
				},
			},
		})
		return json.Unmarshal(b, out)
	case "orders_create_from_invoice":
		b, _ := json.Marshal(map[string]any{
			"order": map[string]any{
				"id":             10,
				"source":         "billing",
				"source_ref":     "invoice:77",
				"order_type":     "hosting",
				"payment_status": "paid",
			},
			"fulfillment": map[string]any{
				"id":               20,
				"order_id":         10,
				"fulfillment_app":  "hosting",
				"fulfillment_type": "hosting_tenant_provision",
				"status":           "queued",
			},
		})
		return json.Unmarshal(b, out)
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
	if m.Version != "1.6.0" {
		t.Errorf("manifest.Version=%q, want 1.6.0", m.Version)
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
	gotEvents := map[string][]string{}
	for _, dep := range m.Requires.Apps {
		gotRequired[dep.Name] = !dep.Optional
		gotEvents[dep.Name] = dep.Events
	}
	if !gotRequired["containers"] {
		t.Error("manifest should require containers")
	}
	if !gotRequired["catalog"] {
		t.Error("manifest should require catalog")
	}
	if !containsString(gotEvents["billing"], "invoice.paid") {
		t.Errorf("manifest should subscribe to billing invoice.paid, got %v", gotEvents["billing"])
	}
	if !containsString(gotEvents["subscriptions"], "subscription.cancelled") {
		t.Errorf("manifest should subscribe to subscription lifecycle events, got %v", gotEvents["subscriptions"])
	}
}

func TestCheckoutCreatePaidPlanCreatesSubscriptionInvoiceAndPaymentLink(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	got, err := app.toolCheckoutCreate(ctx, map[string]any{
		"catalog_price_id": int64(99),
		"owner_email":      "buyer@example.com",
		"customer_name":    "Checkout Buyer",
		"slug":             "checkout-apteva",
		"success_url":      "https://example.com/success",
		"cancel_url":       "https://example.com/cancel",
	})
	if err != nil {
		t.Fatalf("checkout create: %v", err)
	}
	result := got.(map[string]any)
	checkout := result["checkout"].(map[string]any)
	if checkout["requires_payment"] != true || checkout["url"] != "https://checkout.stripe.test/session" {
		t.Fatalf("unexpected checkout: %+v", checkout)
	}
	if result["tenant"] != nil {
		t.Fatalf("paid checkout should wait for invoice.paid, got tenant %+v", result["tenant"])
	}

	wantCalls := []string{
		"catalog:catalog_prices_get",
		"catalog:catalog_products_get",
		"billing:customers_upsert_by_email",
		"subscriptions:subscriptions_create",
		"billing:invoices_create",
		"billing:invoices_finalize",
		"billing:invoices_send_payment_link",
	}
	for _, want := range wantCalls {
		parts := strings.Split(want, ":")
		if !containsToolCall(pf.callAppCalls, parts[0], parts[1]) {
			t.Fatalf("missing call %s in %+v", want, pf.callAppCalls)
		}
	}

	var invoiceCall *callAppCall
	var subCall *callAppCall
	for i := range pf.callAppCalls {
		if pf.callAppCalls[i].Tool == "invoices_create" {
			invoiceCall = &pf.callAppCalls[i]
		}
		if pf.callAppCalls[i].Tool == "subscriptions_create" {
			subCall = &pf.callAppCalls[i]
		}
	}
	if invoiceCall == nil || int64Arg(mapArg(invoiceCall.Input, "metadata"), "subscription_id") != 66 {
		t.Fatalf("invoice metadata missing subscription_id: %+v", invoiceCall)
	}
	if subCall == nil || subCall.Input["status"] != "past_due" {
		t.Fatalf("subscription status should start past_due before payment: %+v", subCall)
	}
	line := invoiceCall.Input["line_items"].([]any)[0].(map[string]any)
	if line["price_id"] != int64(99) {
		t.Fatalf("invoice line should reference catalog price: %+v", line)
	}
}

func TestCheckoutCreateNoCardTrialProvisionsImmediately(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	got, err := app.toolCheckoutCreate(ctx, map[string]any{
		"catalog_price_id": int64(100),
		"owner_email":      "trial@example.com",
		"customer_name":    "Trial Buyer",
		"slug":             "trial-apteva",
	})
	if err != nil {
		t.Fatalf("checkout create: %v", err)
	}
	result := got.(map[string]any)
	checkout := result["checkout"].(map[string]any)
	if checkout["status"] != "trialing" || checkout["requires_payment"] != false || checkout["trial_end"] == "" {
		t.Fatalf("unexpected checkout: %+v", checkout)
	}
	tenant, ok := result["tenant"].(*Tenant)
	if !ok || tenant == nil {
		t.Fatalf("trial checkout should provision tenant immediately: %+v", result["tenant"])
	}
	if containsToolCall(pf.callAppCalls, "billing", "invoices_create") || containsToolCall(pf.callAppCalls, "billing", "invoices_send_payment_link") {
		t.Fatalf("no-card trial should not create invoice/payment link: %+v", pf.callAppCalls)
	}

	var subCall *callAppCall
	for i := range pf.callAppCalls {
		if pf.callAppCalls[i].Tool == "subscriptions_create" {
			subCall = &pf.callAppCalls[i]
			break
		}
	}
	if subCall == nil {
		t.Fatalf("missing subscriptions_create call")
	}
	if subCall.Input["status"] != "trialing" || subCall.Input["trial_start"] == "" || subCall.Input["trial_end"] == "" {
		t.Fatalf("trial subscription missing status/dates: %+v", subCall.Input)
	}
	meta := mapArg(subCall.Input, "metadata")
	if int64Arg(meta, "trial_days") != 14 || meta["on_unpaid_grace_expired"] != "delete" {
		t.Fatalf("trial metadata not carried through: %+v", meta)
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

func TestPaidInvoiceProvisionDrivesOrdersAndTenantProvisioning(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	got, err := app.toolPaidInvoiceProvision(ctx, map[string]any{"invoice_id": int64(77)})
	if err != nil {
		t.Fatalf("paid invoice provision: %v", err)
	}
	tenant := got.(map[string]any)["tenant"].(*Tenant)
	if tenant.Slug != "paid-wp-sale" {
		t.Fatalf("tenant slug=%q, want paid-wp-sale", tenant.Slug)
	}
	if tenant.PlanKey != "wordpress-starter" || tenant.Image != "wordpress:php8.3-apache" {
		t.Fatalf("unexpected tenant plan/image: %+v", tenant)
	}
	if tenant.SubscriptionID == nil || *tenant.SubscriptionID != 66 {
		t.Fatalf("tenant subscription_id=%v, want 66", tenant.SubscriptionID)
	}
	wantCalls := []string{"billing:invoices_get", "orders:orders_create_from_invoice", "containers:containers_run", "orders:fulfillments_update", "subscriptions:subscriptions_update_status"}
	var gotCalls []string
	for _, c := range pf.callAppCalls {
		gotCalls = append(gotCalls, c.App+":"+c.Tool)
	}
	for _, want := range wantCalls {
		found := false
		for _, got := range gotCalls {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing call %s in %+v", want, gotCalls)
		}
	}
	var orderCall *callAppCall
	var containersCall *callAppCall
	for i := range pf.callAppCalls {
		if pf.callAppCalls[i].Tool == "orders_create_from_invoice" {
			orderCall = &pf.callAppCalls[i]
		}
		if pf.callAppCalls[i].Tool == "containers_run" {
			containersCall = &pf.callAppCalls[i]
		}
	}
	if orderCall == nil || orderCall.Input["fulfillment_app"] != "hosting" || orderCall.Input["fulfillment_type"] != "hosting_tenant_provision" {
		t.Fatalf("unexpected orders_create_from_invoice input: %+v", orderCall)
	}
	if containersCall == nil || containersCall.Input["health_path"] != "/wp-admin/setup-config.php" {
		t.Fatalf("unexpected containers_run health path: %+v", containersCall)
	}
	if !containsToolCall(pf.callAppCalls, "subscriptions", "subscriptions_update_status") {
		t.Fatalf("expected subscription active update, got %+v", pf.callAppCalls)
	}
}

func TestInvoicePaidEventUsesPaidInvoiceProvisioning(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	err := app.handleInvoicePaidEvent(ctx, sdk.Event{
		Event:     "invoice.paid",
		ProjectID: "proj-test",
		Data:      map[string]any{"id": int64(77)},
	})
	if err != nil {
		t.Fatalf("handle invoice.paid: %v", err)
	}
	tenant, err := dbTenantGetBySlug(db, "paid-wp-sale")
	if err != nil || tenant == nil {
		t.Fatalf("expected tenant from invoice event, tenant=%+v err=%v", tenant, err)
	}
	if tenant.PlanKey != "wordpress-starter" {
		t.Fatalf("tenant plan=%q, want wordpress-starter", tenant.PlanKey)
	}
}

func TestSubscriptionEventSyncsTenantLifecycle(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	created, err := app.toolTenantCreate(ctx, map[string]any{
		"owner_email":     "owner@example.com",
		"slug":            "subevent",
		"plan_key":        "free",
		"subscription_id": int64(123),
	})
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	tenant := created.(map[string]any)["tenant"].(*Tenant)

	err = app.handleSubscriptionEvent(ctx, sdk.Event{
		Event:     "subscription.cancelled",
		SourceApp: "subscriptions",
		Data:      map[string]any{"id": int64(123)},
	})
	if err != nil {
		t.Fatalf("handle subscription.cancelled: %v", err)
	}
	got, err := dbTenantGet(db, tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusSuspended {
		t.Fatalf("tenant status=%q, want suspended", got.Status)
	}
	if !containsToolCall(pf.callAppCalls, "containers", "containers_stop") {
		t.Fatalf("expected containers_stop call, got %+v", pf.callAppCalls)
	}
}

func TestSubscriptionPastDueSuspendsTenant(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	created, err := app.toolTenantCreate(ctx, map[string]any{
		"owner_email":     "owner@example.com",
		"slug":            "pastdue",
		"plan_key":        "free",
		"subscription_id": int64(124),
	})
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	tenant := created.(map[string]any)["tenant"].(*Tenant)
	pf.callAppCalls = nil

	err = app.handleSubscriptionEvent(ctx, sdk.Event{
		Event:     "subscription.past_due",
		SourceApp: "subscriptions",
		Data:      map[string]any{"id": int64(124), "status": "past_due"},
	})
	if err != nil {
		t.Fatalf("handle subscription.past_due: %v", err)
	}
	got, err := dbTenantGet(db, tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusSuspended {
		t.Fatalf("tenant status=%q, want suspended", got.Status)
	}
	if !containsToolCall(pf.callAppCalls, "containers", "containers_stop") {
		t.Fatalf("expected containers_stop call, got %+v", pf.callAppCalls)
	}
}

func TestSubscriptionEndedPolicyDeletesTenant(t *testing.T) {
	pf := &platformStub{}
	ctx, db := newTestCtx(t, pf)
	defer db.Close()

	app := &App{}
	created, err := app.toolTenantCreate(ctx, map[string]any{
		"owner_email":     "owner@example.com",
		"slug":            "ended-delete",
		"plan_key":        "free",
		"subscription_id": int64(125),
		"metadata": map[string]any{
			"on_unpaid_grace_expired":  "delete",
			"delete_volumes_on_expiry": true,
		},
	})
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	tenant := created.(map[string]any)["tenant"].(*Tenant)
	pf.callAppCalls = nil

	err = app.handleSubscriptionEvent(ctx, sdk.Event{
		Event:     "subscription.ended",
		SourceApp: "subscriptions",
		Data: map[string]any{
			"id":     int64(125),
			"status": "ended",
			"metadata": map[string]any{
				"on_unpaid_grace_expired":  "delete",
				"delete_volumes_on_expiry": true,
			},
		},
	})
	if err != nil {
		t.Fatalf("handle subscription.ended: %v", err)
	}
	got, err := dbTenantGet(db, tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusDeleted {
		t.Fatalf("tenant status=%q, want deleted", got.Status)
	}
	var destroy *callAppCall
	for i := range pf.callAppCalls {
		if pf.callAppCalls[i].Tool == "containers_destroy" {
			destroy = &pf.callAppCalls[i]
			break
		}
	}
	if destroy == nil || destroy.Input["delete_volumes"] != true {
		t.Fatalf("expected containers_destroy with delete_volumes=true, got %+v", pf.callAppCalls)
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

func containsToolCall(calls []callAppCall, app, tool string) bool {
	for _, c := range calls {
		if c.App == app && c.Tool == tool {
			return true
		}
	}
	return false
}
