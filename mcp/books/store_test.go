package main

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrations, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(migrations)
	for _, migration := range migrations {
		schema, err := os.ReadFile(migration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatalf("apply %s: %v", migration, err)
		}
	}
	return db
}

func TestPublicationAssetsAndExports(t *testing.T) {
	db := testDB(t)
	book, err := createBook(db, "p1", &Book{
		Title: "Shipping a Small Book", Subtitle: "A practical field guide", AuthorName: "Ada Writer",
		Description: "A concise guide to publishing reliable books.", Language: "en",
		Publication: PublicationMetadata{
			Publisher: "Example Press", Categories: []string{"Technology"}, Keywords: []string{"publishing", "books"},
			RightsStatement: "All rights reserved.", Territories: []string{"WORLD"}, AIDisclosure: "none",
			EbookISBN: "9780000000002", PrintISBN: "9780000000003",
			Prices: []BookPrice{{Marketplace: "US", Currency: "USD", Amount: 4.99}},
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	node, err := createNode(db, &BookNode{BookID: book.ID, Type: "chapter", Title: "Begin", BodyMarkdown: "A first paragraph.\n\n- One\n- Two", Position: -1})
	if err != nil {
		t.Fatal(err)
	}
	coverImage := image.NewRGBA(image.Rect(0, 0, 625, 1000))
	for y := 0; y < 1000; y++ {
		for x := 0; x < 625; x++ {
			coverImage.Set(x, y, color.RGBA{R: 24, G: uint8(60 + y%80), B: 145, A: 255})
		}
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, coverImage); err != nil {
		t.Fatal(err)
	}
	cover, err := createAsset(db, &BookAsset{BookID: book.ID, Kind: "cover", Filename: "cover.png", ContentType: "image/png", AltText: "Blue book cover", Content: pngBytes.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	if cover.SHA256 == "" || cover.SizeBytes != pngBytes.Len() || cover.WidthPX != 625 || cover.HeightPX != 1000 {
		t.Fatalf("asset metadata not populated: %#v", cover)
	}
	interior, err := createAsset(db, &BookAsset{BookID: book.ID, NodeID: &node.ID, Kind: "interior_image", Filename: "workflow.png", ContentType: "image/png", AltText: "Publication workflow diagram", Caption: "The publication workflow", Content: pngBytes.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	if err := updateNode(db, node.ID, map[string]any{"body_markdown": "A first paragraph.\n\n- One\n- Two\n\n![Publication workflow](asset:" + strconv.FormatInt(interior.ID, 10) + ")"}); err != nil {
		t.Fatal(err)
	}
	assets, err := listAssetsWithContent(db, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	book, err = getBook(db, book.ID, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if book.Publication.Publisher != "Example Press" || book.Publication.Print.TrimWidthIn != 6 {
		t.Fatalf("publication metadata not persisted: %#v", book.Publication)
	}
	nodes, _ := listNodes(db, book.ID)
	epub, validation, err := renderEPUB(book, nodes, nil, assets, false)
	if err != nil {
		t.Fatal(err)
	}
	if !validation.Valid || len(epub) < 1000 {
		t.Fatalf("invalid EPUB: %#v (%d bytes)", validation, len(epub))
	}
	pdf, pdfReport, err := renderPrintPDF(book, nodeTree(nodes), nil, assets, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !pdfReport.Valid || pdfReport.PageCount < 4 {
		t.Fatalf("invalid PDF: %#v (%d bytes)", pdfReport, len(pdf))
	}
	if outputDir := os.Getenv("BOOKS_QA_OUTPUT_DIR"); outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outputDir, "books-publication-sample.pdf"), pdf, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outputDir, "books-publication-sample.epub"), epub, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkg, packageReport, err := renderPublicationPackage(book, nodes, nil, assets, "kindle", false)
	if err != nil {
		t.Fatal(err)
	}
	if packageReport.EPUB.Validator == "W3C EPUBCheck" && !packageReport.Checklist.Ready {
		t.Fatalf("expected complete checklist after EPUBCheck: %#v", packageReport.Checklist)
	}
	if packageReport.EPUB.Validator != "W3C EPUBCheck" && packageReport.Checklist.Ready {
		t.Fatalf("checklist must require official EPUBCheck: %#v", packageReport.Checklist)
	}
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"shipping-a-small-book.epub": false, "shipping-a-small-book-ebook-cover.png": false, "metadata.json": false, "PUBLICATION-CHECKLIST.md": false}
	for _, file := range zr.File {
		if _, ok := want[file.Name]; ok {
			want[file.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("package missing %s", name)
		}
	}
	if node.ID == 0 {
		t.Fatal("node was not created")
	}
}

func TestBookNodesRevisionsAndExport(t *testing.T) {
	db := testDB(t)
	book, err := createBook(db, "p1", &Book{Title: "Practical Systems", AuthorName: "Apteva"}, false)
	if err != nil {
		t.Fatal(err)
	}
	ch1, err := createNode(db, &BookNode{BookID: book.ID, Type: "chapter", Title: "First", BodyMarkdown: "Alpha beta.", Position: -1})
	if err != nil {
		t.Fatal(err)
	}
	ch2, err := createNode(db, &BookNode{BookID: book.ID, Type: "chapter", Title: "Second", BodyMarkdown: "Gamma delta.", Position: -1})
	if err != nil {
		t.Fatal(err)
	}
	if ch1.Position != 0 || ch2.Position != 1 {
		t.Fatalf("positions = %d,%d; want 0,1", ch1.Position, ch2.Position)
	}

	if err := moveNode(db, ch2.ID, nil, 0); err != nil {
		t.Fatal(err)
	}
	nodes, err := listNodes(db, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	tree := nodeTree(nodes)
	if len(tree) != 2 || tree[0].ID != ch2.ID || tree[1].ID != ch1.ID {
		t.Fatalf("unexpected tree order: %#v", tree)
	}

	if err := updateNode(db, ch1.ID, map[string]any{"body_markdown": "Alpha beta gamma.", "change_summary": "add gamma"}); err != nil {
		t.Fatal(err)
	}
	revs, err := listRevisions(db, ch1.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 1 || revs[0].BodyMarkdown != "Alpha beta." {
		t.Fatalf("revision not captured: %#v", revs)
	}

	updated, err := getNode(db, ch1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ActualWordCount != 3 {
		t.Fatalf("word count = %d, want 3", updated.ActualWordCount)
	}

	if err := restoreRevision(db, revs[0].ID); err != nil {
		t.Fatal(err)
	}
	restored, err := getNode(db, ch1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.BodyMarkdown != "Alpha beta." {
		t.Fatalf("restore body = %q", restored.BodyMarkdown)
	}

	book, err = getBook(db, book.ID, "p1")
	if err != nil {
		t.Fatal(err)
	}
	nodes, err = listNodes(db, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	md := renderMarkdownExport(book, nodes, false)
	second := strings.Index(md, "# Second")
	first := strings.Index(md, "# First")
	if second < 0 || first < 0 || second > first {
		t.Fatalf("export order wrong:\n%s", md)
	}
}

func TestNotesCRUD(t *testing.T) {
	db := testDB(t)
	book, err := createBook(db, "p1", &Book{Title: "Notes Book"}, false)
	if err != nil {
		t.Fatal(err)
	}
	note, err := createNote(db, &BookNote{BookID: book.ID, Type: "research", Title: "Source", Body: "Useful reference", Tags: []string{"source"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := updateNote(db, note.ID, map[string]any{"title": "Primary Source", "tags": []any{"source", "todo"}}); err != nil {
		t.Fatal(err)
	}
	notes, err := listNotes(db, book.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Title != "Primary Source" || len(notes[0].Tags) != 2 {
		t.Fatalf("unexpected notes: %#v", notes)
	}
	if err := deleteNote(db, note.ID); err != nil {
		t.Fatal(err)
	}
	notes, err = listNotes(db, book.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("deleted note still listed: %#v", notes)
	}
}
