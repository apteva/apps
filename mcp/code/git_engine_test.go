package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGitEngineStatusCommitAndDiff(t *testing.T) {
	root := t.TempDir()
	engine, err := newGitEngine(root)
	if err != nil {
		t.Skipf("Git unavailable: %v", err)
	}
	workTree := filepath.Join(root, "work")
	gitDir := filepath.Join(root, "metadata.git")
	if _, err := engine.run(context.Background(), "", "", nil,
		"init", "--separate-git-dir="+gitDir, "--initial-branch=main", workTree); err != nil {
		t.Fatal(err)
	}
	if err := engine.configure(context.Background(), workTree, gitDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workTree, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := engine.status(context.Background(), workTree, gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty || len(status.Changes) != 1 || status.Changes[0].Path != "hello.txt" {
		t.Fatalf("unexpected dirty status: %+v", status)
	}
	sha, err := engine.commit(context.Background(), workTree, gitDir, "initial", nil, "Test", "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if sha == "" {
		t.Fatal("empty commit SHA")
	}
	status, err = engine.status(context.Background(), workTree, gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Dirty || status.Branch != "main" {
		t.Fatalf("unexpected clean status: %+v", status)
	}
	if err := os.WriteFile(filepath.Join(workTree, "hello.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, truncated, err := engine.diff(context.Background(), workTree, gitDir, "", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || diff == "" {
		t.Fatalf("unexpected diff truncated=%v body=%q", truncated, diff)
	}
}

func TestParseGitPorcelainRename(t *testing.T) {
	changes := parseGitPorcelain([]byte("R  new.txt\x00old.txt\x00?? other.txt\x00"))
	if len(changes) != 2 {
		t.Fatalf("changes=%+v", changes)
	}
	if changes[0].Path != "new.txt" || changes[0].Original != "old.txt" {
		t.Fatalf("rename=%+v", changes[0])
	}
}

func TestGitEngineBranchesCreateAndSwitch(t *testing.T) {
	root := t.TempDir()
	engine, err := newGitEngine(root)
	if err != nil {
		t.Skipf("Git unavailable: %v", err)
	}
	workTree := filepath.Join(root, "work")
	gitDir := filepath.Join(root, "metadata.git")
	if _, err := engine.run(context.Background(), "", "", nil,
		"init", "--separate-git-dir="+gitDir, "--initial-branch=main", workTree); err != nil {
		t.Fatal(err)
	}
	if err := engine.configure(context.Background(), workTree, gitDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workTree, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.commit(context.Background(), workTree, gitDir, "initial", nil, "Test", "test@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := engine.createBranch(context.Background(), workTree, gitDir, "feature/test", ""); err != nil {
		t.Fatal(err)
	}
	branches, err := engine.branches(context.Background(), workTree, gitDir)
	if err != nil {
		t.Fatal(err)
	}
	foundMain, foundFeature := false, false
	for _, branch := range branches {
		switch branch.Name {
		case "main":
			foundMain = branch.Current
		case "feature/test":
			foundFeature = !branch.Current
		}
	}
	if !foundMain || !foundFeature {
		t.Fatalf("unexpected branches: %+v", branches)
	}
	if err := engine.switchBranch(context.Background(), workTree, gitDir, "feature/test"); err != nil {
		t.Fatal(err)
	}
	status, err := engine.status(context.Background(), workTree, gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "feature/test" {
		t.Fatalf("branch=%q, want feature/test", status.Branch)
	}
}
