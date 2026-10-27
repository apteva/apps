package main

import (
	"errors"
	tk "github.com/apteva/app-sdk/testkit"
	"testing"
)

type pagedLegacyFixture struct {
	legacyTradingFixture
	failOnce bool
}

func (p *pagedLegacyFixture) CallAppResult(app, tool string, args map[string]any, out any) error {
	if tool != "research_legacy_export" {
		return p.legacyTradingFixture.CallAppResult(app, tool, args, out)
	}
	page := researchSnapshot{Version: 2, SourceInstallID: 11, ProjectID: "test-proj", Tables: map[string][]map[string]any{}}
	if strArg(args, "phase") == "manifest" {
		page.TableNames = legacyImportTables
		page.RowCounts = map[string]int64{}
		for _, table := range legacyImportTables {
			page.RowCounts[table] = int64(len(p.legacy.Tables[table]))
		}
		return decodeResearchValue(page, out)
	}
	table := strArg(args, "table")
	cursor := int64Arg(args, "after_row_id", 0)
	if p.failOnce && table == "strategies" && cursor == 1 {
		p.failOnce = false
		return errors.New("transient transport failure")
	}
	rows := p.legacy.Tables[table]
	next := cursor
	if cursor < int64(len(rows)) {
		next++
		page.Tables[table] = rows[cursor:next]
	} else {
		page.Tables[table] = []map[string]any{}
	}
	page.NextRowID = next
	page.Done = next == int64(len(rows))
	return decodeResearchValue(page, out)
}

func TestPagedLegacyImportResumesAndChecksCounts(t *testing.T) {
	p := &pagedLegacyFixture{failOnce: true}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(p))
	globalCtx = ctx
	p.ctx = ctx
	p.legacy.Tables = map[string][]map[string]any{"strategies": {{"id": int64(41), "project_id": "test-proj", "name": "one", "definition_json": "{}", "status": "active"}, {"id": int64(42), "project_id": "test-proj", "name": "two", "definition_json": "{}", "status": "draft"}, {"id": int64(43), "project_id": "test-proj", "name": "three", "definition_json": "{}", "status": "archived"}}}
	args := map[string]any{"paged": true, "max_pages": 1}
	first, err := (&App{}).importLegacyResearch(ctx, args)
	if err != nil || first.(map[string]any)["status"] != "importing" {
		t.Fatalf("start: %v %v", first, err)
	}
	if _, err = (&App{}).importLegacyResearch(ctx, args); err == nil {
		t.Fatal("transport failure not surfaced")
	}
	var cursor int64
	if err = ctx.AppDB().QueryRow("SELECT row_cursor FROM legacy_import_progress").Scan(&cursor); err != nil || cursor != 1 {
		t.Fatalf("checkpoint advanced on failure: %d %v", cursor, err)
	}
	args["max_pages"] = 32
	result, err := (&App{}).importLegacyResearch(ctx, args)
	if err != nil || result.(map[string]any)["status"] != "imported" {
		t.Fatalf("resume: %v %v", result, err)
	}
	var count int
	ctx.AppDB().QueryRow("SELECT COUNT(*) FROM strategies").Scan(&count)
	if count != 3 {
		t.Fatalf("lost/duplicated rows: %d", count)
	}
	result, err = (&App{}).importLegacyResearch(ctx, args)
	if err != nil || result.(map[string]any)["status"] != "already_imported" {
		t.Fatalf("retry: %v %v", result, err)
	}
}
