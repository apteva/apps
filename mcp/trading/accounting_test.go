package main

import (
	"database/sql"
	"math"
	"testing"
	"time"
)

func TestRealizedPnLSurvivesFullCloseAndRebuild(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID := mustCreatePortfolio(t, ctx, "Realized ledger", []string{"crypto"})
	if err := dbUpdatePortfolioConfig(ctx.AppDB(), portfolioID, map[string]any{
		"fee_bps": 10.0, "slippage_bps": 0.0,
	}); err != nil {
		t.Fatal(err)
	}

	fillAccountingOrder(t, ctx, portfolioID, "accounting-buy", "buy", 10, 100)
	fillAccountingOrder(t, ctx, portfolioID, "accounting-sell", "sell", 10, 110)

	if position, err := dbGetPosition(ctx.AppDB(), portfolioID, "BTC-USD", ""); err != nil || position != nil {
		t.Fatalf("closed position = %#v, err=%v", position, err)
	}
	assertPortfolioAccounting(t, ctx, portfolioID, 97.9, 2.1)

	if _, err := ctx.AppDB().Exec(`DELETE FROM position_accounting`); err != nil {
		t.Fatal(err)
	}
	if err := dbRebuildPositionAccounting(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	assertPortfolioAccounting(t, ctx, portfolioID, 97.9, 2.1)
	out, err := (&App{}).toolAccountSummary(ctx, map[string]any{"portfolio_id": float64(portfolioID)})
	if err != nil {
		t.Fatal(err)
	}
	summary := out.(map[string]any)
	if math.Abs(summary["realized_pnl"].(float64)-97.9) > 1e-6 || math.Abs(summary["fees_paid"].(float64)-2.1) > 1e-6 {
		t.Fatalf("account summary = %#v", summary)
	}
}

func TestLivePortfolioInitialHoldingsAreNotReportedAsProfit(t *testing.T) {
	ctx := newTestCtx(t)
	portfolioID, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{
		ProjectID: "test-proj", Name: "Existing broker account",
		AllowedClasses: []string{"equity"}, StartingCash: 500, Mode: "live", BrokerSlug: "alpaca-trading",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbInsertPositionRaw(ctx.AppDB(), "test-proj", portfolioID, "AAPL", "equity", "", 10, 100); err != nil {
		t.Fatal(err)
	}
	if err := dbUpsertMark(ctx.AppDB(), &Mark{
		Symbol: "AAPL", AssetClass: "equity", Price: 100,
		MarkedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	portfolio, err := dbGetPortfolio(ctx.AppDB(), "test-proj", portfolioID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotPortfolio(ctx.AppDB(), portfolio)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TotalPnL != 0 || snapshot.TotalPnLPct != 0 {
		t.Fatalf("initial broker holdings reported as profit: total=%v pct=%v", snapshot.TotalPnL, snapshot.TotalPnLPct)
	}
}

func fillAccountingOrder(t *testing.T, ctx interface{ AppDB() *sql.DB }, portfolioID int64, id, side string, qty, mark float64) {
	t.Helper()
	db := ctx.AppDB()
	if err := dbUpsertMark(db, &Mark{
		Symbol: "BTC-USD", AssetClass: "crypto", Price: mark,
		MarkedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	order := &Order{
		ID: id, PortfolioID: portfolioID, Symbol: "BTC-USD", AssetClass: "crypto",
		Side: side, Type: "market", Qty: qty, TIF: "day", Status: "working",
		Rationale: "accounting regression test fill", Source: "test",
	}
	if err := dbInsertOrder(db, order, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if err := tryFill(globalEngine, order); err != nil {
		t.Fatal(err)
	}
}

func assertPortfolioAccounting(t *testing.T, ctx interface{ AppDB() *sql.DB }, portfolioID int64, wantRealized, wantFees float64) {
	t.Helper()
	portfolio, err := dbGetPortfolio(ctx.AppDB(), "test-proj", portfolioID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotPortfolio(ctx.AppDB(), portfolio)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(snapshot.RealizedPnL-wantRealized) > 1e-6 {
		t.Fatalf("realized P&L = %.8f, want %.8f", snapshot.RealizedPnL, wantRealized)
	}
	if math.Abs(snapshot.FeesPaid-wantFees) > 1e-6 {
		t.Fatalf("fees = %.8f, want %.8f", snapshot.FeesPaid, wantFees)
	}
	if math.Abs(snapshot.TotalPnL-wantRealized) > 1e-6 {
		t.Fatalf("total P&L = %.8f, want %.8f", snapshot.TotalPnL, wantRealized)
	}
}
