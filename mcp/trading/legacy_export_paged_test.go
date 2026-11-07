package main

import (
	"strings"
	"testing"
)

func TestLegacyExportPagesAreBoundedScopedAndStable(t *testing.T) {
	ctx := newTestCtx(t)
	for i := 0; i < 3; i++ {
		if _, err := dbCreateStrategy(ctx.AppDB(), &Strategy{ProjectID: "test-proj", Name: strings.Repeat("x", 1600000), Definition: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dbCreateStrategy(ctx.AppDB(), &Strategy{ProjectID: "other-project", Name: "Do not export", Definition: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	out, err := exportResearchSnapshot(ctx, map[string]any{"phase": "manifest"}, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest := out
	if manifest.Version != 2 || manifest.RowCounts["strategies"] != 3 {
		t.Fatalf("manifest=%#v", manifest)
	}
	args := map[string]any{"phase": "table", "table": "strategies", "limit": 2000}
	out, err = exportResearchSnapshot(ctx, args, true)
	if err != nil {
		t.Fatal(err)
	}
	page := out
	if len(page.Tables["strategies"]) != 2 || page.Done {
		t.Fatalf("byte budget not enforced: %#v", page)
	}
	firstCursor := page.NextRowID
	args["after_row_id"] = firstCursor
	out, err = exportResearchSnapshot(ctx, args, true)
	if err != nil {
		t.Fatal(err)
	}
	page = out
	if len(page.Tables["strategies"]) != 1 || !page.Done || page.NextRowID <= firstCursor {
		t.Fatal("keyset page lost rows or scope")
	}
	args["table"] = "portfolios; DROP TABLE orders"
	if _, err = exportResearchSnapshot(ctx, args, true); err == nil {
		t.Fatal("unlisted table accepted")
	}
}
