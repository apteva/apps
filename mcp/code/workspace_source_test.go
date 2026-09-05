package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceSourceRoundTripPreservesModesAndSkipsRuntimeState(t *testing.T) {
	root := t.TempDir()
	writeTestSource(t, root, "src/main.sh", []byte("#!/bin/sh\necho one\n"), 0o755)
	writeTestSource(t, root, "package.json", []byte("{}\n"), 0o644)
	writeTestSource(t, root, "node_modules/pkg/index.js", []byte("cache"), 0o644)
	writeTestSource(t, root, ".git", []byte("gitdir: elsewhere\n"), 0o600)
	if err := os.Symlink("main.sh", filepath.Join(root, "src", "current.sh")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := buildSourceSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Entries["node_modules/pkg/index.js"]; ok {
		t.Fatal("dependency cache was included in source snapshot")
	}
	if _, ok := snapshot.Entries[".git"]; ok {
		t.Fatal("Git metadata was included in source snapshot")
	}
	roundTrip, err := parseSourceArchive(snapshot.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.Digest != snapshot.Digest {
		t.Fatalf("source digest changed across archive: %s != %s", roundTrip.Digest, snapshot.Digest)
	}
	if roundTrip.Entries["src/main.sh"].Mode.Perm() != 0o755 {
		t.Fatal("executable mode was not preserved")
	}
	if roundTrip.Entries["src/current.sh"].LinkTarget != "main.sh" {
		t.Fatal("safe relative symlink was not preserved")
	}
}

func TestApplyWorkspaceSourceKeepsCachesAndAppliesReviewedFiles(t *testing.T) {
	root := t.TempDir()
	writeTestSource(t, root, "src/old.txt", []byte("old"), 0o644)
	writeTestSource(t, root, "src/delete.txt", []byte("delete"), 0o644)
	writeTestSource(t, root, "node_modules/pkg/cache", []byte("keep"), 0o644)
	base, err := buildSourceSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	next, err := finishSourceSnapshot(map[string]sourceEntry{
		"src/old.txt": {Path: "src/old.txt", Mode: 0o644, Data: []byte("new")},
		"src/new.sh":  {Path: "src/new.sh", Mode: 0o755, Data: []byte("#!/bin/sh\n")},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	changes := diffSourceSnapshots(base, next)
	if len(changes) != 3 {
		t.Fatalf("changes=%+v, want modified/deleted/added", changes)
	}
	if err := applySourceSnapshot(root, base.Paths, next); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(root, "src", "old.txt")); string(body) != "new" {
		t.Fatalf("modified file=%q", body)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "delete.txt")); !os.IsNotExist(err) {
		t.Fatal("deleted workspace source still exists")
	}
	if body, _ := os.ReadFile(filepath.Join(root, "node_modules", "pkg", "cache")); string(body) != "keep" {
		t.Fatal("dependency cache was changed")
	}
	if info, err := os.Stat(filepath.Join(root, "src", "new.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("new executable mode=%v err=%v", info, err)
	}
}

func TestWorkspaceSourceRejectsEscapingSymlink(t *testing.T) {
	if err := validateSourceSymlink("src/link", "../../outside"); err == nil {
		t.Fatal("escaping symlink should be rejected")
	}
}

func TestScopedWorkspaceSourceIncludesEditableAndSupportPathsOnly(t *testing.T) {
	root := t.TempDir()
	writeTestSource(t, root, "mcp/books/main.go", []byte("package main\n"), 0o644)
	writeTestSource(t, root, "mcp/other/main.go", []byte("package main\n"), 0o644)
	writeTestSource(t, root, "go.work", []byte("go 1.25\n"), 0o644)

	snapshot, err := buildScopedSourceSnapshot(root, []string{"mcp/books/**"}, []string{"go.work"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"mcp/books/main.go", "go.work"} {
		if _, ok := snapshot.Entries[path]; !ok {
			t.Fatalf("scoped snapshot omitted %s", path)
		}
	}
	if _, ok := snapshot.Entries["mcp/other/main.go"]; ok {
		t.Fatal("scoped snapshot included another monorepo app")
	}
	if _, _, err := normalizeWorkspaceScope([]string{"../secret/**"}, nil); err == nil {
		t.Fatal("escaping workspace pattern should be rejected")
	}
}

func TestBooksMonorepoScopeFitsWorkspaceBoundary(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	snapshot, err := buildScopedSourceSnapshot(root,
		[]string{"mcp/books/**"},
		[]string{"package.json", "bun.lock", "scripts/build-panels.ts", "scripts/verify-panels.ts", "scripts/panel-react-surface.json"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Paths) == 0 || len(snapshot.Archive) == 0 {
		t.Fatal("Books scope produced an empty workspace source archive")
	}
	if _, ok := snapshot.Entries["mcp/code/main.go"]; ok {
		t.Fatal("Books scope included Code source")
	}
	if _, ok := snapshot.Entries["scripts/panel-react-surface.json"]; !ok {
		t.Fatal("Books scope omitted the portable panel verifier contract")
	}
}

func writeTestSource(t *testing.T, root, rel string, body []byte, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, body, mode); err != nil {
		t.Fatal(err)
	}
}
