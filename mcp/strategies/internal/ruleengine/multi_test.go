package ruleengine

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	sim "github.com/apteva/apps/mcp/strategies/internal/backtest"
)

var day0 = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

// dailyTape mirrors the captured-history tape: a quote at each bar's open and
// the bar's close available a day later. closes[symbol][i] is day i's close;
// day i opens at day i-1's close.
func dailyTape(closes map[string][]float64) []sim.Input {
	var out []sim.Input
	for symbol, series := range closes {
		for i, c := range series {
			at := day0.Add(time.Duration(i) * 24 * time.Hour)
			open := c
			if i > 0 {
				open = series[i-1]
			}
			if i > 0 {
				out = append(out, sim.Input{ID: fmt.Sprintf("q/%s/%03d", symbol, i), Type: "market.quote", Symbol: symbol, Source: "test", EventTime: at, AvailableAt: at, Data: map[string]float64{"price": open, "volume": 1e9}})
			}
			out = append(out, sim.Input{ID: fmt.Sprintf("b/%s/%03d", symbol, i), Type: "market.bar.close", Symbol: symbol, Source: "test", EventTime: at, AvailableAt: at.Add(24 * time.Hour), Data: map[string]float64{"open": open, "high": math.Max(open, c), "low": math.Min(open, c), "price": c, "volume": 1e6}, Metadata: map[string]string{"timeframe": "1d"}})
		}
	}
	return out
}

func multiProgram(symbols []string, rules ...Rule) *Program {
	return &Program{Version: Version, Symbols: symbols, Timeframe: "1d", Timezone: "UTC", Rules: rules}
}

func runMulti(t *testing.T, p *Program, config sim.Config, tape []sim.Input) (*sim.Engine, []sim.Output) {
	t.Helper()
	strategy, err := Strategy(p, config)
	if err != nil {
		t.Fatal(err)
	}
	e, err := sim.New(config, EvaluationInputs(p, tape), nil, strategy)
	if err != nil {
		t.Fatal(err)
	}
	var out []sim.Output
	for i := 0; !e.State.Finished; i++ {
		if i > 100000 {
			t.Fatal("did not terminate")
		}
		batch, err := e.Advance()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, batch...)
	}
	return e, out
}

func shortsConfig() sim.Config {
	return sim.Config{StartingCash: 10000, Shorts: sim.DefaultShorts(), NotifyFills: true}
}

func spreadRule(n float64, construct *Construct) Rule {
	score := Expr{Metric: "return", Period: 1}
	weight := Operation("if", Operation("top", score, Number(n)), Number(1), Operation("if", Operation("bottom", score, Number(n)), Number(-1), Number(0)))
	return Rule{ID: "spread", On: "bar.close", Actions: []Action{{Kind: "target", Weight: &weight, Construct: construct}}}
}

func TestV2ValidationBoundaries(t *testing.T) {
	weight := Number(1)
	bad := map[string]*Program{
		"no symbols":          multiProgram(nil, Rule{ID: "r", On: "bar.close", Actions: []Action{{Kind: "cancel"}}}),
		"duplicate symbols":   multiProgram([]string{"A", "A"}, Rule{ID: "r", On: "bar.close", Actions: []Action{{Kind: "cancel"}}}),
		"unknown expr symbol": multiProgram([]string{"A"}, Rule{ID: "r", On: "bar.close", When: &Expr{Metric: "close", Symbol: "B"}, Actions: []Action{{Kind: "cancel"}}}),
		"implicit symbol":     multiProgram([]string{"A", "B"}, Rule{ID: "r", On: "bar.close", When: &Expr{Metric: "close"}, Actions: []Action{{Kind: "cancel"}}}),
		"target in for_each":  multiProgram([]string{"A"}, Rule{ID: "r", On: "bar.close", ForEach: true, Actions: []Action{{Kind: "target", Weight: &weight}}}),
		"target and enter": multiProgram([]string{"A", "B"}, Rule{ID: "r", On: "bar.close", Actions: []Action{{Kind: "target", Weight: &weight}}},
			Rule{ID: "e", On: "bar.close", ForEach: true, Actions: []Action{{Kind: "enter", Side: "buy", Sizing: Sizing{Mode: "quantity", Amount: 1}}}}),
		"enter without symbol": multiProgram([]string{"A", "B"}, Rule{ID: "e", On: "bar.close", Actions: []Action{{Kind: "enter", Side: "buy", Sizing: Sizing{Mode: "quantity", Amount: 1}}}}),
		"top needs literal":    multiProgram([]string{"A"}, Rule{ID: "r", On: "bar.close", ForEach: true, When: ptr(Operation("top", Expr{Metric: "close"}, Expr{Metric: "close"})), Actions: []Action{{Kind: "cancel"}}}),
		"cs in crossover":      multiProgram([]string{"A"}, Rule{ID: "r", On: "bar.close", ForEach: true, When: ptr(Operation("crosses_above", Operation("cs_rank", Expr{Metric: "close"}), Number(0.5))), Actions: []Action{{Kind: "cancel"}}}),
		"bad construct":        multiProgram([]string{"A"}, Rule{ID: "r", On: "bar.close", Actions: []Action{{Kind: "target", Weight: &weight, Construct: &Construct{Gross: 3}}}}),
	}
	for name, p := range bad {
		if err := Validate(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	v1 := testProgram()
	v1.Rules[0].When = ptr(Operation(">", Operation("clamp", Operation("log", Expr{Metric: "close"}), Number(0), Number(5)), Number(1)))
	if err := Validate(v1); err != nil {
		t.Errorf("one-symbol program needs no explicit symbols: %v", err)
	}
	pairs := multiProgram([]string{"KO", "PEP"}, Rule{ID: "short", On: "bar.close", When: ptr(Operation(">", Expr{Metric: "close", Symbol: "KO"}, Expr{Metric: "close", Symbol: "PEP"})), Actions: []Action{{Kind: "enter", Symbol: "KO", Side: "sell", Sizing: Sizing{Mode: "quantity", Amount: 1}}}})
	if err := Validate(pairs); err != nil {
		t.Fatal(err)
	}
	if _, err := Strategy(pairs, sim.Config{StartingCash: 1}); err == nil || !strings.Contains(err.Error(), "simulator shorts") {
		t.Fatalf("spot short entry without shorts: %v", err)
	}
	if _, err := Strategy(pairs, shortsConfig()); err != nil {
		t.Fatal(err)
	}
}

func TestCrossSectionalOperators(t *testing.T) {
	p := multiProgram([]string{"A", "B", "C", "D", "E"}, Rule{ID: "r", On: "bar.close", ForEach: true, Actions: []Action{{Kind: "cancel"}}})
	s := state()
	for symbol, v := range map[string]float64{"A": 3, "B": 1, "C": 3, "E": 5} { // D missing
		s.Features[symbol+"/score"] = map[string]float64{"v": v}
	}
	score := Expr{Metric: "feature", Feature: "score", Field: "v"}
	c := context{program: p, runtime: newRuntime(p), series: map[string]*Runtime{}, state: s, memo: map[*Expr]map[string]float64{}}
	eval := func(symbol string, e Expr) (float64, error) {
		x := c
		x.symbol = symbol
		return x.eval(e, 0)
	}
	rank := Operation("cs_rank", score)
	// Ascending B=1, A=3, C=3 (tie in symbol order), E=5.
	for symbol, want := range map[string]float64{"B": 0, "A": 1.0 / 3, "C": 2.0 / 3, "E": 1} {
		if got, err := eval(symbol, rank); err != nil || math.Abs(got-want) > 1e-12 {
			t.Errorf("cs_rank %s = %v %v, want %v", symbol, got, err, want)
		}
	}
	if _, err := eval("D", rank); err != ErrUnavailable {
		t.Errorf("missing symbol ranked: %v", err)
	}
	top := Operation("top", score, Number(2))
	bottom := Operation("bottom", score, Number(2))
	for symbol, want := range map[string][2]float64{"E": {1, 0}, "C": {1, 0}, "A": {0, 1}, "B": {0, 1}} {
		gt, _ := eval(symbol, top)
		gb, _ := eval(symbol, bottom)
		if gt != want[0] || gb != want[1] {
			t.Errorf("%s top=%v bottom=%v want %v", symbol, gt, gb, want)
		}
	}
	if z, _ := eval("E", Operation("cs_zscore", score)); math.Abs(z-(5-3)/math.Sqrt(2)) > 1e-12 {
		t.Errorf("zscore=%v", z)
	}
	if d, _ := eval("B", Operation("cs_demean", score)); d != -2 {
		t.Errorf("demean=%v", d)
	}
	if n, _ := eval("", Operation("cs_count", Operation(">", score, Number(2)))); n != 3 {
		t.Errorf("count=%v", n)
	}
	for symbol, want := range map[string]float64{"B": 0, "A": 0, "C": 1, "E": 1} {
		if q, _ := eval(symbol, Operation("quantile", score, Number(2))); q != want {
			t.Errorf("quantile %s=%v want %v", symbol, q, want)
		}
	}
	for _, tc := range []struct {
		e    Expr
		want float64
	}{
		{Operation("sign", Number(-3)), -1},
		{Operation("sqrt", Number(9)), 3},
		{Operation("clamp", Number(7), Number(0), Number(5)), 5},
		{Operation("if", Number(0), Number(1), Number(2)), 2},
		{Operation("exp", Number(0)), 1},
	} {
		if got, err := eval("", tc.e); err != nil || got != tc.want {
			t.Errorf("%s = %v %v", tc.e.Op, got, err)
		}
	}
	if _, err := eval("", Operation("log", Number(0))); err != ErrUnavailable {
		t.Errorf("log(0)=%v", err)
	}
}

func trendingCloses(symbols []string, days int) map[string][]float64 {
	out := map[string][]float64{}
	for i, symbol := range symbols {
		drift := 0.002 * float64(i-len(symbols)/2) // A falls fastest, last rises fastest
		price := 100.0
		for d := 0; d < days; d++ {
			price *= 1 + drift + 0.001*math.Sin(float64(d+i))
			out[symbol] = append(out[symbol], price)
		}
	}
	return out
}

func TestTargetBuildsDollarNeutralSpread(t *testing.T) {
	symbols := []string{"A", "B", "C", "D", "E", "F"}
	p := multiProgram(symbols, spreadRule(2, nil))
	p.Rules[0].Repeat = ""
	e, out := runMulti(t, p, shortsConfig(), dailyTape(trendingCloses(symbols, 30)))
	m := e.Metrics()
	// Two longs and two shorts, a quarter of equity each.
	longs, shorts := 0, 0
	for symbol, pos := range e.State.Positions {
		value := pos.Qty * e.State.Quotes[symbol].Price / m["equity"]
		if math.Abs(math.Abs(value)-0.25) > 0.01 {
			t.Errorf("%s weight %.4f", symbol, value)
		}
		if pos.Qty > 0 {
			longs++
		} else {
			shorts++
		}
	}
	if longs != 2 || shorts != 2 || e.State.Positions["F"].Qty <= 0 || e.State.Positions["A"].Qty >= 0 {
		t.Fatalf("positions=%v", e.State.Positions)
	}
	if math.Abs(m["net_exposure"]) > 1 || math.Abs(m["long_exposure"]+m["short_exposure"]-100) > 2 {
		t.Fatalf("exposure=%v", m)
	}
	// P&L is the long leg plus the short leg, net of fees and borrow.
	if got, want := m["total_pnl"], m["long_pnl"]+m["short_pnl"]-m["fees"]-m["borrow_cost"]; math.Abs(got-want) > 1e-6 {
		t.Fatalf("total %v != legs %v (%v)", got, want, m)
	}
	if m["short_pnl"] <= 0 || m["long_pnl"] <= 0 || m["borrow_cost"] <= 0 {
		t.Fatalf("trending spread should profit on both legs and pay borrow: %v", m)
	}
	rebalances := 0
	for _, o := range out {
		if o.Type == "strategy.decision" && strings.Contains(string(o.Data), `"rebalanced"`) {
			rebalances++
		}
	}
	if rebalances < 20 {
		t.Fatalf("rebalances=%d", rebalances)
	}
}

func TestTargetCapsAndTurnoverBand(t *testing.T) {
	symbols := []string{"A", "B", "C", "D"}
	closes := trendingCloses(symbols, 15)
	capped := multiProgram(symbols, spreadRule(2, &Construct{MaxPosition: 0.1}))
	e, _ := runMulti(t, capped, shortsConfig(), dailyTape(closes))
	for symbol, pos := range e.State.Positions {
		if w := math.Abs(pos.Qty*e.State.Quotes[symbol].Price) / e.Metrics()["equity"]; w > 0.105 {
			t.Errorf("%s weight %.4f above cap", symbol, w)
		}
	}
	loose, _ := runMulti(t, multiProgram(symbols, spreadRule(2, nil)), shortsConfig(), dailyTape(closes))
	banded, _ := runMulti(t, multiProgram(symbols, spreadRule(2, &Construct{MinTradePct: 5})), shortsConfig(), dailyTape(closes))
	if len(banded.State.Orders) >= len(loose.State.Orders) {
		t.Fatalf("turnover band did not skip small trades: %d vs %d orders", len(banded.State.Orders), len(loose.State.Orders))
	}
}

func TestTargetNeedsShortsForShortWeights(t *testing.T) {
	symbols := []string{"A", "B"}
	p := multiProgram(symbols, spreadRule(1, nil))
	strategy, _ := Strategy(p, sim.Config{StartingCash: 1000})
	e, _ := sim.New(sim.Config{StartingCash: 1000}, EvaluationInputs(p, dailyTape(trendingCloses(symbols, 5))), nil, strategy)
	var err error
	for !e.State.Finished && err == nil {
		_, err = e.Advance()
	}
	if err == nil || !strings.Contains(err.Error(), "shorts are disabled") {
		t.Fatalf("err=%v", err)
	}
	// An unshortable name is held flat instead.
	config := shortsConfig()
	config.Shorts.Unshortable = []string{"A"}
	run, _ := runMulti(t, p, config, dailyTape(trendingCloses(symbols, 5)))
	if run.State.Positions["A"].Qty != 0 || run.State.Positions["B"].Qty <= 0 {
		t.Fatalf("positions=%v", run.State.Positions)
	}
}

func TestFeaturesInvisibleBeforeAvailability(t *testing.T) {
	symbols := []string{"A", "B"}
	weight := Expr{Metric: "feature", Feature: "signal", Field: "v"}
	p := multiProgram(symbols, Rule{ID: "news", On: "bar.close", Actions: []Action{{Kind: "target", Weight: &weight, Construct: &Construct{Normalize: "gross"}}}})
	tape := dailyTape(map[string][]float64{"A": {100, 100, 100, 100}, "B": {50, 50, 50, 50}})
	// Day 1's signal is published an hour after day 1's close is evaluated.
	late := day0.Add(48*time.Hour + time.Hour)
	tape = append(tape, sim.Input{ID: "f/A", Type: "feature.signal", Symbol: "A", Source: "news", EventTime: day0.Add(24 * time.Hour), AvailableAt: late, Data: map[string]float64{"v": 1}})
	e, out := runMulti(t, p, shortsConfig(), tape)
	var firstFill time.Time
	for _, o := range out {
		if o.Type == "fill" && firstFill.IsZero() {
			firstFill = o.At
		}
	}
	if firstFill.Before(late) {
		t.Fatalf("traded on a feature before it was available: %v < %v", firstFill, late)
	}
	if e.State.Positions["A"].Qty <= 0 {
		t.Fatalf("never traded the published signal: %v", e.State.Positions)
	}
}

func TestPairsProgramRoundTrip(t *testing.T) {
	ko, pep := []float64{}, []float64{}
	for d := 0; d < 120; d++ {
		spread := 0.08 * math.Sin(float64(d)*2*math.Pi/30)
		pep = append(pep, 50)
		ko = append(ko, 50*(1+spread))
	}
	ratio := Operation("/", Expr{Metric: "close", Symbol: "KO"}, Expr{Metric: "close", Symbol: "PEP"})
	z := Operation("/", Operation("-", Expr{Name: "ratio"}, Expr{Metric: "sma", Period: 20, Input: ptr(Expr{Name: "ratio"})}), Expr{Metric: "stddev", Period: 20, Input: ptr(Expr{Name: "ratio"})})
	flat := Operation("==", Expr{Metric: "position_qty", Symbol: "KO"}, Number(0))
	open := Operation("and", Operation(">", Expr{Name: "z"}, Number(1)), flat)
	size := Sizing{Mode: "equity_pct", Amount: 0.4}
	p := multiProgram([]string{"KO", "PEP"},
		Rule{ID: "short_ko", On: "bar.close", When: &open, Actions: []Action{{Kind: "enter", Symbol: "KO", Side: "sell", Sizing: size}}},
		Rule{ID: "long_pep", On: "bar.close", When: &open, Actions: []Action{{Kind: "enter", Symbol: "PEP", Side: "buy", Sizing: size}}},
		Rule{ID: "exit", On: "bar.close", When: ptr(Operation("and", Operation("<", Expr{Name: "z"}, Number(-0.5)), Operation("<", Expr{Metric: "position_qty", Symbol: "KO"}, Number(0)))), Actions: []Action{{Kind: "flatten"}}},
	)
	p.Calculations = map[string]Expr{"ratio": ratio, "z": z}
	if err := Validate(p); err != nil {
		t.Fatal(err)
	}
	e, out := runMulti(t, p, shortsConfig(), dailyTape(map[string][]float64{"KO": ko, "PEP": pep}))
	shortsOpened := 0
	for _, o := range e.State.Orders {
		if o.Symbol == "KO" && o.Side == "sell" && o.Tag == "short_ko" && o.Status == "filled" {
			shortsOpened++
		}
	}
	if shortsOpened < 2 {
		t.Fatalf("pair opened %d times; outputs %d", shortsOpened, len(out))
	}
	if m := e.Metrics(); m["realized_pnl"] <= 0 {
		t.Fatalf("mean-reverting pair lost money: %v", m)
	}
}

func TestMultiCheckpointResumesIdentically(t *testing.T) {
	symbols := []string{"A", "B", "C", "D"}
	p := multiProgram(symbols, spreadRule(1, nil))
	tape := EvaluationInputs(p, dailyTape(trendingCloses(symbols, 25)))
	config := shortsConfig()
	full, want := runMulti(t, p, config, tape)
	strategy, _ := Strategy(p, config)
	e, _ := sim.New(config, tape, nil, strategy)
	var got []sim.Output
	for i := 0; i < 40; i++ {
		batch, err := e.Advance()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, batch...)
	}
	raw, _ := json.Marshal(e.State)
	if len(raw) > 64*1024 {
		t.Fatalf("checkpoint carries candles: %d bytes", len(raw))
	}
	var checkpoint sim.State
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	strategy, _ = Strategy(p, config)
	e, err := sim.New(config, tape, &checkpoint, strategy)
	if err != nil {
		t.Fatal(err)
	}
	for !e.State.Finished {
		batch, err := e.Advance()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, batch...)
	}
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	if string(a) != string(b) || sim.Hash(full.Metrics()) != sim.Hash(e.Metrics()) {
		t.Fatal("resumed run diverged")
	}
}

func TestMultiWarmupCarriesIntoRun(t *testing.T) {
	symbols := []string{"A", "B"}
	closes := trendingCloses(symbols, 12)
	all := dailyTape(closes)
	split := day0.Add(6 * 24 * time.Hour)
	var warmTape, mainTape []sim.Input
	for _, in := range all {
		if in.AvailableAt.Before(split) {
			warmTape = append(warmTape, in)
		} else {
			mainTape = append(mainTape, in)
		}
	}
	score := Expr{Metric: "return", Period: 5}
	weight := Operation("if", Operation("top", score, Number(1)), Number(1), Number(-1))
	p := multiProgram(symbols, Rule{ID: "t", On: "bar.close", Actions: []Action{{Kind: "target", Weight: &weight}}})
	observe, err := ObserveOnly(p)
	if err != nil {
		t.Fatal(err)
	}
	warm, _ := sim.New(sim.Config{StartingCash: 1}, warmTape, nil, observe)
	for !warm.State.Finished {
		if _, err := warm.Advance(); err != nil {
			t.Fatal(err)
		}
	}
	config := shortsConfig()
	strategy, _ := Strategy(p, config)
	e, _ := sim.New(config, EvaluationInputs(p, mainTape), nil, strategy)
	e.State.StrategyState = warm.State.StrategyState
	e.State.Warmup = warmTape
	var firstFill time.Time
	for !e.State.Finished {
		out, err := e.Advance()
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range out {
			if o.Type == "fill" && firstFill.IsZero() {
				firstFill = o.At
			}
		}
	}
	// return(5) is ready on the first main-tape evaluation only with warmup.
	if !firstFill.Equal(split) {
		t.Fatalf("first fill %v, want %v", firstFill, split)
	}
	e.State.Warmup = nil
	strategy, _ = Strategy(p, config)
	if _, err := strategy(e.State, sim.Input{ID: "x", Type: EvaluateInput, AvailableAt: e.State.Now}); err == nil {
		t.Fatal("resumed without warmup inputs")
	}
}

func TestCorrelationAndBeta(t *testing.T) {
	a, b := []float64{100}, []float64{100}
	for d := 1; d < 40; d++ {
		r := 0.01 * math.Sin(float64(d))
		b = append(b, b[d-1]*math.Exp(r))
		a = append(a, a[d-1]*math.Exp(2*r))
	}
	p := multiProgram([]string{"A", "B"}, Rule{ID: "r", On: "bar.close", ForEach: true, When: &Expr{Metric: "beta", Against: "B", Period: 20}, Actions: []Action{{Kind: "cancel"}}})
	m := newMultiRuntime(p, historyLimits(p), time.UTC)
	for _, in := range dailyTape(map[string][]float64{"A": a, "B": b}) {
		if err := m.observe(p, in); err != nil {
			t.Fatal(err)
		}
	}
	c := context{program: p, runtime: m.root, series: m.series, symbol: "A", state: state()}
	beta, err := c.eval(Expr{Metric: "beta", Against: "B", Period: 20}, 0)
	if err != nil || math.Abs(beta-2) > 1e-9 {
		t.Fatalf("beta=%v %v", beta, err)
	}
	corr, err := c.eval(Expr{Metric: "correlation", Against: "B", Period: 20}, 0)
	if err != nil || math.Abs(corr-1) > 1e-9 {
		t.Fatalf("corr=%v %v", corr, err)
	}
}

func TestTargetKeepsWorkingOrdersThatCoverTheBook(t *testing.T) {
	w := Number(0.5)
	p := &Program{Version: Version, Symbols: []string{"A"}, Timeframe: "1h", Timezone: "UTC", Rules: []Rule{{ID: "hold", On: "bar.close", Repeat: "always", Actions: []Action{{Kind: "target", Weight: &w, Construct: &Construct{Normalize: "none"}}}}}}
	config := sim.Config{StartingCash: 1000, Costs: sim.Costs{QtyStep: 0.0001}}
	r := newMultiRuntime(p, historyLimits(p), time.UTC)
	r.series["A"].Bars["1h"] = []Candle{{At: time.Unix(0, 0), Open: 10, High: 10, Low: 10, Close: 10}}
	working := &sim.Order{ID: "w1", Symbol: "A", Side: "buy", Type: "market", Qty: 50, Status: "submitted", Tag: "hold"}
	s := &sim.State{Cash: 1000, Positions: map[string]sim.Position{}, Quotes: map[string]sim.Quote{"A": {Price: 10.2}}, Orders: []*sim.Order{working}}
	c := context{program: p, config: config, state: s, runtime: r.root, series: r.series, memo: map[*Expr]map[string]float64{}}
	commands, err := c.target("hold", p.Rules[0].Actions[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range commands {
		if cmd.CancelID != "" || cmd.Order != nil {
			t.Fatalf("2%% price drift replaced the working order: %+v", cmd)
		}
	}
	s.Quotes["A"] = sim.Quote{Price: 5} // target doubles: top up past tolerance
	commands, err = c.target("hold", p.Rules[0].Actions[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) < 3 || commands[0].CancelID != "w1" || commands[1].Order == nil || math.Abs(commands[1].Order.Qty-100) > 1e-9 {
		t.Fatalf("commands = %+v, want cancel then a 100-share buy", commands)
	}
}
