package main

import (
	"context"
	"errors"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) gitMCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "repos_git_import",
			Description: "Clone a standard HTTPS Git remote into a new Code repository, preserving history. An optional ref may name a branch, tag, or reachable commit SHA; branches retain upstream tracking while tags and commits are pinned with detached HEAD. Args: remote_url, ref?, name?, slug?, description?, framework?, connection_id?. Public remotes do not require a connection.",
			InputSchema: schemaObject(map[string]any{
				"remote_url": map[string]any{"type": "string"},
				"ref":        map[string]any{"type": "string", "description": "Optional remote branch, tag, or reachable commit SHA."},
				"name":       map[string]any{"type": "string"}, "slug": map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"}, "framework": map[string]any{"type": "string"},
				"connection_id": map[string]any{"type": "integer"},
			}, []string{"remote_url"}),
			HandlerCtx: a.toolGitImport,
		},
		{
			Name:        "repos_git_connect",
			Description: "Connect an existing Code repository to a standard HTTPS Git remote only when the user requests external Git integration. Git is optional: this is not needed to save edits, run HTML, or start/repair workspace previews. Does not overwrite local files. Matching trees attach directly; differing trees are preserved on apteva/local-before-connect and return reconciliation_required. Args: slug, remote_url, branch?, connection_id?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "remote_url": map[string]any{"type": "string"},
				"branch": map[string]any{"type": "string"}, "connection_id": map[string]any{"type": "integer"},
			}, []string{"slug", "remote_url"}),
			HandlerCtx: a.toolGitConnect,
		},
		{
			Name: "repos_git_status",
			Description: "Get whether a repository is externally Git-backed plus branch, HEAD, upstream, ahead/behind counts, and changed or conflicted paths. " +
				"Use this to check prerequisites for requested Git commits or sync, not ordinary edits/previews. If git_backed is false, external Git is not configured; do not commit or enable Git auto-sync. Ordinary Code editing and repos_dev_start work without Git or checkpoints. Args: slug.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}),
			HandlerCtx:  a.toolGitStatus,
		},
		{
			Name:        "repos_git_fetch",
			Description: "Fetch and prune origin tracking refs without changing checked-out files. Args: slug, actor?.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string"}}, []string{"slug"}),
			HandlerCtx:  a.toolGitFetch,
		},
		{
			Name:        "repos_git_pull",
			Description: "Fetch then fast-forward the current branch. Refuses dirty, conflicted, detached, or diverged repositories. Args: slug, actor?.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string"}}, []string{"slug"}),
			HandlerCtx:  a.toolGitPull,
		},
		{
			Name: "repos_git_commit",
			Description: "Commit selected or all visible Code changes locally, but only for a repository whose repos_git_status response has git_backed=true and when the user asked for an external Git commit. " +
				"Native-only repositories return an error; do not retry and do not describe a working-tree write as a commit. For ordinary edits, use code_write_file/code_edit_file and stop after verification. " +
				"Args: slug, message, paths? (all changes when omitted), author_name?, author_email?, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"},
				"paths":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"author_name": map[string]any{"type": "string"}, "author_email": map[string]any{"type": "string"},
				"actor": map[string]any{"type": "string"},
			}, []string{"slug", "message"}),
			HandlerCtx: a.toolGitCommit,
		},
		{
			Name: "repos_git_push",
			Description: "Push a local branch to origin; by default the checked-out branch to the same name. " +
				"source names another local branch and remote_branch another branch on origin, for example source=main remote_branch=release/x to advance a release branch. " +
				"Only new branches and fast-forwards are accepted; force push is not exposed. The response's pushed field says what was sent. Args: slug, source?, remote_branch?, set_upstream?, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "set_upstream": map[string]any{"type": "boolean"},
				"source":        map[string]any{"type": "string", "description": "Local branch to push (default: the checked-out branch)."},
				"remote_branch": map[string]any{"type": "string", "description": "Branch name on origin (default: same as source)."},
				"actor":         map[string]any{"type": "string"},
			}, []string{"slug"}),
			HandlerCtx: a.toolGitPush,
		},
		{
			Name: "repos_git_tag",
			Description: "Manage Git tags of a Git-backed repository. action=create makes an annotated tag (name, target? default HEAD, message?); " +
				"action=list lists local tags newest first (limit?, compare_remote? adds on_remote: same|different|absent); " +
				"action=push publishes one tag to origin (refused if origin already has that name; tags are never moved); " +
				"action=delete removes a local tag only. Args: slug, action, name?, target?, message?, limit?, compare_remote?, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug":           map[string]any{"type": "string"},
				"action":         map[string]any{"type": "string", "enum": []string{"create", "list", "push", "delete"}},
				"name":           map[string]any{"type": "string", "description": "Tag name, for example code/v0.19.0."},
				"target":         map[string]any{"type": "string", "description": "Commit, branch or tag to tag (create; default HEAD)."},
				"message":        map[string]any{"type": "string"},
				"limit":          map[string]any{"type": "integer"},
				"compare_remote": map[string]any{"type": "boolean"},
				"actor":          map[string]any{"type": "string"},
			}, []string{"slug", "action"}),
			HandlerCtx: a.toolGitTag,
		},
		{
			Name: "repos_git_pull_request",
			Description: "GitHub pull requests for a Git-backed repository whose origin is on github.com, through its GitHub connection. " +
				"action=create opens one from head (default the checked-out branch, which must already be pushed) into base (default the remote's default branch): title, body?, draft?; if one is already open for that head and base it is returned with existing=true, so retrying is safe. " +
				"action=list (state? open|closed|all, head?, base?, limit?), action=get (number), action=merge (number, method? merge|squash|rebase, expected_head_sha?, commit_title?, commit_message?), action=close (number). " +
				"Merging needs a person's approval of the pull request's current head (approved_by in list/get): an agent's merge is refused until a person approves it in the Code panel, and is pinned to the approved head. " +
				"action=approve (number, expected_head_sha?) is for people only; agents cannot approve. Args: slug, action, ...",
			InputSchema: schemaObject(map[string]any{
				"slug":              map[string]any{"type": "string"},
				"action":            map[string]any{"type": "string", "enum": []string{"create", "list", "get", "approve", "merge", "close"}},
				"number":            map[string]any{"type": "integer"},
				"title":             map[string]any{"type": "string"},
				"body":              map[string]any{"type": "string"},
				"head":              map[string]any{"type": "string"},
				"base":              map[string]any{"type": "string"},
				"draft":             map[string]any{"type": "boolean"},
				"state":             map[string]any{"type": "string", "enum": []string{"open", "closed", "all"}},
				"limit":             map[string]any{"type": "integer"},
				"method":            map[string]any{"type": "string", "enum": []string{"merge", "squash", "rebase"}},
				"expected_head_sha": map[string]any{"type": "string", "description": "Merge only if the pull request head is still this commit."},
				"commit_title":      map[string]any{"type": "string"},
				"commit_message":    map[string]any{"type": "string"},
			}, []string{"slug", "action"}),
			HandlerCtx: a.toolGitPullRequest,
		},
		{
			Name:        "repos_git_diff",
			Description: "Return a bounded unified diff for a Git-backed repository. Args: slug, base?, max_bytes?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "base": map[string]any{"type": "string"},
				"max_bytes": map[string]any{"type": "integer"},
			}, []string{"slug"}),
			HandlerCtx: a.toolGitDiff,
		},
		{
			Name:        "repos_git_log",
			Description: "List recent commits. Args: slug, limit? (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
			}, []string{"slug"}),
			HandlerCtx: a.toolGitLog,
		},
		{
			Name:        "repos_git_branches",
			Description: "List local and origin-tracking branches, including the current branch and upstream. Args: slug.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}),
			HandlerCtx:  a.toolGitBranches,
		},
		{
			Name:        "repos_git_branch_create",
			Description: "Create a local branch without switching to it. Args: slug, name, start_point?, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"},
				"start_point": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string"},
			}, []string{"slug", "name"}),
			HandlerCtx: a.toolGitBranchCreate,
		},
		{
			Name: "repos_git_branch_delete",
			Description: "Delete a branch locally, on origin, or both (where=local|remote|both, default local). " +
				"A branch whose commits are not in the checked-out branch or origin's default branch is refused unless force=true; " +
				"the checked-out branch and origin's default branch are never deleted. The origin deletion only happens if the branch still points where it was checked. " +
				"The result returns each deleted tip SHA so the branch can be recreated with repos_git_branch_create start_point. Args: slug, name, where?, force?, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"},
				"where": map[string]any{"type": "string", "enum": []string{"local", "remote", "both"}, "description": "local (default), remote (origin only) or both."},
				"force": map[string]any{"type": "boolean", "description": "Delete even if the branch has commits not merged anywhere else."},
				"actor": map[string]any{"type": "string"},
			}, []string{"slug", "name"}),
			HandlerCtx: a.toolGitBranchDelete,
		},
		{
			Name:        "repos_git_switch",
			Description: "Switch to an existing local branch. Refuses repositories with uncommitted or conflicted changes. Args: slug, name, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"},
				"actor": map[string]any{"type": "string"},
			}, []string{"slug", "name"}),
			HandlerCtx: a.toolGitSwitch,
		},
	}
}

func (a *App) requireGit() (*gitService, error) {
	if a.git == nil {
		return nil, errors.New("Git service is not initialized")
	}
	return a.git, nil
}

func (a *App) toolGitImport(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.requireGit()
	if err != nil {
		return nil, err
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	result, err := service.withContext(callCtx).Import(ctx, GitImportInput{
		RemoteURL: strArg(args, "remote_url"), Ref: strArg(args, "ref"),
		Name: strArg(args, "name"), Slug: strArg(args, "slug"),
		Description: strArg(args, "description"), Framework: strArg(args, "framework"),
		ProjectID: pid, ConnectionID: int64(intArg(args, "connection_id", 0)),
	})
	if err != nil {
		return nil, err
	}
	if err := a.ensureNativeRevision(result.Repository); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *App) gitRepoFromArgs(ctx *sdk.AppCtx, args map[string]any) (*gitService, *Repo, error) {
	service, err := a.requireGit()
	if err != nil {
		return nil, nil, err
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	return service, repo, err
}

func (a *App) toolGitConnect(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	result, err := service.withContext(callCtx).Connect(ctx, repo, GitConnectInput{
		RemoteURL: strArg(args, "remote_url"), Branch: strArg(args, "branch"),
		ConnectionID: int64(intArg(args, "connection_id", 0)),
	})
	if err != nil {
		return nil, err
	}
	if err := a.ensureNativeRevision(repo); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *App) toolGitStatus(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.withContext(callCtx).Status(ctx, repo)
}

func (a *App) toolGitFetch(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.withContext(callCtx).Fetch(ctx, repo, strArg(args, "actor"))
}

func (a *App) toolGitPull(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	result, err := service.withContext(callCtx).Pull(ctx, repo, strArg(args, "actor"))
	if err != nil {
		return nil, err
	}
	if err := a.ensureNativeRevision(repo); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *App) toolGitCommit(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	result, err := service.withContext(callCtx).Commit(ctx, repo, strArg(args, "message"), stringSliceArg(args, "paths"),
		strArg(args, "author_name"), strArg(args, "author_email"), strArg(args, "actor"))
	if err != nil {
		return nil, err
	}
	if err := a.ensureNativeRevision(repo); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *App) toolGitPush(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.withContext(callCtx).PushBranch(ctx, repo, GitPushInput{
		Actor: strArg(args, "actor"), SetUpstream: boolArg(args, "set_upstream"),
		Source: strArg(args, "source"), RemoteBranch: strArg(args, "remote_branch"),
	})
}

func (a *App) toolGitTag(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	service = service.withContext(callCtx)
	name, actor := strArg(args, "name"), strArg(args, "actor")
	switch strings.ToLower(strArg(args, "action")) {
	case "create":
		return service.CreateTag(ctx, repo, name, strArg(args, "target"), strArg(args, "message"), actor)
	case "list":
		return service.ListTags(ctx, repo, intArg(args, "limit", 100), boolArg(args, "compare_remote"))
	case "push":
		return service.PushTag(ctx, repo, name, actor)
	case "delete":
		return service.DeleteTag(ctx, repo, name, actor)
	default:
		return nil, errors.New("action must be create, list, push or delete")
	}
}

func (a *App) toolGitPullRequest(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.withContext(callCtx).PullRequest(ctx, repo, pullRequestInputFromArgs(args))
}

func pullRequestInputFromArgs(args map[string]any) PullRequestInput {
	return PullRequestInput{
		Action: strArg(args, "action"), Number: intArg(args, "number", 0),
		Title: strArg(args, "title"), Body: strArg(args, "body"),
		Head: strArg(args, "head"), Base: strArg(args, "base"), Draft: boolArg(args, "draft"),
		State: strArg(args, "state"), Limit: intArg(args, "limit", 0),
		Method: strArg(args, "method"), ExpectedHeadSHA: strArg(args, "expected_head_sha"),
		CommitTitle: strArg(args, "commit_title"), CommitMessage: strArg(args, "commit_message"),
		Actor: strArg(args, "actor"),
	}
}

func (a *App) toolGitDiff(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	diff, truncated, err := service.withContext(callCtx).Diff(ctx, repo, strArg(args, "base"), intArg(args, "max_bytes", 256<<10))
	if err != nil {
		return nil, err
	}
	return map[string]any{"diff": diff, "truncated": truncated}, nil
}

func (a *App) toolGitLog(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	commits, err := service.withContext(callCtx).Log(ctx, repo, intArg(args, "limit", 50))
	if err != nil {
		return nil, err
	}
	return map[string]any{"commits": commits, "count": len(commits)}, nil
}

func (a *App) toolGitBranches(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	branches, err := service.withContext(callCtx).Branches(ctx, repo)
	if err != nil {
		return nil, err
	}
	return map[string]any{"branches": branches, "count": len(branches)}, nil
}

func (a *App) toolGitBranchCreate(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.withContext(callCtx).CreateBranch(ctx, repo, strArg(args, "name"), strArg(args, "start_point"), strArg(args, "actor"))
}

func (a *App) toolGitBranchDelete(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.withContext(callCtx).DeleteBranch(ctx, repo, GitBranchDeleteInput{
		Name: strArg(args, "name"), Where: strArg(args, "where"), Force: boolArg(args, "force"), Actor: strArg(args, "actor"),
	})
}

func (a *App) toolGitSwitch(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	result, err := service.withContext(callCtx).Switch(ctx, repo, strArg(args, "name"), strArg(args, "actor"))
	if err != nil {
		return nil, err
	}
	if err := a.ensureNativeRevision(repo); err != nil {
		return nil, err
	}
	return result, nil
}

func stringSliceArg(args map[string]any, key string) []string {
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	switch items := value.(type) {
	case []string:
		return items
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
