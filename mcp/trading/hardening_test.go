package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type hardeningPlatform struct {
	sdk.PlatformClient
	connections []sdk.PlatformConnection
	execute     func(int64, string, map[string]any) (*sdk.ExecuteResult, error)
}

func (p *hardeningPlatform) ListConnections(filter sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return p.connections, nil
}

func (p *hardeningPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if p.execute != nil {
		return p.execute(id, tool, input)
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}, nil
}

func (p *hardeningPlatform) CallEnvironmentAppResult(string, string, string, map[string]any, any) error {
	return nil
}

func (p *hardeningPlatform) SendEvent(int64, string) error { return nil }

func newHardeningCtx(t *testing.T, platform sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform))
	globalCtx = ctx
	globalEngine = &engine{db: ctx.AppDB(), provider: newMockProvider(), logger: ctx.Logger(), platform: platform}
	for _, mark := range globalEngine.provider.Universe() {
		if err := dbUpsertMark(ctx.AppDB(), mark); err != nil {
			t.Fatal(err)
		}
	}
	return ctx
}

func TestBinanceAccountIncludesLockedBalances(t *testing.T) {
	acct, err := parseBinanceAccount(json.RawMessage(`{"balances":[{"asset":"USDT","free":"50","locked":"50"},{"asset":"BTC","free":"0.4","locked":"0.1"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if acct.QuoteCash != 100 || acct.QuoteAvailable != 50 {
		t.Fatalf("cash total=%v available=%v", acct.QuoteCash, acct.QuoteAvailable)
	}
	if got := brokerBalanceTotal(acct.Holdings["BTC-USD"]); got != 0.5 {
		t.Fatalf("BTC total=%v, want 0.5", got)
	}
}

func TestLiveReconcilePreservesLockedCashAndReducesPositions(t *testing.T) {
	platform := &hardeningPlatform{
		connections: []sdk.PlatformConnection{{ID: 7, AppSlug: "binance-trading", Status: "active"}},
		execute: func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
			if tool != "get_account" {
				t.Fatalf("unexpected tool %q", tool)
			}
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"balances":[{"asset":"USDT","free":"50000","locked":"50000"},{"asset":"BTC","free":"0.4","locked":"0.1"}]}`)}, nil
		},
	}
	ctx := newHardeningCtx(t, platform)
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "live", AllowedClasses: []string{"crypto"}, StartingCash: 100000, Mode: "live", BrokerSlug: "binance-trading"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbInsertPositionRaw(ctx.AppDB(), "test-proj", id, "BTC-USD", "crypto", "", 1, 60000); err != nil {
		t.Fatal(err)
	}
	reconcileLiveAccounts(globalEngine)
	pf, err := dbGetPortfolio(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Cash != 100000 || pf.AvailableCash == nil || *pf.AvailableCash != 50000 {
		t.Fatalf("portfolio cash=%v available=%v", pf.Cash, pf.AvailableCash)
	}
	pos, err := dbGetPosition(ctx.AppDB(), id, "BTC-USD", "")
	if err != nil || pos == nil || pos.Qty != 0.5 {
		t.Fatalf("reconciled position=%+v err=%v", pos, err)
	}
	snap, err := snapshotPortfolio(ctx.AppDB(), pf)
	if err != nil {
		t.Fatal(err)
	}
	if snap.BuyingPower != 50000 {
		t.Fatalf("buying power=%v, want available cash", snap.BuyingPower)
	}
}

func TestFailedLiveCancelKeepsOrderWorking(t *testing.T) {
	platform := &hardeningPlatform{
		connections: []sdk.PlatformConnection{{ID: 8, AppSlug: "binance-trading", Status: "active"}},
		execute: func(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
			return &sdk.ExecuteResult{Success: false, Status: 503, Data: json.RawMessage(`{"code":"down","msg":"temporary"}`)}, nil
		},
	}
	ctx := newHardeningCtx(t, platform)
	pid, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "live", AllowedClasses: []string{"crypto"}, StartingCash: 1000, Mode: "live", BrokerSlug: "binance-trading"})
	if err != nil {
		t.Fatal(err)
	}
	order := &Order{ID: "cancel-me", PortfolioID: pid, Symbol: "BTC-USD", AssetClass: "crypto", Side: "buy", Type: "limit", Qty: 0.01, LimitPrice: ptr(1), TIF: "gtc", Status: "working", Rationale: "test cancellation remains supervised", Source: "test"}
	if err := dbInsertOrder(ctx.AppDB(), order, "test-proj"); err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolOrderCancel(ctx, map[string]any{"order_id": order.ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["code"] != "broker_cancel_unconfirmed" {
		t.Fatalf("cancel response=%v", out)
	}
	got, err := dbGetOrder(ctx.AppDB(), "test-proj", order.ID)
	if err != nil || got.Status != "working" {
		t.Fatalf("order status=%v err=%v", got, err)
	}
	cancelLiveWorkingOrders(globalEngine, &Portfolio{ID: pid, ProjectID: "test-proj", Mode: "live", BrokerSlug: "binance-trading"}, "test")
	got, _ = dbGetOrder(ctx.AppDB(), "test-proj", order.ID)
	if got.Status != "working" {
		t.Fatalf("halt cancel orphaned order locally: %s", got.Status)
	}
}

func TestPortfolioToolsRejectCrossProjectIDs(t *testing.T) {
	ctx := newTestCtx(t)
	otherID, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "other-proj", Name: "other", AllowedClasses: []string{"equity"}, StartingCash: 1000})
	if err != nil {
		t.Fatal(err)
	}
	order := &Order{ID: "other-order", PortfolioID: otherID, Symbol: "AAPL", AssetClass: "equity", Side: "buy", Type: "market", Qty: 1, TIF: "day", Status: "working", Rationale: "belongs to another project only", Source: "test"}
	if err := dbInsertOrder(ctx.AppDB(), order, "other-proj"); err != nil {
		t.Fatal(err)
	}
	_, _ = dbInsertJournal(ctx.AppDB(), "other-proj", otherID, "note", "private", nil)
	_, _ = dbWatchlistAdd(ctx.AppDB(), "other-proj", otherID, "AAPL")
	app := &App{}
	calls := []func() error{
		func() error { _, err := app.toolOrdersList(ctx, map[string]any{"portfolio_id": otherID}); return err },
		func() error { _, err := app.toolJournalRead(ctx, map[string]any{"portfolio_id": otherID}); return err },
		func() error {
			_, err := app.toolJournalWrite(ctx, map[string]any{"portfolio_id": otherID, "kind": "note", "body": "cross project"})
			return err
		},
		func() error {
			_, err := app.toolWatchlistRemove(ctx, map[string]any{"portfolio_id": otherID, "symbol": "AAPL"})
			return err
		},
		func() error {
			_, err := app.toolAlertCreate(ctx, map[string]any{"portfolio_id": otherID, "symbol": "AAPL", "rule": "mark_above", "threshold": 1})
			return err
		},
	}
	for i, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("cross-project call %d succeeded", i)
		}
	}
}

func TestConcurrentPaperBuysCannotOverspend(t *testing.T) {
	ctx := newTestCtx(t)
	ctx.AppDB().SetMaxOpenConns(1)
	pid, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "small", AllowedClasses: []string{"equity"}, StartingCash: 100})
	if err != nil {
		t.Fatal(err)
	}
	orders := []*Order{
		{ID: "buy-a", PortfolioID: pid, Symbol: "AAPL", AssetClass: "equity", Side: "buy", Type: "market", Qty: 0.3, TIF: "day", Status: "working", Rationale: "first concurrent allocation order", Source: "test"},
		{ID: "buy-b", PortfolioID: pid, Symbol: "AAPL", AssetClass: "equity", Side: "buy", Type: "market", Qty: 0.3, TIF: "day", Status: "working", Rationale: "second concurrent allocation order", Source: "test"},
	}
	for _, order := range orders {
		if err := dbInsertOrder(ctx.AppDB(), order, "test-proj"); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for _, order := range orders {
		order := order
		wg.Add(1)
		go func() { defer wg.Done(); _ = tryFill(globalEngine, order) }()
	}
	wg.Wait()
	rows, err := dbListOrders(ctx.AppDB(), pid, "all", 10)
	if err != nil {
		t.Fatal(err)
	}
	filled, rejected := 0, 0
	for _, order := range rows {
		if order.Status == "filled" {
			filled++
		}
		if order.Status == "rejected" {
			rejected++
		}
	}
	if filled != 1 || rejected != 1 {
		t.Fatalf("filled=%d rejected=%d orders=%+v", filled, rejected, rows)
	}
	pf, _ := dbGetPortfolio(ctx.AppDB(), "test-proj", pid)
	if pf.Cash < -1e-9 {
		t.Fatalf("cash overspent: %v", pf.Cash)
	}
}

func TestPolymarketSellClosesSelectedOutcome(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "poly", []string{"polymarket"})
	app := &App{}
	base := map[string]any{"portfolio_id": pid, "symbol": "POLY:btc-100k-2026", "type": "market", "qty": 10.0, "rationale": "opening and closing a selected prediction outcome"}
	buy := map[string]any{}
	for k, v := range base {
		buy[k] = v
	}
	buy["side"] = "yes"
	if out, err := app.toolOrderPlace(ctx, buy); err != nil || out.(map[string]any)["status"] != "working" {
		t.Fatalf("buy=%v err=%v", out, err)
	}
	if err := markTick(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	sell := map[string]any{}
	for k, v := range base {
		sell[k] = v
	}
	sell["side"] = "sell"
	sell["outcome"] = "yes"
	if out, err := app.toolOrderPlace(ctx, sell); err != nil || out.(map[string]any)["status"] != "working" {
		t.Fatalf("sell=%v err=%v", out, err)
	}
	if err := markTick(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	pos, err := dbGetPosition(ctx.AppDB(), pid, "POLY:btc-100k-2026", "YES")
	if err != nil || pos != nil {
		t.Fatalf("YES position still open: %+v err=%v", pos, err)
	}
}

func TestStrategyRotationSellsBeforeBuying(t *testing.T) {
	run := &BacktestRun{PortfolioID: 1, Symbols: []string{"BTC-USD", "ETH-USD"}, StartingCash: 1000}
	state := &strategyBacktestState{Cash: 0, Positions: map[string]*Position{"ETH-USD": {Symbol: "ETH-USD", AssetClass: "crypto", Qty: 10, AvgCost: 100}}}
	orders := applyStrategyTargets(run, state, []StrategyAllocation{{Symbol: "BTC-USD", Weight: 1}}, []map[string]any{{"symbol": "BTC-USD", "price": 100.0}, {"symbol": "ETH-USD", "price": 100.0}})
	if len(orders) != 2 || orders[0].Side != "sell" || orders[1].Side != "buy" {
		t.Fatalf("rotation order=%+v", orders)
	}
	if state.Positions["BTC-USD"] == nil || state.Positions["BTC-USD"].Qty < 9.9 {
		t.Fatalf("BTC target not funded after sale: %+v cash=%v", state.Positions, state.Cash)
	}
}

func TestValidationBarsIncludeWarmupBeforeOutOfSample(t *testing.T) {
	rows := []*BacktestMarketBar{}
	for step := 1; step <= 10; step++ {
		rows = append(rows, &BacktestMarketBar{Step: step, Symbol: "BTC-USD", C: float64(step)})
	}
	got := reindexValidationMarketBars(rows, 6, 10, 3)
	if len(got) != 8 || got[0].Step != -2 || got[3].Step != 1 {
		t.Fatalf("warmup reindex=%+v", got)
	}
}

func TestFinalAgentStepStaysRunningUntilFinalized(t *testing.T) {
	platform := &hardeningPlatform{}
	ctx := newHardeningCtx(t, platform)
	pid := mustCreatePortfolio(t, ctx, "agent", []string{"crypto"})
	runID, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, SourceAgentID: 1, Name: "one step", Status: "running", Symbols: []string{"BTC-USD"}, StartAt: "2026-01-01", EndAt: "2026-01-01", Interval: "1h", StartingCash: 1000, TotalSteps: 1, EnvironmentID: "env", EnvironmentAgentID: 2, EnvironmentPortfolioID: 3, Summary: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateBacktestEnvironment(ctx.AppDB(), runID, "env", 2, 3); err != nil {
		t.Fatal(err)
	}
	barTime := time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC)
	if err := dbReplaceBacktestMarketBars(ctx.AppDB(), runID, []*BacktestMarketBar{{RunID: runID, Step: 1, Symbol: "BTC-USD", AssetClass: "crypto", T: barTime.Unix(), O: 100, H: 100, L: 100, C: 100, Source: "test"}}); err != nil {
		t.Fatal(err)
	}
	run, _ := dbGetBacktestRun(ctx.AppDB(), "test-proj", runID)
	if _, err := stepBacktestRun(run); err != nil {
		t.Fatal(err)
	}
	defer stopBacktestRunner(runID)
	next, _ := dbGetBacktestRun(ctx.AppDB(), "test-proj", runID)
	if next.Status != "running" {
		t.Fatalf("final step completed before agent settlement: %s", next.Status)
	}
	if got := backtestReplayTime(next, 1); !got.Equal(barTime) {
		t.Fatalf("replay time=%s want captured %s", got, barTime)
	}
	stopBacktestRunner(runID)
	finalizeAgentBacktest(next)
	completed, _ := dbGetBacktestRun(ctx.AppDB(), "test-proj", runID)
	if completed.Status != "completed" {
		t.Fatalf("finalized status=%s", completed.Status)
	}
}

func TestBinanceQuoteSupportsArbitraryUSDPair(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, _ := url.QueryUnescape(r.URL.Query().Get("symbol"))
		if q != "XRPUSDT" {
			t.Fatalf("wire symbol=%q", q)
		}
		_, _ = w.Write([]byte(`{"symbol":"XRPUSDT","lastPrice":"0.50","prevClosePrice":"0.49","quoteVolume":"10"}`))
	}))
	defer server.Close()
	provider := &binancePublic{base: server.URL, client: server.Client()}
	mark, err := provider.Quote("XRP-USD")
	if err != nil || mark.Symbol != "XRP-USD" || mark.Price != 0.5 {
		t.Fatalf("mark=%+v err=%v", mark, err)
	}
}
