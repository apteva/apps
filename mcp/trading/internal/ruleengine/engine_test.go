package ruleengine

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

func testProgram() *Program {
	return &Program{Version: Version, Symbol: "TEST", Timeframe: "1m", Timezone: "Europe/Helsinki", Rules: []Rule{{ID: "noop", On: "bar.close", When: ptr(Number(0)), Actions: []Action{{Kind: "cancel"}}}}}
}
func ptr(e Expr) *Expr { return &e }
func candle(at time.Time, o, h, l, c float64) sim.Input {
	return sim.Input{ID: at.String(), Type: "market.bar.close", Symbol: "TEST", EventTime: at, AvailableAt: at.Add(time.Minute), Data: map[string]float64{"open": o, "high": h, "low": l, "price": c}, Metadata: map[string]string{"timeframe": "1m"}}
}
func state() *sim.State {
	return &sim.State{Positions: map[string]sim.Position{}, Quotes: map[string]sim.Quote{}, Features: map[string]map[string]float64{}, Cash: 1000}
}
func TestExpressionsRejectInvalidAndCyclicDefinitions(t *testing.T) {
	cases := []*Program{}
	p := testProgram()
	p.Calculations = map[string]Expr{"a": {Name: "b"}, "b": {Name: "a"}}
	cases = append(cases, p)
	p = testProgram()
	p.Rules[0].When = &Expr{Metric: "atr", Period: 0}
	cases = append(cases, p)
	p = testProgram()
	p.Rules[0].When = ptr(Operation("crosses_above", Expr{Metric: "equity"}, Number(10)))
	cases = append(cases, p)
	p = testProgram()
	p.Rules[0].Repeat = "until_reset"
	cases = append(cases, p)
	p = testProgram()
	p.Rules[0].When = &Expr{Value: ptrFloat(2), Metric: "price"}
	cases = append(cases, p)
	p = testProgram()
	p.Rules[0].When = &Expr{Metric: "sma", Period: 20, Timeframe: "30m"}
	cases = append(cases, p)
	for i, p := range cases {
		if Validate(p) == nil {
			t.Fatalf("invalid program %d accepted", i)
		}
	}
}
func ptrFloat(v float64) *float64 { return &v }

func TestEntryPlanningPreventsImplicitPyramiding(t *testing.T) {
	p := testProgram()
	entry := Action{Kind: "enter", Side: "buy", StopPct: .01, Sizing: Sizing{Mode: "quantity", Amount: 1}}
	p.Rules = []Rule{{ID: "first", On: "quote", Actions: []Action{entry}}, {ID: "second", On: "quote", Actions: []Action{entry}}}
	strategy, err := Strategy(p, sim.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := state()
	s.Now = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	s.Quotes["TEST"] = sim.Quote{Price: 100}
	commands, err := strategy(s, sim.Input{ID: "quote", Type: "market.quote", Symbol: "TEST", AvailableAt: s.Now, Data: map[string]float64{"price": 100}})
	if err != nil {
		t.Fatal(err)
	}
	orders := 0
	for _, command := range commands {
		if command.Order != nil {
			orders++
		}
	}
	if orders != 1 {
		t.Fatalf("expected one planned entry, got %d", orders)
	}
	p.Rules[0].Actions = []Action{entry, entry}
	if Validate(p) == nil {
		t.Fatal("ungrouped simultaneous entries accepted")
	}
	p.Rules[0].Actions[0].Group = "pair"
	p.Rules[0].Actions[1].Group = "pair"
	p.Rules[0].Actions[1].Side = "sell"
	if err := Validate(p); err != nil {
		t.Fatal(err)
	}
}

func TestRuleTapeRejectsExternalControlAndMissingLiquidity(t *testing.T) {
	p := testProgram()
	at := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	bar := candle(at, 100, 101, 99, 100)
	bar.Source = "test_feed"
	quote := sim.Input{ID: "quote", Type: "market.quote", Symbol: "TEST", Source: "test_feed", EventTime: at.Add(time.Minute), AvailableAt: at.Add(time.Minute), Data: map[string]float64{"price": 100}}
	if err := ValidateTape(p, []sim.Input{bar, quote}); err != nil {
		t.Fatal(err)
	}
	for _, tape := range [][]sim.Input{{bar}, {quote}, {bar, quote, {Type: "order.intent"}}} {
		if ValidateTape(p, tape) == nil {
			t.Fatal("incomplete or externally controlled tape accepted")
		}
	}
}

func TestCompiledHistoryRetainsDeclaredOffsetsAndHigherFramePeriods(t *testing.T) {
	p := testProgram()
	p.Calculations = map[string]Expr{"upper": {Metric: "highest_high", Timeframe: "1h", Period: 175}, "past": {Metric: "close", Offset: 20}}
	strategy, err := Strategy(p, sim.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := state()
	start := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 181; i++ {
		in := candle(start.Add(time.Duration(i-181)*time.Hour), 100, 101+float64(i), 99, 100)
		in.Metadata["timeframe"] = "1h"
		in.AvailableAt = in.EventTime.Add(time.Hour)
		s.Now = in.AvailableAt
		if _, err := strategy(s, in); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 40; i++ {
		in := candle(start.Add(time.Duration(i)*time.Minute), 100+float64(i), 101+float64(i), 99+float64(i), 100+float64(i))
		s.Now = in.AvailableAt
		if _, err := strategy(s, in); err != nil {
			t.Fatal(err)
		}
	}
	r := newRuntime(p)
	if err := json.Unmarshal(s.StrategyState, r); err != nil {
		t.Fatal(err)
	}
	if len(r.Bars["1m"]) != 23 || len(r.Bars["1h"]) != 177 {
		t.Fatalf("unnecessary checkpoint history retained: minute=%d hour=%d", len(r.Bars["1m"]), len(r.Bars["1h"]))
	}
	c := context{program: p, runtime: r, state: s}
	if v, err := c.eval(Expr{Name: "past"}, 0); err != nil || v != 119 {
		t.Fatalf("retained offset differs: %v %v", v, err)
	}
	before := string(s.StrategyState)
	if _, err := strategy(s, sim.Input{Type: "market.quote", Symbol: "TEST", AvailableAt: s.Now}); err != nil {
		t.Fatal(err)
	}
	if string(s.StrategyState) != before {
		t.Fatal("unrelated quote rewrote a candle-only checkpoint")
	}
}

func TestCheckpointCacheRecoversAfterActionError(t *testing.T) {
	p := testProgram()
	p.Initial = map[string]float64{"count": 0}
	p.Rules = []Rule{{ID: "attempt", On: "quote", Repeat: "always", Actions: []Action{{Kind: "set", Name: "count", Value: ptr(Operation("+", Expr{Name: "count"}, Number(1)))}, {Kind: "enter", Side: "buy", Stop: ptr(Number(101)), Sizing: Sizing{Mode: "quantity", Amount: 1}}}}}
	strategy, err := Strategy(p, sim.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := state()
	in := sim.Input{Type: "market.quote", Symbol: "TEST"}
	s.Quotes["TEST"] = sim.Quote{Price: 200}
	if _, err := strategy(s, in); err != nil {
		t.Fatal(err)
	}
	checkpoint := string(s.StrategyState)
	s.Quotes["TEST"] = sim.Quote{Price: 100}
	if _, err := strategy(s, in); err == nil {
		t.Fatal("invalid stop accepted")
	}
	if string(s.StrategyState) != checkpoint {
		t.Fatal("failed callback committed variables")
	}
	s.Quotes["TEST"] = sim.Quote{Price: 200}
	if _, err := strategy(s, in); err != nil {
		t.Fatal(err)
	}
	r := newRuntime(p)
	json.Unmarshal(s.StrategyState, r)
	if r.Variables["count"] != 2 {
		t.Fatal("failed cached runtime leaked into retry")
	}
}

func TestCheckpointCacheCanServeIndependentConcurrentStates(t *testing.T) {
	p := testProgram()
	p.Initial = map[string]float64{"count": 0}
	p.Rules = []Rule{{ID: "increment", On: "any", Repeat: "always", Actions: []Action{{Kind: "set", Name: "count", Value: ptr(Operation("+", Expr{Name: "count"}, Number(1)))}}}}
	strategy, err := Strategy(p, sim.Config{})
	if err != nil {
		t.Fatal(err)
	}
	states := []*sim.State{state(), state(), state(), state()}
	var wg sync.WaitGroup
	for _, s := range states {
		wg.Add(1)
		go func(s *sim.State) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if _, err := strategy(s, sim.Input{Type: "clock"}); err != nil {
					t.Error(err)
					return
				}
			}
		}(s)
	}
	wg.Wait()
	for _, s := range states {
		r := newRuntime(p)
		if err := json.Unmarshal(s.StrategyState, r); err != nil {
			t.Fatal(err)
		}
		if r.Variables["count"] != 25 {
			t.Fatal("independent state crossed a cache boundary")
		}
	}
}
func TestOHLCAndHigherTimeframeAvailability(t *testing.T) {
	p := testProgram()
	p.Calculations = map[string]Expr{"channel": {Metric: "highest_high", Period: 1, Timeframe: "1h"}}
	r := newRuntime(p)
	s := state()
	start := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		in := candle(start.Add(time.Duration(i)*time.Minute), 100, 101+float64(i), 99, 100)
		closed, err := r.observe(p, in)
		if err != nil {
			t.Fatal(err)
		}
		if closed["1h"] != (i == 59) {
			t.Fatal("forming hourly candle leaked")
		}
	}
	s.Now = start.Add(time.Hour)
	v, err := (context{program: p, runtime: r, state: s}).eval(Expr{Name: "channel"}, 0)
	if err != nil || v != 160 {
		t.Fatalf("hourly high=%v err=%v", v, err)
	}
	incomplete := newRuntime(p)
	for i := 0; i < 60; i++ {
		if i == 20 {
			continue
		}
		_, err := incomplete.observe(p, candle(start.Add(time.Duration(i)*time.Minute), 100, 101, 99, 100))
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(incomplete.Bars["1h"]) != 0 {
		t.Fatal("missing minute produced complete H1 candle")
	}
	early := candle(start, 100, 101, 99, 100)
	early.AvailableAt = start
	if _, err := r.observe(p, early); err == nil {
		t.Fatal("forming input accepted")
	}
}
func TestSessionWindowsAndDSTSchedules(t *testing.T) {
	for _, date := range []string{"2026-01-14", "2026-07-14"} {
		p := testProgram()
		p.Windows = map[string]Window{"morning": {Start: "08:00", End: "11:00"}}
		p.Schedules = map[string]string{"close": "18:00"}
		loc, _ := time.LoadLocation(p.Timezone)
		day, _ := time.ParseInLocation("2006-01-02", date, loc)
		start := day.Add(8 * time.Hour)
		r := newRuntime(p)
		for i := 0; i < 180; i++ {
			if _, err := r.observe(p, candle(start.Add(time.Duration(i)*time.Minute), 100, 101, 99, 100)); err != nil {
				t.Fatal(err)
			}
		}
		if !r.Windows["morning"].Complete {
			t.Fatal("full window unavailable")
		}
		tape := []sim.Input{{ID: "a", Type: "clock", EventTime: day, AvailableAt: day}, {ID: "b", Type: "clock", EventTime: day.Add(23 * time.Hour), AvailableAt: day.Add(23 * time.Hour)}}
		scheduled, err := ScheduleInputs(p, tape)
		if err != nil {
			t.Fatal(err)
		}
		if len(scheduled) != 3 {
			t.Fatal("clock not generated")
		}
		got := scheduled[2].AvailableAt.In(loc)
		if got.Hour() != 18 {
			t.Fatal("wrong local cutoff")
		}
		wantUTC := 16
		if date == "2026-07-14" {
			wantUTC = 15
		}
		if got.UTC().Hour() != wantUTC {
			t.Fatal("DST ignored")
		}
		missing := newRuntime(p)
		for i := 1; i < 180; i++ {
			_, _ = missing.observe(p, candle(start.Add(time.Duration(i)*time.Minute), 100, 101, 99, 100))
		}
		if missing.Windows["morning"].Complete {
			t.Fatal("truncated session treated as complete")
		}
	}
}
func TestWarmupDoesNotConsumeLatches(t *testing.T) {
	p := testProgram()
	p.Initial = map[string]float64{"remembered": 0}
	p.Rules = []Rule{{ID: "remember", On: "bar.close", Repeat: "until_reset", Reset: ptr(Number(0)), Actions: []Action{{Kind: "set", Name: "remembered", Value: ptr(Number(1))}}}}
	observe, err := ObserveOnly(p)
	if err != nil {
		t.Fatal(err)
	}
	s := state()
	in := candle(time.Unix(0, 0), 100, 101, 99, 100)
	s.Now = in.AvailableAt
	if _, err := observe(s, in); err != nil {
		t.Fatal(err)
	}
	var r Runtime
	_ = json.Unmarshal(s.StrategyState, &r)
	if r.Variables["remembered"] != 0 || len(r.Fired) != 0 {
		t.Fatal("warmup consumed an action")
	}
	live, err := Strategy(p, sim.Config{StartingCash: 1000})
	if err != nil {
		t.Fatal(err)
	}
	in = candle(time.Unix(60, 0), 100, 101, 99, 100)
	s.Now = in.AvailableAt
	if _, err := live(s, in); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(s.StrategyState, &r)
	if r.Variables["remembered"] != 1 || len(r.Fired) != 1 {
		t.Fatal("first active candle did not act")
	}
}
func TestFlatCandleAndMissingWarmupDoNotInventValues(t *testing.T) {
	p := testProgram()
	r := newRuntime(p)
	r.Bars["1m"] = []Candle{{Open: 100, High: 100, Low: 100, Close: 100}}
	c := context{program: p, runtime: r, state: state()}
	for _, e := range []Expr{{Metric: "close_fraction"}, {Metric: "atr", Period: 200}, Operation("/", Number(1), Number(0))} {
		if _, err := c.eval(e, 0); err == nil {
			t.Fatal("undefined expression accepted")
		}
	}
}

func TestNestedIndicatorsAndPastOnlyFeatures(t *testing.T) {
	p := testProgram()
	p.Calculations = map[string]Expr{"macd": Operation("-", Expr{Metric: "ema", Period: 12}, Expr{Metric: "ema", Period: 26}), "signal": {Metric: "ema", Period: 9, Input: &Expr{Name: "macd"}}}
	if err := Validate(p); err != nil {
		t.Fatal(err)
	}
	if RequiredBaseBars(p) < 174 {
		t.Fatal("nested warmup undercounted")
	}
	r := newRuntime(p)
	for i := 0; i < 180; i++ {
		v := 100 + float64(i)
		r.Bars["1m"] = append(r.Bars["1m"], Candle{At: time.Unix(int64(i*60), 0), Open: v, High: v + 1, Low: v - 1, Close: v})
	}
	s := state()
	c := context{program: p, runtime: r, state: s}
	macd, err := c.eval(Expr{Name: "macd"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	signal, err := c.eval(Expr{Name: "signal"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if macd < 6.9 || macd > 7.1 || signal < 6.9 || signal > 7.1 {
		t.Fatalf("nested MACD %f signal %f", macd, signal)
	}
	expr := Expr{Metric: "feature", Feature: "sentiment", Field: "score"}
	if _, err := c.eval(expr, 0); err == nil {
		t.Fatal("future/missing feature invented")
	}
	s.Features["TEST/sentiment"] = map[string]float64{"score": .75}
	v, err := c.eval(expr, 0)
	if err != nil || v != .75 {
		t.Fatal("available feature not readable")
	}
}
func TestMixedTimeframeCrossoverUsesPreviousSignalAvailability(t *testing.T) {
	p := testProgram()
	r := newRuntime(p)
	at := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	r.Bars["1h"] = []Candle{{At: at.Add(-2 * time.Hour), High: 90, Low: 80, Close: 85}, {At: at.Add(-time.Hour), High: 110, Low: 90, Close: 100}}
	r.Bars["1m"] = []Candle{{At: at, Close: 109}, {At: at.Add(time.Minute), Close: 111}}
	expr := Operation("crosses_above", Expr{Metric: "close"}, Expr{Metric: "highest_high", Period: 1, Timeframe: "1h"})
	c := context{program: p, runtime: r, state: state()}
	v, err := c.eval(expr, 0)
	if err != nil || v != 1 {
		t.Fatalf("mixed-timeframe crossover: %v %v", v, err)
	}
}
