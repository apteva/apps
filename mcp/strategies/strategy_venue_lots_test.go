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
			if err := dbUpdatePortfolioConfig(ctx.AppDB(), pid, map[string]any{"fee_bps": tc.fee}); err != nil {
				t.Fatal(err)
			}
			_, err := (&App{}).toolVenueProfileUpdate(ctx, map[string]any{"venue_slug": "simulation", "asset_class": "crypto", "symbol": "ETH-USD", "qty_step": .003})
			if err != nil {
				t.Fatal(err)
			}
			mark, err := dbGetMark(ctx.AppDB(), "ETH-USD")
			if err != nil {
				t.Fatal(err)
			}
			if tc.held > 0 {
				if err = dbInsertPositionRaw(ctx.AppDB(), "test-proj", pid, "ETH-USD", "crypto", "", tc.held, mark.Price); err != nil {
					t.Fatal(err)
				}
			}
			pf := mustPortfolio(t, ctx, pid)
			profile := resolveVenueProfile(ctx.AppDB(), pf, "ETH-USD", "crypto")
			if profile.QtyStep != .003 {
				t.Fatalf("fixture qty step %v", profile.QtyStep)
			}
			sid := mustCreateTestStrategy(t, ctx, "ETH-USD")
			s, err := dbGetStrategy(ctx.AppDB(), "test-proj", sid)
			if err != nil {
				t.Fatal(err)
			}
			orders, _, err := placeStrategyPaperOrders(globalEngine, ctx, pf, s, &StrategyAssignment{ID: 1}, &StrategyEvaluation{TargetAllocations: []StrategyAllocation{{Symbol: "ETH-USD", Weight: tc.weight}}})
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
			if err = tryFill(globalEngine, o); err != nil {
				t.Fatal(err)
			}
			filled, err := dbGetOrder(ctx.AppDB(), "test-proj", o.ID)
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
