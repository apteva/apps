package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type PackageReport struct {
	Platform  string               `json:"platform"`
	Files     []string             `json:"files"`
	Checklist PublicationChecklist `json:"checklist"`
	EPUB      ValidationReport     `json:"epub_validation"`
	PDF       PDFReport            `json:"pdf_validation"`
}

func renderPublicationPackage(book *Book, nodes []*BookNode, notes []BookNote, assets []BookAsset, platform string, includeNotes bool) ([]byte, PackageReport, error) {
	platform = normalizePlatform(platform)
	if platform == "" {
		return nil, PackageReport{}, errors.New("platform must be generic, kindle, apple_books, kobo, google_play, or print")
	}
	epubReport := ValidationReport{Valid: true, Validator: "not applicable", Checks: []string{}, Warnings: []string{}, Errors: []string{}}
	pdfReport := PDFReport{Valid: true, Warnings: []string{}}
	var epubData, pdfData []byte
	var err error
	if platform != "print" {
		epubData, epubReport, err = renderEPUB(book, nodes, notes, assets, includeNotes)
		if err != nil {
			return nil, PackageReport{}, err
		}
	}
	if platform == "generic" || platform == "print" {
		pdfData, pdfReport, err = renderPrintPDF(book, nodeTree(nodes), notes, assets, includeNotes)
		if err != nil {
			return nil, PackageReport{}, err
		}
	}
	checklist := buildChecklist(book, nodes, assets, platform, epubReport)
	report := PackageReport{Platform: platform, Checklist: checklist, EPUB: epubReport, PDF: pdfReport, Files: []string{}}
	base := slugFilename(book.Title)
	if base == "" {
		base = "book"
	}
	var output bytes.Buffer
	zw := zip.NewWriter(&output)
	add := func(name string, data []byte) error {
		report.Files = append(report.Files, name)
		return addZipFile(zw, name, data)
	}

	if platform != "print" {
		if err := add(base+".epub", epubData); err != nil {
			return nil, report, err
		}
	}
	if platform == "generic" || platform == "print" {
		if err := add(base+"-interior.pdf", pdfData); err != nil {
			return nil, report, err
		}
	}
	if cover := firstAssetOfKind(assets, "cover"); cover != nil && platform != "print" {
		if err := add(base+"-ebook-cover"+assetExtension(*cover), cover.Content); err != nil {
			return nil, report, err
		}
	}
	if printCover := firstAssetOfKind(assets, "print_cover"); printCover != nil && (platform == "generic" || platform == "kindle" || platform == "print") {
		if err := add(base+"-print-cover.pdf", printCover.Content); err != nil {
			return nil, report, err
		}
	}
	if err := add("metadata.json", metadataJSON(book)); err != nil {
		return nil, report, err
	}
	if platform != "print" {
		if err := add("EPUB-VALIDATION.md", []byte(validationMarkdown(epubReport))); err != nil {
			return nil, report, err
		}
	}
	if err := add("PUBLICATION-CHECKLIST.md", []byte(checklistMarkdown(checklist))); err != nil {
		return nil, report, err
	}
	if err := add("README.md", []byte(packageReadme(platform))); err != nil {
		return nil, report, err
	}
	jsonReport, _ := json.MarshalIndent(report, "", "  ")
	if err := add("package-report.json", append(jsonReport, '\n')); err != nil {
		return nil, report, err
	}
	if err := zw.Close(); err != nil {
		return nil, report, err
	}
	return output.Bytes(), report, nil
}

func normalizePlatform(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	platform = strings.ReplaceAll(platform, "-", "_")
	platform = strings.ReplaceAll(platform, " ", "_")
	if platform == "" {
		return "generic"
	}
	switch platform {
	case "generic", "kindle", "apple_books", "kobo", "google_play", "print":
		return platform
	default:
		return ""
	}
}

func packageReadme(platform string) string {
	label := strings.ReplaceAll(platform, "_", " ")
	return fmt.Sprintf(`# Apteva Books publication package

Target: %s

This package contains generated publication files, normalized metadata, an EPUB validation report, and a release checklist.

Before publishing:

1. Resolve every unchecked checklist item.
2. Run the EPUB through the target store's current previewer.
3. Confirm title, author, series, ISBN, and cover text match exactly.
4. For print, inspect every page and order a physical proof.
5. Enter pricing, rights, territories, tax, and payment details in the publisher portal.

This package prepares files for upload. It does not submit or publish them automatically.
`, label)
}
