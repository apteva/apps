//go:build integration

package main

// Tier 2 coverage for Code 0.19 release plumbing through the real sidecar,
// against a local smart-HTTP Git server behind TLS (git http-backend): import,
// commit, annotated tag, tag push (and refusal to move a published tag), push
// of main to a release branch (fast-forward only), and the pull-request tool's
// refusal for a non-GitHub origin. Pull-request calls themselves are covered
// in tier 1 against a fake GitHub executor.
//
// Run with:  go test -tags integration -run TestSidecar_GitRelease ./...

import (
	"context"
	"encoding/pem"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_GitRelease_TagsReleaseBranchAndPullRequestGuard(t *testing.T) {
	root := t.TempDir()
	g, err := newGitEngine(filepath.Join(root, "engine"))
	if err != nil {
		t.Skipf("Git unavailable: %v", err)
	}
	ctx := context.Background()
	served := filepath.Join(root, "served")
	bare := filepath.Join(served, "apps.git")
	seed := filepath.Join(root, "seed")
	for _, args := range [][]string{
		{"init", "--bare", "--initial-branch=main", bare},
		{"init", "--initial-branch=main", seed},
	} {
		if _, err := g.run(ctx, "", "", nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.run(ctx, "", bare, nil, "config", "http.receivepack", "true"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "app.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", seed, "add", "."},
		{"-C", seed, "-c", "user.name=Seed", "-c", "user.email=seed@example.com", "commit", "-m", "seed"},
		{"-C", seed, "push", bare, "main"},
	} {
		// Plain git: the engine's environment deliberately forbids file transport.
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	execPath, err := g.run(ctx, "", "", nil, "--exec-path")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(&cgi.Handler{Path: filepath.Join(strings.TrimSpace(execPath), "git-http-backend"),
		Env: []string{"GIT_PROJECT_ROOT=" + served, "GIT_HTTP_EXPORT_ALL=1"}})
	defer server.Close()
	caFile := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	remoteRef := func(ref string) string {
		out, _ := g.run(ctx, "", bare, nil, "rev-parse", "-q", "--verify", ref+"^{commit}")
		return strings.TrimSpace(out)
	}

	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("git-release"), tk.WithEnv("CODE_REPOS_DIR", t.TempDir()),
		tk.WithEnv("SSL_CERT_FILE", caFile), tk.WithEnv("GIT_SSL_CAINFO", caFile))
	imported := sc.MCP("repos_git_import", map[string]any{"remote_url": server.URL + "/apps.git", "name": "Apps", "slug": "apps"})
	if imported["status"].(map[string]any)["branch"] != "main" {
		t.Fatalf("import=%v", imported)
	}
	sc.MCP("code_write_file", map[string]any{"slug": "apps", "path": "app.txt", "content": "v2\n"})
	committed := sc.MCP("repos_git_commit", map[string]any{"slug": "apps", "message": "Release app 0.2.0"})
	head := committed["head_sha"].(string)

	// Tag, list against origin, push, and the published tag cannot move.
	tag := sc.MCP("repos_git_tag", map[string]any{"slug": "apps", "action": "create", "name": "app/v0.2.0", "message": "App 0.2.0"})["tag"].(map[string]any)
	if tag["sha"] != head || tag["annotated"] != true {
		t.Fatalf("tag=%v", tag)
	}
	listed := sc.MCP("repos_git_tag", map[string]any{"slug": "apps", "action": "list", "compare_remote": true})
	if listed["count"].(float64) != 1 || listed["tags"].([]any)[0].(map[string]any)["on_remote"] != "absent" {
		t.Fatalf("list before push=%v", listed)
	}
	if err := nativeMCPError(sc, "repos_git_tag", map[string]any{"slug": "apps", "action": "push", "name": "app/v0.2.0"}); err != nil {
		t.Fatalf("tag push: %v", err)
	}
	if remoteRef("refs/tags/app/v0.2.0") != head {
		t.Fatal("tag not on origin")
	}
	listed = sc.MCP("repos_git_tag", map[string]any{"slug": "apps", "action": "list", "compare_remote": true})
	if listed["tags"].([]any)[0].(map[string]any)["on_remote"] != "same" {
		t.Fatalf("list after push=%v", listed)
	}
	sc.MCP("repos_git_tag", map[string]any{"slug": "apps", "action": "delete", "name": "app/v0.2.0"})
	sc.MCP("repos_git_tag", map[string]any{"slug": "apps", "action": "create", "name": "app/v0.2.0", "target": "HEAD~1"})
	if err := nativeMCPError(sc, "repos_git_tag", map[string]any{"slug": "apps", "action": "push", "name": "app/v0.2.0"}); err == nil || !strings.Contains(err.Error(), "never moved") {
		t.Fatalf("moving a published tag: %v", err)
	}
	var tagREST map[string]any
	if resp := sc.POST("/api/repos/apps/git/tags", map[string]any{"action": "push", "name": "app/v0.2.0"}, &tagREST); resp.Status != 409 {
		t.Fatalf("REST tag move status=%d", resp.Status)
	}
	if remoteRef("refs/tags/app/v0.2.0") != head {
		t.Fatal("published tag moved")
	}

	// main is pushed to a release branch; then main is pushed itself.
	pushed := sc.MCP("repos_git_push", map[string]any{"slug": "apps", "remote_branch": "release/app-v0.2"})
	info := pushed["pushed"].(map[string]any)
	if info["source"] != "main" || info["remote_branch"] != "release/app-v0.2" || info["sha"] != head || remoteRef("refs/heads/release/app-v0.2") != head {
		t.Fatalf("release push=%v", pushed)
	}
	if remoteRef("refs/heads/main") == head {
		t.Fatal("pushing to the release branch also moved main")
	}
	sc.MCP("repos_git_push", map[string]any{"slug": "apps"})
	if remoteRef("refs/heads/main") != head {
		t.Fatal("main not pushed")
	}
	// An older local branch cannot rewind the release branch, over MCP or REST.
	sc.MCP("repos_git_branch_create", map[string]any{"slug": "apps", "name": "old", "start_point": "HEAD~1"})
	if err := nativeMCPError(sc, "repos_git_push", map[string]any{"slug": "apps", "source": "old", "remote_branch": "release/app-v0.2"}); err == nil || !strings.Contains(err.Error(), "force push is not available") {
		t.Fatalf("rewind: %v", err)
	}
	var pushREST map[string]any
	if resp := sc.POST("/api/repos/apps/git/push", map[string]any{"source": "old", "remote_branch": "release/app-v0.2"}, &pushREST); resp.Status != 409 {
		t.Fatalf("REST rewind status=%d", resp.Status)
	}
	if remoteRef("refs/heads/release/app-v0.2") != head {
		t.Fatal("refused push changed the release branch")
	}

	// Pull requests need a github.com origin.
	if err := nativeMCPError(sc, "repos_git_pull_request", map[string]any{"slug": "apps", "action": "list"}); err == nil || !strings.Contains(err.Error(), "github.com remotes only") {
		t.Fatalf("pull request on a non-GitHub origin: %v", err)
	}
	var tagsREST map[string]any
	if resp := sc.GET("/api/repos/apps/git/tags", &tagsREST); resp.Status != 200 || tagsREST["count"].(float64) != 1 {
		t.Fatalf("REST tags status=%d %v", resp.Status, tagsREST)
	}
}
