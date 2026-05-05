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
	if err != nil {
		return nil, err
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
	return f, nil
}

func (a *App) toolGetURLCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := a.requireFileAccess(ctx, app, args, "files.read"); err != nil {
		return nil, err
	}
	return a.toolGetURL(app, args)
}

func (a *App) toolDeleteCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := a.requireFileAccess(ctx, app, args, "files.delete"); err != nil {
		return nil, err
	}
	return a.toolDelete(app, args)
}

func (a *App) toolSetTagsCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := a.requireFileAccess(ctx, app, args, "files.write"); err != nil {
		return nil, err
	}
	return a.toolSetTags(app, args)
}

func (a *App) toolSetVisibilityCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := a.requireFileAccess(ctx, app, args, "files.write"); err != nil {
		return nil, err
	}
	return a.toolSetVisibility(app, args)
}

// ─── list / search / list_folders ──────────────────────────────────
//
// For these the platform can't pre-compute the resource (it'd have
// to know what files exist before they're queried), so the gate is
// in the handler — run the query, filter the result.

func (a *App) toolListCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	resp, err := a.toolList(app, args)
	if err != nil {
		return nil, err
	}
	caller := sdk.CallerFrom(ctx)
	if caller == nil {
		return resp, nil
	}
	m := resp.(map[string]any)
	files, _ := m["files"].([]*File)
	filtered := sdk.Filter(caller, "files.read", files, func(f *File) string {
		return fileResource(f.Folder)
	})
	m["files"] = filtered
	m["count"] = len(filtered)
	return m, nil
}

func (a *App) toolSearchCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	resp, err := a.toolSearch(app, args)
	if err != nil {
		return nil, err
	}
	caller := sdk.CallerFrom(ctx)
	if caller == nil {
		return resp, nil
	}
	m := resp.(map[string]any)
	files, _ := m["files"].([]*File)
	filtered := sdk.Filter(caller, "files.read", files, func(f *File) string {
		return fileResource(f.Folder)
	})
	m["files"] = filtered
	m["count"] = len(filtered)
	return m, nil
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
