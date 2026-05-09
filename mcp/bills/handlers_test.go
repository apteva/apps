package main

// Tier 1 — every MCP tool handler exercised against an in-memory
// SQLite. Covers vendor CRUD, the bill state machine, payment
// transitions, the reject-vs-void distinction, the duplicate-entry
// guard on (vendor, vendor_invoice_number), and the W-9 gate.

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Helpers ────────────────────────────────────────────────────────

func newTestCtx(t *testing.T, opts ...tk.Option) *sdk.AppCtx {
	t.Helper()
	full := append([]tk.Option{tk.WithProjectID("test-proj")}, opts...)
	ctx := tk.NewAppCtx(t, "apteva.yaml", full...)
	globalCtx = ctx
	return ctx
}

func mustVendor(t *testing.T, ctx *sdk.AppCtx, email, name string) *Vendor {
	t.Helper()
	app := &App{}
	out, err := app.toolVendorsUpsertByEmail(ctx, map[string]any{
		"email":    email,
		"defaults": map[string]any{"name": name},
	})
	if err != nil {
		t.Fatalf("upsert vendor: %v", err)
	}
	return out.(map[string]any)["vendor"].(*Vendor)
}

func mustBill(t *testing.T, ctx *sdk.AppCtx, vendorID int64, invNum string, lines []any) *Bill {
	t.Helper()
	app := &App{}
	out, err := app.toolBillsCreate(ctx, map[string]any{
		"vendor_id":             vendorID,
		"vendor_invoice_number": invNum,
		"line_items":            lines,
	})
	if err != nil {
		t.Fatalf("create bill: %v", err)
	}
	return out.(map[string]any)["bill"].(*Bill)
}

func mustApprove(t *testing.T, ctx *sdk.AppCtx, id int64) *Bill {
	t.Helper()
	app := &App{}
	out, err := app.toolBillsApprove(ctx, map[string]any{"bill_id": id})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	return out.(map[string]any)["bill"].(*Bill)
}

func line(desc string, qty float64, unit int64, taxBps int) map[string]any {
	return map[string]any{
		"description":      desc,
		"quantity":         qty,
		"unit_price_cents": unit,
		"tax_rate_bps":     taxBps,
	}
}

// ─── Vendors ────────────────────────────────────────────────────────

func TestVendorUpsertByEmail_CreatesThenDedupes(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	out1, err := app.toolVendorsUpsertByEmail(ctx, map[string]any{
		"email":    "billing@aws.amazon.com",
		"defaults": map[string]any{"name": "AWS"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r1 := out1.(map[string]any)
	if r1["was_created"] != true {
		t.Error("expected was_created=true")
	}
	v1 := r1["vendor"].(*Vendor)

	out2, err := app.toolVendorsUpsertByEmail(ctx, map[string]any{
		"email": "BILLING@aws.amazon.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	r2 := out2.(map[string]any)
	if r2["was_created"] != false {
		t.Error("expected was_created=false on dedupe")
	}
	if r2["vendor"].(*Vendor).ID != v1.ID {
		t.Error("expected same vendor id on dedupe")
	}
}

func TestVendorUpdate_PaymentTermsAndW9(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	_, err := app.toolVendorsUpdate(ctx, map[string]any{
		"id": v.ID,
		"patch": map[string]any{
			"default_payment_terms_days": 45,
			"default_payment_method":     "ach",
			"w9_received_at":             "2026-01-15T00:00:00Z",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := app.toolVendorsGet(ctx, map[string]any{"id": v.ID})
	got := out.(map[string]any)["vendor"].(*Vendor)
	if got.DefaultPaymentTermsDays == nil || *got.DefaultPaymentTermsDays != 45 {
		t.Errorf("DefaultPaymentTermsDays = %v, want 45", got.DefaultPaymentTermsDays)
	}
	if got.DefaultPaymentMethod != "ach" {
		t.Errorf("default_payment_method=%q", got.DefaultPaymentMethod)
	}
	if got.W9ReceivedAt == "" {
		t.Errorf("w9_received_at not persisted")
	}
}

func TestVendorMerge_ReassignsBillsAndPayments(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	loser := mustVendor(t, ctx, "ap@acme-old.com", "Acme (old)")
	winner := mustVendor(t, ctx, "ap@acme-new.com", "Acme (new)")
	bill := mustBill(t, ctx, loser.ID, "INV-1", []any{line("X", 1, 1000, 0)})
	mustApprove(t, ctx, bill.ID)
	_, err := app.toolBillPaymentsRecord(ctx, map[string]any{
		"bill_id":      bill.ID,
		"amount_cents": int64(1000),
		"method":       "wire",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolVendorsMerge(ctx, map[string]any{
		"loser_id":  loser.ID,
		"winner_id": winner.ID,
	}); err != nil {
		t.Fatal(err)
	}
	gotBill, _ := app.toolBillsGet(ctx, map[string]any{"id": bill.ID})
	b := gotBill.(map[string]any)["bill"].(*Bill)
	if b.VendorID != winner.ID {
		t.Errorf("bill vendor_id = %d, want %d", b.VendorID, winner.ID)
	}
	pays, _ := app.toolBillPaymentsList(ctx, map[string]any{"vendor_id": winner.ID})
	if got := pays.(map[string]any)["count"].(int); got != 1 {
		t.Errorf("winner payment count = %d, want 1", got)
	}
}

func TestVendorContext_LifetimeSpend(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	bill := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 5000, 0)})
	mustApprove(t, ctx, bill.ID)
	_, _ = app.toolBillPaymentsRecord(ctx, map[string]any{
		"bill_id":      bill.ID,
		"amount_cents": int64(2000),
		"method":       "wire",
	})

	out, err := app.toolVendorsGetContext(ctx, map[string]any{"id": v.ID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	lt := res["lifetime"].(map[string]any)
	if got := lt["billed_cents"].(int64); got != 5000 {
		t.Errorf("billed_cents=%d, want 5000", got)
	}
	if got := lt["paid_cents"].(int64); got != 2000 {
		t.Errorf("paid_cents=%d, want 2000", got)
	}
	if got := lt["outstanding_cents"].(int64); got != 3000 {
		t.Errorf("outstanding_cents=%d, want 3000", got)
	}
}

// ─── Bills: provider + currency gates ───────────────────────────────

func TestBillCreate_RejectsNonLocalProvider_v010(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	_, err := app.toolBillsCreate(ctx, map[string]any{
		"vendor_id": v.ID,
		"provider":  "mercury",
	})
	if err == nil {
		t.Fatal("expected mercury provider rejected in v0.1.0")
	}
	if !strings.Contains(err.Error(), "v0.2") {
		t.Errorf("error %q should mention v0.2", err.Error())
	}
}

func TestBillCreate_RejectsBadCurrency(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	_, err := app.toolBillsCreate(ctx, map[string]any{
		"vendor_id": v.ID,
		"currency":  "DOLLARS",
	})
	if err == nil {
		t.Fatal("expected bad currency rejected")
	}
}

// ─── Bills: state machine ───────────────────────────────────────────

func TestBillsLifecycle_HappyPath(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-2026-0001", []any{
		line("Service", 1, 10000, 1000),
	})
	if b.Status != "received" {
		t.Errorf("post-create status=%q, want received", b.Status)
	}
	if b.SubtotalCents != 10000 || b.TaxCents != 1000 || b.TotalCents != 11000 {
		t.Errorf("totals: %d/%d/%d", b.SubtotalCents, b.TaxCents, b.TotalCents)
	}

	// approve
	approved := mustApprove(t, ctx, b.ID)
	if approved.Status != "approved" {
		t.Errorf("status=%q, want approved", approved.Status)
	}
	if approved.ApprovedAt == "" {
		t.Error("approved_at should be set")
	}

	// schedule
	out, err := app.toolBillsSchedulePayment(ctx, map[string]any{
		"bill_id":       b.ID,
		"scheduled_for": "2026-06-30T00:00:00Z",
		"method":        "wire",
	})
	if err != nil {
		t.Fatal(err)
	}
	scheduled := out.(map[string]any)["bill"].(*Bill)
	if scheduled.Status != "scheduled" {
		t.Errorf("status=%q, want scheduled", scheduled.Status)
	}

	// pay
	out, err = app.toolBillPaymentsRecord(ctx, map[string]any{
		"bill_id":      b.ID,
		"amount_cents": int64(11000),
		"method":       "wire",
	})
	if err != nil {
		t.Fatal(err)
	}
	paid := out.(map[string]any)["bill"].(*Bill)
	if paid.Status != "paid" {
		t.Errorf("status=%q, want paid", paid.Status)
	}
	if paid.PaidAt == "" {
		t.Error("paid_at should be set")
	}
}

func TestBillsApprove_Idempotent(t *testing.T) {
	ctx := newTestCtx(t)
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})
	a1 := mustApprove(t, ctx, b.ID)
	a2 := mustApprove(t, ctx, b.ID)
	if a1.ApprovedAt != a2.ApprovedAt {
		t.Error("re-approve should be idempotent (no new approved_at)")
	}
}

func TestBillsApprove_RejectsEmptyBill(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	empty := mustBill(t, ctx, v.ID, "INV-1", nil)
	_, err := app.toolBillsApprove(ctx, map[string]any{"bill_id": empty.ID})
	if err == nil {
		t.Fatal("expected error: cannot approve empty bill")
	}
}

func TestBillsUpdate_LockedAfterApproval(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})
	mustApprove(t, ctx, b.ID)
	_, err := app.toolBillsUpdate(ctx, map[string]any{
		"id": b.ID,
		"patch": map[string]any{
			"notes": "trying to edit after approval",
		},
	})
	if err == nil {
		t.Fatal("expected update rejected after approval")
	}
	if !strings.Contains(err.Error(), "received") {
		t.Errorf("error %q should mention 'received'", err.Error())
	}
}

func TestBillsUpdate_ReplacesLineItems_RecomputesTotals(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("first", 1, 1000, 0)})
	if b.TotalCents != 1000 {
		t.Fatalf("initial total=%d", b.TotalCents)
	}
	out, err := app.toolBillsUpdate(ctx, map[string]any{
		"id": b.ID,
		"patch": map[string]any{
			"line_items": []any{
				line("replacement", 2, 2500, 0), // 5000
				line("more", 1, 750, 1000),      // 750 + 75 tax
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["bill"].(*Bill)
	if got.SubtotalCents != 5750 || got.TaxCents != 75 || got.TotalCents != 5825 {
		t.Errorf("totals: %d/%d/%d", got.SubtotalCents, got.TaxCents, got.TotalCents)
	}
	if len(got.LineItems) != 2 {
		t.Errorf("line_items=%d, want 2", len(got.LineItems))
	}
}

// ─── Reject vs void ─────────────────────────────────────────────────

func TestBillsReject_RequiresReason(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})
	_, err := app.toolBillsReject(ctx, map[string]any{"bill_id": b.ID})
	if err == nil {
		t.Fatal("expected reject without reason rejected")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("error %q should mention 'reason'", err.Error())
	}
}

func TestBillsReject_TransitionsToDisputed(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})
	out, err := app.toolBillsReject(ctx, map[string]any{
		"bill_id": b.ID,
		"reason":  "duplicate of INV-2026-0001",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["bill"].(*Bill)
	if got.Status != "disputed" {
		t.Errorf("status=%q, want disputed", got.Status)
	}
	if got.DisputedAt == "" {
		t.Error("disputed_at should be set")
	}
}

func TestBillsVoid_RejectsOnPaid(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 1000, 0)})
	mustApprove(t, ctx, b.ID)
	_, _ = app.toolBillPaymentsRecord(ctx, map[string]any{
		"bill_id":      b.ID,
		"amount_cents": int64(1000),
		"method":       "wire",
	})
	_, err := app.toolBillsVoid(ctx, map[string]any{"bill_id": b.ID})
	if err == nil {
		t.Fatal("expected void rejected on paid bill")
	}
	if !strings.Contains(err.Error(), "paid") {
		t.Errorf("error %q should mention 'paid'", err.Error())
	}
}

// ─── Duplicate-entry guard ──────────────────────────────────────────

func TestBillCreate_DuplicateVendorInvoiceNumberRejected(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	mustBill(t, ctx, v.ID, "INV-DUP", []any{line("X", 1, 100, 0)})
	_, err := app.toolBillsCreate(ctx, map[string]any{
		"vendor_id":             v.ID,
		"vendor_invoice_number": "INV-DUP",
		"line_items":            []any{line("X", 1, 100, 0)},
	})
	if err == nil {
		t.Fatal("expected duplicate (vendor, invoice number) rejected")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q should mention 'already exists'", err.Error())
	}
}

// ─── Payments ───────────────────────────────────────────────────────

func TestPayments_PartialKeepsApproved(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 1000, 0)})
	mustApprove(t, ctx, b.ID)
	out, err := app.toolBillPaymentsRecord(ctx, map[string]any{
		"bill_id":      b.ID,
		"amount_cents": int64(400),
		"method":       "wire",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["bill"].(*Bill)
	if got.Status != "approved" {
		t.Errorf("status=%q, want approved (partial payment shouldn't transition)", got.Status)
	}
	if got.AmountPaidCents != 400 {
		t.Errorf("amount_paid=%d, want 400", got.AmountPaidCents)
	}
}

func TestPayments_RejectsExternalRailMethod(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})
	mustApprove(t, ctx, b.ID)
	_, err := app.toolBillPaymentsRecord(ctx, map[string]any{
		"bill_id":      b.ID,
		"amount_cents": int64(100),
		"method":       "external_rail",
	})
	if err == nil {
		t.Fatal("expected external_rail rejected")
	}
	if !strings.Contains(err.Error(), "v0.2") {
		t.Errorf("error %q should mention v0.2", err.Error())
	}
}

func TestPayments_RejectsOnReceivedBill(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})
	// skip approve
	_, err := app.toolBillPaymentsRecord(ctx, map[string]any{
		"bill_id":      b.ID,
		"amount_cents": int64(100),
		"method":       "wire",
	})
	if err == nil {
		t.Fatal("expected payment rejected on received (unapproved) bill")
	}
}

// ─── Search ─────────────────────────────────────────────────────────

func TestBillsSearch_FiltersByStatusAndVendor(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v1 := mustVendor(t, ctx, "ap@acme.com", "Acme")
	v2 := mustVendor(t, ctx, "ap@globex.com", "Globex")
	mustBill(t, ctx, v1.ID, "A1", []any{line("a", 1, 100, 0)})
	approved := mustBill(t, ctx, v1.ID, "A2", []any{line("b", 1, 200, 0)})
	mustApprove(t, ctx, approved.ID)
	mustBill(t, ctx, v2.ID, "B1", []any{line("c", 1, 300, 0)})

	out, _ := app.toolBillsSearch(ctx, map[string]any{"vendor_id": v1.ID})
	if got := out.(map[string]any)["count"].(int); got != 2 {
		t.Errorf("v1 bills=%d, want 2", got)
	}
	out2, _ := app.toolBillsSearch(ctx, map[string]any{"status": "approved"})
	if got := out2.(map[string]any)["count"].(int); got != 1 {
		t.Errorf("approved bills=%d, want 1", got)
	}
}

// ─── Render PDF ─────────────────────────────────────────────────────

func TestBillsRenderPDF_ReturnsBase64(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 1000, 0)})
	mustApprove(t, ctx, b.ID)
	out, err := app.toolBillsRenderPDF(ctx, map[string]any{"bill_id": b.ID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["saved"] != false {
		t.Errorf("saved=%v, want false", res["saved"])
	}
	if res["pdf_base64"] == nil || res["pdf_base64"] == "" {
		t.Error("pdf_base64 missing")
	}
	if size := res["size_bytes"].(int); size < 500 {
		t.Errorf("size_bytes=%d suspiciously small", size)
	}
}

// ─── Attachments (v0.1.1) ───────────────────────────────────────────
//
// These exercise the DB layer directly rather than through the MCP
// tool — the tool calls into ctx.PlatformAPI() to validate the file
// exists in storage, which testkit doesn't stub. Tier 3 scenarios
// cover the cross-app path end-to-end with a real storage app.

func TestBillAttach_LinksAndAudits(t *testing.T) {
	ctx := newTestCtx(t)
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})

	bill, prevID, err := dbBillAttachFile(ctx.AppDB(), "test-proj", b.ID, 42, "agent:test")
	if err != nil {
		t.Fatal(err)
	}
	if bill.AttachedFileID == nil || *bill.AttachedFileID != 42 {
		t.Errorf("attached_file_id = %v, want 42", bill.AttachedFileID)
	}
	if prevID != 0 {
		t.Errorf("prevID=%d on first attach, want 0", prevID)
	}
	// Audit log includes the attach.
	var sawAttach bool
	for _, a := range bill.AuditLog {
		if a.Action == "attach" {
			sawAttach = true
			break
		}
	}
	if !sawAttach {
		t.Error("audit_log should include 'attach' action")
	}
}

func TestBillAttach_ReplacesAndAuditsBothIDs(t *testing.T) {
	ctx := newTestCtx(t)
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})

	if _, _, err := dbBillAttachFile(ctx.AppDB(), "test-proj", b.ID, 42, "agent:test"); err != nil {
		t.Fatal(err)
	}
	bill2, prevID, err := dbBillAttachFile(ctx.AppDB(), "test-proj", b.ID, 99, "agent:test")
	if err != nil {
		t.Fatal(err)
	}
	if prevID != 42 {
		t.Errorf("prevID=%d on replace, want 42", prevID)
	}
	if bill2.AttachedFileID == nil || *bill2.AttachedFileID != 99 {
		t.Errorf("attached_file_id=%v, want 99", bill2.AttachedFileID)
	}
	// Audit should record a 'replace' with both ids.
	var sawReplace bool
	for _, a := range bill2.AuditLog {
		if a.Action == "replace" && strings.Contains(string(a.Details), "42") {
			sawReplace = true
			break
		}
	}
	if !sawReplace {
		t.Error("audit_log should include 'replace' action with previous id 42")
	}
}

func TestBillAttach_RejectsOnVoid(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})
	if _, err := app.toolBillsVoid(ctx, map[string]any{"bill_id": b.ID}); err != nil {
		t.Fatal(err)
	}
	_, _, err := dbBillAttachFile(ctx.AppDB(), "test-proj", b.ID, 42, "agent:test")
	if err == nil {
		t.Fatal("expected attach to fail on voided bill")
	}
	if !strings.Contains(err.Error(), "void") {
		t.Errorf("error %q should mention void", err.Error())
	}
}

func TestBillDetach_ClearsAndAudits(t *testing.T) {
	ctx := newTestCtx(t)
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})

	if _, _, err := dbBillAttachFile(ctx.AppDB(), "test-proj", b.ID, 42, "agent:test"); err != nil {
		t.Fatal(err)
	}
	bill, prevID, err := dbBillDetachFile(ctx.AppDB(), "test-proj", b.ID, "agent:test")
	if err != nil {
		t.Fatal(err)
	}
	if prevID != 42 {
		t.Errorf("prevID=%d, want 42", prevID)
	}
	if bill.AttachedFileID != nil {
		t.Errorf("attached_file_id should be nil after detach, got %v", bill.AttachedFileID)
	}
	var sawDetach bool
	for _, a := range bill.AuditLog {
		if a.Action == "detach" {
			sawDetach = true
			break
		}
	}
	if !sawDetach {
		t.Error("audit_log should include 'detach' action")
	}
}

func TestBillDetach_IdempotentOnEmpty(t *testing.T) {
	ctx := newTestCtx(t)
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})

	bill, prevID, err := dbBillDetachFile(ctx.AppDB(), "test-proj", b.ID, "agent:test")
	if err != nil {
		t.Fatal(err)
	}
	if prevID != 0 {
		t.Errorf("prevID=%d on no-op detach, want 0", prevID)
	}
	if bill.AttachedFileID != nil {
		t.Errorf("AttachedFileID should stay nil")
	}
	// No detach audit entry for the no-op.
	for _, a := range bill.AuditLog {
		if a.Action == "detach" {
			t.Error("idempotent detach should not produce audit entry")
		}
	}
}

func TestBillsAttachFile_RejectsWithoutStorage(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	b := mustBill(t, ctx, v.ID, "INV-1", []any{line("X", 1, 100, 0)})
	// testkit ctx has no PlatformAPI → storageFileExists returns the
	// "storage not installed" error.
	_, err := app.toolBillsAttachFile(ctx, map[string]any{
		"bill_id": b.ID, "file_id": int64(42),
	})
	if err == nil {
		t.Fatal("expected attach to fail without storage")
	}
	if !strings.Contains(err.Error(), "storage") {
		t.Errorf("error %q should mention storage", err.Error())
	}
}

func TestBillsCreateFromFile_RejectsWithoutStorage(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")
	_, err := app.toolBillsCreateFromFile(ctx, map[string]any{
		"name":           "test.pdf",
		"content_base64": "JVBERg==", // "%PDF" base64
		"vendor_id":      v.ID,
	})
	if err == nil {
		t.Fatal("expected create_from_file to fail without storage")
	}
	if !strings.Contains(err.Error(), "storage") {
		t.Errorf("error %q should mention storage", err.Error())
	}
}

// ─── Project scope ──────────────────────────────────────────────────

func TestUpsert_RejectsWithoutProjectID(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	globalCtx = ctx
	app := &App{}
	_, err := app.toolVendorsUpsertByEmail(ctx, map[string]any{
		"email": "x@y.com",
	})
	if err == nil {
		t.Fatal("expected project_id error")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Errorf("error %q should mention project_id", err.Error())
	}
}

// dbBillCreate previously always recomputed totals from line items
// — bad when OCR pulls correct header totals but only a subset of
// line items (typical on multi-page or VAT-summary-style invoices).
// v0.1.10 makes caller-supplied header totals take precedence.
func TestBillCreate_HeaderTotalsOverrideLineItemsSum(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@aws.example", "AWS")
	out, err := app.toolBillsCreate(ctx, map[string]any{
		"vendor_id": v.ID,
		"line_items": []any{
			line("Route 53", 1, 60, 0),
			line("SES", 1, 38, 0),
		},
		"subtotal_cents": int64(7859),
		"tax_cents":      int64(1650),
		"total_cents":    int64(9509),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["bill"].(*Bill)
	if got.SubtotalCents != 7859 || got.TaxCents != 1650 || got.TotalCents != 9509 {
		t.Errorf("header totals not preserved: sub=%d tax=%d total=%d (want 7859/1650/9509)",
			got.SubtotalCents, got.TaxCents, got.TotalCents)
	}
}

func TestBillCreate_NoHeaderTotalsFallsBackToLineItemsSum(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@x.example", "X")
	out, err := app.toolBillsCreate(ctx, map[string]any{
		"vendor_id": v.ID,
		"line_items": []any{
			line("A", 1, 1000, 1000),
			line("B", 2, 500, 0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["bill"].(*Bill)
	if got.SubtotalCents != 2000 || got.TaxCents != 100 || got.TotalCents != 2100 {
		t.Errorf("computed totals wrong: sub=%d tax=%d total=%d (want 2000/100/2100)",
			got.SubtotalCents, got.TaxCents, got.TotalCents)
	}
}

func TestBillCreate_PartialHeaderTotalsBackfills(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@y.example", "Y")
	out, err := app.toolBillsCreate(ctx, map[string]any{
		"vendor_id":   v.ID,
		"line_items":  []any{line("X", 1, 100, 0)},
		"total_cents": int64(9509),
		"tax_cents":   int64(1650),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["bill"].(*Bill)
	if got.SubtotalCents != 7859 {
		t.Errorf("subtotal backfill = %d, want 7859 (= 9509 - 1650)", got.SubtotalCents)
	}
}
