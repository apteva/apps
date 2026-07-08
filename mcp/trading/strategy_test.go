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
