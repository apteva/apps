package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func nativeFixture(t *testing.T) (*nativeVCS, *Repo, *LocalFileStore) {
	t.Helper()
	root := t.TempDir()
	store := NewLocalFileStore(filepath.Join(root, "repos"))
	repo := &Repo{ID: 41, ProjectID: "p", Slug: "demo", Name: "Demo"}
	if err := store.CreateRepo(repoStoreKey(repo)); err != nil {
		t.Fatal(err)
	}
	n := newNativeVCS(filepath.Join(root, "data"), store, newRepoLockSet())
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return n, repo, store
}

func TestNativeVCS_CheckpointHistoryAndDirtyWorkingTree(t *testing.T) {
	n, repo, store := nativeFixture(t)
	if _, err := store.Write(repoStoreKey(repo), "main.go", []byte("package main\n")); err != nil {
		t.Fatal(err)
	}
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	history, err := n.history(repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Message != "Initial repository revision" {
		t.Fatalf("initial history=%+v", history)
	}
	if _, err := store.Write(repoStoreKey(repo), "main.go", []byte("package main\n\nfunc main() {}\n")); err != nil {
		t.Fatal(err)
	}
	status, err := n.status(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty {
		t.Fatal("working-tree edit was not detected")
	}
	rev, err := n.checkpoint(repo, "Add main", "agent:test")
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.ID) != 64 {
		t.Fatalf("revision id=%q", rev.ID)
	}
	status, err = n.status(repo)
	if err != nil {
		t.Fatal(err)
	}
	if status.Dirty || status.Head != rev.ID {
		t.Fatalf("status after checkpoint=%+v", status)
	}
	history, err = n.history(repo, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].ID != rev.ID {
		t.Fatalf("history=%+v", history)
	}
}

func TestNativeVCS_BranchTagRestoreAndExactExport(t *testing.T) {
	n, repo, store := nativeFixture(t)
	if _, err := store.Write(repoStoreKey(repo), "README.md", []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	base, err := n.checkpoint(repo, "Base", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := n.createBranch(repo, "feature", base.ID); err != nil {
		t.Fatal(err)
	}
	if err := n.createTag(repo, "v1", base.ID); err != nil {
		t.Fatal(err)
	}
	if tags, err := n.tags(repo); err != nil || tags["v1"] != base.ID {
		t.Fatalf("tags=%v err=%v", tags, err)
	}
	if _, err := store.Write(repoStoreKey(repo), "README.md", []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	changed, err := n.checkpoint(repo, "Change", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := n.restoreRevision(repo, base.ID, nil); err != nil {
		t.Fatal(err)
	}
	body, err := store.Read(repoStoreKey(repo), "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "one\n" {
		t.Fatalf("restored body=%q", body)
	}
	zipBytes, sha, err := n.exportRef(repo, changed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(zipBytes) == 0 || len(sha) != 64 {
		t.Fatalf("export size=%d sha=%q", len(zipBytes), sha)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("zip entries=%d", len(zr.File))
	}
	r, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	exported, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(exported) != "two\n" {
		t.Fatalf("exported=%q", exported)
	}
	if _, err := n.checkpoint(repo, "Revert", "test"); err != nil {
		t.Fatal(err)
	}
	if err := n.switchBranch(repo, "feature"); err != nil {
		t.Fatal(err)
	}
	status, err := n.status(repo)
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "feature" {
		t.Fatalf("status=%+v", status)
	}
}

func TestNativeVCS_InvalidRefsRejected(t *testing.T) {
	n, repo, store := nativeFixture(t)
	if _, err := store.Write(repoStoreKey(repo), "x.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := n.createBranch(repo, "../escape", ""); err == nil {
		t.Fatal("invalid branch accepted")
	}
	if err := n.createTag(repo, "v1/", ""); err == nil {
		t.Fatal("invalid tag accepted")
	}
}

func TestNativeVCS_CheckpointWithLockedStoreDoesNotDeadlock(t *testing.T) {
	root := t.TempDir()
	raw := NewLocalFileStore(filepath.Join(root, "repos"))
	repo := &Repo{ID: 42, ProjectID: "p", Slug: "locked", Name: "Locked"}
	if err := raw.CreateRepo(repoStoreKey(repo)); err != nil {
		t.Fatal(err)
	}
	locks := newRepoLockSet()
	store := &lockedFileStore{inner: raw, locks: locks}
	n := newNativeVCS(filepath.Join(root, "data"), store, locks)
	if _, err := store.Write(repoStoreKey(repo), "README.md", []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(repoStoreKey(repo), "README.md", []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := n.checkpoint(repo, "Change", "test")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("checkpoint deadlocked while using the app's locked file store")
	}
}
