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
	page := buildPrintableHTML("<main>Body</main>", "", title)
	if !strings.Contains(page, "<title>Guide &lt;2026&gt; &amp; beyond</title>") {
		t.Fatalf("document title was not escaped: %s", page)
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
}
