//go:build integration

package main

// Tier 2 — the real binary, real HTTP. Boot the sidecar, talk MCP +
// REST. Validates SDK wiring (manifest at boot, migrations on disk,
// JSON-RPC dispatch, route mounting, /health, project resolution)
// end-to-end.
//
// Run with:  go test -tags integration ./...

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
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, string(resp.Body))
	}
	if got["ok"] != true {
		t.Errorf("/health body=%v", got)
	}
}

// One end-to-end happy path catches almost all wiring bugs:
// upsert customer → draft invoice → finalize → record payment → paid.
func TestSidecar_FullInvoiceLifecycleViaMCP(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// Upsert customer.
	r := sc.MCP("customers_upsert_by_email", map[string]any{
		"email": "alice@acme.com",
		"defaults": map[string]any{
			"name": "Alice Cooper",
		},
	})
	if r["was_created"] != true {
		t.Fatalf("expected was_created=true, got %#v", r["was_created"])
	}
	customer := r["customer"].(map[string]any)
	cid := int64(customer["id"].(float64))

	// Draft an invoice with one line.
	r2 := sc.MCP("invoices_create", map[string]any{
		"customer_id": cid,
		"line_items": []any{
			map[string]any{
				"description":      "Consulting",
				"quantity":         5,
				"unit_price_cents": 20000, // $200
				"tax_rate_bps":     0,
			},
		},
	})
	inv := r2["invoice"].(map[string]any)
	iid := int64(inv["id"].(float64))
	if inv["status"] != "draft" {
		t.Errorf("post-create status=%v, want draft", inv["status"])
	}
	if int64(inv["total_cents"].(float64)) != 100000 {
		t.Errorf("total_cents=%v, want 100000", inv["total_cents"])
	}

	// Add another line.
	r3 := sc.MCP("invoices_add_line_item", map[string]any{
		"invoice_id":       iid,
		"description":      "Travel",
		"quantity":         1,
		"unit_price_cents": 12500,
	})
	updated := r3["invoice"].(map[string]any)
	if int64(updated["total_cents"].(float64)) != 112500 {
		t.Errorf("after add_line_item total=%v, want 112500", updated["total_cents"])
	}

	// Finalize → mints number, status=open.
	r4 := sc.MCP("invoices_finalize", map[string]any{"invoice_id": iid})
	finalized := r4["invoice"].(map[string]any)
	if finalized["status"] != "open" {
		t.Errorf("post-finalize status=%v, want open", finalized["status"])
	}
	num, _ := finalized["number"].(string)
	if !strings.HasPrefix(num, "INV-") {
		t.Errorf("invoice number=%q should start with INV-", num)
	}

	// Idempotent re-finalize returns the same number.
	r5 := sc.MCP("invoices_finalize", map[string]any{"invoice_id": iid})
	if r5["invoice"].(map[string]any)["number"] != num {
		t.Errorf("re-finalize changed the number: %v vs %s",
			r5["invoice"].(map[string]any)["number"], num)
	}

	// Record a wire payment that fully covers it.
	r6 := sc.MCP("payments_record", map[string]any{
		"invoice_id":   iid,
		"amount_cents": 112500,
		"method":       "wire",
	})
	paid := r6["invoice"].(map[string]any)
	if paid["status"] != "paid" {
		t.Errorf("post-payment status=%v, want paid", paid["status"])
	}

	// Fetch via REST — the dashboard's path. Audit log + payment list
	// should be populated.
	var rest map[string]any
	resp := sc.GET("/invoices/"+itoa(iid), &rest)
	if resp.Status != 200 {
		t.Fatalf("REST GET /invoices/%d: %d body=%s", iid, resp.Status, string(resp.Body))
	}
	got := rest["invoice"].(map[string]any)
	if got["status"] != "paid" {
		t.Errorf("REST status=%v, want paid", got["status"])
	}
	pays, _ := got["payments"].([]any)
	if len(pays) != 1 {
		t.Errorf("payments via REST = %d, want 1", len(pays))
	}
	audit, _ := got["audit_log"].([]any)
	// Audit should contain at minimum: create, finalize, paid (3 entries).
	if len(audit) < 3 {
		t.Errorf("audit_log = %d entries, want ≥ 3", len(audit))
	}
}

func TestSidecar_ProviderStripeRejected_v010(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	// Customer first.
	r := sc.MCP("customers_upsert_by_email", map[string]any{"email": "x@y.com"})
	cid := int64(r["customer"].(map[string]any)["id"].(float64))

	// MCPRaw because we want to inspect the error envelope.
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "invoices_create",
		"arguments": map[string]any{
			"customer_id": cid,
			"provider":    "stripe",
		},
	})
	if err == nil {
		t.Fatal("expected provider=stripe to be rejected via MCP")
	}
	if !strings.Contains(err.Error(), "v0.1.1") {
		t.Errorf("error %q should mention v0.1.1", err.Error())
	}
}

func TestSidecar_VoidPaidInvoiceReturns400(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	r := sc.MCP("customers_upsert_by_email", map[string]any{"email": "alice@acme.com"})
	cid := int64(r["customer"].(map[string]any)["id"].(float64))

	r2 := sc.MCP("invoices_create", map[string]any{
		"customer_id": cid,
		"line_items": []any{
			map[string]any{
				"description":      "X",
				"quantity":         1,
				"unit_price_cents": 1000,
			},
		},
	})
	iid := int64(r2["invoice"].(map[string]any)["id"].(float64))
	sc.MCP("invoices_finalize", map[string]any{"invoice_id": iid})
	sc.MCP("payments_record", map[string]any{
		"invoice_id":   iid,
		"amount_cents": 1000,
		"method":       "wire",
	})

	// Now try to void it via REST — should be 400.
	var got map[string]any
	resp := sc.POST("/invoices/"+itoa(iid)+"/void", map[string]any{}, &got)
	if resp.Status != 400 {
		t.Fatalf("void on paid: status=%d body=%s, want 400", resp.Status, string(resp.Body))
	}
	if !strings.Contains(string(resp.Body), "paid") {
		t.Errorf("400 body should mention paid: %s", string(resp.Body))
	}
}

func TestSidecar_ProjectScopeIsolation(t *testing.T) {
	a := tk.SpawnSidecar(t, ".", tk.WithProjectID("proj-A"))
	a.MCP("customers_upsert_by_email", map[string]any{
		"email":    "a-only@x.com",
		"defaults": map[string]any{"name": "AOnly"},
	})
	out := a.MCP("customers_search", map[string]any{"q": "AOnly"})
	if out["count"].(float64) != 1 {
		t.Errorf("project A: expected 1, got %v", out["count"])
	}

	// Second sidecar gets its own temp DB. (Each spawn = isolated DB.)
	b := tk.SpawnSidecar(t, ".", tk.WithProjectID("proj-B"))
	out2 := b.MCP("customers_search", map[string]any{"q": "AOnly"})
	if out2["count"].(float64) != 0 {
		t.Errorf("project B: expected 0, got %v", out2["count"])
	}
}

func TestSidecar_GlobalScope_RequiresProjectIDPerCall(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".") // no APTEVA_PROJECT_ID = global scope
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name":      "customers_search",
		"arguments": map[string]any{"q": "x"},
	})
	if err == nil {
		t.Fatal("expected MCP error when scope=global and project_id is missing")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Errorf("error %q should mention project_id", err.Error())
	}

	// Same call with _project_id should work.
	out := sc.MCP("customers_search", map[string]any{
		"_project_id": "proj-X",
		"q":           "anything",
	})
	if out["count"].(float64) != 0 {
		t.Errorf("expected 0 results in fresh project, got %v", out["count"])
	}
}

// ─── PDF + print view ──────────────────────────────────────────────

func TestSidecar_PrintViewReturnsHTML(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// Set up an invoice to render.
	r := sc.MCP("customers_upsert_by_email", map[string]any{
		"email":    "alice@acme.com",
		"defaults": map[string]any{"name": "Acme Corp"},
	})
	cid := int64(r["customer"].(map[string]any)["id"].(float64))
	r2 := sc.MCP("invoices_create", map[string]any{
		"customer_id": cid,
		"line_items": []any{
			map[string]any{
				"description":      "Widgets",
				"quantity":         3,
				"unit_price_cents": 5000,
			},
		},
	})
	iid := int64(r2["invoice"].(map[string]any)["id"].(float64))
	sc.MCP("invoices_finalize", map[string]any{"invoice_id": iid})

	resp := sc.GET("/invoices/"+itoa(iid)+"/print", nil)
	if resp.Status != 200 {
		t.Fatalf("/print status=%d body=%s", resp.Status, string(resp.Body))
	}
	body := string(resp.Body)
	if !strings.HasPrefix(body, "<!doctype html>") {
		t.Error("expected <!doctype html> prefix")
	}
	if !strings.Contains(body, "Acme Corp") {
		t.Error("print view should include customer name")
	}
	if !strings.Contains(body, "@media print") {
		t.Error("print view should include print stylesheet")
	}
	// Toolbar with the print button is the user-facing affordance.
	if !strings.Contains(body, "Print / Save as PDF") {
		t.Error("print view missing the print button")
	}
}

func TestSidecar_PDFEndpointReturnsBytes(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	r := sc.MCP("customers_upsert_by_email", map[string]any{"email": "x@y.com"})
	cid := int64(r["customer"].(map[string]any)["id"].(float64))
	r2 := sc.MCP("invoices_create", map[string]any{
		"customer_id": cid,
		"line_items": []any{
			map[string]any{"description": "X", "quantity": 1, "unit_price_cents": 1000},
		},
	})
	iid := int64(r2["invoice"].(map[string]any)["id"].(float64))
	sc.MCP("invoices_finalize", map[string]any{"invoice_id": iid})

	resp := sc.GET("/invoices/"+itoa(iid)+"/pdf", nil)
	if resp.Status != 200 {
		t.Fatalf("/pdf status=%d body-len=%d", resp.Status, len(resp.Body))
	}
	if len(resp.Body) < 500 {
		t.Errorf("PDF body suspiciously small: %d bytes", len(resp.Body))
	}
	if !strings.HasPrefix(string(resp.Body[:4]), "%PDF") {
		t.Errorf("expected %%PDF magic, got %q", string(resp.Body[:8]))
	}
	// The PDF should also reference the invoice we just created — the
	// number we minted goes through fpdf's encoded text streams, but
	// the literal title metadata ("INV-…") shows up readable in the
	// uncompressed parts of small PDFs.
	if !strings.Contains(string(resp.Body), "INV-") {
		t.Error("expected PDF to mention the invoice number somewhere")
	}
}

func TestSidecar_PDFNotFound(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	resp := sc.GET("/invoices/99999/pdf", nil)
	if resp.Status != 404 {
		t.Errorf("/pdf for missing invoice: status=%d, want 404", resp.Status)
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
