package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func reliabilityApp(t *testing.T) (*App, *sdk.AppCtx, *Repo) {
	t.Helper()
	db := openTestDB(t)
	m := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&m, db, sdk.Config{"trusted_local_execution": "true"}, nil, nil)
	raw := NewLocalFileStore(t.TempDir())
	locks := newRepoLockSet()
	a := &App{store: &lockedFileStore{inner: raw, locks: locks}, locks: locks, dataDir: t.TempDir()}
	repo, err := dbCreateRepo(db, "p", CreateRepoInput{Name: "R"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.storeFor(repo).CreateRepo(repo.Slug); err != nil {
		t.Fatal(err)
	}
	return a, ctx, repo
}
func TestReliabilityConditionalHTTPWrite(t *testing.T) {
	a, ctx, r := reliabilityApp(t)
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	store := a.storeFor(r)
	store.Write(r.Slug, "f", []byte("one"))
	get := httptest.NewRecorder()
	a.httpRepoFile(get, httptest.NewRequest("GET", "/?project_id=p", nil), r.Slug, "f")
	etag := get.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	store.Write(r.Slug, "f", []byte("two"))
	req := httptest.NewRequest("PUT", "/?project_id=p", strings.NewReader("stale"))
	req.Header.Set("If-Match", etag)
	out := httptest.NewRecorder()
	a.httpRepoFile(out, req, r.Slug, "f")
	if out.Code != 409 {
		t.Fatalf("status %d: %s", out.Code, out.Body)
	}
	body, _ := store.Read(r.Slug, "f")
	if string(body) != "two" {
		t.Fatal("stale save changed source")
	}
}
func TestReliabilityMutationAndGitShareLock(t *testing.T) {
	a, _, r := reliabilityApp(t)
	store := a.storeFor(r)
	store.Write(r.Slug, "f", []byte("a b"))
	release := a.locks.lock(repoStoreKey(r))
	done := make(chan error, 1)
	go func() { _, err := editFile(store, r.Slug, "f", "a", "A", false); done <- err }()
	select {
	case <-done:
		t.Fatal("edit bypassed Git lock")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestReliabilityPatchPreviewRejectsUnrelatedFileDrift(t *testing.T) {
	s := auditStore(t)
	s.Write("r", "f", []byte("one\ntwo\n"))
	patch := "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-one\n+ONE\n"
	preview, err := applyUnifiedPatch(s, "r", patch, true)
	if err != nil {
		t.Fatal(err)
	}
	s.Write("r", "f", []byte("one\nchanged elsewhere\n"))
	if _, err := applyPatchPreview(s, "r", preview.PatchID); !errors.Is(err, errRevisionConflict) {
		t.Fatalf("wanted stale preview rejection, got %v", err)
	}
}
func TestReliabilityPatchStrictFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, path, old, patch, want string
		reject                       bool
	}{
		{"spaces", "a b", "one\n", "--- a/a b\n+++ b/a b\n@@ -1 +1 @@\n-one\n+two\n", "two\n", false},
		{"quoted", "a b", "one\n", "--- \"a/a b\"\n+++ \"b/a b\"\n@@ -1 +1 @@\n-one\n+two\n", "two\n", false},
		{"header-like-removal", "f", "-- old\n", "--- a/f\n+++ b/f\n@@ -1 +1 @@\n--- old\n+new\n", "new\n", false},
		{"bad-count", "f", "one\n", "--- a/f\n+++ b/f\n@@ -1,2 +1,1 @@\n-one\n+two\n", "", true},
		{"duplicate", "f", "one\n", "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-one\n+two\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-one\n+three\n", "", true},
		{"rename", "f", "one\n", "--- a/f\n+++ b/g\n@@ -1 +1 @@\n-one\n+two\n", "", true},
		{"strict-position", "f", "other\none\n", "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-one\n+two\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := auditStore(t)
			s.Write("r", tc.path, []byte(tc.old))
			result, err := applyUnifiedPatch(s, "r", tc.patch, false)
			if tc.reject {
				if err == nil && result.Applied {
					t.Fatal("malformed/stale patch accepted")
				}
				body, _ := s.Read("r", tc.path)
				if string(body) != tc.old {
					t.Fatal("rejected patch changed source")
				}
				return
			}
			if err != nil || !result.Applied {
				t.Fatalf("%v %+v", err, result)
			}
			body, _ := s.Read("r", tc.path)
			if string(body) != tc.want {
				t.Fatalf("got %q", body)
			}
		})
	}
}
func TestReliabilitySourceFileToDirectoryAndUnmanagedChildren(t *testing.T) {
	root := t.TempDir()
	writeTestSource(t, root, "a", []byte("old"), 0755)
	next, err := finishSourceSnapshot(map[string]sourceEntry{"a/child": {Path: "a/child", Mode: 0644, Data: []byte("new")}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := applySourceSnapshot(root, []string{"a"}, next); err != nil {
		t.Fatal(err)
	}
	writeTestSource(t, root, "a/unmanaged", []byte("keep"), 0644)
	replacement, _ := finishSourceSnapshot(map[string]sourceEntry{"a": {Path: "a", Mode: 0644, Data: []byte("replace")}}, false)
	if err := applySourceSnapshot(root, []string{"a/child"}, replacement); err == nil {
		t.Fatal("removed unmanaged directory")
	}
	for _, p := range []string{"a/child", "a/unmanaged"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestReliabilityZIPPreservesModeAndRejectsDuplicates(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		s := auditStore(t)
		var archive bytes.Buffer
		z := zip.NewWriter(&archive)
		h := &zip.FileHeader{Name: "run.sh"}
		h.SetMode(0755)
		w, _ := z.CreateHeader(h)
		w.Write([]byte("#!/bin/sh\n"))
		if duplicate {
			w, _ = z.Create("./run.sh")
			w.Write([]byte("bad"))
		}
		z.Close()
		zr, _ := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
		_, err := readZipInto(s, "r", zr)
		if duplicate {
			if err == nil {
				t.Fatal("duplicate accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(filepath.Join(s.RepoPath("r"), "run.sh"))
		if info.Mode().Perm() != 0755 {
			t.Fatal("import lost executable")
		}
		var exported bytes.Buffer
		if err := zipRepo(&exported, s, "r"); err != nil {
			t.Fatal(err)
		}
		zz, _ := zip.NewReader(bytes.NewReader(exported.Bytes()), int64(exported.Len()))
		if zz.File[0].Mode().Perm() != 0755 {
			t.Fatal("export lost executable")
		}
	}
}
func TestReliabilityMetadataAtomicValidation(t *testing.T) {
	a, ctx, r := reliabilityApp(t)
	_ = a
	name := "changed"
	badPort := 99999
	if _, err := dbPatchRepoMetadata(ctx.AppDB(), "p", r.Slug, repoMetadataPatch{Name: &name, Port: &badPort}); err == nil {
		t.Fatal("invalid port accepted")
	}
	saved, _ := dbGetRepoBySlug(ctx.AppDB(), "p", r.Slug)
	if saved.Name != r.Name {
		t.Fatal("failed metadata patch partially committed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); dbPatchRepo(ctx.AppDB(), "p", r.Slug, &name, nil) }()
		go func() { defer wg.Done(); desc := "description"; dbPatchRepo(ctx.AppDB(), "p", r.Slug, nil, &desc) }()
	}
	wg.Wait()
	saved, _ = dbGetRepoBySlug(ctx.AppDB(), "p", r.Slug)
	if saved.Name != name || saved.Description != "description" {
		t.Fatal("lost metadata update")
	}
}
func TestReliabilityIssuePagination(t *testing.T) {
	_, ctx, r := reliabilityApp(t)
	for i := 0; i < 205; i++ {
		if _, err := dbCreateIssue(ctx.AppDB(), "p", r, IssueCreateInput{Title: fmt.Sprintf("Issue %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	for _, offset := range []int{0, 100, 200, 300} {
		total := 0
		page, err := dbListIssues(ctx.AppDB(), "p", r.ID, IssueListOptions{State: "all", Limit: 100, Offset: offset, Total: &total})
		if err != nil {
			t.Fatal(err)
		}
		if total != 205 {
			t.Fatalf("total %d", total)
		}
		for _, issue := range page {
			if seen[issue.ID] {
				t.Fatal("overlapping pages")
			}
			seen[issue.ID] = true
		}
	}
	if len(seen) != 205 {
		t.Fatal("missing issues")
	}
}
func TestReliabilityDeleteRestoresFilesWhenDBFails(t *testing.T) {
	a, ctx, r := reliabilityApp(t)
	a.storeFor(r).Write(r.Slug, "f", []byte("keep"))
	_, err := ctx.AppDB().Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON repositories BEGIN SELECT RAISE(ABORT,'injected failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.hardDeleteRepo(ctx.AppDB(), "p", r.Slug); err == nil {
		t.Fatal("expected deletion failure")
	}
	body, err := a.storeFor(r).Read(r.Slug, "f")
	if err != nil || string(body) != "keep" {
		t.Fatalf("source lost %q %v", body, err)
	}
	ctx.AppDB().Exec("DROP TRIGGER reject_delete")
	if err := a.hardDeleteRepo(ctx.AppDB(), "p", r.Slug); err != nil {
		t.Fatal(err)
	}
}
func TestReliabilityCoordinatorDoesNotStarveOtherRepositories(t *testing.T) {
	t.Setenv("CODE_MAX_COMMANDS", "2")
	c := commandCoordinator{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, _ := c.acquire(ctx, 1)
	defer release()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, err := c.acquire(ctx, 1)
		if err == nil {
			r()
		}
	}()
	time.Sleep(20 * time.Millisecond)
	free, err := c.acquire(ctx, 2)
	if err != nil {
		t.Fatal("same-repo queue consumed global capacity")
	}
	free()
	cancel()
	<-done
}
func TestReliabilityLocalCommandCancellation(t *testing.T) {
	a, _, r := reliabilityApp(t)
	path, _ := repoLocalPath(a.storeFor(r), r.Slug)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := a.runRepoCommandContext(ctx, r, path, repoCommandInput{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || time.Since(start) > 4*time.Second {
		t.Fatalf("cancellation ignored: %+v", result)
	}
}
func TestReliabilityLogRetainsFinalFailure(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := newCappedWriter(f, 4096)
	w.Write(bytes.Repeat([]byte("x"), 10000))
	w.Write([]byte("FINAL FAILURE\n"))
	body, _ := os.ReadFile(f.Name())
	if len(body) > 4096 || !bytes.Contains(body, []byte("FINAL FAILURE")) {
		t.Fatalf("bad log tail len=%d", len(body))
	}
}
func TestReliabilityLogSSEResumesWithoutGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	os.WriteFile(path, []byte("first\nsecond\n"), 0600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { streamLogSSE(w, r, path) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	req.Header.Set("Last-Event-ID", "run.log:6")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := make([]byte, 1024)
	n, err := res.Body.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), "second") || strings.Contains(string(buf[:n]), "first") {
		t.Fatalf("bad resume %q", buf[:n])
	}
	cancel()
}
func TestReliabilityPageCacheInvalidatesSameSizeRewrite(t *testing.T) {
	s := auditStore(t)
	s.Write("r", "f", []byte("one"))
	path := filepath.Join(s.RepoPath("r"), "f")
	info, _ := os.Stat(path)
	first, _ := s.ReadPage("r", "f", 1, 20)
	os.WriteFile(path, []byte("two"), 0644)
	os.Chtimes(path, info.ModTime(), info.ModTime())
	second, _ := s.ReadPage("r", "f", 1, 20)
	if first.SHA256 == second.SHA256 || !strings.Contains(second.Content, "two") {
		t.Fatal("stale cached receipt")
	}
}
func TestReliabilityLocalDependencyFingerprintIncludesMembers(t *testing.T) {
	root := t.TempDir()
	writeTestSource(t, root, "package.json", []byte(`{"workspaces":["packages/*"]}`), 0644)
	writeTestSource(t, root, "packages/a/package.json", []byte(`{}`), 0644)
	before, _ := dependencyFingerprint(root)
	writeTestSource(t, root, "packages/a/package.json", []byte(`{"dependencies":{"x":"1"}}`), 0644)
	after, _ := dependencyFingerprint(root)
	if before == after {
		t.Fatal("nested manifest not fingerprinted")
	}
}
func TestReliabilityInvalidEnvironmentHasNoWorkspaceSideEffects(t *testing.T) {
	a, ctx, r := reliabilityApp(t)
	p := &codeWorkspacePlatform{}
	m := a.Manifest()
	ctx = sdk.NewAppCtxForTest(&m, ctx.AppDB(), nil, p, nil)
	_, err := a.toolRunCommand(context.Background(), ctx, map[string]any{"_project_id": "p", "slug": r.Slug, "command": "true", "env_json": "[]"})
	if err == nil || len(p.calls) != 0 {
		t.Fatalf("invalid env caused platform calls: %v %v", err, p.calls)
	}
}
func TestReliabilityPortReservationsUnique(t *testing.T) {
	s := newDevSupervisor(t.TempDir(), nil, nil, 36000, 36500)
	ports := map[int]bool{}
	for i := 0; i < 20; i++ {
		p, err := s.allocateDevPort()
		if err != nil {
			t.Fatal(err)
		}
		if ports[p] {
			t.Fatal("port reused while reserved")
		}
		ports[p] = true
		defer releaseDevPort(p)
	}
}
func TestReliabilitySlowDevReadinessAndStop(t *testing.T) {
	if testing.Short() {
		t.Skip("slow startup lifecycle exercised in full suite")
	}
	a, ctx, r := reliabilityApp(t)
	s := newDevSupervisor(a.dataDir, a.store, a, 36501, 36900)
	a.dev = s
	defer s.stopAll()
	dr, err := s.startDevRun(ctx, startDevInput{ProjectID: "p", Repo: r, Framework: "blank", RunCmd: `sleep 6; python3 -m http.server "$PORT" --bind 127.0.0.1`})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		dr, _ = dbGetDevRun(ctx.AppDB(), "p", r.ID)
		if dr.Status == "live" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if dr.Status != "live" {
		t.Fatalf("slow server remained %s: %s", dr.Status, dr.Error)
	}
	if err := s.stopDevRun(ctx, "p", r.ID); err != nil {
		t.Fatal(err)
	}
}
