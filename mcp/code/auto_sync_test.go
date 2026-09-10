package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type syncTestPlatform struct{ tk.BasePlatformClient }

func (syncTestPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{}}, nil
}

type syncFixture struct {
	ctx                         *sdk.AppCtx
	repo                        *Repo
	service                     *gitService
	supervisor                  *autoSyncSupervisor
	remote, seed, work, offline string
	store                       FileStore
}

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "protocol.file.allow=always", "-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...)
	cmd.Dir = dir
	cmd.Env = safeGitEnvironment()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func shellQuoteTest(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	root := t.TempDir()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(syncTestPlatform{}))
	remote := filepath.Join(root, "remote.git")
	testGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(root, "seed")
	testGit(t, root, "clone", remote, seed)
	if err := os.WriteFile(filepath.Join(seed, "hello.txt"), []byte("initial\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testGit(t, seed, "add", ".")
	testGit(t, seed, "commit", "-m", "initial")
	testGit(t, seed, "push", "-u", "origin", "main")
	raw := NewLocalFileStore(filepath.Join(root, "repos"))
	locks := newRepoLockSet()
	service, err := newGitService(root, raw, locks)
	if err != nil {
		t.Fatal(err)
	}
	// Real Git and real bare remotes, with only the transport redirected in this
	// test executable. Production still rejects all local/file remote URLs.
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(root, "git-test")
	offline := filepath.Join(root, "offline")
	script := "#!/bin/sh\nfor arg do\n shift\n case \"$arg\" in fetch|push) if test -e " + shellQuoteTest(offline) + "; then echo 'Could not resolve host: github.com' >&2; exit 128; fi;; esac\n if test \"$arg\" = protocol.file.allow=never; then arg=protocol.file.allow=always; fi\n set -- \"$@\" \"$arg\"\ndone\nexec " + shellQuoteTest(binary) + " -c protocol.file.allow=always -c " + shellQuoteTest("url."+remote+".insteadOf=https://github.com/test/fixture.git") + " \"$@\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	service.engine.binary = wrapper
	imported, err := service.Import(ctx, GitImportInput{RemoteURL: "https://github.com/test/fixture.git", Slug: "demo", ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	sup := newAutoSyncSupervisor(service)
	t.Cleanup(sup.stop)
	store := &lockedFileStore{inner: raw, locks: locks}
	f := &syncFixture{ctx: ctx, repo: imported.Repository, service: service, supervisor: sup, remote: remote, seed: seed, work: raw.RepoPath(repoStoreKey(imported.Repository)), offline: offline, store: store}
	state, err := sup.configure(ctx, f.repo, true)
	if err != nil || !state.Enabled {
		t.Fatalf("enable: %+v %v", state, err)
	}
	return f
}
func (f *syncFixture) tick(t *testing.T, now time.Time, force bool) *AutoSyncState {
	t.Helper()
	state, err := f.supervisor.tick(f.ctx, f.repo, now, force)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func (f *syncFixture) write(t *testing.T, path, body string) {
	t.Helper()
	if _, err := f.store.Write(repoStoreKey(f.repo), path, []byte(body)); err != nil {
		t.Fatal(err)
	}
}
func TestAutoSyncDebounceIgnoredFilesAndRestart(t *testing.T) {
	f := newSyncFixture(t)
	now := time.Now()
	initial := testGit(t, f.seed, "rev-parse", "HEAD")
	f.write(t, "hello.txt", "one\n")
	f.write(t, ".gitignore", "ignored.txt\n")
	f.write(t, "ignored.txt", "do not commit\n")
	if s := f.tick(t, now, false); s.Status != "pending" {
		t.Fatalf("state=%+v", s)
	}
	if got := testGit(t, f.work, "rev-parse", "HEAD"); got != initial {
		t.Fatal("committed before debounce")
	}
	f.write(t, "hello.txt", "two\n")
	f.tick(t, now.Add(5*time.Second), false)
	// Restart does not lose the debounce window or enabled setting.
	f.supervisor.stop()
	f.supervisor = newAutoSyncSupervisor(f.service)
	t.Cleanup(f.supervisor.stop)
	f.tick(t, now.Add(12*time.Second), false)
	if got := testGit(t, f.work, "rev-parse", "HEAD"); got != initial {
		t.Fatal("restart ignored last edit")
	}
	s := f.tick(t, now.Add(16*time.Second), false)
	if s.Status != "synced" {
		t.Fatalf("state=%+v", s)
	}
	if got := testGit(t, f.remote, "show", "main:hello.txt"); got != "two" {
		t.Fatalf("remote=%q", got)
	}
	if got := testGit(t, f.remote, "rev-list", "--count", "main"); got != "2" {
		t.Fatalf("expected single batched commit: %s", got)
	}
	if got := testGit(t, f.remote, "ls-tree", "--name-only", "main"); strings.Contains(got, "ignored.txt") {
		t.Fatal("ignored file committed")
	}
}
func TestAutoSyncContinuousChangesCheckpoint(t *testing.T) {
	f := newSyncFixture(t)
	now := time.Now()
	for i := 0; i <= 60; i += 5 {
		f.write(t, "hello.txt", time.Unix(int64(i), 0).String())
		f.tick(t, now.Add(time.Duration(i)*time.Second), false)
	}
	if count := testGit(t, f.remote, "rev-list", "--count", "main"); count != "2" {
		t.Fatalf("continuous changes never checkpointed: %s", count)
	}
}
func TestAutoSyncOfflineRecoveryAndLocalCheckpoints(t *testing.T) {
	f := newSyncFixture(t)
	now := time.Now()
	os.WriteFile(f.offline, []byte("offline"), 0600)
	f.write(t, "hello.txt", "offline one")
	s := f.tick(t, now, true)
	if s.Status != "offline" || s.RetryAt == 0 {
		t.Fatalf("state=%+v", s)
	}
	if got := testGit(t, f.work, "show", "HEAD:hello.txt"); got != "offline one" {
		t.Fatal("offline change not checkpointed")
	}
	f.write(t, "hello.txt", "offline two")
	f.tick(t, now.Add(time.Second), false)
	s = f.tick(t, now.Add(12*time.Second), false)
	if s.Status != "offline" {
		t.Fatalf("state=%+v", s)
	}
	if got := testGit(t, f.work, "show", "HEAD:hello.txt"); got != "offline two" {
		t.Fatal("retry backoff blocked local checkpoint")
	}
	f.supervisor.stop()
	f.supervisor = newAutoSyncSupervisor(f.service)
	t.Cleanup(f.supervisor.stop)
	os.Remove(f.offline)
	s = f.tick(t, now.Add(16*time.Second), false)
	if s.Status != "synced" || s.RetryAt != 0 {
		t.Fatalf("recovery=%+v", s)
	}
	if got := testGit(t, f.remote, "show", "main:hello.txt"); got != "offline two" {
		t.Fatalf("remote=%s", got)
	}
}
func TestAutoSyncFastForwardAndDivergence(t *testing.T) {
	f := newSyncFixture(t)
	now := time.Now()
	os.WriteFile(filepath.Join(f.seed, "hello.txt"), []byte("remote update"), 0644)
	testGit(t, f.seed, "commit", "-am", "remote update")
	testGit(t, f.seed, "push")
	s := f.tick(t, now, true)
	if s.Status != "synced" {
		t.Fatalf("ff=%+v", s)
	}
	body, _ := os.ReadFile(filepath.Join(f.work, "hello.txt"))
	if string(body) != "remote update" {
		t.Fatal("remote-only update not applied")
	}
	os.WriteFile(filepath.Join(f.seed, "hello.txt"), []byte("conflicting remote"), 0644)
	testGit(t, f.seed, "commit", "-am", "remote conflict")
	testGit(t, f.seed, "push")
	f.write(t, "hello.txt", "local work")
	s = f.tick(t, now.Add(time.Minute), true)
	if s.Status != "needs_attention" || !strings.Contains(s.LastError, "diverged") {
		t.Fatalf("divergence=%+v", s)
	}
	if got := testGit(t, f.remote, "show", "main:hello.txt"); got != "conflicting remote" {
		t.Fatal("remote overwritten")
	}
	if got := testGit(t, f.work, "show", "HEAD:hello.txt"); got != "local work" {
		t.Fatal("local work lost")
	}
}
func TestAutoSyncPauseArchiveAndBranchChange(t *testing.T) {
	f := newSyncFixture(t)
	now := time.Now()
	_, err := f.supervisor.configure(f.ctx, f.repo, false)
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, "hello.txt", "paused")
	s := f.tick(t, now, false)
	if s.Enabled || s.Status != "paused" {
		t.Fatalf("pause=%+v", s)
	}
	_, err = f.supervisor.configure(f.ctx, f.repo, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.engine.createBranch(context.Background(), f.work, f.service.gitDir(f.repo.ID), "other", ""); err != nil {
		t.Fatal(err)
	}
	testGit(t, f.work, "switch", "other")
	s = f.tick(t, now, true)
	if s.Status != "needs_attention" {
		t.Fatalf("branch change=%+v", s)
	}
	if err = dbArchiveRepo(f.ctx.AppDB(), f.repo.ProjectID, f.repo.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err = f.supervisor.tick(f.ctx, f.repo, now, true); err == nil {
		t.Fatal("archived repository synchronized")
	}
}
func TestAutoSyncWaitsForCompleteEditingOperation(t *testing.T) {
	f := newSyncFixture(t)
	key := repoStoreKey(f.repo)
	unlock := f.service.locks.lock(key)
	raw := f.service.store
	raw.Write(key, "a.txt", []byte("a"))
	done := make(chan *AutoSyncState, 1)
	go func() { s, _ := f.supervisor.tick(f.ctx, f.repo, time.Now(), true); done <- s }()
	select {
	case <-done:
		unlock()
		t.Fatal("sync captured an unfinished edit")
	case <-time.After(50 * time.Millisecond):
	}
	raw.Write(key, "b.txt", []byte("b"))
	unlock()
	select {
	case state := <-done:
		if state == nil || state.Status != "synced" {
			t.Fatalf("state=%+v", state)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sync deadlocked")
	}
	if got := testGit(t, f.remote, "ls-tree", "--name-only", "main"); !strings.Contains(got, "a.txt") || !strings.Contains(got, "b.txt") {
		t.Fatalf("partial checkpoint: %s", got)
	}
}

func TestAutoSyncSymlinkCommitsLinkWithoutReadingTarget(t *testing.T) {
	f := newSyncFixture(t)
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.work, "link")); err != nil {
		t.Fatal(err)
	}
	if s := f.tick(t, time.Now(), true); s.Status != "synced" {
		t.Fatalf("state=%+v", s)
	}
	if got := testGit(t, f.remote, "show", "main:link"); got != outside {
		t.Fatalf("committed link contents instead of target path: %q", got)
	}
}

type syncBindingPlatform struct {
	tk.BasePlatformClient
	bindings map[string]any
}

func (p *syncBindingPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: p.bindings}, nil
}
func (p *syncBindingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{AppSlug: "github"}, nil
}
func (p *syncBindingPlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	return &sdk.ConnectionCredentials{ConnectionID: id, Slug: "github", Fields: map[string]string{"token": "test-token"}}, nil
}
func TestGitHubPickerAndCloneUseSameConnection(t *testing.T) {
	pf := &syncBindingPlatform{bindings: map[string]any{"github": float64(1), "git": float64(2)}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(pf))
	picker := boundGitIntegrationForSlug(ctx, "github")
	auth, err := gitAuthForRemote(ctx, "https://github.com/acme/private.git", 0)
	if err != nil {
		t.Fatal(err)
	}
	if picker == nil || auth.ConnectionID != picker.ConnectionID {
		t.Fatalf("picker=%+v clone=%d", picker, auth.ConnectionID)
	}
	auth, err = gitAuthForRemote(ctx, "https://github.com/acme/private.git", 2)
	if err != nil || auth.ConnectionID != 2 {
		t.Fatalf("explicit connection: %+v %v", auth, err)
	}
}
