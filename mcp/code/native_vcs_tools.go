package main

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) nativeMCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "repos_version_status", Description: "Get native Code version-control status. Editing remains available when the working tree is dirty; revisions are managed in the background. Args: slug.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}), HandlerCtx: a.toolNativeStatus},
		{Name: "repos_checkpoint", Description: "Optionally create an immutable native Code revision from the current working tree. No external Git provider is required, and ordinary file edits do not require a checkpoint. Use only when the user asks to save a named revision or the workflow calls for one; this is not an external Git commit. Pass expected_head (the head revision you last saw) to refuse the checkpoint if someone else moved the branch meanwhile. Args: slug, message?, actor?, expected_head?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string"}, "expected_head": map[string]any{"type": "string"}}, []string{"slug"}), HandlerCtx: a.toolNativeCheckpoint},
		{Name: "repos_history", Description: "List immutable native Code revisions on the current branch. Args: slug, limit?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, []string{"slug"}), HandlerCtx: a.toolNativeHistory},
		{Name: "repos_diff", Description: "Compare the current working tree or two native revisions and return changed paths. Args: slug, from_revision?, to_revision?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "from_revision": map[string]any{"type": "string"}, "to_revision": map[string]any{"type": "string"}}, []string{"slug"}), HandlerCtx: a.toolNativeDiff},
		{Name: "repos_branch_create", Description: "Create a native Code branch at a revision (defaults to current head). Args: slug, name, start_revision?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "start_revision": map[string]any{"type": "string"}}, []string{"slug", "name"}), HandlerCtx: a.toolNativeBranchCreate},
		{Name: "repos_branch_switch", Description: "Switch the native working tree to a clean branch. Args: slug, name.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}}, []string{"slug", "name"}), HandlerCtx: a.toolNativeBranchSwitch},
		{Name: "repos_tag_create", Description: "Create an immutable native tag at a revision (defaults to current head). Args: slug, name, revision?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "revision": map[string]any{"type": "string"}}, []string{"slug", "name"}), HandlerCtx: a.toolNativeTagCreate},
		{Name: "repos_tags_list", Description: "List native Code tags and their immutable revisions. Args: slug.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}), HandlerCtx: a.toolNativeTagsList},
		{Name: "repos_restore", Description: "Restore selected paths from a native revision. Defaults to a new revert revision; pass working_tree_only=true only for an uncommitted working-tree restore. Args: slug, revision, paths?, working_tree_only?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "revision": map[string]any{"type": "string"}, "paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "working_tree_only": map[string]any{"type": "boolean"}}, []string{"slug", "revision"}), HandlerCtx: a.toolNativeRestore},
		{Name: "repos_undo", Description: "Undo native branch moves. Without revision, lists the branch's recent moves (checkpoints, restores, branch creation, undos) newest first. With revision, moves the branch back to a revision from that list; for the checked-out branch the working tree is restored too, which requires no changes outside a revision. The previous head stays in the list, so an undo can itself be undone. Args: slug, branch? (default: checked-out branch), revision?, limit?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "branch": map[string]any{"type": "string"}, "revision": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, []string{"slug"}), HandlerCtx: a.toolNativeUndo},
		{Name: "repos_export_ref", Description: "Export the exact source tree at a native revision as a bounded base64 ZIP. Args: slug, revision.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "revision": map[string]any{"type": "string"}}, []string{"slug", "revision"}), HandlerCtx: a.toolNativeExportRef},
	}
}

func (a *App) nativeRepo(ctx *sdk.AppCtx, args map[string]any) (*nativeVCS, *Repo, error) {
	if a.native == nil {
		return nil, nil, errors.New("native version control is not initialized")
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, nil, err
	}
	return a.native, repo, nil
}

func (a *App) toolNativeStatus(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	return n.status(repo)
}

func (a *App) toolNativeCheckpoint(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	message := strings.TrimSpace(strArg(args, "message"))
	if message == "" {
		message = "Checkpoint"
	}
	rev, err := n.checkpointExpect(repo, message, strArg(args, "actor"), strArg(args, "expected_head"))
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("repo.revision.created", map[string]any{"id": repo.ID, "slug": repo.Slug, "revision": rev.ID, "message": rev.Message})
	}
	return map[string]any{"revision": rev}, nil
}

func (a *App) toolNativeHistory(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	revisions, err := n.history(repo, intArg(args, "limit", 50))
	if err != nil {
		return nil, err
	}
	return map[string]any{"revisions": revisions, "count": len(revisions)}, nil
}

func (a *App) toolNativeDiff(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	from, to, paths, err := n.changedPaths(repo, strArg(args, "from_revision"), strArg(args, "to_revision"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"from_revision": from, "to_revision": to, "changed_paths": paths, "count": len(paths)}, nil
}

func (a *App) toolNativeBranchCreate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := n.createBranch(repo, strArg(args, "name"), strArg(args, "start_revision")); err != nil {
		return nil, err
	}
	return n.status(repo)
}
func (a *App) toolNativeBranchSwitch(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := n.switchBranch(repo, strArg(args, "name")); err != nil {
		return nil, err
	}
	return n.status(repo)
}
func (a *App) toolNativeTagCreate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := n.createTag(repo, strArg(args, "name"), strArg(args, "revision")); err != nil {
		return nil, err
	}
	return n.tags(repo)
}
func (a *App) toolNativeTagsList(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	tags, err := n.tags(repo)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tags": tags, "count": len(tags)}, nil
}

func (a *App) toolNativeRestore(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	workingTreeOnly := boolArg(args, "working_tree_only")
	rev, err := n.restore(repo, strArg(args, "revision"), stringSliceArg(args, "paths"), workingTreeOnly, strArg(args, "actor"))
	if err != nil {
		return nil, err
	}
	if workingTreeOnly {
		return n.status(repo)
	}
	if ctx != nil {
		ctx.Emit("repo.revision.created", map[string]any{"id": repo.ID, "slug": repo.Slug, "revision": rev.ID, "message": rev.Message})
	}
	return map[string]any{"revision": rev}, nil
}

func (a *App) toolNativeUndo(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	revision := strings.TrimSpace(strArg(args, "revision"))
	if revision == "" {
		branch, moves, err := n.refMoves(repo, strArg(args, "branch"), intArg(args, "limit", 20))
		if err != nil {
			return nil, err
		}
		return map[string]any{"branch": branch, "moves": moves, "count": len(moves)}, nil
	}
	branch, move, err := n.undo(repo, strArg(args, "branch"), revision, strArg(args, "actor"))
	if err != nil {
		return nil, err
	}
	status, err := n.status(repo)
	if err != nil {
		return nil, err
	}
	if move == nil {
		return map[string]any{"branch": branch, "moved": false, "status": status}, nil
	}
	if ctx != nil {
		ctx.Emit("repo.ref.moved", map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": branch, "from": move.From, "to": move.To, "action": "undo"})
	}
	return map[string]any{"branch": branch, "moved": true, "move": move, "status": status}, nil
}

func (a *App) toolNativeExportRef(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	body, sha, err := n.exportRef(repo, strArg(args, "revision"))
	if err != nil {
		return nil, err
	}
	if len(body) > 32<<20 {
		return nil, errors.New("native revision export exceeds 32 MiB")
	}
	return map[string]any{"revision": strArg(args, "revision"), "sha256": sha, "size": len(body), "zip_b64": base64.StdEncoding.EncodeToString(body)}, nil
}
