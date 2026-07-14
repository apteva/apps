package main

import (
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type recordingStrategyProvider struct {
	mu       sync.Mutex
	interval string
	limit    int
	symbols  []string
	bars     bool
	errors   map[string]error
	short    map[string]int
}

func (p *recordingStrategyProvider) Quote(symbol string) (*Mark, error) {
	return &Mark{Symbol: symbol, AssetClass: inferAssetClass(symbol), Price: 100}, nil
}
func (p *recordingStrategyProvider) Universe() []*Mark { return nil }
func (p *recordingStrategyProvider) Bars(symbol, rng string) ([]Bar, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bars = true
	return []Bar{{T: 1, C: 100}}, nil
}
func (p *recordingStrategyProvider) StrategyBars(symbol, interval string, limit int) ([]Bar, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interval = interval
	p.limit = limit
	p.symbols = append(p.symbols, symbol)
	if err := p.errors[symbol]; err != nil {
		return nil, err
	}
	if n := p.short[symbol]; n > 0 && n < limit {
		limit = n
	}
	out := make([]Bar, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, Bar{T: int64(i + 1), C: 100 + float64(i)})
	}
	return out, nil
}

func (p *recordingStrategyProvider) calls() (string, int, []string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.interval, p.limit, append([]string(nil), p.symbols...), p.bars
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
	market, err := liveStrategyMarket(ctx, def)
	if err != nil {
		t.Fatal(err)
	}
	eval, err := evaluateStrategy(strategy, market)
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

	market, err := liveStrategyMarket(ctx, def)
	if err != nil {
		t.Fatal(err)
	}
	interval, limit, symbols, usedChartBars := provider.calls()
	if usedChartBars {
		t.Fatal("liveStrategyMarket used chart Bars fallback; want StrategyBars")
	}
	if interval != "1h" {
		t.Fatalf("interval = %q, want 1h", interval)
	}
	if limit != 121 {
		t.Fatalf("limit = %d, want 121 for return_120", limit)
	}
	if len(symbols) != 3 {
		t.Fatalf("symbols fetched = %v", symbols)
	}
	if got := len(market.history["BTC-USD"]); got != 121 {
		t.Fatalf("BTC history len = %d, want 121", got)
	}
	if ret, err := strategyMetric("BTC-USD", "return_120", market); err != nil || ret <= 0 {
		t.Fatalf("return_120 = %v, %v", ret, err)
	}
}

func TestLiveStrategyMarketLoadsEnoughBarsForRebalanceSchedule(t *testing.T) {
	ctx := newTestCtx(t)
	provider := &recordingStrategyProvider{}
	globalEngine.provider = provider
	def := &StrategyDefinition{
		Universe:       []string{"AAPL"},
		Cadence:        "1d",
		RebalanceEvery: 5,
		Rules: []StrategyRule{{
			Name: "fixed", Allocate: []StrategyAllocation{{Symbol: "AAPL", Weight: 0.5}},
		}},
	}
	market, err := liveStrategyMarket(ctx, def)
	if err != nil {
		t.Fatal(err)
	}
	interval, limit, _, _ := provider.calls()
	if interval != "1d" || limit != 6 {
		t.Fatalf("history request = %s/%d, want 1d/6 for a five-bar schedule", interval, limit)
	}
	if len(market.barTimes) != 6 {
		t.Fatalf("schedule bars = %d, want 6", len(market.barTimes))
	}
}

func TestLiveStrategyMarketFailsClosedOnPartialUniverse(t *testing.T) {
	ctx := newTestCtx(t)
	provider := &recordingStrategyProvider{errors: map[string]error{"ETH-USD": errors.New("upstream unavailable")}}
	globalEngine.provider = provider
	def := &StrategyDefinition{
		Universe: []string{"BTC-USD", "ETH-USD"},
		Cadence:  "1h",
		Rules: []StrategyRule{{Rank: &StrategyRank{
			Symbols: []string{"BTC-USD", "ETH-USD"}, By: "return_1", Top: 1,
		}}},
	}

	_, err := liveStrategyMarket(ctx, def)
	if err == nil || !strings.Contains(err.Error(), "ETH-USD") {
		t.Fatalf("error = %v, want ETH-USD history failure", err)
	}
}

func TestLiveStrategyMarketFailsClosedOnShortHistory(t *testing.T) {
	ctx := newTestCtx(t)
	provider := &recordingStrategyProvider{short: map[string]int{"BTC-USD": 1}}
	globalEngine.provider = provider
	def := &StrategyDefinition{
		Universe: []string{"BTC-USD"},
		Cadence:  "1h",
		Rules: []StrategyRule{{Rank: &StrategyRank{
			Symbols: []string{"BTC-USD"}, By: "return_2", Top: 1,
		}}},
	}

	_, err := liveStrategyMarket(ctx, def)
	if err == nil || !strings.Contains(err.Error(), "need 3 valid closed bars, got 1") {
		t.Fatalf("error = %v, want incomplete-history detail", err)
	}
}

func TestStrategyRankHoldIncludesComputedValues(t *testing.T) {
	market := strategyMarket{
		prices: map[string]float64{"BTC-USD": 104.98, "ETH-USD": 99},
		history: map[string][]float64{
			"BTC-USD": []float64{100, 104.98},
			"ETH-USD": []float64{100, 99},
		},
	}
	_, reason, err := evalStrategyRank(StrategyRank{
		Symbols: []string{"BTC-USD", "ETH-USD"}, By: "return_1", Top: 1, Min: 5,
	}, market)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "BTC-USD 4.9800") || !strings.Contains(reason, "ETH-USD -1.0000") {
		t.Fatalf("reason = %q, want all computed rank values", reason)
	}
}

func TestStrategyRankFailsWhenAnySymbolMetricIsUnavailable(t *testing.T) {
	market := strategyMarket{
		prices:  map[string]float64{"BTC-USD": 101},
		history: map[string][]float64{"BTC-USD": []float64{100, 101}},
	}
	_, _, err := evalStrategyRank(StrategyRank{
		Symbols: []string{"BTC-USD", "ETH-USD"}, By: "return_1", Top: 1,
	}, market)
	if err == nil || !strings.Contains(err.Error(), "ETH-USD") {
		t.Fatalf("error = %v, want missing ETH-USD metric", err)
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

func TestStrategyAssignmentUsesImmutableVersion(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Pinned strategy", []string{"crypto"})
	v1 := map[string]any{
		"universe": []any{"BTC-USD", "ETH-USD"},
		"cadence":  "1h",
		"rules": []any{map[string]any{
			"name": "BTC allocation", "allocate": []any{map[string]any{"symbol": "BTC-USD", "weight": 0.5}},
		}},
	}
	strategyID, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
		ProjectID: "test-proj", Name: "Pinned", Status: "active", Definition: v1, Version: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbAssignStrategy(ctx.AppDB(), &StrategyAssignment{
		ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID,
		StrategyVersion: 1, ControlMode: "strategy", Cadence: "1h",
	}); err != nil {
		t.Fatal(err)
	}
	v2 := map[string]any{
		"universe": []any{"BTC-USD", "ETH-USD"},
		"cadence":  "1h",
		"rules": []any{map[string]any{
			"name": "ETH allocation", "allocate": []any{map[string]any{"symbol": "ETH-USD", "weight": 0.5}},
		}},
	}
	updated, err := dbUpdateStrategy(ctx.AppDB(), "test-proj", strategyID, &Strategy{Definition: v2})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 {
		t.Fatalf("updated version = %d, want 2", updated.Version)
	}
	assignment, err := dbActiveStrategyAssignment(ctx.AppDB(), "test-proj", portfolioID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.StrategyVersion != 1 {
		t.Fatalf("assignment version = %d, want 1", assignment.StrategyVersion)
	}
	pinned, err := dbGetStrategyVersion(ctx.AppDB(), "test-proj", strategyID, assignment.StrategyVersion)
	if err != nil {
		t.Fatal(err)
	}
	def, _, err := validateStrategyDefinition(pinned.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if got := def.Rules[0].Allocate[0].Symbol; got != "BTC-USD" {
		t.Fatalf("pinned allocation = %s, want BTC-USD", got)
	}
}

func TestStockStrategyExecutionWaitsForRegularSession(t *testing.T) {
	def := &StrategyDefinition{Universe: []string{"AAPL", "SPY"}}
	e := &engine{provider: &recordingStrategyProvider{}}
	if stockStrategyExecutionReady(e, def, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("stock strategy became executable before the US regular session")
	}
	if !stockStrategyExecutionReady(e, def, time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)) {
		t.Fatal("stock strategy did not become executable during the US regular session")
	}
	if stockStrategyExecutionReady(e, def, time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC)) {
		t.Fatal("stock strategy became executable on a weekend")
	}
	if !stockStrategyExecutionReady(e, &StrategyDefinition{Universe: []string{"BTC-USD"}}, time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC)) {
		t.Fatal("crypto strategy should not be constrained by the stock session")
	}
}

func TestStrategyAssignmentCadenceCountsCompletedBars(t *testing.T) {
	def := &StrategyDefinition{Cadence: "1d", RebalanceEvery: 5, Universe: []string{"AAPL"}}
	a := &StrategyAssignment{LastMarketBarAt: "2026-07-02T13:30:00Z"}
	market := strategyMarket{barTimes: []time.Time{
		time.Date(2026, 7, 6, 13, 30, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 13, 30, 0, 0, time.UTC),
		time.Date(2026, 7, 8, 13, 30, 0, 0, time.UTC),
		time.Date(2026, 7, 9, 13, 30, 0, 0, time.UTC),
	}}
	if strategyAssignmentDueForMarket(a, def, market) {
		t.Fatal("four completed sessions became due after a weekend")
	}
	market.barTimes = append(market.barTimes, time.Date(2026, 7, 10, 13, 30, 0, 0, time.UTC))
	if !strategyAssignmentDueForMarket(a, def, market) {
		t.Fatal("five completed sessions did not become due")
	}
}

func TestStrategyAssignmentStockCheckSlotsAlignToSessionBars(t *testing.T) {
	def := &StrategyDefinition{Cadence: "1h", Universe: []string{"AAPL"}}
	now := time.Date(2026, 7, 14, 14, 37, 0, 0, time.UTC) // 10:37 New York.
	slot, ok := strategyAssignmentCheckSlot(def, now)
	want := time.Date(2026, 7, 14, 14, 30, 0, 0, time.UTC)
	if !ok || !slot.Equal(want) {
		t.Fatalf("stock check slot = %s ok=%v, want %s", slot, ok, want)
	}
	a := &StrategyAssignment{LastCheckedAt: want.Format(time.RFC3339)}
	if strategyAssignmentCheckDue(a, slot) {
		t.Fatal("assignment checked the same completed-bar slot twice")
	}
	next, _ := strategyAssignmentCheckSlot(def, time.Date(2026, 7, 14, 15, 31, 0, 0, time.UTC))
	if !strategyAssignmentCheckDue(a, next) {
		t.Fatal("assignment did not check the next session-aligned bar slot")
	}
}

func TestStrategyAssignmentUpgradeInitializesBarClockWithoutTrading(t *testing.T) {
	ctx := newTestCtx(t)
	globalEngine.provider = &recordingStrategyProvider{}
	portfolioID := mustCreatePortfolio(t, ctx, "Legacy schedule", []string{"crypto"})
	strategyID := mustCreateFixedStrategy(t, ctx, "Legacy BTC", "BTC-USD", 0.5)
	if _, err := dbAssignStrategy(ctx.AppDB(), &StrategyAssignment{
		ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID,
		StrategyVersion: 1, ControlMode: "strategy", Cadence: "1h",
	}); err != nil {
		t.Fatal(err)
	}
	const previousEvaluation = "2026-07-13T09:00:00Z"
	if _, err := ctx.AppDB().Exec(`
		UPDATE portfolio_strategy_assignments SET last_evaluated_at = ?
		WHERE portfolio_id = ? AND status = 'active'`, previousEvaluation, portfolioID); err != nil {
		t.Fatal(err)
	}

	if got := evaluateLiveStrategyAssignments(globalEngine, ctx, time.Date(2026, 7, 13, 10, 37, 0, 0, time.UTC)); got != 0 {
		t.Fatalf("strategy orders = %d, want no upgrade-time trade", got)
	}
	assignment, err := dbActiveStrategyAssignment(ctx.AppDB(), "test-proj", portfolioID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.LastEvaluatedAt != previousEvaluation {
		t.Fatalf("last evaluated = %q, want existing value preserved", assignment.LastEvaluatedAt)
	}
	if assignment.LastMarketBarAt == "" || assignment.LastSeenBarAt == "" || assignment.LastCheckedAt == "" {
		t.Fatalf("bar scheduler was not initialized: %#v", assignment)
	}
	filled, err := dbListOrders(ctx.AppDB(), portfolioID, "filled", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(filled) != 0 {
		t.Fatalf("upgrade initialization created orders: %#v", filled)
	}
}

func TestStrategyFullAllocationReservesFeesAndSlippage(t *testing.T) {
	ctx := newTestCtx(t)
	provider := &recordingStrategyProvider{}
	globalEngine.provider = provider
	portfolioID := mustCreatePortfolio(t, ctx, "Full allocation", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(ctx.AppDB(), portfolioID, map[string]any{
		"fee_bps": 1.0, "slippage_bps": 5.0,
	}); err != nil {
		t.Fatal(err)
	}
	strategyID := mustCreateFixedStrategy(t, ctx, "Full BTC", "BTC-USD", 1)
	if _, err := dbAssignStrategy(ctx.AppDB(), &StrategyAssignment{
		ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID,
		StrategyVersion: 1, ControlMode: "strategy", Cadence: "1h",
	}); err != nil {
		t.Fatal(err)
	}

	if got := evaluateLiveStrategyAssignments(globalEngine, ctx, time.Date(2026, 7, 13, 10, 37, 0, 0, time.UTC)); got != 1 {
		t.Fatalf("strategy orders = %d, want 1", got)
	}
	filled, err := dbListOrders(ctx.AppDB(), portfolioID, "filled", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(filled) != 1 {
		t.Fatalf("filled orders = %d, want 1", len(filled))
	}
	pf, err := dbGetPortfolio(ctx.AppDB(), "test-proj", portfolioID)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Cash < -1e-6 || pf.Cash >= 100 {
		t.Fatalf("cash = %.8f, want a non-negative near-full allocation remainder", pf.Cash)
	}
	assignment, err := dbActiveStrategyAssignment(ctx.AppDB(), "test-proj", portfolioID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.LastEvaluatedAt != "2026-07-13T10:00:00Z" {
		t.Fatalf("last evaluated = %q, want aligned hourly slot", assignment.LastEvaluatedAt)
	}
}

func TestRejectedStrategyOrderDoesNotConsumeCadenceSlot(t *testing.T) {
	ctx := newTestCtx(t)
	globalEngine.provider = &recordingStrategyProvider{}
	portfolioID := mustCreatePortfolio(t, ctx, "Retry rejection", []string{"crypto"})
	strategyID := mustCreateFixedStrategy(t, ctx, "Retry BTC", "BTC-USD", 1)
	if _, err := dbAssignStrategy(ctx.AppDB(), &StrategyAssignment{
		ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID,
		StrategyVersion: 1, ControlMode: "strategy", Cadence: "1h",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`
		CREATE TRIGGER force_strategy_insufficient_cash
		AFTER INSERT ON orders WHEN NEW.source = 'strategy'
		BEGIN
			UPDATE portfolios SET cash = 0 WHERE id = NEW.portfolio_id;
		END`); err != nil {
		t.Fatal(err)
	}

	if got := evaluateLiveStrategyAssignments(globalEngine, ctx, time.Date(2026, 7, 13, 10, 37, 0, 0, time.UTC)); got != 1 {
		t.Fatalf("strategy orders = %d, want 1", got)
	}
	rejected, err := dbListOrders(ctx.AppDB(), portfolioID, "rejected", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 1 || rejected[0].RejectionCode != "insufficient_cash" {
		t.Fatalf("rejected orders = %#v, want one insufficient_cash rejection", rejected)
	}
	assignment, err := dbActiveStrategyAssignment(ctx.AppDB(), "test-proj", portfolioID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.LastEvaluatedAt != "" {
		t.Fatalf("last evaluated = %q, want empty so the strategy retries", assignment.LastEvaluatedAt)
	}
}

func mustCreateFixedStrategy(t *testing.T, ctx *sdk.AppCtx, name, symbol string, weight float64) int64 {
	t.Helper()
	id, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
		ProjectID: "test-proj", Name: name, Status: "active", Version: 1,
		Definition: map[string]any{
			"universe": []any{symbol}, "cadence": "1h",
			"rules": []any{map[string]any{
				"name": "fixed allocation", "allocate": []any{map[string]any{"symbol": symbol, "weight": weight}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
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
	if len(snaps[1].Positions) != 0 || len(snaps[1].Orders) != 0 {
		t.Fatalf("step-one state positions=%#v orders=%#v, want signal only", snaps[1].Positions, snaps[1].Orders)
	}
	if _, err := stepStrategyBacktestRun(next); err != nil {
		t.Fatal(err)
	}
	snaps, err = dbListBacktestSnapshots(ctx.AppDB(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps[2].Positions) != 1 || len(snaps[2].Orders) != 1 {
		t.Fatalf("step-two state positions=%#v orders=%#v, want next-open BTC fill", snaps[2].Positions, snaps[2].Orders)
	}
	events, err := dbListBacktestEvents(ctx.AppDB(), runID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected strategy backtest events")
	}
}

func TestStrategyBacktestUsesPinnedDefinitionAfterUpdate(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Pinned backtest", []string{"crypto"})
	strategyID := mustCreateFixedStrategy(t, ctx, "Pinned replay", "BTC-USD", 0.5)
	runID, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{
		ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID,
		RunKind: "strategy", StrategyVersion: 1, Name: "Pinned replay", Status: "queued",
		Symbols: []string{"BTC-USD", "ETH-USD"}, StartAt: "2026-01-01", EndAt: "2026-01-03",
		Interval: "1d", StartingCash: 100000, TotalSteps: 3, Summary: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedDefinition := map[string]any{
		"universe": []any{"ETH-USD"}, "cadence": "1h",
		"rules": []any{map[string]any{
			"name": "ETH allocation", "allocate": []any{map[string]any{"symbol": "ETH-USD", "weight": 0.5}},
		}},
	}
	if _, err := dbUpdateStrategy(ctx.AppDB(), "test-proj", strategyID, &Strategy{Definition: updatedDefinition}); err != nil {
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
	if _, err := stepStrategyBacktestRun(next); err != nil {
		t.Fatal(err)
	}
	snapshots, err := dbListBacktestSnapshots(ctx.AppDB(), runID)
	if err != nil {
		t.Fatal(err)
	}
	latest := snapshots[len(snapshots)-1]
	if len(latest.Positions) != 1 || latest.Positions[0].Symbol != "BTC-USD" {
		t.Fatalf("positions = %#v, want pinned v1 BTC allocation", latest.Positions)
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
	if len(snaps[4].Orders) != 0 {
		t.Fatalf("step 4 orders=%d, want signal only", len(snaps[4].Orders))
	}
	if len(snaps[5].Orders) != 1 || len(snaps[5].Positions) != 1 {
		t.Fatalf("step 5 should execute step 4 signal; orders=%d positions=%d", len(snaps[5].Orders), len(snaps[5].Positions))
	}
	if len(snaps[7].Orders) != 0 || len(snaps[8].Orders) == 0 {
		t.Fatalf("step 7 should signal and step 8 execute; orders=%d/%d", len(snaps[7].Orders), len(snaps[8].Orders))
	}
}

func TestStrategyBacktestExecutesPriorCloseSignalAtNextOpen(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Stock timing", []string{"equity"})
	strategyID := mustCreateFixedStrategy(t, ctx, "AAPL fixed", "AAPL", 0.5)
	runID, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{
		ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID,
		RunKind: "strategy", StrategyVersion: 1, Name: "next-open timing", Status: "queued",
		Symbols: []string{"AAPL"}, StartAt: "2026-07-09", EndAt: "2026-07-10",
		Interval: "1d", StartingCash: 100000, SlippageBps: 5, TotalSteps: 2, Summary: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := []*BacktestMarketBar{
		{Step: 1, Symbol: "AAPL", AssetClass: "equity", T: 1783603800, O: 310, H: 317, L: 309, C: 316.22, Source: "fixture"},
		{Step: 2, Symbol: "AAPL", AssetClass: "equity", T: 1783690200, O: 300, H: 312, L: 299, C: 310, Source: "fixture"},
	}
	if err := dbReplaceBacktestMarketBars(ctx.AppDB(), runID, rows); err != nil {
		t.Fatal(err)
	}
	run, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runStrategyBacktestToEnd(run); err != nil {
		t.Fatal(err)
	}
	snaps, err := dbListBacktestSnapshots(ctx.AppDB(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 3 || len(snaps[1].Orders) != 0 || len(snaps[2].Orders) != 1 {
		t.Fatalf("snapshot orders = %#v, want no step-one fill and one step-two fill", snaps)
	}
	wantFill := 300.0 * 1.0005
	assertClose(t, "next-open fill", snaps[2].Orders[0].AvgFillPrice, wantFill)
	if math.Abs(snaps[2].Orders[0].AvgFillPrice-316.22*1.0005) < 0.0001 {
		t.Fatal("fill still uses the signal session close")
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
