package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// releaseFixture makes a working repository with one commit on main and a
// bare "origin" it has pushed main to.
func releaseFixture(t *testing.T) (*gitEngine, string, string, string) {
	t.Helper()
	root := t.TempDir()
	g, err := newGitEngine(root)
	if err != nil {
		t.Skipf("Git unavailable: %v", err)
	}
	ctx := context.Background()
	remoteRoot := filepath.Join(root, "served")
	bare := filepath.Join(remoteRoot, "origin.git")
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
	server := httptest.NewServer(&cgi.Handler{Path: filepath.Join(strings.TrimSpace(execPath), "git-http-backend"),
		Env: []string{"GIT_PROJECT_ROOT=" + remoteRoot, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(server.Close)
	w, d := filepath.Join(root, "work"), filepath.Join(root, "work.git")
	if _, err := g.run(ctx, "", "", nil, "init", "--separate-git-dir="+d, "--initial-branch=main", w); err != nil {
		t.Fatal(err)
	}
	if err := g.configure(ctx, w, d); err != nil {
		t.Fatal(err)
	}
	if _, err := g.run(ctx, w, d, nil, "remote", "add", "origin", server.URL+"/origin.git"); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, g, w, d, "app.txt", "v1\n", "first")
	if err := g.pushBranch(ctx, w, d, "origin", "main", "main", true, nil); err != nil {
		t.Fatal(err)
	}
	return g, w, d, bare
}

func writeAndCommit(t *testing.T, g *gitEngine, w, d, path, body, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(w, path), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := g.commit(context.Background(), w, d, message, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestGitTags_CreateListPushAndNeverMove(t *testing.T) {
	g, w, d, bare := releaseFixture(t)
	ctx := context.Background()
	first := g.head(ctx, w, d)

	for _, bad := range []string{"", "-x", "a..b", "with space", "end.lock", "x~1"} {
		if _, err := g.createTag(ctx, w, d, bad, "", "", "", ""); err == nil {
			t.Fatalf("tag name %q accepted", bad)
		}
	}
	tag, err := g.createTag(ctx, w, d, "app/v1.0.0", "", "Release 1.0.0", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !tag.Annotated || tag.SHA != first || tag.Message != "Release 1.0.0" || tag.Tagger != "Apteva Code" {
		t.Fatalf("tag=%+v", tag)
	}
	if _, err := g.createTag(ctx, w, d, "app/v1.0.0", "", "", "", ""); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate local tag: %v", err)
	}
	if _, err := g.createTag(ctx, w, d, "app/v9", "no-such-ref", "", "", ""); err == nil {
		t.Fatal("tag on a missing target accepted")
	}
	second := writeAndCommit(t, g, w, d, "app.txt", "v2\n", "second")
	if _, err := g.createTag(ctx, w, d, "app/v1.1.0", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.createTag(ctx, w, d, "app/v1.0.1", first, "backport", "", ""); err != nil {
		t.Fatal(err)
	}
	tags, err := g.listTags(ctx, w, d, "", 10)
	if err != nil || len(tags) != 3 {
		t.Fatalf("tags=%+v err=%v", tags, err)
	}
	bySHA := map[string]string{}
	for _, tg := range tags {
		bySHA[tg.Name] = tg.SHA
	}
	if bySHA["app/v1.1.0"] != second || bySHA["app/v1.0.1"] != first {
		t.Fatalf("tag targets=%v", bySHA)
	}

	if err := g.pushTag(ctx, w, d, "origin", "app/v1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if err := g.pushTag(ctx, w, d, "origin", "app/v1.0.0", nil); err != nil {
		t.Fatalf("re-pushing the same tag should be a no-op: %v", err)
	}
	if err := g.pushTag(ctx, w, d, "origin", "app/v2.0.0", nil); err == nil || !strings.Contains(err.Error(), "does not exist locally") {
		t.Fatalf("push of a missing tag: %v", err)
	}
	remote, err := g.remoteTags(ctx, w, d, "origin", nil)
	if err != nil || remote["app/v1.0.0"] != first || len(remote) != 1 {
		t.Fatalf("remote tags=%v err=%v", remote, err)
	}

	// Recreating the tag locally on another commit must not move it on origin.
	if err := g.deleteTag(ctx, w, d, "app/v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.createTag(ctx, w, d, "app/v1.0.0", second, "moved", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := g.pushTag(ctx, w, d, "origin", "app/v1.0.0", nil); !errors.Is(err, errGitTagExists) {
		t.Fatalf("moving a published tag: %v", err)
	}
	if out, _ := g.run(ctx, "", bare, nil, "rev-parse", "refs/tags/app/v1.0.0^{commit}"); strings.TrimSpace(out) != first {
		t.Fatalf("origin tag moved to %s", out)
	}
	if err := g.deleteTag(ctx, w, d, "app/v7"); err == nil {
		t.Fatal("deleting a missing tag succeeded")
	}
}

func TestGitPushBranch_ToAnotherBranchFastForwardOnly(t *testing.T) {
	g, w, d, bare := releaseFixture(t)
	ctx := context.Background()
	remoteSHA := func(branch string) string {
		out, _ := g.run(ctx, "", bare, nil, "rev-parse", "-q", "--verify", "refs/heads/"+branch)
		return strings.TrimSpace(out)
	}
	// A new release branch from main.
	if err := g.pushBranch(ctx, w, d, "origin", "main", "release/app-v1", false, nil); err != nil {
		t.Fatal(err)
	}
	if remoteSHA("release/app-v1") != g.head(ctx, w, d) {
		t.Fatal("release branch not created at main")
	}
	// main moves on; the release branch fast-forwards to it.
	next := writeAndCommit(t, g, w, d, "app.txt", "v2\n", "second")
	if err := g.pushBranch(ctx, w, d, "origin", "main", "release/app-v1", false, nil); err != nil {
		t.Fatal(err)
	}
	if remoteSHA("release/app-v1") != next || remoteSHA("main") == next {
		t.Fatalf("release=%s main=%s", remoteSHA("release/app-v1"), remoteSHA("main"))
	}
	// Push a branch that is not checked out.
	if _, err := g.run(ctx, w, d, nil, "branch", "hotfix", "HEAD~1"); err != nil {
		t.Fatal(err)
	}
	if err := g.pushBranch(ctx, w, d, "origin", "hotfix", "hotfix", false, nil); err != nil {
		t.Fatal(err)
	}
	// hotfix is behind release/app-v1: pushing it there is not a fast-forward.
	if err := g.pushBranch(ctx, w, d, "origin", "hotfix", "release/app-v1", false, nil); !errors.Is(err, errGitNonFastForward) {
		t.Fatalf("rewinding a remote branch: %v", err)
	}
	// A diverged commit is refused too.
	if _, err := g.run(ctx, w, d, nil, "switch", "hotfix"); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, g, w, d, "other.txt", "x\n", "diverge")
	if err := g.pushBranch(ctx, w, d, "origin", "hotfix", "release/app-v1", false, nil); !errors.Is(err, errGitNonFastForward) {
		t.Fatalf("diverged push: %v", err)
	}
	if remoteSHA("release/app-v1") != next {
		t.Fatal("refused push changed the remote branch")
	}
	for _, bad := range []string{"", "-f", "a..b", "x y"} {
		if err := g.pushBranch(ctx, w, d, "origin", "hotfix", bad, false, nil); err == nil {
			t.Fatalf("remote branch %q accepted", bad)
		}
	}
}

// fakeGitHub records ExecuteIntegrationTool calls and answers from a table.
type fakeGitHub struct {
	calls   []string
	inputs  []map[string]any
	replies map[string]*sdk.ExecuteResult
}

func (f *fakeGitHub) ExecuteIntegrationTool(conn int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	f.calls = append(f.calls, tool)
	f.inputs = append(f.inputs, input)
	if conn != 134 {
		return nil, errors.New("wrong connection")
	}
	if r, ok := f.replies[tool]; ok {
		return r, nil
	}
	return nil, errors.New("unexpected tool " + tool)
}

func ghReply(status int, body any) *sdk.ExecuteResult {
	raw, _ := json.Marshal(body)
	return &sdk.ExecuteResult{Success: status < 400, Status: status, Data: raw}
}

var samplePull = map[string]any{
	"number": 42, "html_url": "https://github.com/apteva/apps/pull/42", "state": "open", "title": "Release places 0.2.0",
	"body": "notes", "draft": false, "mergeable": true, "mergeable_state": "clean", "user": map[string]any{"login": "marco"},
	"head": map[string]any{"ref": "feat/places", "sha": "abc123"}, "base": map[string]any{"ref": "main"},
	"created_at": "2026-11-19T10:00:00Z",
}

func TestPullRequests_ActionsThroughTheConnection(t *testing.T) {
	target := githubRepo{Owner: "apteva", Name: "apps"}
	pushedState := func(string) (string, string) { return "local1", "local1" }
	gh := &fakeGitHub{replies: map[string]*sdk.ExecuteResult{"create_pull": ghReply(201, samplePull)}}
	res, err := runPullRequest(gh, 134, target, PullRequestInput{Action: "create", Title: "Release places 0.2.0", Head: "feat/places", Base: "main", Body: "notes", Draft: true}, pushedState)
	if err != nil {
		t.Fatal(err)
	}
	pr := res.PullRequest
	if pr.Number != 42 || pr.URL == "" || pr.Head != "feat/places" || pr.HeadSHA != "abc123" || pr.Base != "main" || pr.Author != "marco" || pr.Mergeable == nil || !*pr.Mergeable || res.UnpushedCommits {
		t.Fatalf("create result=%+v pr=%+v", res, pr)
	}
	in := gh.inputs[0]
	if in["owner"] != "apteva" || in["repo"] != "apps" || in["head"] != "feat/places" || in["base"] != "main" || in["draft"] != true || in["body"] != "notes" {
		t.Fatalf("create input=%v", in)
	}

	// Validation happens before any call.
	for _, bad := range []PullRequestInput{
		{Action: "create", Head: "feat/places", Base: "main"},
		{Action: "create", Title: "x", Base: "main"},
		{Action: "create", Title: "x", Head: "main", Base: "main"},
		{Action: "get"}, {Action: "merge", Number: 1, Method: "octopus"}, {Action: "list", State: "merged"}, {Action: "reopen", Number: 1},
	} {
		before := len(gh.calls)
		if _, err := runPullRequest(gh, 134, target, bad, pushedState); err == nil || len(gh.calls) != before {
			t.Fatalf("input %+v: err=%v calls=%v", bad, err, gh.calls)
		}
	}
	// The head branch must be on origin; unpushed commits are reported.
	if _, err := runPullRequest(gh, 134, target, PullRequestInput{Action: "create", Title: "x", Head: "feat/places", Base: "main"},
		func(string) (string, string) { return "local1", "" }); err == nil || !strings.Contains(err.Error(), "push it first") {
		t.Fatalf("unpushed head: %v", err)
	}
	res, err = runPullRequest(gh, 134, target, PullRequestInput{Action: "create", Title: "x", Head: "feat/places", Base: "main"},
		func(string) (string, string) { return "local2", "local1" })
	if err != nil || !res.UnpushedCommits {
		t.Fatalf("ahead of origin: res=%+v err=%v", res, err)
	}

	gh.replies["list_pulls"] = ghReply(200, []any{samplePull})
	res, err = runPullRequest(gh, 134, target, PullRequestInput{Action: "list", Head: "feat/places", Limit: 500}, nil)
	if err != nil || res.Count != 1 || res.PullRequests[0].Number != 42 {
		t.Fatalf("list=%+v err=%v", res, err)
	}
	if in := gh.inputs[len(gh.inputs)-1]; in["head"] != "apteva:feat/places" || in["state"] != "open" || in["per_page"] != 30 || in["base"] != nil {
		t.Fatalf("list input=%v", in)
	}

	merged := map[string]any{}
	for k, v := range samplePull {
		merged[k] = v
	}
	merged["state"], merged["merged"], merged["merged_at"], merged["merge_commit_sha"] = "closed", true, "2026-11-19T11:00:00Z", "def456"
	gh.replies["merge_pull"] = ghReply(200, map[string]any{"merged": true, "sha": "def456", "message": "Pull Request successfully merged"})
	gh.replies["get_pull"] = ghReply(200, merged)
	res, err = runPullRequest(gh, 134, target, PullRequestInput{Action: "merge", Number: 42, Method: "squash", ExpectedHeadSHA: "abc123"}, nil)
	if err != nil || !res.Merged || res.MergeSHA != "def456" || !res.PullRequest.Merged || res.PullRequest.MergeCommitSHA != "def456" {
		t.Fatalf("merge=%+v err=%v", res, err)
	}
	mergeIn := gh.inputs[len(gh.inputs)-2]
	if mergeIn["pull_number"] != 42 || mergeIn["merge_method"] != "squash" || mergeIn["sha"] != "abc123" {
		t.Fatalf("merge input=%v", mergeIn)
	}

	closed := map[string]any{}
	for k, v := range samplePull {
		closed[k] = v
	}
	closed["state"] = "closed"
	gh.replies["update_pull"] = ghReply(200, closed)
	res, err = runPullRequest(gh, 134, target, PullRequestInput{Action: "close", Number: 42}, nil)
	if err != nil || res.PullRequest.State != "closed" || gh.inputs[len(gh.inputs)-1]["state"] != "closed" {
		t.Fatalf("close=%+v err=%v", res, err)
	}

	// GitHub's validation message comes back readable.
	gh.replies["create_pull"] = ghReply(422, map[string]any{"message": "Validation Failed",
		"errors": []any{map[string]any{"message": "A pull request already exists for apteva:feat/places."}}})
	_, err = runPullRequest(gh, 134, target, PullRequestInput{Action: "create", Title: "x", Head: "feat/places", Base: "main"}, pushedState)
	if err == nil || !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "already exists for apteva:feat/places") {
		t.Fatalf("422: %v", err)
	}
	gh.replies["get_pull"] = ghReply(401, map[string]any{"message": "Bad credentials"})
	if _, err := runPullRequest(gh, 134, target, PullRequestInput{Action: "get", Number: 42}, nil); err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("401: %v", err)
	}
}

func TestParseGitHubRemote(t *testing.T) {
	for raw, want := range map[string]string{
		"https://github.com/apteva/apps.git": "apteva/apps",
		"https://github.com/apteva/apps":     "apteva/apps",
		"https://GitHub.com/apteva/apps/":    "apteva/apps",
	} {
		got, err := parseGitHubRemote(raw)
		if err != nil || got.String() != want {
			t.Fatalf("%s -> %v %v", raw, got, err)
		}
	}
	for _, raw := range []string{"https://gitlab.com/a/b.git", "http://github.com/a/b", "https://github.com/a", "https://github.com/a/b/c", "git@github.com:a/b.git"} {
		if _, err := parseGitHubRemote(raw); err == nil {
			t.Fatalf("%s accepted", raw)
		}
	}
}

func TestPullRequestConnectionChoice(t *testing.T) {
	if id, err := pullRequestConnection(nil, &GitRemote{ConnectionID: 7, ProviderSlug: "github"}); err != nil || id != 7 {
		t.Fatalf("origin connection: %d %v", id, err)
	}
	if _, err := pullRequestConnection(nil, &GitRemote{ConnectionID: 7, ProviderSlug: "gitlab"}); err == nil {
		t.Fatal("gitlab connection accepted for pull requests")
	}
	if _, err := pullRequestConnection(nil, &GitRemote{}); err == nil || !strings.Contains(err.Error(), "no GitHub connection") {
		t.Fatalf("no connection: %v", err)
	}
}
