package main

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type bumpTestPlatform struct {
	tk.BasePlatformClient
	checkoutPaymentPlatform
	invoice checkoutInvoice
	prices  map[int64]bumpPrice
	lines   []map[string]any
	callsMu sync.Mutex
}

func (p *bumpTestPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	p.callsMu.Lock()
	defer p.callsMu.Unlock()
	var value any
	switch app + ":" + tool {
	case "catalog:catalog_prices_get":
		value = map[string]any{"price": p.prices[int64Arg(args, "id")]}
	case "catalog:catalog_products_get":
		value = map[string]any{"product": map[string]any{"type": "one_time", "name": "Course"}}
	case "billing:invoices_create":
		p.lines = args["line_items"].([]map[string]any)
		p.invoice = checkoutInvoice{ID: 91, Status: "open", Currency: "EUR"}
		for _, line := range p.lines {
			p.invoice.TotalCents += int64(float64(int64Arg(line, "unit_price_cents")) * line["quantity"].(float64))
		}
		value = map[string]any{"invoice": map[string]any{"id": 91}}
	case "billing:invoices_get":
		value = map[string]any{"invoice": p.invoice}
	case "billing:invoices_refund":
		p.invoice.AmountPaidCents -= int64Arg(args, "amount_cents")
		p.invoice.Payments = append(p.invoice.Payments, struct {
			AmountCents int64 `json:"amount_cents"`
		}{-int64Arg(args, "amount_cents")})
		value = map[string]any{"refund": map[string]any{"status": "succeeded"}}
	default:
		return p.checkoutPaymentPlatform.CallAppResult(app, tool, args, out)
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, out)
}
func bumpFixture(t *testing.T) (*sdk.AppCtx, *bumpTestPlatform, *CheckoutSession, int64) {
	t.Helper()
	p := &bumpTestPlatform{prices: map[int64]bumpPrice{100: {ID: 100, ProductID: 10, UnitAmountCents: 9900, Currency: "EUR", Active: true}, 101: {ID: 101, ProductID: 11, UnitAmountCents: 1900, Currency: "EUR", Active: true}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testPID), tk.WithPlatform(p))
	cart, err := dbCartCreate(ctx, testPID, map[string]any{"metadata": map[string]any{"source_app": "community", "context_ref": "academy"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dbCartAddItem(ctx, testPID, cart.ID, 100, 1); err != nil {
		t.Fatal(err)
	}
	s, err := dbCheckoutStart(ctx, testPID, cart.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := upsertOrderBump(ctx, map[string]any{"_project_id": testPID, "trigger_price_id": 100, "price_id": 101, "title": "Workbook", "source_app": "community", "context_ref": "academy", "fulfillment_app": "community", "fulfillment_ref": "workbook-course"})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["bump"].(OrderBump).ID
	return ctx, p, s, id
}
func bumpArgs(sid, bid int64, selected bool) map[string]any {
	return map[string]any{"_project_id": testPID, "session_id": sid, "bump_id": bid, "selected": selected}
}

func TestOrderBumpsOptInRecoveryIdentityAndInvoice(t *testing.T) {
	ctx, p, s, bid := bumpFixture(t)
	bs, err := sessionBumps(ctx, testPID, s.ID, true)
	if err != nil || len(bs) != 1 || bs[0].Selected {
		t.Fatalf("offers=%+v err=%v", bs, err)
	}
	if _, err = setCheckoutBump(ctx, bumpArgs(s.ID, bid, true)); err != nil {
		t.Fatal(err)
	}
	if _, err = setCheckoutBump(ctx, bumpArgs(s.ID, bid, true)); err != nil {
		t.Fatal(err)
	}
	// Repeated selection, recovery and sign-in must not duplicate or lose it.
	restored, err := resolveCheckoutSession(ctx.AppDB(), testPID, map[string]any{"recovery_token": s.RecoveryToken})
	if err != nil {
		t.Fatal(err)
	}
	if restored.TotalCents != 11800 {
		t.Fatalf("total=%d", restored.TotalCents)
	}
	if _, err = dbCheckoutUpdate(ctx.AppDB(), testPID, s.ID, map[string]any{"email": "buyer@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err = setCheckoutBump(ctx, bumpArgs(s.ID, bid, false)); err != nil {
		t.Fatal(err)
	}
	restored, _ = dbCheckoutGet(ctx.AppDB(), testPID, s.ID)
	if restored.TotalCents != 9900 {
		t.Fatalf("removed total=%d", restored.TotalCents)
	}
	if _, err = setCheckoutBump(ctx, bumpArgs(s.ID, bid, true)); err != nil {
		t.Fatal(err)
	}
	_, iid, _, _, err := dbCheckoutPay(ctx, testPID, s.ID, map[string]any{"provider": "stripe", "presentation": "deferred"})
	if err != nil {
		t.Fatal(err)
	}
	if iid != 91 || len(p.lines) != 2 || p.invoice.TotalCents != 11800 {
		t.Fatalf("invoice=%+v lines=%+v", p.invoice, p.lines)
	}
	if meta := p.lines[1]["metadata"].(map[string]any); int64Arg(meta, "order_bump_id") != bid || strArg(meta, "fulfillment_ref") != "workbook-course" || int64Arg(meta, "checkout_item_id") == 0 {
		t.Fatalf("lost attribution: %+v", meta)
	}
	if _, err = setCheckoutBump(ctx, bumpArgs(s.ID, bid, false)); err == nil {
		t.Fatal("changed an invoiced checkout")
	}
}

func TestOrderBumpsRejectWrongScopeCurrencyRecurringAndUnavailable(t *testing.T) {
	ctx, p, s, bid := bumpFixture(t)
	args := bumpArgs(s.ID, bid, true)
	args["_project_id"] = "other"
	if _, err := setCheckoutBump(ctx, args); err == nil {
		t.Fatal("cross-project selection allowed")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE order_bumps SET context_ref='another-academy' WHERE id=?`, bid); err != nil {
		t.Fatal(err)
	}
	if _, err := setCheckoutBump(ctx, bumpArgs(s.ID, bid, true)); err == nil {
		t.Fatal("wrong community allowed")
	}
	ctx.AppDB().Exec(`UPDATE order_bumps SET context_ref='academy' WHERE id=?`, bid)
	price := p.prices[101]
	price.Currency = "USD"
	p.prices[101] = price
	if _, err := setCheckoutBump(ctx, bumpArgs(s.ID, bid, true)); err == nil {
		t.Fatal("mixed currency allowed")
	}
	price.Currency = "EUR"
	price.Interval = "month"
	p.prices[101] = price
	if _, err := setCheckoutBump(ctx, bumpArgs(s.ID, bid, true)); err == nil {
		t.Fatal("recurring bump allowed")
	}
	price.Interval = ""
	p.prices[101] = price
	if _, err := setCheckoutBump(ctx, bumpArgs(s.ID, bid, true)); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().Exec(`UPDATE order_bumps SET active=0 WHERE id=?`, bid)
	dbCheckoutUpdate(ctx.AppDB(), testPID, s.ID, map[string]any{"email": "buyer@example.com"})
	if _, _, _, _, err := dbCheckoutPay(ctx, testPID, s.ID, map[string]any{}); err == nil {
		t.Fatal("disabled selected offer charged")
	}
	if _, err := setCheckoutBump(ctx, bumpArgs(s.ID, bid, false)); err != nil {
		t.Fatal("cannot remove disabled offer", err)
	}
}

func TestItemRefundLeavesMainProductPaidAndIsIdempotent(t *testing.T) {
	ctx, p, s, bid := bumpFixture(t)
	if _, err := setCheckoutBump(ctx, bumpArgs(s.ID, bid, true)); err != nil {
		t.Fatal(err)
	}
	dbCheckoutUpdate(ctx.AppDB(), testPID, s.ID, map[string]any{"email": "buyer@example.com"})
	if _, _, _, _, err := dbCheckoutPay(ctx, testPID, s.ID, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	p.invoice.Status = "paid"
	p.invoice.AmountPaidCents = 11800
	ctx.AppDB().Exec(`UPDATE checkout_sessions SET status='paid' WHERE id=?`, s.ID)
	r, err := readCheckoutReceipt(ctx, testPID, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"_project_id": testPID, "session_id": s.ID, "item_id": r.Items[1].ID, "idempotency_key": "refund-workbook"}
	for range 2 {
		if _, err = refundCheckoutItem(ctx, args); err != nil {
			t.Fatal(err)
		}
	}
	r, err = readCheckoutReceipt(ctx, testPID, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].Status != "paid" || r.Items[0].RefundedCents != 0 || r.Items[1].Status != "refunded" || p.invoice.AmountPaidCents != 9900 {
		t.Fatalf("refund leaked to main item: %+v invoice=%+v", r, p.invoice)
	}
	args["idempotency_key"] = "refund-again"
	if _, err = refundCheckoutItem(ctx, args); err == nil {
		t.Fatal("refunded item charged twice")
	}
}

func TestReceiptDoesNotFulfillUnpaidInvoice(t *testing.T) {
	ctx, _, s, _ := bumpFixture(t)
	r, err := readCheckoutReceipt(ctx, testPID, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].Status != "pending" {
		t.Fatal("unpaid item fulfilled")
	}
	if _, err = refundCheckoutItem(ctx, map[string]any{"_project_id": testPID, "session_id": s.ID, "item_id": r.Items[0].ID, "idempotency_key": "unpaid"}); err == nil || errors.Is(err, nil) {
		t.Fatal("unpaid refund accepted")
	}
}

func TestCheckoutRejectsStalePageBeforeCreatingPayment(t *testing.T) {
	ctx, p, s, bid := bumpFixture(t)
	if _, err := setCheckoutBump(ctx, bumpArgs(s.ID, bid, true)); err != nil {
		t.Fatal(err)
	}
	dbCheckoutUpdate(ctx.AppDB(), testPID, s.ID, map[string]any{"email": "buyer@example.com"})
	if _, _, _, _, err := dbCheckoutPay(ctx, testPID, s.ID, map[string]any{"expected_total_cents": 9900, "expected_price_ids": []int64{100}}); err == nil {
		t.Fatal("stale amount accepted")
	}
	if p.invoice.ID != 0 {
		t.Fatal("stale page created an invoice")
	}
	if _, _, _, _, err := dbCheckoutPay(ctx, testPID, s.ID, map[string]any{"expected_total_cents": 11800, "expected_price_ids": []int64{100, 999}}); err == nil {
		t.Fatal("stale items accepted")
	}
	if p.invoice.ID != 0 {
		t.Fatal("stale item list created an invoice")
	}
}

func TestInvoicedBumpSnapshotSurvivesRuleChanges(t *testing.T) {
	ctx, _, s, bid := bumpFixture(t)
	if _, err := setCheckoutBump(ctx, bumpArgs(s.ID, bid, true)); err != nil {
		t.Fatal(err)
	}
	dbCheckoutUpdate(ctx.AppDB(), testPID, s.ID, map[string]any{"email": "buyer@example.com"})
	args := map[string]any{"provider": "stripe", "presentation": "deferred"}
	if _, _, _, _, err := dbCheckoutPay(ctx, testPID, s.ID, args); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().Exec(`UPDATE order_bumps SET active=0,title='Changed',price_id=999 WHERE id=?`, bid)
	offers, err := sessionBumps(ctx, testPID, s.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || !offers[0].Selected || !offers[0].Available || offers[0].PriceID != 101 || offers[0].AmountCents != 1900 {
		t.Fatalf("frozen offer changed: %+v", offers)
	}
	if _, _, _, _, err = dbCheckoutPay(ctx, testPID, s.ID, args); err != nil {
		t.Fatal("existing payment could not resume", err)
	}
}
