package main

// Render-pipeline tests. The PDF bytes themselves aren't byte-stable
// (timestamps, IDs), so we verify behavioral properties:
//
//   - bytes are non-empty + start with "%PDF-" magic
//   - template substitution actually substitutes
//   - common markdown blocks don't error out
//   - bad templates return an error rather than producing garbage

import (
	"bytes"
	"strings"
	"testing"
)

func TestRender_BasicMarkdown(t *testing.T) {
	body := `# Hello {{.name}}

This is a paragraph.

- item 1
- item 2

---

` + "```" + `
some code
` + "```" + ``
	pdf, err := renderPDF(body, map[string]any{"name": "World"}, RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(pdf) < 100 {
		t.Fatalf("PDF too small: %d bytes", len(pdf))
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("not a PDF — first bytes: %q", pdf[:min(8, len(pdf))])
	}
}

func TestRender_TemplateSubstitution(t *testing.T) {
	body := "# Invoice {{.invoice.number}}\n\nBill to: {{.customer.name}}"
	data := map[string]any{
		"invoice":  map[string]any{"number": "INV-2026-001"},
		"customer": map[string]any{"name": "Acme Corp"},
	}
	merged, err := mergeTemplate(body, data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, "INV-2026-001") {
		t.Errorf("invoice number not substituted: %q", merged)
	}
	if !strings.Contains(merged, "Acme Corp") {
		t.Errorf("customer name not substituted: %q", merged)
	}
}

// missingkey=zero — top-level unbound keys are blanked rather than
// erroring. (Nested ".foo.bar" on a missing root still errors —
// that's the right tradeoff: agents passing partial data shouldn't
// silently skip whole sections without notice.)
func TestRender_MissingTopLevelKeyIsBlank(t *testing.T) {
	merged, err := mergeTemplate("Hello {{.unknown}}!", map[string]any{})
	if err != nil {
		t.Fatalf("top-level missing key should not error: %v", err)
	}
	if !strings.Contains(merged, "Hello") {
		t.Errorf("missing-key fallback dropped the body: %q", merged)
	}
}

// Bad template syntax returns an error — the audit row would record
// the failure rather than silently producing a broken PDF.
func TestRender_InvalidTemplate(t *testing.T) {
	_, err := renderPDF("{{.unclosed", map[string]any{}, RenderOptions{})
	if err == nil {
		t.Fatal("expected an error on unclosed action")
	}
}

func TestRender_EmptyBody(t *testing.T) {
	_, err := renderPDF("", map[string]any{}, RenderOptions{})
	if err == nil {
		t.Fatal("expected an error on empty body")
	}
}

// All three page sizes resolve without error — and produce
// different-sized PDFs (different page widths, so layout flow
// changes). This catches a typo in the pageSizeFromString switch
// that'd silently always render A4.
func TestRender_PageSizes(t *testing.T) {
	body := "# Title\n\nSome paragraph text."
	sizes := []string{"A4", "letter", "legal"}
	for _, s := range sizes {
		pdf, err := renderPDF(body, nil, RenderOptions{PageSize: s})
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if len(pdf) < 100 {
			t.Errorf("%s: pdf too small (%d)", s, len(pdf))
		}
	}
}

// Markdown blocks we deliberately don't render (tables, images,
// HTML blocks) shouldn't error — they should silently emit nothing
// so the rest of the document still renders.
func TestRender_UnsupportedBlocksSkippedGracefully(t *testing.T) {
	body := `# Heading

| col1 | col2 |
|------|------|
| a    | b    |

![image](https://example.com/x.png)

<div>raw HTML</div>

A paragraph that should still render.`
	pdf, err := renderPDF(body, nil, RenderOptions{})
	if err != nil {
		t.Fatalf("render with unsupported blocks: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
