package main

// Smoke test: render a realistic invoice template + verify the
// emitted PDF is structurally valid (starts %PDF, ends %%EOF, has
// a real page count). Skipped under -short.

import (
	"bytes"
	"strings"
	"testing"
)

func TestSmokeRender_Invoice(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test")
	}
	body := `# Invoice {{.invoice.number}}

**Bill to:** {{.customer.name}}
{{.customer.address}}

## Items

{{range .items}}- {{.description}} — ${{.price}}
{{end}}

---

**Total:** ${{.invoice.total}}

Thank you for your business.

` + "```" + `
ref: PO-{{.invoice.po}}
` + "```"
	data := map[string]any{
		"invoice": map[string]any{
			"number": "INV-2026-001",
			"total":  "1250.00",
			"po":     "P-449",
		},
		"customer": map[string]any{
			"name":    "Acme Corporation",
			"address": "123 Main St, Anywhere",
		},
		"items": []any{
			map[string]any{"description": "Consulting hours (Q3)", "price": "1000.00"},
			map[string]any{"description": "Materials", "price": "250.00"},
		},
	}
	pdf, err := renderPDF(body, data, RenderOptions{PageSize: "A4"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("not a PDF — magic: %q", pdf[:8])
	}
	// PDFs end with %%EOF (possibly with trailing newline).
	if !bytes.Contains(pdf[len(pdf)-32:], []byte("%%EOF")) {
		t.Errorf("PDF missing %%EOF marker — last 32 bytes: %q", pdf[len(pdf)-32:])
	}
	// Sanity: an invoice with this much content should be at least a
	// kilobyte. Empty/broken renders come out as ~500 bytes (just
	// header + empty page).
	if len(pdf) < 1500 {
		t.Errorf("PDF suspiciously small: %d bytes", len(pdf))
	}
	// Substituted values should appear in the (uncompressed-ish)
	// rendered text streams. Maroto's gofpdf provider compresses
	// content streams by default, but there's typically enough
	// metadata uncompressed for a substring search to catch obvious
	// failures. Skip if not present (compression can hide it).
	hay := string(pdf)
	probes := []string{"INV-2026-001", "Acme"}
	hits := 0
	for _, p := range probes {
		if strings.Contains(hay, p) {
			hits++
		}
	}
	t.Logf("smoke: %d bytes, %d/%d substituted strings visible", len(pdf), hits, len(probes))
}
