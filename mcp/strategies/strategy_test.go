package main

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	sim "github.com/apteva/apps/mcp/strategies/internal/backtest"
	rules "github.com/apteva/apps/mcp/strategies/internal/ruleengine"
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

// rotationDefinition holds the top n symbols by period-bar return, equal
// weight, at most 70% each.
func rotationDefinition(symbols []string, timeframe string, period, n int) map[string]any {
	return map[string]any{"program": map[string]any{
		"version": rules.Version, "symbols": symbols, "timeframe": timeframe, "timezone": "UTC",
		"rules": []any{map[string]any{"id": "rotate", "on": "bar.close", "repeat": "always", "actions": []any{map[string]any{
			"kind": "target",
			"weight": map[string]any{"op": "if", "args": []any{
				map[string]any{"op": "top", "args": []any{map[string]any{"metric": "return", "period": period}, map[string]any{"value": n}}},
				map[string]any{"value": 1}, map[string]any{"value": 0},
			}},
			"construct": map[string]any{"normalize": "gross", "gross": 1, "max_position": 0.7},
		}}}},
	}}
}

func cashState(cash float64) *TradingState {
	return &TradingState{Portfolio: &Portfolio{Cash: cash}, Equity: cash, Marks: map[string]*Mark{}}
}

func TestStrategyValidateAndEvaluate(t *testing.T) {
	ctx := newTestCtx(t)
	provider := &recordingStrategyProvider{}
	globalEngine.provider = provider
	id, err := dbCreateStrategy(ctx.AppDB(), &Strategy{ProjectID: "test-proj", Name: "Crypto momentum", Status: "active", Definition: rotationDefinition([]string{"BTC-USD", "ETH-USD"}, "1d", 1, 1), Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := dbGetStrategy(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	def, warnings, err := validateStrategyDefinition(strategy.Definition)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("validate: warnings=%v err=%v", warnings, err)
	}
	result, err := evaluateRulesLive(def, cashState(100000), nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	interval, limit, symbols, usedChartBars := provider.calls()
	if usedChartBars || interval != "1d" || limit != rules.RequiredBaseBars(def.Program)+2 || len(symbols) != 2 {
		t.Fatalf("history request = %s/%d %v chart=%v", interval, limit, symbols, usedChartBars)
	}
	if result.Targets == nil || len(result.Targets.TargetAllocations) != 1 {
		t.Fatalf("targets = %#v, want one ranked holding", result.Targets)
	}
	if w := result.Targets.TargetAllocations[0].Weight; w > 0.7+1e-9 {
		t.Fatalf("weight = %v, want capped at 0.7", w)
	}
	if len(result.Checkpoint) == 0 || len(result.Targets.Decisions) == 0 {
		t.Fatal("missing checkpoint or decision explanation")
	}
}

func TestLiveRuleTapeFailsClosedOnPartialUniverse(t *testing.T) {
	newTestCtx(t)
	globalEngine.provider = &recordingStrategyProvider{errors: map[string]error{"ETH-USD": errors.New("upstream unavailable")}}
	def := mustTargetDefinition([]string{"BTC-USD", "ETH-USD"}, "1h", 0.4)
	if _, _, err := liveRuleTape(def, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "ETH-USD") {
		t.Fatalf("error = %v, want ETH-USD history failure", err)
	}
}

func TestLiveRuleTapeSkipsFormingBars(t *testing.T) {
	newTestCtx(t)
	globalEngine.provider = &recordingStrategyProvider{}
	def := mustTargetDefinition([]string{"BTC-USD"}, "1h", 0.4)
	tape, latest, err := liveRuleTape(def, time.Unix(3600+2, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Equal(time.Unix(3600+2, 0).UTC()) {
		t.Fatalf("latest close = %s, want bar 2", latest)
	}
	for _, in := range tape {
		if in.AvailableAt.After(latest) {
			t.Fatalf("forming bar in tape: %+v", in)
		}
	}
	if last := tape[len(tape)-1]; last.Type != rules.EvaluateInput {
		t.Fatalf("tape ends with %s, want an evaluation", last.Type)
	}
}

func TestLiveRuleCheckpointKeepsLatches(t *testing.T) {
	newTestCtx(t)
	globalEngine.provider = &recordingStrategyProvider{}
	def, _, err := validateStrategyDefinition(map[string]any{"program": map[string]any{
		"version": rules.Version, "symbols": []string{"BTC-USD"}, "timeframe": "1h", "timezone": "UTC",
		"rules": []any{map[string]any{"id": "once", "on": "bar.close", "repeat": "once_per_day", "actions": []any{
			map[string]any{"kind": "enter", "side": "buy", "sizing": map[string]any{"mode": "equity_pct", "amount": 0.1}},
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(10*3600, 0).UTC()
	first, err := evaluateRulesLive(def, cashState(1000), nil, now)
	if err != nil || len(first.Orders) != 1 {
		t.Fatalf("first evaluation orders=%v err=%v, want one entry", first, err)
	}
	again, err := evaluateRulesLive(def, cashState(1000), first.Checkpoint, now.Add(time.Hour))
	if err != nil || len(again.Orders) != 0 {
		t.Fatalf("checkpointed evaluation orders=%v err=%v, want latch held", again, err)
	}
	fresh, err := evaluateRulesLive(def, cashState(1000), nil, now.Add(time.Hour))
	if err != nil || len(fresh.Orders) != 1 {
		t.Fatalf("fresh evaluation orders=%v err=%v, want entry", fresh, err)
	}
}

func TestLiveSupportRefusesBacktestOnlyPrograms(t *testing.T) {
	short := map[string]any{"program": map[string]any{
		"version": rules.Version, "symbols": []string{"AAPL"}, "timeframe": "1d", "timezone": "America/New_York",
		"rules": []any{map[string]any{"id": "short", "on": "bar.close", "actions": []any{map[string]any{"kind": "enter", "side": "sell", "sizing": map[string]any{"mode": "quantity", "amount": 1}}}}},
	}}
	feature := targetDefinition([]string{"AAPL"}, "1d", map[string]any{"metric": "feature", "feature": "sentiment", "field": "score"})
	for name, raw := range map[string]map[string]any{"short entry": short, "feature": feature} {
		def, warnings, err := validateStrategyDefinition(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if liveSupport(def) == nil || len(warnings) == 0 || !strings.HasPrefix(warnings[0], "Backtest only") {
			t.Fatalf("%s: live support allowed, warnings=%v", name, warnings)
		}
	}
	if err := liveSupport(mustTargetDefinition([]string{"AAPL"}, "1d", 0.5)); err != nil {
		t.Fatalf("long target refused live: %v", err)
	}
}

func TestLiveTargetRefusesShortWeights(t *testing.T) {
	newTestCtx(t)
	globalEngine.provider = &recordingStrategyProvider{}
	def := mustTargetDefinition([]string{"BTC-USD"}, "1h", -0.5)
	if _, err := evaluateRulesLive(def, cashState(1000), nil, time.Unix(10*3600, 0).UTC()); err == nil || !strings.Contains(err.Error(), "short") {
		t.Fatalf("error = %v, want short refusal", err)
	}
}

func TestStrategyAssignmentUsesImmutableVersion(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Pinned strategy", []string{"crypto"})
	strategyID, err := dbCreateStrategy(ctx.AppDB(), &Strategy{ProjectID: "test-proj", Name: "Pinned", Status: "active", Definition: targetDefinition([]string{"BTC-USD"}, "1h", 0.5), Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbAssignStrategy(ctx.AppDB(), &StrategyAssignment{ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID, StrategyVersion: 1, ControlMode: "strategy", Cadence: "1h"}); err != nil {
		t.Fatal(err)
	}
	updated, err := dbUpdateStrategy(ctx.AppDB(), "test-proj", strategyID, &Strategy{Definition: targetDefinition([]string{"ETH-USD"}, "1h", 0.5)})
	if err != nil || updated.Version != 2 {
		t.Fatalf("update = %v, %v; want version 2", updated, err)
	}
	assignment, err := dbActiveStrategyAssignment(ctx.AppDB(), "test-proj", portfolioID)
	if err != nil || assignment.StrategyVersion != 1 {
		t.Fatalf("assignment = %v, %v; want version 1", assignment, err)
	}
	pinned, err := dbGetStrategyVersion(ctx.AppDB(), "test-proj", strategyID, 1)
	if err != nil {
		t.Fatal(err)
	}
	def, _, err := validateStrategyDefinition(pinned.Definition)
	if err != nil || def.Universe[0] != "BTC-USD" {
		t.Fatalf("pinned symbols = %v, %v; want BTC-USD", def, err)
	}
}

func TestStockStrategyExecutionWaitsForRegularSession(t *testing.T) {
	def := &StrategyDefinition{Universe: []string{"AAPL", "SPY"}}
	e := &engine{provider: &recordingStrategyProvider{}}
	if stockStrategyExecutionReady(e, nil, def, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("stock strategy became executable before the US regular session")
	}
	if !stockStrategyExecutionReady(e, nil, def, time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)) {
		t.Fatal("stock strategy did not become executable during the US regular session")
	}
	if stockStrategyExecutionReady(e, nil, def, time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC)) {
		t.Fatal("stock strategy became executable on a weekend")
	}
	if !stockStrategyExecutionReady(e, nil, &StrategyDefinition{Universe: []string{"BTC-USD"}}, time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC)) {
		t.Fatal("crypto strategy should not be constrained by the stock session")
	}
}

func TestStockStrategyExecutionHonorsUSHolidaysAndEarlyClose(t *testing.T) {
	def := &StrategyDefinition{Universe: []string{"AAPL"}}
	e := &engine{provider: &recordingStrategyProvider{}}

	holiday := time.Date(2026, 7, 3, 15, 0, 0, 0, time.UTC)
	if usEquityRegularSession(holiday) || stockStrategyExecutionReady(e, nil, def, holiday) {
		t.Fatal("stock strategy became executable on observed Independence Day")
	}
	if _, ok := strategyAssignmentCheckSlot(def, holiday); ok {
		t.Fatal("holiday produced a strategy check slot")
	}

	afterEarlyClose := time.Date(2026, 11, 27, 19, 0, 0, 0, time.UTC)
	if usEquityRegularSession(afterEarlyClose) {
		t.Fatal("stock session remained open after the scheduled early close")
	}

	newYearsEve := time.Date(2027, 12, 31, 16, 0, 0, 0, time.UTC)
	if !usEquityRegularSession(newYearsEve) {
		t.Fatal("Friday before Saturday New Year's Day should remain open")
	}
}

func TestStrategyAssignmentScheduleExposesHolidayDecision(t *testing.T) {
	assignment := &StrategyAssignment{}
	def := &StrategyDefinition{Universe: []string{"AAPL"}, Cadence: "1d"}
	now := time.Date(2026, 7, 3, 15, 0, 0, 0, time.UTC)
	decorateStrategyAssignmentSchedule(assignment, def, now)

	if assignment.Eligibility != "waiting" || assignment.EligibilityReason != "market_holiday: Independence Day" {
		t.Fatalf("schedule = %#v", assignment)
	}
	if assignment.NextEligibleAt != "2026-07-06T13:30:00Z" {
		t.Fatalf("next eligible = %q", assignment.NextEligibleAt)
	}
}

func TestStrategyRuntimeDistinguishesAssignedAndUnassigned(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Assigned portfolio", []string{"crypto"})
	assignedID := mustCreateFixedStrategy(t, ctx, "Assigned strategy", "BTC-USD", 0.5)
	unassignedID := mustCreateFixedStrategy(t, ctx, "Unassigned strategy", "ETH-USD", 0.5)
	if _, err := dbAssignStrategy(ctx.AppDB(), &StrategyAssignment{
		ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: assignedID,
		StrategyVersion: 1, ControlMode: "strategy", Cadence: "1h",
	}); err != nil {
		t.Fatal(err)
	}
	strategies, err := dbListStrategies(ctx.AppDB(), "test-proj", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := enrichStrategyRuntime(ctx.AppDB(), "test-proj", strategies, time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	byID := map[int64]*Strategy{}
	for _, strategy := range strategies {
		byID[strategy.ID] = strategy
	}
	if byID[assignedID].AssignmentStatus != "assigned" || len(byID[assignedID].Assignments) != 1 {
		t.Fatalf("assigned strategy = %#v", byID[assignedID])
	}
	if byID[assignedID].Assignments[0].PortfolioName != "Assigned portfolio" {
		t.Fatalf("portfolio name = %q", byID[assignedID].Assignments[0].PortfolioName)
	}
	if byID[unassignedID].AssignmentStatus != "unassigned" || len(byID[unassignedID].Assignments) != 0 {
		t.Fatalf("unassigned strategy = %#v", byID[unassignedID])
	}
}

func TestStrategyAssignmentStockCheckSlotsAlignToSessionBars(t *testing.T) {
	def := &StrategyDefinition{Cadence: "1h", Universe: []string{"AAPL"}}
	now := time.Date(2026, 7, 14, 14, 37, 0, 0, time.UTC)
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

	if got, err := evaluateLiveStrategyAssignments(globalEngine, ctx, time.Date(2026, 7, 13, 10, 37, 0, 0, time.UTC)); err != nil || got != 0 {
		t.Fatalf("strategy orders = %d, want no upgrade-time trade, err = %v", got, err)
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
	filled, err := dbListOrders(tradingDB(ctx), portfolioID, "filled", 10)
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
	if err := dbUpdatePortfolioConfig(tradingDB(ctx), portfolioID, map[string]any{
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

	if got, err := evaluateLiveStrategyAssignments(globalEngine, ctx, time.Date(2026, 7, 13, 10, 37, 0, 0, time.UTC)); err != nil || got != 1 {
		t.Fatalf("strategy orders = %d, want 1, err = %v", got, err)
	}
	filled, err := dbListOrders(tradingDB(ctx), portfolioID, "filled", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(filled) != 1 {
		t.Fatalf("filled orders = %d, want 1", len(filled))
	}
	pf, err := dbGetPortfolio(tradingDB(ctx), "test-proj", portfolioID)
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
	if _, err := tradingDB(ctx).Exec(`
		CREATE TRIGGER force_strategy_insufficient_cash
		AFTER INSERT ON orders WHEN NEW.source = 'app:strategies'
		BEGIN
			UPDATE portfolios SET cash = 0 WHERE id = NEW.portfolio_id;
		END`); err != nil {
		t.Fatal(err)
	}

	if got, err := evaluateLiveStrategyAssignments(globalEngine, ctx, time.Date(2026, 7, 13, 10, 37, 0, 0, time.UTC)); err != nil || got != 1 {
		t.Fatalf("strategy orders = %d, want 1, err = %v", got, err)
	}
	rejected, err := dbListOrders(tradingDB(ctx), portfolioID, "cancelled", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 1 || rejected[0].RejectionDetail == "" {
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

func mustCreateFixedStrategy(t *testing.T, ctx *sdk.AppCtx, name, symbol string, weight float64, cadence ...string) int64 {
	t.Helper()
	interval := "1h"
	if len(cadence) > 0 {
		interval = cadence[0]
	}
	id, err := dbCreateStrategy(ctx.AppDB(), &Strategy{ProjectID: "test-proj", Name: name, Status: "active", Version: 1, Definition: targetDefinition([]string{symbol}, interval, weight)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// mustRuleBacktest creates a strategy run over seeded bars, enables its event
// simulation and returns the run.
func mustRuleBacktest(t *testing.T, ctx *sdk.AppCtx, portfolioID, strategyID int64, symbols []string, interval string, steps int, seed func(runID int64)) *BacktestRun {
	t.Helper()
	id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID, RunKind: "strategy", StrategyVersion: 1, Name: "rule replay", Status: "queued", Symbols: symbols, Interval: interval, StartingCash: 100000, FeeBps: 1, SlippageBps: 5, TotalSteps: steps, Summary: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(id)
	} else {
		seedBacktestMarketBars(t, ctx, id, symbols, steps)
	}
	if err := enableEventBacktest(ctx.AppDB(), "test-proj", id, nil, nil); err != nil {
		t.Fatal(err)
	}
	run, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func mustFinishRuleBacktest(t *testing.T, ctx *sdk.AppCtx, run *BacktestRun) *simulationRecord {
	t.Helper()
	if _, err := runEventSimulationToEnd(run); err != nil {
		t.Fatal(err)
	}
	record, err := loadSimulation(ctx.AppDB(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !record.State.Finished {
		t.Fatal("simulation did not finish")
	}
	return record
}

func filledOrders(s *sim.State) []*sim.Order {
	out := []*sim.Order{}
	for _, o := range s.Orders {
		if o.FilledQty > 0 {
			out = append(out, o)
		}
	}
	return out
}

func TestRuleBacktestHoldsTargetWeight(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Strategy BT", []string{"crypto"})
	sid := mustCreateFixedStrategy(t, ctx, "BTC fixed", "BTC-USD", 0.5)
	record := mustFinishRuleBacktest(t, ctx, mustRuleBacktest(t, ctx, pid, sid, []string{"BTC-USD"}, "1h", 8, nil))
	pos := record.State.Positions["BTC-USD"]
	price := record.State.Quotes["BTC-USD"].Price
	equity := record.State.Cash + pos.Qty*price
	if w := pos.Qty * price / equity; math.Abs(w-0.5) > 0.03 {
		t.Fatalf("BTC weight = %.4f, want about 0.5", w)
	}
	if fills := filledOrders(record.State); len(fills) == 0 || fills[0].Side != "buy" {
		t.Fatalf("fills = %+v, want an opening buy", fills)
	}
}

func TestStrategyBacktestUsesPinnedDefinitionAfterUpdate(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Pinned backtest", []string{"crypto"})
	sid := mustCreateFixedStrategy(t, ctx, "Pinned replay", "BTC-USD", 0.5)
	if _, err := dbUpdateStrategy(ctx.AppDB(), "test-proj", sid, &Strategy{Definition: targetDefinition([]string{"ETH-USD"}, "1h", 0.5)}); err != nil {
		t.Fatal(err)
	}
	record := mustFinishRuleBacktest(t, ctx, mustRuleBacktest(t, ctx, pid, sid, []string{"BTC-USD", "ETH-USD"}, "1h", 4, nil))
	if record.State.Positions["BTC-USD"].Qty <= 0 || record.State.Positions["ETH-USD"].Qty != 0 {
		t.Fatalf("positions = %+v, want pinned v1 BTC holding", record.State.Positions)
	}
}

func TestStrategyBacktestRunMatchesManualSteps(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Strategy BT Compare", []string{"crypto"})
	symbols := []string{"BTC-USD", "ETH-USD", "SOL-USD"}
	sid, err := dbCreateStrategy(ctx.AppDB(), &Strategy{ProjectID: "test-proj", Name: "Momentum compare", Status: "active", Definition: rotationDefinition(symbols, "1h", 5, 2), Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	slow := mustRuleBacktest(t, ctx, pid, sid, symbols, "1h", 30, nil)
	fast := mustRuleBacktest(t, ctx, pid, sid, symbols, "1h", 30, nil)
	for i := 0; ; i++ {
		next, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", slow.ID)
		if err != nil {
			t.Fatal(err)
		}
		if next.Status == "completed" {
			break
		}
		if i > 1000 {
			t.Fatal("manual stepping did not finish")
		}
		if _, err := startEventSimulationWith(next, true, true); err != nil {
			t.Fatal(err)
		}
	}
	a, err := loadSimulation(ctx.AppDB(), slow.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := mustFinishRuleBacktest(t, ctx, fast)
	assertClose(t, "cash", a.State.Cash, b.State.Cash)
	for _, s := range symbols {
		assertClose(t, s+" qty", a.State.Positions[s].Qty, b.State.Positions[s].Qty)
	}
	if len(filledOrders(a.State)) != len(filledOrders(b.State)) || len(filledOrders(b.State)) == 0 {
		t.Fatalf("fills %d vs %d", len(filledOrders(a.State)), len(filledOrders(b.State)))
	}
}

func TestStrategyBacktestRankThresholdRotates(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Winner Shape", []string{"crypto"})
	ret := map[string]any{"metric": "return", "period": 3}
	weight := map[string]any{"op": "if", "args": []any{
		map[string]any{"op": "and", "args": []any{map[string]any{"op": "top", "args": []any{ret, map[string]any{"value": 1}}}, map[string]any{"op": ">", "args": []any{ret, map[string]any{"value": 2}}}}},
		map[string]any{"value": 1}, map[string]any{"value": 0},
	}}
	def := targetDefinition([]string{"BTC-USD", "ETH-USD"}, "1h", weight)
	def["program"].(map[string]any)["rules"].([]any)[0].(map[string]any)["actions"].([]any)[0].(map[string]any)["construct"] = map[string]any{"normalize": "gross", "max_position": 0.9}
	sid, err := dbCreateStrategy(ctx.AppDB(), &Strategy{ProjectID: "test-proj", Name: "Gated momentum", Status: "active", Definition: def, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	run := mustRuleBacktest(t, ctx, pid, sid, []string{"BTC-USD", "ETH-USD"}, "1h", 8, func(id int64) {
		if err := seedGatedMomentumBars(ctx, id); err != nil {
			t.Fatal(err)
		}
	})
	record := mustFinishRuleBacktest(t, ctx, run)
	fills := filledOrders(record.State)
	if len(fills) < 3 || fills[0].Symbol != "BTC-USD" || fills[0].Side != "buy" {
		t.Fatalf("fills = %+v, want BTC buy then rotation", fills)
	}
	// BTC clears the 2% gate at bar 4 and fills on bar 5; ETH leads at bar 7.
	if got := fills[0].FirstFillAt.Unix(); got != 1704067200+5*3600 {
		t.Fatalf("first fill at %d, want bar 5", got)
	}
	if record.State.Positions["BTC-USD"].Qty != 0 || record.State.Positions["ETH-USD"].Qty <= 0 {
		t.Fatalf("positions = %+v, want rotated into ETH", record.State.Positions)
	}
}

func TestStrategyBacktestExecutesPriorCloseSignalAtNextOpen(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Stock timing", []string{"equity"})
	sid := mustCreateFixedStrategy(t, ctx, "AAPL fixed", "AAPL", 0.5, "1d")
	run := mustRuleBacktest(t, ctx, pid, sid, []string{"AAPL"}, "1d", 2, func(id int64) {
		rows := []*BacktestMarketBar{
			{Step: 1, Symbol: "AAPL", AssetClass: "equity", T: 1783603800, O: 310, H: 317, L: 309, C: 316.22, Source: "fixture"},
			{Step: 2, Symbol: "AAPL", AssetClass: "equity", T: 1783690200, O: 300, H: 312, L: 299, C: 310, Source: "fixture"},
		}
		if err := dbReplaceBacktestMarketBars(ctx.AppDB(), id, rows); err != nil {
			t.Fatal(err)
		}
	})
	fills := filledOrders(mustFinishRuleBacktest(t, ctx, run).State)
	if len(fills) != 1 {
		t.Fatalf("fills = %+v, want one", fills)
	}
	assertClose(t, "next-open fill", fills[0].AvgFillPrice, 300*1.0005)
}

func TestStrategyValidationCreatesOutOfSampleRuns(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Strategy Validation", []string{"crypto"})
	pf, err := dbGetPortfolio(tradingDB(ctx), "test-proj", pid)
	if err != nil {
		t.Fatal(err)
	}
	strategyID, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
		ProjectID:  "test-proj",
		Name:       "Validation momentum",
		Status:     "active",
		Definition: rotationDefinition([]string{"BTC-USD", "ETH-USD"}, "1d", 3, 1),
		Version:    1,
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

func TestLiveAssignmentPersistsRuleCheckpoint(t *testing.T) {
	ctx := newTestCtx(t)
	globalEngine.provider = &recordingStrategyProvider{}
	portfolioID := mustCreatePortfolio(t, ctx, "Checkpointed", []string{"crypto"})
	strategyID := mustCreateFixedStrategy(t, ctx, "Checkpoint BTC", "BTC-USD", 0.5)
	if _, err := dbAssignStrategy(ctx.AppDB(), &StrategyAssignment{ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID, StrategyVersion: 1, ControlMode: "strategy", Cadence: "1h"}); err != nil {
		t.Fatal(err)
	}
	if _, err := evaluateLiveStrategyAssignments(globalEngine, ctx, time.Date(2026, 7, 13, 10, 37, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	assignment, err := dbActiveStrategyAssignment(ctx.AppDB(), "test-proj", portfolioID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := dbRuleAssignmentState(ctx.AppDB(), assignment.ID)
	if err != nil || len(state) == 0 {
		t.Fatalf("rule checkpoint = %q, %v; want saved", state, err)
	}
	var cp map[string]any
	if err := json.Unmarshal(state, &cp); err != nil || cp["variables"] == nil {
		t.Fatalf("checkpoint = %s, %v", state, err)
	}
}
