package main

import "testing"

func TestAttributionDoesNotReopenActiveCheckout(t *testing.T) {
	ctx, p, store, sale := seedAnalyticsSale(t, true)
	cart, _ := dbCartGet(ctx.AppDB(), ctx.CurrentProject(), ptrValue(sale.CartID), true)
	ch, e := dbCheckoutCreate(ctx.AppDB(), ctx.CurrentProject(), cart, 702, nil, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = (&App{}).toolCartAttributionSet(ctx, map[string]any{"store_id": store.ID, "session_token": cart.SessionToken, "attribution": map[string]any{"consent": "granted", "visitor_id": "stable-visitor"}})
	if e != nil {
		t.Fatal(e)
	}
	current, _ := dbCheckoutGet(ctx.AppDB(), ctx.CurrentProject(), ch.ID)
	if current.Status != "started" {
		t.Fatal("attribution changed checkout", current.Status)
	}
	if hasPlatformCall(p.calls, "checkout", "checkout_cancel") || hasPlatformCall(p.calls, "checkout", "cart_create") {
		t.Fatal("optional measurement modified payment state")
	}
}
func TestRetryPreservesCancelledSaleInvoice(t *testing.T) {
	ctx, p, store, sale := seedAnalyticsSale(t, true)
	pid := ctx.CurrentProject()
	cart, _ := dbCartGet(ctx.AppDB(), pid, ptrValue(sale.CartID), true)
	variants, e := dbListingGet(ctx.AppDB(), pid, 1, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = dbCartAddItem(ctx.AppDB(), pid, cart.ID, variants.Variants[0], 1, 601); e != nil {
		t.Fatal(e)
	}
	cart, _ = dbCartGet(ctx.AppDB(), pid, cart.ID, true)
	ch, e := dbCheckoutCreate(ctx.AppDB(), pid, cart, 702, nil, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ctx.AppDB().Exec(`UPDATE commerce_sales SET checkout_id=?,payment_status='unpaid',status='cancelled',paid_at=NULL WHERE id=?`, ch.ID, sale.ID); e != nil {
		t.Fatal(e)
	}
	ctx.AppDB().Exec(`UPDATE commerce_checkout_sessions SET status='cancelled',invoice_id=501 WHERE id=?`, ch.ID)
	ctx.AppDB().Exec(`UPDATE commerce_carts SET status='open' WHERE id=?`, cart.ID)
	p.responses["checkout:cart_create"] = map[string]any{"cart": map[string]any{"id": 802}}
	p.responses["checkout:cart_add_item"] = map[string]any{"cart": map[string]any{"items": []any{map[string]any{"id": 901, "price_id": 201, "quantity": 1}}}}
	fresh, e := (&App{}).createCart(ctx, map[string]any{"store_id": store.ID, "session_token": cart.SessionToken})
	if e != nil {
		t.Fatal(e)
	}
	if fresh.ID == cart.ID || len(fresh.Items) != 1 || fresh.SubtotalCents != cart.SubtotalCents {
		t.Fatal("retry cart not preserved", fresh)
	}
	old, _ := dbCheckoutGet(ctx.AppDB(), pid, ch.ID)
	oldSale, _ := dbSaleGet(ctx.AppDB(), pid, sale.ID)
	if ptrValue(old.InvoiceID) != 501 || ptrValue(oldSale.InvoiceID) != 501 || old.Status != "cancelled" || oldSale.Status != "cancelled" {
		t.Fatal("old financial attempt was overwritten")
	}
	next, e := dbCheckoutCreate(ctx.AppDB(), pid, fresh, 803, nil, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	if next.ID == ch.ID {
		t.Fatal("reused cancelled checkout identity")
	}
}
