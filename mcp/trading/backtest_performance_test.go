package main

import (
	"math"
	"testing"
)

func TestBacktestSnapshots_UpsertAndMetrics(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "BT", []string{"crypto"})
	runID, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{
		ProjectID:     "test-proj",
		PortfolioID:   pid,
		SourceAgentID: 7,
		Name:          "BT replay",
		Status:        "running",
		Symbols:       []string{"BTC-USD"},
		StartAt:       "2026-01-01",
		EndAt:         "2026-01-03",
		Interval:      "1d",
		StartingCash:  100000,
		TotalSteps:    3,
		Summary:       map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", runID)
	if err != nil {
		t.Fatal(err)
	}
	run.CurrentStep = 2

	for _, snap := range []*BacktestSnapshot{
		{RunID: runID, Step: 1, Equity: 102000, Cash: 80000, BuyingPower: 80000, Exposure: 21.5},
		{RunID: runID, Step: 2, Equity: 99000, Cash: 79000, BuyingPower: 79000, OpenPnL: -1000, Exposure: 20},
	} {
		if err := dbUpsertBacktestSnapshot(ctx.AppDB(), snap); err != nil {
			t.Fatal(err)
		}
	}
	// Upsert should replace the existing step rather than add a duplicate.
	if err := dbUpsertBacktestSnapshot(ctx.AppDB(), &BacktestSnapshot{
		RunID: runID, Step: 2, Equity: 99500, Cash: 79500, BuyingPower: 79500, OpenPnL: -500, Exposure: 19,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := dbListBacktestSnapshots(ctx.AppDB(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("snapshots=%d, want 2", len(rows))
	}
	if rows[1].Equity != 99500 {
		t.Fatalf("step 2 equity=%v, want replacement 99500", rows[1].Equity)
	}
	series := backtestSeriesWithBaseline(run, rows)
	if len(series) != 3 || series[0].Step != 0 || series[0].Equity != 100000 {
		t.Fatalf("series baseline wrong: %#v", series)
	}
	metrics := backtestPerformanceMetrics(run, series, rows[1])
	if metrics["total_pnl"] != -500 {
		t.Fatalf("total_pnl=%v, want -500", metrics["total_pnl"])
	}
	if math.Abs(metrics["return_pct"] - -0.5) > 0.0001 {
		t.Fatalf("return_pct=%v, want -0.5", metrics["return_pct"])
	}
	if metrics["max_drawdown_pct"] >= -2.45 || metrics["max_drawdown_pct"] <= -2.46 {
		t.Fatalf("max_drawdown_pct=%v, want about -2.45", metrics["max_drawdown_pct"])
	}
}

func TestBacktestPerformance_RepricesPositionsFromReplayMarks(t *testing.T) {
	positions := []*Position{{
		Symbol:      "BTC-USD",
		AssetClass:  "crypto",
		Qty:         0.3,
		AvgCost:     69319.25,
		MarketPrice: 70000,
		MarketValue: 21000,
	}}
	prices := []map[string]any{{"symbol": "BTC-USD", "price": 69263.7251, "asset_class": "crypto"}}

	equity, openPnL, openPnLPct, realized, exposure := valueBacktestPositions(79220.88247, positions, prices)

	wantMarketValue := 0.3 * 69263.7251
	if math.Abs(positions[0].MarketPrice-69263.7251) > 0.0001 {
		t.Fatalf("market price=%v, want replay price", positions[0].MarketPrice)
	}
	if math.Abs(positions[0].MarketValue-wantMarketValue) > 0.0001 {
		t.Fatalf("market value=%v, want %v", positions[0].MarketValue, wantMarketValue)
	}
	if math.Abs(openPnL-(-16.65747)) > 0.0001 {
		t.Fatalf("open pnl=%v, want replay-priced pnl", openPnL)
	}
	if math.Abs(equity-100000) > 0.0001 {
		t.Fatalf("equity=%v, want 100000", equity)
	}
	if openPnLPct >= 0 {
		t.Fatalf("open pnl pct=%v, want negative", openPnLPct)
	}
	if realized != 0 {
		t.Fatalf("realized=%v, want 0", realized)
	}
	if exposure <= 0 {
		t.Fatalf("exposure=%v, want positive", exposure)
	}
}
