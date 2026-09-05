package main

// Caller-aware tool handlers — the second half of storage's
// permissions-feature integration (the first half is the manifest's
// `provides.permissions` block + per-tool `requires`/`resource_from`
// annotations, which the SDK gate enforces pre-handler for write
// tools where the folder lives in args).
//
// This file holds the handler-side enforcement: the read/list/search/
// delete-by-id tools where the folder isn't known until after a row
// lookup. Each *Ctx variant pulls the Caller from context, runs the
// existing logic, then either filters returns (list/search) or
// returns sdk.Forbidden when the looked-up resource is outside the
// caller's scope.
//
// Resource-string convention: a folder's namespaced resource string
// is "folder/" + the folder path with the leading slash stripped.
// Root ("/") becomes "folder/" — matches "folder/**" globs.

import (
	"context"
	"errors"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// fileResource builds the namespaced resource string the gate
// matchers operate on.
func fileResource(folder string) string {
	return "folder/" + strings.TrimPrefix(folder, "/")
}

// requireFileAccess loads the file row and refuses if the caller
// doesn't hold `permission` on its folder. Returns the loaded *File
// so the handler can continue without re-querying. ErrForbidden
// surfaces as MCP -32000 with a stable prefix so clients can
// distinguish authz failures.
func (a *App) requireFileAccess(ctx context.Context, app *sdk.AppCtx, args map[string]any, permission string) (*File, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	f, err := dbGetByID(app.AppDB(), pid, id)
	if err == nil && f == nil && permission == "files.delete" {
		f, err = scanFile(app.AppDB().QueryRow(`SELECT `+fileSelectColumns+` FROM files WHERE id=? AND project_id=?`, id, pid), true)
	}
	if err != nil {
		return nil, err
	}
	// A missing/soft-deleted row has no resource to authorize. Return
	// it to the wrapper so each tool can preserve its own documented
	// contract (files_get => found=false, delete => idempotent success,
	// mutations/content/URL => not-found error). In particular, never
	// dereference f.Folder below when f is nil.
	if f == nil {
		return nil, nil
	}
	if caller := sdk.CallerFrom(ctx); caller != nil {
		res := fileResource(f.Folder)
		if !caller.Allows(permission, res) {
			return nil, sdk.Forbidden(permission, res)
		}
	}
	return f, nil
}

// ─── id-based tools ────────────────────────────────────────────────

func (a *App) toolGetCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	f, err := a.requireFileAccess(ctx, app, args, "files.read")
	if err != nil {
		return nil, err
	}
	if f == nil {
		return map[string]any{"file": nil, "found": false}, nil
	}
	return map[string]any{"file": f, "found": true}, nil
}

func (a *App) toolGetURLCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	f, err := a.requireFileAccess(ctx, app, args, "files.read")
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("file not found")
	}
	return a.toolGetURL(app, args)
}

func (a *App) toolGetContentCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	f, err := a.requireFileAccess(ctx, app, args, "files.read")
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("file not found")
	}
	return a.toolGetContent(app, args)
}

func (a *App) toolDeleteCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := a.requireFileAccess(ctx, app, args, "files.delete"); err != nil {
		return nil, err
	}
	// Deletion is intentionally idempotent. A missing row has no
	// protected resource and toolDelete already returns
	// {deleted:true, hard:false} without emitting an event.
	return a.toolDelete(app, args)
}

func (a *App) toolSetTagsCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	f, err := a.requireFileAccess(ctx, app, args, "files.write")
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("file not found")
	}
	return a.toolSetTags(app, args)
}

func (a *App) toolSetVisibilityCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	f, err := a.requireFileAccess(ctx, app, args, "files.write")
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("file not found")
	}
	return a.toolSetVisibility(app, args)
}

// toolMoveCtx protects both ends of a move. The source folder is loaded
// from the file row; the destination is either args.folder or the existing
// source folder for rename-only calls. The old manifest-only gate could
// check the destination but never the source, and treated an omitted folder
// as root even for a rename in place.
func (a *App) toolMoveCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	f, err := a.requireFileAccess(ctx, app, args, "files.write")
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("file not found")
	}
	if caller := sdk.CallerFrom(ctx); caller != nil {
		destination := f.Folder
		if raw, exists := args["folder"]; exists {
			if folder, ok := raw.(string); ok {
				destination, err = validateFolder(folder)
				if err != nil {
					return nil, err
				}
			}
		}
		resource := fileResource(destination)
		if !caller.Allows("files.write", resource) {
			return nil, sdk.Forbidden("files.write", resource)
		}
	}
	return a.toolMove(app, args)
}

// Dedupe is a discovery operation: an unauthorized hash match must look
// identical to no match, otherwise callers can enumerate file metadata
// outside their read scope.
func (a *App) toolDedupeCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := a.toolDedupe(app, args)
	if err != nil {
		return nil, err
	}
	caller := sdk.CallerFrom(ctx)
	if caller == nil {
		return out, nil
	}
	result := out.(map[string]any)
	f, _ := result["file"].(*File)
	if f == nil || caller.Allows("files.read", fileResource(f.Folder)) {
		return result, nil
	}
	return map[string]any{"found": false}, nil
}

func (a *App) toolRenameFolderCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	from, to, err := renameFolderArgs(args)
	if err != nil {
		return nil, err
	}
	if caller := sdk.CallerFrom(ctx); caller != nil {
		for _, folder := range []string{from, to} {
			res := fileResource(folder)
			if !caller.Allows("files.write", res) {
				return nil, sdk.Forbidden("files.write", res)
			}
		}
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := dbRenameFolderAuthorized(app.AppDB(), pid, from, to, func(folder string) error { _, e := authorizeFolder(ctx, "files.write", folder); return e })
	if err != nil {
		return nil, err
	}
	for _, f := range rows {
		emitFileEvent(app, "file.updated", f, false)
	}
	return map[string]any{"from": from, "to": to, "updated": len(rows)}, nil
}

// ─── list / search / list_folders ──────────────────────────────────
//
// For these the platform can't pre-compute the resource (it'd have
// to know what files exist before they're queried), so the gate is
// in the handler — run the query, filter the result.

func (a *App) toolListCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	return a.listFilesPage(ctx, app, args, true)
}

func (a *App) toolSearchCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	return a.listFilesPage(ctx, app, args, false)
}

func (a *App) toolListFoldersCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	resp, err := a.toolListFolders(app, args)
	if err != nil {
		return nil, err
	}
	caller := sdk.CallerFrom(ctx)
	if caller == nil {
		return resp, nil
	}
	m := resp.(map[string]any)
	folders, _ := m["folders"].([]string)
	parent := normaliseFolder(strArg(args, "parent"))
	// FilterTree is the load-bearing helper: ancestor stubs visible
	// so an agent scoped to "folder/invoices/**" still sees
	// "invoices" at root and can drill in.
	filtered := sdk.FilterTree(caller, "files.read", folders,
		func(seg string) string {
			// Resource string = parent + segment, normalized.
			full := strings.TrimPrefix(parent, "/") + seg
			return "folder/" + strings.TrimPrefix(full, "/")
		},
		func(seg string) string {
			full := strings.TrimPrefix(parent, "/") + seg
			return strings.TrimPrefix(full, "/")
		},
	)
	m["folders"] = filtered
	m["count"] = len(filtered)
	return m, nil
}
