package main

import (
	"math"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type recordingStrategyProvider struct {
	interval string
	limit    int
	symbols  []string
	bars     bool
}

func (p *recordingStrategyProvider) Quote(symbol string) (*Mark, error) {
	return &Mark{Symbol: symbol, AssetClass: inferAssetClass(symbol), Price: 100}, nil
}
func (p *recordingStrategyProvider) Universe() []*Mark { return nil }
func (p *recordingStrategyProvider) Bars(symbol, rng string) ([]Bar, error) {
	p.bars = true
	return []Bar{{T: 1, C: 100}}, nil
}
func (p *recordingStrategyProvider) StrategyBars(symbol, interval string, limit int) ([]Bar, error) {
	p.interval = interval
	p.limit = limit
	p.symbols = append(p.symbols, symbol)
	out := make([]Bar, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, Bar{T: int64(i + 1), C: 100 + float64(i)})
	}
	return out, nil
}

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

func TestLiveStrategyMarketUsesCadenceBarsForIndicatorWindow(t *testing.T) {
	ctx := newTestCtx(t)
	provider := &recordingStrategyProvider{}
	globalEngine.provider = provider
	def := &StrategyDefinition{
		Universe: []string{"BTC-USD", "ETH-USD", "SOL-USD"},
		Cadence:  "1h",
		Rules: []StrategyRule{{
			Name: "top 120h momentum",
			Rank: &StrategyRank{
				Symbols: []string{"BTC-USD", "ETH-USD", "SOL-USD"},
				By:      "return_120",
				Top:     1,
			},
		}},
	}

	market := liveStrategyMarket(ctx, def)
	if provider.bars {
		t.Fatal("liveStrategyMarket used chart Bars fallback; want StrategyBars")
	}
	if provider.interval != "1h" {
		t.Fatalf("interval = %q, want 1h", provider.interval)
	}
	if provider.limit != 121 {
		t.Fatalf("limit = %d, want 121 for return_120", provider.limit)
	}
	if len(provider.symbols) != 3 {
		t.Fatalf("symbols fetched = %v", provider.symbols)
	}
	if got := len(market.history["BTC-USD"]); got != 121 {
		t.Fatalf("BTC history len = %d, want 121", got)
	}
	if ret, err := strategyMetric("BTC-USD", "return_120", market); err != nil || ret <= 0 {
		t.Fatalf("return_120 = %v, %v", ret, err)
	}
}

func TestStrategyRequiredBars(t *testing.T) {
	def := &StrategyDefinition{Rules: []StrategyRule{{
		When: &StrategyCondition{Indicator: "rsi_14", Compare: "sma_50"},
		Rank: &StrategyRank{By: "return_120"},
	}}}
	if got := strategyRequiredBars(def); got != 121 {
		t.Fatalf("strategyRequiredBars = %d, want 121", got)
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

func TestStrategyBacktestRebalanceCadenceAndRankThreshold(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Winner Shape", []string{"crypto"})
	strategyID, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
		ProjectID: "test-proj",
		Name:      "Hourly gated momentum",
		Status:    "active",
		Definition: map[string]any{
			"universe":        []any{"BTC-USD", "ETH-USD"},
			"cadence":         "1h",
			"rebalance_every": float64(3),
			"rules": []any{
				map[string]any{
					"name": "top gated momentum",
					"rank": map[string]any{
						"symbols": []any{"BTC-USD", "ETH-USD"},
						"by":      "return_3",
						"top":     float64(1),
						"min":     float64(2),
					},
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
		Name:            "cadence gated momentum",
		Status:          "queued",
		Symbols:         []string{"BTC-USD", "ETH-USD"},
		StartAt:         "2026-01-01",
		EndAt:           "2026-01-01",
		Interval:        "1h",
		StartingCash:    100000,
		FeeBps:          1,
		SlippageBps:     5,
		TotalSteps:      8,
		Summary:         map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedGatedMomentumBars(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runStrategyBacktestToEnd(run); err != nil {
		t.Fatal(err)
	}
	snaps, err := dbListBacktestSnapshots(ctx.AppDB(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 9 {
		t.Fatalf("snapshots=%d, want baseline + 8 steps", len(snaps))
	}
	if len(snaps[4].Orders) != 1 {
		t.Fatalf("step 4 orders=%d, want first buy after return_3 crosses threshold", len(snaps[4].Orders))
	}
	if len(snaps[5].Orders) != 1 || len(snaps[5].Positions) != 1 {
		t.Fatalf("step 5 should hold without a new order; orders=%d positions=%d", len(snaps[5].Orders), len(snaps[5].Positions))
	}
	if len(snaps[7].Orders) <= len(snaps[4].Orders) {
		t.Fatalf("step 7 should rebalance again; orders=%d earlier=%d", len(snaps[7].Orders), len(snaps[4].Orders))
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

func seedGatedMomentumBars(ctx *sdk.AppCtx, runID int64) error {
	btc := []float64{100, 101, 102, 105, 106, 107, 108, 109}
	eth := []float64{100, 100, 100, 100, 100, 100, 104, 108}
	rows := []*BacktestMarketBar{}
	for i := range btc {
		step := i + 1
		for symbol, price := range map[string]float64{"BTC-USD": btc[i], "ETH-USD": eth[i]} {
			rows = append(rows, &BacktestMarketBar{
				Step: step, Symbol: symbol, AssetClass: "crypto",
				T: int64(1704067200 + step*3600),
				O: price, H: price, L: price, C: price, V: 1000,
				Source: "fixture",
			})
		}
	}
	return dbReplaceBacktestMarketBars(ctx.AppDB(), runID, rows)
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
