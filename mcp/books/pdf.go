package main

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-pdf/fpdf"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/goregular"
)

type PDFReport struct {
	Valid         bool     `json:"valid"`
	PageCount     int      `json:"page_count"`
	TrimWidthIn   float64  `json:"trim_width_in"`
	TrimHeightIn  float64  `json:"trim_height_in"`
	FontsEmbedded bool     `json:"fonts_embedded"`
	Warnings      []string `json:"warnings"`
}

type tocEntry struct {
	Title string
	Depth int
	Page  int
}

type printBlock struct {
	Kind    string
	Level   int
	Text    string
	AssetID int64
}

var (
	markdownImageRE = regexp.MustCompile(`^!\[([^]]*)\]\(asset:(?://)?([0-9]+)\)\s*$`)
	markdownLinkRE  = regexp.MustCompile(`\[([^]]+)\]\([^)]+\)`)
	markdownMarkRE  = regexp.MustCompile(`[*_~` + "`" + `]+`)
)

func renderPrintPDF(book *Book, tree []*BookNode, notes []BookNote, assets []BookAsset, includeNotes bool) ([]byte, PDFReport, error) {
	settings := normalizePublication(book.Publication).Print
	trimWidth, trimHeight := settings.TrimWidthIn, settings.TrimHeightIn
	if settings.Bleed {
		// KDP-style interior bleed: 0.125in on the outside edge and on both
		// top and bottom edges. The binding edge remains at trim size.
		settings.TrimWidthIn += 0.125
		settings.TrimHeightIn += 0.25
		settings.OuterMarginIn += 0.125
		settings.TopMarginIn += 0.125
		settings.BottomMarginIn += 0.125
	}
	pdf := fpdf.NewCustom(&fpdf.InitType{
		OrientationStr: "P", UnitStr: "in",
		Size: fpdf.SizeType{Wd: settings.TrimWidthIn, Ht: settings.TrimHeightIn},
	})
	pdf.SetCreator("Apteva Books", true)
	pdf.SetTitle(book.Title, true)
	pdf.SetAuthor(book.AuthorName, true)
	pdf.SetSubject(book.Description, true)
	pdf.SetKeywords(strings.Join(book.Publication.Keywords, ", "), true)
	pdf.AddUTF8FontFromBytes("Go", "", goregular.TTF)
	pdf.AddUTF8FontFromBytes("Go", "B", gobold.TTF)
	pdf.AddUTF8FontFromBytes("Go", "I", goitalic.TTF)
	pdf.SetAutoPageBreak(true, settings.BottomMarginIn)
	pdf.SetHeaderFunc(func() {
		setFacingMargins(pdf, settings)
	})
	pdf.SetFooterFunc(func() {
		if pdf.PageNo() <= 2 {
			return
		}
		pdf.SetY(-0.42)
		pdf.SetFont("Go", "", 8)
		pdf.SetTextColor(90, 90, 90)
		pdf.CellFormat(0, 0.16, strconv.Itoa(pdf.PageNo()-2), "", 0, "C", false, 0, "")
	})

	// Half title / title page.
	pdf.AddPage()
	pdf.SetY(settings.TrimHeightIn * 0.28)
	pdf.SetFont("Go", "B", 24)
	pdf.SetTextColor(20, 20, 20)
	pdf.MultiCell(0, 0.38, book.Title, "", "C", false)
	if book.Subtitle != "" {
		pdf.Ln(0.12)
		pdf.SetFont("Go", "", 14)
		pdf.MultiCell(0, 0.26, book.Subtitle, "", "C", false)
	}
	if book.AuthorName != "" {
		pdf.Ln(0.45)
		pdf.SetFont("Go", "", 12)
		pdf.MultiCell(0, 0.24, "By "+book.AuthorName, "", "C", false)
	}

	// Copyright and edition page.
	pdf.AddPage()
	pdf.SetY(settings.TrimHeightIn * 0.57)
	pdf.SetFont("Go", "", 8.5)
	pdf.SetTextColor(45, 45, 45)
	meta := normalizePublication(book.Publication)
	copyright := fmt.Sprintf("Copyright © %d %s", meta.CopyrightYear, book.AuthorName)
	pdf.MultiCell(0, 0.17, copyright, "", "L", false)
	if meta.RightsStatement != "" {
		pdf.MultiCell(0, 0.17, meta.RightsStatement, "", "L", false)
	} else {
		pdf.MultiCell(0, 0.17, "All rights reserved.", "", "L", false)
	}
	for _, line := range []string{meta.Publisher, meta.Imprint, editionLine(meta), isbnLine(meta)} {
		if line != "" {
			pdf.MultiCell(0, 0.17, line, "", "L", false)
		}
	}

	treeEntries := countNodes(tree)
	tocPages := (treeEntries + 31) / 32
	if tocPages == 0 {
		tocPages = 1
	}
	tocStart := pdf.PageNo() + 1
	for i := 0; i < tocPages; i++ {
		pdf.AddPage()
	}

	assetByID := map[int64]BookAsset{}
	for _, asset := range assets {
		assetByID[asset.ID] = asset
	}
	entries := []tocEntry{}
	var renderNode func(*BookNode, int)
	renderNode = func(node *BookNode, depth int) {
		if depth == 0 || node.Type == "chapter" || node.Type == "part" || node.Type == "front_matter" || node.Type == "back_matter" || node.Type == "appendix" {
			pdf.AddPage()
		} else if pdf.GetY() > settings.TrimHeightIn-settings.BottomMarginIn-1.0 {
			pdf.AddPage()
		}
		entries = append(entries, tocEntry{Title: node.Title, Depth: depth, Page: pdf.PageNo() - 2})
		renderPrintHeading(pdf, node.Title, depth)
		renderMarkdownPDF(pdf, node.BodyMarkdown, assetByID, settings)
		for _, child := range node.Children {
			renderNode(child, depth+1)
		}
	}
	for _, node := range tree {
		renderNode(node, 0)
	}
	if includeNotes && len(notes) > 0 {
		pdf.AddPage()
		entries = append(entries, tocEntry{Title: "Notes", Depth: 0, Page: pdf.PageNo() - 2})
		renderPrintHeading(pdf, "Notes", 0)
		for _, note := range notes {
			renderPrintHeading(pdf, note.Title, 1)
			pdf.SetFont("Go", "", settings.FontSizePt)
			pdf.MultiCell(0, settings.LineHeightPt/72, cleanPDFText(note.Body), "", "J", false)
			if note.URL != "" {
				pdf.SetFont("Go", "I", 8.5)
				pdf.MultiCell(0, 0.17, cleanPDFText(note.URL), "", "L", false)
			}
		}
	}

	writePrintTOC(pdf, entries, tocStart, settings)
	pdf.SetPage(pdf.PageCount())
	var output bytes.Buffer
	err := pdf.Output(&output)
	report := PDFReport{
		Valid: err == nil, PageCount: pdf.PageCount(), TrimWidthIn: trimWidth,
		TrimHeightIn: trimHeight, FontsEmbedded: true, Warnings: []string{},
	}
	if settings.Bleed {
		report.Warnings = append(report.Warnings, "Interior bleed is enabled in metadata, but automatic edge-to-edge image bleed requires manual preflight")
	}
	if err != nil {
		return nil, report, err
	}
	return output.Bytes(), report, nil
}

func setFacingMargins(pdf *fpdf.Fpdf, settings PrintSettings) {
	left, right := settings.InnerMarginIn, settings.OuterMarginIn
	if pdf.PageNo()%2 == 0 {
		left, right = right, left
	}
	pdf.SetMargins(left, settings.TopMarginIn, right)
	pdf.SetLeftMargin(left)
	pdf.SetRightMargin(right)
}

func renderPrintHeading(pdf *fpdf.Fpdf, title string, depth int) {
	sizes := []float64{18, 14, 12}
	if depth >= len(sizes) {
		depth = len(sizes) - 1
	}
	if pdf.GetY() > 1.1 {
		pdf.Ln(0.12)
	}
	pdf.SetTextColor(20, 20, 20)
	pdf.SetFont("Go", "B", sizes[depth])
	pdf.MultiCell(0, sizes[depth]/55, cleanPDFText(title), "", "L", false)
	pdf.Ln(0.12)
}

func renderMarkdownPDF(pdf *fpdf.Fpdf, source string, assets map[int64]BookAsset, settings PrintSettings) {
	for _, block := range parsePrintBlocks(source) {
		switch block.Kind {
		case "heading":
			renderPrintHeading(pdf, block.Text, block.Level)
		case "bullet":
			pdf.SetFont("Go", "", settings.FontSizePt)
			pdf.MultiCell(0, settings.LineHeightPt/72, "• "+cleanPDFText(block.Text), "", "L", false)
		case "quote":
			pdf.SetFont("Go", "I", settings.FontSizePt)
			pdf.SetTextColor(65, 65, 65)
			pdf.MultiCell(0, settings.LineHeightPt/72, cleanPDFText(block.Text), "L", "L", false)
			pdf.SetTextColor(20, 20, 20)
		case "image":
			if asset, ok := assets[block.AssetID]; ok && asset.Kind == "interior_image" {
				renderPDFImage(pdf, asset, settings)
			}
		default:
			pdf.SetFont("Go", "", settings.FontSizePt)
			pdf.SetTextColor(20, 20, 20)
			pdf.MultiCell(0, settings.LineHeightPt/72, cleanPDFText(block.Text), "", "J", false)
			pdf.Ln(0.08)
		}
	}
}

func renderPDFImage(pdf *fpdf.Fpdf, asset BookAsset, settings PrintSettings) {
	typeName := "PNG"
	if asset.ContentType == "image/jpeg" {
		typeName = "JPG"
	}
	name := "asset-" + strconv.FormatInt(asset.ID, 10)
	options := fpdf.ImageOptions{ImageType: typeName, ReadDpi: true}
	info := pdf.RegisterImageOptionsReader(name, options, bytes.NewReader(asset.Content))
	if info == nil || pdf.Error() != nil {
		return
	}
	maxWidth := settings.TrimWidthIn - settings.InnerMarginIn - settings.OuterMarginIn
	width, height := info.Extent()
	if width <= 0 || height <= 0 {
		return
	}
	renderWidth := maxWidth
	renderHeight := renderWidth * height / width
	maxHeight := settings.TrimHeightIn - settings.TopMarginIn - settings.BottomMarginIn - 0.5
	if renderHeight > maxHeight {
		renderHeight = maxHeight
		renderWidth = renderHeight * width / height
	}
	if pdf.GetY()+renderHeight > settings.TrimHeightIn-settings.BottomMarginIn {
		pdf.AddPage()
	}
	x := (settings.TrimWidthIn - renderWidth) / 2
	pdf.ImageOptions(name, x, pdf.GetY(), renderWidth, renderHeight, true, options, 0, "")
	if asset.Caption != "" {
		pdf.SetFont("Go", "I", 8.5)
		pdf.MultiCell(0, 0.17, cleanPDFText(asset.Caption), "", "C", false)
	}
	pdf.Ln(0.08)
}

func parsePrintBlocks(source string) []printBlock {
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	blocks := []printBlock{}
	paragraph := []string{}
	flush := func() {
		if len(paragraph) > 0 {
			blocks = append(blocks, printBlock{Kind: "paragraph", Text: strings.Join(paragraph, " ")})
			paragraph = nil
		}
	}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			flush()
			continue
		}
		if match := markdownImageRE.FindStringSubmatch(line); match != nil {
			flush()
			id, _ := strconv.ParseInt(match[2], 10, 64)
			blocks = append(blocks, printBlock{Kind: "image", Text: match[1], AssetID: id})
			continue
		}
		if strings.HasPrefix(line, "#") {
			level := len(line) - len(strings.TrimLeft(line, "#"))
			if level <= 6 && len(line) > level && line[level] == ' ' {
				flush()
				blocks = append(blocks, printBlock{Kind: "heading", Level: level, Text: strings.TrimSpace(line[level:])})
				continue
			}
		}
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
			flush()
			blocks = append(blocks, printBlock{Kind: "bullet", Text: strings.TrimSpace(line[2:])})
			continue
		}
		if strings.HasPrefix(line, "> ") {
			flush()
			blocks = append(blocks, printBlock{Kind: "quote", Text: strings.TrimSpace(line[2:])})
			continue
		}
		paragraph = append(paragraph, line)
	}
	flush()
	return blocks
}

func cleanPDFText(value string) string {
	value = markdownLinkRE.ReplaceAllString(value, "$1")
	value = markdownMarkRE.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "\u2011", "-")
	return strings.TrimSpace(value)
}

func writePrintTOC(pdf *fpdf.Fpdf, entries []tocEntry, startPage int, settings PrintSettings) {
	const entriesPerPage = 32
	for index, entry := range entries {
		page := startPage + index/entriesPerPage
		row := index % entriesPerPage
		pdf.SetPage(page)
		setFacingMargins(pdf, settings)
		if row == 0 {
			pdf.SetY(settings.TopMarginIn)
			pdf.SetFont("Go", "B", 18)
			pdf.CellFormat(0, 0.35, "Contents", "", 1, "L", false, 0, "")
			pdf.Ln(0.18)
		}
		left, _, right, _ := pdf.GetMargins()
		usable := settings.TrimWidthIn - left - right
		indent := float64(entry.Depth) * 0.18
		pdf.SetX(left + indent)
		pdf.SetFont("Go", "", 9.5)
		pdf.CellFormat(usable-indent-0.42, 0.21, cleanPDFText(entry.Title), "", 0, "L", false, 0, "")
		pdf.CellFormat(0.42, 0.21, strconv.Itoa(entry.Page), "", 1, "R", false, 0, "")
	}
	if len(entries) == 0 {
		pdf.SetPage(startPage)
		pdf.SetY(settings.TopMarginIn)
		pdf.SetFont("Go", "B", 18)
		pdf.CellFormat(0, 0.35, "Contents", "", 1, "L", false, 0, "")
	}
}

func countNodes(nodes []*BookNode) int {
	count := 0
	for _, node := range nodes {
		count++
		count += countNodes(node.Children)
	}
	return count
}

func editionLine(meta PublicationMetadata) string {
	if meta.Edition == "" {
		return ""
	}
	return "Edition: " + meta.Edition
}

func isbnLine(meta PublicationMetadata) string {
	if meta.PrintISBN == "" {
		return ""
	}
	return "ISBN: " + meta.PrintISBN
}
