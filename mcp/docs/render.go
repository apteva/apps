package main

// Pure-Go render pipeline:
//
//   markdown body + Go-template placeholders
//        │
//        ▼  text/template substitution against caller's data
//   plain markdown
//        │
//        ▼  goldmark → AST
//   AST blocks (Paragraph, Heading, List, ThematicBreak, CodeBlock)
//        │
//        ▼  walked into maroto row/col/text components
//   *.PDF bytes
//
// Small subset of markdown supported in v0.1: headings (h1-h3),
// paragraphs, unordered lists, code blocks (fenced + indented),
// thematic breaks (---), inline emphasis/bold/code spans. Tables +
// images deferred to v0.2 (maroto has both, but the AST→layout
// mapping is its own design problem).
//
// Tradeoff: maroto is grid-based, not flow-based. Wrapping long
// paragraphs across page breaks works because text.New on auto rows
// reflows, but custom CSS or webfonts aren't possible here. That's
// the deliberate choice over chromedp for the v0.1 backend.

import (
	"bytes"
	"errors"
	"fmt"
	htemplate "html/template"
	"strings"

	"github.com/johnfercher/maroto/v2"
	"github.com/johnfercher/maroto/v2/pkg/components/col"
	"github.com/johnfercher/maroto/v2/pkg/components/line"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/config"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/consts/pagesize"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/props"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	gtext "github.com/yuin/goldmark/text"
)

// RenderOptions tunes the PDF output. Sensible defaults — most
// callers pass zero-value for everything except Body+Data.
type RenderOptions struct {
	// PageSize is "A4" | "letter" | "legal". Empty = "A4".
	PageSize string
}

// renderPDF is the one-shot render entry point. Takes the raw
// template body + caller data, returns the assembled PDF bytes.
//
// Errors are flat — the audit row records the data + template_id
// regardless of whether the PDF actually built, so a render failure
// is debuggable from the templates editor.
func renderPDF(body string, data map[string]any, opts RenderOptions) ([]byte, error) {
	if body == "" {
		return nil, errors.New("template body empty")
	}
	if data == nil {
		data = map[string]any{}
	}
	merged, err := mergeTemplate(body, data)
	if err != nil {
		return nil, fmt.Errorf("template substitution: %w", err)
	}
	return markdownToPDF(merged, opts)
}

// mergeTemplate runs html/template against the body. The choice of
// html/template (vs text/template) is intentional: caller data may
// contain user-supplied strings, and html/template's contextual
// auto-escaping prevents an injected `{{ ... }}` in the data from
// being executed as template syntax. Markdown in the body still
// renders normally because we run goldmark against the post-merge
// string.
func mergeTemplate(body string, data map[string]any) (string, error) {
	t, err := htemplate.New("doc").Option("missingkey=zero").Parse(body)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// markdownToPDF — the AST→maroto walk. Builds rows in document
// order; each block type emits a row of the right height.
func markdownToPDF(md string, opts RenderOptions) ([]byte, error) {
	cfg := config.NewBuilder().
		WithPageSize(pageSizeFromString(opts.PageSize)).
		WithLeftMargin(20).
		WithRightMargin(20).
		WithTopMargin(20).
		Build()
	m := maroto.New(cfg)

	parser := goldmark.DefaultParser()
	ast := parser.Parse(gtext.NewReader([]byte(md)))

	for n := ast.FirstChild(); n != nil; n = n.NextSibling() {
		rows := blockToRows(n, []byte(md))
		if len(rows) > 0 {
			m.AddRows(rows...)
		}
	}

	doc, err := m.Generate()
	if err != nil {
		return nil, fmt.Errorf("maroto generate: %w", err)
	}
	return doc.GetBytes(), nil
}

// blockToRows turns one top-level AST block into 0+ maroto rows.
// Returning a slice (vs a single row) lets a list emit one row per
// item, a code block split across pages cleanly, etc.
func blockToRows(n gast.Node, source []byte) []core.Row {
	switch b := n.(type) {
	case *gast.Heading:
		return []core.Row{headingRow(b, source)}
	case *gast.Paragraph:
		return []core.Row{paragraphRow(b, source)}
	case *gast.List:
		return listRows(b, source)
	case *gast.ThematicBreak:
		// --- horizontal rule. line.NewRow gives us a row whose
		// content is a horizontal line at the configured offset;
		// height 4mm gives some breathing room above + below.
		return []core.Row{line.NewRow(4)}
	case *gast.FencedCodeBlock:
		return []core.Row{codeRow(extractText(b, source))}
	case *gast.CodeBlock:
		return []core.Row{codeRow(extractText(b, source))}
	case *gast.Blockquote:
		return []core.Row{
			row.New().Add(
				col.New(12).Add(text.New(extractText(b, source), props.Text{
					Style: fontstyle.Italic,
					Left:  10,
					Top:   2,
					Size:  10,
				})),
			),
		}
	}
	// Unknown / unhandled: skip silently. Avoids hard-failing on
	// unsupported markdown features (HTML blocks, images, tables)
	// so partial templates still render.
	return nil
}

func headingRow(h *gast.Heading, source []byte) core.Row {
	size := 12.0
	switch h.Level {
	case 1:
		size = 22
	case 2:
		size = 18
	case 3:
		size = 14
	case 4:
		size = 12
	default:
		size = 11
	}
	return row.New().Add(
		col.New(12).Add(text.New(extractText(h, source), props.Text{
			Size:  size,
			Style: fontstyle.Bold,
			Top:   4,
		})),
	)
}

func paragraphRow(p *gast.Paragraph, source []byte) core.Row {
	return row.New().Add(
		col.New(12).Add(text.New(renderInline(p, source), props.Text{
			Size: 11,
			Top:  2,
		})),
	)
}

// listRows iterates the list's children. Each ListItem becomes one
// auto-row prefixed with a bullet so wrapping works correctly across
// page breaks. Numbered lists (Ordered=true) get "1. " prefixes.
func listRows(list *gast.List, source []byte) []core.Row {
	out := []core.Row{}
	idx := 1
	for li := list.FirstChild(); li != nil; li = li.NextSibling() {
		txt := extractText(li, source)
		var prefix string
		if list.IsOrdered() {
			prefix = fmt.Sprintf("%d. ", idx)
			idx++
		} else {
			prefix = "• "
		}
		out = append(out, row.New().Add(
			col.New(12).Add(text.New(prefix+txt, props.Text{
				Size: 11,
				Left: 6,
				Top:  1,
			})),
		))
	}
	return out
}

func codeRow(s string) core.Row {
	return row.New().Add(
		col.New(12).Add(text.New(s, props.Text{
			Family: "courier",
			Size:   9,
			Top:    2,
			Left:   6,
		})),
	)
}

// extractText flattens the node's text content. Drops formatting —
// fine for code blocks + simple headings; paragraphs use renderInline
// instead so emphasis is preserved (well, dropped to plain since
// maroto can't mid-line bold without separate components, but
// textually intact).
func extractText(n gast.Node, source []byte) string {
	var b strings.Builder
	gast.Walk(n, func(node gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}
		switch t := node.(type) {
		case *gast.Text:
			b.Write(t.Segment.Value(source))
		case *gast.CodeSpan:
			// Inline code: surround with backticks so the reader can
			// see "this is code" even without monospace.
			b.WriteByte('`')
			for c := t.FirstChild(); c != nil; c = c.NextSibling() {
				if tx, ok := c.(*gast.Text); ok {
					b.Write(tx.Segment.Value(source))
				}
			}
			b.WriteByte('`')
			return gast.WalkSkipChildren, nil
		}
		return gast.WalkContinue, nil
	})
	return b.String()
}

// renderInline handles inline emphasis/bold/code by markdown-style
// fallback. Maroto doesn't do mid-line styling in a single text run,
// so we keep the markdown markers visible to preserve intent. Future
// versions can split into multi-component rows.
func renderInline(n gast.Node, source []byte) string {
	var b strings.Builder
	gast.Walk(n, func(node gast.Node, entering bool) (gast.WalkStatus, error) {
		switch t := node.(type) {
		case *gast.Text:
			if entering {
				b.Write(t.Segment.Value(source))
			}
		case *gast.Emphasis:
			marker := "*"
			if t.Level == 2 {
				marker = "**"
			}
			b.WriteString(marker)
		case *gast.CodeSpan:
			if entering {
				b.WriteByte('`')
				for c := t.FirstChild(); c != nil; c = c.NextSibling() {
					if tx, ok := c.(*gast.Text); ok {
						b.Write(tx.Segment.Value(source))
					}
				}
				b.WriteByte('`')
				return gast.WalkSkipChildren, nil
			}
		}
		return gast.WalkContinue, nil
	})
	return b.String()
}

func pageSizeFromString(s string) pagesize.Type {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "letter":
		return pagesize.Letter
	case "legal":
		return pagesize.Legal
	default:
		return pagesize.A4
	}
}
