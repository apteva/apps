package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	files, _ := filepath.Glob("migrations/*.sql")
	sort.Strings(files)
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	return db
}

func TestFoldersStartEmptyAndFileNotes(t *testing.T) {
	db := testDB(t)
	const p = "proj"

	folders, err := listFolders(db, p)
	if err != nil || len(folders) != 0 {
		t.Fatalf("want no folders by default, got %v %v", folders, err)
	}

	work, err := createFolder(db, p, "  Work  ")
	if err != nil {
		t.Fatal(err)
	}
	if work.Name != "Work" {
		t.Fatalf("name not trimmed: %q", work.Name)
	}
	if _, err := createFolder(db, p, "work"); err == nil {
		t.Fatal("duplicate folder name (case-insensitive) should fail")
	}

	// Filed by id, by name (existing), and by a new name (auto-created).
	a, err := createNoteFromArgs(db, p, map[string]any{"title": "A", "folder_id": float64(work.ID)})
	if err != nil || a.FolderID != work.ID {
		t.Fatalf("folder_id create: %+v %v", a, err)
	}
	b, err := createNoteFromArgs(db, p, map[string]any{"title": "B", "folder": "WORK"})
	if err != nil || b.FolderID != work.ID {
		t.Fatalf("folder name create: %+v %v", b, err)
	}
	c, err := createNoteFromArgs(db, p, map[string]any{"title": "C", "folder": "Ideas"})
	if err != nil || c.FolderID == 0 || c.FolderID == work.ID {
		t.Fatalf("new folder by name: %+v %v", c, err)
	}
	d, err := createNoteFromArgs(db, p, map[string]any{"title": "D"})
	if err != nil || d.FolderID != 0 {
		t.Fatalf("unfiled note: %+v %v", d, err)
	}
	if _, err := createNoteFromArgs(db, p, map[string]any{"title": "E", "folder_id": float64(9999)}); err == nil {
		t.Fatal("unknown folder_id should fail")
	}

	inWork, err := listNotes(db, p, noteFilter{FolderID: &work.ID})
	if err != nil || len(inWork) != 2 {
		t.Fatalf("want 2 notes in Work, got %d %v", len(inWork), err)
	}
	folders, _ = listFolders(db, p)
	if len(folders) != 2 || folders[0].Name != "Ideas" || folders[1].NoteCount != 2 {
		t.Fatalf("folders: %+v", folders)
	}

	// Move a note out with folder_id 0.
	moved, err := updateNoteFromArgs(db, p, map[string]any{"id": float64(a.ID), "folder_id": float64(0)})
	if err != nil || moved.FolderID != 0 {
		t.Fatalf("unfile: %+v %v", moved, err)
	}

	renamed, err := renameFolder(db, p, work.ID, "Job")
	if err != nil || renamed.Name != "Job" {
		t.Fatalf("rename: %+v %v", renamed, err)
	}

	n, err := deleteFolder(db, p, work.ID)
	if err != nil || n != 1 {
		t.Fatalf("delete moved %d notes, err %v", n, err)
	}
	kept, err := getNote(db, p, b.ID)
	if err != nil || kept == nil || kept.FolderID != 0 {
		t.Fatalf("note should survive folder delete, unfiled: %+v %v", kept, err)
	}
	if _, err := deleteFolder(db, p, work.ID); err == nil {
		t.Fatal("second delete should report not found")
	}

	// Folders are per project.
	other, _ := listFolders(db, "other")
	if len(other) != 0 {
		t.Fatalf("folders leaked across projects: %+v", other)
	}
}
