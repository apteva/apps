package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type branchDeleteFixture struct {
	ctx                *sdk.AppCtx
	repo               *Repo
	service            *gitService
	remote, seed, work string
}

// newBranchDeleteFixture imports a real bare remote through the service, with
// only the transport redirected in this test executable (as in auto_sync_test).
func newBranchDeleteFixture(t *testing.T) *branchDeleteFixture {
	t.Helper()
	root := t.TempDir()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(syncTestPlatform{}))
	remote := filepath.Join(root, "remote.git")
	testGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(root, "seed")
	testGit(t, root, "clone", remote, seed)
	if err := os.WriteFile(filepath.Join(seed, "hello.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, seed, "add", ".")
	testGit(t, seed, "commit", "-m", "initial")
	testGit(t, seed, "push", "-u", "origin", "main")
	raw := NewLocalFileStore(filepath.Join(root, "repos"))
	service, err := newGitService(root, raw, newRepoLockSet())
	if err != nil {
		t.Fatal(err)
	}
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(root, "git-test")
	script := "#!/bin/sh\nfor arg do\n shift\n if test \"$arg\" = protocol.file.allow=never; then arg=protocol.file.allow=always; fi\n set -- \"$@\" \"$arg\"\ndone\nexec " +
		shellQuoteTest(binary) + " -c protocol.file.allow=always -c " + shellQuoteTest("url."+remote+".insteadOf=https://github.com/test/fixture.git") + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	service.engine.binary = wrapper
	imported, err := service.Import(ctx, GitImportInput{RemoteURL: "https://github.com/test/fixture.git", Slug: "demo", ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	return &branchDeleteFixture{ctx: ctx, repo: imported.Repository, service: service, remote: remote, seed: seed, work: raw.RepoPath(repoStoreKey(imported.Repository))}
}

func (f *branchDeleteFixture) del(name, where string, force bool) (*GitBranchDeleteResult, error) {
	return f.service.DeleteBranch(f.ctx, f.repo, GitBranchDeleteInput{Name: name, Where: where, Force: force, Actor: "test"})
}

func (f *branchDeleteFixture) remoteHas(t *testing.T, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir="+f.remote, "rev-parse", "-q", "--verify", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (f *branchDeleteFixture) localHas(t *testing.T, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "-q", "--verify", ref)
	cmd.Dir = f.work
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// workGit runs git in the imported working tree with origin redirected to the bare remote.
func (f *branchDeleteFixture) git(t *testing.T, args ...string) {
	t.Helper()
	testGit(t, f.work, append([]string{"-c", "url." + f.remote + ".insteadOf=https://github.com/test/fixture.git"}, args...)...)
}

func wantErr(t *testing.T, err error, fragment string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), fragment) {
		t.Fatalf("want error containing %q, got %v", fragment, err)
	}
}

func TestGitBranchDelete_LocalMergedUnmergedAndPushed(t *testing.T) {
	f := newBranchDeleteFixture(t)
	main := f.localHas(t, "HEAD")

	_, err := f.del("main", "local", true)
	wantErr(t, err, "checked-out branch")
	_, err = f.del("nope", "local", false)
	wantErr(t, err, "does not exist")
	_, err = f.del("-x", "local", false)
	wantErr(t, err, "invalid branch name")
	_, err = f.del("x", "sideways", false)
	wantErr(t, err, "where must be")

	// A branch at main's tip is merged: deleted without force.
	if _, err := f.service.CreateBranch(f.ctx, f.repo, "merged", "", "test"); err != nil {
		t.Fatal(err)
	}
	got, err := f.del("merged", "", false)
	if err != nil || got.Where != "local" || got.Local == nil || got.Local.SHA != main || got.Local.MergedInto != "main" || got.Remote != nil {
		t.Fatalf("merged delete=%+v err=%v", got, err)
	}
	if f.localHas(t, "refs/heads/merged") != "" {
		t.Fatal("merged branch still exists")
	}

	// Unmerged and unpushed work is kept unless forced.
	testGit(t, f.work, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(f.work, "feature.txt"), []byte("f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, f.work, "add", "feature.txt")
	testGit(t, f.work, "commit", "-m", "feature")
	feature := f.localHas(t, "HEAD")
	testGit(t, f.work, "switch", "main")
	_, err = f.del("feature", "local", false)
	wantErr(t, err, "the local branch of feature has commits not merged into main")
	if f.localHas(t, "refs/heads/feature") != feature {
		t.Fatal("refused delete removed the branch")
	}

	// Once pushed, deleting only the local copy is safe: origin keeps the commits.
	f.git(t, "push", "origin", "feature")
	got, err = f.del("feature", "local", false)
	if err != nil || got.Local.SHA != feature || got.Local.MergedInto != "origin/feature" {
		t.Fatalf("pushed local delete=%+v err=%v", got, err)
	}
	if f.remoteHas(t, "feature") != feature {
		t.Fatal("local delete touched origin")
	}

	// Recreate from the returned SHA, then force-delete unpushed work.
	if _, err := f.service.CreateBranch(f.ctx, f.repo, "feature", got.Local.SHA, "test"); err != nil {
		t.Fatal(err)
	}
	if f.localHas(t, "refs/heads/feature") != feature {
		t.Fatal("recreate from returned SHA failed")
	}

	// The branch is then deleted on origin by someone else. The tracking ref
	// still says origin/feature, but origin is asked directly: the local copy
	// is now the only one, so it is kept.
	testGit(t, f.seed, "push", "origin", "--delete", "feature")
	if f.localHas(t, "refs/remotes/origin/feature") != feature {
		t.Fatal("fixture: tracking ref should still be stale here")
	}
	_, err = f.del("feature", "local", false)
	wantErr(t, err, "the local branch of feature has commits not merged into main")
	if f.localHas(t, "refs/heads/feature") != feature {
		t.Fatal("stale tracking ref let unmerged work be deleted")
	}
	if f.localHas(t, "refs/remotes/origin/feature") != "" {
		t.Fatal("stale tracking ref not dropped after origin said the branch is gone")
	}
	got, err = f.del("feature", "local", true)
	if err != nil || got.Local.SHA != feature || got.Local.MergedInto != "" {
		t.Fatalf("forced delete=%+v err=%v", got, err)
	}
}

func TestGitBranchDelete_FetchDropsBranchesDeletedOnOrigin(t *testing.T) {
	f := newBranchDeleteFixture(t)
	testGit(t, f.seed, "push", "origin", "main:gone")
	if _, err := f.service.Fetch(f.ctx, f.repo, "test"); err != nil {
		t.Fatal(err)
	}
	if f.localHas(t, "refs/remotes/origin/gone") == "" {
		t.Fatal("fetch did not bring origin/gone")
	}
	testGit(t, f.seed, "push", "origin", "--delete", "gone")
	if _, err := f.service.Fetch(f.ctx, f.repo, "test"); err != nil {
		t.Fatal(err)
	}
	branches, err := f.service.Branches(f.ctx, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range branches {
		if b.Name == "origin/gone" {
			t.Fatal("fetch kept a branch deleted on origin")
		}
	}
}

func TestGitBranchDelete_RemoteAndBoth(t *testing.T) {
	f := newBranchDeleteFixture(t)
	main := f.localHas(t, "HEAD")

	// origin's default branch is never deleted, even with force.
	_, err := f.del("main", "remote", true)
	wantErr(t, err, "origin's default branch")
	if f.remoteHas(t, "main") != main {
		t.Fatal("main deleted on origin")
	}
	_, err = f.del("nope", "remote", false)
	wantErr(t, err, "origin has no branch nope")
	_, err = f.del("nope", "both", false)
	wantErr(t, err, "exists neither")

	// A merged branch on both sides goes in one call.
	testGit(t, f.work, "branch", "done")
	f.git(t, "push", "origin", "done")
	got, err := f.del("done", "both", false)
	if err != nil || got.Local == nil || got.Remote == nil || got.Local.MergedInto != "main" || got.Remote.MergedInto != "main" || got.Remote.SHA != main {
		t.Fatalf("both delete=%+v err=%v", got, err)
	}
	if f.remoteHas(t, "done") != "" || f.localHas(t, "refs/heads/done") != "" || f.localHas(t, "refs/remotes/origin/done") != "" {
		t.Fatal("done still present somewhere")
	}

	// Unmerged work on both sides: refused, nothing deleted anywhere.
	testGit(t, f.work, "switch", "-c", "wip")
	if err := os.WriteFile(filepath.Join(f.work, "wip.txt"), []byte("w\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, f.work, "add", "wip.txt")
	testGit(t, f.work, "commit", "-m", "wip")
	wip := f.localHas(t, "HEAD")
	f.git(t, "push", "origin", "wip")
	testGit(t, f.work, "switch", "main")
	_, err = f.del("wip", "both", false)
	wantErr(t, err, "the local branch and origin's branch of wip have commits not merged")
	if f.remoteHas(t, "wip") != wip || f.localHas(t, "refs/heads/wip") != wip {
		t.Fatal("refused delete removed something")
	}

	// remote only, forced: origin and the tracking ref go, the local branch stays.
	got, err = f.del("wip", "remote", true)
	if err != nil || got.Remote == nil || got.Remote.SHA != wip || got.Remote.MergedInto != "" || got.Local != nil {
		t.Fatalf("forced remote delete=%+v err=%v", got, err)
	}
	if f.remoteHas(t, "wip") != "" || f.localHas(t, "refs/remotes/origin/wip") != "" || f.localHas(t, "refs/heads/wip") != wip {
		t.Fatal("remote-only delete state wrong")
	}

	// A branch someone else pushed and this repository never fetched is unknown
	// work: refused without force, deleted with it.
	testGit(t, f.seed, "switch", "-c", "theirs")
	if err := os.WriteFile(filepath.Join(f.seed, "theirs.txt"), []byte("t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, f.seed, "add", "theirs.txt")
	testGit(t, f.seed, "commit", "-m", "theirs")
	testGit(t, f.seed, "push", "origin", "theirs")
	_, err = f.del("theirs", "remote", false)
	wantErr(t, err, "origin's branch of theirs has commits not merged")
	if f.remoteHas(t, "theirs") == "" {
		t.Fatal("refused delete removed theirs")
	}
	if _, err := f.del("theirs", "remote", true); err != nil || f.remoteHas(t, "theirs") != "" {
		t.Fatalf("forced delete of unfetched branch: %v", err)
	}

	// Operation log records every attempt.
	var n int
	if err := f.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM repo_git_operations WHERE operation = 'branch_delete'`).Scan(&n); err != nil || n < 3 {
		t.Fatalf("branch_delete operations=%d err=%v", n, err)
	}
}
