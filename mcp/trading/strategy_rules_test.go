package main

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
	rules "github.com/apteva/apps/mcp/trading/internal/ruleengine"
)

type ruleFixture struct {
	ID         string         `json:"id"`
	Definition map[string]any `json:"definition"`
	Simulation sim.Config     `json:"simulation"`
}

func loadRuleFixture(t *testing.T, file string) ruleFixture {
	t.Helper()
	raw, err := simulationSources.ReadFile("rule_examples/" + file + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var f ruleFixture
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if _, _, err = validateStrategyDefinition(f.Definition); err != nil {
		t.Fatal(err)
	}
	return f
}
func ruleBar(id, symbol, frame string, at time.Time, o, h, l, c float64) sim.Input {
	d, _ := strategyCadenceDuration(frame)
	return sim.Input{ID: id, Type: "market.bar.close", Symbol: symbol, Source: "synthetic_acceptance", EventTime: at, AvailableAt: at.Add(d), Data: map[string]float64{"open": o, "high": h, "low": l, "price": c, "volume": 100000}, Metadata: map[string]string{"timeframe": frame}}
}
func ruleQuote(id, symbol string, at time.Time, price float64) sim.Input {
	return sim.Input{ID: id, Type: "market.quote", Symbol: symbol, Source: "synthetic_acceptance", EventTime: at, AvailableAt: at, Data: map[string]float64{"price": price, "volume": 100000}}
}
func runRuleFixture(t *testing.T, f ruleFixture, tape []sim.Input) (*sim.Engine, []sim.Output) {
	t.Helper()
	def, _, err := validateStrategyDefinition(f.Definition)
	if err != nil {
		t.Fatal(err)
	}
	tape, err = rules.ScheduleInputs(def.Program, tape)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := rules.Strategy(def.Program, f.Simulation)
	if err != nil {
		t.Fatal(err)
	}
	f.Simulation.NotifyFills = true
	e, err := sim.New(f.Simulation, tape, nil, strategy)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateRuleTape(def.Program, e.Inputs); err != nil {
		t.Fatal(err)
	}
	var outputs []sim.Output
	for !e.State.Finished {
		out, err := e.Advance()
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, out...)
	}
	return e, outputs
}
func entryOrders(e *sim.Engine) []*sim.Order {
	var out []*sim.Order
	for _, o := range e.State.Orders {
		if o.ParentID == "" && !o.ReduceOnly && o.FilledQty > 0 {
			out = append(out, o)
		}
	}
	return out
}
func closeTo(t *testing.T, want, got float64) {
	t.Helper()
	if math.Abs(want-got) > 1e-6 {
		t.Fatalf("want %.9f, got %.9f", want, got)
	}
}
func writeRuleProof(t *testing.T, f ruleFixture, e *sim.Engine, outputs []sim.Output) {
	t.Helper()
	dir := os.Getenv("RULE_PROOF_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"schema": "apteva.rule-proof/v1", "case": t.Name(), "data_class": "synthetic_acceptance", "purpose": "mechanical correctness; not historical performance or profitability evidence", "engine_source_sha256": simulationSourceHash(), "execution_source_sha256": sim.SourceHash(), "rule_source_sha256": rules.SourceHash(), "definition": f.Definition, "simulation": e.Config, "inputs": e.Inputs, "input_sha256": sim.Hash(e.Inputs), "outputs": outputs, "output_sha256": sim.Hash(outputs), "metrics": e.Metrics(), "filled_entries": len(entryOrders(e))}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, f.ID+"-"+filepath.Base(t.Name())+".json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestScreenshotDonchian(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		t.Run(side, func(t *testing.T) {
			f := loadRuleFixture(t, "donchian")
			symbol := "XAUUSD"
			at := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
			var tape []sim.Input
			for i := 0; i < 175; i++ {
				tape = append(tape, ruleBar("warm-"+time.Duration(i).String(), symbol, "1h", at.Add(time.Duration(i-175)*time.Hour), 2000, 2010, 1990, 2000))
			}
			entry, peak, trailExit, reset, target := 2012.0, 2024.0, 2021.0, 1999.0, 2034.0
			if side == "short" {
				entry, peak, trailExit, reset, target = 1988, 1976, 1979, 2001, 1966
			}
			bar := func(id string, minute int, close float64) {
				tape = append(tape, ruleBar(id, symbol, "1m", at.Add(time.Duration(minute)*time.Minute), 2000, math.Max(2000, close)+1, math.Min(2000, close)-1, close))
			}
			bar("breakout", 0, entry)
			tape = append(tape, ruleQuote("entry", symbol, at.Add(time.Minute), entry), ruleQuote("arm-trail", symbol, at.Add(2*time.Minute), peak), ruleQuote("trail-exit", symbol, at.Add(3*time.Minute), trailExit))
			bar("still-outside", 3, entry)
			tape = append(tape, ruleQuote("no-reentry", symbol, at.Add(4*time.Minute), entry))
			bar("midpoint-reset", 4, reset)
			bar("new-breakout", 5, entry)
			tape = append(tape, ruleQuote("second-entry", symbol, at.Add(6*time.Minute), entry), ruleQuote("target", symbol, at.Add(7*time.Minute), target))
			e, out := runRuleFixture(t, f, tape)
			entries := entryOrders(e)
			if len(entries) != 2 {
				t.Fatalf("expected 2 trades separated by midpoint reset, got %d", len(entries))
			}
			if len(e.State.Positions) != 0 {
				t.Fatal("protection did not close trades")
			}
			risk := entries[0].Qty * entry * .005
			if risk > 100+1e-9 || risk < 99.98 {
				t.Fatalf("rounded risk %f", risk)
			}
			direction := 1.0
			if side == "short" {
				direction = -1
			}
			closeTo(t, entries[0].Qty*(trailExit-entry)*direction+entries[1].Qty*(target-entry)*direction, e.State.RealizedPnL)
			if !entries[1].FirstFillAt.Equal(at.Add(6 * time.Minute)) {
				t.Fatal("strategy reentered before midpoint reset")
			}
			writeRuleProof(t, f, e, out)
		})
	}
}

func TestScreenshotATRCandle(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		for _, exit := range []string{"target", "stop"} {
			t.Run(side+"-"+exit, func(t *testing.T) {
				f := loadRuleFixture(t, "atr_candle")
				f.Simulation.Costs.QtyStep = 0
				at := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
				var tape []sim.Input
				for i := 0; i < 1001; i++ {
					tape = append(tape, ruleBar("warm-"+time.Duration(i).String(), "XAUUSD", "1h", at.Add(time.Duration(i-1001)*time.Hour), 2000, 2001, 1999, 2000))
				}
				entry, h, l, direction := 2009.0, 2010.0, 1999.0, 1.0
				if side == "short" {
					entry, h, l, direction = 1991, 2001, 1990, -1
				}
				tape = append(tape, ruleBar("signal", "XAUUSD", "1h", at, 2000, h, l, entry), ruleQuote("entry", "XAUUSD", at.Add(time.Hour), entry))
				price := entry * (1 + direction*.035)
				expected := 700.0
				if exit == "stop" {
					price = entry * (1 - direction*.005)
					expected = -100
				}
				tape = append(tape, ruleQuote("exit", "XAUUSD", at.Add(time.Hour+time.Minute), price))
				e, out := runRuleFixture(t, f, tape)
				if len(entryOrders(e)) != 1 || len(e.State.Positions) != 0 {
					t.Fatal("expected exactly one closed trade")
				}
				closeTo(t, expected, e.State.RealizedPnL)
				writeRuleProof(t, f, e, out)
			})
		}
	}
}

func TestScreenshotSessionRange(t *testing.T) {
	for _, side := range []string{"long", "short", "unfilled"} {
		t.Run(side, func(t *testing.T) {
			f := loadRuleFixture(t, "session_range")
			loc, _ := time.LoadLocation("Europe/Helsinki")
			start := time.Date(2026, 9, 14, 8, 0, 0, 0, loc)
			var tape []sim.Input
			for i := 0; i < 180; i++ {
				tape = append(tape, ruleBar("range-"+time.Duration(i).String(), "DE40", "1m", start.Add(time.Duration(i)*time.Minute), 16000, 16020, 15980, 16000))
			}
			end := start.Add(3 * time.Hour)
			tape = append(tape, ruleQuote("place-entries", "DE40", end, 16000))
			direction := 1.0
			entry := 16020.0
			if side == "short" {
				direction = -1
				entry = 15980
			}
			if side != "unfilled" {
				tape = append(tape, ruleQuote("breakout", "DE40", end.Add(time.Minute), entry))
			}
			deadline := time.Date(2026, 9, 14, 18, 0, 0, 0, loc)
			tape = append(tape, sim.Input{ID: "close-clock", Type: "clock", EventTime: deadline, AvailableAt: deadline}, ruleQuote("close-quote", "DE40", deadline, entry+10*direction), ruleQuote("after-deadline", "DE40", deadline.Add(time.Minute), 16021))
			e, out := runRuleFixture(t, f, tape)
			entries := entryOrders(e)
			if side == "unfilled" {
				if len(entries) != 0 {
					t.Fatal("entry filled after cancellation deadline")
				}
			} else {
				if len(entries) != 1 {
					t.Fatalf("expected 1 daily trade, got %d", len(entries))
				}
				closeTo(t, 100, entries[0].Qty*40)
				closeTo(t, 25, e.State.RealizedPnL)
			}
			if len(e.State.Positions) != 0 {
				t.Fatal("18:00 did not flatten")
			}
			for _, o := range e.State.Orders {
				if o.Status == "working" || o.Status == "partially_filled" || o.Status == "submitted" {
					t.Fatal("working order after deadline")
				}
			}
			writeRuleProof(t, f, e, out)
		})
	}
}

func TestRuleBacktestAPIAndReplay(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Rules", []string{"equity"})
	pf, err := dbGetPortfolio(ctx.AppDB(), "test-proj", pid)
	if err != nil {
		t.Fatal(err)
	}
	f := loadRuleFixture(t, "session_range")
	id, err := dbCreateStrategy(ctx.AppDB(), &Strategy{ProjectID: "test-proj", Name: "Generic session", Definition: f.Definition, Status: "active", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := dbGetStrategy(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Helsinki")
	start := time.Date(2026, 9, 14, 8, 0, 0, 0, loc)
	var tape []sim.Input
	for i := 0; i < 180; i++ {
		tape = append(tape, ruleBar(time.Duration(i).String(), "DE40", "1m", start.Add(time.Duration(i)*time.Minute), 16000, 16020, 15980, 16000))
	}
	tape = append(tape, ruleQuote("opening", "DE40", start.Add(3*time.Hour), 16000), ruleQuote("breakout", "DE40", start.Add(3*time.Hour+time.Minute), 16020), ruleQuote("stop", "DE40", start.Add(3*time.Hour+2*time.Minute), 15980))
	run, err := createRuleEventBacktest(ctx, pf, strategy, "API proof", 100000, &f.Simulation, tape)
	if err != nil {
		t.Fatal(err)
	}
	record, err := loadSimulation(ctx.AppDB(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	e, err := newSimulationEngine(record, simulationStrategy(record.Spec))
	if err != nil {
		t.Fatal(err)
	}
	var continuous []sim.Output
	for !e.State.Finished {
		out, err := e.Advance()
		if err != nil {
			t.Fatal(err)
		}
		continuous = append(continuous, out...)
	}
	e2, err := newSimulationEngine(record, simulationStrategy(record.Spec))
	if err != nil {
		t.Fatal(err)
	}
	var resumed []sim.Output
	for i := 0; i < 182; i++ {
		out, err := e2.Advance()
		if err != nil {
			t.Fatal(err)
		}
		resumed = append(resumed, out...)
	}
	checkpoint, _ := json.Marshal(e2.State)
	var state sim.State
	if err := json.Unmarshal(checkpoint, &state); err != nil {
		t.Fatal(err)
	}
	e2, err = sim.New(record.Spec.Config, record.Inputs, &state, simulationStrategy(record.Spec))
	if err != nil {
		t.Fatal(err)
	}
	for !e2.State.Finished {
		out, err := e2.Advance()
		if err != nil {
			t.Fatal(err)
		}
		resumed = append(resumed, out...)
	}
	if sim.Hash(continuous) != sim.Hash(resumed) || sim.Hash(e.State) != sim.Hash(e2.State) {
		t.Fatal("checkpoint replay differs from continuous backtest")
	}
	closeTo(t, -100, e.Metrics()["realized_pnl"])
	if err := dbSetBacktestStatus(ctx.AppDB(), run.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runEventSimulation(context.Background(), run, false); err != nil {
		t.Fatal(err)
	}
	completed, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", run.ID)
	if err != nil || completed.Status != "completed" {
		t.Fatal("worker did not finish", err)
	}
	bundle, err := simulationBundle(ctx.AppDB(), completed)
	if err != nil {
		t.Fatal(err)
	}
	if sim.Hash(bundle.Outputs) != sim.Hash(continuous) {
		t.Fatal("persisted event ledger differs from direct execution")
	}
	imported, err := importSimulation(ctx.AppDB(), "test-proj", pid, "Reproduce rule result", bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbSetBacktestStatus(ctx.AppDB(), imported.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runEventSimulation(context.Background(), imported, false); err != nil {
		t.Fatal(err)
	}
	reproduced, err := simulationBundle(ctx.AppDB(), imported)
	if err != nil {
		t.Fatal(err)
	}
	if sim.Hash(reproduced.Outputs) != sim.Hash(bundle.Outputs) {
		t.Fatal("export/import changed execution")
	}
	if _, err := (&App{}).toolStrategyAssign(ctx, map[string]any{"portfolio_id": pid, "strategy_id": id}); err == nil {
		t.Fatal("unsupported live assignment accepted")
	}
}

func TestRuleDeadlineHandlesCancellationRace(t *testing.T) {
	f := loadRuleFixture(t, "session_range")
	f.Simulation.CancellationLatencyMS = 60000
	loc, _ := time.LoadLocation("Europe/Helsinki")
	start := time.Date(2026, 9, 14, 8, 0, 0, 0, loc)
	var tape []sim.Input
	for i := 0; i < 180; i++ {
		tape = append(tape, ruleBar(time.Duration(i).String(), "DE40", "1m", start.Add(time.Duration(i)*time.Minute), 16000, 16020, 15980, 16000))
	}
	deadline := time.Date(2026, 9, 14, 18, 0, 0, 0, loc)
	tape = append(tape, ruleQuote("pending", "DE40", start.Add(3*time.Hour), 16000), sim.Input{ID: "deadline", Type: "clock", EventTime: deadline, AvailableAt: deadline}, ruleQuote("race", "DE40", deadline, 16020), ruleQuote("close-late-fill", "DE40", deadline.Add(time.Minute), 16021))
	e, _ := runRuleFixture(t, f, tape)
	if len(entryOrders(e)) != 1 || len(e.State.Positions) != 0 {
		t.Fatal("late cancellation fill escaped flatten policy")
	}
	closeTo(t, 2.5, e.State.RealizedPnL)
}

func TestRuleUnknownFieldsAndLegacyCompatibility(t *testing.T) {
	f := loadRuleFixture(t, "donchian")
	program := f.Definition["program"].(map[string]any)
	program["stop_los"] = .005
	if _, _, err := validateStrategyDefinition(f.Definition); err == nil {
		t.Fatal("misspelled rule field silently accepted")
	}
	legacy := testStrategyDefinition()
	if _, _, err := validateStrategyDefinition(legacy); err != nil {
		t.Fatal("existing allocation definition changed", err)
	}
}
