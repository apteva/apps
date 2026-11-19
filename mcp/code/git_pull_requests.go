package main

// GitHub pull requests for Git-backed repositories (Code 0.19). Calls go
// through the GitHub integration's catalog tools on the repository's bound
// connection (ExecuteIntegrationTool), so the token never leaves the
// platform and the same connection that pushes also opens the request.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type pullRequestExecutor interface {
	ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error)
}

// GitPullRequest is the trimmed view of a GitHub pull request.
type GitPullRequest struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	State          string `json:"state"`
	Title          string `json:"title"`
	Body           string `json:"body,omitempty"`
	Head           string `json:"head"`
	HeadSHA        string `json:"head_sha,omitempty"`
	Base           string `json:"base"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	Mergeable      *bool  `json:"mergeable,omitempty"`
	MergeableState string `json:"mergeable_state,omitempty"`
	Author         string `json:"author,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	MergedAt       string `json:"merged_at,omitempty"`
	MergeCommitSHA string `json:"merge_commit_sha,omitempty"`
	// ApprovedBy lists the people who approved this pull request at its
	// current head in Code; approvals of earlier heads are not counted.
	ApprovedBy []string `json:"approved_by,omitempty"`
}

type PullRequestInput struct {
	Action          string
	Number          int
	Title           string
	Body            string
	Head            string
	Base            string
	Draft           bool
	State           string
	Limit           int
	Method          string
	ExpectedHeadSHA string
	CommitTitle     string
	CommitMessage   string
	// Actor is the verified caller (set by the MCP wrapper or the REST
	// handler, never by the client); it decides who may approve and merge.
	Actor string
}

type PullRequestResult struct {
	Action       string           `json:"action"`
	Repository   string           `json:"repository"`
	PullRequest  *GitPullRequest  `json:"pull_request,omitempty"`
	PullRequests []GitPullRequest `json:"pull_requests,omitempty"`
	Count        int              `json:"count,omitempty"`
	Merged       bool             `json:"merged,omitempty"`
	MergeSHA     string           `json:"merge_sha,omitempty"`
	// UnpushedCommits is true on create when the local head branch has
	// commits origin does not have yet; the request shows origin's state.
	UnpushedCommits bool `json:"unpushed_commits,omitempty"`
	// Existing is true on create when an open pull request from the same head
	// into the same base was already there and is returned unchanged, so a
	// retried create never fails or opens a second one.
	Existing bool `json:"existing,omitempty"`
}

type githubRepo struct{ Owner, Name string }

func (r githubRepo) String() string { return r.Owner + "/" + r.Name }

func parseGitHubRemote(raw string) (githubRepo, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "github.com") {
		return githubRepo{}, errors.New("pull requests are available for github.com remotes only")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return githubRepo{}, errors.New("origin is not a github.com/<owner>/<repository> URL")
	}
	return githubRepo{Owner: parts[0], Name: strings.TrimSuffix(parts[1], ".git")}, nil
}

// pullRequestConnection picks the GitHub connection: the one the origin
// remote was set up with, else the install's bound GitHub connection.
func pullRequestConnection(ctx *sdk.AppCtx, remote *GitRemote) (int64, error) {
	if remote.ConnectionID > 0 {
		if remote.ProviderSlug != "" && !strings.EqualFold(remote.ProviderSlug, "github") {
			return 0, fmt.Errorf("origin uses a %s connection; pull requests need a GitHub connection", remote.ProviderSlug)
		}
		return remote.ConnectionID, nil
	}
	for _, bound := range boundGitIntegrations(ctx) {
		if bound.AppSlug == "" || strings.EqualFold(bound.AppSlug, "github") {
			return bound.ConnectionID, nil
		}
	}
	return 0, errors.New("no GitHub connection is bound to Code; connect GitHub and import or connect the repository with connection_id")
}

// PullRequest gathers the repository context and runs one action.
func (s *gitService) PullRequest(ctx *sdk.AppCtx, repo *Repo, in PullRequestInput) (*PullRequestResult, error) {
	remote, err := dbGetGitRemote(ctx.AppDB(), repo.ID, "origin")
	if err != nil {
		return nil, err
	}
	if remote == nil {
		return nil, errors.New("origin remote is not configured")
	}
	target, err := parseGitHubRemote(firstNonEmpty(remote.PushURL, remote.FetchURL))
	if err != nil {
		return nil, err
	}
	conn, err := pullRequestConnection(ctx, remote)
	if err != nil {
		return nil, err
	}
	branchState := func(string) (string, string) { return "", "" }
	if strings.EqualFold(strings.TrimSpace(in.Action), "create") {
		unlock := s.locks.rlock(repoStoreKey(repo))
		workTree, gitDir, err := s.paths(repo)
		if err != nil {
			unlock()
			return nil, err
		}
		status, err := s.engine.status(s.context(), workTree, gitDir)
		if err != nil {
			unlock()
			return nil, err
		}
		if strings.TrimSpace(in.Head) == "" {
			in.Head = status.Branch
		}
		head := strings.TrimSpace(in.Head)
		local := s.engine.refCommit(s.context(), workTree, gitDir, "refs/heads/"+head)
		pushed := s.engine.refCommit(s.context(), workTree, gitDir, "refs/remotes/"+remote.Name+"/"+head)
		unlock()
		branchState = func(string) (string, string) { return local, pushed }
	}
	if strings.EqualFold(strings.TrimSpace(in.Action), "create") && strings.TrimSpace(in.Base) == "" {
		in.Base = firstNonEmpty(remote.DefaultBranch, "main")
	}
	return reviewedPullRequest(ctx.AppDB(), repo.ID, ctx.PlatformAPI(), conn, target, in, branchState, mergeApprovalRequired())
}

// runPullRequest performs one action against GitHub. branchState returns the
// local and origin-tracking SHAs of the head branch (create only).
func runPullRequest(exec pullRequestExecutor, conn int64, target githubRepo, in PullRequestInput, branchState func(string) (string, string)) (*PullRequestResult, error) {
	action := strings.ToLower(strings.TrimSpace(in.Action))
	result := &PullRequestResult{Action: action, Repository: target.String()}
	base := map[string]any{"owner": target.Owner, "repo": target.Name}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	requireNumber := func() error {
		if in.Number <= 0 {
			return errors.New("number required")
		}
		return nil
	}
	switch action {
	case "create":
		title := strings.TrimSpace(in.Title)
		if title == "" {
			return nil, errors.New("title required")
		}
		head := strings.TrimSpace(in.Head)
		if head == "" {
			return nil, errors.New("head branch required (the checked-out branch is detached)")
		}
		if head == strings.TrimSpace(in.Base) {
			return nil, errors.New("head and base are the same branch")
		}
		local, pushed := branchState(head)
		if pushed == "" {
			return nil, fmt.Errorf("branch %s is not on origin; push it first with repos_git_push", head)
		}
		result.UnpushedCommits = local != "" && local != pushed
		base := strings.TrimSpace(in.Base)
		openPull := func() (*GitPullRequest, error) {
			var pulls []githubPull
			args := map[string]any{"state": "open", "head": target.Owner + ":" + head, "per_page": 100}
			if base != "" {
				args["base"] = base
			}
			if err := callGitHub(exec, conn, "list_pulls", with(args), &pulls); err != nil {
				return nil, err
			}
			for i := range pulls {
				if view := pulls[i].view(); view.Head == head && (base == "" || view.Base == base) {
					return view, nil
				}
			}
			return nil, nil
		}
		// A retried create returns the pull request the first attempt opened.
		existing, err := openPull()
		if err != nil {
			return nil, err
		}
		if existing != nil {
			result.PullRequest, result.Existing = existing, true
			break
		}
		var pr githubPull
		if err := callGitHub(exec, conn, "create_pull", with(map[string]any{
			"title": title, "head": head, "base": base, "body": in.Body, "draft": in.Draft,
		}), &pr); err != nil {
			// Opened concurrently between the check and the create.
			if strings.Contains(err.Error(), "already exists") {
				if existing, lookupErr := openPull(); lookupErr == nil && existing != nil {
					result.PullRequest, result.Existing = existing, true
					break
				}
			}
			return nil, err
		}
		result.PullRequest = pr.view()
	case "list":
		state := strings.ToLower(strings.TrimSpace(in.State))
		if state == "" {
			state = "open"
		}
		if state != "open" && state != "closed" && state != "all" {
			return nil, errors.New("state must be open, closed or all")
		}
		limit := in.Limit
		if limit <= 0 || limit > 100 {
			limit = 30
		}
		args := map[string]any{"state": state, "per_page": limit}
		if head := strings.TrimSpace(in.Head); head != "" {
			args["head"] = target.Owner + ":" + head
		}
		if b := strings.TrimSpace(in.Base); b != "" {
			args["base"] = b
		}
		var pulls []githubPull
		if err := callGitHub(exec, conn, "list_pulls", with(args), &pulls); err != nil {
			return nil, err
		}
		result.PullRequests = make([]GitPullRequest, 0, len(pulls))
		for i := range pulls {
			result.PullRequests = append(result.PullRequests, *pulls[i].view())
		}
		result.Count = len(result.PullRequests)
	case "get":
		if err := requireNumber(); err != nil {
			return nil, err
		}
		var pr githubPull
		if err := callGitHub(exec, conn, "get_pull", with(map[string]any{"pull_number": in.Number}), &pr); err != nil {
			return nil, err
		}
		result.PullRequest = pr.view()
	case "merge":
		if err := requireNumber(); err != nil {
			return nil, err
		}
		method := strings.ToLower(strings.TrimSpace(in.Method))
		if method == "" {
			method = "merge"
		}
		if method != "merge" && method != "squash" && method != "rebase" {
			return nil, errors.New("method must be merge, squash or rebase")
		}
		args := map[string]any{"pull_number": in.Number, "merge_method": method}
		if sha := strings.TrimSpace(in.ExpectedHeadSHA); sha != "" {
			args["sha"] = sha
		}
		if in.CommitTitle != "" {
			args["commit_title"] = in.CommitTitle
		}
		if in.CommitMessage != "" {
			args["commit_message"] = in.CommitMessage
		}
		var merged struct {
			Merged bool   `json:"merged"`
			SHA    string `json:"sha"`
		}
		if err := callGitHub(exec, conn, "merge_pull", with(args), &merged); err != nil {
			return nil, err
		}
		result.Merged, result.MergeSHA = merged.Merged, merged.SHA
		var pr githubPull
		if err := callGitHub(exec, conn, "get_pull", with(map[string]any{"pull_number": in.Number}), &pr); err == nil {
			result.PullRequest = pr.view()
		}
	case "close":
		if err := requireNumber(); err != nil {
			return nil, err
		}
		var pr githubPull
		if err := callGitHub(exec, conn, "update_pull", with(map[string]any{"pull_number": in.Number, "state": "closed"}), &pr); err != nil {
			return nil, err
		}
		result.PullRequest = pr.view()
	default:
		return nil, errors.New("action must be create, list, get, merge or close")
	}
	return result, nil
}

func callGitHub(exec pullRequestExecutor, conn int64, tool string, input map[string]any, out any) error {
	res, err := exec.ExecuteIntegrationTool(conn, tool, input)
	if err != nil {
		return fmt.Errorf("github %s: %w", tool, err)
	}
	if res == nil {
		return fmt.Errorf("github %s: empty response", tool)
	}
	if !res.Success || res.Status >= 400 {
		return fmt.Errorf("github %s failed (%d): %s", tool, res.Status, githubErrorText(res.Data))
	}
	if out == nil || len(res.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(res.Data, out); err != nil {
		return fmt.Errorf("github %s: unexpected response: %w", tool, err)
	}
	return nil
}

func githubErrorText(data json.RawMessage) string {
	var body struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Field   string `json:"field"`
		} `json:"errors"`
	}
	if json.Unmarshal(data, &body) == nil && body.Message != "" {
		details := []string{}
		for _, e := range body.Errors {
			details = append(details, strings.TrimSpace(firstNonEmpty(e.Message, strings.TrimSpace(e.Field+" "+e.Code))))
		}
		if len(details) > 0 {
			return body.Message + ": " + strings.Join(details, "; ")
		}
		return body.Message
	}
	text := strings.TrimSpace(string(data))
	if len(text) > 400 {
		text = text[:400]
	}
	return text
}

type githubPull struct {
	Number         int    `json:"number"`
	HTMLURL        string `json:"html_url"`
	State          string `json:"state"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	MergedAt       string `json:"merged_at"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	User           struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p *githubPull) view() *GitPullRequest {
	body := p.Body
	if len(body) > 4000 {
		body = body[:4000]
	}
	return &GitPullRequest{
		Number: p.Number, URL: p.HTMLURL, State: p.State, Title: p.Title, Body: body,
		Head: p.Head.Ref, HeadSHA: p.Head.SHA, Base: p.Base.Ref, Draft: p.Draft,
		Merged: p.Merged || p.MergedAt != "", Mergeable: p.Mergeable, MergeableState: p.MergeableState,
		Author: p.User.Login, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, MergedAt: p.MergedAt,
		MergeCommitSHA: p.MergeCommitSHA,
	}
}
