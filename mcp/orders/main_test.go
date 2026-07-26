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
)

type platformStub struct {
	tk.BasePlatformClient
}

func (p *platformStub) CallAppResult(appName, tool string, input map[string]any, out any) error {
	if appName == "billing" && tool == "invoices_get" {
		b, _ := json.Marshal(map[string]any{
			"found": true,
			"invoice": map[string]any{
				"id":                input["id"],
				"customer_id":       42,
				"customer_email":    "wp@example.com",
				"customer_name":     "WP Buyer",
				"status":            "paid",
				"currency":          "USD",
				"subtotal_cents":    990,
				"tax_cents":         0,
				"total_cents":       990,
				"amount_paid_cents": 990,
				"metadata": map[string]any{
					"product_type":    "wordpress_hosting",
					"product_key":     "wordpress-single",
					"plan_key":        "wordpress-starter",
					"fulfillment_app": "hosting",
				},
				"line_items": []map[string]any{{
					"id":               7,
					"description":      "WordPress Hosting Starter",
					"quantity":         1,
					"unit_price_cents": 990,
					"amount_cents":     990,
				}},
			},
		})
		return json.Unmarshal(b, out)
	}
	return nil
}

var _ sdk.PlatformClient = (*platformStub)(nil)

func TestEnsureGenericFulfillmentSchemaIsIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "orders.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	baseSchema, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatalf("read base migration: %v", err)
	}
	if _, err := db.Exec(string(baseSchema)); err != nil {
		t.Fatalf("apply base migration: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := ensureGenericFulfillmentSchema(db); err != nil {
			t.Fatalf("ensure schema attempt %d: %v", attempt, err)
		}
	}

	for _, column := range genericFulfillmentColumns {
		exists, err := tableHasColumn(db, column.table, column.name)
		if err != nil {
			t.Fatalf("inspect %s.%s: %v", column.table, column.name, err)
		}
		if !exists {
			t.Errorf("missing column %s.%s", column.table, column.name)
		}
	}

	for _, index := range []string{
		"ix_orders_type",
		"ix_order_items_fulfillment",
		"ix_fulfillments_app",
		"ux_fulfillments_idempotency",
	} {
		var found string
		if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&found); err != nil {
			t.Errorf("missing index %s: %v", index, err)
		}
	}
}

func TestFulfillmentItemsMigrationReconcilesDuplicateProviderShipments(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "orders-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base, _ := os.ReadFile("migrations/001_init.sql")
	if _, err := db.Exec(string(base)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := db.Exec(`INSERT INTO shipments
			(project_id, order_id, provider, provider_shipment_id)
			VALUES ('upgrade', 1, 'printful', 'duplicate-shipment')`); err != nil {
			t.Fatal(err)
		}
	}
	migration, _ := os.ReadFile("migrations/003_fulfillment_items.sql")
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	var preserved, cleared int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shipments WHERE provider_shipment_id='duplicate-shipment'`).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM shipments WHERE provider_shipment_id IS NULL`).Scan(&cleared); err != nil {
		t.Fatal(err)
	}
	if preserved != 1 || cleared != 1 {
		t.Fatalf("duplicate reconciliation preserved=%d cleared=%d", preserved, cleared)
	}
}

func TestEnsureGenericFulfillmentSchemaAddsMissingColumns(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "orders-legacy.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	legacySchema := `
		CREATE TABLE orders (
			id INTEGER PRIMARY KEY,
			project_id TEXT NOT NULL,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE order_items (
			id INTEGER PRIMARY KEY
		);
		CREATE TABLE fulfillments (
			id INTEGER PRIMARY KEY,
			project_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'queued'
		);
	`
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := ensureGenericFulfillmentSchema(db); err != nil {
		t.Fatalf("ensure legacy schema: %v", err)
	}

	for _, column := range genericFulfillmentColumns {
		exists, err := tableHasColumn(db, column.table, column.name)
		if err != nil {
			t.Fatalf("inspect %s.%s: %v", column.table, column.name, err)
		}
		if !exists {
			t.Errorf("missing repaired column %s.%s", column.table, column.name)
		}
	}
}

func TestOrderLifecycle(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))

	order, err := dbOrderCreate(ctx, "proj-test", map[string]any{
		"source":         "manual",
		"source_ref":     "test-001",
		"customer_email": "buyer@example.com",
		"customer_name":  "Test Buyer",
		"currency":       "EUR",
		"payment_status": "paid",
		"items": []any{
			map[string]any{
				"title":             "Resin lamp",
				"sku":               "LAMP-001",
				"quantity":          2,
				"unit_amount_cents": 3499,
				"currency":          "EUR",
			},
		},
	}, "order.created")
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if order.OrderNumber == "" {
		t.Fatalf("expected order number")
	}
	if order.TotalCents != 6998 {
		t.Fatalf("total cents = %d, want 6998", order.TotalCents)
	}
	if len(order.Items) != 1 || order.Items[0].SKU != "LAMP-001" {
		t.Fatalf("unexpected items: %+v", order.Items)
	}
	if order.OrderStatus != "paid" || order.PaymentStatus != "paid" {
		t.Fatalf("unexpected statuses: order=%s payment=%s", order.OrderStatus, order.PaymentStatus)
	}

	updated, err := dbOrderUpdateStatus(ctx.AppDB(), "proj-test", order.ID, statusPatch{
		OrderStatus:       "ready_to_fulfill",
		FulfillmentStatus: "queued",
		Actor:             "test",
		Note:              "ready after review",
	})
	if err != nil {
		t.Fatalf("update status: %v", err)
	}
	if updated.OrderStatus != "ready_to_fulfill" || updated.FulfillmentStatus != "queued" {
		t.Fatalf("unexpected updated statuses: %+v", updated)
	}
	if len(updated.Events) < 2 {
		t.Fatalf("expected audit events, got %d", len(updated.Events))
	}

	cancelled, err := dbOrderCancel(ctx.AppDB(), "proj-test", order.ID, "customer request", "test")
	if err != nil {
		t.Fatalf("cancel order: %v", err)
	}
	if cancelled.OrderStatus != "cancelled" || cancelled.FulfillmentStatus != "cancelled" {
		t.Fatalf("unexpected cancelled statuses: %+v", cancelled)
	}
}

func TestSplitFulfillmentMembershipAndShipmentUpsert(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-split"))
	order, err := dbOrderCreate(ctx, "proj-split", map[string]any{
		"source": "commerce", "source_ref": "sale:split", "currency": "EUR", "payment_status": "paid",
		"items": []any{
			map[string]any{"title": "POD shirt", "sku": "POD-1", "quantity": 1, "unit_amount_cents": 2500},
			map[string]any{"title": "Dropship lamp", "sku": "DROP-1", "quantity": 2, "unit_amount_cents": 4000},
		},
	}, "order.created")
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := dbFulfillmentCreate(ctx, "proj-split", map[string]any{
		"order_id": order.ID, "provider": "printful", "idempotency_key": "sale:split:printful",
		"items": []any{map[string]any{"order_item_id": order.Items[0].ID, "quantity": 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := dbFulfillmentCreate(ctx, "proj-split", map[string]any{
		"order_id": order.ID, "provider": "bigbuy", "idempotency_key": "sale:split:bigbuy",
		"items": []any{map[string]any{"order_item_id": order.Items[1].ID, "quantity": 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || len(second.Items) != 1 {
		t.Fatalf("fulfillment membership missing: first=%+v second=%+v", first.Items, second.Items)
	}
	shipment, _, err := dbShipmentUpsert(ctx.AppDB(), "proj-split", map[string]any{
		"order_id": order.ID, "fulfillment_id": first.ID, "provider": "printful",
		"provider_shipment_id": "pf-ship-1", "carrier": "DHL", "tracking_number": "TRACK-1", "status": "shipped",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, partiallyFulfilled, err := dbShipmentUpsert(ctx.AppDB(), "proj-split", map[string]any{
		"order_id": order.ID, "fulfillment_id": first.ID, "provider": "printful",
		"provider_shipment_id": "pf-ship-1", "tracking_number": "TRACK-1", "status": "delivered",
	})
	if err != nil {
		t.Fatal(err)
	}
	if shipment.ID != updated.ID || updated.Status != "delivered" || updated.Carrier != "DHL" {
		t.Fatalf("shipment upsert was not idempotent: before=%+v after=%+v", shipment, updated)
	}
	if partiallyFulfilled.FulfillmentStatus != "partially_fulfilled" {
		t.Fatalf("split order status=%q, want partially_fulfilled", partiallyFulfilled.FulfillmentStatus)
	}
	_, fullyFulfilled, err := dbFulfillmentUpdate(ctx.AppDB(), "proj-split", map[string]any{
		"id": second.ID, "status": "delivered",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fullyFulfilled.FulfillmentStatus != "delivered" || fullyFulfilled.OrderStatus != "fulfilled" {
		t.Fatalf("completed split order has unexpected state: %+v", fullyFulfilled)
	}
}

func TestOrdersCreateToolAcceptsJSONStringItems(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}

	out, err := app.toolOrdersCreate(ctx, map[string]any{
		"_project_id":        "proj-test",
		"source":             "billing",
		"source_ref":         "NSL-ENT-2026-07",
		"invoice_id":         "3",
		"customer_email":     "ap@northstar.example",
		"customer_name":      "Northstar Labs",
		"currency":           "USD",
		"payment_status":     "paid",
		"order_status":       "ready_to_fulfill",
		"fulfillment_status": "queued",
		"shipping_address":   `{"line1":"123 Benchmark Ave","city":"New York","region":"NY","postal_code":"10001","country":"US"}`,
		"billing_address":    `{"line1":"123 Benchmark Ave","city":"New York","region":"NY","postal_code":"10001","country":"US"}`,
		"metadata":           `{"source_ref":"NSL-ENT-2026-07"}`,
		"items":              `[{"title":"Secure launch kit","sku":"NSL-KIT-001","quantity":12,"unit_amount_cents":25000,"currency":"USD"}]`,
	})
	if err != nil {
		t.Fatalf("create order with JSON-string items: %v", err)
	}
	order := out.(map[string]any)["order"].(*Order)
	if order.Source != "billing" || order.SourceRef != "NSL-ENT-2026-07" {
		t.Fatalf("unexpected source fields: %+v", order)
	}
	if order.PaymentStatus != "paid" || order.OrderStatus != "ready_to_fulfill" || order.FulfillmentStatus != "queued" {
		t.Fatalf("unexpected statuses: %+v", order)
	}
	if order.TotalCents != 300000 {
		t.Fatalf("total cents = %d, want 300000", order.TotalCents)
	}
	if len(order.Items) != 1 || order.Items[0].SKU != "NSL-KIT-001" || order.Items[0].Quantity != 12 {
		t.Fatalf("unexpected items: %+v", order.Items)
	}
}

func TestOrdersUpdateStatusRequiresExistingOrderID(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}

	_, err := app.toolOrdersUpdateStatus(ctx, map[string]any{
		"_project_id":        "proj-test",
		"order_status":       "ready_to_fulfill",
		"payment_status":     "paid",
		"fulfillment_status": "queued",
		"actor":              "benchmark-recovery",
	})
	if err == nil || !strings.Contains(err.Error(), "id required") {
		t.Fatalf("expected id required error, got %v", err)
	}
}

func TestDuplicateSourceRefRejected(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	body := map[string]any{
		"source":     "shopify",
		"source_ref": "gid://shopify/Order/1",
		"currency":   "USD",
		"items": []any{
			map[string]any{
				"title":             "Widget",
				"quantity":          1,
				"unit_amount_cents": 1000,
			},
		},
	}
	if _, err := dbOrderCreate(ctx, "proj-test", body, "order.imported"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := dbOrderCreate(ctx, "proj-test", body, "order.imported"); err == nil {
		t.Fatalf("expected duplicate source ref error")
	}
}

func TestCreateFromInvoiceQueuesGenericHostingFulfillmentIdempotently(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithPlatform(&platformStub{}))
	app := &App{}

	out, err := app.toolOrdersCreateFromInvoice(ctx, map[string]any{
		"_project_id": "proj-test",
		"invoice_id":  int64(55),
	})
	if err != nil {
		t.Fatalf("create from invoice: %v", err)
	}
	order := out.(map[string]any)["order"].(*Order)
	fulfillment := out.(map[string]any)["fulfillment"].(*Fulfillment)
	if order.OrderType != "hosting" {
		t.Fatalf("order_type=%q, want hosting", order.OrderType)
	}
	if order.Source != "billing" || order.SourceRef != "invoice:55" || order.InvoiceID == nil || *order.InvoiceID != 55 {
		t.Fatalf("unexpected order source/invoice: %+v", order)
	}
	if fulfillment.FulfillmentApp != "hosting" || fulfillment.FulfillmentType != "hosting_tenant_provision" {
		t.Fatalf("unexpected fulfillment: %+v", fulfillment)
	}
	if fulfillment.IdempotencyKey != "billing_invoice:55:hosting_tenant_provision" {
		t.Fatalf("idempotency_key=%q", fulfillment.IdempotencyKey)
	}

	out2, err := app.toolOrdersCreateFromInvoice(ctx, map[string]any{
		"_project_id": "proj-test",
		"invoice_id":  int64(55),
	})
	if err != nil {
		t.Fatalf("second create from invoice: %v", err)
	}
	order2 := out2.(map[string]any)["order"].(*Order)
	fulfillment2 := out2.(map[string]any)["fulfillment"].(*Fulfillment)
	if order2.ID != order.ID || fulfillment2.ID != fulfillment.ID {
		t.Fatalf("expected idempotent reuse, got order %d/%d fulfillment %d/%d", order.ID, order2.ID, fulfillment.ID, fulfillment2.ID)
	}
}
