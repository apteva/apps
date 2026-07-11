package main

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRepoStorageMigrationIsolatesDuplicateSlugsAcrossProjects(t *testing.T) {
	db := openTestDB(t)
	first, err := dbCreateRepo(db, "project-a", CreateRepoInput{Name: "Site"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := dbCreateRepo(db, "project-b", CreateRepoInput{Name: "Site"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store := NewLocalFileStore(root)
	if err := store.CreateRepo("site"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write("site", "shared.txt", []byte("legacy")); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE repositories SET storage_root = '/repos/site/'`)

	if err := migrateLegacyRepoStorage(db, store); err != nil {
		t.Fatal(err)
	}
	firstStore := bindRepoStore(store, first)
	secondStore := bindRepoStore(store, second)
	if _, err := firstStore.Write(first.Slug, "only-a.txt", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := secondStore.Read(second.Slug, "only-a.txt"); !os.IsNotExist(err) {
		t.Fatalf("project-b observed project-a file: %v", err)
	}
	if err := firstStore.DropRepo(first.Slug); err != nil {
		t.Fatal(err)
	}
	if body, err := secondStore.Read(second.Slug, "shared.txt"); err != nil || string(body) != "legacy" {
		t.Fatalf("dropping project-a damaged project-b: body=%q err=%v", body, err)
	}
}

func TestRepoStorageMigrationRenamesUniqueLegacyTree(t *testing.T) {
	db := openTestDB(t)
	repo, err := dbCreateRepo(db, "project", CreateRepoInput{Name: "Unique"})
	if err != nil {
		t.Fatal(err)
	}
	store := NewLocalFileStore(t.TempDir())
	if err := store.CreateRepo(repo.Slug); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(store.RepoPath(repo.Slug), "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE repositories SET storage_root = '/repos/unique/' WHERE id = ?`, repo.ID)
	if err := migrateLegacyRepoStorage(db, store); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.RepoPath(repo.Slug)); !os.IsNotExist(err) {
		t.Fatalf("legacy tree still exists: %v", err)
	}
	info, err := os.Stat(filepath.Join(store.RepoPath(repoStoreKey(repo)), "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("migration lost executable mode: %v", info.Mode())
	}
}

func TestLocalFileStoreRejectsSymlinkEscape(t *testing.T) {
	store := NewLocalFileStore(t.TempDir())
	if err := store.CreateRepo("repo"); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	secret := filepath.Join(external, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := store.RepoPath("repo")
	if err := os.Symlink(secret, filepath.Join(root, "secret-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("repo", "secret-link"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink escape rejection, got %v", err)
	}
	if err := os.Symlink(external, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write("repo", "outside/new.txt", []byte("bad")); err == nil {
		t.Fatal("expected write through symlinked parent to fail")
	}
}

func TestRunRepoCommandSanitizesEnvironmentAndAllowsExplicitValues(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "must-not-leak")
	a := &App{dataDir: t.TempDir()}
	repo := &Repo{ID: 41, ProjectID: "p", Slug: "repo"}
	result, err := a.runRepoCommand(repo, t.TempDir(), repoCommandInput{
		Command: `test -z "$APTEVA_APP_TOKEN" && test "$EXPLICIT" = allowed`,
		EnvJSON: `{"EXPLICIT":"allowed"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" {
		t.Fatalf("sanitized command failed: %+v", result)
	}
}

func TestRunRepoCommandTimeoutKillsProcessGroup(t *testing.T) {
	a := &App{dataDir: t.TempDir()}
	repo := &Repo{ID: 42, ProjectID: "p", Slug: "repo"}
	started := time.Now()
	result, err := a.runRepoCommand(repo, t.TempDir(), repoCommandInput{
		Command:        `trap '' TERM; (trap '' TERM; sleep 30) & wait`,
		TimeoutSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || time.Since(started) > 5*time.Second {
		t.Fatalf("timeout did not terminate process group promptly: elapsed=%s result=%+v", time.Since(started), result)
	}
}

func TestRunRepoCommandSerializesSameRepository(t *testing.T) {
	a := &App{dataDir: t.TempDir()}
	repo := &Repo{ID: 43, ProjectID: "p", Slug: "repo"}
	dir := t.TempDir()
	var wg sync.WaitGroup
	results := make(chan *repoCommandResult, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, _ := a.runRepoCommand(repo, dir, repoCommandInput{
				Command: `mkdir .exclusive && sleep 0.2 && rmdir .exclusive`,
			})
			results <- res
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result == nil || result.Status != "success" {
			t.Fatalf("same-repo commands overlapped: %+v", result)
		}
	}
}

func TestZipImportLimitsExpandedSize(t *testing.T) {
	t.Setenv("CODE_IMPORT_MAX_FILE_BYTES", "8")
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	w, _ := zw.Create("large.txt")
	_, _ = w.Write([]byte("123456789"))
	_ = zw.Close()
	zr, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	store := newMemFileStore()
	_ = store.CreateRepo("repo")
	if _, err := readZipInto(store, "repo", zr); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected expanded-size rejection, got %v", err)
	}
}

func TestStaticPreviewUsesBuildRootAndHidesDotFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "index.html"), []byte("built"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := staticPreviewHandler(root)
	for path, wantStatus := range map[string]int{"/": http.StatusOK, "/.env": http.StatusNotFound} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != wantStatus {
			t.Fatalf("%s status=%d want=%d", path, rec.Code, wantStatus)
		}
		if path == "/" && !strings.Contains(rec.Body.String(), "built") {
			t.Fatalf("preview did not use build root: %q", rec.Body.String())
		}
	}
	external := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(external, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "dist", "outside.txt")); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/outside.txt", nil))
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "outside") {
		t.Fatalf("preview followed an escaping symlink: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestSearchSkipsGeneratedTreesByDefault(t *testing.T) {
	store := newMemFileStore()
	_ = store.CreateRepo("repo")
	_, _ = store.Write("repo", "src/app.ts", []byte("needle"))
	_, _ = store.Write("repo", "node_modules/pkg/index.ts", []byte("needle"))
	result, err := grepRepo(store, "repo", GrepOptions{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Paths) != 1 || result.Paths[0] != "src/app.ts" {
		t.Fatalf("default grep included generated files: %+v", result.Paths)
	}
	result, err = grepRepo(store, "repo", GrepOptions{Pattern: "needle", IncludeGenerated: true})
	if err != nil || len(result.Paths) != 2 {
		t.Fatalf("include_generated did not restore access: paths=%v err=%v", result.Paths, err)
	}
}

type countingStore struct {
	FileStore
	reads int
}

func (s *countingStore) Read(slug, path string) ([]byte, error) {
	s.reads++
	return s.FileStore.Read(slug, path)
}

func TestReadExcerptReadsFileOnce(t *testing.T) {
	base := newMemFileStore()
	_ = base.CreateRepo("repo")
	_, _ = base.Write("repo", "file.txt", []byte("one\ntwo\nthree\n"))
	store := &countingStore{FileStore: base}
	result, err := readExcerpt(store, "repo", "file.txt", ExcerptOptions{Around: 2})
	if err != nil {
		t.Fatal(err)
	}
	if store.reads != 1 || !strings.Contains(result.Content, "two") {
		t.Fatalf("excerpt reads=%d result=%+v", store.reads, result)
	}
}

func TestLocalPagedReadMatchesExistingReadSemantics(t *testing.T) {
	content := []byte("one\ntwo\nthree\n")
	local := NewLocalFileStore(t.TempDir())
	if err := local.CreateRepo("repo"); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Write("repo", "file.txt", content); err != nil {
		t.Fatal(err)
	}
	memory := newMemFileStore()
	_ = memory.CreateRepo("repo")
	_, _ = memory.Write("repo", "file.txt", content)
	for _, tc := range []struct{ offset, limit int }{{0, 0}, {2, 1}, {20, 5}} {
		got, err := readWithLineNumbers(local, "repo", "file.txt", tc.offset, tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		want, err := readWithLineNumbers(memory, "repo", "file.txt", tc.offset, tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		if got.Content != want.Content || got.TotalLines != want.TotalLines || got.StartLine != want.StartLine || got.EndLine != want.EndLine || got.SHA256 != want.SHA256 {
			t.Fatalf("offset=%d limit=%d paged=%+v fallback=%+v", tc.offset, tc.limit, got, want)
		}
	}
}

func TestInstallNodeDepsHonorsContextDeadline(t *testing.T) {
	dir := t.TempDir()
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "npm")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntrap '' TERM\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := installNodeDeps(ctx, dir, os.Stderr, nodeDepsInstall{PM: "npm", Args: []string{"install"}}, os.Environ())
	if !isContextDeadline(err) || time.Since(started) > 4*time.Second {
		t.Fatalf("install deadline not enforced: elapsed=%s err=%v", time.Since(started), err)
	}
}
