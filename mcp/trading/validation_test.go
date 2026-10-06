package main

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

func seedValidationStrategy(t *testing.T) (*sdk.AppCtx, *BacktestRun) {
	t.Helper()
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "validation", []string{"crypto"})
	sid := mustCreateFixedStrategy(t, ctx, "BTC allocation", "BTC-USD", .1)
	id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, StrategyID: sid, StrategyVersion: 1, RunKind: "strategy", Symbols: []string{"BTC-USD"}, Interval: "1h", StartingCash: 100000, TotalSteps: 12, Name: "source"})
	if err != nil {
		t.Fatal(err)
	}
	seedBacktestMarketBars(t, ctx, id, []string{"BTC-USD"}, 12)
	if err = enableEventBacktest(ctx.AppDB(), "test-proj", id, nil, nil); err != nil {
		t.Fatal(err)
	}
	run, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, run
}
func executeValidationTest(t *testing.T, ctx *sdk.AppCtx, s *validationSuite) *validationSuite {
	t.Helper()
	if _, err := ctx.AppDB().Exec(`UPDATE validation_suites SET status='running' WHERE id=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := runValidationSuite(context.Background(), s.ProjectID, s.ID); err != nil {
		t.Fatal(err)
	}
	out, err := readValidation(ctx.AppDB(), s.ProjectID, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "completed" {
		t.Fatalf("status=%s error=%s", out.Status, out.Error)
	}
	return out
}
func TestValidationWalkForwardIsolatedWindowsAndArtifact(t *testing.T) {
	ctx, run := seedValidationStrategy(t)
	s, err := createValidation(ctx, "test-proj", run.ID, "walk", validationConfig{Mode: "walk_forward", TrainSteps: 4, TestSteps: 2, Seed: 42})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Plan) != 8 {
		t.Fatalf("want four train/test folds, got %d", len(s.Plan))
	}
	for i := 0; i < len(s.Plan); i += 2 {
		train, test := s.Plan[i], s.Plan[i+1]
		if !train.End.Equal(test.Start) {
			t.Fatal("training crosses holdout")
		}
		if i > 0 && !s.Plan[i-1].End.Equal(test.Start) {
			t.Fatal("overlapping or missing holdout")
		}
	}
	s = executeValidationTest(t, ctx, s)
	groups := s.Report["groups"].(map[string]any)
	group := groups["out_of_sample"].(map[string]any)
	if group["completed"] != 4 {
		t.Fatalf("training contaminated OOS report: %v", group)
	}
	firstTest := s.Cases[1]
	record, err := loadSimulation(ctx.AppDB(), firstTest.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Spec.WarmupInputs) == 0 {
		t.Fatal("warmup missing")
	}
	for _, in := range record.Spec.WarmupInputs {
		if !in.AvailableAt.Before(record.Inputs[0].AvailableAt) {
			t.Fatal("future warmup input")
		}
	}
	child, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", firstTest.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if child.TotalSteps <= 0 {
		t.Fatal("validation child progress total missing")
	}
	bundle, err := simulationBundle(ctx.AppDB(), child)
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range bundle.Outputs {
		if out.At.Before(s.Plan[1].Start) {
			t.Fatal("warmup included in financial results")
		}
	}
	clone, err := importSimulation(ctx.AppDB(), "test-proj", run.PortfolioID, "replay test window", bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runStrategyBacktestToEnd(clone); err != nil {
		t.Fatal(err)
	}
	replay, err := simulationBundle(ctx.AppDB(), clone)
	if err != nil || replay.ResultHash != bundle.ResultHash {
		t.Fatalf("warmup replay mismatch %v", err)
	}
	artifact, err := validationArtifact(ctx.AppDB(), "test-proj", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact["sha256"] != sim.Hash(artifact["artifact"]) {
		t.Fatal("artifact digest incorrect")
	}
	if _, err = readValidation(ctx.AppDB(), "another-project", s.ID); err == nil {
		t.Fatal("cross-project suite leaked")
	}
	if err = forbidValidationChild(ctx.AppDB(), firstTest.RunID); err == nil {
		t.Fatal("child independent control allowed")
	}
}
func TestValidationSelectionUsesTrainingOnly(t *testing.T) {
	s := &validationSuite{Config: validationConfig{SelectionMetric: "return_pct"}, Plan: []validationCase{{Ordinal: 0, Fold: 0, Phase: "train", Candidate: 0}, {Ordinal: 1, Fold: 0, Phase: "train", Candidate: 1}, {Ordinal: 2, Fold: 0, Phase: "test", Candidate: -1}}, Cases: []validationCaseResult{{Status: "completed", Summary: map[string]any{"metrics": map[string]any{"return_pct": 2.0}}}, {Status: "completed", Summary: map[string]any{"metrics": map[string]any{"return_pct": 4.0}}}, {Status: "completed", Summary: map[string]any{"metrics": map[string]any{"return_pct": 100000.0}}}}}
	got, err := selectValidationCandidate(s, 0)
	if err != nil || got != 1 {
		t.Fatalf("selected %d %v", got, err)
	}
	s.Cases[2].Summary["metrics"] = map[string]any{"return_pct": -100000.0}
	next, err := selectValidationCandidate(s, 0)
	if err != nil || next != got {
		t.Fatal("test results affected selection")
	}
	s.Cases[0].Status = "failed"
	if _, err = selectValidationCandidate(s, 0); err == nil {
		t.Fatal("missing training silently excluded")
	}
}
func TestValidationMonteCarloSeedAndDistributions(t *testing.T) {
	ctx, run := seedValidationStrategy(t)
	cfg := validationConfig{Mode: "monte_carlo", Seed: 91, Samples: 4}
	a, err := createValidation(ctx, "test-proj", run.ID, "a", cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := createValidation(ctx, "test-proj", run.ID, "b", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a.PlanHash != b.PlanHash {
		t.Fatal("same seed gave different scenarios")
	}
	cfg.Seed++
	c, err := createValidation(ctx, "test-proj", run.ID, "c", cfg)
	if err != nil || c.PlanHash == a.PlanHash {
		t.Fatal("different seed did not change sampling")
	}
	a = executeValidationTest(t, ctx, a)
	b = executeValidationTest(t, ctx, b)
	if sim.Hash(a.Report) != sim.Hash(b.Report) {
		t.Fatal("Monte Carlo report not reproducible")
	}
	for i, r := range a.Cases {
		ra, _ := dbGetBacktestRun(ctx.AppDB(), "test-proj", r.RunID)
		rb, _ := dbGetBacktestRun(ctx.AppDB(), "test-proj", b.Cases[i].RunID)
		aa, _ := simulationBundle(ctx.AppDB(), ra)
		bb, _ := simulationBundle(ctx.AppDB(), rb)
		if aa.ResultHash != bb.ResultHash {
			t.Fatal("seeded child results diverged")
		}
	}
	group := a.Report["groups"].(map[string]any)["Source"].(map[string]any)
	if group["completed"] != 4 {
		t.Fatal("baseline was counted as a random sample")
	}
	stats := validationStats([]float64{-20, 0, 10, 30, 80})
	if stats.P50 != 10 || stats.Worst != -20 || math.Abs(stats.Mean-20) > 1e-9 || stats.P05 != -16 {
		t.Fatalf("incorrect distribution %+v", stats)
	}
}
func TestValidationStressPerturbsCostsPricesAndLiquidity(t *testing.T) {
	ctx, run := seedValidationStrategy(t)
	s, err := createValidation(ctx, "test-proj", run.ID, "stress", validationConfig{Mode: "stress"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Plan) != 6 {
		t.Fatal("missing stress scenarios")
	}
	original := sim.Hash(s.Source)
	for _, c := range s.Plan {
		spec, tape, _, err := validationCaseData(s.Source, s.Config, c, 0)
		if err != nil {
			t.Fatal(err)
		}
		switch c.Shock.Name {
		case "high costs":
			for symbol, profile := range spec.Config.SymbolCosts {
				if profile.SlippageBps <= s.Source.Spec.Config.SymbolCosts[symbol].SlippageBps {
					t.Fatal("per-symbol cost override ignored stress")
				}
			}
		case "slow execution":
			if spec.Config.SubmissionLatencyMS < s.Source.Spec.Config.SubmissionLatencyMS+5000 {
				t.Fatal("latency stress missing")
			}
		case "liquidity drought":
			if spec.Config.ParticipationRate == 0 {
				t.Fatal("liquidity limit inactive")
			}
		case "market crash":
			last := tape[len(tape)-1]
			base := s.Source.Inputs[len(s.Source.Inputs)-1]
			if math.Abs(last.Data["price"]/base.Data["price"]-.7) > 1e-8 {
				t.Fatal("crash did not affect market tape")
			}
		}
	}
	if sim.Hash(s.Source) != original {
		t.Fatal("scenario mutated baseline")
	}
	executeValidationTest(t, ctx, s)
}
func TestValidationWarmupCannotTradeOrSeeFuture(t *testing.T) {
	ctx, run := seedValidationStrategy(t)
	s, err := createValidation(ctx, "test-proj", run.ID, "warm", validationConfig{Mode: "out_of_sample", TrainSteps: 4, TestSteps: 4})
	if err != nil {
		t.Fatal(err)
	}
	spec, tape, warm, err := validationCaseData(s.Source, s.Config, s.Plan[1], 0)
	if err != nil {
		t.Fatal(err)
	}
	e, err := newSimulationEngine(&simulationRecord{Spec: spec, Inputs: tape}, simulationStrategy(spec))
	if err != nil {
		t.Fatal(err)
	}
	if e.State.Cash != spec.Config.StartingCash || len(e.State.Orders) != 0 || len(e.State.Positions) != 0 || e.State.BenchmarkQty != 0 || e.State.Cursor != 0 {
		t.Fatal("warmup changed financial state")
	}
	if len(e.State.StrategyState) == 0 || len(warm) == 0 {
		t.Fatal("warmup did not preserve indicator history")
	}
	future := cloneValidation(warm[0])
	future.ID = "future"
	future.AvailableAt = tape[0].AvailableAt
	spec.WarmupInputs = append(spec.WarmupInputs, future)
	if err := validateSimulationWarmup(spec, tape); err == nil {
		t.Fatal("future observation accepted as warmup")
	}
}
func seedValidationAgent(t *testing.T, p *eventAgentPlatform) (*sdk.AppCtx, *BacktestRun) {
	ctx, run := seedEventAgentRun(t, p)
	record, err := loadSimulation(ctx.AppDB(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	var tape []sim.Input
	for i := 0; i < 6; i++ {
		stamp := at.Add(time.Duration(i) * time.Hour)
		tape = append(tape, sim.Input{ID: strconvForTest(i), Type: "market.quote", Symbol: "AAPL", EventTime: stamp, AvailableAt: stamp, Data: map[string]float64{"price": 100 + float64(i)}})
	}
	if err = storeSimulation(ctx.AppDB(), run, record.Spec, tape); err != nil {
		t.Fatal(err)
	}
	return ctx, run
}
func strconvForTest(i int) string { raw, _ := json.Marshal(i); return "quote-" + string(raw) }
func TestValidationAgentBudgetAcrossWindows(t *testing.T) {
	p := &eventAgentPlatform{t: t}
	ctx, run := seedValidationAgent(t, p)
	s, err := createValidation(ctx, "test-proj", run.ID, "agent OOS", validationConfig{Mode: "out_of_sample", TrainSteps: 2, TestSteps: 2, MaxAgentDecisions: 3})
	if err != nil {
		t.Fatal(err)
	}
	if p.starts != 0 {
		t.Fatal("creation invoked model")
	}
	_, err = ctx.AppDB().Exec(`UPDATE validation_suites SET status='running' WHERE id=?`, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = runValidationSuite(context.Background(), s.ProjectID, s.ID)
	if err == nil || !strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("suite budget not enforced: %v", err)
	}
	if p.starts != 3 || p.destroys != 3 {
		t.Fatalf("agent calls/cleanup %d/%d", p.starts, p.destroys)
	}
	fresh, _ := readValidation(ctx.AppDB(), s.ProjectID, s.ID)
	if fresh.Cases[0].Status != "completed" || fresh.Cases[1].Status != "failed" {
		t.Fatal("budget not shared across windows")
	}
	// A held-out window starts with fresh explicit memory, even after training.
	if p.observations[2].Memory != "" {
		t.Fatal("training agent memory leaked to test")
	}
}
func TestValidationAgentPauseResumeAndRecordedReplay(t *testing.T) {
	p := &eventAgentPlatform{t: t}
	ctx, run := seedValidationAgent(t, p)
	s, err := createValidation(ctx, "test-proj", run.ID, "agent", validationConfig{Mode: "out_of_sample", TrainSteps: 2, TestSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	p.onRead = func() {
		if p.starts != 3 {
			return
		}
		p.waitForFinish = true
		p.onRead = nil
		if _, err := controlValidation("test-proj", s.ID, "pause"); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = ctx.AppDB().Exec(`UPDATE validation_suites SET status='running' WHERE id=?`, s.ID)
	if err = runValidationSuite(context.Background(), "test-proj", s.ID); err != nil {
		t.Fatal(err)
	}
	paused, err := readValidation(ctx.AppDB(), "test-proj", s.ID)
	if err != nil || paused.Status != "paused" || p.destroys != 3 {
		t.Fatal("pause did not clean up active agent")
	}
	if paused.Cases[0].Status != "completed" || paused.Cases[1].Summary["processed_events"] != float64(0) {
		t.Fatal("unfinished decision committed")
	}
	p.waitForFinish = false
	s = executeValidationTest(t, ctx, s)
	calls := p.starts
	if calls != 5 {
		t.Fatal("resume repeated completed training decisions")
	}
	for _, r := range s.Cases {
		child, _ := dbGetBacktestRun(ctx.AppDB(), s.ProjectID, r.RunID)
		artifact, err := simulationBundle(ctx.AppDB(), child)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := importSimulation(ctx.AppDB(), s.ProjectID, s.PortfolioID, "recorded", artifact)
		if err != nil {
			t.Fatal(err)
		}
		_ = dbSetBacktestStatus(ctx.AppDB(), replay.ID, "running", "")
		if _, err = runEventSimulation(context.Background(), replay, false); err != nil {
			t.Fatal(err)
		}
		got, err := simulationBundle(ctx.AppDB(), replay)
		if err != nil || got.ResultHash != artifact.ResultHash {
			t.Fatal("agent validation result replay differs")
		}
	}
	if p.starts != calls {
		t.Fatal("recorded validation replay invoked model")
	}
}
func TestValidationParameterGridAndInvalidPlans(t *testing.T) {
	ctx, run := seedValidationStrategy(t)
	r, _ := loadSimulation(ctx.AppDB(), run.ID)
	source := validationSource{Spec: r.Spec, Inputs: r.Inputs, InputHash: r.InputHash}
	grid := map[string][]any{"rules.0.allocate.0.weight": {.05, .1, .2}}
	cfg := validationConfig{Mode: "robustness", ParameterGrid: grid}
	plan, err := planValidation(source, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Candidates) != 3 || len(plan) != 9 {
		t.Fatal("parameter sweep not expanded")
	}
	if _, err := validationGrid(source.Spec.Strategy, map[string][]any{"missing": {1}}); err == nil {
		t.Fatal("unknown parameter ignored")
	}
	for _, bad := range []validationConfig{{Mode: "walk_forward", TrainSteps: 4, TestSteps: 3, StepSteps: 2}, {Mode: "monte_carlo", Samples: 501}, {Mode: "stress", Scenarios: []validationShock{{Name: "invalid", PriceShockPct: -100}}}} {
		if _, err := planValidation(source, &bad); err == nil {
			t.Fatalf("invalid configuration accepted %+v", bad)
		}
	}
}

func TestValidationBackgroundControlsAndCancellation(t *testing.T) {
	ctx, run := seedValidationStrategy(t)
	s, err := createValidation(ctx, "test-proj", run.ID, "background", validationConfig{Mode: "out_of_sample", TrainSteps: 4, TestSteps: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = controlValidation("wrong-project", s.ID, "run"); err == nil {
		t.Fatal("cross-project control allowed")
	}
	if _, err = controlValidation("test-proj", s.ID, "run"); err != nil {
		t.Fatal(err)
	}
	validationWorkers.Lock()
	worker := validationWorkers.entries[s.ID]
	validationWorkers.Unlock()
	if worker != nil {
		select {
		case <-worker.done:
		case <-time.After(10 * time.Second):
			t.Fatal("background validation did not finish")
		}
	}
	finished, err := readValidation(ctx.AppDB(), "test-proj", s.ID)
	if err != nil || finished.Status != "completed" {
		t.Fatalf("background completion %+v %v", finished, err)
	}
	cancelled, err := createValidation(ctx, "test-proj", run.ID, "cancel", validationConfig{Mode: "stress"})
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := controlValidation("test-proj", cancelled.ID, "cancel")
	if err != nil || stopped.Status != "cancelled" {
		t.Fatal("queued cancellation failed")
	}
	if _, err = controlValidation("test-proj", cancelled.ID, "run"); err == nil {
		t.Fatal("cancelled suite restarted")
	}
	for _, r := range stopped.Cases {
		if r.RunID != 0 {
			t.Fatal("cancelled queued suite created financial runs")
		}
	}
}

func TestValidationDelayedFeaturesRespectWindowAvailability(t *testing.T) {
	ctx, run := seedValidationStrategy(t)
	record, _ := loadSimulation(ctx.AppDB(), run.ID)
	source := validationSource{Spec: record.Spec, Inputs: record.Inputs, InputHash: record.InputHash}
	cfg := validationConfig{Mode: "out_of_sample", TrainSteps: 4, TestSteps: 4}
	plan, err := planValidation(source, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	arrived := plan[1].Start.Add(time.Second)
	source.Inputs = append(source.Inputs, sim.Input{ID: "late-sentiment", Type: "feature.sentiment", Symbol: "BTC-USD", EventTime: plan[0].Start, AvailableAt: arrived, Data: map[string]float64{"score": .9}})
	canonical, err := sim.New(source.Spec.Config, source.Inputs, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	source.Inputs = canonical.Inputs
	_, train, _, err := validationCaseData(source, cfg, plan[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range train {
		if in.ID == "late-sentiment" {
			t.Fatal("training saw unavailable feature")
		}
	}
	_, test, warm, err := validationCaseData(source, cfg, plan[1], 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range warm {
		if in.ID == "late-sentiment" {
			t.Fatal("warmup saw future feature")
		}
	}
	found := false
	for _, in := range test {
		if in.ID == "late-sentiment" {
			found = true
			if !in.AvailableAt.Equal(arrived) {
				t.Fatal("feature arrival shifted into past")
			}
		}
	}
	if !found {
		t.Fatal("test lost generic feature")
	}
}

func TestValidationShutdownPausesAndDrainsAgentWorker(t *testing.T) {
	p := &eventAgentPlatform{t: t, waitForFinish: true}
	ctx, run := seedValidationAgent(t, p)
	defer func() {
		_ = stopValidationWorkers(ctx.AppDB())
		validationWorkers.Lock()
		validationWorkers.stopping = false
		validationWorkers.Unlock()
	}()
	s, err := createValidation(ctx, "test-proj", run.ID, "shutdown", validationConfig{Mode: "out_of_sample", TrainSteps: 2, TestSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan struct{}, 1)
	p.onRead = func() {
		select {
		case observed <- struct{}{}:
		default:
		}
	}
	if _, err = controlValidation("test-proj", s.ID, "run"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not start")
	}
	if err = stopValidationWorkers(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	fresh, err := readValidation(ctx.AppDB(), "test-proj", s.ID)
	if err != nil || fresh.Status != "paused" || fresh.Cases[0].Status != "paused" || p.destroys != 1 {
		t.Fatalf("shutdown failed to drain/retain paused state: %+v %v", fresh, err)
	}
	if fresh.Cases[0].Summary["processed_events"] != float64(0) {
		t.Fatal("shutdown committed an unfinished decision")
	}
}
