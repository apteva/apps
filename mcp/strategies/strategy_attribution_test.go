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
	insertFilledOrder(t, ctx, portfolioID, id, symbol, side, qty, mark,
		`UPDATE orders SET origin_app='strategies', external_strategy_id=?, external_strategy_version=1 WHERE id=? AND ?>0`, strategyID)
	if err := syncStrategyFills(ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
}

// insertFilledOrder writes and fills one order on the Trading side. tag
// stamps its origin before the fill, the way Trading records it at placement.
func insertFilledOrder(
	t *testing.T, ctx *sdk.AppCtx, portfolioID int64,
	id, symbol, side string, qty, mark float64, tag string, strategyID int64,
) {
	t.Helper()
	db := tradingDB(ctx)
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
	if _, err := db.Exec(tag, strategyID, id, strategyID); err != nil {
		t.Fatal(err)
	}
	if err := tryFill(tradingEngine(ctx), order); err != nil {
		t.Fatal(err)
	}
}

func attributionRowCount(t *testing.T, db *sql.DB, strategyID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM strategy_position_accounting WHERE strategy_id=?`, strategyID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
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
	if err := dbUpdatePortfolioConfig(tradingDB(ctx), portfolioID, map[string]any{
		"fee_bps": 0.0, "slippage_bps": 0.0,
	}); err != nil {
		t.Fatal(err)
	}
	momentum := mustCreateStrategy(t, ctx, "Momentum")
	meanRev := mustCreateStrategy(t, ctx, "Mean reversion")

	fillStrategyOrder(t, ctx, portfolioID, momentum, "own-a-buy", "BTC-USD", "buy", 10, 100)
	fillStrategyOrder(t, ctx, portfolioID, meanRev, "own-b-buy", "BTC-USD", "buy", 5, 110)
	fillStrategyOrder(t, ctx, portfolioID, momentum, "own-a-sell", "BTC-USD", "sell", 10, 120)

	momentumRow := readAttribution(t, ctx.AppDB(), portfolioID, momentum, "BTC-USD")
	assertNear(t, "momentum realized", momentumRow.gross, 200)
	assertNear(t, "momentum open qty", momentumRow.openQty, 0)

	if math.Abs(momentumRow.gross-500.0/3.0) < 1e-3 {
		t.Fatal("momentum realized P&L used the portfolio blended average")
	}

	meanRevRow := readAttribution(t, ctx.AppDB(), portfolioID, meanRev, "BTC-USD")
	assertNear(t, "mean reversion open qty", meanRevRow.openQty, 5)
	assertNear(t, "mean reversion avg cost", meanRevRow.avgCost, 110)
	assertNear(t, "mean reversion realized", meanRevRow.gross, 0)
}

// Replaying Trading's append-only fills feed from the start must land on
// exactly the numbers incremental sync produced, and syncing again (a
// restart) must fold nothing twice.
func TestStrategyFillCursorIsIdempotentAndReplays(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Rebuild desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(tradingDB(ctx), portfolioID, map[string]any{
		"fee_bps": 10.0, "slippage_bps": 25.0,
	}); err != nil {
		t.Fatal(err)
	}
	strategyID := mustCreateStrategy(t, ctx, "Rebuildable")

	fillStrategyOrder(t, ctx, portfolioID, strategyID, "rb-buy-1", "BTC-USD", "buy", 10, 100)
	fillStrategyOrder(t, ctx, portfolioID, strategyID, "rb-buy-2", "BTC-USD", "buy", 10, 120)
	fillStrategyOrder(t, ctx, portfolioID, strategyID, "rb-sell", "BTC-USD", "sell", 15, 130)

	before := readAttribution(t, ctx.AppDB(), portfolioID, strategyID, "BTC-USD")
	assertNear(t, "incremental open qty", before.openQty, 5)
	if before.gross <= 0 || before.fees <= 0 || before.slippage <= 0 {
		t.Fatalf("expected gain, fees and slippage on the strategy book: %+v", before)
	}
	if before.fillCount != 3 {
		t.Fatalf("fill count = %d, want 3", before.fillCount)
	}

	for i := 0; i < 2; i++ {
		if err := syncStrategyFills(ctx, "test-proj"); err != nil {
			t.Fatal(err)
		}
	}
	again := readAttribution(t, ctx.AppDB(), portfolioID, strategyID, "BTC-USD")
	if again != before {
		t.Fatalf("repeat sync changed the book: %+v -> %+v", before, again)
	}

	if err := resetStrategyFills(ctx.AppDB(), "test-proj"); err != nil {
		t.Fatal(err)
	}
	if err := syncStrategyFills(ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	after := readAttribution(t, ctx.AppDB(), portfolioID, strategyID, "BTC-USD")
	assertNear(t, "replayed realized", after.gross, before.gross)
	assertNear(t, "replayed open qty", after.openQty, before.openQty)
	assertNear(t, "replayed avg cost", after.avgCost, before.avgCost)
	assertNear(t, "replayed fees", after.fees, before.fees)
	assertNear(t, "replayed spread", after.spread, before.spread)
	assertNear(t, "replayed slippage", after.slippage, before.slippage)
	if after.fillCount != before.fillCount {
		t.Fatalf("replayed fill count = %d, want %d", after.fillCount, before.fillCount)
	}
}

// Manual and agent orders are Trading's alone: Strategies never sees them.
func TestManualFillsStayOutOfStrategyBooks(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Mixed desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(tradingDB(ctx), portfolioID, map[string]any{
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
	assertNear(t, "strategy realized", strategyRow.gross, 200)
	if strategyRow.fillCount != 2 {
		t.Fatalf("strategy fill count = %d, want 2", strategyRow.fillCount)
	}
	if n := attributionRowCount(t, ctx.AppDB(), 0); n != 0 {
		t.Fatalf("manual fills created %d strategy rows", n)
	}
}

// Trading-era strategy orders carry strategy_id but no origin_app. After a
// legacy import they still belong to the imported strategy's book.
func TestLegacyStrategyFillsCountOnlyAfterImport(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Legacy desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(tradingDB(ctx), portfolioID, map[string]any{
		"fee_bps": 0.0, "slippage_bps": 0.0,
	}); err != nil {
		t.Fatal(err)
	}
	strategyID := mustCreateStrategy(t, ctx, "Imported")
	// Trading now refuses to fill an order of this shape, so the history is
	// stamped after the fills, as it sits in a pre-split install.
	none := `SELECT ?, ?, ?`
	insertFilledOrder(t, ctx, portfolioID, "legacy-buy", "BTC-USD", "buy", 2, 100, none, strategyID)
	insertFilledOrder(t, ctx, portfolioID, "legacy-sell", "BTC-USD", "sell", 2, 130, none, strategyID)
	if _, err := tradingDB(ctx).Exec(`UPDATE orders SET origin_app='', strategy_id=?, strategy_version=1 WHERE id IN ('legacy-buy','legacy-sell')`, strategyID); err != nil {
		t.Fatal(err)
	}

	if err := syncStrategyFills(ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if n := attributionRowCount(t, ctx.AppDB(), strategyID); n != 0 {
		t.Fatalf("legacy fills counted without an import: %d rows", n)
	}

	if _, err := ctx.AppDB().Exec(`INSERT INTO research_imports(source_install_id,project_id) VALUES(7,'test-proj')`); err != nil {
		t.Fatal(err)
	}
	if err := resetStrategyFills(ctx.AppDB(), "test-proj"); err != nil {
		t.Fatal(err)
	}
	if err := syncStrategyFills(ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	row := readAttribution(t, ctx.AppDB(), portfolioID, strategyID, "BTC-USD")
	assertNear(t, "legacy realized", row.gross, 60)
	assertNear(t, "legacy open qty", row.openQty, 0)
}

// A strategy may be handed a position it did not open, or have one closed out
// from under it. It must never realize more than its own book can account for.
func TestStrategySellBeyondItsOwnBookDoesNotOverRealize(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Handover desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(tradingDB(ctx), portfolioID, map[string]any{
		"fee_bps": 0.0, "slippage_bps": 0.0,
	}); err != nil {
		t.Fatal(err)
	}
	opener := mustCreateStrategy(t, ctx, "Opener")
	closer := mustCreateStrategy(t, ctx, "Closer")

	fillStrategyOrder(t, ctx, portfolioID, opener, "ho-buy", "BTC-USD", "buy", 10, 100)

	fillStrategyOrder(t, ctx, portfolioID, closer, "ho-sell", "BTC-USD", "sell", 10, 150)

	closerRow := readAttribution(t, ctx.AppDB(), portfolioID, closer, "BTC-USD")
	assertNear(t, "closer realized", closerRow.gross, 0)
	assertNear(t, "closer open qty", closerRow.openQty, 0)

	openerRow := readAttribution(t, ctx.AppDB(), portfolioID, opener, "BTC-USD")
	assertNear(t, "opener open qty", openerRow.openQty, 10)
	assertNear(t, "opener realized", openerRow.gross, 0)
}

// The rollup the dashboard widget reads.
func TestStrategyLiveRowsReportPnLPositionsAndSharing(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Rollup desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(tradingDB(ctx), portfolioID, map[string]any{
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

	fillStrategyOrder(t, ctx, portfolioID, alpha, "lr-a-buy", "BTC-USD", "buy", 10, 100)
	fillStrategyOrder(t, ctx, portfolioID, beta, "lr-b-buy", "BTC-USD", "buy", 5, 100)
	fillStrategyOrder(t, ctx, portfolioID, alpha, "lr-a-sell", "BTC-USD", "sell", 10, 120)
	fillStrategyOrder(t, ctx, portfolioID, alpha, "lr-a-eth", "ETH-USD", "buy", 2, 50)
	if err := dbUpsertMark(tradingDB(ctx), &Mark{
		Symbol: "ETH-USD", AssetClass: "crypto", Price: 75,
		MarkedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := dbStrategyLiveRows(ctx, "test-proj")
	if err != nil {
		t.Fatal(err)
	}
	var alphaRow *StrategyLiveRow
	for _, row := range rows {
		if row.StrategyID == alpha {
			alphaRow = row
		}
		if row.StrategyID == 0 {
			t.Fatal("unattributed bucket leaked into the rollup")
		}
	}
	if alphaRow == nil {
		t.Fatalf("no row for alpha in %d rows", len(rows))
	}
	assertNear(t, "alpha realized", alphaRow.RealizedPnL, 200)

	assertNear(t, "alpha unrealized", alphaRow.UnrealizedPnL, 50)
	assertNear(t, "alpha total", alphaRow.TotalPnL, 250)
	assertNear(t, "alpha market value", alphaRow.MarketValue, 150)
	if alphaRow.AssignmentStatus != "active" {
		t.Fatalf("alpha assignment status = %q, want active", alphaRow.AssignmentStatus)
	}
	if alphaRow.StrategyName != "Alpha" || alphaRow.PortfolioName != "Rollup desk" {
		t.Fatalf("alpha row naming = %q / %q", alphaRow.StrategyName, alphaRow.PortfolioName)
	}

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

}

// The route the widget calls, including the filters it passes.
func TestStrategiesLiveEndpointFiltersAndTotals(t *testing.T) {
	ctx := newTestCtx(t)
	globalCtx = ctx
	portfolioID := mustCreatePortfolio(t, ctx, "Endpoint desk", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(tradingDB(ctx), portfolioID, map[string]any{
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
		t.Fatalf("manual fills must not appear in the rollup, got %d rows", len(rows))
	}
	first := rows[0].(map[string]any)
	assertNear(t, "endpoint realized", first["realized_pnl"].(float64), 200)
	totals := body["totals"].(map[string]any)
	assertNear(t, "endpoint totals", totals["realized_pnl"].(float64), 200)

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
