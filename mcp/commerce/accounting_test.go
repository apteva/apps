package main

import (
	"database/sql"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

func TestSaleSnapshotsSupplierCostAndProviderShipping(t *testing.T) {
	db := openCommerceTestDB(t)
	sale := createProfitabilitySale(t, db, "snapshots")

	if len(sale.Items) != 1 {
		t.Fatalf("sale items=%d, want 1", len(sale.Items))
	}
	item := sale.Items[0]
	if item.UnitCostCents != 1100 || item.CostCurrency != "USD" || item.CostSource != "prodigi" || item.CostCapturedAt == "" {
		t.Fatalf("unexpected immutable item cost: %#v", item)
	}
	if sale.ShippingRevenueCents != 550 || sale.ShippingCostCents != 550 || sale.ProductCostCents != 1100 {
		t.Fatalf("unexpected sale financial snapshot: %#v", sale)
	}
	if sale.ProfitabilityStatus != "pending_payment" {
		t.Fatalf("profitability status=%q, want pending_payment", sale.ProfitabilityStatus)
	}

	if _, err := db.Exec(`UPDATE commerce_variant_sources SET unit_cost_cents=9999 WHERE project_id=? AND variant_id=?`,
		"snapshots", *item.VariantID); err != nil {
		t.Fatal(err)
	}
	unchanged, err := dbSaleGet(db, "snapshots", sale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Items[0].UnitCostCents != 1100 || unchanged.ProductCostCents != 1100 {
		t.Fatalf("sale cost changed with live source: %#v", unchanged)
	}
}

func TestFinancialEventsAreIdempotentAndComputeContributionProfit(t *testing.T) {
	db := openCommerceTestDB(t)
	pid := "ledger"
	sale := createProfitabilitySale(t, db, pid)
	if _, err := db.Exec(`UPDATE commerce_sales SET status='paid', payment_status='paid', paid_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, sale.ID); err != nil {
		t.Fatal(err)
	}

	event, err := dbFinancialEventEnsure(db, pid, sale.ID, financialKindPaymentFee, 130, "USD", "stripe",
		"stripe:fee:1", "fee_1", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := dbFinancialEventEnsure(db, pid, sale.ID, financialKindPaymentFee, 130, "USD", "stripe",
		"stripe:fee:1", "fee_1", nil, "")
	if err != nil || replayed.ID != event.ID {
		t.Fatalf("idempotent replay=%#v err=%v", replayed, err)
	}
	externalReplay, err := dbFinancialEventEnsure(db, pid, sale.ID, financialKindPaymentFee, 130, "USD", "billing",
		"billing:fee:1", "fee_1", nil, "")
	if err != nil || externalReplay.ID != event.ID {
		t.Fatalf("external-id replay=%#v err=%v", externalReplay, err)
	}
	if _, err := dbFinancialEventEnsure(db, pid, sale.ID, financialKindPaymentFee, 131, "USD", "stripe",
		"stripe:fee:1", "fee_1", nil, ""); err == nil {
		t.Fatal("conflicting idempotent event was accepted")
	}
	if _, err := dbFinancialEventEnsure(db, pid, sale.ID, financialKindRefund, 500, "USD", "billing",
		"billing:refund:1", "refund_1", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := dbFinancialEventEnsure(db, pid, sale.ID, financialKindBillingReconciliation, 0, "USD", "billing",
		"billing:reconciled:1", "invoice_1", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := dbRecomputeSaleProfitability(db, pid, sale.ID); err != nil {
		t.Fatal(err)
	}
	got, err := dbSaleGet(db, pid, sale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalCents != 3750 || got.NetRevenueCents != 3250 || got.ProductCostCents != 1100 ||
		got.ShippingCostCents != 550 || got.PaymentFeeCents != 130 || got.RefundedCents != 500 ||
		got.GrossProfitCents != 2150 || got.ContributionProfitCents != 1470 || got.ProfitabilityStatus != "complete" {
		t.Fatalf("unexpected profitability totals: %#v", got)
	}
	report, err := dbProfitabilityReport(db, pid, map[string]any{"store_id": got.StoreID})
	if err != nil {
		t.Fatal(err)
	}
	if report.SaleCount != 1 || report.ContributionProfitCents != 1470 || report.CompleteSaleCount != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestBillingReconciliationImportsRefundAndExactFee(t *testing.T) {
	platform := newCommercePlatformStub()
	platform.responses["billing:invoices_get"] = map[string]any{"invoice": map[string]any{
		"id": int64(501), "currency": "USD", "status": "open", "total_cents": int64(3750),
		"amount_paid_cents": int64(3250), "updated_at": "2026-08-20T10:00:00Z",
		"payments": []any{
			map[string]any{"id": int64(1), "amount_cents": int64(3750), "currency": "USD", "method": "stripe", "fee_cents": int64(130), "external_id": "pi_1", "received_at": "2026-08-20T09:00:00Z"},
			map[string]any{"id": int64(2), "amount_cents": int64(-500), "currency": "USD", "method": "stripe", "external_id": "re_1", "received_at": "2026-08-20T10:00:00Z"},
		},
	}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("billing-sync"), tk.WithPlatform(platform))
	sale := createProfitabilitySale(t, ctx.AppDB(), "billing-sync")
	if _, err := ctx.AppDB().Exec(`UPDATE commerce_sales SET status='paid', payment_status='paid', paid_at=CURRENT_TIMESTAMP WHERE id=?`, sale.ID); err != nil {
		t.Fatal(err)
	}
	sale, _ = dbSaleGet(ctx.AppDB(), "billing-sync", sale.ID)
	if err := (&App{}).reconcileSaleBilling(ctx, "billing-sync", sale); err != nil {
		t.Fatal(err)
	}
	got, err := dbSaleGet(ctx.AppDB(), "billing-sync", sale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PaymentStatus != "partially_refunded" || got.RefundedCents != 500 || got.PaymentFeeCents != 130 || got.ProfitabilityStatus != "complete" {
		t.Fatalf("unexpected reconciled sale: %#v", got)
	}
	events, err := dbFinancialEvents(ctx.AppDB(), "billing-sync", sale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 { // checkout shipping, refund, fee, reconciliation marker
		t.Fatalf("financial events=%d, want 4: %#v", len(events), events)
	}
}

func TestProfitabilityMigrationPreservesExistingSales(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, path := range []string{
		"migrations/001_init.sql", "migrations/002_harden_checkout.sql", "migrations/003_provider_fulfillment.sql",
		"migrations/004_store_payments.sql", "migrations/005_checkout_quotes.sql", "migrations/006_cart_activity_index.sql",
		"migrations/007_marketing_channels.sql",
	} {
		execMigration(t, db, path)
	}
	res, err := db.Exec(`INSERT INTO commerce_stores (project_id, slug, name) VALUES ('upgrade-profit', 'main', 'Main')`)
	if err != nil {
		t.Fatal(err)
	}
	storeID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO commerce_sales (project_id, store_id, shipping_cents, total_cents, currency) VALUES ('upgrade-profit', ?, 700, 3200, 'USD')`, storeID)
	if err != nil {
		t.Fatal(err)
	}
	saleID, _ := res.LastInsertId()
	execMigration(t, db, "migrations/008_sale_profitability.sql")
	var shippingRevenue int64
	var status string
	if err := db.QueryRow(`SELECT shipping_revenue_cents, profitability_status FROM commerce_sales WHERE id=?`, saleID).Scan(&shippingRevenue, &status); err != nil {
		t.Fatal(err)
	}
	if shippingRevenue != 700 || status != "pending" {
		t.Fatalf("upgraded sale shipping=%d status=%q", shippingRevenue, status)
	}
	if _, err := db.Exec(`INSERT INTO commerce_sale_financial_events (project_id, sale_id, kind, amount_cents, currency, idempotency_key) VALUES ('upgrade-profit', ?, 'payment_fee', 100, 'USD', 'upgrade:fee')`, saleID); err != nil {
		t.Fatalf("new ledger unavailable after upgrade: %v", err)
	}
}

func createProfitabilitySale(t *testing.T, db *sql.DB, pid string) *Sale {
	t.Helper()
	store, err := dbStoreCreate(db, pid, map[string]any{"slug": "main", "name": "Main", "default_currency": "USD"})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := dbListingCreate(db, pid, map[string]any{"store_id": store.ID, "title": "Cat Tee", "status": "active"})
	if err != nil {
		t.Fatal(err)
	}
	variant, err := dbVariantCreate(db, pid, map[string]any{"listing_id": listing.ID, "sku": "CAT-M", "title": "M", "price_cents": int64(3200), "currency": "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO commerce_variant_sources
		(project_id, store_id, variant_id, connection_id, provider_slug, external_product_id, external_variant_id,
		provider_sku, unit_cost_cents, currency, availability) VALUES (?, ?, ?, 77, 'prodigi', 'cat', 'cat-m', 'CAT-M', 1100, 'USD', 'available')`,
		pid, store.ID, variant.ID); err != nil {
		t.Fatal(err)
	}
	cart, err := dbCartCreate(db, pid, map[string]any{"store_id": store.ID, "session_token": "session-" + pid, "currency": "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbCartAddItem(db, pid, cart.ID, variant, 1, 0); err != nil {
		t.Fatal(err)
	}
	quote := map[string]any{"selected": []any{map[string]any{
		"id": "standard", "name": "Standard", "amount_cents": int64(550), "currency": "USD",
		"provider": "prodigi", "connection_id": int64(77),
	}}}
	if err := dbCartApplyShipping(db, pid, cart.ID, 550, quote); err != nil {
		t.Fatal(err)
	}
	cart, err = dbCartGet(db, pid, cart.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dbCheckoutCreate(db, pid, cart, 301, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbCheckoutInvoice(db, pid, checkout.ID, 501, "INV-501"); err != nil {
		t.Fatal(err)
	}
	checkout, err = dbCheckoutGet(db, pid, checkout.ID)
	if err != nil {
		t.Fatal(err)
	}
	sale, err := dbSaleCreateFromCheckout(db, pid, checkout)
	if err != nil {
		t.Fatal(err)
	}
	return sale
}
