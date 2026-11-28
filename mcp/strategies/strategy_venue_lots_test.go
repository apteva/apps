package main

import (
	"math"
	"testing"
)

func TestStrategyOrdersRespectVenueLots(t *testing.T) {
	for _, tc := range []struct {
		name              string
		weight, held, fee float64
	}{
		{"buy", .12345, 0, 1}, {"sell", .01, 12.345, 1}, {"cash_scaled_buy", 1, 0, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newTestCtx(t)
			pid := mustCreatePortfolio(t, ctx, "Venue lot strategy", []string{"crypto"})
			if err := dbUpdatePortfolioConfig(tradingDB(ctx), pid, map[string]any{"fee_bps": tc.fee}); err != nil {
				t.Fatal(err)
			}
			profile := defaultVenueProfile("simulation", "crypto")
			profile.Symbol, profile.QtyStep = "ETH-USD", .003
			if err := dbUpsertVenueProfile(tradingDB(ctx), &profile); err != nil {
				t.Fatal(err)
			}
			mark, err := dbGetMark(tradingDB(ctx), "ETH-USD")
			if err != nil {
				t.Fatal(err)
			}
			if tc.held > 0 {
				if err = dbInsertPositionRaw(tradingDB(ctx), "test-proj", pid, "ETH-USD", "crypto", "", tc.held, mark.Price); err != nil {
					t.Fatal(err)
				}
			}
			state, err := newTradingReader(ctx).State("test-proj", pid, []string{"ETH-USD"})
			if err != nil {
				t.Fatal(err)
			}
			if profile := state.Profile("ETH-USD"); profile.QtyStep != .003 {
				t.Fatalf("fixture qty step %v", profile.QtyStep)
			}
			sid := mustCreateTestStrategy(t, ctx, "ETH-USD")
			s, err := dbGetStrategy(ctx.AppDB(), "test-proj", sid)
			if err != nil {
				t.Fatal(err)
			}
			orders, _, err := placeStrategyPaperOrders(globalEngine, ctx, state, s, &StrategyAssignment{ID: 1}, &StrategyEvaluation{TargetAllocations: []StrategyAllocation{{Symbol: "ETH-USD", Weight: tc.weight}}})
			if err != nil || len(orders) != 1 {
				t.Fatalf("orders=%v err=%v", orders, err)
			}
			o := orders[0]
			if !multipleOf(o.Qty, .003) {
				t.Fatalf("off-lot order %v", o.Qty)
			}
			if tc.held > 0 && (o.Side != "sell" || o.Qty > tc.held+1e-9) {
				t.Fatalf("invalid sell %+v", o)
			}
			if tc.held == 0 && o.Side != "buy" {
				t.Fatalf("expected buy %+v", o)
			}
			filled, err := dbGetOrder(tradingDB(ctx), "test-proj", o.ID)
			if err != nil {
				t.Fatal(err)
			}
			if filled.Status != "filled" {
				t.Fatalf("execution failed %+v", filled)
			}
			after := mustPortfolio(t, ctx, pid)
			if math.IsNaN(after.Cash) || after.Cash < -1e-8 {
				t.Fatalf("invalid remaining cash %v", after.Cash)
			}
		})
	}
}
