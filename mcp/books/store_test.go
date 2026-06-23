package main

import (
	"database/sql"
	"os"
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
	schema, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
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
