package main

// Tests for custom-field (attribute) filtering in contacts_search (v0.8.2).

import (
	"fmt"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestSearch_PaginationOffsetAndTotal(t *testing.T) {
	ctx := newTestCtx(t)
	for i := 0; i < 3; i++ {
		mustCreate(t, ctx, map[string]any{"display_name": fmt.Sprintf("Pager %d", i)})
	}
	db := ctx.AppDB()

	total, err := dbSearchCount(db, "test-proj", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("total=%d, want 3", total)
	}

	page1, err := dbSearch(db, "test-proj", "", nil, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	page2, err := dbSearch(db, "test-proj", "", nil, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || len(page2) != 1 {
		t.Fatalf("page sizes: page1=%d page2=%d, want 2 and 1", len(page1), len(page2))
	}
	seen := map[int64]bool{}
	for _, c := range append(page1, page2...) {
		if seen[c.ID] {
			t.Fatalf("contact %d appeared on two pages", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestSearch_FilterByCustomAttribute(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	// Define a custom field + two contacts with different values.
	if _, err := app.toolDefineAttribute(ctx, map[string]any{
		"key": "region", "label": "Région", "type": "text",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolDefineAttribute(ctx, map[string]any{
		"key": "share_capital", "label": "Capital", "type": "number",
	}); err != nil {
		t.Fatal(err)
	}

	a := mustCreate(t, ctx, map[string]any{"display_name": "PACA Co"})
	b := mustCreate(t, ctx, map[string]any{"display_name": "Paris Co"})
	setAttr(t, ctx, a.ID, "region", "PACA")
	setAttr(t, ctx, a.ID, "share_capital", float64(82000))
	setAttr(t, ctx, b.ID, "region", "Île-de-France")
	setAttr(t, ctx, b.ID, "share_capital", float64(5000))

	// eq on a text attribute.
	got := searchIDs(t, ctx, app, map[string]any{
		"filters": []any{map[string]any{"attribute": "region", "op": "eq", "value": "PACA"}},
	})
	if len(got) != 1 || !got[a.ID] {
		t.Fatalf("region=PACA should return only A, got %v", got)
	}

	// gte on a numeric attribute.
	got = searchIDs(t, ctx, app, map[string]any{
		"filters": []any{map[string]any{"attribute": "share_capital", "op": "gte", "value": float64(10000)}},
	})
	if len(got) != 1 || !got[a.ID] {
		t.Fatalf("share_capital>=10000 should return only A, got %v", got)
	}

	// Combined free-text + attribute filter (AND).
	got = searchIDs(t, ctx, app, map[string]any{
		"q":       "Co",
		"filters": []any{map[string]any{"attribute": "region", "op": "contains", "value": "Île"}},
	})
	if len(got) != 1 || !got[b.ID] {
		t.Fatalf("q=Co + region contains Île should return only B, got %v", got)
	}

	// Core-field filter still works alongside.
	got = searchIDs(t, ctx, app, map[string]any{
		"filters": []any{map[string]any{"field": "display_name", "op": "eq", "value": "Paris Co"}},
	})
	if len(got) != 1 || !got[b.ID] {
		t.Fatalf("core field filter regressed, got %v", got)
	}
}

func TestSearch_FilterByListMembership(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	list, err := dbListCreate(ctx.AppDB(), "test-proj", &List{Name: "Newsletter"})
	if err != nil {
		t.Fatal(err)
	}
	in := mustCreate(t, ctx, map[string]any{"display_name": "In List"})
	out := mustCreate(t, ctx, map[string]any{"display_name": "Outside List"})
	if err := dbListAddContact(ctx.AppDB(), "test-proj", list.ID, in.ID, "test"); err != nil {
		t.Fatal(err)
	}

	got := searchIDs(t, ctx, app, map[string]any{
		"filters": []any{map[string]any{"predicate": "in_list", "list_id": float64(list.ID)}},
	})
	if len(got) != 1 || !got[in.ID] {
		t.Fatalf("in_list should return only member, got %v", got)
	}

	got = searchIDs(t, ctx, app, map[string]any{
		"filters": []any{map[string]any{"predicate": "not_in_list", "list_id": float64(list.ID)}},
	})
	if len(got) != 1 || !got[out.ID] {
		t.Fatalf("not_in_list should return only non-member, got %v", got)
	}
}

func setAttr(t *testing.T, ctx *sdk.AppCtx, contactID int64, key string, value any) {
	t.Helper()
	if err := dbSetAttribute(ctx.AppDB(), "test-proj", contactID, key, value, "test"); err != nil {
		t.Fatalf("set attr %s: %v", key, err)
	}
}

func searchIDs(t *testing.T, ctx *sdk.AppCtx, app *App, args map[string]any) map[int64]bool {
	t.Helper()
	out, err := app.toolSearch(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	set := map[int64]bool{}
	for _, c := range out.(map[string]any)["contacts"].([]*Contact) {
		set[c.ID] = true
	}
	return set
}
