package main

import (
	"math"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func testStrategyDefinition() map[string]any {
	return map[string]any{
		"universe": []any{"BTC-USD", "ETH-USD"},
		"cadence":  "1d",
		"rules": []any{
			map[string]any{
				"name": "risk on",
				"rank": map[string]any{
					"symbols": []any{"BTC-USD", "ETH-USD"},
					"by":      "return_1",
					"top":     float64(1),
					"weight":  "equal_weight",
				},
			},
		},
		"risk": map[string]any{"max_position_weight": 0.7},
	}
}

func TestStrategyValidateAndEvaluate(t *testing.T) {
	ctx := newTestCtx(t)
	id, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
		ProjectID:  "test-proj",
		Name:       "Crypto momentum",
		Status:     "active",
		Definition: testStrategyDefinition(),
		Version:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := dbGetStrategy(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	def, warnings, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v, want none", warnings)
	}
	eval, err := evaluateStrategy(strategy, liveStrategyMarket(ctx, def))
	if err != nil {
		t.Fatal(err)
	}
	if len(eval.TargetAllocations) != 1 {
		t.Fatalf("allocations=%#v, want one ranked target", eval.TargetAllocations)
	}
	if eval.TargetAllocations[0].Weight > 0.7 {
		t.Fatalf("weight=%v, want capped at 0.7", eval.TargetAllocations[0].Weight)
	}
	if len(eval.Decisions) == 0 {
		t.Fatal("missing decision explanation")
	}
}

func TestStrategyBacktestUsesExistingSnapshots(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Strategy BT", []string{"crypto"})
	strategyID, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
		ProjectID: "test-proj",
		Name:      "BTC fixed",
		Status:    "active",
		Definition: map[string]any{
			"universe": []any{"BTC-USD"},
			"rules": []any{
				map[string]any{
					"name":     "always long",
					"allocate": []any{map[string]any{"symbol": "BTC-USD", "weight": 0.5}},
				},
			},
		},
		Version: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{
		ProjectID:       "test-proj",
		PortfolioID:     pid,
		StrategyID:      strategyID,
		RunKind:         "strategy",
		StrategyVersion: 1,
		Name:            "BTC strategy replay",
		Status:          "queued",
		Symbols:         []string{"BTC-USD"},
		StartAt:         "2026-01-01",
		EndAt:           "2026-01-03",
		Interval:        "1d",
		StartingCash:    100000,
		SlippageBps:     5,
		TotalSteps:      3,
		Summary:         map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", runID)
	if err != nil {
		t.Fatal(err)
	}
	seedBacktestMarketBars(t, ctx, run.ID, run.Symbols, run.TotalSteps)
	if _, err := startStrategyBacktestRun(run); err != nil {
		t.Fatal(err)
	}
	next, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", runID)
	if err != nil {
		t.Fatal(err)
	}
	if next.RunKind != "strategy" || next.CurrentStep != 1 {
		t.Fatalf("run kind/step = %s/%d, want strategy/1", next.RunKind, next.CurrentStep)
	}
	snaps, err := dbListBacktestSnapshots(ctx.AppDB(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("snapshots=%d, want baseline + step", len(snaps))
	}
	if len(snaps[1].Positions) != 1 {
		t.Fatalf("positions=%#v, want one BTC position", snaps[1].Positions)
	}
	events, err := dbListBacktestEvents(ctx.AppDB(), runID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected strategy backtest events")
	}
}

func TestStrategyBacktestRunMatchesManualSteps(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Strategy BT Compare", []string{"crypto"})
	strategyID, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
		ProjectID: "test-proj",
		Name:      "Crypto momentum compare",
		Status:    "active",
		Definition: map[string]any{
			"universe": []any{"BTC-USD", "ETH-USD", "SOL-USD"},
			"rules": []any{
				map[string]any{
					"name": "BTC trend momentum",
					"when": map[string]any{
						"symbol":    "BTC-USD",
						"indicator": "price",
						"operator":  "above",
						"compare":   "sma_20",
					},
					"rank": map[string]any{
						"symbols": []any{"BTC-USD", "ETH-USD", "SOL-USD"},
						"by":      "return_20",
						"top":     float64(2),
					},
				},
				map[string]any{
					"name": "Defensive crypto basket",
					"allocate": []any{
						map[string]any{"symbol": "BTC-USD", "weight": 0.4},
						map[string]any{"symbol": "ETH-USD", "weight": 0.3},
						map[string]any{"symbol": "SOL-USD", "weight": 0.2},
					},
				},
			},
			"risk": map[string]any{"max_position_weight": 0.5},
		},
		Version: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	slowRun := mustCreateStrategyBacktestRun(t, ctx, pid, strategyID, "manual steps")
	fastRun := mustCreateStrategyBacktestRun(t, ctx, pid, strategyID, "fast run")
	seedBacktestMarketBars(t, ctx, slowRun.ID, slowRun.Symbols, slowRun.TotalSteps)
	seedBacktestMarketBars(t, ctx, fastRun.ID, fastRun.Symbols, fastRun.TotalSteps)

	if _, err := startStrategyBacktestRun(slowRun); err != nil {
		t.Fatal(err)
	}
	for {
		next, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", slowRun.ID)
		if err != nil {
			t.Fatal(err)
		}
		if next.Status != "running" || next.CurrentStep >= next.TotalSteps {
			break
		}
		if _, err := stepStrategyBacktestRun(next); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runStrategyBacktestToEnd(fastRun); err != nil {
		t.Fatal(err)
	}

	slowSnap := mustLastBacktestSnapshot(t, ctx, slowRun.ID)
	fastSnap := mustLastBacktestSnapshot(t, ctx, fastRun.ID)
	assertClose(t, "equity", fastSnap.Equity, slowSnap.Equity)
	assertClose(t, "cash", fastSnap.Cash, slowSnap.Cash)
	assertClose(t, "open pnl", fastSnap.OpenPnL, slowSnap.OpenPnL)
	assertClose(t, "realized pnl", fastSnap.RealizedPnL, slowSnap.RealizedPnL)
	if len(fastSnap.Positions) != len(slowSnap.Positions) {
		t.Fatalf("positions=%d, want %d", len(fastSnap.Positions), len(slowSnap.Positions))
	}
	for i := range slowSnap.Positions {
		got := fastSnap.Positions[i]
		want := slowSnap.Positions[i]
		if got.Symbol != want.Symbol {
			t.Fatalf("position[%d] symbol=%s, want %s", i, got.Symbol, want.Symbol)
		}
		assertClose(t, got.Symbol+" qty", got.Qty, want.Qty)
		assertClose(t, got.Symbol+" avg cost", got.AvgCost, want.AvgCost)
		assertClose(t, got.Symbol+" value", got.MarketValue, want.MarketValue)
	}
	if len(fastSnap.Orders) != len(slowSnap.Orders) {
		t.Fatalf("orders=%d, want %d", len(fastSnap.Orders), len(slowSnap.Orders))
	}
}

func TestStrategyValidationCreatesOutOfSampleRuns(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Strategy Validation", []string{"crypto"})
	pf, err := dbGetPortfolio(ctx.AppDB(), "test-proj", pid)
	if err != nil {
		t.Fatal(err)
	}
	strategyID, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
		ProjectID: "test-proj",
		Name:      "Validation momentum",
		Status:    "active",
		Definition: map[string]any{
			"universe": []any{"BTC-USD", "ETH-USD"},
			"rules": []any{
				map[string]any{
					"name": "top one",
					"rank": map[string]any{
						"symbols": []any{"BTC-USD", "ETH-USD"},
						"by":      "return_3",
						"top":     float64(1),
					},
				},
			},
		},
		Version: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := dbGetStrategy(ctx.AppDB(), "test-proj", strategyID)
	if err != nil {
		t.Fatal(err)
	}
	def, _, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		t.Fatal(err)
	}
	bars := validationFixtureBars(def.Universe, 30)
	app := &App{}
	train, err := app.createCompletedStrategyValidationRun(ctx, pf, strategy, def, strategyValidationRunSpec{
		Label:        "in_sample",
		Name:         "validation train",
		Interval:     "1d",
		StartingCash: 100000,
		FeeBps:       1,
		SlippageBps:  5,
		MarketSource: "fixture",
		Bars:         reindexBacktestMarketBars(bars, 1, 21),
	})
	if err != nil {
		t.Fatal(err)
	}
	test, err := app.createCompletedStrategyValidationRun(ctx, pf, strategy, def, strategyValidationRunSpec{
		Label:        "out_of_sample",
		Name:         "validation test",
		Interval:     "1d",
		StartingCash: 100000,
		FeeBps:       1,
		SlippageBps:  5,
		MarketSource: "fixture",
		Bars:         reindexBacktestMarketBars(bars, 22, 30),
	})
	if err != nil {
		t.Fatal(err)
	}
	if train.Run.Status != "completed" || test.Run.Status != "completed" {
		t.Fatalf("statuses train/test=%s/%s, want completed/completed", train.Run.Status, test.Run.Status)
	}
	if train.Run.ID == test.Run.ID {
		t.Fatal("train and test should be separate backtest runs")
	}
	if train.Metrics["return_pct"] == 0 || test.Metrics["return_pct"] == 0 {
		t.Fatalf("missing validation returns: train=%v test=%v", train.Metrics["return_pct"], test.Metrics["return_pct"])
	}
	if train.Metrics["sharpe_ratio"] == 0 || test.Metrics["sharpe_ratio"] == 0 {
		t.Fatalf("missing validation Sharpe: train=%v test=%v", train.Metrics["sharpe_ratio"], test.Metrics["sharpe_ratio"])
	}
	if verdict := strategyValidationVerdict(train.Metrics, test.Metrics); verdict == "" {
		t.Fatal("missing validation verdict")
	}
}

func mustCreateStrategyBacktestRun(t *testing.T, ctx *sdk.AppCtx, portfolioID, strategyID int64, name string) *BacktestRun {
	t.Helper()
	id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{
		ProjectID:       "test-proj",
		PortfolioID:     portfolioID,
		StrategyID:      strategyID,
		RunKind:         "strategy",
		StrategyVersion: 1,
		Name:            name,
		Status:          "queued",
		Symbols:         []string{"BTC-USD", "ETH-USD", "SOL-USD"},
		StartAt:         "2026-01-01",
		EndAt:           "2026-03-31",
		Interval:        "1d",
		StartingCash:    100000,
		FeeBps:          1,
		SlippageBps:     5,
		TotalSteps:      90,
		Summary:         map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func mustLastBacktestSnapshot(t *testing.T, ctx *sdk.AppCtx, runID int64) *BacktestSnapshot {
	t.Helper()
	snaps, err := dbListBacktestSnapshots(ctx.AppDB(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) == 0 {
		t.Fatal("missing snapshots")
	}
	return snaps[len(snaps)-1]
}

func assertClose(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.0001 {
		t.Fatalf("%s=%v, want %v", label, got, want)
	}
}

func validationFixtureBars(symbols []string, steps int) []*BacktestMarketBar {
	rows := []*BacktestMarketBar{}
	for step := 1; step <= steps; step++ {
		for i, symbol := range symbols {
			base := 100.0 + float64(i*20)
			trend := 1.0 + float64(i)*0.4
			close := base + float64(step)*trend + math.Sin(float64(step))*0.5
			rows = append(rows, &BacktestMarketBar{
				Step: step, Symbol: symbol, AssetClass: inferAssetClass(symbol),
				T: int64(1704067200 + step*86400),
				O: close * 0.999, H: close * 1.002, L: close * 0.998, C: close, V: 1000,
				Source: "fixture",
			})
		}
	}
	return rows
}

func seedBacktestMarketBars(t *testing.T, ctx *sdk.AppCtx, runID int64, symbols []string, steps int) {
	t.Helper()
	rows := []*BacktestMarketBar{}
	for step := 1; step <= steps; step++ {
		for i, symbol := range symbols {
			base := 100.0 + float64(i*50)
			close := base + float64(step)*(1+float64(i)*0.3)
			rows = append(rows, &BacktestMarketBar{
				Step: step, Symbol: symbol, AssetClass: inferAssetClass(symbol),
				T: int64(1704067200 + step*3600),
				O: close * 0.999, H: close * 1.002, L: close * 0.998, C: close, V: 1000,
				Source: "fixture",
			})
		}
	}
	if err := dbReplaceBacktestMarketBars(ctx.AppDB(), runID, rows); err != nil {
		t.Fatal(err)
	}
}
