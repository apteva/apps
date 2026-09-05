package main

import (
	"errors"
	"math"
	"testing"
	"time"
)

func resetVenueRuntime() {
	venueRuntime.Lock()
	venueRuntime.bySlug = map[string]VenueRuntimeHealth{}
	venueRuntime.Unlock()
}

func TestVenueProfileUpdateAndInstrumentConstraints(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	_, err := app.toolVenueProfileUpdate(ctx, map[string]any{
		"venue_slug": "simulation", "asset_class": "crypto",
		"maker_fee_bps": 2.0, "taker_fee_bps": 7.0,
		"min_notional": 25.0, "qty_step": 0.001, "price_tick": 0.05,
	})
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := app.toolVenueProfilesList(ctx, map[string]any{"venue_slug": "simulation", "asset_class": "crypto"})
	if err != nil {
		t.Fatal(err)
	}
	rows := profiles.(map[string]any)["profiles"].([]VenueExecutionProfile)
	if len(rows) != 1 || rows[0].MakerFeeBps != 2 || rows[0].TakerFeeBps != 7 || rows[0].MinNotional != 25 {
		t.Fatalf("profile = %#v", rows)
	}
	pfID := mustCreatePortfolio(t, ctx, "constraints", []string{"crypto"})
	pf, _ := dbGetPortfolio(ctx.AppDB(), "test-proj", pfID)
	p := resolveVenueProfile(ctx.AppDB(), pf, "BTC-USD", "crypto")
	if v := validateExecutionOrder(p, nil, nil, 0.0015, 20_000); v == nil || v.Code != "invalid_qty_step" {
		t.Fatalf("qty violation = %#v", v)
	}
	if v := validateExecutionOrder(p, nil, nil, 0.001, 20_000); v == nil || v.Code != "below_min_notional" {
		t.Fatalf("notional violation = %#v", v)
	}
	if v := validatePriceTick(p, 100.03, "limit_price"); v == nil || v.Code != "invalid_price_tick" {
		t.Fatalf("tick violation = %#v", v)
	}
}

func TestSessionPolicyDistinguishesRegularAndContinuous(t *testing.T) {
	closed := time.Date(2026, time.July, 4, 15, 0, 0, 0, time.UTC)
	equity := defaultVenueProfile("simulation", "equity")
	mark := &Mark{Symbol: "AAPL", AssetClass: "equity", Price: 200, Source: "alpaca-market-data"}
	if v := validateExecutionOrderAt(equity, defaultInstrument("AAPL", "equity", "alpaca", closed), mark, 1, 200, closed); v == nil || v.Code != "market_closed" {
		t.Fatalf("equity session violation = %#v", v)
	}
	crypto := defaultVenueProfile("simulation", "crypto")
	if v := validateExecutionOrderAt(crypto, defaultInstrument("BTC-USD", "crypto", "binance", closed), &Mark{Source: "binance-public"}, 1, 200, closed); v != nil {
		t.Fatalf("24x7 crypto rejected: %#v", v)
	}
}

func TestSimulationExecutionCrossesQuoteAndAttributesCosts(t *testing.T) {
	bid, ask := 99.0, 101.0
	mark := &Mark{Price: 100, BidPrice: &bid, AskPrice: &ask}
	profile := defaultVenueProfile("simulation", "crypto")
	profile.TakerFeeBps = 20
	profile.SlippageBps = 10
	got := estimateSimulationExecution(mark, "buy", "market", 2, 100, profile)
	if math.Abs(got.Price-101.101) > 1e-9 {
		t.Fatalf("price=%v", got.Price)
	}
	if math.Abs(got.SpreadCost-2) > 1e-9 {
		t.Fatalf("spread=%v", got.SpreadCost)
	}
	if math.Abs(got.SlippageCost-0.202) > 1e-9 {
		t.Fatalf("slippage=%v", got.SlippageCost)
	}
	if math.Abs(got.Fee-0.404404) > 1e-9 || got.LiquidityRole != "taker" {
		t.Fatalf("fee/role=%v/%s", got.Fee, got.LiquidityRole)
	}

	profile.MakerFeeBps = 3
	maker := estimateSimulationExecution(mark, "buy", "limit", 2, 100, profile, "maker")
	if maker.FeeBps != 3 || maker.SpreadCost != 0 || maker.SlippageCost != 0 || maker.Price != 100 {
		t.Fatalf("maker=%#v", maker)
	}
}

func TestPaperLimitUsesOppositeQuoteAndMakerPrice(t *testing.T) {
	ctx := newTestCtx(t)
	pfID := mustCreatePortfolio(t, ctx, "maker quote", []string{"crypto"})
	bid, ask := 99.0, 101.0
	if err := dbUpsertMark(ctx.AppDB(), &Mark{
		Symbol: "BTC-USD", AssetClass: "crypto", Price: 100, BidPrice: &bid, AskPrice: &ask,
		Source: "mock", MarkedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	limit := 100.0
	order := &Order{
		ID: "o-maker-touch", PortfolioID: pfID, Symbol: "BTC-USD", AssetClass: "crypto",
		Side: "buy", Type: "limit", Qty: 1, LimitPrice: &limit, TIF: "gtc", Status: "working",
		Rationale: "verify resting maker order waits for the opposite quote", Source: "test", LiquidityRole: "maker",
	}
	if err := dbInsertOrder(ctx.AppDB(), order, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if err := tryFill(globalEngine, order); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetOrder(ctx.AppDB(), "test-proj", order.ID)
	if got.Status != "working" {
		t.Fatalf("midpoint must not fill a buy while ask is above limit: %#v", got)
	}
	ask = 100
	if err := dbUpsertMark(ctx.AppDB(), &Mark{
		Symbol: "BTC-USD", AssetClass: "crypto", Price: 99.5, BidPrice: &bid, AskPrice: &ask,
		Source: "mock", MarkedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tryFill(globalEngine, order); err != nil {
		t.Fatal(err)
	}
	got, _ = dbGetOrder(ctx.AppDB(), "test-proj", order.ID)
	if got.Status != "filled" || got.AvgFillPrice != limit {
		t.Fatalf("maker fill=%#v", got)
	}
	costs, err := dbExecutionCosts(ctx.AppDB(), pfID, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, cost := range costs {
		if cost.Kind == "spread" || cost.Kind == "slippage" {
			t.Fatalf("maker fill incorrectly charged %s: %#v", cost.Kind, cost)
		}
	}
}

func TestMarketableLimitCapsAndReattributesSlippage(t *testing.T) {
	ctx := newTestCtx(t)
	pfID := mustCreatePortfolio(t, ctx, "taker cap", []string{"crypto"})
	bid, ask := 99.0, 100.0
	if err := dbUpsertMark(ctx.AppDB(), &Mark{
		Symbol: "BTC-USD", AssetClass: "crypto", Price: 99.5, BidPrice: &bid, AskPrice: &ask,
		Source: "mock", MarkedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	limit := 100.0
	order := &Order{
		ID: "o-taker-cap", PortfolioID: pfID, Symbol: "BTC-USD", AssetClass: "crypto",
		Side: "buy", Type: "limit", Qty: 1, LimitPrice: &limit, TIF: "gtc", Status: "working",
		Rationale: "verify a marketable limit caps modeled adverse slippage", Source: "test", LiquidityRole: "taker",
	}
	if err := dbInsertOrder(ctx.AppDB(), order, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if err := tryFill(globalEngine, order); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetOrder(ctx.AppDB(), "test-proj", order.ID)
	if got.Status != "filled" || got.AvgFillPrice != limit {
		t.Fatalf("marketable limit fill=%#v", got)
	}
	costs, err := dbExecutionCosts(ctx.AppDB(), pfID, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, cost := range costs {
		if cost.Kind == "slippage" && cost.Amount != 0 {
			t.Fatalf("limit cap left stale slippage attribution: %#v", cost)
		}
	}
}

func TestOrderPlaceRejectsConfiguredMinimumAndOutage(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	pfID := mustCreatePortfolio(t, ctx, "rules", []string{"crypto"})
	_, err := app.toolVenueProfileUpdate(ctx, map[string]any{
		"venue_slug": "simulation", "asset_class": "crypto", "min_notional": 1_000.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	place := func() map[string]any {
		out, err := app.toolOrderPlace(ctx, map[string]any{
			"portfolio_id": pfID, "symbol": "BTC-USD", "side": "buy", "type": "market", "qty": 0.001,
			"rationale": "Venue execution validation test with a sufficiently detailed rationale.",
		})
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}
	if got := place(); got["code"] != "below_min_notional" {
		t.Fatalf("minimum result=%#v", got)
	}
	_, err = app.toolVenueProfileUpdate(ctx, map[string]any{
		"venue_slug": "simulation", "asset_class": "crypto", "min_notional": 0.0, "status": "outage",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := place(); got["code"] != "venue_unavailable" {
		t.Fatalf("outage result=%#v", got)
	}
}

func TestVenueCircuitBreakerOpensAndRecovers(t *testing.T) {
	resetVenueRuntime()
	defer resetVenueRuntime()
	for i := 0; i < venueCircuitThreshold; i++ {
		noteVenueCall("kraken", errors.New("temporary venue failure"))
	}
	if open, _ := venueCircuitOpen("kraken", time.Now().UTC()); !open {
		t.Fatal("circuit did not open")
	}
	noteVenueCall("kraken", nil)
	if open, _ := venueCircuitOpen("kraken", time.Now().UTC()); open {
		t.Fatal("circuit did not recover")
	}
}

func TestFundingPaymentIsIdempotentAndAffectsPaperPnL(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	pfID := mustCreatePortfolio(t, ctx, "funding", []string{"crypto"})
	args := map[string]any{
		"portfolio_id": pfID, "symbol": "BTC-USD", "venue_slug": "simulation",
		"provider_event_id": "funding-2026-09-04T08:00Z", "amount": 12.5, "currency": "USD", "rate_bps": 1.25,
	}
	first, err := app.toolFundingPaymentRecord(ctx, args)
	if err != nil || first.(map[string]any)["recorded"] != true {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := app.toolFundingPaymentRecord(ctx, args)
	if err != nil || second.(map[string]any)["duplicate"] != true {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	pf, _ := dbGetPortfolio(ctx.AppDB(), "test-proj", pfID)
	snap, _ := snapshotPortfolio(ctx.AppDB(), pf)
	if snap.Cash != 99_987.5 || snap.FundingPaid != 12.5 || snap.RealizedPnL != -12.5 {
		t.Fatalf("snapshot cash=%v funding=%v realized=%v", snap.Cash, snap.FundingPaid, snap.RealizedPnL)
	}
	costs, err := dbExecutionCosts(ctx.AppDB(), pfID, 10)
	if err != nil || len(costs) != 1 || costs[0].Kind != "funding" {
		t.Fatalf("costs=%#v err=%v", costs, err)
	}
}

func TestDetailedFillWritesExecutionCostLedger(t *testing.T) {
	ctx := newTestCtx(t)
	pfID := mustCreatePortfolio(t, ctx, "cost ledger", []string{"crypto"})
	o := &Order{ID: "o-cost-ledger", PortfolioID: pfID, Symbol: "BTC-USD", AssetClass: "crypto", Side: "buy", Type: "market", Qty: 1, TIF: "day", Status: "working", Rationale: "test", Source: "test", LiquidityRole: "taker"}
	if err := dbInsertOrder(ctx.AppDB(), o, "test-proj"); err != nil {
		t.Fatal(err)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = dbInsertFillDetailed(tx, "test-proj", o.ID, pfID, 1, 100, 0.2, FillCostDetails{
		VenueSlug: "simulation", FeeCurrency: "USD", FeeSource: "model", LiquidityRole: "taker", FeeBps: 20,
		SpreadCost: 0.5, SlippageCost: 0.1,
	})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	costs, err := dbExecutionCosts(ctx.AppDB(), pfID, 10)
	if err != nil || len(costs) != 3 {
		t.Fatalf("costs=%#v err=%v", costs, err)
	}
	totals := map[string]float64{}
	for _, c := range costs {
		totals[c.Kind] += c.Amount
	}
	if totals["fee"] != 0.2 || totals["spread"] != 0.5 || totals["slippage"] != 0.1 {
		t.Fatalf("totals=%v", totals)
	}
}

func TestCommissionConversionPreservesUnknownCurrency(t *testing.T) {
	ctx := newTestCtx(t)
	o := &Order{Symbol: "BTC-USD"}
	if got, ok := convertCommissionToQuote(ctx.AppDB(), o, 0.001, "BTC", "USD", 60_000); !ok || got != 60 {
		t.Fatalf("base conversion=%v/%v", got, ok)
	}
	if got, ok := convertCommissionToQuote(ctx.AppDB(), o, 1, "UNLISTED", "USD", 60_000); ok || got != 0 {
		t.Fatalf("unknown conversion=%v/%v", got, ok)
	}
}

func TestPolymarketOrderUsesResolvedVenueFee(t *testing.T) {
	price := 0.55
	args, err := (polymarketAdapter{}).TranslateOrder(&Order{
		ID: "o-fee", Symbol: "POLY:tokenid:123", AssetClass: "polymarket", Side: "yes",
		Type: "limit", Qty: 10, LimitPrice: &price, TIF: "gtc", VenueFeeBps: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	order := args["order"].(map[string]any)
	if order["feeRateBps"] != "25" {
		t.Fatalf("feeRateBps=%v", order["feeRateBps"])
	}
}
