package main

// Tier 1 — every interesting tool path exercised against an
// in-memory SQLite. Whole suite < 1s.

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func newTestCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"))
	globalCtx = ctx
	// Mock provider so handlers that hit the engine work without a tick.
	globalEngine = &engine{
		db:       ctx.AppDB(),
		provider: newMockProvider(),
		logger:   ctx.Logger(),
	}
	for _, m := range globalEngine.provider.Universe() {
		_ = dbUpsertMark(ctx.AppDB(), m)
	}
	return ctx
}

func mustCreatePortfolio(t *testing.T, ctx *sdk.AppCtx, name string, classes []string) int64 {
	t.Helper()
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{
		ProjectID: "test-proj", Name: name, AllowedClasses: classes,
		StartingCash: 100_000,
	})
	if err != nil { t.Fatalf("create portfolio: %v", err) }
	return id
}

// ─── Portfolio reads ───────────────────────────────────────────────

func TestPortfolioList_EmptyByDefault(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	out, err := app.toolPortfolioList(ctx, map[string]any{})
	if err != nil { t.Fatal(err) }
	pfs := out.(map[string]any)["portfolios"].([]map[string]any)
	if len(pfs) != 0 {
		t.Errorf("want 0 portfolios, got %d", len(pfs))
	}
}

func TestPortfolioGet_AfterCreate(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Long-Term Equity", []string{"equity", "etf"})
	app := &App{}
	out, err := app.toolPortfolioGet(ctx, map[string]any{"portfolio_id": float64(id)})
	if err != nil { t.Fatal(err) }
	p := out.(map[string]any)["portfolio"].(*Portfolio)
	if p.Name != "Long-Term Equity" {
		t.Errorf("name=%q", p.Name)
	}
	if p.Equity != 100_000 {
		t.Errorf("equity=%v, want 100000 (cash baseline)", p.Equity)
	}
}

// ─── order_place: the pre-trade pipeline ──────────────────────────

func TestOrderPlace_RejectsShortRationale(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id), "symbol": "AAPL",
		"side": "buy", "type": "market", "qty": 1.0,
		"rationale": "too short",
	})
	got := out.(map[string]any)
	if got["status"] != "rejected" || got["code"] != "rationale_required" {
		t.Errorf("expected rationale_required rejection, got %#v", got)
	}
}

func TestOrderPlace_RejectsBlockedAssetClass(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "EquityOnly", []string{"equity"})
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id), "symbol": "BTC-USD",
		"side": "buy", "type": "market", "qty": 0.01,
		"rationale": "trying to add crypto to an equity-only portfolio",
	})
	got := out.(map[string]any)
	if got["status"] != "rejected" || got["code"] != "asset_class_blocked" {
		t.Errorf("expected asset_class_blocked rejection, got %#v", got)
	}
}

func TestOrderPlace_PolymarketSideMustBeYesNo(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Poly", []string{"polymarket"})
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id), "symbol": "POLY:btc-100k-2026",
		"side": "buy", "type": "limit", "qty": 100.0, "limit_price": 0.5,
		"rationale": "this should be rejected — buy isn't a poly side",
	})
	got := out.(map[string]any)
	if got["status"] != "rejected" || got["code"] != "invalid_side" {
		t.Errorf("expected invalid_side rejection, got %#v", got)
	}
}

func TestOrderPlace_PolymarketLimitPriceMustBeBounded(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Poly", []string{"polymarket"})
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id), "symbol": "POLY:btc-100k-2026",
		"side": "yes", "type": "limit", "qty": 100.0, "limit_price": 1.5,
		"rationale": "this should be rejected — price > 1 is impossible on poly",
	})
	got := out.(map[string]any)
	if got["status"] != "rejected" || got["code"] != "invalid_args" {
		t.Errorf("expected invalid_args rejection, got %#v", got)
	}
}

func TestOrderPlace_HappyPath_LandsAsWorking(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id), "symbol": "AAPL",
		"side": "buy", "type": "limit", "qty": 5.0, "limit_price": 220.0,
		"rationale": "leg in below recent support; risk budget allows 5 shares.",
	})
	got := out.(map[string]any)
	if got["status"] != "working" {
		t.Fatalf("expected status=working, got %#v", got)
	}
	if got["order_id"] == nil || !strings.HasPrefix(got["order_id"].(string), "o-") {
		t.Errorf("order_id prefix wrong: %v", got["order_id"])
	}
	// Auto-rationale row landed in the journal.
	entries, _ := dbReadJournal(ctx.AppDB(), id, "rationale", "", 10)
	if len(entries) != 1 {
		t.Errorf("expected 1 rationale journal entry, got %d", len(entries))
	}
}

// ─── Engine: tryFill against a freshly-marked symbol ───────────────

func TestEngine_FillsMarketBuyOnNextTick(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Crypto", []string{"crypto"})
	// place
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id), "symbol": "BTC-USD",
		"side": "buy", "type": "market", "qty": 0.01,
		"rationale": "starter position — small size to test fill path.",
	})
	if out.(map[string]any)["status"] != "working" {
		t.Fatalf("order didn't reach working: %#v", out)
	}
	// tick the engine
	if err := markTick(nil, ctx); err != nil {
		t.Fatal(err)
	}
	// orders_list should now show 'filled'
	orders, _ := dbListOrders(ctx.AppDB(), id, "filled", 10)
	if len(orders) != 1 {
		t.Fatalf("expected 1 filled order, got %d", len(orders))
	}
	o := orders[0]
	if o.AvgFillPrice <= 0 {
		t.Errorf("avg fill price not set")
	}
	// Position should be opened.
	pos, _ := dbListPositions(ctx.AppDB(), id)
	if len(pos) != 1 {
		t.Fatalf("expected 1 position, got %d", len(pos))
	}
	if pos[0].Symbol != "BTC-USD" || pos[0].Qty != 0.01 {
		t.Errorf("position wrong: %+v", pos[0])
	}
	// Cash should have dropped by qty*price.
	pf, _ := dbGetPortfolio(ctx.AppDB(), "test-proj", id)
	if pf.Cash >= 100_000 {
		t.Errorf("cash didn't decrement: %v", pf.Cash)
	}
	// Fill journal entry should exist.
	fills, _ := dbReadJournal(ctx.AppDB(), id, "fill", "", 10)
	if len(fills) != 1 {
		t.Errorf("expected 1 fill journal entry, got %d", len(fills))
	}
}

func TestEngine_LimitOnlyFillsWhenCrossed(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	// AAPL marks ~ $224. Place a buy limit at $200 — won't fill.
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id), "symbol": "AAPL",
		"side": "buy", "type": "limit", "qty": 1.0, "limit_price": 200.0,
		"rationale": "cheeky low limit; will not fill at current marks.",
	})
	if out.(map[string]any)["status"] != "working" {
		t.Fatalf("not working: %#v", out)
	}
	_ = markTick(nil, ctx)
	working, _ := dbListOrders(ctx.AppDB(), id, "working", 10)
	if len(working) != 1 {
		t.Errorf("expected order to stay working, got %d working", len(working))
	}
}

// ─── Polymarket fill — buys YES, position records outcome ──────────

func TestEngine_PolymarketYesBuy(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Poly", []string{"polymarket"})
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id), "symbol": "POLY:btc-100k-2026",
		"side": "yes", "type": "market", "qty": 100.0,
		"rationale": "small starter — confirming polymarket market fills work end to end.",
	})
	if out.(map[string]any)["status"] != "working" {
		t.Fatalf("not working: %#v", out)
	}
	if err := markTick(nil, ctx); err != nil { t.Fatal(err) }
	pos, _ := dbListPositions(ctx.AppDB(), id)
	if len(pos) != 1 {
		t.Fatalf("expected 1 poly position, got %d", len(pos))
	}
	if pos[0].Outcome != "YES" {
		t.Errorf("outcome=%q, want YES", pos[0].Outcome)
	}
}

// ─── Cancel ────────────────────────────────────────────────────────

func TestOrderCancel_Working(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id), "symbol": "AAPL",
		"side": "buy", "type": "limit", "qty": 1.0, "limit_price": 100.0,
		"rationale": "stays working at low limit so we can cancel it cleanly.",
	})
	oid := out.(map[string]any)["order_id"].(string)
	res, _ := (&App{}).toolOrderCancel(ctx, map[string]any{"order_id": oid, "reason": "test cleanup"})
	if res.(map[string]any)["status"] != "cancelled" {
		t.Errorf("cancel result: %#v", res)
	}
}

// ─── Journal write/read ───────────────────────────────────────────

func TestJournal_WriteThenRead(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	_, err := (&App{}).toolJournalWrite(ctx, map[string]any{
		"portfolio_id": float64(id), "kind": "thesis",
		"body": "first thesis — testing journal round-trip.",
	})
	if err != nil { t.Fatal(err) }
	out, _ := (&App{}).toolJournalRead(ctx, map[string]any{
		"portfolio_id": float64(id), "kind": "thesis",
	})
	entries := out.(map[string]any)["entries"].([]*JournalEntry)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Body, "first thesis") {
		t.Errorf("body=%q", entries[0].Body)
	}
}

// ─── Watchlist ─────────────────────────────────────────────────────

func TestWatchlist_AddDedupesThenRemove(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	app := &App{}
	r1, _ := app.toolWatchlistAdd(ctx, map[string]any{"portfolio_id": float64(id), "symbol": "AAPL"})
	if r1.(map[string]any)["added"] != true {
		t.Errorf("first add: %#v", r1)
	}
	r2, _ := app.toolWatchlistAdd(ctx, map[string]any{"portfolio_id": float64(id), "symbol": "AAPL"})
	if r2.(map[string]any)["added"] != false {
		t.Errorf("second add should be no-op: %#v", r2)
	}
	r3, _ := app.toolWatchlistRemove(ctx, map[string]any{"portfolio_id": float64(id), "symbol": "AAPL"})
	if r3.(map[string]any)["removed"] != true {
		t.Errorf("remove: %#v", r3)
	}
}

// ─── Pause ─────────────────────────────────────────────────────────

func TestPortfolioPause_BlocksFurtherOrders(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	app := &App{}
	_, err := app.toolPortfolioPause(ctx, map[string]any{
		"portfolio_id": float64(id), "reason": "self-test pause",
	})
	if err != nil { t.Fatal(err) }
	out, _ := app.toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id), "symbol": "AAPL",
		"side": "buy", "type": "market", "qty": 1.0,
		"rationale": "this should be rejected — portfolio is paused.",
	})
	got := out.(map[string]any)
	if got["status"] != "rejected" || got["code"] != "portfolio_not_active" {
		t.Errorf("expected portfolio_not_active rejection, got %#v", got)
	}
}
