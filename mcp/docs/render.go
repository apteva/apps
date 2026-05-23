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
// Markdown supported (v0.2): headings (h1-h3), paragraphs, unordered
// + ordered lists, GFM tables, images (logo / inline), code blocks
// (fenced + indented), thematic breaks (---), inline emphasis/bold/
// code spans. Tables parse via the goldmark GFM table extension and
// map onto the maroto 12-col grid; images resolve through
// RenderOptions.ImageResolver (storage:<id> or data: URIs — http(s)
// is intentionally excluded; see resolveImageSrc in storageclient.go).
//
// Tradeoff: maroto is grid-based, not flow-based. Wrapping long
// paragraphs across page breaks works because text.New on auto rows
// reflows, but custom CSS or webfonts aren't possible here. That's
// the deliberate choice over chromedp for the backend.

import (
	"bytes"
	"errors"
	"fmt"
	htemplate "html/template"
	"strconv"
	"strings"

	"github.com/johnfercher/maroto/v2"
	"github.com/johnfercher/maroto/v2/pkg/components/col"
	mimage "github.com/johnfercher/maroto/v2/pkg/components/image"
	"github.com/johnfercher/maroto/v2/pkg/components/line"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/config"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	mext "github.com/johnfercher/maroto/v2/pkg/consts/extension"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/consts/pagesize"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/props"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	gtext "github.com/yuin/goldmark/text"
)

// RenderOptions tunes the PDF output. Sensible defaults — most
// callers pass zero-value for everything except Body+Data.
type RenderOptions struct {
	// PageSize is "A4" | "letter" | "legal". Empty = "A4".
	PageSize string
	// ImageResolver turns a markdown image src into bytes. Set by the
	// tool layer (backed by the storage client). Nil = images render
	// as a labeled placeholder rather than failing the render — see
	// imageResolver.
	ImageResolver imageResolver
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

	source := []byte(md)
	// goldmark.New(WithExtensions(Table)) keeps the CommonMark base
	// (headings/lists/paragraphs/...) and adds GFM table parsing.
	mdParser := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser()
	ast := mdParser.Parse(gtext.NewReader(source))

	for n := ast.FirstChild(); n != nil; n = n.NextSibling() {
		rows := blockToRows(n, source, opts.ImageResolver)
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
func blockToRows(n gast.Node, source []byte, resolve imageResolver) []core.Row {
	switch b := n.(type) {
	case *gast.Heading:
		return []core.Row{headingRow(b, source)}
	case *gast.Paragraph:
		// A paragraph whose inline content includes image(s) is split
		// so the image gets its own row (maroto can't flow an image
		// inline with text). The common "logo on its own line" case
		// yields a single image row.
		if paragraphHasImage(b) {
			return paragraphToRows(b, source, resolve)
		}
		return []core.Row{paragraphRow(b, source)}
	case *gast.List:
		return listRows(b, source)
	case *extast.Table:
		return tableRows(b, source)
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
	// Unknown / unhandled: skip silently (raw HTML blocks, etc.) so
	// partial templates still render.
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

// ─── v0.2: tables ─────────────────────────────────────────────────────

// tableRows maps a GFM table into one maroto row per header/body row.
// The 12-col grid is split evenly across the table's columns; the last
// column absorbs any remainder so each row spans the full width.
func tableRows(tbl *extast.Table, source []byte) []core.Row {
	var rows []core.Row
	for n := tbl.FirstChild(); n != nil; n = n.NextSibling() {
		switch r := n.(type) {
		case *extast.TableHeader:
			rows = append(rows, tableLine(r, source, tbl.Alignments, true))
		case *extast.TableRow:
			rows = append(rows, tableLine(r, source, tbl.Alignments, false))
		}
	}
	return rows
}

// tableLine builds one maroto row from a header/body row node. Header
// rows are bold on a light-gray fill so pricing tables read cleanly.
func tableLine(node gast.Node, source []byte, aligns []extast.Alignment, header bool) core.Row {
	var cells []gast.Node
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		if _, ok := c.(*extast.TableCell); ok {
			cells = append(cells, c)
		}
	}
	n := len(cells)
	if n == 0 {
		return row.New()
	}
	base := 12 / n
	if base < 1 {
		base = 1 // >12 columns: degrade rather than vanish
	}
	rem := 12 - base*n
	if rem < 0 {
		rem = 0
	}
	cols := make([]core.Col, 0, n)
	for i, cell := range cells {
		span := base
		if i == n-1 {
			span += rem
		}
		tp := props.Text{Size: 10, Top: 2, Left: 2, Align: cellAlign(aligns, i)}
		if header {
			tp.Style = fontstyle.Bold
		}
		cols = append(cols, col.New(span).Add(text.New(strings.TrimSpace(renderInline(cell, source)), tp)))
	}
	r := row.New().Add(cols...)
	if header {
		r = r.WithStyle(&props.Cell{BackgroundColor: &props.Color{Red: 238, Green: 238, Blue: 238}})
	}
	return r
}

func cellAlign(aligns []extast.Alignment, i int) align.Type {
	if i >= len(aligns) {
		return align.Left
	}
	switch aligns[i] {
	case extast.AlignRight:
		return align.Right
	case extast.AlignCenter:
		return align.Center
	default:
		return align.Left
	}
}

// ─── v0.2: images ─────────────────────────────────────────────────────

// imageResolver turns a markdown image src into raw bytes + a maroto
// image extension ("png"|"jpg"|"jpeg"). Set on RenderOptions by the
// tool layer (backed by the storage client). Nil in unit tests /
// preview-without-storage — images then degrade to a labeled
// placeholder rather than failing the whole render.
type imageResolver func(src string) (data []byte, ext string, err error)

func paragraphHasImage(p gast.Node) bool {
	for c := p.FirstChild(); c != nil; c = c.NextSibling() {
		if _, ok := c.(*gast.Image); ok {
			return true
		}
	}
	return false
}

// paragraphToRows splits a paragraph containing image(s) into a
// sequence of rows: accumulated inline text flushes to a text row,
// each image emits its own image row, preserving document order.
func paragraphToRows(p gast.Node, source []byte, resolve imageResolver) []core.Row {
	var rows []core.Row
	var buf strings.Builder
	flush := func() {
		s := strings.TrimSpace(buf.String())
		buf.Reset()
		if s != "" {
			rows = append(rows, row.New().Add(col.New(12).Add(text.New(s, props.Text{Size: 11, Top: 2}))))
		}
	}
	for c := p.FirstChild(); c != nil; c = c.NextSibling() {
		if img, ok := c.(*gast.Image); ok {
			flush()
			rows = append(rows, imageRow(img, source, resolve))
			continue
		}
		buf.WriteString(renderInline(c, source))
	}
	flush()
	return rows
}

// imageRow resolves an image's bytes and renders it at the width hint
// from the image title (`"width=30%"` / `"30%"`), defaulting to 30%.
// Any resolution failure degrades to a placeholder so the render
// always completes.
func imageRow(img *gast.Image, source []byte, resolve imageResolver) core.Row {
	src := strings.TrimSpace(string(img.Destination))
	alt := strings.TrimSpace(extractText(img, source))
	width := parseWidthPercent(string(img.Title))
	if width <= 0 {
		width = 30 // sensible logo default
	}
	if resolve == nil {
		return placeholderRow(alt, src)
	}
	data, ext, err := resolve(src)
	if err != nil || len(data) == 0 {
		return placeholderRow(alt, src)
	}
	return row.New().Add(
		col.New(12).Add(mimage.NewFromBytes(data, imageExt(ext), props.Rect{Percent: width})),
	)
}

// placeholderRow stands in for an image we couldn't resolve (nil
// resolver, http(s) src, fetch error). Leaves a visible breadcrumb
// instead of a silent gap.
func placeholderRow(alt, src string) core.Row {
	label := alt
	if label == "" {
		label = src
	}
	if label == "" {
		label = "image"
	}
	return row.New().Add(
		col.New(12).Add(text.New("[image: "+label+"]", props.Text{
			Size:  9,
			Style: fontstyle.Italic,
			Top:   2,
			Left:  2,
			Color: &props.Color{Red: 130, Green: 130, Blue: 130},
		})),
	)
}

// parseWidthPercent reads a width hint from a markdown image title:
// `![logo](storage:12 "width=30%")` or `"30%"`. Returns 0 when
// absent/invalid so the caller applies its default.
func parseWidthPercent(title string) float64 {
	t := strings.ToLower(strings.TrimSpace(title))
	t = strings.TrimPrefix(t, "width=")
	t = strings.TrimSuffix(strings.TrimSpace(t), "%")
	if v, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil && v > 0 && v <= 100 {
		return v
	}
	return 0
}

func imageExt(ext string) mext.Type {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case "jpg":
		return mext.Jpg
	case "jpeg":
		return mext.Jpeg
	default:
		return mext.Png
	}
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
