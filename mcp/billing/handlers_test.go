package main

// Tier 1 — every MCP tool handler exercised against an in-memory
// SQLite. Fast (<1s end-to-end), runs on every commit. Covers the
// happy paths plus the lifecycle gates: provider freezing, finalize
// idempotency, void/finalize/payment status guards, project-scope
// safety, and the v0.1.0 ⇄ stripe gate.

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Helpers ────────────────────────────────────────────────────────

// newTestCtx returns a fresh *sdk.AppCtx with the manifest loaded,
// migrations applied, APTEVA_PROJECT_ID="test-proj" set, and the
// package-level globalCtx wired up so REST-shaped handlers (which
// read it from a global) see the same DB.
func newTestCtx(t *testing.T, opts ...tk.Option) *sdk.AppCtx {
	t.Helper()
	full := append([]tk.Option{tk.WithProjectID("test-proj")}, opts...)
	ctx := tk.NewAppCtx(t, "apteva.yaml", full...)
	globalCtx = ctx
	return ctx
}

func mustCustomer(t *testing.T, ctx *sdk.AppCtx, email, name string) *Customer {
	t.Helper()
	app := &App{}
	out, err := app.toolCustomersUpsertByEmail(ctx, map[string]any{
		"email": email,
		"defaults": map[string]any{
			"name": name,
		},
	})
	if err != nil {
		t.Fatalf("upsert customer: %v", err)
	}
	return out.(map[string]any)["customer"].(*Customer)
}

func mustDraft(t *testing.T, ctx *sdk.AppCtx, customerID int64, lines []any) *Invoice {
	t.Helper()
	app := &App{}
	out, err := app.toolInvoicesCreate(ctx, map[string]any{
		"customer_id": customerID,
		"line_items":  lines,
	})
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	return out.(map[string]any)["invoice"].(*Invoice)
}

func mustFinalize(t *testing.T, ctx *sdk.AppCtx, id int64) *Invoice {
	t.Helper()
	app := &App{}
	out, err := app.toolInvoicesFinalize(ctx, map[string]any{"invoice_id": id})
	if err != nil {
		t.Fatalf("finalize invoice %d: %v", id, err)
	}
	return out.(map[string]any)["invoice"].(*Invoice)
}

func line(desc string, qty float64, unitCents int64, taxBps int) map[string]any {
	return map[string]any{
		"description":      desc,
		"quantity":         qty,
		"unit_price_cents": unitCents,
		"tax_rate_bps":     taxBps,
	}
}

// ─── Customers ──────────────────────────────────────────────────────

func TestCustomerUpsertByEmail_CreatesThenDedupes(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	out1, err := app.toolCustomersUpsertByEmail(ctx, map[string]any{
		"email": "alice@acme.com",
		"defaults": map[string]any{
			"name": "Alice Cooper",
		},
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	r1 := out1.(map[string]any)
	if r1["was_created"] != true {
		t.Errorf("expected was_created=true on first call")
	}
	c1 := r1["customer"].(*Customer)

	// Second call with case-folded value should hit the same row.
	out2, err := app.toolCustomersUpsertByEmail(ctx, map[string]any{
		"email": "ALICE@acme.com",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	r2 := out2.(map[string]any)
	if r2["was_created"] != false {
		t.Errorf("expected was_created=false on dedupe")
	}
	if r2["customer"].(*Customer).ID != c1.ID {
		t.Errorf("expected same id on dedupe")
	}
}

func TestCustomerSearch_MatchesNameAndEmail(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	mustCustomer(t, ctx, "alice@acme.com", "Alice Cooper")
	mustCustomer(t, ctx, "bob@globex.com", "Bob Dylan")
	mustCustomer(t, ctx, "charlie@acme.com", "Charlie Parker")

	cases := []struct {
		q    string
		want int
	}{
		{"alice", 1},
		{"acme", 2}, // alice@acme + charlie@acme
		{"dylan", 1},
		{"nonexistent", 0},
	}
	for _, c := range cases {
		t.Run(c.q, func(t *testing.T) {
			out, err := app.toolCustomersSearch(ctx, map[string]any{"q": c.q})
			if err != nil {
				t.Fatal(err)
			}
			if got := out.(map[string]any)["count"].(int); got != c.want {
				t.Errorf("q=%q got %d, want %d", c.q, got, c.want)
			}
		})
	}
}

func TestCustomerUpdate_PartialPatch(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")

	_, err := app.toolCustomersUpdate(ctx, map[string]any{
		"id": c.ID,
		"patch": map[string]any{
			"phone":    "+14155550100",
			"currency": "EUR",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := app.toolCustomersGet(ctx, map[string]any{"id": c.ID})
	got := out.(map[string]any)["customer"].(*Customer)
	if got.Phone != "+14155550100" {
		t.Errorf("phone=%q", got.Phone)
	}
	if got.Currency != "EUR" {
		t.Errorf("currency=%q", got.Currency)
	}
	// Original name preserved.
	if got.Name != "Alice" {
		t.Errorf("name=%q (should not have been wiped)", got.Name)
	}
}

func TestCustomerMerge_ReassignsInvoicesAndPayments(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	loser := mustCustomer(t, ctx, "alice@home.com", "Alice (home)")
	winner := mustCustomer(t, ctx, "alice@work.com", "Alice (work)")

	// Draft + finalize an invoice on the loser; record a payment.
	inv := mustDraft(t, ctx, loser.ID, []any{
		line("Service", 1, 1000, 0),
	})
	mustFinalize(t, ctx, inv.ID)
	if _, err := app.toolPaymentsRecord(ctx, map[string]any{
		"invoice_id":   inv.ID,
		"amount_cents": int64(1000),
		"method":       "wire",
	}); err != nil {
		t.Fatal(err)
	}

	// Merge.
	if _, err := app.toolCustomersMerge(ctx, map[string]any{
		"loser_id":  loser.ID,
		"winner_id": winner.ID,
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	// Invoice now belongs to winner.
	out, _ := app.toolInvoicesGet(ctx, map[string]any{"id": inv.ID})
	got := out.(map[string]any)["invoice"].(*Invoice)
	if got.CustomerID != winner.ID {
		t.Errorf("invoice customer_id = %d, want %d (winner)", got.CustomerID, winner.ID)
	}

	// Payments reassigned too.
	plOut, _ := app.toolPaymentsList(ctx, map[string]any{"customer_id": winner.ID})
	if got := plOut.(map[string]any)["count"].(int); got != 1 {
		t.Errorf("winner payment count = %d, want 1", got)
	}

	// Loser is soft-deleted (lookup returns nil).
	gotLoser, _ := app.toolCustomersGet(ctx, map[string]any{"id": loser.ID})
	if gotLoser.(map[string]any)["found"] != false {
		t.Errorf("loser should be soft-deleted, got %#v", gotLoser)
	}
}

func TestCustomerGetContext_LifetimeTotals(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("X", 1, 5000, 0)})
	mustFinalize(t, ctx, inv.ID)
	if _, err := app.toolPaymentsRecord(ctx, map[string]any{
		"invoice_id":   inv.ID,
		"amount_cents": int64(2000), // partial
		"method":       "wire",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolCustomersGetContext(ctx, map[string]any{"id": c.ID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	lt := res["lifetime"].(map[string]any)
	if got := lt["invoiced_cents"].(int64); got != 5000 {
		t.Errorf("invoiced_cents=%d, want 5000", got)
	}
	if got := lt["paid_cents"].(int64); got != 2000 {
		t.Errorf("paid_cents=%d, want 2000", got)
	}
	if got := lt["outstanding_cents"].(int64); got != 3000 {
		t.Errorf("outstanding_cents=%d, want 3000", got)
	}
}

// ─── Project-scope safety ───────────────────────────────────────────

func TestUpsert_RejectsWithoutProjectID_GlobalScope(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	globalCtx = ctx
	app := &App{}
	_, err := app.toolCustomersUpsertByEmail(ctx, map[string]any{
		"email": "alice@acme.com",
	})
	if err == nil {
		t.Fatal("expected error when project_id is missing in global scope")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Errorf("error %q should mention project_id", err.Error())
	}
}

// ─── Invoices: provider gate (v0.1.0) ───────────────────────────────

func TestInvoiceCreate_RejectsStripeProvider_v010(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	_, err := app.toolInvoicesCreate(ctx, map[string]any{
		"customer_id": c.ID,
		"provider":    "stripe",
	})
	if err == nil {
		t.Fatal("expected provider=stripe to be rejected in v0.1.0")
	}
	if !strings.Contains(err.Error(), "v0.1.1") {
		t.Errorf("error %q should mention v0.1.1", err.Error())
	}
}

func TestInvoiceCreate_RejectsUnknownProvider(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	_, err := app.toolInvoicesCreate(ctx, map[string]any{
		"customer_id": c.ID,
		"provider":    "paypal", // not a real provider
	})
	if err == nil {
		t.Fatal("expected unknown provider to be rejected")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error %q should mention unknown provider", err.Error())
	}
}

func TestInvoiceCreate_RejectsBadCurrency(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	_, err := app.toolInvoicesCreate(ctx, map[string]any{
		"customer_id": c.ID,
		"currency":    "DOLLARS", // 7 letters, not a code
	})
	if err == nil {
		t.Fatal("expected bad currency to be rejected")
	}
	if !strings.Contains(err.Error(), "ISO 4217") {
		t.Errorf("error %q should mention ISO 4217", err.Error())
	}
}

// ─── Invoices: create + totals ──────────────────────────────────────

func TestInvoiceCreate_ComputesTotalsFromLineItems(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{
		line("Consulting", 10, 15000, 2000), // 150000 + 30000 tax
		line("Travel", 1, 8500, 0),          // 8500 untaxed
	})
	if inv.SubtotalCents != 158500 {
		t.Errorf("subtotal=%d, want 158500", inv.SubtotalCents)
	}
	if inv.TaxCents != 30000 {
		t.Errorf("tax=%d, want 30000", inv.TaxCents)
	}
	if inv.TotalCents != 188500 {
		t.Errorf("total=%d, want 188500", inv.TotalCents)
	}
	if inv.Status != "draft" {
		t.Errorf("status=%q, want draft", inv.Status)
	}
	if inv.Number != "" {
		t.Errorf("draft should not have a number, got %q", inv.Number)
	}
	if inv.Provider != "local" {
		t.Errorf("provider=%q, want local (install default)", inv.Provider)
	}
	if inv.Currency != "USD" {
		t.Errorf("currency=%q, want USD (install default)", inv.Currency)
	}
}

func TestInvoiceAddLineItem_RecomputesTotals(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("First", 1, 1000, 0)})
	if inv.TotalCents != 1000 {
		t.Fatalf("initial total=%d", inv.TotalCents)
	}

	out, err := app.toolInvoicesAddLineItem(ctx, map[string]any{
		"invoice_id":       inv.ID,
		"description":      "Second",
		"quantity":         2,
		"unit_price_cents": int64(500),
		"tax_rate_bps":     1000, // 10%
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["invoice"].(*Invoice)
	// 1000 (first) + 1000 (2 * 500 second) = 2000 subtotal; tax = 100
	if got.SubtotalCents != 2000 || got.TaxCents != 100 || got.TotalCents != 2100 {
		t.Errorf("after add: sub=%d tax=%d total=%d, want 2000/100/2100",
			got.SubtotalCents, got.TaxCents, got.TotalCents)
	}
	if len(got.LineItems) != 2 {
		t.Errorf("line_items=%d, want 2", len(got.LineItems))
	}
}

func TestInvoiceAddLineItem_RejectsOnNonDraft(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("X", 1, 1000, 0)})
	mustFinalize(t, ctx, inv.ID)

	_, err := app.toolInvoicesAddLineItem(ctx, map[string]any{
		"invoice_id":       inv.ID,
		"description":      "Late addition",
		"unit_price_cents": int64(500),
	})
	if err == nil {
		t.Fatal("expected error: cannot add line item to open invoice")
	}
	if !strings.Contains(err.Error(), "draft") {
		t.Errorf("error %q should mention draft", err.Error())
	}
}

// ─── Invoices: finalize ─────────────────────────────────────────────

func TestInvoiceFinalize_MintsNumberAndSequences(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	a := mustDraft(t, ctx, c.ID, []any{line("X", 1, 1000, 0)})
	b := mustDraft(t, ctx, c.ID, []any{line("Y", 1, 2000, 0)})

	gotA := mustFinalize(t, ctx, a.ID)
	gotB := mustFinalize(t, ctx, b.ID)

	if gotA.Number == "" || gotB.Number == "" {
		t.Fatal("numbers should be minted on finalize")
	}
	if gotA.Number == gotB.Number {
		t.Errorf("expected distinct numbers, both got %q", gotA.Number)
	}
	if !strings.HasPrefix(gotA.Number, "INV-") {
		t.Errorf("expected default INV- prefix, got %q", gotA.Number)
	}
	// Default format ends in :04 → 4-digit padded sequence; default
	// seq_start is 1001, so the first finalize this year is ...1001,
	// second is ...1002. (Avoids the "0001" first-invoice tell.)
	if !strings.HasSuffix(gotA.Number, "1001") {
		t.Errorf("first invoice number=%q, expected ...1001 suffix", gotA.Number)
	}
	if !strings.HasSuffix(gotB.Number, "1002") {
		t.Errorf("second invoice number=%q, expected ...1002 suffix", gotB.Number)
	}
	if gotA.Status != "open" {
		t.Errorf("post-finalize status=%q, want open", gotA.Status)
	}
}

func TestInvoiceFinalize_Idempotent(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("X", 1, 1000, 0)})
	first := mustFinalize(t, ctx, inv.ID)
	second := mustFinalize(t, ctx, inv.ID)
	if first.Number != second.Number {
		t.Errorf("idempotent finalize should return same number, got %q vs %q",
			first.Number, second.Number)
	}
	if first.ID != second.ID {
		t.Errorf("idempotent finalize should return same row, ids %d vs %d", first.ID, second.ID)
	}
}

func TestInvoiceFinalize_RejectsEmptyDraft(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	empty := mustDraft(t, ctx, c.ID, nil)

	_, err := app.toolInvoicesFinalize(ctx, map[string]any{"invoice_id": empty.ID})
	if err == nil {
		t.Fatal("expected error: empty draft cannot finalize")
	}
	if !strings.Contains(err.Error(), "empty") && !strings.Contains(err.Error(), "line item") {
		t.Errorf("error %q should mention empty / line item", err.Error())
	}
}

// ─── Invoices: void ─────────────────────────────────────────────────

func TestInvoiceVoid_OnOpenSucceeds(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("X", 1, 1000, 0)})
	mustFinalize(t, ctx, inv.ID)

	out, err := app.toolInvoicesVoid(ctx, map[string]any{
		"invoice_id": inv.ID,
		"reason":     "duplicate",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["invoice"].(*Invoice)
	if got.Status != "void" {
		t.Errorf("status=%q, want void", got.Status)
	}
	if got.VoidedAt == "" {
		t.Errorf("voided_at should be set")
	}
}

func TestInvoiceVoid_RejectsOnPaid(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("X", 1, 1000, 0)})
	mustFinalize(t, ctx, inv.ID)
	if _, err := app.toolPaymentsRecord(ctx, map[string]any{
		"invoice_id":   inv.ID,
		"amount_cents": int64(1000),
		"method":       "wire",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := app.toolInvoicesVoid(ctx, map[string]any{"invoice_id": inv.ID})
	if err == nil {
		t.Fatal("expected void to be rejected on paid invoice")
	}
	if !strings.Contains(err.Error(), "paid") {
		t.Errorf("error %q should mention paid", err.Error())
	}
}

func TestInvoiceVoid_RejectsOnDraftWithHint(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("X", 1, 1000, 0)})

	_, err := app.toolInvoicesVoid(ctx, map[string]any{"invoice_id": inv.ID})
	if err == nil {
		t.Fatal("expected void to be rejected on draft")
	}
	if !strings.Contains(err.Error(), "delete") {
		t.Errorf("error %q should hint at delete", err.Error())
	}
}

// ─── Payments ───────────────────────────────────────────────────────

func TestPayments_PartialKeepsOpen(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("X", 1, 1000, 0)})
	mustFinalize(t, ctx, inv.ID)

	out, err := app.toolPaymentsRecord(ctx, map[string]any{
		"invoice_id":   inv.ID,
		"amount_cents": int64(400),
		"method":       "wire",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["invoice"].(*Invoice)
	if got.Status != "open" {
		t.Errorf("status=%q, want open (partial payment)", got.Status)
	}
	if got.AmountPaidCents != 400 {
		t.Errorf("amount_paid=%d, want 400", got.AmountPaidCents)
	}
}

func TestPayments_CoveringTotalTransitionsToPaid(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("X", 1, 1000, 0)})
	mustFinalize(t, ctx, inv.ID)

	// Two partials totalling the invoice.
	for i, amt := range []int64{600, 400} {
		out, err := app.toolPaymentsRecord(ctx, map[string]any{
			"invoice_id":   inv.ID,
			"amount_cents": amt,
			"method":       "wire",
		})
		if err != nil {
			t.Fatalf("payment %d: %v", i, err)
		}
		got := out.(map[string]any)["invoice"].(*Invoice)
		if i == 0 && got.Status != "open" {
			t.Errorf("after first partial: status=%q, want open", got.Status)
		}
		if i == 1 {
			if got.Status != "paid" {
				t.Errorf("after second covering payment: status=%q, want paid", got.Status)
			}
			if got.PaidAt == "" {
				t.Errorf("paid_at should be set")
			}
			if got.AmountPaidCents != 1000 {
				t.Errorf("amount_paid=%d, want 1000", got.AmountPaidCents)
			}
		}
	}
}

// As of v0.8.0, method='stripe' is accepted (webhook handler uses it;
// manual recording is allowed for off-platform Stripe activity). The
// (method, external_id) unique index handles idempotency for
// webhook re-deliveries.
func TestPayments_AcceptsStripeMethod(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("X", 1, 1000, 0)})
	mustFinalize(t, ctx, inv.ID)

	_, err := app.toolPaymentsRecord(ctx, map[string]any{
		"invoice_id":   inv.ID,
		"amount_cents": int64(1000),
		"method":       "stripe",
		"external_id":  "pi_test_abc",
	})
	if err != nil {
		t.Fatalf("method=stripe should be accepted in v0.8.0+: %v", err)
	}
}

// Idempotency: re-submitting the same (method, external_id) pair
// returns the existing payment instead of double-charging.
func TestPayments_StripeIdempotent(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "bob@acme.com", "Bob")
	inv := mustDraft(t, ctx, c.ID, []any{line("X", 1, 2000, 0)})
	mustFinalize(t, ctx, inv.ID)

	args := map[string]any{
		"invoice_id":   inv.ID,
		"amount_cents": int64(2000),
		"method":       "stripe",
		"external_id":  "pi_test_xyz",
	}
	if _, err := app.toolPaymentsRecord(ctx, args); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Re-deliver — must not double-record.
	if _, err := app.toolPaymentsRecord(ctx, args); err != nil {
		t.Fatalf("re-deliver: %v", err)
	}
	// Invoice should be paid exactly once (amount_paid_cents = 2000, not 4000).
	got, err := app.toolInvoicesGet(ctx, map[string]any{"id": inv.ID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, _ := got.(map[string]any)
	invMap, _ := out["invoice"].(*Invoice)
	if invMap == nil || invMap.AmountPaidCents != 2000 {
		t.Errorf("expected amount_paid_cents=2000 after idempotent re-deliver, got %d", func() int64 {
			if invMap == nil {
				return -1
			}
			return invMap.AmountPaidCents
		}())
	}
	if invMap != nil && invMap.Status != "paid" {
		t.Errorf("expected status=paid, got %q", invMap.Status)
	}
}

func TestPayments_RejectsOnDraftInvoice(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("X", 1, 1000, 0)})
	// Skip finalize on purpose.

	_, err := app.toolPaymentsRecord(ctx, map[string]any{
		"invoice_id":   inv.ID,
		"amount_cents": int64(1000),
		"method":       "wire",
	})
	if err == nil {
		t.Fatal("expected payment to be rejected on draft")
	}
}

// ─── Search ─────────────────────────────────────────────────────────

// ─── PDF tool ───────────────────────────────────────────────────────

func TestInvoicesRenderPDF_ReturnsBase64ByDefault(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	inv := mustDraft(t, ctx, c.ID, []any{line("Service", 1, 1000, 0)})
	mustFinalize(t, ctx, inv.ID)

	out, err := app.toolInvoicesRenderPDF(ctx, map[string]any{
		"invoice_id": inv.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["saved"] != false {
		t.Errorf("saved=%v, want false (default)", res["saved"])
	}
	if res["pdf_base64"] == nil || res["pdf_base64"] == "" {
		t.Error("pdf_base64 missing")
	}
	if size := res["size_bytes"].(int); size < 500 {
		t.Errorf("size_bytes=%d suspiciously small", size)
	}
	filename := res["filename"].(string)
	if !strings.HasSuffix(filename, ".pdf") {
		t.Errorf("filename=%q, expected .pdf suffix", filename)
	}
}

func TestInvoicesRenderPDF_NotFoundReturnsError(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	_, err := app.toolInvoicesRenderPDF(ctx, map[string]any{
		"invoice_id": int64(99999),
	})
	if err == nil {
		t.Fatal("expected error for missing invoice")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q should mention 'not found'", err.Error())
	}
}

func TestInvoicesRenderPDF_RequiresInvoiceID(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	_, err := app.toolInvoicesRenderPDF(ctx, map[string]any{})
	if err == nil {
		t.Fatal("expected error when invoice_id missing")
	}
}

func TestInvoicesSearch_FiltersByStatusAndCustomer(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c1 := mustCustomer(t, ctx, "alice@acme.com", "Alice")
	c2 := mustCustomer(t, ctx, "bob@globex.com", "Bob")
	mustDraft(t, ctx, c1.ID, []any{line("a", 1, 100, 0)})
	openInv := mustDraft(t, ctx, c1.ID, []any{line("b", 1, 200, 0)})
	mustFinalize(t, ctx, openInv.ID)
	mustDraft(t, ctx, c2.ID, []any{line("c", 1, 300, 0)})

	// All for c1.
	out, err := app.toolInvoicesSearch(ctx, map[string]any{"customer_id": c1.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["count"].(int); got != 2 {
		t.Errorf("c1 invoices = %d, want 2", got)
	}

	// Only open ones, project-wide.
	out2, _ := app.toolInvoicesSearch(ctx, map[string]any{"status": "open"})
	if got := out2.(map[string]any)["count"].(int); got != 1 {
		t.Errorf("open invoices = %d, want 1", got)
	}
}
