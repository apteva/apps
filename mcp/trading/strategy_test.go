package main

import "testing"

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
