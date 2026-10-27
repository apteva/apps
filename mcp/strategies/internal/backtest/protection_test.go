package backtest

import (
	"math"
	"testing"
	"time"
)

func protectedTape(prices ...float64) []Input {
	var out []Input
	for i, p := range prices {
		at := time.Unix(int64(i+1), 0)
		out = append(out, Input{ID: at.String(), Type: "market.quote", Symbol: "TEST", EventTime: at, AvailableAt: at, Data: map[string]float64{"price": p, "volume": 1000}})
	}
	return out
}
func protectedRun(t *testing.T, c Config, tape []Input, order Order) *Engine {
	t.Helper()
	entered := false
	e, err := New(c, tape, nil, func(*State, Input) ([]Command, error) {
		if entered {
			return nil, nil
		}
		entered = true
		return []Command{{Order: &order}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for !e.State.Finished {
		if _, err = e.Advance(); err != nil {
			t.Fatal(err)
		}
	}
	return e
}
func contractConfig() Config {
	return Config{StartingCash: 1000, Contracts: map[string]Contract{"TEST": {Multiplier: 10, CurrencyRate: 1, MarginFraction: .1}}}
}
func TestLinearContractsAndProtection(t *testing.T) {
	for _, side := range []string{"buy", "sell"} {
		t.Run(side, func(t *testing.T) {
			exit := 90.0
			if side == "buy" {
				exit = 110
			}
			e := protectedRun(t, contractConfig(), protectedTape(100, exit), Order{Symbol: "TEST", Side: side, Type: "market", Qty: 2, Protection: &Protection{StopPct: .05, TargetPct: .1}})
			if len(e.State.Positions) != 0 || math.Abs(e.State.RealizedPnL-200) > 1e-9 || math.Abs(e.Metrics()["equity"]-1200) > 1e-9 {
				t.Fatalf("bad contract accounting: %v", e.Metrics())
			}
		})
	}
}
func TestContractMarginDoesNotCreateUnlimitedShorts(t *testing.T) {
	e := protectedRun(t, contractConfig(), protectedTape(100, 100), Order{Symbol: "TEST", Side: "sell", Qty: 11})
	if len(e.State.Positions) != 0 || e.State.Orders[0].Status != "rejected" {
		t.Fatal("short exceeded available margin")
	}
}
func TestProtectedPartialFillCancelsEntryRemainder(t *testing.T) {
	c := contractConfig()
	c.MaxFillQty = 1
	e := protectedRun(t, c, protectedTape(100, 90, 90), Order{Symbol: "TEST", Side: "buy", Qty: 3, Protection: &Protection{StopPct: .05, TargetPct: .1}})
	if e.State.Orders[0].FilledQty != 1 || e.State.Orders[0].Status != "cancelled" || len(e.State.Positions) != 0 {
		t.Fatalf("entry reopened after stop: %+v", e.State.Orders[0])
	}
	if math.Abs(e.State.RealizedPnL+100) > 1e-9 {
		t.Fatal(e.Metrics())
	}
}
func TestOCOAndReduceOnlyCannotReverse(t *testing.T) {
	c := contractConfig()
	c.MaxFillQty = 1
	e := protectedRun(t, c, protectedTape(100, 100, 110, 110, 110), Order{Symbol: "TEST", Side: "buy", Qty: 2, Protection: &Protection{StopPct: .05, TargetPct: .1}})
	if len(e.State.Positions) != 0 || math.Abs(e.State.RealizedPnL-200) > 1e-9 {
		t.Fatal(e.Metrics())
	}
	for _, o := range e.State.Orders {
		if o.ReduceOnly && o.FilledQty > o.Qty+1e-9 {
			t.Fatal("protective order overfilled")
		}
	}
}
func TestProtectionUsesExecutableQuoteSide(t *testing.T) {
	tape := protectedTape(100, 96)
	tape[1].Data["bid"] = 94
	tape[1].Data["ask"] = 98
	e := protectedRun(t, contractConfig(), tape, Order{Symbol: "TEST", Side: "buy", Qty: 1, Protection: &Protection{StopPct: .05}})
	if len(e.State.Positions) != 0 || math.Abs(e.State.RealizedPnL+60) > 1e-9 {
		t.Fatal("bid-side stop did not execute", e.Metrics())
	}
}
func TestDefaultSpotStillRejectsShortFill(t *testing.T) {
	e := protectedRun(t, Config{StartingCash: 1000}, protectedTape(100, 90), Order{Symbol: "TEST", Side: "sell", Qty: 1})
	if len(e.State.Positions) != 0 || e.State.RealizedPnL != 0 {
		t.Fatal("cash portfolio silently enabled shorting")
	}
}
