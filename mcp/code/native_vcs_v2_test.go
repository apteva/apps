package main

// Tier 1 coverage for native storage format 2: format-1 migration and
// rollback compatibility, the change index, folder trees, compare-and-swap
// refs, ref logs and repos_undo, garbage collection, and lock discipline.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFile writes a working-tree file and backdates it out of the racy
// window so the change index can trust it.
func writeOld(t *testing.T, store *LocalFileStore, repo *Repo, path, body string) {
	t.Helper()
	if _, err := store.Write(repoStoreKey(repo), path, []byte(body)); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(store.RepoPath(repoStoreKey(repo)), filepath.FromSlash(path)), old, old); err != nil {
		t.Fatal(err)
	}
}

func mustCheckpoint(t *testing.T, n *nativeVCS, repo *Repo, msg string) *nativeRevision {
	t.Helper()
	rev, err := n.checkpoint(repo, msg, "test")
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

func readWorking(t *testing.T, store FileStore, repo *Repo, path string) string {
	t.Helper()
	body, err := store.Read(repoStoreKey(repo), path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// writeFormat1Repo lays out a repository exactly as Code 0.14 stored it: flat
// tree manifests, revisions without a root, ref files, HEAD, no format file
// and no logs. It returns the revision IDs oldest first.
func writeFormat1Repo(t *testing.T, n *nativeVCS, repo *Repo, versions []map[string]string) []string {
	t.Helper()
	dir := n.repoDir(repo)
	for _, sub := range []string{"blobs", "revisions", "refs/heads", "refs/tags"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{}
	parent := ""
	for i, files := range versions {
		entries := map[string]nativeEntry{}
		for path, body := range files {
			blob := hashBytes([]byte(body))
			if err := os.WriteFile(filepath.Join(dir, "blobs", blob), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			entries[path] = nativeEntry{Blob: blob, Mode: 0o644}
		}
		tree := flatTreeHash(entries)
		body, _ := json.Marshal(entries)
		if err := os.WriteFile(filepath.Join(dir, "revisions", "tree-"+tree+".json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		created := time.Date(2026, 10, 1, 12, i, 0, 0, time.UTC).Format(time.RFC3339Nano)
		seed, _ := json.Marshal(struct {
			Tree      string   `json:"tree"`
			Parents   []string `json:"parents,omitempty"`
			Message   string   `json:"message"`
			Actor     string   `json:"actor,omitempty"`
			CreatedAt string   `json:"created_at"`
		}{tree, nonEmpty(parent), fmt.Sprintf("v1 revision %d", i), "legacy", created})
		// Format-1 revision JSON: no "root" field.
		rev := map[string]any{"id": hashBytes(seed), "tree": tree, "message": fmt.Sprintf("v1 revision %d", i), "actor": "legacy", "created_at": created}
		if parent != "" {
			rev["parents"] = []string{parent}
		}
		revBody, _ := json.Marshal(rev)
		if err := os.WriteFile(filepath.Join(dir, "revisions", hashBytes(seed)+".json"), revBody, 0o600); err != nil {
			t.Fatal(err)
		}
		parent = hashBytes(seed)
		ids = append(ids, parent)
	}
	if err := os.WriteFile(filepath.Join(dir, "refs", "heads", "main"), []byte(parent+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "refs", "tags", "v1"), []byte(ids[0]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestNativeV2_MigratesFormat1RepoWithoutChangingHistory(t *testing.T) {
	n, repo, store := nativeFixture(t)
	versions := []map[string]string{
		{"README.md": "one\n"},
		{"README.md": "two\n", "src/app.go": "package app\n"},
	}
	ids := writeFormat1Repo(t, n, repo, versions)
	for path, body := range versions[1] {
		writeOld(t, store, repo, path, body)
	}

	status, err := n.status(repo)
	if err != nil {
		t.Fatal(err)
	}
	if status.Dirty || status.Head != ids[1] || status.Branch != "main" || status.Revision != 2 {
		t.Fatalf("status after migration=%+v", status)
	}
	if body, _ := os.ReadFile(n.formatPath(repo)); strings.TrimSpace(string(body)) != "2" {
		t.Fatalf("format=%q", body)
	}
	history, err := n.history(repo, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].ID != ids[1] || history[1].ID != ids[0] {
		t.Fatalf("history changed by migration: %+v", history)
	}
	if tags, _ := n.tags(repo); tags["v1"] != ids[0] {
		t.Fatalf("tags=%v", tags)
	}
	_, moves, err := n.refMoves(repo, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 1 || moves[0].Action != "migrate" || moves[0].To != ids[1] {
		t.Fatalf("seeded moves=%+v", moves)
	}
	// Format-1 revisions are still exportable and diffable.
	if _, _, err := n.exportRef(repo, ids[0]); err != nil {
		t.Fatal(err)
	}
	_, _, paths, err := n.changedPaths(repo, ids[0], ids[1])
	if err != nil || strings.Join(paths, ",") != "README.md,src/app.go" {
		t.Fatalf("diff v1 revisions paths=%v err=%v", paths, err)
	}
	// Migration is idempotent: a second ensure writes no new log entries.
	if err := n.ensureRepoLocked(repo); err != nil {
		t.Fatal(err)
	}
	if _, moves, _ = n.refMoves(repo, "", 10); len(moves) != 1 {
		t.Fatalf("migration repeated: %+v", moves)
	}

	// A checkpoint after migration chains onto the v1 head and gets a root.
	writeOld(t, store, repo, "README.md", "three\n")
	rev := mustCheckpoint(t, n, repo, "after migration")
	if len(rev.Parents) != 1 || rev.Parents[0] != ids[1] || rev.Root == "" {
		t.Fatalf("new revision=%+v", rev)
	}
	_, _, paths, err = n.changedPaths(repo, ids[1], rev.ID)
	if err != nil || strings.Join(paths, ",") != "README.md" {
		t.Fatalf("mixed-format diff paths=%v err=%v", paths, err)
	}
}

func TestNativeV2_RevisionsStayReadableByFormat1Reader(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "a/b/c.txt", "deep\n")
	writeOld(t, store, repo, "top.txt", "top\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	writeOld(t, store, repo, "top.txt", "changed\n")
	rev := mustCheckpoint(t, n, repo, "v2")

	// What a 0.14 build does: read revision JSON, then revisions/tree-<tree>.json.
	body, err := os.ReadFile(filepath.Join(n.revisionsDir(repo), rev.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var v1 struct {
		ID   string `json:"id"`
		Tree string `json:"tree"`
	}
	if err := json.Unmarshal(body, &v1); err != nil || v1.ID != rev.ID {
		t.Fatalf("v1 decode id=%q err=%v", v1.ID, err)
	}
	flat, err := n.loadTree(repo, v1.Tree)
	if err != nil {
		t.Fatalf("0.14 reader cannot load tree: %v", err)
	}
	if len(flat) != 2 || flat["top.txt"].Blob != hashBytes([]byte("changed\n")) {
		t.Fatalf("flat tree=%v", flat)
	}
	// The folder tree reproduces exactly the same content.
	viaFolders := map[string]nativeEntry{}
	if err := n.readFolderTree(repo, rev.Root, "", viaFolders); err != nil {
		t.Fatal(err)
	}
	if flatTreeHash(viaFolders) != rev.Tree {
		t.Fatal("folder tree and flat tree disagree")
	}
}

// nativeReadCounter counts working-tree reads while keeping local paths, so the
// change index is active.
type nativeReadCounter struct {
	*LocalFileStore
	mu    sync.Mutex
	reads map[string]int
}

func (s *nativeReadCounter) Read(slug, path string) ([]byte, error) {
	s.mu.Lock()
	s.reads[path]++
	s.mu.Unlock()
	return s.LocalFileStore.Read(slug, path)
}

func (s *nativeReadCounter) take() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.reads
	s.reads = map[string]int{}
	return out
}

func countingFixture(t *testing.T) (*nativeVCS, *Repo, *nativeReadCounter) {
	t.Helper()
	root := t.TempDir()
	local := NewLocalFileStore(filepath.Join(root, "repos"))
	repo := &Repo{ID: 51, ProjectID: "p", Slug: "count", Name: "Count"}
	if err := local.CreateRepo(repoStoreKey(repo)); err != nil {
		t.Fatal(err)
	}
	store := &nativeReadCounter{LocalFileStore: local, reads: map[string]int{}}
	return newNativeVCS(filepath.Join(root, "data"), store, newRepoLockSet()), repo, store
}

func TestNativeV2_IndexSkipsUnchangedFiles(t *testing.T) {
	n, repo, store := countingFixture(t)
	for i := 0; i < 20; i++ {
		writeOld(t, store.LocalFileStore, repo, fmt.Sprintf("pkg%d/file.go", i), fmt.Sprintf("package p%d\n", i))
	}
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	if got := store.take(); len(got) != 20 {
		t.Fatalf("first snapshot read %d files, want all 20", len(got))
	}
	// Nothing changed: status and checkpoint read nothing.
	if s, err := n.status(repo); err != nil || s.Dirty {
		t.Fatalf("status=%+v err=%v", s, err)
	}
	mustCheckpoint(t, n, repo, "noop")
	if got := store.take(); len(got) != 0 {
		t.Fatalf("unchanged snapshot reread %v", got)
	}
	// One file changes: only it is read.
	writeOld(t, store.LocalFileStore, repo, "pkg7/file.go", "package p7 // edited\n")
	rev := mustCheckpoint(t, n, repo, "edit one")
	if got := store.take(); len(got) != 1 || got["pkg7/file.go"] != 1 {
		t.Fatalf("reads after one edit=%v", got)
	}
	if entries, err := n.revisionEntries(repo, rev); err != nil || entries["pkg7/file.go"].Blob != hashBytes([]byte("package p7 // edited\n")) {
		t.Fatalf("checkpoint content wrong: %v %v", entries["pkg7/file.go"], err)
	}
}

func TestNativeV2_IndexRehashesRacilyCleanFiles(t *testing.T) {
	n, repo, store := countingFixture(t)
	writeOld(t, store.LocalFileStore, repo, "old.txt", "old\n")
	if _, err := store.Write(repoStoreKey(repo), "fresh.txt", []byte("AAAA\n")); err != nil {
		t.Fatal(err)
	}
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	store.take()
	// Same size and same mtime as indexed, different bytes: only safe because
	// fresh.txt is inside the racy window and is rehashed.
	path := filepath.Join(store.RepoPath(repoStoreKey(repo)), "fresh.txt")
	info, _ := os.Stat(path)
	if err := os.WriteFile(path, []byte("BBBB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	status, err := n.status(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty {
		t.Fatal("same-size same-mtime edit inside the racy window was missed")
	}
	if got := store.take(); got["fresh.txt"] != 1 || got["old.txt"] != 0 {
		t.Fatalf("reads=%v", got)
	}
}

func TestNativeV2_DamagedIndexOnlyCostsRehash(t *testing.T) {
	n, repo, store := countingFixture(t)
	writeOld(t, store.LocalFileStore, repo, "a.txt", "a\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(n.indexPath(repo), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.take()
	if s, err := n.status(repo); err != nil || s.Dirty {
		t.Fatalf("status=%+v err=%v", s, err)
	}
	if got := store.take(); got["a.txt"] != 1 {
		t.Fatalf("damaged index did not fall back to reading: %v", got)
	}
}

func TestNativeV2_FolderTreesAreSharedAndDiffSkipsThem(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "lib/a.go", "a\n")
	writeOld(t, store, repo, "lib/deep/b.go", "b\n")
	writeOld(t, store, repo, "app/main.go", "main\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	first := mustCheckpoint(t, n, repo, "base")
	head, _ := n.status(repo)
	base, _ := n.loadRevision(repo, head.Head)
	writeOld(t, store, repo, "app/main.go", "main changed\n")
	writeOld(t, store, repo, "app/new.go", "new\n")
	second := mustCheckpoint(t, n, repo, "change app")

	libOf := func(rev *nativeRevision) string {
		children, err := n.loadFolderTree(repo, rev.Root)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range children {
			if c.Name == "lib" {
				return c.Hash
			}
		}
		t.Fatal("no lib folder")
		return ""
	}
	if libOf(base) != libOf(second) {
		t.Fatal("unchanged lib/ folder was not shared between revisions")
	}
	_ = first
	// Remove the lib subtree's objects: a folder-level diff must not need them.
	children, _ := n.loadFolderTree(repo, libOf(second))
	for _, c := range children {
		if c.Kind == "tree" {
			if err := os.Remove(filepath.Join(n.treesDir(repo), c.Hash+".json")); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, _, paths, err := n.changedPaths(repo, base.ID, second.ID)
	if err != nil {
		t.Fatalf("diff touched an unchanged folder: %v", err)
	}
	if strings.Join(paths, ",") != "app/main.go,app/new.go" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestNativeV2_FolderDiffHandlesFileFolderSwaps(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "x", "file\n")
	writeOld(t, store, repo, "keep.txt", "k\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	a := mustCheckpoint(t, n, repo, "x is a file")
	if err := store.Delete(repoStoreKey(repo), "x"); err != nil {
		t.Fatal(err)
	}
	writeOld(t, store, repo, "x/inner.txt", "now a folder\n")
	b := mustCheckpoint(t, n, repo, "x is a folder")
	_, _, paths, err := n.changedPaths(repo, a.ID, b.ID)
	if err != nil || strings.Join(paths, ",") != "x,x/inner.txt" {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
}

func TestNativeV2_TamperedObjectsFailIntegrity(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "a/b.txt", "b\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	writeOld(t, store, repo, "a/b.txt", "b2\n")
	rev := mustCheckpoint(t, n, repo, "x")
	children, _ := n.loadFolderTree(repo, rev.Root)
	tampered, _ := json.Marshal([]nativeTreeEntry{{Name: "evil", Kind: "blob", Hash: hashBytes([]byte("evil"))}})
	if err := os.WriteFile(filepath.Join(n.treesDir(repo), children[0].Hash+".json"), tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.loadFolderTree(repo, children[0].Hash); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("tampered tree accepted: %v", err)
	}
}

func TestNativeV2_ExpectedHeadRefusesStaleCheckpoint(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "f.txt", "1\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	seen, _ := n.status(repo)
	writeOld(t, store, repo, "f.txt", "2\n")
	other := mustCheckpoint(t, n, repo, "someone else")
	writeOld(t, store, repo, "f.txt", "3\n")
	_, err := n.checkpointExpect(repo, "mine", "agent", seen.Head)
	if !errors.Is(err, errNativeRefMoved) {
		t.Fatalf("stale expected_head accepted: %v", err)
	}
	if now, _ := n.status(repo); now.Head != other.ID || !now.Dirty {
		t.Fatalf("refused checkpoint moved the branch: %+v", now)
	}
	rev, err := n.checkpointExpect(repo, "mine", "agent", other.ID)
	if err != nil || rev.Parents[0] != other.ID {
		t.Fatalf("current expected_head refused: rev=%+v err=%v", rev, err)
	}
}

func TestNativeV2_ConcurrentCheckpointsWithSameExpectedHead(t *testing.T) {
	root := t.TempDir()
	raw := NewLocalFileStore(filepath.Join(root, "repos"))
	repo := &Repo{ID: 61, ProjectID: "p", Slug: "race", Name: "Race"}
	if err := raw.CreateRepo(repoStoreKey(repo)); err != nil {
		t.Fatal(err)
	}
	locks := newRepoLockSet()
	store := &lockedFileStore{inner: raw, locks: locks}
	n := newNativeVCS(filepath.Join(root, "data"), store, locks)
	if _, err := store.Write(repoStoreKey(repo), "f.txt", []byte("base\n")); err != nil {
		t.Fatal(err)
	}
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	base, _ := n.status(repo)
	if _, err := store.Write(repoStoreKey(repo), "f.txt", []byte("changed\n")); err != nil {
		t.Fatal(err)
	}
	const workers = 12
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := n.checkpointExpect(repo, fmt.Sprintf("worker %d", i), "agent", base.Head)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	ok, moved := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, errNativeRefMoved):
			moved++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || moved != workers-1 {
		t.Fatalf("ok=%d moved=%d; exactly one checkpoint may win", ok, moved)
	}
}

func TestNativeV2_ConcurrentEditsAndCheckpointsKeepALinearLog(t *testing.T) {
	root := t.TempDir()
	raw := NewLocalFileStore(filepath.Join(root, "repos"))
	repo := &Repo{ID: 62, ProjectID: "p", Slug: "linear", Name: "Linear"}
	if err := raw.CreateRepo(repoStoreKey(repo)); err != nil {
		t.Fatal(err)
	}
	locks := newRepoLockSet()
	store := &lockedFileStore{inner: raw, locks: locks}
	n := newNativeVCS(filepath.Join(root, "data"), store, locks)
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := store.Write(repoStoreKey(repo), fmt.Sprintf("w%d.txt", w), []byte(fmt.Sprintf("%d-%d\n", w, i))); err != nil {
					t.Error(err)
					return
				}
				if _, err := n.checkpoint(repo, fmt.Sprintf("w%d i%d", w, i), "agent"); err != nil {
					t.Error(err)
					return
				}
				if _, err := n.status(repo); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	log, err := n.readRefLog(repo, "main", false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(log); i++ {
		if log[i].Old != log[i-1].New {
			t.Fatalf("ref log is not linear at %d: %+v then %+v", i, log[i-1], log[i])
		}
	}
	head := n.readRef(repo, "main", false)
	if log[len(log)-1].New != head {
		t.Fatal("last logged move is not the branch head")
	}
	// Every logged revision is in first-parent history.
	history, _ := n.history(repo, 200)
	inHistory := map[string]bool{}
	for _, rev := range history {
		inHistory[rev.ID] = true
	}
	for _, e := range log {
		if !inHistory[e.New] {
			t.Fatalf("logged revision %s lost from history", e.New)
		}
	}
}

func TestNativeV2_UndoMovesCheckedOutBranchAndRestoresFiles(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "f.txt", "a\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	a := n.readRef(repo, "main", false)
	writeOld(t, store, repo, "f.txt", "b\n")
	writeOld(t, store, repo, "added.txt", "only in b\n")
	b := mustCheckpoint(t, n, repo, "b")

	_, moves, err := n.refMoves(repo, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 2 || moves[0].To != b.ID || moves[0].From != a || moves[1].Action != "init" {
		t.Fatalf("moves=%+v", moves)
	}
	_, move, err := n.undo(repo, "", a, "agent")
	if err != nil || move == nil || move.From != b.ID || move.To != a {
		t.Fatalf("undo move=%+v err=%v", move, err)
	}
	if got := readWorking(t, store, repo, "f.txt"); got != "a\n" {
		t.Fatalf("working tree not restored: %q", got)
	}
	if _, err := store.Read(repoStoreKey(repo), "added.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file added after the undo target still present: %v", err)
	}
	if s, _ := n.status(repo); s.Dirty || s.Head != a {
		t.Fatalf("status=%+v", s)
	}
	// The undone revision is still in the log, so the undo can be undone.
	if _, _, err := n.undo(repo, "", b.ID, "agent"); err != nil {
		t.Fatal(err)
	}
	if got := readWorking(t, store, repo, "added.txt"); got != "only in b\n" {
		t.Fatalf("redo did not restore: %q", got)
	}
	// Undoing to the current head is a no-op.
	if _, move, err := n.undo(repo, "", b.ID, "agent"); err != nil || move != nil {
		t.Fatalf("no-op undo move=%+v err=%v", move, err)
	}
}

func TestNativeV2_UndoRefusals(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "f.txt", "a\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	a := n.readRef(repo, "main", false)
	writeOld(t, store, repo, "f.txt", "b\n")
	b := mustCheckpoint(t, n, repo, "b")

	writeOld(t, store, repo, "f.txt", "unsaved\n")
	if _, _, err := n.undo(repo, "", a, "agent"); err == nil || !strings.Contains(err.Error(), "checkpoint them") {
		t.Fatalf("undo with unsaved changes: %v", err)
	}
	if got := readWorking(t, store, repo, "f.txt"); got != "unsaved\n" {
		t.Fatalf("refused undo touched the working tree: %q", got)
	}
	if n.readRef(repo, "main", false) != b.ID {
		t.Fatal("refused undo moved the branch")
	}
	stranger := strings.Repeat("ab", 32)
	if _, _, err := n.undo(repo, "", stranger, "agent"); err == nil || !strings.Contains(err.Error(), "never appears") {
		t.Fatalf("undo to a revision outside the log: %v", err)
	}
	if _, _, err := n.undo(repo, "missing", a, "agent"); err == nil {
		t.Fatal("undo on a missing branch accepted")
	}
}

func TestNativeV2_UndoOtherBranchLeavesWorkingTree(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "f.txt", "a\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	a := n.readRef(repo, "main", false)
	if err := n.createBranch(repo, "feature", a); err != nil {
		t.Fatal(err)
	}
	if err := n.switchBranch(repo, "feature"); err != nil {
		t.Fatal(err)
	}
	writeOld(t, store, repo, "f.txt", "feature\n")
	f := mustCheckpoint(t, n, repo, "feature work")
	if err := n.switchBranch(repo, "main"); err != nil {
		t.Fatal(err)
	}
	writeOld(t, store, repo, "f.txt", "main unsaved\n")
	if _, _, err := n.undo(repo, "feature", a, "agent"); err != nil {
		t.Fatal(err)
	}
	if n.readRef(repo, "feature", false) != a {
		t.Fatal("feature branch not moved")
	}
	if got := readWorking(t, store, repo, "f.txt"); got != "main unsaved\n" {
		t.Fatalf("undo of another branch touched the working tree: %q", got)
	}
	_, moves, _ := n.refMoves(repo, "feature", 10)
	if len(moves) != 3 || moves[0].Action != "undo" || moves[1].To != f.ID || moves[2].Action != "branch.create" {
		t.Fatalf("feature moves=%+v", moves)
	}
}

func TestNativeV2_RefLogSkipsTornLine(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "f.txt", "a\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(n.logPath(repo, "main", false), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"old":"abc","new":"de`)
	_ = f.Close()
	if _, moves, err := n.refMoves(repo, "", 10); err != nil || len(moves) != 1 {
		t.Fatalf("torn line: moves=%+v err=%v", moves, err)
	}
	writeOld(t, store, repo, "f.txt", "b\n")
	mustCheckpoint(t, n, repo, "after crash")
	log, _ := n.readRefLog(repo, "main", false)
	if len(log) != 2 || log[1].Message != "after crash" {
		t.Fatalf("entry after a torn line was lost: %+v", log)
	}
}

func TestNativeV2_GCRemovesOnlyOldUnreachableObjects(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "keep.txt", "keep\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	writeOld(t, store, repo, "keep.txt", "keep 2\n")
	rev := mustCheckpoint(t, n, repo, "two")

	old := time.Now().Add(-30 * 24 * time.Hour)
	stray := func(dir, name, body string, age time.Time) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, age, age); err != nil {
			t.Fatal(err)
		}
		return path
	}
	oldBlob := stray(n.blobsDir(repo), hashBytes([]byte("orphan")), "orphan", old)
	newBlob := stray(n.blobsDir(repo), hashBytes([]byte("fresh orphan")), "fresh orphan", time.Now())
	oldTree := stray(n.treesDir(repo), strings.Repeat("1", 64)+".json", "[]", old)
	oldRev := stray(n.revisionsDir(repo), strings.Repeat("2", 64)+".json", "{}", old)
	oldFlat := stray(n.revisionsDir(repo), "tree-"+strings.Repeat("3", 64)+".json", "{}", old)
	// Age every reachable object too: reachability, not age, must keep them.
	_ = filepath.WalkDir(n.repoDir(repo), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && path != newBlob {
			_ = os.Chtimes(path, old, old)
		}
		return nil
	})

	result, err := n.gc(repo, nativeGCGrace)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{oldBlob, oldTree, oldRev, oldFlat} {
		if fileExists(path) {
			t.Fatalf("unreachable old object kept: %s", path)
		}
	}
	if !fileExists(newBlob) {
		t.Fatal("unreachable object inside the grace period was removed")
	}
	if result.Blobs != 1 || result.Trees != 1 || result.Revisions != 2 {
		t.Fatalf("result=%+v", result)
	}
	// Everything reachable still works.
	history, err := n.history(repo, 10)
	if err != nil || len(history) != 2 {
		t.Fatalf("history after gc=%v err=%v", history, err)
	}
	for _, r := range history {
		if _, _, err := n.exportRef(repo, r.ID); err != nil {
			t.Fatalf("export %s after gc: %v", r.ID, err)
		}
	}
	if _, _, err := n.undo(repo, "", history[1].ID, "agent"); err != nil {
		t.Fatalf("undo after gc: %v", err)
	}
	_ = rev
}

func TestNativeV2_GCKeepsUndoneRevisions(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "f.txt", "a\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	a := n.readRef(repo, "main", false)
	writeOld(t, store, repo, "f.txt", "b\n")
	b := mustCheckpoint(t, n, repo, "b")
	if _, _, err := n.undo(repo, "", a, "agent"); err != nil {
		t.Fatal(err)
	}
	n.now = func() time.Time { return time.Now().Add(60 * 24 * time.Hour) }
	if _, err := n.gc(repo, nativeGCGrace); err != nil {
		t.Fatal(err)
	}
	n.now = time.Now
	if _, err := n.loadRevision(repo, b.ID); err != nil {
		t.Fatalf("undone revision collected although it is in the ref log: %v", err)
	}
	if _, _, err := n.undo(repo, "", b.ID, "agent"); err != nil {
		t.Fatalf("redo after gc: %v", err)
	}
}

func TestNativeV2_GCAbortsOnDamageAndWithoutBranches(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "f.txt", "a\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	first := n.readRef(repo, "main", false)
	writeOld(t, store, repo, "f.txt", "b\n")
	mustCheckpoint(t, n, repo, "b")
	old := time.Now().Add(-30 * 24 * time.Hour)
	orphan := filepath.Join(n.blobsDir(repo), hashBytes([]byte("orphan")))
	_ = os.WriteFile(orphan, []byte("orphan"), 0o600)
	_ = os.Chtimes(orphan, old, old)

	if err := os.Remove(filepath.Join(n.revisionsDir(repo), first+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := n.gc(repo, nativeGCGrace); err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("gc on a damaged history: %v", err)
	}
	if !fileExists(orphan) {
		t.Fatal("aborted gc still deleted objects")
	}
	if err := os.RemoveAll(n.refsDir(repo)); err != nil {
		t.Fatal(err)
	}
	if _, err := n.gc(repo, nativeGCGrace); err == nil || !strings.Contains(err.Error(), "no branches") {
		t.Fatalf("gc without branches: %v", err)
	}
	if !fileExists(orphan) {
		t.Fatal("gc without branches deleted objects")
	}
}

func TestNativeV2_MaybeGCRunsOncePerInterval(t *testing.T) {
	n, repo, store := nativeFixture(t)
	writeOld(t, store, repo, "f.txt", "a\n")
	if err := n.ensureRepo(repo); err != nil {
		t.Fatal(err)
	}
	if r, err := n.maybeGC(repo); err != nil || r == nil {
		t.Fatalf("first maybeGC r=%v err=%v", r, err)
	}
	if r, err := n.maybeGC(repo); err != nil || r != nil {
		t.Fatalf("second maybeGC ran again: r=%v err=%v", r, err)
	}
	n.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if r, err := n.maybeGC(repo); err != nil || r == nil {
		t.Fatalf("maybeGC after interval r=%v err=%v", r, err)
	}
}

// Regression: auto-sync's tick holds the repository write lock and used to
// call checkpoint(), which takes the same lock again and hung forever.
func TestNativeV2_CheckpointHeldUnderRepoLockDoesNotDeadlock(t *testing.T) {
	root := t.TempDir()
	raw := NewLocalFileStore(filepath.Join(root, "repos"))
	repo := &Repo{ID: 63, ProjectID: "p", Slug: "held", Name: "Held"}
	if err := raw.CreateRepo(repoStoreKey(repo)); err != nil {
		t.Fatal(err)
	}
	locks := newRepoLockSet()
	n := newNativeVCS(filepath.Join(root, "data"), &lockedFileStore{inner: raw, locks: locks}, locks)
	if _, err := raw.Write(repoStoreKey(repo), "f.txt", []byte("a\n")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		unlock := locks.lock(repoStoreKey(repo))
		defer unlock()
		_, err := n.checkpointHeld(repo, "Auto-save: update f.txt", "auto-sync")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("checkpoint under the auto-sync lock deadlocked")
	}
	if head := n.readRef(repo, "main", false); head == "" {
		t.Fatal("held checkpoint did not create a revision")
	}
}

func TestNativeV2_InvalidRefNamesStillRejected(t *testing.T) {
	for _, name := range []string{"", ".", "../x", "a/b", "a b", ".tmp-x", "x~1", "x:y"} {
		if _, err := normaliseRefName(name); err == nil {
			t.Errorf("ref name %q accepted", name)
		}
	}
	got := []string{}
	for _, name := range []string{"main", "feature-1", "agent.work", "v1.2.3"} {
		if _, err := normaliseRefName(name); err != nil {
			got = append(got, name)
		}
	}
	sort.Strings(got)
	if len(got) != 0 {
		t.Fatalf("valid names rejected: %v", got)
	}
}
