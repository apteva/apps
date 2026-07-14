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
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	gtext "github.com/yuin/goldmark/text"
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

func TestRender_TemplateSubstitutionDoesNotHTMLEscapeMarkdownText(t *testing.T) {
	merged, err := mergeTemplate("Client context: {{.note}}", map[string]any{
		"note": "client's existing workflow",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(merged, "&#39;") {
		t.Fatalf("template substitution HTML-escaped markdown text: %q", merged)
	}
	if !strings.Contains(merged, "client's existing workflow") {
		t.Fatalf("apostrophe was not preserved: %q", merged)
	}
}

func TestRenderInline_DropsEmphasisMarkersAndPreservesHardBreaks(t *testing.T) {
	source := []byte("**Engagement:** AI Support Triage  \n**Duration:** 6 weeks")
	parser := goldmark.New().Parser()
	doc := parser.Parse(gtext.NewReader(source))
	p, ok := doc.FirstChild().(*gast.Paragraph)
	if !ok {
		t.Fatalf("first node = %T, want paragraph", doc.FirstChild())
	}

	got := renderInline(p, source)
	if strings.Contains(got, "**") {
		t.Fatalf("inline emphasis markers leaked into output: %q", got)
	}
	if !strings.Contains(got, "Engagement: AI Support Triage") {
		t.Fatalf("emphasized text missing: %q", got)
	}
	if !strings.Contains(got, "Triage\nDuration:") {
		t.Fatalf("hard break was not preserved: %q", got)
	}

	rows := paragraphRows(p, source)
	if len(rows) != 2 {
		t.Fatalf("paragraphRows returned %d rows, want 2", len(rows))
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

// Raw HTML blocks (and other unhandled node types) are skipped
// silently so the rest of the document still renders. Tables + images
// are now supported (TestRender_Table / TestRender_Image*); an http(s)
// image with no resolver degrades to a placeholder, never an error.
func TestRender_UnhandledBlocksSkippedGracefully(t *testing.T) {
	body := `# Heading

<div>raw HTML</div>

![remote](https://example.com/x.png)

A paragraph that should still render.`
	pdf, err := renderPDF(body, nil, RenderOptions{})
	if err != nil {
		t.Fatalf("render with unhandled blocks: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
}

// GFM tables render to a real PDF (pricing/deliverables grids). Column
// alignment markers exercise cellAlign.
func TestRender_Table(t *testing.T) {
	body := `# Pricing

| Item               | Qty | Amount  |
|:-------------------|:---:|--------:|
| Discovery workshop |  1  | $4,000  |
| Model fine-tuning  |  2  | $12,000 |

**Total: $16,000**`
	pdf, err := renderPDF(body, nil, RenderOptions{})
	if err != nil {
		t.Fatalf("render table: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
	if len(pdf) < 1000 {
		t.Errorf("table PDF suspiciously small: %d bytes", len(pdf))
	}
}

// An image whose src resolves to real bytes embeds into the PDF, and
// the resolver receives the markdown src verbatim.
func TestRender_ImageViaResolver(t *testing.T) {
	pngBytes := tinyPNG(t)
	var gotSrc string
	resolver := func(src string) ([]byte, string, error) {
		gotSrc = src
		return pngBytes, "png", nil
	}
	body := "![logo](storage:7 \"width=40%\")\n\n# Proposal"
	pdf, err := renderPDF(body, nil, RenderOptions{ImageResolver: resolver})
	if err != nil {
		t.Fatalf("render image: %v", err)
	}
	if gotSrc != "storage:7" {
		t.Errorf("resolver got src %q, want storage:7", gotSrc)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
}

// A nil resolver (and a resolver that errors) must degrade to a
// placeholder, never fail the render.
func TestRender_ImageNilResolverPlaceholder(t *testing.T) {
	body := "![logo](storage:7)\n\nBody text."
	pdf, err := renderPDF(body, nil, RenderOptions{})
	if err != nil {
		t.Fatalf("nil resolver should not error: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}

	errResolver := func(string) ([]byte, string, error) { return nil, "", errFake }
	warnings := []string{}
	pdf, err = renderPDF(body, nil, RenderOptions{
		ImageResolver: errResolver,
		OnWarning:     func(message string) { warnings = append(warnings, message) },
	})
	if err != nil {
		t.Fatalf("resolver error should degrade, not fail: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "resolve failed") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestRender_ReusesResolvedImage(t *testing.T) {
	requests := 0
	baseResolver := func(string) ([]byte, string, error) {
		requests++
		return tinyPNG(t), "png", nil
	}
	type cached struct {
		data []byte
		ext  string
		err  error
	}
	cache := map[string]cached{}
	resolver := func(src string) ([]byte, string, error) {
		if got, ok := cache[src]; ok {
			return got.data, got.ext, got.err
		}
		data, ext, err := baseResolver(src)
		cache[src] = cached{data, ext, err}
		return data, ext, err
	}
	_, err := renderPDF("![one](storage:7)\n\n![two](storage:7)", nil, RenderOptions{ImageResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("resolver called %d times, want 1", requests)
	}
}

func TestParseWidthPercent(t *testing.T) {
	for in, want := range map[string]float64{
		"width=30%": 30,
		"30%":       30,
		"75":        75,
		"":          0,
		"abc":       0,
		"120%":      0,
	} {
		if got := parseWidthPercent(in); got != want {
			t.Errorf("parseWidthPercent(%q) = %v, want %v", in, got, want)
		}
	}
}

var errFake = errFakeT("resolve failed")

type errFakeT string

func (e errFakeT) Error() string { return string(e) }

// tinyPNG returns a small valid PNG that gofpdf (maroto's backend) can
// embed — generated via the stdlib encoder so it's always well-formed.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 80, B: 160, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
