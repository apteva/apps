package main

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// fillStrategyOrder places and fills one order attributed to strategyID.
// strategyID 0 leaves the order unattributed, the way a manual or agent
// order arrives.
func fillStrategyOrder(
	t *testing.T, ctx *sdk.AppCtx, portfolioID, strategyID int64,
	id, symbol, side string, qty, mark float64,
) {
	t.Helper()
	db := ctx.AppDB()
	if err := dbUpsertMark(db, &Mark{
		Symbol: symbol, AssetClass: "crypto", Price: mark,
		MarkedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	order := &Order{
		ID: id, PortfolioID: portfolioID, Symbol: symbol, AssetClass: "crypto",
		Side: side, Type: "market", Qty: qty, TIF: "day", Status: "working",
		Rationale: "strategy attribution regression test fill", Source: "test",
	}
	if err := dbInsertOrder(db, order, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if strategyID > 0 {
		if _, err := db.Exec(`UPDATE orders SET strategy_id=?, strategy_version=1 WHERE id=?`, strategyID, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := tryFill(globalEngine, order); err != nil {
		t.Fatal(err)
	}
}

func mustCreateStrategy(t *testing.T, ctx *sdk.AppCtx, name string) int64 {
	t.Helper()
	id, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
		ProjectID: "test-proj", Name: name, Description: name,
		Status: "active", Version: 1,
		Definition: map[string]any{"kind": "fixed_weights", "weights": map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type attributionRow struct {
	openQty, avgCost, gross, fees float64
	spread, slippage              float64
	fillCount                     int
}

func readAttribution(t *testing.T, db *sql.DB, portfolioID, strategyID int64, symbol string) attributionRow {
	t.Helper()
	var r attributionRow
	err := db.QueryRow(`
		SELECT open_qty, avg_cost, gross_realized_pnl, fees_paid, spread_cost, slippage_cost, fill_count
		FROM strategy_position_accounting
		WHERE portfolio_id=? AND strategy_id=? AND symbol=? AND outcome=''`,
		portfolioID, strategyID, symbol).Scan(&r.openQty, &r.avgCost, &r.gross, &r.fees,
		&r.spread, &r.slippage, &r.fillCount)
	if err != nil {
		t.Fatalf("attribution row (pf=%d strategy=%d %s): %v", portfolioID, strategyID, symbol, err)
	}
	return r
}

func assertNear(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

// The case portfolio-level accounting cannot answer: two strategies holding
// the same symbol at different costs. Each must close against its own lots,
// never the portfolio's blended average.
func TestStrategyClosesAgainstItsOwnLotsNotThePortfolioBlend(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Shared symbol desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(ctx.AppDB(), portfolioID, map[string]any{
		"fee_bps": 0.0, "slippage_bps": 0.0,
	}); err != nil {
		t.Fatal(err)
	}
	momentum := mustCreateStrategy(t, ctx, "Momentum")
	meanRev := mustCreateStrategy(t, ctx, "Mean reversion")

	fillStrategyOrder(t, ctx, portfolioID, momentum, "own-a-buy", "BTC-USD", "buy", 10, 100)
	fillStrategyOrder(t, ctx, portfolioID, meanRev, "own-b-buy", "BTC-USD", "buy", 5, 110)
	fillStrategyOrder(t, ctx, portfolioID, momentum, "own-a-sell", "BTC-USD", "sell", 10, 120)

	// Momentum bought its 10 at 100 and sold them at 120.
	momentumRow := readAttribution(t, ctx.AppDB(), portfolioID, momentum, "BTC-USD")
	assertNear(t, "momentum realized", momentumRow.gross, 200)
	assertNear(t, "momentum open qty", momentumRow.openQty, 0)

	// The portfolio blend would have charged (10*100+5*110)/15 = 103.33 and
	// produced 166.67. That number must not appear on the strategy book.
	if math.Abs(momentumRow.gross-500.0/3.0) < 1e-3 {
		t.Fatal("momentum realized P&L used the portfolio blended average")
	}

	// Mean reversion still holds its 5 at its own cost, untouched.
	meanRevRow := readAttribution(t, ctx.AppDB(), portfolioID, meanRev, "BTC-USD")
	assertNear(t, "mean reversion open qty", meanRevRow.openQty, 5)
	assertNear(t, "mean reversion avg cost", meanRevRow.avgCost, 110)
	assertNear(t, "mean reversion realized", meanRevRow.gross, 0)
}

// Rebuilding from the append-only fills ledger must land on exactly the
// numbers incremental accrual produced, or a restart would silently restate
// P&L.
func TestStrategyAttributionRebuildMatchesIncrementalAccrual(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Rebuild desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(ctx.AppDB(), portfolioID, map[string]any{
		"fee_bps": 10.0, "slippage_bps": 25.0,
	}); err != nil {
		t.Fatal(err)
	}
	strategyID := mustCreateStrategy(t, ctx, "Rebuildable")

	fillStrategyOrder(t, ctx, portfolioID, strategyID, "rb-buy-1", "BTC-USD", "buy", 10, 100)
	fillStrategyOrder(t, ctx, portfolioID, strategyID, "rb-buy-2", "BTC-USD", "buy", 10, 120)
	fillStrategyOrder(t, ctx, portfolioID, strategyID, "rb-sell", "BTC-USD", "sell", 15, 130)

	before := readAttribution(t, ctx.AppDB(), portfolioID, strategyID, "BTC-USD")
	// Fees and slippage are on, so the exact figures depend on the venue model.
	// This test is about replay equality; exact lot arithmetic is covered by
	// TestStrategyClosesAgainstItsOwnLotsNotThePortfolioBlend at zero cost.
	assertNear(t, "incremental open qty", before.openQty, 5)
	if before.gross <= 0 {
		t.Fatalf("expected a realized gain, got %v", before.gross)
	}

	if err := dbRebuildStrategyAttribution(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	after := readAttribution(t, ctx.AppDB(), portfolioID, strategyID, "BTC-USD")
	assertNear(t, "rebuilt realized", after.gross, before.gross)
	assertNear(t, "rebuilt open qty", after.openQty, before.openQty)
	assertNear(t, "rebuilt avg cost", after.avgCost, before.avgCost)
	assertNear(t, "rebuilt fees", after.fees, before.fees)
	// Spread and slippage are written to the fills row, so incremental accrual
	// must record exactly what the replay will find. A restart that restated
	// execution cost would be indistinguishable from a real cost change.
	assertNear(t, "rebuilt spread", after.spread, before.spread)
	assertNear(t, "rebuilt slippage", after.slippage, before.slippage)
	if before.slippage <= 0 {
		t.Fatal("expected slippage cost to accrue to the strategy book")
	}
	if after.fillCount != before.fillCount {
		t.Fatalf("rebuilt fill count = %d, want %d", after.fillCount, before.fillCount)
	}
	if before.fees <= 0 {
		t.Fatal("expected fees to accrue to the strategy book")
	}

	// Rebuilding twice must not double anything.
	if err := dbRebuildStrategyAttribution(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	twice := readAttribution(t, ctx.AppDB(), portfolioID, strategyID, "BTC-USD")
	assertNear(t, "idempotent realized", twice.gross, before.gross)
	assertNear(t, "idempotent fees", twice.fees, before.fees)
}

// Manual and agent orders carry no strategy. They must land in bucket 0 so
// the strategy books plus that bucket reconcile against the portfolio book.
func TestUnattributedFillsReconcileWithPortfolioAccounting(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Mixed desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(ctx.AppDB(), portfolioID, map[string]any{
		"fee_bps": 0.0, "slippage_bps": 0.0,
	}); err != nil {
		t.Fatal(err)
	}
	strategyID := mustCreateStrategy(t, ctx, "Half the desk")

	fillStrategyOrder(t, ctx, portfolioID, strategyID, "mix-s-buy", "BTC-USD", "buy", 4, 100)
	fillStrategyOrder(t, ctx, portfolioID, 0, "mix-m-buy", "BTC-USD", "buy", 6, 100)
	fillStrategyOrder(t, ctx, portfolioID, strategyID, "mix-s-sell", "BTC-USD", "sell", 4, 150)
	fillStrategyOrder(t, ctx, portfolioID, 0, "mix-m-sell", "BTC-USD", "sell", 6, 150)

	strategyRow := readAttribution(t, ctx.AppDB(), portfolioID, strategyID, "BTC-USD")
	manualRow := readAttribution(t, ctx.AppDB(), portfolioID, 0, "BTC-USD")
	assertNear(t, "strategy realized", strategyRow.gross, 200)
	assertNear(t, "unattributed realized", manualRow.gross, 300)

	portfolioRealized, _, err := dbPortfolioAccounting(ctx.AppDB(), portfolioID)
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, "books reconcile to portfolio", strategyRow.gross+manualRow.gross, portfolioRealized)
}

// A strategy may be handed a position it did not open, or have one closed out
// from under it. It must never realize more than its own book can account for.
func TestStrategySellBeyondItsOwnBookDoesNotOverRealize(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Handover desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(ctx.AppDB(), portfolioID, map[string]any{
		"fee_bps": 0.0, "slippage_bps": 0.0,
	}); err != nil {
		t.Fatal(err)
	}
	opener := mustCreateStrategy(t, ctx, "Opener")
	closer := mustCreateStrategy(t, ctx, "Closer")

	fillStrategyOrder(t, ctx, portfolioID, opener, "ho-buy", "BTC-USD", "buy", 10, 100)
	// The closer never bought; it sells a position it does not own on its book.
	fillStrategyOrder(t, ctx, portfolioID, closer, "ho-sell", "BTC-USD", "sell", 10, 150)

	closerRow := readAttribution(t, ctx.AppDB(), portfolioID, closer, "BTC-USD")
	assertNear(t, "closer realized", closerRow.gross, 0)
	assertNear(t, "closer open qty", closerRow.openQty, 0)

	// The opener keeps its open lot; the P&L belongs to whoever opened it and
	// stays unrealized on that book rather than being invented on the closer's.
	openerRow := readAttribution(t, ctx.AppDB(), portfolioID, opener, "BTC-USD")
	assertNear(t, "opener open qty", openerRow.openQty, 10)
	assertNear(t, "opener realized", openerRow.gross, 0)
}

// The rollup the dashboard widget reads.
func TestStrategyLiveRowsReportPnLPositionsAndSharing(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Rollup desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(ctx.AppDB(), portfolioID, map[string]any{
		"fee_bps": 0.0, "slippage_bps": 0.0,
	}); err != nil {
		t.Fatal(err)
	}
	alpha := mustCreateStrategy(t, ctx, "Alpha")
	beta := mustCreateStrategy(t, ctx, "Beta")
	if _, err := dbAssignStrategy(ctx.AppDB(), &StrategyAssignment{
		ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: alpha,
		StrategyVersion: 1, ControlMode: "strategy", Status: "active", Cadence: "1d",
	}); err != nil {
		t.Fatal(err)
	}

	// Alpha realizes on BTC and keeps an open ETH lot; beta shares BTC.
	fillStrategyOrder(t, ctx, portfolioID, alpha, "lr-a-buy", "BTC-USD", "buy", 10, 100)
	fillStrategyOrder(t, ctx, portfolioID, beta, "lr-b-buy", "BTC-USD", "buy", 5, 100)
	fillStrategyOrder(t, ctx, portfolioID, alpha, "lr-a-sell", "BTC-USD", "sell", 10, 120)
	fillStrategyOrder(t, ctx, portfolioID, alpha, "lr-a-eth", "ETH-USD", "buy", 2, 50)
	if err := dbUpsertMark(ctx.AppDB(), &Mark{
		Symbol: "ETH-USD", AssetClass: "crypto", Price: 75,
		MarkedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := dbStrategyLiveRows(ctx.AppDB(), "test-proj", false)
	if err != nil {
		t.Fatal(err)
	}
	var alphaRow *StrategyLiveRow
	for _, row := range rows {
		if row.StrategyID == alpha {
			alphaRow = row
		}
		if row.StrategyID == 0 {
			t.Fatal("unattributed bucket leaked into the default rollup")
		}
	}
	if alphaRow == nil {
		t.Fatalf("no row for alpha in %d rows", len(rows))
	}
	assertNear(t, "alpha realized", alphaRow.RealizedPnL, 200)
	// One open ETH lot of 2 bought at 50, marked at 75.
	assertNear(t, "alpha unrealized", alphaRow.UnrealizedPnL, 50)
	assertNear(t, "alpha total", alphaRow.TotalPnL, 250)
	assertNear(t, "alpha market value", alphaRow.MarketValue, 150)
	if alphaRow.AssignmentStatus != "active" {
		t.Fatalf("alpha assignment status = %q, want active", alphaRow.AssignmentStatus)
	}
	if alphaRow.StrategyName != "Alpha" || alphaRow.PortfolioName != "Rollup desk" {
		t.Fatalf("alpha row naming = %q / %q", alphaRow.StrategyName, alphaRow.PortfolioName)
	}
	// BTC is shared with beta; ETH is not. The widget needs that flag to avoid
	// presenting a convention-dependent number as though it were the only one.
	if alphaRow.SharedSymbols != 1 {
		t.Fatalf("alpha shared symbols = %d, want 1", alphaRow.SharedSymbols)
	}
	if len(alphaRow.Positions) != 1 || alphaRow.Positions[0].Symbol != "ETH-USD" {
		t.Fatalf("alpha positions = %#v", alphaRow.Positions)
	}
	if alphaRow.Positions[0].Shared {
		t.Fatal("ETH-USD is held only by alpha and must not be flagged shared")
	}

	// Beta traded but was never assigned — it still owns its P&L and appears.
	var betaRow *StrategyLiveRow
	for _, row := range rows {
		if row.StrategyID == beta {
			betaRow = row
		}
	}
	if betaRow == nil {
		t.Fatal("beta traded but is missing from the rollup")
	}
	if betaRow.AssignmentStatus != "unassigned" {
		t.Fatalf("beta assignment status = %q, want unassigned", betaRow.AssignmentStatus)
	}

	withBucket, err := dbStrategyLiveRows(ctx.AppDB(), "test-proj", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withBucket) != len(rows) {
		t.Fatalf("no unattributed fills exist, so the bucket must add no rows: %d vs %d", len(withBucket), len(rows))
	}
}

// The route the widget calls, including the filters it passes.
func TestStrategiesLiveEndpointFiltersAndTotals(t *testing.T) {
	ctx := newTestCtx(t)
	globalCtx = ctx
	portfolioID := mustCreatePortfolio(t, ctx, "Endpoint desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(ctx.AppDB(), portfolioID, map[string]any{
		"fee_bps": 0.0, "slippage_bps": 0.0,
	}); err != nil {
		t.Fatal(err)
	}
	strategyID := mustCreateStrategy(t, ctx, "Endpoint strategy")
	fillStrategyOrder(t, ctx, portfolioID, strategyID, "ep-buy", "BTC-USD", "buy", 4, 100)
	fillStrategyOrder(t, ctx, portfolioID, strategyID, "ep-sell", "BTC-USD", "sell", 4, 150)
	fillStrategyOrder(t, ctx, portfolioID, 0, "ep-manual-buy", "ETH-USD", "buy", 1, 10)
	fillStrategyOrder(t, ctx, portfolioID, 0, "ep-manual-sell", "ETH-USD", "sell", 1, 20)

	get := func(query string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/strategies/live?project_id=test-proj"+query, nil)
		w := httptest.NewRecorder()
		(&App{}).handleHTTPStrategiesLive(w, req)
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	body := get("")
	rows := body["strategies"].([]any)
	if len(rows) != 1 {
		t.Fatalf("default rollup should hide the unattributed bucket, got %d rows", len(rows))
	}
	first := rows[0].(map[string]any)
	assertNear(t, "endpoint realized", first["realized_pnl"].(float64), 200)
	totals := body["totals"].(map[string]any)
	assertNear(t, "endpoint totals", totals["realized_pnl"].(float64), 200)

	withBucket := get("&include_unattributed=true")
	if len(withBucket["strategies"].([]any)) != 2 {
		t.Fatal("include_unattributed must surface the manual bucket")
	}
	assertNear(t, "totals include manual P&L",
		withBucket["totals"].(map[string]any)["realized_pnl"].(float64), 210)

	// This desk is paper, so a live-only request must come back empty rather
	// than falling back to showing simulated money as though it were real.
	liveOnly := get("&scope=live")
	if len(liveOnly["strategies"].([]any)) != 0 {
		t.Fatal("scope=live must exclude simulated portfolios")
	}
	assertNear(t, "live-only totals reset", liveOnly["totals"].(map[string]any)["realized_pnl"].(float64), 0)

	req := httptest.NewRequest(http.MethodPost, "/strategies/live?project_id=test-proj", nil)
	w := httptest.NewRecorder()
	(&App{}).handleHTTPStrategiesLive(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d, want 405", w.Code)
	}
}
