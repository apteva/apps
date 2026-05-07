package main

// Tier 1 — pure logic tests for the v0.1.2 OCR integration. No
// network, no sidecar, no Mindee. Tests the field-merge and vendor
// resolution algorithms; the live extract_invoice round-trip is
// covered by tier 3 scenarios with a real Mindee install.

import (
	"strings"
	"testing"
)

// ─── Field merge ────────────────────────────────────────────────────

func sampleExtraction() *ExtractedInvoice {
	e := &ExtractedInvoice{
		InvoiceNumber: "AWS-2026-04-001",
		IssueDate:     "2026-04-01",
		DueDate:       "2026-05-01",
		Currency:      "USD",
		SubtotalCents: 48000,
		TotalCents:    48000,
	}
	e.Vendor.Name = "AWS"
	e.Vendor.Email = "billing@aws.amazon.com"
	e.LineItems = []struct {
		Description    string  `json:"description"`
		Quantity       float64 `json:"quantity,omitempty"`
		UnitPriceCents int64   `json:"unit_price_cents,omitempty"`
		AmountCents    int64   `json:"amount_cents,omitempty"`
		TaxRateBps     int     `json:"tax_rate_bps,omitempty"`
		Confidence     float64 `json:"confidence,omitempty"`
	}{
		{Description: "EC2", Quantity: 1, UnitPriceCents: 48000, AmountCents: 48000},
	}
	return e
}

func TestMergeExtractedIntoArgs_FillsMissing(t *testing.T) {
	args := map[string]any{}
	filled := mergeExtractedIntoArgs(args, sampleExtraction())

	want := map[string]any{
		"vendor_invoice_number": "AWS-2026-04-001",
		"vendor_invoice_date":   "2026-04-01",
		"due_date":              "2026-05-01",
		"currency":              "USD",
	}
	for k, v := range want {
		if got, ok := args[k]; !ok || got != v {
			t.Errorf("args[%q]=%v, want %v", k, got, v)
		}
	}
	if _, ok := args["line_items"].([]any); !ok {
		t.Error("expected line_items filled")
	}
	for _, k := range []string{"vendor_invoice_number", "vendor_invoice_date", "due_date", "currency", "line_items"} {
		found := false
		for _, f := range filled {
			if f == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("filled list missing %q (got %v)", k, filled)
		}
	}
}

func TestMergeExtractedIntoArgs_CallerAlwaysWins(t *testing.T) {
	// Every key the extraction would fill is pre-populated by the caller.
	args := map[string]any{
		"vendor_invoice_number": "MY-OVERRIDE",
		"vendor_invoice_date":   "2026-12-31",
		"due_date":              "2099-01-01",
		"currency":              "EUR",
		"line_items":            []any{map[string]any{"description": "stub", "unit_price_cents": int64(1)}},
	}
	filled := mergeExtractedIntoArgs(args, sampleExtraction())
	if len(filled) != 0 {
		t.Errorf("expected nothing filled when caller pre-populated, got %v", filled)
	}
	if args["vendor_invoice_number"] != "MY-OVERRIDE" {
		t.Errorf("caller's vendor_invoice_number was overwritten")
	}
	if args["currency"] != "EUR" {
		t.Errorf("caller's currency was overwritten")
	}
}

func TestMergeExtractedIntoArgs_NilExtraction(t *testing.T) {
	args := map[string]any{"vendor_id": int64(7)}
	if filled := mergeExtractedIntoArgs(args, nil); filled != nil {
		t.Errorf("nil extraction should fill nothing, got %v", filled)
	}
}

func TestConvertExtractedLineItems_FallbackForMissingUnit(t *testing.T) {
	e := &ExtractedInvoice{}
	e.LineItems = []struct {
		Description    string  `json:"description"`
		Quantity       float64 `json:"quantity,omitempty"`
		UnitPriceCents int64   `json:"unit_price_cents,omitempty"`
		AmountCents    int64   `json:"amount_cents,omitempty"`
		TaxRateBps     int     `json:"tax_rate_bps,omitempty"`
		Confidence     float64 `json:"confidence,omitempty"`
	}{
		{Description: "Lump sum", AmountCents: 2500},
		{Description: "", AmountCents: 0}, // skipped — no fallback
	}
	got := convertExtractedLineItems(e)
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1 (the second is unsalvageable)", len(got))
	}
	row := got[0].(map[string]any)
	if row["unit_price_cents"].(int64) != 2500 {
		t.Errorf("expected unit fallback to AmountCents, got %v", row["unit_price_cents"])
	}
	if row["quantity"].(float64) != 1 {
		t.Errorf("expected quantity fallback to 1, got %v", row["quantity"])
	}
}

// ─── Vendor resolution ──────────────────────────────────────────────

func TestResolveVendor_CallerSuppliedSkips(t *testing.T) {
	ctx := newTestCtx(t)
	args := map[string]any{"vendor_id": int64(42)}
	via, err := resolveVendorFromExtraction(ctx.AppDB(), "test-proj", sampleExtraction(), args)
	if err != nil {
		t.Fatal(err)
	}
	if via != "" {
		t.Errorf("via=%q, want empty when caller supplied vendor_id", via)
	}
	if args["vendor_id"].(int64) != 42 {
		t.Error("caller's vendor_id was clobbered")
	}
}

func TestResolveVendor_ByEmail_UpsertExisting(t *testing.T) {
	ctx := newTestCtx(t)
	v := mustVendor(t, ctx, "billing@aws.amazon.com", "AWS")
	args := map[string]any{}

	via, err := resolveVendorFromExtraction(ctx.AppDB(), "test-proj", sampleExtraction(), args)
	if err != nil {
		t.Fatal(err)
	}
	if via != "email" {
		t.Errorf("via=%q, want email", via)
	}
	if got := args["vendor_id"].(int64); got != v.ID {
		t.Errorf("vendor_id=%d, want %d", got, v.ID)
	}
}

func TestResolveVendor_ByEmail_CreatesNew(t *testing.T) {
	ctx := newTestCtx(t)
	args := map[string]any{}
	via, err := resolveVendorFromExtraction(ctx.AppDB(), "test-proj", sampleExtraction(), args)
	if err != nil {
		t.Fatal(err)
	}
	if via != "email" {
		t.Errorf("via=%q, want email", via)
	}
	if args["vendor_id"] == nil {
		t.Error("vendor_id should have been set to the new vendor's id")
	}
}

func TestResolveVendor_UniqueNameMatch(t *testing.T) {
	ctx := newTestCtx(t)
	v := mustVendor(t, ctx, "ap@only-vendor.com", "Acme Corp")
	e := &ExtractedInvoice{}
	e.Vendor.Name = "Acme Corp" // no email in extraction

	args := map[string]any{}
	via, err := resolveVendorFromExtraction(ctx.AppDB(), "test-proj", e, args)
	if err != nil {
		t.Fatal(err)
	}
	if via != "name_unique" {
		t.Errorf("via=%q, want name_unique", via)
	}
	if args["vendor_id"].(int64) != v.ID {
		t.Errorf("vendor_id=%v, want %d", args["vendor_id"], v.ID)
	}
}

func TestResolveVendor_AmbiguousNameErrors(t *testing.T) {
	ctx := newTestCtx(t)
	mustVendor(t, ctx, "v1@x.com", "Acme")
	mustVendor(t, ctx, "v2@x.com", "Acme Industries")

	e := &ExtractedInvoice{}
	e.Vendor.Name = "Acme"
	args := map[string]any{}
	_, err := resolveVendorFromExtraction(ctx.AppDB(), "test-proj", e, args)
	if err == nil {
		t.Fatal("expected ambiguity error with 2 matches")
	}
	if !strings.Contains(err.Error(), "2 existing vendors") || !strings.Contains(err.Error(), "vendor_id") {
		t.Errorf("error %q should mention the count + vendor_id hint", err.Error())
	}
}

func TestResolveVendor_NoMatchAutoCreates(t *testing.T) {
	ctx := newTestCtx(t)
	e := &ExtractedInvoice{}
	e.Vendor.Name = "Brand New Vendor LLC"
	args := map[string]any{}

	via, err := resolveVendorFromExtraction(ctx.AppDB(), "test-proj", e, args)
	if err != nil {
		t.Fatal(err)
	}
	if via != "auto_created" {
		t.Errorf("via=%q, want auto_created", via)
	}
	if args["vendor_id"] == nil {
		t.Error("vendor_id should be set on auto-create")
	}
}

func TestResolveVendor_NoVendorInfoErrors(t *testing.T) {
	ctx := newTestCtx(t)
	e := &ExtractedInvoice{} // empty
	args := map[string]any{}
	_, err := resolveVendorFromExtraction(ctx.AppDB(), "test-proj", e, args)
	if err == nil {
		t.Fatal("expected error with no vendor info")
	}
	if !strings.Contains(err.Error(), "vendor_id") {
		t.Errorf("error %q should hint at vendor_id", err.Error())
	}
}

// ─── End-to-end fallback (no provider configured) ───────────────────

func TestBillsCreateFromFile_NoOCRProviderFallsThrough(t *testing.T) {
	// With no ocr_provider config set, callOCR returns nil/nil and
	// bills_create_from_file should behave exactly like v0.1.1: caller
	// must supply vendor_id + line_items, no auto-fill happens, no
	// "extracted" audit entry is written. Caught here to prevent a
	// regression where some half-merged extraction sneaks through.
	ctx := newTestCtx(t)
	app := &App{}
	v := mustVendor(t, ctx, "ap@acme.com", "Acme")

	// Without storage installed, bills_create_from_file rejects on
	// upload — same as v0.1.1. That's the test: the OCR layer doesn't
	// change v0.1.1's "needs storage" failure mode when ocr_provider
	// is empty.
	_, err := app.toolBillsCreateFromFile(ctx, map[string]any{
		"name":           "x.pdf",
		"content_base64": "JVBERg==",
		"vendor_id":      v.ID,
	})
	if err == nil {
		t.Fatal("expected upload to fail without storage")
	}
	if !strings.Contains(err.Error(), "storage") {
		t.Errorf("error %q should be the storage error, not OCR", err.Error())
	}
}
