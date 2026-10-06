package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

func TestEventBacktestArtifactReproducesAndResumes(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "event replay", []string{"crypto"})
	sid := mustCreateFixedStrategy(t, ctx, "hourly BTC", "BTC-USD", 0.1)
	id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, StrategyID: sid, StrategyVersion: 1, RunKind: "strategy", Symbols: []string{"BTC-USD"}, Interval: "1h", StartingCash: 100000, TotalSteps: 8, Name: "original"})
	if err != nil {
		t.Fatal(err)
	}
	seedBacktestMarketBars(t, ctx, id, []string{"BTC-USD"}, 8)
	if err := enableEventBacktest(ctx.AppDB(), "test-proj", id, &sim.Config{Seed: 42, MaxFillQty: 2, SubmissionLatencyMS: 10, Costs: sim.Costs{FeeBps: 10}}, nil); err != nil {
		t.Fatal(err)
	}
	run, _ := dbGetBacktestRun(ctx.AppDB(), "test-proj", id)
	if _, err := startEventSimulation(run, true); err != nil {
		t.Fatal(err)
	}
	if err := dbSetBacktestStatus(ctx.AppDB(), id, "paused", ""); err != nil {
		t.Fatal(err)
	}
	record, err := loadSimulation(ctx.AppDB(), id)
	if err != nil || record.State == nil {
		t.Fatalf("checkpoint %v %v", record, err)
	}
	if err := dbSetBacktestStatus(ctx.AppDB(), id, "running", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runEventSimulation(context.Background(), run, false); err != nil {
		t.Fatal(err)
	}
	bundle, err := simulationBundle(ctx.AppDB(), run)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ResultHash == "" || len(bundle.Outputs) == 0 || bundle.Result["fees"] <= 0 {
		t.Fatalf("incomplete bundle: %+v", bundle.Result)
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var imported simulationArtifact
	if err := json.Unmarshal(raw, &imported); err != nil {
		t.Fatal(err)
	}
	clone, err := importSimulation(ctx.AppDB(), "test-proj", pid, "clone", &imported)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runStrategyBacktestToEnd(clone); err != nil {
		t.Fatal(err)
	}
	copy, err := simulationBundle(ctx.AppDB(), clone)
	if err != nil {
		t.Fatal(err)
	}
	if copy.ResultHash != bundle.ResultHash {
		t.Fatalf("artifact reproduction diverged: %s != %s", copy.ResultHash, bundle.ResultHash)
	}
	if err := enableEventBacktest(ctx.AppDB(), "test-proj", id, nil, nil); err == nil {
		t.Fatal("completed inputs were mutable")
	}
	paused, err := pauseBacktestRun(run)
	if err != nil || paused["status"] != "completed" {
		t.Fatalf("pause changed completed run: %v, %v", paused, err)
	}
	imported.Inputs[0].Data["price"]++
	if _, err := importSimulation(ctx.AppDB(), "test-proj", pid, "tampered", &imported); err == nil {
		t.Fatal("accepted modified input tape")
	}
}

func TestEventBacktestDisabledDrawdownLimit(t *testing.T) {
	policy := &replayExecutionPolicy{Allowed: map[string]bool{"BTC-USD": true}, Risk: &PortfolioRiskPolicy{MaxOrderPct: 100, MaxPositionPct: 100, MaxGrossExposurePct: 100}}
	spec := simulationSpec{Config: sim.Config{StartingCash: 1000}, Symbols: []string{"BTC-USD"}, Policy: policy}
	state := &sim.State{Cash: 900, Peak: 1000, Positions: map[string]sim.Position{}, Quotes: map[string]sim.Quote{"BTC-USD": {Price: 100}}}
	targets := []StrategyAllocation{{Symbol: "BTC-USD", Weight: 0.1}}
	commands := simulationTargets(spec, state, targets, nil)
	if len(commands) != 1 || commands[0].Order == nil {
		t.Fatalf("disabled drawdown limit prevented rebalance: %+v", commands)
	}
	policy.Risk.MaxDrawdownPct = 5
	if commands := simulationTargets(spec, state, targets, nil); len(commands) != 0 {
		t.Fatalf("enabled drawdown limit allowed rebalance: %+v", commands)
	}
}

func TestEventBacktestSentimentHasNoLookahead(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "sentiment", []string{"crypto"})
	definition := map[string]any{"universe": []string{"BTC-USD"}, "cadence": "1h", "rules": []any{map[string]any{"when": map[string]any{"symbol": "BTC-USD", "indicator": "feature:sentiment.score", "operator": ">", "value": 0.5}, "allocate": []any{map[string]any{"symbol": "BTC-USD", "weight": 0.1}}}}}
	sid, err := dbCreateStrategy(ctx.AppDB(), &Strategy{ProjectID: "test-proj", Name: "sentiment", Status: "active", Version: 1, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, StrategyID: sid, StrategyVersion: 1, RunKind: "strategy", Symbols: []string{"BTC-USD"}, Interval: "1h", StartingCash: 100000, TotalSteps: 8})
	if err != nil {
		t.Fatal(err)
	}
	seedBacktestMarketBars(t, ctx, id, []string{"BTC-USD"}, 8)
	at := time.Unix(1704067200+3600, 0).UTC()
	published := at.Add(4 * time.Hour)
	input := sim.Input{ID: "sentiment-1", Type: "feature.sentiment", Symbol: "BTC-USD", Source: "fixture", EventTime: at, AvailableAt: published, Data: map[string]float64{"score": 0.8}}
	if err := enableEventBacktest(ctx.AppDB(), "test-proj", id, nil, []sim.Input{input}); err != nil {
		t.Fatal(err)
	}
	run, _ := dbGetBacktestRun(ctx.AppDB(), "test-proj", id)
	if _, err := runStrategyBacktestToEnd(run); err != nil {
		t.Fatal(err)
	}
	record, err := loadSimulation(ctx.AppDB(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.State.Orders) == 0 {
		t.Fatal("sentiment never affected strategy")
	}
	for _, o := range record.State.Orders {
		if o.SubmittedAt.Before(published) {
			t.Fatalf("lookahead order: %+v", o)
		}
	}
}

func TestEventBacktestBackgroundDispatchAndArtifactDownload(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "background", []string{"crypto"})
	sid := mustCreateFixedStrategy(t, ctx, "background", "BTC-USD", 0.1)
	id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, StrategyID: sid, StrategyVersion: 1, RunKind: "strategy", Symbols: []string{"BTC-USD"}, Interval: "1h", StartingCash: 100000, TotalSteps: 100})
	if err != nil {
		t.Fatal(err)
	}
	seedBacktestMarketBars(t, ctx, id, []string{"BTC-USD"}, 100)
	if err := enableEventBacktest(ctx.AppDB(), "test-proj", id, nil, nil); err != nil {
		t.Fatal(err)
	}
	run, _ := dbGetBacktestRun(ctx.AppDB(), "test-proj", id)
	out, err := startEventSimulation(run, false)
	if err != nil {
		t.Fatal(err)
	}
	if out["runner_started"] != true {
		t.Fatal("background worker did not start")
	}
	if _, err := pauseBacktestRun(run); err != nil {
		t.Fatal(err)
	}
	// Explicitly pause the fresh row: the caller's queued run predates dispatch.
	if err := dbSetBacktestStatus(ctx.AppDB(), id, "paused", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		simulationWorkers.Lock()
		running := simulationWorkers.running[id]
		simulationWorkers.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not pause")
		}
		time.Sleep(time.Millisecond)
	}
	fresh, _ := dbGetBacktestRun(ctx.AppDB(), "test-proj", id)
	if fresh.Status != "paused" {
		t.Fatalf("status=%s", fresh.Status)
	}
	req := httptest.NewRequest(http.MethodGet, "/backtests/1/artifact", nil)
	w := httptest.NewRecorder()
	(&App{}).handleSimulationArtifact(w, req, fresh)
	if w.Code != 200 || w.Header().Get("Content-Disposition") == "" {
		t.Fatalf("download status=%d", w.Code)
	}
}

func TestEventBacktestRecordedOrdersAndLivePortfolioState(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "recorded decisions", []string{"equity"})
	run := &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, RunKind: "strategy", Symbols: []string{"AAPL"}, Interval: "1h", StartingCash: 1000, TotalSteps: 1}
	id, err := dbCreateBacktestRun(ctx.AppDB(), run)
	if err != nil {
		t.Fatal(err)
	}
	run.ID = id
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	inputs := []sim.Input{
		{ID: "decision", Type: "order.intent", Symbol: "AAPL", EventTime: at, AvailableAt: at, Data: map[string]float64{"qty": 2, "limit_price": 101}, Metadata: map[string]string{"side": "buy", "order_type": "limit"}},
		{ID: "q1", Type: "market.quote", Symbol: "AAPL", EventTime: at, AvailableAt: at, Data: map[string]float64{"price": 100}},
		{ID: "q2", Type: "market.quote", Symbol: "AAPL", EventTime: at.Add(time.Second), AvailableAt: at.Add(time.Second), Data: map[string]float64{"price": 100}},
	}
	spec := simulationSpec{Version: sim.Version, SourceHash: simulationSourceHash(), DecisionMode: "recorded_orders", Config: sim.Config{StartingCash: 1000, MaxFillQty: 1, BenchmarkSymbol: "AAPL"}, Symbols: run.Symbols, Interval: run.Interval}
	if err := storeSimulation(ctx.AppDB(), run, spec, inputs); err != nil {
		t.Fatal(err)
	}
	run, _ = dbGetBacktestRun(ctx.AppDB(), run.ProjectID, id)
	if _, err := runStrategyBacktestToEnd(run); err != nil {
		t.Fatal(err)
	}
	perf, err := backtestPerformance(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(perf.Positions) != 1 || perf.Positions[0].Qty != 2 || len(perf.Orders) != 1 || perf.Orders[0].Status != "filled" {
		t.Fatalf("quote-only portfolio not displayed: %+v", perf)
	}
	if perf.Current.Cash != 800 || perf.Current.Equity != 1000 {
		t.Fatalf("portfolio=%+v", perf.Current)
	}
}

func TestEventBacktestRejectsCommitAfterPause(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "atomic pause", []string{"crypto"})
	sid := mustCreateFixedStrategy(t, ctx, "atomic pause", "BTC-USD", 0.1)
	id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, StrategyID: sid, StrategyVersion: 1, RunKind: "strategy", Symbols: []string{"BTC-USD"}, Interval: "1h", StartingCash: 1000, TotalSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	seedBacktestMarketBars(t, ctx, id, []string{"BTC-USD"}, 2)
	if err := enableEventBacktest(ctx.AppDB(), "test-proj", id, nil, nil); err != nil {
		t.Fatal(err)
	}
	run, _ := dbGetBacktestRun(ctx.AppDB(), "test-proj", id)
	record, err := loadSimulation(ctx.AppDB(), id)
	if err != nil {
		t.Fatal(err)
	}
	e, err := sim.New(record.Spec.Config, record.Inputs, nil, simulationStrategy(record.Spec))
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := e.Advance()
	if err != nil {
		t.Fatal(err)
	}
	if err := dbSetBacktestStatus(ctx.AppDB(), id, "paused", ""); err != nil {
		t.Fatal(err)
	}
	if err := commitSimulation(ctx.AppDB(), run, record, e, outputs); err == nil {
		t.Fatal("paused simulation committed")
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM backtest_simulation_outputs WHERE run_id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("partially committed output ledger")
	}
}
