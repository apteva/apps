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
		{Name: "repos_history", Description: "List immutable native Code revisions, newest first. Defaults to the checked-out branch; ref may name another branch, tag or revision. With path, lists only revisions that changed that file or folder. With path and lines=true, returns blame instead: the revision, actor and date that last changed each line of the file. Args: slug, ref?, path?, lines?, limit?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "ref": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"}, "lines": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer"}}, []string{"slug"}), HandlerCtx: a.toolNativeHistory},
		{Name: "repos_diff", Description: "Compare two native refs (revision IDs, branch or tag names), or a ref and the working tree, and return changed paths. Defaults: from = checked-out head, to = working tree. Pass lines=true for per-file unified patches with added/removed counts, optionally limited to paths. Args: slug, from_revision?, to_revision?, lines?, paths?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "from_revision": map[string]any{"type": "string"}, "to_revision": map[string]any{"type": "string"}, "lines": map[string]any{"type": "boolean"}, "paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, []string{"slug"}), HandlerCtx: a.toolNativeDiff},
		{Name: "repos_branch", Description: "Manage native Code branches with one action. list: every branch with its head. create: name, start? (revision, branch or tag; default checked-out head); with switch=true the new branch starts at the checked-out head and is checked out at once, carrying unsaved changes with it (use this to start isolated work). switch: name; changes that are not in a revision are auto-stashed for the branch you leave and restored when you come back. delete: name, force? (refuses unmerged branches without force; the branch can be recreated from its head). compare: name (from) against target? (default checked-out branch): ahead/behind, merge base, fast-forward possible, changed paths. protect: name, required_approvals? (default 0): the branch then only changes through merged change_requests. unprotect: name. Args: slug, action, name?, start?, switch?, target?, force?, required_approvals?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "action": map[string]any{"type": "string", "enum": []string{"list", "create", "switch", "delete", "compare", "protect", "unprotect"}}, "name": map[string]any{"type": "string"}, "start": map[string]any{"type": "string"}, "switch": map[string]any{"type": "boolean"}, "target": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}, "required_approvals": map[string]any{"type": "integer"}}, []string{"slug", "action"}), HandlerCtx: a.toolNativeBranch},
		{Name: "change_requests", Description: "Native change requests: ask to land a source branch on a target branch, review it, and merge it. Protected branches (repos_branch action=protect) only change this way. create: title, source, target? (default main), body?, issues? (issue numbers). list: state? open|merged|closed|all. get: number, lines? (include unified patches); shows reviews, mergeability, blockers and notes. comment: number, body, path?, line?. review: number, verdict approve|request_changes|comment, body? (approvals count only on the current source head; authors cannot approve their own). merge: number, mode? merge|squash, message?, delete_source?; merges without needing the target checked out, refused while blockers remain. close / reopen: number. Args: slug, action, number?, title?, body?, source?, target?, issues?, state?, lines?, path?, line?, verdict?, mode?, message?, delete_source?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "action": map[string]any{"type": "string", "enum": []string{"create", "list", "get", "comment", "review", "merge", "close", "reopen"}}, "number": map[string]any{"type": "integer"}, "title": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}, "source": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "issues": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "state": map[string]any{"type": "string", "enum": []string{"open", "merged", "closed", "all"}}, "lines": map[string]any{"type": "boolean"}, "path": map[string]any{"type": "string"}, "line": map[string]any{"type": "integer"}, "verdict": map[string]any{"type": "string", "enum": []string{"approve", "request_changes", "comment"}}, "mode": map[string]any{"type": "string", "enum": []string{"merge", "squash"}}, "message": map[string]any{"type": "string"}, "delete_source": map[string]any{"type": "boolean"}}, []string{"slug", "action"}), HandlerCtx: a.toolChangeRequests},
		{Name: "repos_merge", Description: "Combine native history into the checked-out branch. start: source (branch, tag or revision), mode? merge (default; fast-forwards when possible unless no_ff) | squash (one ordinary revision) | cherry_pick (apply one revision) | revert (undo one revision), message?, markers? (write conflict markers into files). Lines changed on only one side merge automatically. Conflicts come back as data with base/ours/theirs content and leave a merge in progress: resolve each with action=resolve, path, and choice (ours, theirs, base, delete, working = keep the file as edited) or content; then action=continue (records the revision) or action=abort (restores the branch head). status shows the merge in progress. While a merge is in progress, checkpoints, branch switches and undo are refused. Args: slug, action, source?, mode?, message?, markers?, no_ff?, path?, choice?, content?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "action": map[string]any{"type": "string", "enum": []string{"start", "status", "resolve", "continue", "abort"}}, "source": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string", "enum": []string{"merge", "squash", "cherry_pick", "revert"}}, "message": map[string]any{"type": "string"}, "markers": map[string]any{"type": "boolean"}, "no_ff": map[string]any{"type": "boolean"}, "path": map[string]any{"type": "string"}, "choice": map[string]any{"type": "string", "enum": []string{"ours", "theirs", "base", "delete", "working"}}, "content": map[string]any{"type": "string"}}, []string{"slug", "action"}), HandlerCtx: a.toolNativeMerge},
		{Name: "repos_branch_create", Description: "Create a native Code branch at a revision (defaults to current head). Args: slug, name, start_revision?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "start_revision": map[string]any{"type": "string"}}, []string{"slug", "name"}), HandlerCtx: a.toolNativeBranchCreate},
		{Name: "repos_branch_switch", Description: "Switch the native working tree to another branch; unsaved changes are auto-stashed for the branch you leave and restored when you return (same as repos_branch action=switch). Args: slug, name.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}}, []string{"slug", "name"}), HandlerCtx: a.toolNativeBranchSwitch},
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
	ref, path := strings.TrimSpace(strArg(args, "ref")), strings.TrimSpace(strArg(args, "path"))
	if boolArg(args, "lines") {
		if path == "" {
			return nil, errors.New("lines=true (blame) needs path")
		}
		head, lines, truncated, err := n.blame(repo, ref, path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"revision": head, "path": path, "lines": lines, "count": len(lines), "truncated": truncated}, nil
	}
	if path != "" {
		head, revisions, err := n.pathHistory(repo, ref, path, intArg(args, "limit", 50))
		if err != nil {
			return nil, err
		}
		return map[string]any{"revision": head, "path": path, "revisions": revisions, "count": len(revisions)}, nil
	}
	revisions, err := n.historyRef(repo, ref, intArg(args, "limit", 50))
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
	paths := stringSliceArg(args, "paths")
	if boolArg(args, "lines") || len(paths) > 0 {
		from, to, files, err := n.lineDiff(repo, strArg(args, "from_revision"), strArg(args, "to_revision"), paths)
		if err != nil {
			return nil, err
		}
		changed := make([]string, 0, len(files))
		for _, f := range files {
			changed = append(changed, f.Path)
		}
		if !boolArg(args, "lines") {
			for i := range files {
				files[i].Patch = ""
			}
		}
		return map[string]any{"from_revision": from, "to_revision": to, "changed_paths": changed, "count": len(changed), "files": files}, nil
	}
	from, to, err := n.resolvePair(repo, strArg(args, "from_revision"), strArg(args, "to_revision"))
	if err != nil {
		return nil, err
	}
	from, to, changed, err := n.changedPaths(repo, from, to)
	if err != nil {
		return nil, err
	}
	return map[string]any{"from_revision": from, "to_revision": to, "changed_paths": changed, "count": len(changed)}, nil
}

func (a *App) toolNativeBranch(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name"))
	action := strings.TrimSpace(strArg(args, "action"))
	needName := func() error {
		if name == "" {
			return errors.New(action + " needs name")
		}
		return nil
	}
	switch action {
	case "list":
		branches, err := n.branchList(repo)
		if err != nil {
			return nil, err
		}
		return map[string]any{"branches": branches, "count": len(branches)}, nil
	case "create":
		if err := needName(); err != nil {
			return nil, err
		}
		start := strings.TrimSpace(strArg(args, "start"))
		if start == "" {
			start = strings.TrimSpace(strArg(args, "start_revision"))
		}
		if boolArg(args, "switch") {
			if start != "" {
				return nil, errors.New("create with switch=true starts at the checked-out head; omit start")
			}
			if err := n.createAndSwitch(repo, name, strArg(args, "actor")); err != nil {
				return nil, err
			}
		} else if err := n.createBranchRef(repo, name, start, strArg(args, "actor")); err != nil {
			return nil, err
		}
		if ctx != nil {
			ctx.Emit("repo.branch.created", map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": name})
		}
		return n.status(repo)
	case "switch":
		if err := needName(); err != nil {
			return nil, err
		}
		return a.nativeSwitch(ctx, n, repo, name, strArg(args, "actor"))
	case "delete":
		if err := needName(); err != nil {
			return nil, err
		}
		head, err := n.deleteBranch(repo, name, boolArg(args, "force"), strArg(args, "actor"))
		if err != nil {
			return nil, err
		}
		if ctx != nil {
			ctx.Emit("repo.branch.deleted", map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": name, "head": head})
		}
		return map[string]any{"deleted": name, "head": head, "recover": "repos_branch action=create name=" + name + " start=" + head}, nil
	case "compare":
		if err := needName(); err != nil {
			return nil, err
		}
		return n.compareRefs(repo, name, strArg(args, "target"))
	case "protect", "unprotect":
		if err := needName(); err != nil {
			return nil, err
		}
		approvals := -1
		if action == "protect" {
			approvals = intArg(args, "required_approvals", 0)
			if approvals < 0 {
				return nil, errors.New("required_approvals must be between 0 and 10")
			}
		}
		prot, err := n.setProtection(repo, name, approvals, strArg(args, "actor"))
		if err != nil {
			return nil, err
		}
		if ctx != nil {
			ctx.Emit("repo.branch.protection", map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": name, "protected": prot != nil})
		}
		return map[string]any{"branch": name, "protected": prot != nil, "protection": prot}, nil
	case "":
		return nil, errors.New("action is required: list, create, switch, delete, compare, protect or unprotect")
	default:
		return nil, errors.New("unknown action " + action + ": use list, create, switch, delete, compare, protect or unprotect")
	}
}

func intSliceArg(args map[string]any, key string) []int {
	raw, _ := args[key].([]any)
	out := []int{}
	for _, v := range raw {
		switch x := v.(type) {
		case float64:
			out = append(out, int(x))
		case int:
			out = append(out, x)
		}
	}
	return out
}

func (a *App) toolChangeRequests(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, errors.New("change requests need the app database")
	}
	svc := &changeRequestService{db: ctx.AppDB(), n: n}
	actor := strArg(args, "actor")
	number := intArg(args, "number", 0)
	emit := func(event string, payload map[string]any) {
		payload["id"], payload["slug"] = repo.ID, repo.Slug
		ctx.Emit(event, payload)
	}
	switch action := strings.TrimSpace(strArg(args, "action")); action {
	case "create":
		cr, err := svc.create(repo, changeRequestCreate{Title: strArg(args, "title"), Body: strArg(args, "body"), Source: strArg(args, "source"), Target: strArg(args, "target"), Issues: intSliceArg(args, "issues"), Author: actor})
		if err != nil {
			return nil, err
		}
		emit("repo.change_request.opened", map[string]any{"number": cr.Number, "source": cr.Source, "target": cr.Target})
		return svc.detail(repo, cr.Number, false)
	case "list":
		list, err := svc.list(repo, strArg(args, "state"), intArg(args, "limit", 50))
		if err != nil {
			return nil, err
		}
		return map[string]any{"change_requests": list, "count": len(list)}, nil
	case "get":
		return svc.detail(repo, number, boolArg(args, "lines"))
	case "comment":
		return svc.comment(repo, number, actor, strArg(args, "body"), strArg(args, "path"), intArg(args, "line", 0))
	case "review":
		d, err := svc.review(repo, number, actor, strArg(args, "verdict"), strArg(args, "body"))
		if err != nil {
			return nil, err
		}
		emit("repo.change_request.reviewed", map[string]any{"number": number, "verdict": strArg(args, "verdict")})
		return d, nil
	case "merge":
		d, res, err := svc.merge(repo, number, actor, strArg(args, "mode"), strArg(args, "message"), boolArg(args, "delete_source"))
		if err != nil {
			return nil, err
		}
		emit("repo.change_request.merged", map[string]any{"number": number, "revision": res.Revision.ID, "target": d.Target})
		emit("repo.revision.created", map[string]any{"revision": res.Revision.ID, "message": res.Revision.Message})
		return map[string]any{"change_request": d, "merge": res}, nil
	case "close", "reopen":
		state := map[string]string{"close": "closed", "reopen": "open"}[action]
		d, err := svc.setState(repo, number, actor, state)
		if err != nil {
			return nil, err
		}
		emit("repo.change_request."+state, map[string]any{"number": number})
		return d, nil
	case "":
		return nil, errors.New("action is required: create, list, get, comment, review, merge, close or reopen")
	default:
		return nil, errors.New("unknown action " + action + ": use create, list, get, comment, review, merge, close or reopen")
	}
}

func (a *App) nativeSwitch(ctx *sdk.AppCtx, n *nativeVCS, repo *Repo, name, actor string) (any, error) {
	result, err := n.switchBranchActor(repo, name, actor)
	if err != nil {
		return nil, err
	}
	status, err := n.status(repo)
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("repo.branch.switched", map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": name})
	}
	return map[string]any{"switch": result, "status": status, "branch": status.Branch, "head": status.Head, "dirty": status.Dirty}, nil
}

func (a *App) toolNativeMerge(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, repo, err := a.nativeRepo(ctx, args)
	if err != nil {
		return nil, err
	}
	var res *nativeMergeResult
	switch action := strings.TrimSpace(strArg(args, "action")); action {
	case "start":
		res, err = n.mergeStart(repo, nativeMergeOptions{Source: strArg(args, "source"), Mode: strArg(args, "mode"), Message: strArg(args, "message"), Actor: strArg(args, "actor"), Markers: boolArg(args, "markers"), NoFF: boolArg(args, "no_ff")})
	case "status":
		res, err = n.mergeStatus(repo)
	case "resolve":
		var content *string
		if v, ok := args["content"].(string); ok {
			content = &v
		}
		res, err = n.mergeResolve(repo, strArg(args, "path"), strArg(args, "choice"), content)
	case "continue":
		res, err = n.mergeContinue(repo, strArg(args, "message"), strArg(args, "actor"))
	case "abort":
		res, err = n.mergeAbort(repo)
	case "":
		return nil, errors.New("action is required: start, status, resolve, continue or abort")
	default:
		return nil, errors.New("unknown action " + action + ": use start, status, resolve, continue or abort")
	}
	if err != nil {
		return nil, err
	}
	if ctx != nil && res.Revision != nil && (res.Status == "merged" || res.Status == "fast_forward") {
		ctx.Emit("repo.revision.created", map[string]any{"id": repo.ID, "slug": repo.Slug, "revision": res.Revision.ID, "message": res.Revision.Message, "merge": res.Mode})
	}
	return res, nil
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
	return a.nativeSwitch(ctx, n, repo, strArg(args, "name"), strArg(args, "actor"))
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
