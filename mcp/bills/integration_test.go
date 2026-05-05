//go:build integration

package main

// Tier 2 — boot the real binary, exercise REST + MCP end-to-end.

import (
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 || got["ok"] != true {
		t.Fatalf("/health status=%d body=%v", resp.Status, got)
	}
}

// One full lifecycle round-trip catches almost every wiring bug.
func TestSidecar_FullBillLifecycle(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// Vendor.
	r := sc.MCP("vendors_upsert_by_email", map[string]any{
		"email":    "billing@aws.amazon.com",
		"defaults": map[string]any{"name": "AWS"},
	})
	if r["was_created"] != true {
		t.Fatal("expected was_created=true")
	}
	vid := int64(r["vendor"].(map[string]any)["id"].(float64))

	// Bill.
	r2 := sc.MCP("bills_create", map[string]any{
		"vendor_id":             vid,
		"vendor_invoice_number": "AWS-2026-04-001",
		"line_items": []any{
			map[string]any{"description": "EC2", "quantity": 1, "unit_price_cents": 50000, "tax_rate_bps": 1000},
		},
	})
	bill := r2["bill"].(map[string]any)
	bid := int64(bill["id"].(float64))
	if bill["status"] != "received" {
		t.Errorf("post-create status=%v, want received", bill["status"])
	}
	if int64(bill["total_cents"].(float64)) != 55000 {
		t.Errorf("total=%v, want 55000", bill["total_cents"])
	}

	// Approve.
	r3 := sc.MCP("bills_approve", map[string]any{"bill_id": bid})
	if r3["bill"].(map[string]any)["status"] != "approved" {
		t.Errorf("post-approve status=%v", r3["bill"].(map[string]any)["status"])
	}

	// Schedule.
	r4 := sc.MCP("bills_schedule_payment", map[string]any{
		"bill_id":       bid,
		"scheduled_for": "2026-06-30T00:00:00Z",
		"method":        "ach",
	})
	if r4["bill"].(map[string]any)["status"] != "scheduled" {
		t.Errorf("post-schedule status=%v", r4["bill"].(map[string]any)["status"])
	}

	// Pay.
	r5 := sc.MCP("bill_payments_record", map[string]any{
		"bill_id":      bid,
		"amount_cents": 55000,
		"method":       "ach",
	})
	if r5["bill"].(map[string]any)["status"] != "paid" {
		t.Errorf("post-payment status=%v", r5["bill"].(map[string]any)["status"])
	}

	// REST round-trip — audit log should have at least 4 entries.
	var rest map[string]any
	resp := sc.GET("/bills/"+itoa(bid), &rest)
	if resp.Status != 200 {
		t.Fatalf("REST GET /bills/%d: %d body=%s", bid, resp.Status, string(resp.Body))
	}
	got := rest["bill"].(map[string]any)
	if got["status"] != "paid" {
		t.Errorf("REST status=%v", got["status"])
	}
	audit, _ := got["audit_log"].([]any)
	if len(audit) < 4 {
		t.Errorf("audit_log entries=%d, want ≥ 4 (create, approve, schedule, paid)", len(audit))
	}
}

func TestSidecar_ProviderNonLocalRejected(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	r := sc.MCP("vendors_upsert_by_email", map[string]any{"email": "x@y.com"})
	vid := int64(r["vendor"].(map[string]any)["id"].(float64))
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "bills_create",
		"arguments": map[string]any{
			"vendor_id": vid,
			"provider":  "mercury",
		},
	})
	if err == nil {
		t.Fatal("expected provider=mercury rejected")
	}
	if !strings.Contains(err.Error(), "v0.2") {
		t.Errorf("error %q should mention v0.2", err.Error())
	}
}

func TestSidecar_DuplicateBillRejected(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	r := sc.MCP("vendors_upsert_by_email", map[string]any{"email": "ap@acme.com"})
	vid := int64(r["vendor"].(map[string]any)["id"].(float64))
	sc.MCP("bills_create", map[string]any{
		"vendor_id":             vid,
		"vendor_invoice_number": "INV-DUP",
		"line_items":            []any{map[string]any{"description": "X", "unit_price_cents": 100}},
	})
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "bills_create",
		"arguments": map[string]any{
			"vendor_id":             vid,
			"vendor_invoice_number": "INV-DUP",
			"line_items":            []any{map[string]any{"description": "X", "unit_price_cents": 100}},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate rejected")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q should mention 'already exists'", err.Error())
	}
}

func TestSidecar_VoidPaidBillReturns400(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	r := sc.MCP("vendors_upsert_by_email", map[string]any{"email": "ap@acme.com"})
	vid := int64(r["vendor"].(map[string]any)["id"].(float64))
	r2 := sc.MCP("bills_create", map[string]any{
		"vendor_id":             vid,
		"vendor_invoice_number": "INV-1",
		"line_items":            []any{map[string]any{"description": "X", "unit_price_cents": 100}},
	})
	bid := int64(r2["bill"].(map[string]any)["id"].(float64))
	sc.MCP("bills_approve", map[string]any{"bill_id": bid})
	sc.MCP("bill_payments_record", map[string]any{
		"bill_id": bid, "amount_cents": 100, "method": "wire",
	})
	resp := sc.POST("/bills/"+itoa(bid)+"/void", map[string]any{}, nil)
	if resp.Status != 400 {
		t.Fatalf("void on paid: status=%d body=%s, want 400", resp.Status, string(resp.Body))
	}
}

func TestSidecar_PrintAndPDF(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	r := sc.MCP("vendors_upsert_by_email", map[string]any{
		"email": "ap@acme.com", "defaults": map[string]any{"name": "Acme"},
	})
	vid := int64(r["vendor"].(map[string]any)["id"].(float64))
	r2 := sc.MCP("bills_create", map[string]any{
		"vendor_id":             vid,
		"vendor_invoice_number": "INV-2026-0001",
		"line_items": []any{
			map[string]any{"description": "Service", "quantity": 1, "unit_price_cents": 1000},
		},
	})
	bid := int64(r2["bill"].(map[string]any)["id"].(float64))
	sc.MCP("bills_approve", map[string]any{"bill_id": bid})

	resp := sc.GET("/bills/"+itoa(bid)+"/print", nil)
	if resp.Status != 200 {
		t.Fatalf("/print status=%d", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "Acme") {
		t.Error("print view missing vendor name")
	}
	if !strings.Contains(string(resp.Body), "VOUCHER") {
		t.Error("print view should be marked VOUCHER")
	}

	resp2 := sc.GET("/bills/"+itoa(bid)+"/pdf", nil)
	if resp2.Status != 200 {
		t.Fatalf("/pdf status=%d", resp2.Status)
	}
	if !strings.HasPrefix(string(resp2.Body[:4]), "%PDF") {
		t.Errorf("expected %%PDF magic, got %q", string(resp2.Body[:8]))
	}
}

func TestSidecar_ProjectScopeIsolation(t *testing.T) {
	a := tk.SpawnSidecar(t, ".", tk.WithProjectID("proj-A"))
	a.MCP("vendors_upsert_by_email", map[string]any{
		"email":    "a-only@x.com",
		"defaults": map[string]any{"name": "AOnly"},
	})
	out := a.MCP("vendors_search", map[string]any{"q": "AOnly"})
	if out["count"].(float64) != 1 {
		t.Errorf("project A: expected 1, got %v", out["count"])
	}
	b := tk.SpawnSidecar(t, ".", tk.WithProjectID("proj-B"))
	out2 := b.MCP("vendors_search", map[string]any{"q": "AOnly"})
	if out2["count"].(float64) != 0 {
		t.Errorf("project B: expected 0, got %v", out2["count"])
	}
}

// ─── Attachments (v0.1.1) ───────────────────────────────────────────
//
// Tier 2 covers the error path when storage isn't installed alongside
// bills (the typical CI shape — bills sidecar runs alone). The
// happy-path cross-app round-trip is covered by tier 3 scenarios with
// a real storage app installed in the same project.

func TestSidecar_AttachLink_FailsWithoutStorage(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	r := sc.MCP("vendors_upsert_by_email", map[string]any{"email": "x@y.com"})
	vid := int64(r["vendor"].(map[string]any)["id"].(float64))
	r2 := sc.MCP("bills_create", map[string]any{
		"vendor_id": vid,
		"line_items": []any{
			map[string]any{"description": "X", "quantity": 1, "unit_price_cents": 100},
		},
	})
	bid := int64(r2["bill"].(map[string]any)["id"].(float64))

	// MCP path — should error on storage validation.
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name":      "bills_attach_file",
		"arguments": map[string]any{"bill_id": bid, "file_id": 42},
	})
	if err == nil {
		t.Fatal("expected attach to fail without storage")
	}
	if !strings.Contains(err.Error(), "storage") {
		t.Errorf("error %q should mention storage", err.Error())
	}
}

func TestSidecar_DetachIsIdempotentNoOp(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	r := sc.MCP("vendors_upsert_by_email", map[string]any{"email": "x@y.com"})
	vid := int64(r["vendor"].(map[string]any)["id"].(float64))
	r2 := sc.MCP("bills_create", map[string]any{
		"vendor_id": vid,
		"line_items": []any{
			map[string]any{"description": "X", "quantity": 1, "unit_price_cents": 100},
		},
	})
	bid := int64(r2["bill"].(map[string]any)["id"].(float64))

	// Detach when nothing's attached — succeeds, returns detached:false.
	r3 := sc.MCP("bills_detach_file", map[string]any{"bill_id": bid})
	if r3["detached"] != false {
		t.Errorf("detached=%v, want false on no-op", r3["detached"])
	}
}

func TestSidecar_BillIncludesAttachedFileIDInResponse(t *testing.T) {
	// Round-trip: bills_create with attached_file_id (we don't validate
	// here because we're testing the column wiring, not the storage
	// integration). The agent's normal path is bills_create_from_file
	// which DOES validate.
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	r := sc.MCP("vendors_upsert_by_email", map[string]any{"email": "x@y.com"})
	vid := int64(r["vendor"].(map[string]any)["id"].(float64))
	r2 := sc.MCP("bills_create", map[string]any{
		"vendor_id":        vid,
		"attached_file_id": 7, // fake but valid shape
		"line_items": []any{
			map[string]any{"description": "X", "quantity": 1, "unit_price_cents": 100},
		},
	})
	bill := r2["bill"].(map[string]any)
	if int64(bill["attached_file_id"].(float64)) != 7 {
		t.Errorf("attached_file_id=%v, want 7", bill["attached_file_id"])
	}

	// REST should also surface it.
	bid := int64(bill["id"].(float64))
	var rest map[string]any
	resp := sc.GET("/bills/"+itoa(bid), &rest)
	if resp.Status != 200 {
		t.Fatalf("/bills/%d: %d", bid, resp.Status)
	}
	got := rest["bill"].(map[string]any)
	if int64(got["attached_file_id"].(float64)) != 7 {
		t.Errorf("REST attached_file_id=%v, want 7", got["attached_file_id"])
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
