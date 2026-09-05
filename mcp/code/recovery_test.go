package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryGitSelectedAddDeleteAndFailedIndex(t *testing.T) {
	g, w, d := auditGit(t)
	ctx := context.Background()
	os.WriteFile(filepath.Join(w, "new"), []byte("added"), 0644)
	os.Remove(filepath.Join(w, "a"))
	os.WriteFile(filepath.Join(w, "b"), []byte("unrelated staged"), 0644)
	if _, err := g.run(ctx, w, d, nil, "add", "--all"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.commit(ctx, w, d, "selected", []string{"new", "a"}, "Test", "test@example.invalid"); err != nil {
		t.Fatal(err)
	}
	changed, err := g.run(ctx, w, d, nil, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if err != nil || strings.TrimSpace(changed) != "a\nnew" {
		t.Fatalf("changed=%q err=%v", changed, err)
	}
	staged, _ := g.run(ctx, w, d, nil, "diff", "--cached", "--name-only")
	if strings.TrimSpace(staged) != "b" {
		t.Fatalf("unrelated staging lost: %q", staged)
	}
	index, _ := os.ReadFile(filepath.Join(d, "index"))
	// A real Git hook failure after staging must restore the exact original index.
	hooks := t.TempDir()
	os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0755)
	if _, err := g.run(ctx, w, d, nil, "config", "core.hooksPath", hooks); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w, "new"), []byte("modified"), 0644)
	if _, err := g.commit(ctx, w, d, "fails", []string{"new"}, "Test", "test@example.invalid"); err == nil {
		t.Fatal("expected hook failure")
	}
	restored, _ := os.ReadFile(filepath.Join(d, "index"))
	if !bytes.Equal(index, restored) {
		t.Fatal("failed commit changed index")
	}
}

func TestRecoveryPatchMatchesRealGitDiff(t *testing.T) {
	for _, name := range []string{"space name.txt", "unicode-ø.txt", "normal.txt"} {
		t.Run(name, func(t *testing.T) {
			g, w, d := auditGit(t)
			ctx := context.Background()
			before := []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten")
			after := []byte("prefix\none\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nnine\nTEN")
			os.WriteFile(filepath.Join(w, name), before, 0644)
			if _, err := g.commit(ctx, w, d, "fixture", nil, "Test", "test@example.invalid"); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(w, name), after, 0644)
			patch, err := g.run(ctx, w, d, nil, "diff", "--no-ext-diff", "--no-color", "--unified=1", "--", name)
			if err != nil {
				t.Fatal(err)
			}
			s := auditStore(t)
			s.Write("r", name, before)
			result, err := applyUnifiedPatch(s, "r", patch, false)
			if err != nil || result == nil || !result.Applied {
				t.Fatalf("Git diff rejected: %+v %v\n%s", result, err, patch)
			}
			actual, _ := s.Read("r", name)
			if !bytes.Equal(actual, after) {
				t.Fatalf("mismatch %q", actual)
			}
		})
	}
}
func TestRecoveryCreatePatchRejectsDanglingSymlink(t *testing.T) {
	s := auditStore(t)
	os.Symlink("missing", filepath.Join(s.RepoPath("r"), "f"))
	_, err := applyUnifiedPatch(s, "r", "--- /dev/null\n+++ b/f\n@@ -0,0 +1 @@\n+new\n", false)
	if !errors.Is(err, errRevisionConflict) {
		t.Fatalf("expected occupied conflict: %v", err)
	}
	if target, _ := os.Readlink(filepath.Join(s.RepoPath("r"), "f")); target != "missing" {
		t.Fatal("symlink replaced")
	}
}
func TestRecoveryBudgetsAndSummaryInvalidation(t *testing.T) {
	a, ctx, r := reliabilityApp(t)
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	t.Setenv("CODE_MAX_FILE_BYTES", "32")
	t.Setenv("CODE_MAX_REPO_BYTES", "48")
	store := a.storeFor(r)
	store.Write(r.Slug, "f", bytes.Repeat([]byte("x"), 30))
	count, size, err := a.sourceSummary(r)
	if err != nil || count != 1 || size != 30 {
		t.Fatal(count, size, err)
	}
	store.Write(r.Slug, "f", []byte("small"))
	_, size, err = a.sourceSummary(r)
	if err != nil || size != 5 {
		t.Fatal("stale summary", size, err)
	}
	req := httptest.NewRequest("PUT", "/?project_id=p", strings.NewReader(strings.Repeat("z", 33)))
	res := httptest.NewRecorder()
	a.httpRepoFile(res, req, r.Slug, "f")
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized HTTP write: %d %s", res.Code, res.Body)
	}
	_, err = withRepoWrite(store, r.Slug, func(raw FileStore) (bool, error) {
		return false, applyFileMutations(raw, r.Slug, []fileMutation{{Path: "f", Body: bytes.Repeat([]byte("y"), 30)}, {Path: "g", Body: bytes.Repeat([]byte("z"), 30)}})
	})
	if err == nil {
		t.Fatal("aggregate budget bypassed")
	}
	body, _ := store.Read(r.Slug, "f")
	if string(body) != "small" {
		t.Fatal("budget failure changed source")
	}
}
func TestRecoveryIssueHistoryAndLinkUpsert(t *testing.T) {
	_, ctx, r := reliabilityApp(t)
	db := ctx.AppDB()
	issue, err := dbCreateIssue(db, "p", r, IssueCreateInput{Title: "History"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 201; i++ {
		if _, err := dbAddIssueComment(db, issue.ID, "tester", fmt.Sprintf("comment %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := dbGetIssueDetailPage(db, "p", r.ID, issue.Number, 200, 200)
	if err != nil || page.CommentsTotal != 201 || len(page.Comments) != 1 || page.Comments[0].Body != "comment 200" {
		t.Fatalf("history %+v %v", page, err)
	}
	first, err := dbAddIssueLink(db, issue.ID, "path", "first", "First", nil, "tester")
	if err != nil {
		t.Fatal(err)
	}
	dbAddIssueLink(db, issue.ID, "path", "second", "Second", nil, "tester")
	updated, err := dbAddIssueLink(db, issue.ID, "path", "first", "Updated", nil, "tester")
	if err != nil || updated.ID != first.ID || updated.Title != "Updated" {
		t.Fatalf("wrong upsert result: %+v %v", updated, err)
	}
}
func TestRecoveryStaticRestartAndLogRetention(t *testing.T) {
	a, ctx, r := reliabilityApp(t)
	s := newDevSupervisor(a.dataDir, a.store, a, 36901, 37100)
	defer s.stopAll()
	a.storeFor(r).Write(r.Slug, "index.html", []byte("preview"))
	var last string
	for i := 0; i < 5; i++ {
		dr, err := s.startDevRun(ctx, startDevInput{ProjectID: "p", Repo: r, Framework: "static"})
		if err != nil {
			t.Fatal(err)
		}
		if dr.Status != "live" || dr.PID != 0 || dr.LogPath == last {
			t.Fatalf("bad static run: %+v", dr)
		}
		last = dr.LogPath
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", dr.Port))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal(resp.Status)
		}
		if err := s.stopDevRun(ctx, "p", r.ID); err != nil {
			t.Fatal(err)
		}
	}
	paths, _ := filepath.Glob(s.logPathForRepo(r.ID) + ".*")
	if len(paths) > 3 {
		t.Fatalf("logs not bounded: %v", paths)
	}
}
func TestRecoveryDevStartupTimeout(t *testing.T) {
	t.Setenv("CODE_DEV_STARTUP_TIMEOUT_SECONDS", "1")
	a, ctx, r := reliabilityApp(t)
	s := newDevSupervisor(a.dataDir, a.store, a, 37101, 37200)
	defer s.stopAll()
	dr, err := s.startDevRun(ctx, startDevInput{ProjectID: "p", Repo: r, Framework: "blank", RunCmd: "sleep 60"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for s.get(dr.ID) != nil && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	current, _ := dbGetDevRun(ctx.AppDB(), "p", r.ID)
	if s.get(dr.ID) != nil || current.Status != "crashed" || !strings.Contains(current.Error, "startup deadline") {
		t.Fatalf("timeout not recovered: %+v", current)
	}
}

type pollingFailurePlatform struct {
	codeWorkspacePlatform
	cancelFailed bool
}

func (p *pollingFailurePlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	if tool == "workspace_command_start" {
		return json.Unmarshal([]byte(`{"command":{"id":"cmd_test","status":"running"}}`), out)
	}
	if tool == "workspace_command_get" {
		return errors.New("status transport failed")
	}
	if tool == "workspace_command_cancel" {
		p.calls = append(p.calls, tool)
		if p.cancelFailed {
			return errors.New("cancel transport failed")
		}
		return nil
	}
	return p.codeWorkspacePlatform.CallAppResult(app, tool, in, out)
}
func TestRecoveryWorkspacePollingFailureCancelsAndReportsUncertainty(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			a, ctx, _ := reliabilityApp(t)
			p := &pollingFailurePlatform{cancelFailed: failed}
			m := a.Manifest()
			ctx = sdk.NewAppCtxForTest(&m, ctx.AppDB(), nil, p, nil)
			_, _, err := a.executeWorkspaceCommand(context.Background(), ctx, "wsp_test", "sleep 60", nil, 60, 20, "test")
			if err == nil || !containsCall(p.calls, "workspace_command_cancel") {
				t.Fatal("polling failure did not cancel", err, p.calls)
			}
			if failed && !strings.Contains(err.Error(), "cmd_test in workspace wsp_test may still be running") {
				t.Fatal("uncertain outcome hidden", err)
			}
		})
	}
}

func TestRecoveryStopCancelsDependencyInstallation(t *testing.T) {
	a, ctx, r := reliabilityApp(t)
	s := newDevSupervisor(a.dataDir, a.store, a, 37201, 37300)
	defer s.stopAll()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "bun"), []byte("#!/bin/sh\ntouch install-started\nexec sleep 60\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODE_SKIP_AUTO_INSTALL", "")
	a.storeFor(r).Write(r.Slug, "package.json", []byte(`{"name":"fixture","packageManager":"bun@1.3.13"}`))
	finished := make(chan error, 1)
	go func() {
		_, err := s.startDevRun(ctx, startDevInput{ProjectID: "p", Repo: r, Framework: "blank", RunCmd: "sleep 60"})
		finished <- err
	}()
	local := a.storeFor(r).(FileStoreLocalPath)
	marker := filepath.Join(local.RepoPath(r.Slug), "install-started")
	deadline := time.Now().Add(8 * time.Second)
	for !exists(marker) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !exists(marker) {
		t.Fatal("dependency process did not start")
	}
	if err := s.stopDevRun(ctx, "p", r.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled start succeeded")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("dependency installation outlived stop")
	}
	dr, _ := dbGetDevRun(ctx.AppDB(), "p", r.ID)
	if dr.Status != "stopped" || s.get(dr.ID) != nil {
		t.Fatalf("cancelled start left runtime: %+v", dr)
	}
}

type ingressRecoveryPlatform struct {
	codeWorkspacePlatform
	exposed string
	removed string
	fail    bool
}

func (p *ingressRecoveryPlatform) ExposeIngress(in sdk.IngressExposeRequest) (*sdk.IngressRoute, error) {
	p.exposed = in.Hostname
	return &sdk.IngressRoute{}, nil
}
func (p *ingressRecoveryPlatform) UnexposeIngress(hostname string) error {
	if p.fail {
		return errors.New("ingress transport failed")
	}
	p.removed = hostname
	return nil
}
func TestRecoveryIngressOwnershipSurvivesCleanupFailureAndConfigChange(t *testing.T) {
	a, ctx, r := reliabilityApp(t)
	platform := &ingressRecoveryPlatform{}
	manifest := a.Manifest()
	config := sdk.Config{"dev_base_hostname": "preview.example.invalid"}
	ctx = sdk.NewAppCtxForTest(&manifest, ctx.AppDB(), config, platform, nil)
	s := newDevSupervisor(a.dataDir, a.store, a, 37301, 37400)
	defer s.stopAll()
	dr, err := s.startDevRun(ctx, startDevInput{ProjectID: "p", Repo: r, Framework: "static"})
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.expose(ctx, r, dr)
	if err != nil {
		t.Fatal(err)
	}
	platform.fail = true
	if err := s.stopDevRun(ctx, "p", r.ID); err == nil {
		t.Fatal("failed ingress cleanup reported success")
	}
	current, _ := dbGetDevRun(ctx.AppDB(), "p", r.ID)
	if current.IngressHostname != host || s.get(dr.ID) == nil {
		t.Fatal("recovery ownership lost")
	}
	devPortMu.Lock()
	reserved := reservedDevPorts[dr.Port]
	devPortMu.Unlock()
	if !reserved {
		t.Fatal("exposed port released")
	}
	platform.fail = false
	// Cleanup must remove the original route even after configuration changes.
	config["dev_base_hostname"] = "changed.example.invalid"
	if err := s.stopDevRun(ctx, "p", r.ID); err != nil {
		t.Fatal(err)
	}
	current, _ = dbGetDevRun(ctx.AppDB(), "p", r.ID)
	if platform.removed != host || current.IngressHostname != "" || current.Status != "stopped" {
		t.Fatalf("wrong ingress cleanup: %q %+v", platform.removed, current)
	}
}

// Exercise the engine against a disposable smart-HTTP Git server. Production
// remote URL validation separately requires HTTPS; this transport stays local.
func TestRecoveryGitRemoteLifecycle(t *testing.T) {
	g, w, d := auditGit(t)
	ctx := context.Background()
	remoteRoot := t.TempDir()
	bare := filepath.Join(remoteRoot, "repo.git")
	if _, err := g.run(ctx, "", "", nil, "init", "--bare", "--initial-branch=main", bare); err != nil {
		t.Fatal(err)
	}
	if _, err := g.run(ctx, "", bare, nil, "config", "http.receivepack", "true"); err != nil {
		t.Fatal(err)
	}
	execPath, err := g.run(ctx, "", "", nil, "--exec-path")
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(strings.TrimSpace(execPath), "git-http-backend")
	server := httptest.NewServer(&cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + remoteRoot, "GIT_HTTP_EXPORT_ALL=1"}})
	defer server.Close()
	remote := server.URL + "/repo.git"
	if _, err := g.run(ctx, w, d, nil, "remote", "add", "origin", remote); err != nil {
		t.Fatal(err)
	}
	if err := g.push(ctx, w, d, "origin", "main", true, nil); err != nil {
		t.Fatal(err)
	}
	cloned := t.TempDir()
	cw, cd := filepath.Join(cloned, "work"), filepath.Join(cloned, "metadata.git")
	if err := g.clone(ctx, remote, "main", cw, cd, nil); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w, "a"), []byte("upstream"), 0644)
	if _, err := g.commit(ctx, w, d, "upstream", []string{"a"}, "Test", "test@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if err := g.push(ctx, w, d, "origin", "main", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.fetch(ctx, cw, cd, remote, "origin", nil); err != nil {
		t.Fatal(err)
	}
	if err := g.fastForward(ctx, cw, cd, "origin", "main"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(cw, "a"))
	if string(body) != "upstream" {
		t.Fatalf("pull failed: %q", body)
	}
	if err := g.createBranch(ctx, cw, cd, "feature", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := g.switchBranch(ctx, cw, cd, "feature"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(cw, "b"), []byte("feature"), 0644)
	if _, err := g.commit(ctx, cw, cd, "feature", []string{"b"}, "Test", "test@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if err := g.push(ctx, cw, cd, "origin", "feature", true, nil); err != nil {
		t.Fatal(err)
	}
	remoteBody, err := g.run(ctx, "", bare, nil, "show", "refs/heads/feature:b")
	if err != nil || remoteBody != "feature" {
		t.Fatalf("push verification %q %v", remoteBody, err)
	}
}
