package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	goldhtml "github.com/yuin/goldmark/renderer/html"
)

type ValidationReport struct {
	Valid     bool     `json:"valid"`
	Validator string   `json:"validator"`
	Checks    []string `json:"checks"`
	Warnings  []string `json:"warnings"`
	Errors    []string `json:"errors"`
}

type epubFile struct {
	Path        string
	ContentType string
	Properties  string
	Data        []byte
}

var assetReferenceRE = regexp.MustCompile(`asset:(?://)?([0-9]+)`)

func renderEPUB(book *Book, nodes []*BookNode, notes []BookNote, assets []BookAsset, includeNotes bool) ([]byte, ValidationReport, error) {
	assetByID := map[int64]BookAsset{}
	for _, asset := range assets {
		assetByID[asset.ID] = asset
	}
	files := []epubFile{
		{Path: "EPUB/styles.css", ContentType: "text/css", Data: []byte(epubCSS)},
	}

	cover := firstAssetOfKind(assets, "cover")
	if cover != nil {
		coverName := "images/cover" + assetExtension(*cover)
		files = append(files,
			epubFile{Path: "EPUB/" + coverName, ContentType: cover.ContentType, Properties: "cover-image", Data: cover.Content},
			epubFile{Path: "EPUB/cover.xhtml", ContentType: "application/xhtml+xml", Data: []byte(renderCoverXHTML(book, coverName, cover.AltText))},
		)
	}
	for _, asset := range assets {
		if asset.Kind != "interior_image" {
			continue
		}
		files = append(files, epubFile{
			Path:        "EPUB/images/asset-" + strconv.FormatInt(asset.ID, 10) + assetExtension(asset),
			ContentType: asset.ContentType,
			Data:        asset.Content,
		})
	}

	flat := flattenNodes(nodeTree(nodes))
	for _, node := range flat {
		body := resolveAssetReferences(node.BodyMarkdown, assetByID)
		htmlBody, err := markdownToXHTML(body)
		if err != nil {
			return nil, ValidationReport{}, err
		}
		files = append(files, epubFile{
			Path:        "EPUB/text/node-" + strconv.FormatInt(node.ID, 10) + ".xhtml",
			ContentType: "application/xhtml+xml",
			Data:        []byte(renderNodeXHTML(book, node, htmlBody)),
		})
	}
	if includeNotes && len(notes) > 0 {
		files = append(files, epubFile{Path: "EPUB/text/notes.xhtml", ContentType: "application/xhtml+xml", Data: []byte(renderNotesXHTML(book, notes))})
	}

	nav := renderNavigationXHTML(book, nodeTree(nodes), includeNotes && len(notes) > 0, cover != nil)
	files = append(files, epubFile{Path: "EPUB/nav.xhtml", ContentType: "application/xhtml+xml", Properties: "nav", Data: []byte(nav)})
	opf := renderPackageOPF(book, files, flat, cover != nil, includeNotes && len(notes) > 0)
	files = append(files, epubFile{Path: "EPUB/package.opf", ContentType: "application/oebps-package+xml", Data: []byte(opf)})

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mimeHeader := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	mimeWriter, err := zw.CreateHeader(mimeHeader)
	if err != nil {
		return nil, ValidationReport{}, err
	}
	if _, err := io.WriteString(mimeWriter, "application/epub+zip"); err != nil {
		return nil, ValidationReport{}, err
	}
	container := `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="EPUB/package.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`
	if err := addZipFile(zw, "META-INF/container.xml", []byte(container)); err != nil {
		return nil, ValidationReport{}, err
	}
	for _, file := range files {
		if err := addZipFile(zw, file.Path, file.Data); err != nil {
			return nil, ValidationReport{}, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, ValidationReport{}, err
	}
	report := validateEPUBBytes(buf.Bytes())
	return buf.Bytes(), report, nil
}

func markdownToXHTML(source string) (string, error) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(goldhtml.WithXHTML()),
	)
	var out bytes.Buffer
	if err := md.Convert([]byte(source), &out); err != nil {
		return "", err
	}
	return out.String(), nil
}

func resolveAssetReferences(markdown string, assets map[int64]BookAsset) string {
	return assetReferenceRE.ReplaceAllStringFunc(markdown, func(match string) string {
		parts := assetReferenceRE.FindStringSubmatch(match)
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		asset, ok := assets[id]
		if !ok || asset.Kind != "interior_image" {
			return match
		}
		return "../images/asset-" + strconv.FormatInt(id, 10) + assetExtension(asset)
	})
}

func renderCoverXHTML(book *Book, imagePath, alt string) string {
	if strings.TrimSpace(alt) == "" {
		alt = "Cover of " + book.Title
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="%s" dir="%s">
<head><title>Cover</title><link rel="stylesheet" type="text/css" href="styles.css"/></head>
<body class="cover"><section epub:type="cover"><img src="%s" alt="%s"/></section></body></html>`,
		html.EscapeString(book.Language), html.EscapeString(book.Publication.ReadingDirection), html.EscapeString(imagePath), html.EscapeString(alt))
}

func renderNodeXHTML(book *Book, node *BookNode, body string) string {
	semantic := epubSemantic(node.Type)
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="%s" dir="%s">
<head><title>%s</title><link rel="stylesheet" type="text/css" href="../styles.css"/></head>
<body><section epub:type="%s"><h1>%s</h1>%s</section></body></html>`,
		html.EscapeString(book.Language), html.EscapeString(book.Publication.ReadingDirection), html.EscapeString(node.Title),
		semantic, html.EscapeString(node.Title), body)
}

func renderNotesXHTML(book *Book, notes []BookNote) string {
	var body strings.Builder
	for _, note := range notes {
		body.WriteString("<section><h2>" + html.EscapeString(note.Title) + "</h2><p>" + html.EscapeString(note.Body) + "</p>")
		if note.URL != "" {
			body.WriteString(`<p><a href="` + html.EscapeString(note.URL) + `">` + html.EscapeString(note.URL) + `</a></p>`)
		}
		body.WriteString("</section>")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="%s" dir="%s">
<head><title>Notes</title><link rel="stylesheet" type="text/css" href="../styles.css"/></head>
<body><section epub:type="endnotes"><h1>Notes</h1>%s</section></body></html>`, html.EscapeString(book.Language), html.EscapeString(book.Publication.ReadingDirection), body.String())
}

func renderNavigationXHTML(book *Book, tree []*BookNode, withNotes, withCover bool) string {
	var list func([]*BookNode) string
	list = func(nodes []*BookNode) string {
		var b strings.Builder
		b.WriteString("<ol>")
		for _, node := range nodes {
			b.WriteString(`<li><a href="text/node-` + strconv.FormatInt(node.ID, 10) + `.xhtml">` + html.EscapeString(node.Title) + `</a>`)
			if len(node.Children) > 0 {
				b.WriteString(list(node.Children))
			}
			b.WriteString("</li>")
		}
		b.WriteString("</ol>")
		return b.String()
	}
	tocList := list(tree)
	if withNotes {
		tocList = strings.TrimSuffix(tocList, "</ol>") + `<li><a href="text/notes.xhtml">Notes</a></li></ol>`
	}
	landmarkItems := []string{}
	if withCover {
		landmarkItems = append(landmarkItems, `<li><a epub:type="cover" href="cover.xhtml">Cover</a></li>`)
	}
	flat := flattenNodes(tree)
	if len(flat) > 0 {
		landmarkItems = append(landmarkItems, `<li><a epub:type="bodymatter" href="text/node-`+strconv.FormatInt(flat[0].ID, 10)+`.xhtml">Start</a></li>`)
	}
	landmarks := ""
	if len(landmarkItems) > 0 {
		landmarks = `<nav epub:type="landmarks" hidden="hidden"><ol>` + strings.Join(landmarkItems, "") + `</ol></nav>`
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="%s" dir="%s">
<head><title>Table of Contents</title><link rel="stylesheet" type="text/css" href="styles.css"/></head>
<body><nav epub:type="toc" id="toc"><h1>Contents</h1>%s</nav>%s</body></html>`,
		html.EscapeString(book.Language), html.EscapeString(book.Publication.ReadingDirection), tocList, landmarks)
}

func renderPackageOPF(book *Book, files []epubFile, flat []*BookNode, withCover, withNotes bool) string {
	identifier := strings.TrimSpace(book.Publication.EbookISBN)
	if identifier == "" {
		identifier = "urn:apteva:book:" + strconv.FormatInt(book.ID, 10)
	}
	var manifest, spine strings.Builder
	for index, file := range files {
		id := fmt.Sprintf("item-%d", index+1)
		href := strings.TrimPrefix(file.Path, "EPUB/")
		properties := ""
		if file.Properties != "" {
			properties = ` properties="` + file.Properties + `"`
		}
		manifest.WriteString(fmt.Sprintf(`<item id="%s" href="%s" media-type="%s"%s/>`, id, html.EscapeString(href), file.ContentType, properties))
	}
	// Files are added in cover, assets, then node order. Resolve spine ids by href.
	if withCover {
		spine.WriteString(`<itemref idref="` + manifestIDFor(files, "EPUB/cover.xhtml") + `" linear="no"/>`)
	}
	for _, node := range flat {
		spine.WriteString(`<itemref idref="` + manifestIDFor(files, "EPUB/text/node-"+strconv.FormatInt(node.ID, 10)+".xhtml") + `"/>`)
	}
	if withNotes {
		spine.WriteString(`<itemref idref="` + manifestIDFor(files, "EPUB/text/notes.xhtml") + `"/>`)
	}
	meta := normalizePublication(book.Publication)
	var contributors strings.Builder
	for _, contributor := range meta.Contributors {
		if strings.TrimSpace(contributor.Name) != "" {
			contributors.WriteString(`<dc:contributor>` + html.EscapeString(contributor.Name) + `</dc:contributor>`)
		}
	}
	var publicationMeta strings.Builder
	if meta.PublicationDate != "" {
		publicationMeta.WriteString(`<dc:date>` + html.EscapeString(meta.PublicationDate) + `</dc:date>`)
	}
	for _, category := range meta.Categories {
		publicationMeta.WriteString(`<dc:subject>` + html.EscapeString(category) + `</dc:subject>`)
	}
	if meta.SeriesName != "" {
		publicationMeta.WriteString(`<meta property="belongs-to-collection" id="series">` + html.EscapeString(meta.SeriesName) + `</meta><meta refines="#series" property="collection-type">series</meta>`)
		if meta.SeriesNumber != "" {
			publicationMeta.WriteString(`<meta refines="#series" property="group-position">` + html.EscapeString(meta.SeriesNumber) + `</meta>`)
		}
	}
	publicationMeta.WriteString(`<meta property="schema:accessMode">textual</meta>`)
	for _, feature := range meta.AccessibilityFeatures {
		publicationMeta.WriteString(`<meta property="schema:accessibilityFeature">` + html.EscapeString(feature) + `</meta>`)
	}
	for _, hazard := range meta.AccessibilityHazards {
		publicationMeta.WriteString(`<meta property="schema:accessibilityHazard">` + html.EscapeString(hazard) + `</meta>`)
	}
	if meta.AccessibilitySummary != "" {
		publicationMeta.WriteString(`<meta property="schema:accessibilitySummary">` + html.EscapeString(meta.AccessibilitySummary) + `</meta>`)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub-id" xml:lang="%s" dir="%s" prefix="schema: http://schema.org/">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="pub-id">%s</dc:identifier><dc:title>%s</dc:title><dc:language>%s</dc:language>
<dc:creator>%s</dc:creator>%s<dc:description>%s</dc:description><dc:publisher>%s</dc:publisher><dc:rights>%s</dc:rights>
%s<meta property="dcterms:modified">%s</meta>
</metadata><manifest>%s</manifest><spine page-progression-direction="%s">%s</spine></package>`,
		html.EscapeString(book.Language), html.EscapeString(meta.ReadingDirection), html.EscapeString(identifier), html.EscapeString(book.Title),
		html.EscapeString(book.Language), html.EscapeString(book.AuthorName), contributors.String(), html.EscapeString(book.Description), html.EscapeString(meta.Publisher), html.EscapeString(meta.RightsStatement),
		publicationMeta.String(), time.Now().UTC().Format("2006-01-02T15:04:05Z"), manifest.String(), html.EscapeString(meta.ReadingDirection), spine.String())
}

func manifestIDFor(files []epubFile, path string) string {
	for index, file := range files {
		if file.Path == path {
			return fmt.Sprintf("item-%d", index+1)
		}
	}
	return ""
}

func epubSemantic(nodeType string) string {
	switch nodeType {
	case "front_matter":
		return "frontmatter"
	case "back_matter":
		return "backmatter"
	case "appendix":
		return "appendix"
	case "chapter":
		return "chapter"
	case "part":
		return "part"
	default:
		return "section"
	}
}

func flattenNodes(tree []*BookNode) []*BookNode {
	out := []*BookNode{}
	var walk func([]*BookNode)
	walk = func(nodes []*BookNode) {
		for _, node := range nodes {
			out = append(out, node)
			walk(node.Children)
		}
	}
	walk(tree)
	return out
}

func addZipFile(zw *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetModTime(time.Unix(0, 0))
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func validateEPUBBytes(data []byte) ValidationReport {
	report := ValidationReport{Valid: true, Validator: "Books EPUB preflight", Checks: []string{}, Warnings: []string{}, Errors: []string{}}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		report.Valid = false
		report.Errors = append(report.Errors, "archive is not a readable ZIP: "+err.Error())
		return report
	}
	files := map[string]*zip.File{}
	for _, file := range zr.File {
		files[file.Name] = file
	}
	if len(zr.File) == 0 || zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store {
		report.Errors = append(report.Errors, "mimetype must be the first uncompressed ZIP entry")
	} else if string(readZipEntry(zr.File[0])) != "application/epub+zip" {
		report.Errors = append(report.Errors, "invalid EPUB mimetype")
	} else {
		report.Checks = append(report.Checks, "EPUB mimetype and ZIP ordering")
	}
	for _, required := range []string{"META-INF/container.xml", "EPUB/package.opf", "EPUB/nav.xhtml", "EPUB/styles.css"} {
		if files[required] == nil {
			report.Errors = append(report.Errors, "missing "+required)
		}
	}
	for name, file := range files {
		if strings.HasSuffix(name, ".xhtml") || strings.HasSuffix(name, ".opf") || strings.HasSuffix(name, ".xml") {
			decoder := xml.NewDecoder(bytes.NewReader(readZipEntry(file)))
			for {
				_, err := decoder.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					report.Errors = append(report.Errors, name+" is not valid XML: "+err.Error())
					break
				}
			}
		}
	}
	if len(report.Errors) == 0 {
		report.Checks = append(report.Checks, "Container, package, navigation, and XHTML are well formed")
	}
	report.Valid = len(report.Errors) == 0
	if external, ok := runExternalEPUBCheck(data); ok {
		report.Validator = external.Validator
		report.Warnings = append(report.Warnings, external.Warnings...)
		report.Errors = append(report.Errors, external.Errors...)
		report.Checks = append(report.Checks, external.Checks...)
		report.Valid = len(report.Errors) == 0
	} else {
		report.Warnings = append(report.Warnings, "W3C EPUBCheck executable not available; built-in structural preflight completed")
	}
	return report
}

func runExternalEPUBCheck(data []byte) (ValidationReport, bool) {
	command, commandArgs, err := epubCheckCommand()
	if err != nil {
		return ValidationReport{}, false
	}
	tmp, err := os.MkdirTemp("", "books-epubcheck-")
	if err != nil {
		return ValidationReport{}, false
	}
	defer os.RemoveAll(tmp)
	path := filepath.Join(tmp, "book.epub")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return ValidationReport{}, false
	}
	commandArgs = append(commandArgs, path)
	out, runErr := exec.Command(command, commandArgs...).CombinedOutput()
	report := ValidationReport{Valid: runErr == nil, Validator: "W3C EPUBCheck", Checks: []string{}, Warnings: []string{}, Errors: []string{}}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "ERROR(") || strings.HasPrefix(upper, "FATAL(") || strings.HasPrefix(upper, "ERROR:") || strings.HasPrefix(upper, "FATAL:") {
			report.Errors = append(report.Errors, line)
		} else if strings.HasPrefix(upper, "WARNING(") || strings.HasPrefix(upper, "WARNING:") || strings.HasPrefix(upper, "WARN(") || strings.HasPrefix(upper, "WARN:") {
			report.Warnings = append(report.Warnings, line)
		}
	}
	if runErr == nil {
		report.Checks = append(report.Checks, "W3C EPUBCheck passed")
	} else if len(report.Errors) == 0 {
		report.Errors = append(report.Errors, "EPUBCheck failed: "+runErr.Error())
	}
	return report, true
}

func epubCheckCommand() (string, []string, error) {
	if command, err := exec.LookPath("epubcheck"); err == nil {
		return command, nil, nil
	}
	jar := strings.TrimSpace(os.Getenv("EPUBCHECK_JAR"))
	if jar == "" {
		for _, candidate := range []string{"/opt/epubcheck/epubcheck.jar", "/usr/local/share/epubcheck/epubcheck.jar"} {
			if _, err := os.Stat(candidate); err == nil {
				jar = candidate
				break
			}
		}
	}
	if jar == "" {
		return "", nil, exec.ErrNotFound
	}
	java, err := exec.LookPath("java")
	if err != nil {
		return "", nil, err
	}
	return java, []string{"-jar", jar}, nil
}

func readZipEntry(file *zip.File) []byte {
	r, err := file.Open()
	if err != nil {
		return nil
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	return b
}

const epubCSS = `
html { color: #1a1a1a; background: #fff; }
body { font-family: serif; line-height: 1.55; margin: 5%; }
h1 { break-before: page; font-size: 1.8em; margin: 2em 0 1em; }
h2, h3 { break-after: avoid; margin-top: 1.5em; }
p { margin: 0 0 0.8em; text-indent: 1.2em; }
h1 + p, h2 + p, h3 + p { text-indent: 0; }
img { display: block; max-width: 100%; height: auto; margin: 1em auto; }
.cover { margin: 0; padding: 0; text-align: center; }
.cover img { width: 100%; height: auto; margin: 0; }
nav ol { list-style-type: none; padding-left: 1.2em; }
nav li { margin: 0.4em 0; }
`

func validationMarkdown(report ValidationReport) string {
	var b strings.Builder
	b.WriteString("# EPUB validation report\n\n")
	b.WriteString("Validator: " + report.Validator + "\n\n")
	if report.Valid {
		b.WriteString("Result: PASS\n\n")
	} else {
		b.WriteString("Result: FAIL\n\n")
	}
	for _, check := range report.Checks {
		b.WriteString("- PASS: " + check + "\n")
	}
	for _, warning := range report.Warnings {
		b.WriteString("- WARNING: " + warning + "\n")
	}
	for _, issue := range report.Errors {
		b.WriteString("- ERROR: " + issue + "\n")
	}
	return b.String()
}

func sortedEPUBFiles(files []epubFile) []epubFile {
	out := append([]epubFile(nil), files...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
