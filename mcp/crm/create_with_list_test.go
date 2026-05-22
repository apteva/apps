package main

// Tests for adding a contact to a list directly from create / upsert
// (v0.8.1): list_ids / list_id by numeric id or slug.

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func mkList(t *testing.T, ctx *sdk.AppCtx, name, slug string) int64 {
	t.Helper()
	app := &App{}
	out, err := app.toolListsCreate(ctx, map[string]any{"name": name, "slug": slug})
	if err != nil {
		t.Fatalf("create list %s: %v", name, err)
	}
	return out.(map[string]any)["list"].(*List).ID
}

func contactInList(t *testing.T, ctx *sdk.AppCtx, contactID, listID int64) bool {
	t.Helper()
	var n int
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM contact_list_members WHERE contact_id = ? AND list_id = ?`,
		contactID, listID).Scan(&n)
	return n > 0
}

func TestCreate_AddsToListsByIDAndSlug(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	l1 := mkList(t, ctx, "VIPs", "vips")
	l2 := mkList(t, ctx, "Newsletter", "newsletter")

	// Create with one list by id and one by slug.
	out, err := app.toolCreate(ctx, map[string]any{
		"display_name": "Dana",
		"list_ids":     []any{float64(l1), "newsletter"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := out.(map[string]any)["contact"].(*Contact)
	added, _ := out.(map[string]any)["lists_added"].([]int64)
	if len(added) != 2 {
		t.Fatalf("expected 2 lists added, got %v", added)
	}
	if !contactInList(t, ctx, c.ID, l1) || !contactInList(t, ctx, c.ID, l2) {
		t.Errorf("contact should be in both lists")
	}
}

func TestCreate_UnknownListRefIsSkipped(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	l1 := mkList(t, ctx, "Real", "real")

	out, err := app.toolCreate(ctx, map[string]any{
		"display_name": "Sam",
		"list_ids":     []any{float64(l1), float64(999999), "no-such-slug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	added, _ := out.(map[string]any)["lists_added"].([]int64)
	if len(added) != 1 || added[0] != l1 {
		t.Errorf("only the real list should be applied, got %v", added)
	}
}

func TestUpsertByChannel_EnsuresListMembership(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	l := mkList(t, ctx, "Leads", "leads")

	// First upsert creates + adds.
	out1, err := app.toolUpsertByChannel(ctx, map[string]any{
		"kind": "email", "value": "lead@x.com", "list_id": "leads",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := out1.(map[string]any)["contact"].(*Contact)
	if out1.(map[string]any)["was_created"] != true {
		t.Fatalf("first upsert should create")
	}
	if !contactInList(t, ctx, c.ID, l) {
		t.Errorf("created contact should be in leads")
	}

	// Remove, then upsert again (found path) — membership re-ensured.
	if err := dbListRemoveContact(ctx.AppDB(), "test-proj", l, c.ID); err != nil {
		t.Fatal(err)
	}
	out2, _ := app.toolUpsertByChannel(ctx, map[string]any{
		"kind": "email", "value": "lead@x.com", "list_id": float64(l),
	})
	if out2.(map[string]any)["was_created"] != false {
		t.Fatalf("second upsert should find, not create")
	}
	if !contactInList(t, ctx, c.ID, l) {
		t.Errorf("found contact should be re-added to leads")
	}
}
