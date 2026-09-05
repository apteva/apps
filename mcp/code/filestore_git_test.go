package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalFileStoreHidesGitMetadata(t *testing.T) {
	store := NewLocalFileStore(t.TempDir())
	if err := store.CreateRepo("demo"); err != nil {
		t.Fatal(err)
	}
	root := store.RepoPath("demo")
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("visible"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".GIT", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".GIT", "objects", "secret"), []byte("metadata"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := store.List("demo", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "visible.txt" {
		t.Fatalf("visible files=%+v", files)
	}
	total, err := store.TotalSize("demo")
	if err != nil {
		t.Fatal(err)
	}
	if total != int64(len("visible")) {
		t.Fatalf("total=%d, want %d", total, len("visible"))
	}
}
