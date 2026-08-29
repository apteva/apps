package main

import (
	"errors"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) gitMCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "repos_git_import",
			Description: "Clone a standard HTTPS Git remote into a new Code repository, preserving history and upstream tracking. Args: remote_url, ref?, name?, slug?, description?, framework?, connection_id?. Public remotes do not require a connection.",
			InputSchema: schemaObject(map[string]any{
				"remote_url": map[string]any{"type": "string"}, "ref": map[string]any{"type": "string"},
				"name": map[string]any{"type": "string"}, "slug": map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"}, "framework": map[string]any{"type": "string"},
				"connection_id": map[string]any{"type": "integer"},
			}, []string{"remote_url"}),
			Handler: a.toolGitImport,
		},
		{
			Name:        "repos_git_connect",
			Description: "Connect an existing Code repository to a standard HTTPS Git remote without overwriting local files. Matching trees attach directly; differing trees are preserved on apteva/local-before-connect and return reconciliation_required. Args: slug, remote_url, branch?, connection_id?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "remote_url": map[string]any{"type": "string"},
				"branch": map[string]any{"type": "string"}, "connection_id": map[string]any{"type": "integer"},
			}, []string{"slug", "remote_url"}),
			Handler: a.toolGitConnect,
		},
		{
			Name:        "repos_git_status",
			Description: "Get branch, HEAD, upstream, ahead/behind counts, and changed or conflicted paths. Args: slug.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}),
			Handler:     a.toolGitStatus,
		},
		{
			Name:        "repos_git_fetch",
			Description: "Fetch and prune origin tracking refs without changing checked-out files. Args: slug, actor?.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string"}}, []string{"slug"}),
			Handler:     a.toolGitFetch,
		},
		{
			Name:        "repos_git_pull",
			Description: "Fetch then fast-forward the current branch. Refuses dirty, conflicted, detached, or diverged repositories. Args: slug, actor?.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string"}}, []string{"slug"}),
			Handler:     a.toolGitPull,
		},
		{
			Name:        "repos_git_commit",
			Description: "Commit selected or all visible Code changes locally. Args: slug, message, paths? (all changes when omitted), author_name?, author_email?, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"},
				"paths":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"author_name": map[string]any{"type": "string"}, "author_email": map[string]any{"type": "string"},
				"actor": map[string]any{"type": "string"},
			}, []string{"slug", "message"}),
			Handler: a.toolGitCommit,
		},
		{
			Name:        "repos_git_push",
			Description: "Push the current branch to origin. Non-fast-forward pushes fail; force push is not exposed. Args: slug, set_upstream?, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "set_upstream": map[string]any{"type": "boolean"},
				"actor": map[string]any{"type": "string"},
			}, []string{"slug"}),
			Handler: a.toolGitPush,
		},
		{
			Name:        "repos_git_diff",
			Description: "Return a bounded unified diff for a Git-backed repository. Args: slug, base?, max_bytes?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "base": map[string]any{"type": "string"},
				"max_bytes": map[string]any{"type": "integer"},
			}, []string{"slug"}),
			Handler: a.toolGitDiff,
		},
		{
			Name:        "repos_git_log",
			Description: "List recent commits. Args: slug, limit? (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
			}, []string{"slug"}),
			Handler: a.toolGitLog,
		},
		{
			Name:        "repos_git_branches",
			Description: "List local and origin-tracking branches, including the current branch and upstream. Args: slug.",
			InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}),
			Handler:     a.toolGitBranches,
		},
		{
			Name:        "repos_git_branch_create",
			Description: "Create a local branch without switching to it. Args: slug, name, start_point?, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"},
				"start_point": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string"},
			}, []string{"slug", "name"}),
			Handler: a.toolGitBranchCreate,
		},
		{
			Name:        "repos_git_switch",
			Description: "Switch to an existing local branch. Refuses repositories with uncommitted or conflicted changes. Args: slug, name, actor?.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"},
				"actor": map[string]any{"type": "string"},
			}, []string{"slug", "name"}),
			Handler: a.toolGitSwitch,
		},
	}
}

func (a *App) requireGit() (*gitService, error) {
	if a.git == nil {
		return nil, errors.New("Git service is not initialized")
	}
	return a.git, nil
}

func (a *App) toolGitImport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.requireGit()
	if err != nil {
		return nil, err
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	return service.Import(ctx, GitImportInput{
		RemoteURL: strArg(args, "remote_url"), Ref: strArg(args, "ref"),
		Name: strArg(args, "name"), Slug: strArg(args, "slug"),
		Description: strArg(args, "description"), Framework: strArg(args, "framework"),
		ProjectID: pid, ConnectionID: int64(intArg(args, "connection_id", 0)),
	})
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

func (a *App) toolGitConnect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.Connect(ctx, repo, GitConnectInput{
		RemoteURL: strArg(args, "remote_url"), Branch: strArg(args, "branch"),
		ConnectionID: int64(intArg(args, "connection_id", 0)),
	})
}

func (a *App) toolGitStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.Status(ctx, repo)
}

func (a *App) toolGitFetch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.Fetch(ctx, repo, strArg(args, "actor"))
}

func (a *App) toolGitPull(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.Pull(ctx, repo, strArg(args, "actor"))
}

func (a *App) toolGitCommit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.Commit(ctx, repo, strArg(args, "message"), stringSliceArg(args, "paths"),
		strArg(args, "author_name"), strArg(args, "author_email"), strArg(args, "actor"))
}

func (a *App) toolGitPush(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.Push(ctx, repo, strArg(args, "actor"), boolArg(args, "set_upstream"))
}

func (a *App) toolGitDiff(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	diff, truncated, err := service.Diff(ctx, repo, strArg(args, "base"), intArg(args, "max_bytes", 256<<10))
	if err != nil {
		return nil, err
	}
	return map[string]any{"diff": diff, "truncated": truncated}, nil
}

func (a *App) toolGitLog(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	commits, err := service.Log(ctx, repo, intArg(args, "limit", 50))
	if err != nil {
		return nil, err
	}
	return map[string]any{"commits": commits, "count": len(commits)}, nil
}

func (a *App) toolGitBranches(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	branches, err := service.Branches(ctx, repo)
	if err != nil {
		return nil, err
	}
	return map[string]any{"branches": branches, "count": len(branches)}, nil
}

func (a *App) toolGitBranchCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.CreateBranch(ctx, repo, strArg(args, "name"), strArg(args, "start_point"), strArg(args, "actor"))
}

func (a *App) toolGitSwitch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	service, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return service.Switch(ctx, repo, strArg(args, "name"), strArg(args, "actor"))
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
