package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"math"
	"testing"
	"time"
)

func auditOrder(t *testing.T, ctx *sdk.AppCtx, id int64, oid, side string, qty float64) *Order {
	t.Helper()
	o := &Order{ID: oid, PortfolioID: id, Symbol: "BTC-USD", AssetClass: "crypto", Side: side, Type: "market", Qty: qty, TIF: "day", Status: "working", Rationale: "Audit reproduction with controlled broker responses", Source: "test"}
	if err := dbInsertOrder(ctx.AppDB(), o, "test-proj"); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestAuditPartialFillVWAP(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "partial", []string{"crypto"})
	pf := mustPortfolio(t, ctx, id)
	o := auditOrder(t, ctx, id, "partial", "buy", 2)
	for _, br := range []*brokerOrderResult{{Status: "working", ExecutedQty: 1, CummulativeQuoteQty: 100}, {Status: "filled", ExecutedQty: 2, CummulativeQuoteQty: 300}} {
		if _, err := applyBrokerProgress(ctx.AppDB(), "test-proj", pf, o, br); err != nil {
			t.Fatal(err)
		}
	}
	got := mustPortfolio(t, ctx, id)
	pos, err := dbGetPosition(ctx.AppDB(), id, "BTC-USD", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cash != 99700 || pos.AvgCost != 150 {
		t.Errorf("two fills at 100 and 200: cash=%v want 99700, avg_cost=%v want 150", got.Cash, pos.AvgCost)
	}
}

func TestAuditCancelDropsBrokerFill(t *testing.T) {
	p := &hardeningPlatform{connections: []sdk.PlatformConnection{{ID: 7, AppSlug: "binance-trading", Status: "active"}}, execute: func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
		return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"orderId":123,"status":"CANCELED","executedQty":"1","cummulativeQuoteQty":"100"}`)}, nil
	}}
	ctx := newHardeningCtx(t, p)
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "cancel", AllowedClasses: []string{"crypto"}, StartingCash: 1000, Mode: "live", BrokerSlug: "binance-trading"})
	if err != nil {
		t.Fatal(err)
	}
	o := auditOrder(t, ctx, id, "cancel", "buy", 2)
	if _, err := (&App{}).toolOrderCancel(ctx, map[string]any{"order_id": o.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := dbGetOrder(ctx.AppDB(), "test-proj", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilledQty != 1 {
		t.Errorf("broker cancellation reports 1 executed; local status=%s filled_qty=%v", got.Status, got.FilledQty)
	}
}

func TestAuditBitstampUnfilledAcceptedOrder(t *testing.T) {
	br, err := (bitstampAdapter{}).ParseOrder(json.RawMessage(`{"id":"123","amount":"2","price":"100","type":"0","datetime":"2026-09-05 10:00:00"}`))
	if err != nil {
		t.Fatal(err)
	}
	if br.ExecutedQty != 0 {
		t.Errorf("new resting limit has no fills but parsed executed_qty=%v cumulative_quote=%v", br.ExecutedQty, br.CummulativeQuoteQty)
	}
}

func TestAuditUniverseChangeDoesNotBlockPendingBuy(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "universe", []string{"crypto"})
	o := auditOrder(t, ctx, id, "pending", "buy", 0.01)
	pf := mustPortfolio(t, ctx, id)
	policy := defaultUniversePolicy(pf)
	policy.ExcludeSymbols = []string{"BTC-USD"}
	if _, err := dbUpsertPortfolioUniversePolicy(ctx.AppDB(), pf, *policy); err != nil {
		t.Fatal(err)
	}
	if err := tryFill(globalEngine, o); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetOrder(ctx.AppDB(), "test-proj", o.ID)
	if got.Status == "filled" {
		t.Errorf("pending buy filled after BTC was explicitly excluded")
	}
}

func TestAuditExpiredDayOrderFills(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "day", []string{"crypto"})
	o := auditOrder(t, ctx, id, "old-day", "buy", 0.01)
	if _, err := ctx.AppDB().Exec(`UPDATE orders SET placed_at='2020-01-01 00:00:00' WHERE id=?`, o.ID); err != nil {
		t.Fatal(err)
	}
	o, _ = dbGetOrder(ctx.AppDB(), "test-proj", o.ID)
	if err := tryFill(globalEngine, o); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetOrder(ctx.AppDB(), "test-proj", o.ID)
	if got.Status == "filled" {
		t.Errorf("day order placed in 2020 filled in 2026")
	}
}

func TestAuditRebuildLosesSeededRealizedPnL(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "seed", []string{"crypto"})
	pf := mustPortfolio(t, ctx, id)
	if err := dbInsertPositionRaw(ctx.AppDB(), "test-proj", id, "BTC-USD", "crypto", "", 1, 100); err != nil {
		t.Fatal(err)
	}
	o := auditOrder(t, ctx, id, "sell-seed", "sell", 1)
	if _, err := applyBrokerProgress(ctx.AppDB(), "test-proj", pf, o, &brokerOrderResult{Status: "filled", ExecutedQty: 1, CummulativeQuoteQty: 150}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := dbPortfolioAccounting(ctx.AppDB(), id)
	if err := dbRebuildPositionAccounting(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	after, _, _ := dbPortfolioAccounting(ctx.AppDB(), id)
	if math.Abs(before-after) > 1e-9 {
		t.Errorf("restart rebuild changed realized P&L from %v to %v", before, after)
	}
}

func TestAuditScorecardApprovalSurvivesStrategyVersionChange(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "gate", []string{"crypto"})
	sid := mustCreateTestStrategy(t, ctx, "BTC-USD")
	policy := defaultScorecardPolicy(id, sid, "test-proj")
	policy.EnforcementEnabled = true
	policy.RequireOutOfSample = false
	policy.Criteria = []ScorecardCriterion{{Metric: "return_pct", Operator: "min", Threshold: 0, Required: true}}
	policy, err := dbUpsertStrategyScorecardPolicy(ctx.AppDB(), *policy)
	if err != nil {
		t.Fatal(err)
	}
	rid, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{ProjectID: "test-proj", PortfolioID: id, StrategyID: sid, StrategyVersion: 1, RunKind: "strategy", Name: "v1", Status: "completed", Symbols: []string{"BTC-USD"}, StartAt: "2026-01-01", EndAt: "2026-02-01", Interval: "1d", StartingCash: 100000, TotalSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpsertBacktestSnapshot(ctx.AppDB(), &BacktestSnapshot{RunID: rid, Step: 1, Equity: 110000, Cash: 110000}); err != nil {
		t.Fatal(err)
	}
	if _, err := evaluateAndStoreStrategyScorecard(ctx.AppDB(), "test-proj", id, sid, rid); err != nil {
		t.Fatal(err)
	}
	if _, err := dbUpdateStrategy(ctx.AppDB(), "test-proj", sid, &Strategy{Definition: governanceStrategyDefinition("ETH-USD")}); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"paper_candidate", "paper", "live_candidate", "live"} {
		policy, err = promoteStrategyScorecard(ctx.AppDB(), policy, stage)
		if err != nil {
			return
		} // Correct: stale evidence cannot promote v2.
	}
	pf := mustPortfolio(t, ctx, id)
	pf.ExecutionEnvironment = "broker_live"
	if allowed, _ := scorecardAllowsExecution(ctx.AppDB(), pf, sid); allowed {
		t.Errorf("untested strategy v2 allowed live using only v1 evidence")
	}
}

func TestAuditQuoteMarksOldTradeFresh(t *testing.T) {
	ctx := newTestCtx(t)
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if err := dbUpsertMark(ctx.AppDB(), &Mark{Symbol: "AAPL", AssetClass: "equity", Price: 100, MarkedAt: old}); err != nil {
		t.Fatal(err)
	}
	m, err := streamedAlpacaMark(ctx.AppDB(), "iex", alpacaMarketMessage{Type: "q", Symbol: "AAPL", Time: time.Now().UTC().Format(time.RFC3339Nano), BidPrice: 199, AskPrice: 201})
	if err != nil {
		t.Fatal(err)
	}
	if m.Price == 100 && markFresh(m, time.Now()) {
		t.Errorf("one-hour-old price 100 becomes fresh after quote 199/201")
	}
}

func TestAuditAssetRemovalDeadlocksSingleConnection(t *testing.T) {
	p := &hardeningPlatform{execute: func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
		return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`[]`)}, nil
	}}
	ctx := newHardeningCtx(t, p)
	db := ctx.AppDB()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`INSERT INTO securities(id,asset_class,name,status,source) VALUES('removed','equity','Removed','active','test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO universe_memberships(universe_id,security_id,valid_from,source) VALUES(?,'removed','2026-01-01','test')`, referenceUniverseAlpaca); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- syncAlpacaAssets(ctx, 7, &referenceSyncResult{}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		// Close the test database to release the blocked waiter before cleanup.
		stats := db.Stats()
		_ = db.Close()
		<-done
		t.Errorf("asset removal blocked with the only connection held: InUse=%d WaitCount=%d", stats.InUse, stats.WaitCount)
	}
}

func TestAuditReplayToolCanOverwriteOrdinaryPortfolioPrices(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "ordinary", []string{"crypto"})
	out, err := (&App{}).toolBacktestMarketStep(ctx, map[string]any{"portfolio_id": id, "step": 1, "prices": []any{map[string]any{"symbol": "BTC-USD", "asset_class": "crypto", "price": 1.0}}})
	if err == nil {
		mark, _ := dbGetMark(ctx.AppDB(), "BTC-USD")
		t.Errorf("ordinary portfolio accepted replay injection: price=%v response=%v", mark.Price, out)
	}
}

func TestAuditRiskBreachDoesNotBlockNextBuy(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "risk", []string{"crypto"})
	pf := mustPortfolio(t, ctx, id)
	if _, err := dbUpsertPortfolioRiskPolicy(ctx.AppDB(), pf, riskPresets["balanced"]); err != nil {
		t.Fatal(err)
	}
	if err := dbSetDayBaseline(ctx.AppDB(), id, utcDay(time.Now()), 100000); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE portfolios SET cash=90000 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolOrderPlace(ctx, map[string]any{"portfolio_id": id, "symbol": "BTC-USD", "side": "buy", "type": "market", "qty": 0.001, "rationale": "Audit order while daily loss is already beyond the configured threshold"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["status"] == "working" {
		t.Errorf("10%% daily loss exceeds 3%% policy but new buy accepted before risk sweep")
	}
}

func TestAuditLiveSellWithoutPositionReachesBroker(t *testing.T) {
	calls := 0
	p := &hardeningPlatform{connections: []sdk.PlatformConnection{{ID: 7, AppSlug: "alpaca-trading", Status: "active"}}, execute: func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "create_order" {
			calls++
		}
		return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"id":"broker-short","status":"accepted","filled_qty":"0"}`)}, nil
	}}
	ctx := newHardeningCtx(t, p)
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "long only", AllowedClasses: []string{"equity"}, StartingCash: 100000, Mode: "live", ExecutionEnvironment: "broker_paper", BrokerSlug: "alpaca-trading"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpsertMark(ctx.AppDB(), &Mark{Symbol: "AAPL", AssetClass: "equity", Price: 100, Source: alpacaMarketDataSlug, MarkedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	profile := defaultVenueProfile("alpaca-trading", "equity")
	profile.SessionPolicy = "continuous"
	if err := dbUpsertVenueProfile(ctx.AppDB(), &profile); err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolOrderPlace(ctx, map[string]any{"portfolio_id": id, "symbol": "AAPL", "side": "sell", "type": "market", "qty": 1, "rationale": "Audit attempts to sell one share despite an entirely empty portfolio"})
	if err != nil {
		t.Fatal(err)
	}
	if calls > 0 {
		t.Errorf("sell with zero holdings sent to broker: %v", out)
	}
}

func TestAuditOldRESTMarkOverwritesNewStreamMark(t *testing.T) {
	ctx := newTestCtx(t)
	now := time.Now().UTC()
	for _, m := range []*Mark{{Symbol: "AAPL", AssetClass: "equity", Price: 200, Source: "alpaca-market-data", MarkedAt: now.Format(time.RFC3339Nano)}, {Symbol: "AAPL", AssetClass: "equity", Price: 100, Source: "yahoo-finance", MarkedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)}} {
		if err := dbUpsertMark(ctx.AppDB(), m); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := dbGetMark(ctx.AppDB(), "AAPL")
	if m.Price != 200 {
		t.Errorf("new stream price 200 replaced by older REST price %v", m.Price)
	}
}

func TestAuditOutOfSampleLabelLost(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "validation", []string{"crypto"})
	sid := mustCreateTestStrategy(t, ctx, "BTC-USD")
	strategy, err := dbGetStrategy(ctx.AppDB(), "test-proj", sid)
	if err != nil {
		t.Fatal(err)
	}
	def, _, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		t.Fatal(err)
	}
	bars := []*BacktestMarketBar{}
	for i := 1; i <= 4; i++ {
		bars = append(bars, &BacktestMarketBar{Step: i, Symbol: "BTC-USD", AssetClass: "crypto", T: time.Date(2026, 1, i, 0, 0, 0, 0, time.UTC).Unix(), O: 100, H: 100 + float64(i), L: 100, C: 100 + float64(i)})
	}
	period, err := (&App{}).createCompletedStrategyValidationRun(ctx, mustPortfolio(t, ctx, id), strategy, def, strategyValidationRunSpec{Label: "out_of_sample", Name: "test", Interval: "1d", StartingCash: 100000, MarketSource: "test", AdjustmentMode: "provider_adjusted", Bars: bars})
	if err != nil {
		t.Fatal(err)
	}
	if got := scorecardScope(period.Run); got != "out_of_sample" {
		t.Errorf("completed out-of-sample validation has scope=%s, summary=%v", got, period.Run.Summary)
	}
}

func TestAuditMissingMarkDisagreesOnEquity(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "valuation", []string{"equity"})
	if err := dbInsertPositionRaw(ctx.AppDB(), "test-proj", id, "UNQUOTED", "equity", "", 10, 100); err != nil {
		t.Fatal(err)
	}
	pf := mustPortfolio(t, ctx, id)
	eq, err := computeEquity(ctx.AppDB(), pf)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := snapshotPortfolio(ctx.AppDB(), pf)
	if err != nil {
		t.Fatal(err)
	}
	if eq != snap.Equity {
		t.Errorf("risk engine equity=%v while displayed equity=%v", eq, snap.Equity)
	}
}

func TestAuditStrategyRotationRejectsBuyBeforeSellSettles(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "rotation", []string{"crypto"})
	mark, _ := dbGetMark(ctx.AppDB(), "BTC-USD")
	if err := dbInsertPositionRaw(ctx.AppDB(), "test-proj", id, "BTC-USD", "crypto", "", 1, mark.Price); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE portfolios SET cash=0 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	sid := mustCreateTestStrategy(t, ctx, "BTC-USD", "ETH-USD")
	s, _ := dbGetStrategy(ctx.AppDB(), "test-proj", sid)
	orders, pending, err := placeStrategyPaperOrders(globalEngine, ctx, mustPortfolio(t, ctx, id), s, &StrategyAssignment{ID: 1}, &StrategyEvaluation{TargetAllocations: []StrategyAllocation{{Symbol: "ETH-USD", Weight: 1}}})
	if err != nil {
		t.Errorf("100%% BTC -> 100%% ETH rotation failed with %d already-created orders pending=%v: %v", len(orders), pending, err)
	}
}
