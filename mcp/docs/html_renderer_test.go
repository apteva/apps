package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestMergeHTMLTemplateEscapesData(t *testing.T) {
	merged, err := mergeHTMLTemplate(`<h1>{{.title}}</h1><a href="{{.url}}">Open</a>`, map[string]any{
		"title": `<script>alert(1)</script>`,
		"url":   `javascript:alert(1)`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(merged, "<script>") || !strings.Contains(merged, "&lt;script&gt;") {
		t.Fatalf("data was not HTML-escaped: %s", merged)
	}
	if !strings.Contains(merged, "#ZgotmplZ") {
		t.Fatalf("unsafe URL was not rejected by html/template: %s", merged)
	}
}

func TestPrintableHTMLUsesEscapedDocumentTitle(t *testing.T) {
	title := documentTitle(map[string]any{"title": `Guide <2026> & beyond`})
	page := buildPrintableHTML("<main>Body</main>", "", title, printGeometry{PaperWidthIn: 8.27, PaperHeightIn: 11.69, ContentWidthIn: 8.27, ContentHeightIn: 11.69})
	if !strings.Contains(page, "<title>Guide &lt;2026&gt; &amp; beyond</title>") {
		t.Fatalf("document title was not escaped: %s", page)
	}
}

func TestPopulatePageMarkers(t *testing.T) {
	marked, err := populatePageMarkers(`<article><section data-pdf-page><span data-page-number></span></section><section data-pdf-page><span class="page-number"></span><span data-page-total="decimal-leading-zero"></span></section></article>`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-page-index="1"`, `data-page-total="2"`, `>1</span>`, `data-page-number-value="02"`, `data-page-total-value="02"`, `>02</span>`} {
		if !strings.Contains(marked, want) {
			t.Fatalf("prepared HTML missing %q: %s", want, marked)
		}
	}
}

func TestFixedLayoutRejectsPageSizeOverride(t *testing.T) {
	for _, mode := range []string{"", "fixed"} {
		_, err := resolveHTMLPageSize(DocumentSettings{LayoutMode: mode, PageSize: "A4"}, "letter")
		if err == nil || !strings.Contains(err.Error(), "locked to A4") {
			t.Fatalf("expected fixed-layout error for mode %q, got %v", mode, err)
		}
	}
	pageSize, err := resolveHTMLPageSize(DocumentSettings{LayoutMode: "flow", PageSize: "A4"}, "letter")
	if err != nil || pageSize != "letter" {
		t.Fatalf("flow layout override = %q, %v", pageSize, err)
	}
}

func TestHTMLSanitizerRejectsActiveAndRemoteContent(t *testing.T) {
	for _, source := range []string{
		`<script>alert(1)</script>`,
		`<iframe src="https://example.com"></iframe>`,
		`<img src="https://example.com/tracker.png">`,
		`<div onclick="alert(1)">x</div>`,
	} {
		if _, err := sanitizeHTMLFragment(source, nil); err == nil {
			t.Fatalf("expected sanitizer rejection for %q", source)
		}
	}
	for _, css := range []string{
		`@import "https://example.com/style.css";`,
		`.x { background: url(https://example.com/pixel.png) }`,
		`.x { behavior: url(x.htc) }`,
	} {
		if _, err := sanitizeCSS(css, nil); err == nil {
			t.Fatalf("expected CSS rejection for %q", css)
		}
	}
}

func TestHTMLSanitizerResolvesStorageImage(t *testing.T) {
	resolver := func(src string) ([]byte, string, error) {
		if src != "storage:42" {
			t.Fatalf("resolver source = %q", src)
		}
		return tinyPNG(t), "png", nil
	}
	clean, err := sanitizeHTMLFragment(`<figure><img src="storage:42" alt="Chart"><figcaption>Result</figcaption></figure>`, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clean, "data:image/png;base64,") || strings.Contains(clean, "storage:42") {
		t.Fatalf("image was not embedded: %s", clean)
	}
}

func TestHTMLRendererProducesPDFWhenChromeAvailable(t *testing.T) {
	if _, err := findChromeExecutable(); err != nil {
		t.Skip(err)
	}
	zero := 0.0
	background := true
	pdf, err := renderHTMLToPDF(context.Background(), `
<main class="page">
  <p class="eyebrow">APTEVA FIELD GUIDE</p>
  <h1>{{.title}}</h1>
  <p class="lede">{{.summary}}</p>
  <section><strong>Outcome</strong><p>One polished, deterministic PDF.</p></section>
</main>`, `
@page { size: A4; margin: 0; }
body { font-family: Arial, sans-serif; color: #172033; }
.page { min-height: 260mm; padding: 22mm; box-sizing: border-box; background: linear-gradient(145deg,#eef5ff,#fff); }
.eyebrow { color: #5b4ce6; letter-spacing: .16em; font-weight: 700; }
h1 { font-size: 40pt; max-width: 150mm; }
.lede { font-size: 16pt; max-width: 130mm; }
section { margin-top: 30mm; padding: 8mm; border-left: 4mm solid #5b4ce6; background: white; }
`, map[string]any{"title": "The Practical AI Automation Playbook", "summary": "A 30-day framework for safer automation."}, DocumentSettings{
		PageSize: "A4", MarginTopMM: &zero, MarginRightMM: &zero,
		MarginBottomMM: &zero, MarginLeftMM: &zero, PrintBackground: &background,
	}, htmlRenderOptions{PageSize: "A4", Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || len(pdf) < 3000 {
		t.Fatalf("invalid or suspiciously small PDF: %d bytes", len(pdf))
	}
	if !bytes.Contains(pdf, []byte("/StructTreeRoot")) {
		t.Fatal("PDF is missing a tagged-document structure tree")
	}
	if !bytes.Contains(pdf, []byte("/Outlines")) {
		t.Fatal("PDF is missing a document outline")
	}
}

func TestHTMLRendererRejectsClippedPageContent(t *testing.T) {
	if _, err := findChromeExecutable(); err != nil {
		t.Skip(err)
	}
	zero := 0.0
	background := true
	_, err := renderHTMLToPDF(context.Background(), `
<main data-pdf-page>
  <h1>Overflow test</h1>
  <div class="too-tall">This content cannot fit.</div>
</main>`, `
@page { margin: 0; }
main { width: var(--document-page-width); height: var(--document-page-height); overflow: hidden; }
.too-tall { height: 400mm; }
`, map[string]any{"title": "Overflow test"}, DocumentSettings{
		LayoutMode: "fixed", PageSize: "A4", MarginTopMM: &zero, MarginRightMM: &zero,
		MarginBottomMM: &zero, MarginLeftMM: &zero, PrintBackground: &background,
	}, htmlRenderOptions{PageSize: "A4", Timeout: 20 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "document page 1 overflows vertically") {
		t.Fatalf("expected actionable overflow error, got %v", err)
	}
}

func TestHTMLRendererRejectsUndecodableImage(t *testing.T) {
	if _, err := findChromeExecutable(); err != nil {
		t.Skip(err)
	}
	zero := 0.0
	background := true
	_, err := renderHTMLToPDF(context.Background(), `<main><img src="storage:7" alt="Broken chart"></main>`, ``, nil, DocumentSettings{
		PageSize: "A4", MarginTopMM: &zero, MarginRightMM: &zero,
		MarginBottomMM: &zero, MarginLeftMM: &zero, PrintBackground: &background,
	}, htmlRenderOptions{
		PageSize: "A4", Timeout: 20 * time.Second,
		ImageResolver: func(string) ([]byte, string, error) { return []byte("not a png"), "png", nil },
	})
	if err == nil || !strings.Contains(err.Error(), "failed to decode: Broken chart") {
		t.Fatalf("expected image readiness error, got %v", err)
	}
}
