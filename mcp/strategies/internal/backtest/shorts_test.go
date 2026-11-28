package backtest

import (
	"math"
	"testing"
	"time"
)

func shortConfig() Config {
	return Config{StartingCash: 1000, Shorts: DefaultShorts()}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func spacedTape(step time.Duration, prices ...float64) []Input {
	var out []Input
	for i, p := range prices {
		at := epoch.Add(time.Duration(i) * step)
		out = append(out, Input{ID: at.String(), Type: "market.quote", Symbol: "TEST", EventTime: at, AvailableAt: at, Data: map[string]float64{"price": p, "volume": 1e6}})
	}
	return out
}

func scripted(t *testing.T, c Config, tape []Input, orders map[int][]Order) *Engine {
	t.Helper()
	step := 0
	e, err := New(c, tape, nil, func(s *State, in Input) ([]Command, error) {
		defer func() { step++ }()
		var out []Command
		for i := range orders[step] {
			o := orders[step][i]
			out = append(out, Command{Order: &o})
		}
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; !e.State.Finished; i++ {
		if i > 10000 {
			t.Fatal("did not terminate")
		}
		if _, err := e.Advance(); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func TestShortSaleProceedsGoToCash(t *testing.T) {
	e := scripted(t, shortConfig(), protectedTape(100, 100, 90), map[int][]Order{0: {{Symbol: "TEST", Side: "sell", Qty: 5}}})
	pos := e.State.Positions["TEST"]
	if pos.Qty != -5 || pos.AvgCost != 100 || !near(e.State.Cash, 1500) {
		t.Fatalf("pos=%+v cash=%v", pos, e.State.Cash)
	}
	m := e.Metrics()
	if !near(m["equity"], 1050) || !near(m["short_pnl"], 50) || !near(m["short_exposure"], 450/1050.0*100) || m["long_exposure"] != 0 {
		t.Fatalf("metrics=%v", m)
	}
}

func TestSellAcrossZeroClosesLongThenOpensShort(t *testing.T) {
	e := scripted(t, shortConfig(), protectedTape(100, 100, 110, 110, 100), map[int][]Order{
		0: {{Symbol: "TEST", Side: "buy", Qty: 3}},
		2: {{Symbol: "TEST", Side: "sell", Qty: 5}},
	})
	pos := e.State.Positions["TEST"]
	if pos.Qty != -2 || pos.AvgCost != 110 {
		t.Fatalf("pos=%+v", pos)
	}
	if !near(e.State.RealizedLong, 30) || e.State.RealizedShort != 0 || !near(e.State.RealizedPnL, 30) {
		t.Fatalf("state=%+v", e.State)
	}
	if m := e.Metrics(); !near(m["short_pnl"], 20) || !near(m["equity"], 1050) {
		t.Fatalf("metrics=%v", m)
	}
}

func TestBorrowAccruesOnShortValueAndStopsAfterCover(t *testing.T) {
	day := 24 * time.Hour
	// Short 10 at 100 on day 0, hold for 365 days, cover, hold another 365.
	tape := spacedTape(day, 100, 100)
	tape = append(tape, spacedTape(365*day, 100, 100, 100)[1:]...)
	for i := range tape {
		tape[i].ID = tape[i].AvailableAt.String()
	}
	c := shortConfig()
	c.Shorts.BorrowBpsAnnual = 100
	e := scripted(t, c, tape, map[int][]Order{0: {{Symbol: "TEST", Side: "sell", Qty: 10}}, 2: {{Symbol: "TEST", Side: "buy", Qty: 10}}})
	if len(e.State.Positions) != 0 {
		t.Fatalf("positions=%v", e.State.Positions)
	}
	// Zero latency: the short fills on the day-0 quote and the cover on day 365.
	want := 1000 * 0.01
	if !near(e.State.BorrowCost, want) || !near(e.Metrics()["equity"], 1000-want) {
		t.Fatalf("borrow=%v want %v equity=%v", e.State.BorrowCost, want, e.Metrics()["equity"])
	}
}

func TestPerSymbolBorrowOverride(t *testing.T) {
	c := shortConfig()
	c.Shorts.SymbolBorrowBps = map[string]float64{"TEST": 0}
	e := scripted(t, c, spacedTape(24*time.Hour, 100, 100, 100), map[int][]Order{0: {{Symbol: "TEST", Side: "sell", Qty: 1}}})
	if e.State.BorrowCost != 0 {
		t.Fatalf("borrow=%v", e.State.BorrowCost)
	}
}

func TestInitialMarginRejectsOversizedShort(t *testing.T) {
	e := scripted(t, shortConfig(), protectedTape(100, 100), map[int][]Order{0: {{Symbol: "TEST", Side: "sell", Qty: 25}}})
	if o := e.State.Orders[0]; o.Status != "rejected" || o.Reason != "insufficient initial margin" {
		t.Fatalf("order=%+v", o)
	}
	ok := scripted(t, shortConfig(), protectedTape(100, 100), map[int][]Order{0: {{Symbol: "TEST", Side: "sell", Qty: 19}}})
	if ok.State.Positions["TEST"].Qty != -19 {
		t.Fatalf("19 shares (1900 gross on 1000 equity) fits 50%% initial margin: %+v", ok.State.Orders[0])
	}
}

func TestDollarNeutralBookFitsInitialMargin(t *testing.T) {
	tape := []Input{}
	for i, at := range []time.Time{epoch, epoch.Add(time.Second)} {
		for _, sym := range []string{"AAA", "BBB"} {
			tape = append(tape, Input{ID: sym + at.String(), Type: "market.quote", Symbol: sym, EventTime: at, AvailableAt: at, Data: map[string]float64{"price": 100, "volume": 1e6}})
		}
		_ = i
	}
	e := scripted(t, shortConfig(), tape, map[int][]Order{0: {{Symbol: "AAA", Side: "buy", Qty: 9.9}, {Symbol: "BBB", Side: "sell", Qty: 9.9}}})
	if e.State.Positions["AAA"].Qty != 9.9 || e.State.Positions["BBB"].Qty != -9.9 {
		t.Fatalf("positions=%v orders=%+v %+v", e.State.Positions, e.State.Orders[0], e.State.Orders[1])
	}
	if m := e.Metrics(); !near(m["net_exposure"], 0) || !near(m["long_exposure"]+m["short_exposure"], 198) {
		t.Fatalf("metrics=%v", m)
	}
}

func TestMaintenanceBreachForcesCover(t *testing.T) {
	// Short 15 at 100 (gross 1500, 750 initial margin on 1000 equity). At 140
	// equity is 400 against 630 maintenance; the cover restores 40% margin.
	e := scripted(t, shortConfig(), protectedTape(100, 100, 140, 140, 140), map[int][]Order{0: {{Symbol: "TEST", Side: "sell", Qty: 15}}})
	if e.State.MarginCalls != 1 {
		t.Fatalf("margin calls=%d orders=%d", e.State.MarginCalls, len(e.State.Orders))
	}
	cover := e.State.Orders[1]
	if cover.Tag != marginCallTag || !cover.ReduceOnly || cover.Side != "buy" || cover.Status != "filled" {
		t.Fatalf("cover=%+v", cover)
	}
	equity := 1000 + 15*100 - 15*140.0
	wantQty := 15 * (15*140 - equity/0.4) / (15 * 140)
	if !near(cover.FilledQty, wantQty) {
		t.Fatalf("cover qty=%v want %v", cover.FilledQty, wantQty)
	}
	m := e.Metrics()
	if m["equity"] < 0.3*math.Abs(e.State.Positions["TEST"].Qty)*140-1e-6 || m["margin_calls"] != 1 || m["max_margin_used_pct"] < m["margin_requirement_pct"] {
		t.Fatalf("metrics=%v", m)
	}
}

func TestUnshortableAndContractsKeepTheirRules(t *testing.T) {
	c := shortConfig()
	c.Shorts.Unshortable = []string{"TEST"}
	e := scripted(t, c, protectedTape(100, 90), map[int][]Order{0: {{Symbol: "TEST", Side: "sell", Qty: 1}}})
	if len(e.State.Positions) != 0 {
		t.Fatalf("unshortable symbol went short: %v", e.State.Positions)
	}
}

func TestShortsDisabledKeepsMetricSet(t *testing.T) {
	e := scripted(t, Config{StartingCash: 1000}, protectedTape(100, 100), map[int][]Order{0: {{Symbol: "TEST", Side: "buy", Qty: 1}}})
	if _, ok := e.Metrics()["short_exposure"]; ok {
		t.Fatal("long-only run grew short metrics")
	}
	if _, err := New(Config{StartingCash: 1, Shorts: &Shorts{Enabled: true, InitialMargin: .3, MaintenanceMargin: .5}}, protectedTape(1), nil, nil); err == nil {
		t.Fatal("maintenance above initial accepted")
	}
}
