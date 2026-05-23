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
	"strconv"
	"strings"
	ttemplate "text/template"

	"github.com/johnfercher/maroto/v2"
	"github.com/johnfercher/maroto/v2/pkg/components/col"
	mimage "github.com/johnfercher/maroto/v2/pkg/components/image"
	"github.com/johnfercher/maroto/v2/pkg/components/line"
	"github.com/johnfercher/maroto/v2/pkg/components/page"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/config"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/border"
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

// mergeTemplate runs text/template against the body. The merged output
// is Markdown, not HTML, so HTML escaping would leak entities like
// &#39; into the final PDF. Values are written as text after the template
// is parsed; data containing `{{ ... }}` is not evaluated as template
// syntax in a second pass.
func mergeTemplate(body string, data map[string]any) (string, error) {
	t, err := ttemplate.New("doc").Option("missingkey=zero").Parse(body)
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
		WithLeftMargin(18).
		WithRightMargin(18).
		WithTopMargin(18).
		Build()
	m := maroto.New(cfg)

	source := []byte(md)
	// goldmark.New(WithExtensions(Table)) keeps the CommonMark base
	// (headings/lists/paragraphs/...) and adds GFM table parsing.
	mdParser := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser()
	ast := mdParser.Parse(gtext.NewReader(source))

	for n := ast.FirstChild(); n != nil; {
		if h, ok := n.(*gast.Heading); ok {
			if tbl, ok := n.NextSibling().(*extast.Table); ok {
				rows := append([]core.Row{headingRow(h, source)}, tableRows(tbl, source)...)
				addRowsWithTableGuard(m, rows)
				n = tbl.NextSibling()
				continue
			}
		}
		rows := blockToRows(n, source, opts.ImageResolver)
		if len(rows) > 0 {
			if _, ok := n.(*extast.Table); ok {
				addRowsWithTableGuard(m, rows)
			} else {
				m.AddRows(rows...)
			}
		}
		n = n.NextSibling()
	}

	doc, err := m.Generate()
	if err != nil {
		return nil, fmt.Errorf("maroto generate: %w", err)
	}
	return doc.GetBytes(), nil
}

func addRowsWithTableGuard(m core.Maroto, rows []core.Row) {
	if len(rows) == 0 {
		return
	}
	// Keep heading + table header + the first body row together. This
	// avoids orphaned table headers at the bottom of a page without
	// forcing every table onto a new page.
	needed := 14 + float64(len(rows))*4.4
	if needed > 38 {
		needed = 38
	}
	if !m.FitlnCurrentPage(needed) {
		m.AddPages(page.New().Add(rows...))
		return
	}
	m.AddRows(rows...)
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
		return paragraphRows(b, source)
	case *gast.List:
		return listRows(b, source)
	case *extast.Table:
		return tableRows(b, source)
	case *gast.ThematicBreak:
		return []core.Row{thematicBreakRow()}
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
					Size:  8.6,
					Color: bodyColor(),
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
	top := 2.4
	switch h.Level {
	case 1:
		size = 14.8
		top = 2
	case 2:
		size = 11.8
		top = 3
	case 3:
		size = 9.8
		top = 1.8
	case 4:
		size = 9.2
		top = 1.6
	default:
		size = 8.8
		top = 1.4
	}
	return row.New().Add(
		col.New(12).Add(text.New(extractText(h, source), props.Text{
			Size:  size,
			Style: fontstyle.Bold,
			Top:   top,
			Color: headingColor(),
		})),
	)
}

func thematicBreakRow() core.Row {
	return line.NewRow(8, props.Line{
		Color:         ruleColor(),
		Thickness:     0.12,
		OffsetPercent: 72,
		SizePercent:   100,
	})
}

func paragraphRows(p *gast.Paragraph, source []byte) []core.Row {
	lines := splitRenderedLines(renderInline(p, source))
	out := make([]core.Row, 0, len(lines))
	for i, s := range lines {
		if s == "" {
			continue
		}
		top := 1.0
		if i == 0 {
			top = 1.3
		}
		out = append(out, textRow(s, 8.8, top, 0))
	}
	return out
}

func textRow(s string, size, top, left float64) core.Row {
	return row.New().Add(
		col.New(12).Add(text.New(s, props.Text{
			Size:            size,
			Top:             top,
			Left:            left,
			VerticalPadding: 0.35,
			Color:           bodyColor(),
		})),
	)
}

func splitRenderedLines(s string) []string {
	parts := strings.Split(s, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
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
			col.New(1).Add(text.New(strings.TrimSpace(prefix), props.Text{
				Size:  8.7,
				Top:   0.7,
				Align: align.Right,
				Color: mutedColor(),
			})),
			col.New(11).Add(text.New(txt, props.Text{
				Size:            8.8,
				Left:            1.5,
				Top:             0.7,
				VerticalPadding: 0.35,
				Color:           bodyColor(),
			})),
		))
	}
	return out
}

func codeRow(s string) core.Row {
	return row.New().Add(
		col.New(12).Add(text.New(s, props.Text{
			Family: "courier",
			Size:   8,
			Top:    2,
			Left:   6,
			Color:  bodyColor(),
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
			if t.HardLineBreak() {
				b.WriteByte('\n')
			} else if t.SoftLineBreak() {
				b.WriteByte(' ')
			}
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

// renderInline flattens inline emphasis/bold/code into clean text.
// Maroto doesn't do mid-line styling in a single text run, and visible
// Markdown markers look broken in client PDFs, so emphasis markers are
// dropped while the emphasized text stays intact.
func renderInline(n gast.Node, source []byte) string {
	var b strings.Builder
	gast.Walk(n, func(node gast.Node, entering bool) (gast.WalkStatus, error) {
		switch t := node.(type) {
		case *gast.Text:
			if entering {
				b.Write(t.Segment.Value(source))
				if t.HardLineBreak() {
					b.WriteByte('\n')
				} else if t.SoftLineBreak() {
					b.WriteByte(' ')
				}
			}
		case *gast.Emphasis:
			return gast.WalkContinue, nil
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
	spans := tableSpans(n, aligns)
	cols := make([]core.Col, 0, n)
	for i, cell := range cells {
		span := spans[i]
		tp := props.Text{
			Size:            8.1,
			Top:             1.0,
			Left:            1.6,
			Right:           1.2,
			Align:           cellAlign(aligns, i),
			VerticalPadding: 0.25,
			Color:           bodyColor(),
		}
		if header {
			tp.Style = fontstyle.Bold
			tp.Size = 8.2
			tp.Color = headingColor()
		}
		cols = append(cols, col.New(span).WithStyle(tableCellStyle(header)).Add(text.New(strings.TrimSpace(renderInline(cell, source)), tp)))
	}
	r := row.New().Add(cols...)
	if header {
		r = r.WithStyle(&props.Cell{BackgroundColor: tableHeaderColor()})
	}
	return r
}

func tableSpans(n int, aligns []extast.Alignment) []int {
	if n == 3 {
		if len(aligns) >= 3 && aligns[1] == extast.AlignCenter && aligns[2] == extast.AlignRight {
			return []int{7, 2, 3}
		}
		return []int{4, 2, 6}
	}
	base := 12 / n
	if base < 1 {
		base = 1 // >12 columns: degrade rather than vanish
	}
	rem := 12 - base*n
	if rem < 0 {
		rem = 0
	}
	spans := make([]int, n)
	for i := range spans {
		spans[i] = base
		if i == n-1 {
			spans[i] += rem
		}
	}
	return spans
}

func tableCellStyle(header bool) *props.Cell {
	c := &props.Cell{
		BorderType:      border.Bottom,
		BorderThickness: 0.04,
		BorderColor:     ruleColor(),
	}
	if header {
		c.BackgroundColor = tableHeaderColor()
	}
	return c
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
			for _, line := range splitRenderedLines(s) {
				if line != "" {
					rows = append(rows, textRow(line, 8.8, 1.3, 0))
				}
			}
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
			Color: mutedColor(),
		})),
	)
}

func headingColor() *props.Color {
	return &props.Color{Red: 24, Green: 31, Blue: 42}
}

func bodyColor() *props.Color {
	return &props.Color{Red: 36, Green: 43, Blue: 52}
}

func mutedColor() *props.Color {
	return &props.Color{Red: 112, Green: 122, Blue: 135}
}

func ruleColor() *props.Color {
	return &props.Color{Red: 218, Green: 224, Blue: 231}
}

func tableHeaderColor() *props.Color {
	return &props.Color{Red: 247, Green: 249, Blue: 251}
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
