package main

// End-to-end scope tests through the storage MCP tools — exercises
// the user's exact scenario: an agent scoped to invoices/** lists
// folders/files and only sees what the policy allows. Plus the
// back-compat case (nil caller passes through unchanged).

import (
	"context"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// withCaller stamps a Caller onto a ctx for the *Ctx tool variants.
func withCaller(grants ...sdk.Grant) context.Context {
	c := &sdk.Caller{
		AgentID:    7,
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
	if got.(*File).ID != in_scope.ID {
		t.Fatal("returned wrong file")
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
		AgentID:    7,
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
