package main

import (
	"strings"
	"sync"
	"testing"
)

func TestSourceObligationReplayAndConflict(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "supplier@example.test", "Supplier")
	args := map[string]any{"source_key": "generic:install-45:gig-1:obligation-1", "vendor_id": v.ID, "currency": "EUR", "line_items": []any{line("Accepted work", 1, 5000, 0), line("Travel", 1, 300, 0)}}
	out, e := app.toolBillsCreateObligation(ctx, args)
	if e != nil {
		t.Fatal(e)
	}
	b := out.(map[string]any)["bill"].(*Bill)
	if b.Status != "received" || b.TotalCents != 5300 || b.AmountPaidCents != 0 || b.SourceKey == "" || b.OutstandingMinor != 5300 {
		t.Fatal(b)
	}
	out, e = app.toolBillsCreateObligation(ctx, args)
	if e != nil || out.(map[string]any)["bill"].(*Bill).ID != b.ID {
		t.Fatal(out, e)
	}
	args["line_items"] = []any{line("Changed", 1, 9000, 0)}
	if _, e = app.toolBillsCreateObligation(ctx, args); e == nil {
		t.Fatal("changed source accepted")
	}
	delete(args, "line_items")
	args["paid"] = map[string]any{"method": "cash"}
	if _, e = app.toolBillsCreateObligation(ctx, args); e == nil {
		t.Fatal("paid-on-create allowed")
	}
	if _, e = app.toolBillsUpdate(ctx, map[string]any{"id": b.ID, "patch": map[string]any{"total_cents": 9000}}); e == nil {
		t.Fatal("source bill mutated")
	}
}
func TestSourceObligationConcurrent(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "source-race@example.test", "Race")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = app.toolBillsCreateObligation(ctx, map[string]any{"source_key": "same-obligation", "vendor_id": v.ID, "currency": "USD", "line_items": []any{line("Work", 1, 100, 0)}})
		}()
	}
	wg.Wait()
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM bills`).Scan(&n)
	if n != 1 {
		t.Fatal("duplicate bills", n)
	}
}
func TestPaymentRetryAndAdjustments(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "payments@example.test", "Payments")
	b := mustBill(t, ctx, v.ID, "P1", []any{line("Work", 1, 10000, 0)})
	mustApprove(t, ctx, b.ID)
	pay := map[string]any{"bill_id": b.ID, "amount_cents": int64(4000), "method": "wire", "request_key": "payment-1"}
	out, e := app.toolBillPaymentsRecord(ctx, pay)
	if e != nil {
		t.Fatal(e)
	}
	b = out.(map[string]any)["bill"].(*Bill)
	if b.Status != "approved" || b.AmountPaidCents != 4000 {
		t.Fatal(b)
	}
	out, e = app.toolBillPaymentsRecord(ctx, pay)
	if e != nil || out.(map[string]any)["bill"].(*Bill).AmountPaidCents != 4000 {
		t.Fatal(out, e)
	}
	pay["amount_cents"] = 5000
	if _, e = app.toolBillPaymentsRecord(ctx, pay); e == nil {
		t.Fatal("conflicting payment replay accepted")
	}
	if _, e = app.toolBillsVoid(ctx, map[string]any{"bill_id": b.ID}); e == nil {
		t.Fatal("partially paid bill voided")
	}
	adj := map[string]any{"bill_id": b.ID, "amount_minor": int64(2000), "kind": "credit", "reason": "Scope reduction", "request_key": "credit-1"}
	out, e = app.toolBillsAdjust(ctx, adj)
	if e != nil {
		t.Fatal(e)
	}
	b = out.(map[string]any)["bill"].(*Bill)
	if b.CreditMinor != 2000 || b.OutstandingMinor != 4000 || b.TotalCents != 10000 {
		t.Fatal(b)
	}
	out, e = app.toolBillsAdjust(ctx, adj)
	if e != nil || out.(map[string]any)["bill"].(*Bill).CreditMinor != 2000 {
		t.Fatal(out, e)
	}
	out, e = app.toolBillPaymentsRecord(ctx, map[string]any{"bill_id": b.ID, "amount_cents": int64(4000), "method": "wire", "request_key": "payment-2"})
	if e != nil {
		t.Fatal(e)
	}
	b = out.(map[string]any)["bill"].(*Bill)
	if b.Status != "paid" || b.OutstandingMinor != 0 {
		t.Fatal(b)
	}
	out, e = app.toolBillsAdjust(ctx, map[string]any{"bill_id": b.ID, "amount_minor": int64(1000), "kind": "reversal", "reason": "Incorrect payment record", "request_key": "reverse-1"})
	if e != nil {
		t.Fatal(e)
	}
	b = out.(map[string]any)["bill"].(*Bill)
	if b.Status != "approved" || b.AmountPaidCents != 7000 || b.OutstandingMinor != 1000 {
		t.Fatal(b)
	}
	if _, e = app.toolBillsAdjust(ctx, map[string]any{"bill_id": b.ID, "amount_minor": int64(9000), "kind": "refund", "reason": "Invalid", "request_key": "bad-refund"}); e == nil || !strings.Contains(e.Error(), "exceeds") {
		t.Fatal(e)
	}
}

func TestAdoptExistingBillAndVoidReplay(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "legacy@example.test", "Legacy")
	b := mustBill(t, ctx, v.ID, "Legacy-1", []any{line("Work", 1, 1000, 0)})
	args := map[string]any{"source_key": "legacy-source", "existing_bill_id": b.ID, "vendor_id": v.ID, "currency": b.Currency, "line_items": []any{line("Approved historical work", 1, 1000, 0)}}
	out, e := app.toolBillsCreateObligation(ctx, args)
	if e != nil || out.(map[string]any)["bill"].(*Bill).ID != b.ID {
		t.Fatal(out, e)
	}
	if _, e = app.toolBillsVoid(ctx, map[string]any{"bill_id": b.ID, "reason": "Replacement"}); e != nil {
		t.Fatal(e)
	}
	out, e = app.toolBillsCreateObligation(ctx, args)
	if e != nil || out.(map[string]any)["bill"].(*Bill).Status != "void" {
		t.Fatal(out, e)
	}
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM bills`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}
func TestBackdatedPaymentReturnsExactRecord(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "backdated@example.test", "Backdated")
	b := mustBill(t, ctx, v.ID, "Backdate", []any{line("Work", 1, 10000, 0)})
	mustApprove(t, ctx, b.ID)
	for _, date := range []string{"2026-09-08T12:00:00Z", "2026-01-01T12:00:00Z"} {
		args := map[string]any{"bill_id": b.ID, "amount_cents": 1000, "method": "wire", "sent_at": date, "request_key": date}
		out, e := app.toolBillPaymentsRecord(ctx, args)
		if e != nil {
			t.Fatal(e)
		}
		pay := out.(map[string]any)["payment"].(*BillPayment)
		if pay == nil || pay.SentAt != date {
			t.Fatal(pay)
		}
		if _, e = app.toolBillPaymentsRecord(ctx, args); e != nil {
			t.Fatal(e)
		}
	}
}
