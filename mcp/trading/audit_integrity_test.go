package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"math"
	"sync"
	"testing"
	"time"
)

func TestIntegrityOrderRetryIsDurableAndRejectsChangedIntent(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Retry", []string{"crypto"})
	args := map[string]any{"portfolio_id": id, "symbol": "BTC-USD", "side": "buy", "type": "market", "qty": 0.01, "rationale": "Buy a small position to exercise durable retry handling", "idempotency_key": "retry-1"}
	var wg sync.WaitGroup
	results := make(chan string, 16)
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := (&App{}).toolOrderPlace(ctx, args)
			if err != nil {
				errs <- err
				return
			}
			results <- fmt.Sprint(out.(map[string]any)["order_id"])
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	oid := ""
	for result := range results {
		if oid != "" && oid != result {
			t.Fatalf("duplicate request created %s and %s", oid, result)
		}
		oid = result
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM orders WHERE portfolio_id=?`, id).Scan(&count)
	if count != 1 {
		t.Fatalf("orders=%d", count)
	}
	args["qty"] = 0.02
	if _, err := (&App{}).toolOrderPlace(ctx, args); err == nil {
		t.Fatal("changed intent reused key")
	}
	if out, err := previousOrderRequest(ctx.AppDB(), "test-proj", id, "retry-1", orderIntentHash(map[string]any{"symbol": "BTC-USD", "side": "buy", "type": "market", "qty": 0.01})); err != nil || out["order_id"] != oid {
		t.Fatalf("persisted retry: %v %v", out, err)
	}
}

func TestIntegrityPartialFillConservationAndDuplicateUpdates(t *testing.T) {
	ctx := newTestCtx(t)
	ctx.AppDB().SetMaxOpenConns(1)
	id := mustCreatePortfolio(t, ctx, "Fill sequences", []string{"crypto"})
	pf := mustPortfolio(t, ctx, id)
	o := auditOrder(t, ctx, id, "conservation", "buy", 10)
	notional := 0.0
	for i := 1; i <= 10; i++ {
		price := float64(80 + i*17)
		notional += price
		br := &brokerOrderResult{Status: "working", ExecutedQty: float64(i), CummulativeQuoteQty: notional}
		if i == 10 {
			br.Status = "filled"
		}
		old := *o
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				copy := old
				_, err := applyBrokerProgress(ctx.AppDB(), "test-proj", pf, &copy, br)
				if err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		o, _ = dbGetOrder(ctx.AppDB(), "test-proj", o.ID)
	}
	pos, _ := dbGetPosition(ctx.AppDB(), id, "BTC-USD", "")
	cash := mustPortfolio(t, ctx, id).Cash
	if math.Abs(cash-(100000-notional)) > 1e-8 || math.Abs(pos.AvgCost-notional/10) > 1e-8 || pos.Qty != 10 {
		t.Fatalf("cash=%v pos=%+v", cash, pos)
	}
	var fills int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM fills WHERE order_id=?`, o.ID).Scan(&fills)
	if fills != 10 {
		t.Fatalf("duplicate fills: %d", fills)
	}
}

func TestIntegrityCancelAcknowledgementKeepsOrderSupervised(t *testing.T) {
	pending := true
	platform := &hardeningPlatform{connections: []sdk.PlatformConnection{{ID: 7, AppSlug: "binance-trading", Status: "active"}}, execute: func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if pending {
			return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"orderId":10,"status":"NEW","executedQty":"0"}`)}, nil
		}
		return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"orderId":10,"status":"FILLED","executedQty":"1","cummulativeQuoteQty":"100"}`)}, nil
	}}
	ctx := newHardeningCtx(t, platform)
	id, _ := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "Cancel", Mode: "live", BrokerSlug: "binance-trading", AllowedClasses: []string{"crypto"}, StartingCash: 1000})
	o := auditOrder(t, ctx, id, "cancel-pending", "buy", 1)
	if _, err := (&App{}).toolOrderCancel(ctx, map[string]any{"order_id": o.ID}); err != nil {
		t.Fatal(err)
	}
	stored, _ := dbGetOrder(ctx.AppDB(), "test-proj", o.ID)
	if stored.Status != "working" {
		t.Fatalf("accepted cancel became terminal: %s", stored.Status)
	}
	pending = false
	if err := tryReconcile(globalEngine, mustPortfolio(t, ctx, id), stored); err != nil {
		t.Fatal(err)
	}
	stored, _ = dbGetOrder(ctx.AppDB(), "test-proj", o.ID)
	if stored.Status != "filled" || stored.FilledQty != 1 {
		t.Fatalf("late fill lost: %+v", stored)
	}
}

func TestIntegrityBrokerBindingCannotRebindOrDuplicateAccount(t *testing.T) {
	platform := &hardeningPlatform{connections: []sdk.PlatformConnection{{ID: 7, AppSlug: "binance-trading", Status: "active"}}}
	ctx := newHardeningCtx(t, platform)
	id, _ := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "Bound", Mode: "live", BrokerSlug: "binance-trading", StartingCash: 1000})
	pf := mustPortfolio(t, ctx, id)
	if _, err := brokerFor(ctx, pf); err != nil {
		t.Fatal(err)
	}
	second, _ := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "Duplicate", Mode: "live", BrokerSlug: "binance-trading", StartingCash: 1000})
	if _, err := brokerFor(ctx, mustPortfolio(t, ctx, second)); err == nil {
		t.Fatal("duplicated whole broker account")
	}
	platform.connections = []sdk.PlatformConnection{{ID: 8, AppSlug: "binance-trading", Status: "active"}}
	if _, err := brokerFor(ctx, pf); err == nil {
		t.Fatal("silently rebound to another account")
	}
}

func TestIntegrityAccountSnapshotRejectsConcurrentEconomicChanges(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Snapshot", []string{"crypto"})
	pf := mustPortfolio(t, ctx, id)
	revision, _ := accountRevision(ctx.AppDB(), id)
	if err := dbInsertPositionRaw(ctx.AppDB(), "test-proj", id, "BTC-USD", "crypto", "", 1, 100); err != nil {
		t.Fatal(err)
	}
	err := applyAccountSnapshot(ctx.AppDB(), pf, &brokerAccount{QuoteCash: 123, QuoteAvailable: 123, Holdings: map[string]brokerBalance{}}, true, revision)
	if err == nil {
		t.Fatal("overwrote concurrent position")
	}
	if got := mustPortfolio(t, ctx, id).Cash; got != 100000 {
		t.Fatalf("cash partly written: %v", got)
	}
	revision, _ = accountRevision(ctx.AppDB(), id)
	_, err = ctx.AppDB().Exec(`CREATE TRIGGER reject_snapshot BEFORE UPDATE OF qty ON positions BEGIN SELECT RAISE(ABORT,'injected snapshot failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	err = applyAccountSnapshot(ctx.AppDB(), pf, &brokerAccount{QuoteCash: 123, QuoteAvailable: 123, Holdings: map[string]brokerBalance{"BTC-USD": {Total: 2, AvgCost: 200}}}, true, revision)
	if err == nil {
		t.Fatal("expected injected failure")
	}
	if got := mustPortfolio(t, ctx, id).Cash; got != 100000 {
		t.Fatalf("failed snapshot partly updated cash: %v", got)
	}
}

func TestIntegrityInternalOrdersIncludeMoreThan200(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Many orders", []string{"crypto"})
	for i := 0; i < 251; i++ {
		auditOrder(t, ctx, id, fmt.Sprintf("many-%d", i), "buy", 0.001)
	}
	orders, err := workingPortfolioOrders(ctx.AppDB(), id)
	if err != nil || len(orders) != 251 {
		t.Fatalf("orders=%d err=%v", len(orders), err)
	}
}

func TestIntegrityImmediateOrdersNeverLinger(t *testing.T) {
	for _, tif := range []string{"ioc", "fok"} {
		t.Run(tif, func(t *testing.T) {
			ctx := newTestCtx(t)
			id := mustCreatePortfolio(t, ctx, tif, []string{"crypto"})
			o := auditOrder(t, ctx, id, tif, "buy", 1)
			o.Type = "limit"
			o.TIF = tif
			o.LimitPrice = ptr(1.0)
			ctx.AppDB().Exec(`UPDATE orders SET type='limit',tif=?,limit_price=1 WHERE id=?`, tif, o.ID)
			if err := tryFill(globalEngine, o); err != nil {
				t.Fatal(err)
			}
			o, _ = dbGetOrder(ctx.AppDB(), "test-proj", o.ID)
			if o.Status != "cancelled" {
				t.Fatalf("immediate order status=%s", o.Status)
			}
		})
	}
}

func TestIntegrityNonFiniteAndUnsupportedOrdersRejected(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Validation", []string{"crypto"})
	for _, patch := range []map[string]any{{"qty": "NaN"}, {"qty": "Inf"}, {"type": "trailing_stop"}, {"tif": "forever"}, {"limit_price": "NaN"}} {
		args := map[string]any{"portfolio_id": id, "symbol": "BTC-USD", "side": "buy", "type": "market", "qty": 0.01, "rationale": "This should fail common order validation before any broker operation"}
		for k, v := range patch {
			args[k] = v
		}
		out, err := (&App{}).toolOrderPlace(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		if out.(map[string]any)["status"] != "rejected" {
			t.Fatalf("accepted invalid %v: %v", patch, out)
		}
	}
}

func TestIntegrityRebalanceResumesAfterSellSettlement(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Rotate", []string{"crypto"})
	mark, _ := dbGetMark(ctx.AppDB(), "BTC-USD")
	dbInsertPositionRaw(ctx.AppDB(), "test-proj", id, "BTC-USD", "crypto", "", 1, mark.Price)
	ctx.AppDB().Exec(`UPDATE portfolios SET cash=0 WHERE id=?`, id)
	sid := mustCreateTestStrategy(t, ctx, "BTC-USD", "ETH-USD")
	s, _ := dbGetStrategy(ctx.AppDB(), "test-proj", sid)
	a := &StrategyAssignment{ID: 1}
	eval := &StrategyEvaluation{TargetAllocations: []StrategyAllocation{{Symbol: "ETH-USD", Weight: 1}}}
	sells, pending, err := placeStrategyPaperOrders(globalEngine, ctx, mustPortfolio(t, ctx, id), s, a, eval)
	if err != nil || !pending || len(sells) != 1 || sells[0].Side != "sell" {
		t.Fatalf("sell phase %v %v %v", sells, pending, err)
	}
	for _, o := range sells {
		if err := tryFill(globalEngine, o); err != nil {
			t.Fatal(err)
		}
	}
	buys, _, err := placeStrategyPaperOrders(globalEngine, ctx, mustPortfolio(t, ctx, id), s, a, eval)
	if err != nil || len(buys) != 1 || buys[0].Symbol != "ETH-USD" {
		t.Fatalf("buy phase %v %v", buys, err)
	}
	for _, o := range buys {
		if err := tryFill(globalEngine, o); err != nil {
			t.Fatal(err)
		}
	}
	if pos, _ := dbGetPosition(ctx.AppDB(), id, "ETH-USD", ""); pos == nil || pos.Qty <= 0 {
		t.Fatal("rotation failed to establish replacement")
	}
}

func TestIntegrityBacktestPauseCannotCommitSnapshot(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Replay", []string{"crypto"})
	rid, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{ProjectID: "test-proj", PortfolioID: id, Name: "Replay", Symbols: []string{"BTC-USD"}, Status: "paused", StartingCash: 100000, TotalSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := commitStrategyStep(ctx.AppDB(), rid, 1, map[string]any{}, "running", &BacktestSnapshot{RunID: rid, Step: 1, Equity: 100000}); err == nil {
		t.Fatal("paused run committed")
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM backtest_snapshots WHERE run_id=?`, rid).Scan(&count)
	if count != 0 {
		t.Fatal("snapshot leaked from failed step")
	}
}

func TestIntegrityCorporateActionsUseEffectiveDateHoldings(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Actions", []string{"equity"})
	dbInsertPositionRaw(ctx.AppDB(), "test-proj", id, "AAPL", "equity", "", 10, 100)
	pf := mustPortfolio(t, ctx, id)
	split := testCorporateAction("late-split", "forward_split", "AAPL")
	split.EffectiveDate = "2020-01-01"
	split.RatioNumerator = 2
	split.RatioDenominator = 1
	if err := applySplit(ctx.AppDB(), pf, split); err != nil {
		t.Fatal(err)
	}
	pos, _ := dbGetPosition(ctx.AppDB(), id, "AAPL", "")
	if pos.Qty != 10 {
		t.Fatal("post-split acquisition split again")
	}
	dividend := testCorporateAction("intraday-div", "cash_dividend", "AAPL")
	dividend.ExDate = time.Now().UTC().Format("2006-01-02")
	dividend.CashAmount = 1
	dividend.Currency = "USD"
	if err := applyCashDistribution(ctx.AppDB(), pf, dividend, dividend.ExDate); err != nil {
		t.Fatal(err)
	}
	if mustPortfolio(t, ctx, id).Cash != 100000 {
		t.Fatal("intraday buyer received dividend")
	}
}

func TestIntegrityObjectiveFreezesBeforeDeadline(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Objective", []string{"crypto"})
	pf := mustPortfolio(t, ctx, id)
	obj, err := dbCreatePortfolioObjective(ctx.AppDB(), pf, PortfolioObjective{Name: "Target", Metric: "period_return_pct", Direction: "at_least", Status: "active", TargetPct: 10, StartsAt: time.Now().UTC().Format(time.RFC3339), DeadlineAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, 100000)
	if err != nil {
		t.Fatal(err)
	}
	objectiveProgress(ctx.AppDB(), pf, obj, &Portfolio{Equity: 105000})
	ctx.AppDB().Exec(`UPDATE objective_observations SET observed_at=? WHERE objective_id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), obj.ID)
	obj.DeadlineAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
	objectiveProgress(ctx.AppDB(), pf, obj, &Portfolio{Equity: 120000})
	if obj.Achieved || obj.ActualPct == nil || math.Abs(*obj.ActualPct-5) > 1e-8 {
		t.Fatalf("hindsight changed result %+v", obj)
	}
	objectiveProgress(ctx.AppDB(), pf, obj, &Portfolio{Equity: 500000})
	if obj.Achieved {
		t.Fatal("final result changed")
	}
}

func TestIntegrityReplayPolicyRejectsExcludedSymbolsAndExcessExposure(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Replay rules", []string{"crypto"})
	pf := mustPortfolio(t, ctx, id)
	policy := defaultUniversePolicy(pf)
	policy.ExcludeSymbols = []string{"BTC-USD"}
	dbUpsertPortfolioUniversePolicy(ctx.AppDB(), pf, *policy)
	run := &BacktestRun{ProjectID: "test-proj", PortfolioID: id, Name: "policy", Symbols: []string{"BTC-USD"}, StartingCash: 100000}
	if _, err := dbCreateBacktestRun(ctx.AppDB(), run); err != nil {
		t.Fatal(err)
	}
	state := &strategyBacktestState{Cash: 100000, Positions: map[string]*Position{}}
	orders := applyStrategyTargets(run, state, []StrategyAllocation{{Symbol: "BTC-USD", Weight: 1}}, []map[string]any{{"symbol": "BTC-USD", "price": 100.0}})
	if len(orders) != 0 || state.Cash != 100000 {
		t.Fatal("replay bypassed universe")
	}
}

func TestIntegrityReplayToolRequiresTrustedAppCaller(t *testing.T) {
	ctx := newTestCtx(t)
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name != "backtest_market_step" {
			continue
		}
		if tool.Exposure != sdk.ToolExposureAppOnly {
			t.Fatal("replay exposed to agent")
		}
		if _, err := tool.HandlerCtx(context.Background(), ctx, map[string]any{}); err == nil {
			t.Fatal("accepted missing caller")
		}
		return
	}
	t.Fatal("tool missing")
}

func TestIntegrityExecutionCostTotalsIgnorePageSize(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Costs", []string{"crypto"})
	for i := 0; i < 110; i++ {
		_, err := ctx.AppDB().Exec(`INSERT INTO execution_costs(project_id,portfolio_id,venue_slug,symbol,kind,amount,currency,provider_event_id) VALUES('test-proj',?,'simulation','BTC-USD','funding',1,'USD',?)`, id, fmt.Sprintf("f-%d", i))
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := (&App{}).toolExecutionCostsList(ctx, map[string]any{"portfolio_id": id, "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["totals"].(map[string]float64)["funding"] != 110 {
		t.Fatalf("page-dependent totals: %v", out)
	}
	_, err = (&App{}).toolFundingPaymentRecord(ctx, map[string]any{"portfolio_id": id, "symbol": "BTC-USD", "provider_event_id": "bad-funding", "amount": 1, "currency": "BTC"})
	if err == nil {
		t.Fatal("BTC funding treated as dollars")
	}
}

func TestIntegrityAccountSnapshotDoesNotTouchWorkingOrders(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Pending", []string{"crypto"})
	auditOrder(t, ctx, id, "await-fill", "buy", 1)
	revision, _ := accountRevision(ctx.AppDB(), id)
	if err := applyAccountSnapshot(ctx.AppDB(), mustPortfolio(t, ctx, id), &brokerAccount{QuoteCash: 1, QuoteAvailable: 1}, true, revision); err != nil {
		t.Fatal(err)
	}
	if mustPortfolio(t, ctx, id).Cash != 100000 {
		t.Fatal("snapshot imported pending fills prematurely")
	}
}

func TestIntegrityBrokerFeeCurrenciesConserveBalances(t *testing.T) {
	for _, currency := range []string{"USD", "BTC"} {
		t.Run(currency, func(t *testing.T) {
			ctx := newTestCtx(t)
			id := mustCreatePortfolio(t, ctx, "Fees", []string{"crypto"})
			pf := mustPortfolio(t, ctx, id)
			o := auditOrder(t, ctx, id, "fee-currency", "buy", 2)
			for n := 1; n <= 2; n++ {
				br := &brokerOrderResult{Status: "working", ExecutedQty: float64(n), CummulativeQuoteQty: float64(n) * 100, Fills: []brokerFill{{Qty: float64(n), Price: 100, Commission: float64(n) * 0.01, CommissionAsset: currency}}}
				if _, err := applyBrokerProgress(ctx.AppDB(), "test-proj", pf, o, br); err != nil {
					t.Fatal(err)
				}
			}
			pos, _ := dbGetPosition(ctx.AppDB(), id, "BTC-USD", "")
			cash := mustPortfolio(t, ctx, id).Cash
			if currency == "USD" {
				if math.Abs(cash-99799.98) > 1e-8 || pos.Qty != 2 {
					t.Fatalf("quote fee: cash=%v qty=%v", cash, pos.Qty)
				}
			} else {
				if math.Abs(cash-99800) > 1e-8 || math.Abs(pos.Qty-1.98) > 1e-8 {
					t.Fatalf("base fee: cash=%v qty=%v", cash, pos.Qty)
				}
			}
		})
	}
}

func TestIntegrityPendingBuyRechecksChangedExposureLimit(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Changed risk", []string{"crypto"})
	m, _ := dbGetMark(ctx.AppDB(), "BTC-USD")
	o := auditOrder(t, ctx, id, "changed-risk", "buy", 50000/m.Price)
	if _, err := dbUpsertPortfolioRiskPolicy(ctx.AppDB(), mustPortfolio(t, ctx, id), riskPresets["conservative"]); err != nil {
		t.Fatal(err)
	}
	if err := tryFill(globalEngine, o); err != nil {
		t.Fatal(err)
	}
	o, _ = dbGetOrder(ctx.AppDB(), "test-proj", o.ID)
	if o.Status == "filled" {
		t.Fatal("pending order bypassed tighter policy")
	}
}

func TestIntegrityLegacyUnknownBindingRequiresConfirmation(t *testing.T) {
	platform := &hardeningPlatform{connections: []sdk.PlatformConnection{{ID: 7, AppSlug: "binance-trading", Status: "active"}}}
	ctx := newHardeningCtx(t, platform)
	id, _ := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "Legacy", Mode: "live", BrokerSlug: "binance-trading", StartingCash: 1000})
	ctx.AppDB().Exec(`UPDATE portfolios SET broker_binding_required=1 WHERE id=?`, id)
	if _, err := brokerFor(ctx, mustPortfolio(t, ctx, id)); err == nil {
		t.Fatal("unidentified legacy account silently rebound")
	}
	_, err := (&App{}).toolPortfolioBrokerBind(ctx, map[string]any{"portfolio_id": id, "connection_id": 7, "confirmation": "BIND BROKER ACCOUNT"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokerFor(ctx, mustPortfolio(t, ctx, id)); err != nil {
		t.Fatal(err)
	}
}

type integrityEnvironmentPlatform struct {
	*hardeningPlatform
	host string
}

func (p *integrityEnvironmentPlatform) GetConnectionPublicConfig(id int64) (*sdk.ConnectionPublicConfig, error) {
	return &sdk.ConnectionPublicConfig{Fields: map[string]string{"host": p.host}}, nil
}
func TestIntegrityBrokerEnvironmentCheckedAgainOnEveryResolution(t *testing.T) {
	p := &integrityEnvironmentPlatform{hardeningPlatform: &hardeningPlatform{connections: []sdk.PlatformConnection{{ID: 7, AppSlug: "alpaca-trading", Status: "active"}}}, host: "paper-api.alpaca.markets"}
	ctx := newHardeningCtx(t, p)
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "Alpaca paper", Mode: "live", ExecutionEnvironment: "broker_paper", BrokerSlug: "alpaca-trading", StartingCash: 1000})
	if err != nil {
		t.Fatal(err)
	}
	pf := mustPortfolio(t, ctx, id)
	if _, err := brokerFor(ctx, pf); err != nil {
		t.Fatal(err)
	}
	p.host = "api.alpaca.markets"
	if _, err := brokerFor(ctx, pf); err == nil {
		t.Fatal("same connection changed from paper to live undetected")
	}
}

func TestIntegrityReplayIsAtomicSequentialAndUsesReplayClock(t *testing.T) {
	t.Setenv("APTEVA_ENVIRONMENT_ID", "test-replay")
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Replay", []string{"crypto"})
	dbUpdatePortfolioConfig(ctx.AppDB(), id, map[string]any{"pricing_mode": "backtest", "source_override": "backtest"})
	args := map[string]any{"portfolio_id": id, "run_id": 1, "step": 1, "replay_at": "2020-01-01T10:00:00Z", "prices": []any{map[string]any{"symbol": "BTC-USD", "price": 100.0}}}
	if _, err := (&App{}).toolBacktestMarketStep(ctx, args); err != nil {
		t.Fatal(err)
	}
	if got := executionTime(ctx.AppDB(), id).Year(); got != 2020 {
		t.Fatalf("clock=%d", got)
	}
	if _, err := (&App{}).toolBacktestMarketStep(ctx, args); err != nil {
		t.Fatal(err)
	}
	args["step"] = 3
	args["replay_at"] = "2020-01-03T10:00:00Z"
	if _, err := (&App{}).toolBacktestMarketStep(ctx, args); err == nil {
		t.Fatal("accepted skipped replay step")
	}
	args["step"] = 2
	args["replay_at"] = "2020-01-02T10:00:00Z"
	args["prices"] = []any{map[string]any{"symbol": "BTC-USD", "price": 200.0}, map[string]any{"symbol": "ETH-USD", "price": -1.0}}
	if _, err := (&App{}).toolBacktestMarketStep(ctx, args); err == nil {
		t.Fatal("invalid row accepted")
	}
	m, _ := dbGetMark(ctx.AppDB(), "BTC-USD")
	if m.Price != 100 {
		t.Fatal("invalid replay partially wrote marks")
	}
}

func TestIntegrityPositionHistoryClosesRenamedSymbols(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Rename history", []string{"equity"})
	db := ctx.AppDB()
	if _, err := db.Exec(`INSERT INTO positions(project_id,portfolio_id,symbol,asset_class,qty,avg_cost) VALUES('test-proj',?,'OLD','equity',10,100)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE positions SET symbol='NEW' WHERE portfolio_id=?`, id); err != nil {
		t.Fatal(err)
	}
	tomorrow := time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02")
	old, err := historicalQuantity(db, id, "OLD", tomorrow)
	if err != nil || old != 0 {
		t.Fatalf("old ownership=%v err=%v", old, err)
	}
	newQty, err := historicalQuantity(db, id, "NEW", tomorrow)
	if err != nil || newQty != 10 {
		t.Fatalf("new ownership=%v err=%v", newQty, err)
	}
}

func TestIntegrityUnchangedSnapshotDoesNotGrowHistory(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Unchanged snapshot", []string{"crypto"})
	db := ctx.AppDB()
	pf := mustPortfolio(t, ctx, id)
	acct := &brokerAccount{QuoteCash: 100000, QuoteAvailable: 100000, Holdings: map[string]brokerBalance{"BTC-USD": {Total: 2, AvgCost: 100}}}
	rev, _ := accountRevision(db, id)
	if err := applyAccountSnapshot(db, pf, acct, true, rev); err != nil {
		t.Fatal(err)
	}
	var before, after int
	db.QueryRow(`SELECT COUNT(*) FROM position_history WHERE portfolio_id=?`, id).Scan(&before)
	for i := 0; i < 10; i++ {
		rev, _ = accountRevision(db, id)
		if err := applyAccountSnapshot(db, pf, acct, true, rev); err != nil {
			t.Fatal(err)
		}
	}
	db.QueryRow(`SELECT COUNT(*) FROM position_history WHERE portfolio_id=?`, id).Scan(&after)
	if before != after {
		t.Fatalf("unchanged history grew %d -> %d", before, after)
	}
	var notes int
	db.QueryRow(`SELECT COUNT(*) FROM journal WHERE portfolio_id=? AND body='Applied broker account snapshot atomically'`, id).Scan(&notes)
	if notes != 1 {
		t.Fatalf("snapshot journal spam: %d", notes)
	}
}

func TestIntegrityDelayedBrokerCommissionIsIdempotent(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	db.SetMaxOpenConns(1)
	id := mustCreatePortfolio(t, ctx, "Late fees", []string{"crypto"})
	pf := mustPortfolio(t, ctx, id)
	o := auditOrder(t, ctx, id, "late-fees", "buy", 2)
	br := &brokerOrderResult{Status: "filled", ExecutedQty: 2, CummulativeQuoteQty: 200}
	if _, err := applyBrokerProgress(db, "test-proj", pf, o, br); err != nil {
		t.Fatal(err)
	}
	br.Fills = []brokerFill{{Qty: 2, Price: 100, Commission: 1.25, CommissionAsset: "USD"}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copy := *o
			_, err := applyBrokerProgress(db, "test-proj", pf, &copy, br)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if cash := mustPortfolio(t, ctx, id).Cash; math.Abs(cash-99798.75) > 1e-8 {
		t.Fatalf("cash %v", cash)
	}
	var fee float64
	var fills int
	db.QueryRow(`SELECT SUM(fee),COUNT(*) FROM fills WHERE order_id=?`, o.ID).Scan(&fee, &fills)
	if fee != 1.25 || fills != 1 {
		t.Fatalf("fee=%v fills=%d", fee, fills)
	}
}

func TestIntegrityShortPositionsCannotBecomeEmptySnapshots(t *testing.T) {
	for _, raw := range []string{`[{"symbol":"AAPL","qty":"-2","side":"short"}]`, `[{"symbol":"AAPL","qty":"2","side":"short"}]`} {
		if _, err := alpacaParsePositions(json.RawMessage(raw)); err == nil {
			t.Fatal("short silently dropped")
		}
	}
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Short snapshot", []string{"crypto"})
	db := ctx.AppDB()
	rev, _ := accountRevision(db, id)
	if err := applyAccountSnapshot(db, mustPortfolio(t, ctx, id), &brokerAccount{QuoteCash: 1, Holdings: map[string]brokerBalance{"BTC-USD": {Total: -1}}}, true, rev); err == nil {
		t.Fatal("negative total accepted")
	}
	if mustPortfolio(t, ctx, id).Cash != 100000 {
		t.Fatal("invalid snapshot partially applied")
	}
}

func TestIntegrityMissingHistoricalObjectiveBaselineIsNotInvented(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Historical objective", []string{"crypto"})
	pf := mustPortfolio(t, ctx, id)
	obj := PortfolioObjective{Name: "Historical", Metric: "period_return_pct", Direction: "at_least", Status: "active", TargetPct: 10, StartsAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	if _, err := dbCreatePortfolioObjective(ctx.AppDB(), pf, obj, 100000); err == nil {
		t.Fatal("backdated baseline fabricated")
	}
	obj.StartsAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	created, err := dbCreatePortfolioObjective(ctx.AppDB(), pf, obj, 100000)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a service outage spanning the start of the period.
	if err := initializeDueObjectiveBaselines(ctx.AppDB(), pf, 150000, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	stored, err := dbGetPortfolioObjective(ctx.AppDB(), pf, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BaselineEquity != nil {
		t.Fatal("late observation became historical baseline")
	}
}
