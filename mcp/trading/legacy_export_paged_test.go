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
	out, err := exportLegacyResearchSnapshot(ctx, map[string]any{"phase": "manifest"})
	if err != nil {
		t.Fatal(err)
	}
	manifest := out
	if manifest.Version != 2 || manifest.RowCounts["strategies"] != 3 {
		t.Fatalf("manifest=%#v", manifest)
	}
	args := map[string]any{"phase": "table", "table": "strategies", "limit": 2000}
	out, err = exportLegacyResearchSnapshot(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	page := out
	if len(page.Tables["strategies"]) != 2 || page.Done {
		t.Fatalf("byte budget not enforced: %#v", page)
	}
	firstCursor := page.NextRowID
	args["after_row_id"] = firstCursor
	out, err = exportLegacyResearchSnapshot(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	page = out
	if len(page.Tables["strategies"]) != 1 || !page.Done || page.NextRowID <= firstCursor {
		t.Fatal("keyset page lost rows or scope")
	}
	args["table"] = "portfolios; DROP TABLE orders"
	if _, err = exportLegacyResearchSnapshot(ctx, args); err == nil {
		t.Fatal("unlisted table accepted")
	}
}

func TestLegacyExportIncludesSecurityReferencedOnlyByHistoricalBars(t *testing.T) {
	ctx := newTestCtx(t)
	portfolio := mustCreatePortfolio(t, ctx, "Legacy book", []string{"equity"})
	strategy := mustCreateFixedStrategy(t, ctx, "Legacy strategy", "OLD", .1)
	run := mustCreateStrategyBacktestRun(t, ctx, portfolio, strategy, "Archived run")
	if _, err := ctx.AppDB().Exec(`INSERT INTO securities(id,asset_class,source) VALUES ('historical','equity','fixture'),('unrelated','equity','fixture')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO backtest_market_bars(run_id,step,symbol,security_id,asset_class,t,o,h,l,c,source)
		VALUES (?,0,'OLD','historical','equity',1,1,1,1,1,'fixture')`, run.ID); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{}, {"phase": "table", "table": "backtest_market_bars"}} {
		page, err := exportLegacyResearchSnapshot(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.LegacySecurities) != 1 || page.LegacySecurities[0]["id"] != "historical" {
			t.Fatalf("legacy export did not include exactly the referenced security: %+v", page.LegacySecurities)
		}
	}
}
