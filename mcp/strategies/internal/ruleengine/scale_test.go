package ruleengine

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"runtime"
	"testing"
	"time"

	sim "github.com/apteva/apps/mcp/strategies/internal/backtest"
)

// scaleRun backtests a monthly cross-sectional momentum book (top decile by
// 126-bar return, above its 200-bar average) over symbols × days.
func scaleRun(t *testing.T, symbols, days int) {
	rng := rand.New(rand.NewSource(3))
	names := make([]string, symbols)
	price := make([]float64, symbols)
	for i := range names {
		names[i] = fmt.Sprintf("S%03d", i)
		price[i] = 20 + rng.Float64()*80
	}
	start := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)
	inputs := make([]sim.Input, 0, 2*symbols*days)
	for d := 0; d < days; d++ {
		at := start.AddDate(0, 0, d)
		for i, s := range names {
			o := price[i]
			c := o * math.Exp((float64(i%7)-3)*0.0002+rng.NormFloat64()*0.02)
			price[i] = c
			inputs = append(inputs,
				sim.Input{ID: fmt.Sprintf("q%d/%d", i, d), Type: "market.quote", Symbol: s, Source: "synthetic", EventTime: at, AvailableAt: at, Data: map[string]float64{"price": o, "volume": 1e6}},
				sim.Input{ID: fmt.Sprintf("b%d/%d", i, d), Type: "market.bar.close", Symbol: s, Source: "synthetic", EventTime: at, AvailableAt: at.Add(24 * time.Hour), Data: map[string]float64{"price": c, "open": o, "high": math.Max(o, c), "low": math.Min(o, c), "volume": 1e6}})
		}
	}
	month := Expr{Metric: "month"}
	newMonth := Operation("!=", month, Expr{Name: "last_month"})
	weight := Operation("if", Operation("and", Operation("top", Expr{Metric: "return", Period: 126}, Number(float64(symbols/10))), Operation(">", Expr{Metric: "close"}, Expr{Metric: "sma", Period: 200})), Number(1), Number(0))
	p := &Program{Version: Version, Symbols: names, Timeframe: "1d", Timezone: "UTC", Initial: map[string]float64{"last_month": 0}, Rules: []Rule{{
		ID: "rotate", On: "bar.close", When: &newMonth, Repeat: "always",
		Actions: []Action{{Kind: "target", Weight: &weight, Construct: &Construct{Normalize: "gross", MaxPosition: 0.05}}, {Kind: "set", Name: "last_month", Value: &month}},
	}}}
	config := sim.Config{Seed: 1, StartingCash: 1e7, Costs: sim.Costs{QtyStep: 0.0001, FeeBps: 1}, BenchmarkSymbol: names[0], NotifyFills: true, PruneResolved: true}
	strategy, err := Strategy(p, config)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err = ScheduleInputs(p, inputs)
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	e, err := sim.New(config, inputs, nil, strategy)
	if err != nil {
		t.Fatal(err)
	}
	for !e.State.Finished {
		if _, err := e.Advance(); err != nil {
			t.Fatalf("at %s: %v", e.State.Now, err)
		}
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	held := 0
	for _, pos := range e.State.Positions {
		if pos.Qty > 0 {
			held++
		}
	}
	t.Logf("%d symbols × %d bars: %s, heap %d MB, %d held, metrics %v", symbols, days, time.Since(began).Round(time.Millisecond), mem.HeapAlloc>>20, held, e.Metrics()["return_pct"])
	if e.State.Fees <= 0 || held == 0 || len(e.State.StrategyState) > 1<<20 {
		t.Fatalf("scale run held=%d fees=%v checkpoint=%d bytes", held, e.State.Fees, len(e.State.StrategyState))
	}
}

func TestScaleCrossSectionalBook(t *testing.T) {
	if os.Getenv("RUN_SCALE_TESTS") == "" {
		scaleRun(t, 50, 300)
		return
	}
	scaleRun(t, MaxSymbols, 2500)
}
