package main

// End-to-end scope tests through the storage MCP tools — exercises
// the user's exact scenario: an agent scoped to invoices/** lists
// folders/files and only sees what the policy allows. Plus the
// back-compat case (nil caller passes through unchanged).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// withCaller stamps a Caller onto a ctx for the *Ctx tool variants.
func withCaller(grants ...sdk.Grant) context.Context {
	c := &sdk.Caller{
		AgentID:       7,
		DefaultEffect: "deny",
		Grants:        grants,
		Resources: []sdk.ResourceDecl{
			{Name: "folder", Matcher: "glob", Picker: "tree", ListingVisibility: "navigable"},
		},
	}
	return sdk.WithCaller(context.Background(), c)
}

// User scenario: agent restricted to invoices/** lists folders at root.
// The agent must see "invoices" (ancestor stub) but NOT "salaries"
// or "hr". This is the exact case the user asked about.
func TestScope_ListFolders_AtRoot_OnlyAncestorStubs(t *testing.T) {
	ctx := newTestCtx(t)
	mustUpload(t, ctx, "x", "/invoices/q3/", "x")
	mustUpload(t, ctx, "x", "/salaries/2026/", "x")
	mustUpload(t, ctx, "x", "/hr/onboarding/", "x")

	app := &App{}
	callCtx := withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**",
	})
	out, err := app.toolListFoldersCtx(callCtx, ctx, map[string]any{"parent": "/"})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["folders"].([]string)
	if len(got) != 1 || got[0] != "invoices" {
		t.Fatalf("root listing = %v, want only [invoices]", got)
	}
}

// Inside /invoices/, the agent should see all children (q3, q4, etc.)
// because they're entirely within scope.
func TestScope_ListFolders_InsideScope_AllChildren(t *testing.T) {
	ctx := newTestCtx(t)
	mustUpload(t, ctx, "x", "/invoices/q3/", "x")
	mustUpload(t, ctx, "x", "/invoices/q4/", "x")
	mustUpload(t, ctx, "x", "/invoices/q1/", "x")

	app := &App{}
	callCtx := withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**",
	})
	out, _ := app.toolListFoldersCtx(callCtx, ctx, map[string]any{"parent": "/invoices/"})
	got := out.(map[string]any)["folders"].([]string)
	if len(got) != 3 {
		t.Fatalf("inside-scope listing = %v, want 3 children", got)
	}
}

// Narrower scope: invoices/q3/** only. At /invoices/, only q3 visible.
func TestScope_ListFolders_NarrowScope_OnlyAllowedChild(t *testing.T) {
	ctx := newTestCtx(t)
	mustUpload(t, ctx, "x", "/invoices/q3/", "x")
	mustUpload(t, ctx, "x", "/invoices/q4/", "x")
	mustUpload(t, ctx, "x", "/invoices/q1/", "x")

	app := &App{}
	callCtx := withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.read", Resource: "folder/invoices/q3/**",
	})
	out, _ := app.toolListFoldersCtx(callCtx, ctx, map[string]any{"parent": "/invoices/"})
	got := out.(map[string]any)["folders"].([]string)
	if len(got) != 1 || got[0] != "q3" {
		t.Fatalf("narrow scope listing = %v, want only [q3]", got)
	}
}

// files_list filters by scope on every recursive descent.
func TestScope_FilesList_Recursive_FiltersToScope(t *testing.T) {
	ctx := newTestCtx(t)
	mustUpload(t, ctx, "a", "/invoices/q3/", "x")
	mustUpload(t, ctx, "b", "/salaries/", "x")
	mustUpload(t, ctx, "c", "/invoices/q4/", "x")

	app := &App{}
	callCtx := withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**",
	})
	out, _ := app.toolListCtx(callCtx, ctx, map[string]any{"folder": "/", "recursive": true})
	r := out.(map[string]any)
	files := r["files"].([]*File)
	if len(files) != 2 {
		t.Fatalf("recursive scoped count = %d, want 2 (only invoices/*)", len(files))
	}
	for _, f := range files {
		if f.Folder == "/salaries/" {
			t.Errorf("leak: file at %s in scoped result", f.Folder)
		}
	}
}

func TestScope_SearchAppliesLimitAfterPermissionFiltering(t *testing.T) {
	ctx := newTestCtx(t)
	unauthorized1 := mustUpload(t, ctx, "secret-1", "/salaries/", "x")
	unauthorized2 := mustUpload(t, ctx, "secret-2", "/salaries/", "x")
	authorized1 := mustUpload(t, ctx, "invoice-1", "/invoices/", "x")
	authorized2 := mustUpload(t, ctx, "invoice-2", "/invoices/", "x")
	// Make unauthorized rows the first rows the SQL ordering encounters.
	_, _ = ctx.AppDB().Exec(`UPDATE files SET updated_at='2030-01-01 00:00:00' WHERE id IN (?, ?)`, unauthorized1.ID, unauthorized2.ID)
	_, _ = ctx.AppDB().Exec(`UPDATE files SET updated_at='2020-01-01 00:00:00' WHERE id IN (?, ?)`, authorized1.ID, authorized2.ID)

	callCtx := withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**",
	})
	out, err := (&App{}).toolSearchCtx(callCtx, ctx, map[string]any{"limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	files := out.(map[string]any)["files"].([]*File)
	if len(files) != 2 {
		t.Fatalf("authorized page length = %d, want 2: %+v", len(files), files)
	}
	for _, f := range files {
		if f.Folder != "/invoices/" {
			t.Fatalf("unauthorized row leaked: %+v", f)
		}
	}
}

// files_get on an out-of-scope file returns Forbidden — this is the
// confused-deputy guard for id-based reads.
func TestScope_FilesGet_OutOfScope_Forbidden(t *testing.T) {
	ctx := newTestCtx(t)
	out_of_scope := mustUpload(t, ctx, "secret.txt", "/salaries/", "S")

	app := &App{}
	callCtx := withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**",
	})
	_, err := app.toolGetCtx(callCtx, ctx, map[string]any{"id": out_of_scope.ID})
	if err == nil {
		t.Fatal("expected forbidden error")
	}
	if !sdk.IsForbidden(err) {
		t.Fatalf("err = %v, want IsForbidden", err)
	}
}

// In-scope id-read works as expected.
func TestScope_FilesGet_InScope_Allowed(t *testing.T) {
	ctx := newTestCtx(t)
	in_scope := mustUpload(t, ctx, "r.pdf", "/invoices/q3/", "R")

	app := &App{}
	callCtx := withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**",
	})
	got, err := app.toolGetCtx(callCtx, ctx, map[string]any{"id": in_scope.ID})
	if err != nil {
		t.Fatal(err)
	}
	result := got.(map[string]any)
	if result["found"] != true {
		t.Fatalf("found=%v, want true", result["found"])
	}
	if result["file"].(*File).ID != in_scope.ID {
		t.Fatal("returned wrong file")
	}
}

func TestScope_FilesGet_MissingStableContract(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	contexts := []struct {
		name string
		ctx  context.Context
	}{
		{name: "no caller", ctx: context.Background()},
		{name: "restricted caller", ctx: withCaller()},
	}
	for _, tc := range contexts {
		t.Run(tc.name, func(t *testing.T) {
			got, err := app.toolGetCtx(tc.ctx, ctx, map[string]any{"id": int64(999999)})
			if err != nil {
				t.Fatalf("toolGetCtx: %v", err)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(raw) != `{"file":null,"found":false}` {
				t.Fatalf("missing-file JSON=%s, want stable result object", raw)
			}
		})
	}
}

func TestScope_FilesGet_SoftDeletedStableContract(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "gone.txt", "/invoices/", "gone")
	if err := dbSoftDelete(ctx.AppDB(), "test-proj", f.ID); err != nil {
		t.Fatal(err)
	}
	got, err := (&App{}).toolGetCtx(withCaller(), ctx, map[string]any{"id": f.ID})
	if err != nil {
		t.Fatalf("soft-deleted get: %v", err)
	}
	result := got.(map[string]any)
	if result["found"] != false || result["file"] != nil {
		t.Fatalf("soft-deleted result=%#v", result)
	}
}

func TestScope_MissingIDToolsNeverReturnNullOrPanic(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	callCtx := withCaller()
	tests := []struct {
		name string
		call func() (any, error)
	}{
		{"get_url", func() (any, error) {
			return app.toolGetURLCtx(callCtx, ctx, map[string]any{"id": int64(999999)})
		}},
		{"get_content", func() (any, error) {
			return app.toolGetContentCtx(callCtx, ctx, map[string]any{"id": int64(999999)})
		}},
		{"set_tags", func() (any, error) {
			return app.toolSetTagsCtx(callCtx, ctx, map[string]any{"id": int64(999999), "tags": []any{"x"}})
		}},
		{"set_visibility", func() (any, error) {
			return app.toolSetVisibilityCtx(callCtx, ctx, map[string]any{"id": int64(999999), "visibility": "private"})
		}},
		{"move", func() (any, error) {
			return app.toolMoveCtx(callCtx, ctx, map[string]any{"id": int64(999999), "name": "x.txt"})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.call()
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("out=%#v err=%v, want not-found error", out, err)
			}
			if out != nil {
				t.Fatalf("out=%#v, want nil on error", out)
			}
		})
	}
}

func TestScope_DeleteMissingRemainsIdempotent(t *testing.T) {
	ctx := newTestCtx(t)
	out, err := (&App{}).toolDeleteCtx(
		withCaller(), ctx, map[string]any{"id": int64(999999)},
	)
	if err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	result := out.(map[string]any)
	if result["deleted"] != true || result["hard"] != false {
		t.Fatalf("delete missing result=%#v", result)
	}
}

func TestScope_MoveChecksSourceAndDestination(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	t.Run("source denied", func(t *testing.T) {
		f := mustUpload(t, ctx, "source-denied.txt", "/source/", "x")
		callCtx := withCaller(sdk.Grant{
			Effect: "allow", Permission: "files.write", Resource: "folder/destination/**",
		})
		_, err := app.toolMoveCtx(callCtx, ctx, map[string]any{
			"id": f.ID, "folder": "/destination/",
		})
		if err == nil || !sdk.IsForbidden(err) {
			t.Fatalf("err=%v, want source Forbidden", err)
		}
	})

	t.Run("destination denied", func(t *testing.T) {
		f := mustUpload(t, ctx, "destination-denied.txt", "/source/", "x")
		callCtx := withCaller(sdk.Grant{
			Effect: "allow", Permission: "files.write", Resource: "folder/source/**",
		})
		_, err := app.toolMoveCtx(callCtx, ctx, map[string]any{
			"id": f.ID, "folder": "/destination/",
		})
		if err == nil || !sdk.IsForbidden(err) {
			t.Fatalf("err=%v, want destination Forbidden", err)
		}
	})

	t.Run("rename in place needs no root grant", func(t *testing.T) {
		f := mustUpload(t, ctx, "before.txt", "/source/", "x")
		callCtx := withCaller(sdk.Grant{
			Effect: "allow", Permission: "files.write", Resource: "folder/source/**",
		})
		out, err := app.toolMoveCtx(callCtx, ctx, map[string]any{
			"id": f.ID, "name": "after.txt",
		})
		if err != nil {
			t.Fatalf("rename in place: %v", err)
		}
		if out.(map[string]any)["file"].(*File).Name != "after.txt" {
			t.Fatalf("rename result=%#v", out)
		}
	})

	t.Run("both allowed", func(t *testing.T) {
		f := mustUpload(t, ctx, "allowed.txt", "/source/", "x")
		callCtx := withCaller(
			sdk.Grant{Effect: "allow", Permission: "files.write", Resource: "folder/source/**"},
			sdk.Grant{Effect: "allow", Permission: "files.write", Resource: "folder/destination/**"},
		)
		out, err := app.toolMoveCtx(callCtx, ctx, map[string]any{
			"id": f.ID, "folder": "/destination/",
		})
		if err != nil {
			t.Fatalf("move: %v", err)
		}
		if out.(map[string]any)["file"].(*File).Folder != "/destination/" {
			t.Fatalf("move result=%#v", out)
		}
	})
}

func TestScope_DedupeDoesNotLeakUnauthorizedFile(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "secret.txt", "/salaries/", "secret")
	app := &App{}

	denied, err := app.toolDedupeCtx(withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**",
	}), ctx, map[string]any{"sha256": f.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	if denied.(map[string]any)["found"] != false {
		t.Fatalf("unauthorized dedupe leaked file: %#v", denied)
	}

	allowed, err := app.toolDedupeCtx(withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.read", Resource: "folder/salaries/**",
	}), ctx, map[string]any{"sha256": f.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	if allowed.(map[string]any)["found"] != true ||
		allowed.(map[string]any)["file"].(*File).ID != f.ID {
		t.Fatalf("authorized dedupe result=%#v", allowed)
	}
}

func TestScope_AbortUploadRequiresDestinationWriteAccess(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	initOut, err := app.toolUploadInitCtx(context.Background(), ctx, map[string]any{
		"name":       "large.bin",
		"size_bytes": int64(10),
		"folder":     "/salaries/",
	})
	if err != nil {
		t.Fatalf("init upload: %v", err)
	}
	id := initOut.(map[string]any)["upload_id"].(string)

	deniedCtx := withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.write", Resource: "folder/invoices/**",
	})
	if _, err := app.toolAbortUploadCtx(deniedCtx, ctx, map[string]any{"id": id}); err == nil || !sdk.IsForbidden(err) {
		t.Fatalf("abort outside scope err=%v, want Forbidden", err)
	}
	// Denied abort must leave the session intact.
	if _, err := app.toolUploadStatusCtx(context.Background(), ctx, map[string]any{"upload_id": id}); err != nil {
		t.Fatalf("denied abort removed session: %v", err)
	}

	allowedCtx := withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.write", Resource: "folder/salaries/**",
	})
	out, err := app.toolAbortUploadCtx(allowedCtx, ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatalf("allowed abort: %v", err)
	}
	if out.(map[string]any)["found"] != true {
		t.Fatalf("allowed abort result=%#v", out)
	}

	// Repeating the same authorized operation preserves the documented
	// idempotent found=false contract.
	out, err = app.toolAbortUploadCtx(allowedCtx, ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatalf("repeat abort: %v", err)
	}
	if out.(map[string]any)["found"] != false {
		t.Fatalf("repeat abort result=%#v", out)
	}
}

func TestScope_UploadSessionToolsEnforceGlobalProject(t *testing.T) {
	ctx := newTestCtx(t)
	t.Setenv("APTEVA_PROJECT_ID", "")
	app := &App{}
	initOut, err := app.toolUploadInitCtx(context.Background(), ctx, map[string]any{
		"_project_id": "project-a",
		"name":        "global.bin",
		"size_bytes":  int64(10),
		"folder":      "/private/",
	})
	if err != nil {
		t.Fatalf("init upload: %v", err)
	}
	id := initOut.(map[string]any)["upload_id"].(string)

	if _, err := app.toolUploadStatusCtx(context.Background(), ctx, map[string]any{
		"_project_id": "project-b", "upload_id": id,
	}); err == nil || !strings.Contains(err.Error(), "different project") {
		t.Fatalf("cross-project status err=%v", err)
	}
	if _, err := app.toolAbortUploadCtx(context.Background(), ctx, map[string]any{
		"_project_id": "project-b", "id": id,
	}); err == nil || !strings.Contains(err.Error(), "different project") {
		t.Fatalf("cross-project abort err=%v", err)
	}

	// The owning project can still see and abort the session after the
	// rejected cross-project calls.
	if _, err := app.toolUploadStatusCtx(context.Background(), ctx, map[string]any{
		"_project_id": "project-a", "upload_id": id,
	}); err != nil {
		t.Fatalf("owner status: %v", err)
	}
	if _, err := app.toolAbortUploadCtx(context.Background(), ctx, map[string]any{
		"_project_id": "project-a", "id": id,
	}); err != nil {
		t.Fatalf("owner abort: %v", err)
	}
}

// Read-only scope: agent can read but can't delete.
func TestScope_FilesDelete_NoWriteGrant_Forbidden(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "r.pdf", "/invoices/q3/", "R")

	app := &App{}
	// Read-only on invoices.
	callCtx := withCaller(sdk.Grant{
		Effect: "allow", Permission: "files.read", Resource: "folder/invoices/**",
	})
	_, err := app.toolDeleteCtx(callCtx, ctx, map[string]any{"id": f.ID})
	if err == nil || !sdk.IsForbidden(err) {
		t.Fatalf("expected forbidden, got %v", err)
	}
}

// Back-compat: nil caller (no header forwarded) sees everything,
// just like before the permissions feature shipped.
func TestScope_NilCaller_SeesEverything(t *testing.T) {
	ctx := newTestCtx(t)
	mustUpload(t, ctx, "a", "/invoices/", "x")
	mustUpload(t, ctx, "b", "/salaries/", "x")
	mustUpload(t, ctx, "c", "/hr/", "x")

	app := &App{}
	// Bare context — no caller stashed.
	out, _ := app.toolListFoldersCtx(context.Background(), ctx, map[string]any{"parent": "/"})
	got := out.(map[string]any)["folders"].([]string)
	if len(got) != 3 {
		t.Fatalf("nil caller listing = %v, want 3 (full access)", got)
	}
}

// Back-compat: caller with default_effect=allow + zero rules also
// sees everything — this is what an upgraded server returns for an
// install that hasn't migrated to the permissions feature.
func TestScope_DefaultAllowEmptyGrants_SeesEverything(t *testing.T) {
	ctx := newTestCtx(t)
	mustUpload(t, ctx, "a", "/invoices/", "x")
	mustUpload(t, ctx, "b", "/salaries/", "x")

	app := &App{}
	c := &sdk.Caller{
		AgentID:       7,
		DefaultEffect: "allow",
		// no grants
		Resources: []sdk.ResourceDecl{
			{Name: "folder", Matcher: "glob", Picker: "tree", ListingVisibility: "navigable"},
		},
	}
	callCtx := sdk.WithCaller(context.Background(), c)
	out, _ := app.toolListFoldersCtx(callCtx, ctx, map[string]any{"parent": "/"})
	got := out.(map[string]any)["folders"].([]string)
	if len(got) != 2 {
		t.Fatalf("default-allow caller listing = %v, want 2 (full access)", got)
	}
}
