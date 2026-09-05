package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func auditStore(t *testing.T) *LocalFileStore {
	t.Helper()
	s := NewLocalFileStore(t.TempDir())
	if err := s.CreateRepo("r"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuditWritePreservesExecutableMode(t *testing.T) {
	s := auditStore(t)
	p := filepath.Join(s.RepoPath("r"), "run.sh")
	if err := os.WriteFile(p, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("r", "run.sh", []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0755 {
		t.Fatalf("mode changed from 0755 to %04o", info.Mode().Perm())
	}
}

func TestAuditConcurrentEditsPreserved(t *testing.T) {
	raw := auditStore(t)
	store := &lockedFileStore{inner: raw, locks: newRepoLockSet()}
	for iteration := 0; iteration < 100; iteration++ {
		raw.Write("r", "f", []byte("a b"))
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() { <-start; _, err := editFile(store, "r", "f", "a", "A", false); done <- err }()
		go func() { <-start; _, err := editFile(store, "r", "f", "b", "B", false); done <- err }()
		close(start)
		for i := 0; i < 2; i++ {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		body, _ := raw.Read("r", "f")
		if string(body) != "A B" {
			t.Fatalf("lost edit: %q", body)
		}
	}
}

func auditGit(t *testing.T) (*gitEngine, string, string) {
	t.Helper()
	root := t.TempDir()
	g, e := newGitEngine(root)
	if e != nil {
		t.Fatal(e)
	}
	w, d := filepath.Join(root, "work"), filepath.Join(root, "git")
	if _, e = g.run(context.Background(), "", "", nil, "init", "--separate-git-dir="+d, "--initial-branch=main", w); e != nil {
		t.Fatal(e)
	}
	if e = g.configure(context.Background(), w, d); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"a", "b"} {
		if e = os.WriteFile(filepath.Join(w, p), []byte("old"), 0644); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = g.commit(context.Background(), w, d, "initial", nil, "Test", "test@example.com"); e != nil {
		t.Fatal(e)
	}
	return g, w, d
}
func TestAuditGitUnstagedStatus(t *testing.T) {
	g, w, d := auditGit(t)
	os.WriteFile(filepath.Join(w, "a"), []byte("new"), 0644)
	s, e := g.status(context.Background(), w, d)
	if e != nil {
		t.Fatal(e)
	}
	if !s.Dirty || len(s.Changes) != 1 || s.Changes[0].Path != "a" || s.Changes[0].WorkTree != "M" {
		t.Fatalf("unstaged change to a misparsed: %+v", s)
	}
}
func TestAuditSelectedCommitDoesNotIncludeOtherStagedFiles(t *testing.T) {
	g, w, d := auditGit(t)
	for _, p := range []string{"a", "b"} {
		os.WriteFile(filepath.Join(w, p), []byte("new"), 0644)
	}
	if _, e := g.run(context.Background(), w, d, nil, "add", "--", "b"); e != nil {
		t.Fatal(e)
	}
	if _, e := g.commit(context.Background(), w, d, "only a", []string{"a"}, "Test", "test@example.com"); e != nil {
		t.Fatal(e)
	}
	out, e := g.run(context.Background(), w, d, nil, "show", "--pretty=format:", "--name-only", "HEAD")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out, "b") {
		t.Fatalf("selected a, committed %q", out)
	}
}
func TestAuditCreatePatchRejectsExistingFile(t *testing.T) {
	s := auditStore(t)
	s.Write("r", "f", []byte("valuable\n"))
	r, e := applyUnifiedPatch(s, "r", "--- /dev/null\n+++ b/f\n@@ -0,0 +1 @@\n+replacement\n", false)
	b, _ := s.Read("r", "f")
	if string(b) != "valuable\n" {
		t.Fatalf("create patch overwrote existing file: result=%+v err=%v body=%q", r, e, b)
	}
}
func TestAuditPatchFailureRollsBack(t *testing.T) {
	s := auditStore(t)
	s.Write("r", "a", []byte("old\n"))
	s.Write("r", "z", []byte("blocks parent"))
	_, e := applyUnifiedPatch(s, "r", "--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n--- /dev/null\n+++ b/z/child\n@@ -0,0 +1 @@\n+new\n", false)
	if e == nil {
		t.Fatal("expected failure")
	}
	b, _ := s.Read("r", "a")
	if string(b) != "old\n" {
		t.Fatalf("failed patch left %q instead of original", b)
	}
}
func TestAuditZipFailureRollsBack(t *testing.T) {
	s := auditStore(t)
	s.Write("r", "a", []byte("old"))
	s.Write("r", "z", []byte("blocks parent"))
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, p := range []string{"a", "z/child"} {
		w, _ := zw.Create(p)
		w.Write([]byte("new"))
	}
	zw.Close()
	zr, _ := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	_, e := readZipInto(s, "r", zr)
	if e == nil {
		t.Fatal("expected failure")
	}
	body, _ := s.Read("r", "a")
	if string(body) != "old" {
		t.Fatalf("failed import overwrote a with %q", body)
	}
}
func TestAuditDeleteImportedRepoWithForeignKeys(t *testing.T) {
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	if _, e := db.Exec("PRAGMA foreign_keys=ON"); e != nil {
		t.Fatal(e)
	}
	r, e := dbCreateRepo(db, "p", CreateRepoInput{Name: "r"})
	if e != nil {
		t.Fatal(e)
	}
	if e = dbRecordImport(db, r.ID, "template:nextjs"); e != nil {
		t.Fatal(e)
	}
	if e = dbHardDeleteRepo(db, "p", "r"); e != nil {
		t.Fatalf("cannot delete imported repo with production FK settings: %v", e)
	}
}
func TestAuditStaticPreviewBlocksHiddenSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".env"), []byte("FAKE_TEST_SECRET"), 0600)
	os.Symlink(".env", filepath.Join(root, "public.txt"))
	rr := httptest.NewRecorder()
	staticPreviewHandler(root).ServeHTTP(rr, httptest.NewRequest("GET", "/public.txt", nil))
	if rr.Code == 200 {
		t.Fatalf("hidden file served through symlink: status=%d body=%q", rr.Code, rr.Body.String())
	}
}
func TestAuditSourceDirectoryToFileTransition(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a"), 0755)
	os.WriteFile(filepath.Join(root, "a", "child"), []byte("old"), 0644)
	snap, e := finishSourceSnapshot(map[string]sourceEntry{"a": {Path: "a", Mode: 0644, Data: []byte("new")}}, false)
	if e != nil {
		t.Fatal(e)
	}
	if e = applySourceSnapshot(root, []string{"a/child"}, snap); e != nil {
		t.Fatalf("valid directory-to-file source change fails: %v", e)
	}
}

func TestAuditPatchInsertionUsesUnifiedDiffPosition(t *testing.T) {
	s := auditStore(t)
	s.Write("r", "f", []byte("one\ntwo\n"))
	_, e := applyUnifiedPatch(s, "r", "--- a/f\n+++ b/f\n@@ -1,0 +2 @@\n+inserted\n", false)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := s.Read("r", "f")
	if string(b) != "one\ninserted\ntwo\n" {
		t.Fatalf("insertion at wrong position: %q", b)
	}
}
func TestAuditPatchPreservesMissingFinalNewline(t *testing.T) {
	s := auditStore(t)
	s.Write("r", "f", []byte("old"))
	_, e := applyUnifiedPatch(s, "r", "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n", false)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := s.Read("r", "f")
	if string(b) != "new" {
		t.Fatalf("added unintended final newline: %q", b)
	}
}
func TestAuditMoveDoesNotOverwriteExistingDestination(t *testing.T) {
	s := auditStore(t)
	s.Write("r", "a", []byte("source"))
	s.Write("r", "b", []byte("valuable"))
	_, e := s.Move("r", "a", "b")
	b, _ := s.Read("r", "b")
	if string(b) != "valuable" {
		t.Fatalf("rename silently overwrote destination; err=%v body=%q", e, b)
	}
}
func TestAuditPagedReadExactBufferWithoutNewline(t *testing.T) {
	s := auditStore(t)
	s.Write("r", "f", bytes.Repeat([]byte("x"), 65536))
	r, e := s.ReadPage("r", "f", 1, 200)
	if e != nil {
		t.Fatal(e)
	}
	if r.TotalLines != 1 || r.Content == "" {
		t.Fatalf("nonempty 64KiB file reported %d lines and %q content", r.TotalLines, r.Content)
	}
}
func TestAuditMonorepoDependencyFingerprint(t *testing.T) {
	snapshot := &sourceSnapshot{Entries: map[string]sourceEntry{"package.json": {Data: []byte(`{"workspaces":["apps/*"]}`)}, "apps/web/package.json": {Data: []byte(`{"dependencies":{}}`)}}}
	_, before := dependencyPlan(snapshot)
	snapshot.Entries["apps/web/package.json"] = sourceEntry{Data: []byte(`{"dependencies":{"new-package":"1.0.0"}}`)}
	_, after := dependencyPlan(snapshot)
	if before == after {
		t.Fatal("nested dependency change did not invalidate dependency cache")
	}
}

type auditSyncErrorPlatform struct{ codeWorkspacePlatform }

func (p *auditSyncErrorPlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	if tool == "workspace_source_sync" {
		return errors.New("source revision conflict")
	}
	return p.codeWorkspacePlatform.CallAppResult(app, tool, in, out)
}
func TestAuditSyncConflictDoesNotDestroyWorkspace(t *testing.T) {
	db := openTestDB(t)
	platform := &auditSyncErrorPlatform{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, platform, nil)
	r, e := dbCreateRepo(db, "p", CreateRepoInput{Name: "r"})
	if e != nil {
		t.Fatal(e)
	}
	link := &RepoWorkspace{ProjectID: "p", RepoID: r.ID, WorkspaceID: "wsp_existing", SourceDigest: "before"}
	if e = dbPutRepoWorkspace(db, link); e != nil {
		t.Fatal(e)
	}
	e = (&App{}).syncExecutionWorkspace(ctx, link, &sourceSnapshot{Digest: "next", Archive: "not-used"})
	if e == nil {
		t.Fatal("expected conflict")
	}
	if containsCall(platform.calls, "workspaces/workspace_destroy") {
		t.Fatalf("non-mutating sync conflict triggered permanent destruction: %v", e)
	}
}
