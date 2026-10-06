package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

type eventAgentPlatform struct {
	sdk.PlatformClient
	t             *testing.T
	box           agentMailbox
	starts        int
	destroys      int
	observations  []agentObservation
	waitForFinish bool
	onRead        func()
}

func (p *eventAgentPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{InstallID: 7}, nil
}
func (p *eventAgentPlatform) CreateEnvironment(req sdk.EnvironmentCreateRequest) (*sdk.EnvironmentSummary, error) {
	if req.Mode != sdk.EnvironmentModeBlock || len(req.ConnectionIDs) != 0 {
		p.t.Fatal("agent replay enabled external connections")
	}
	return &sdk.EnvironmentSummary{ID: "isolated"}, nil
}
func (p *eventAgentPlatform) DestroyEnvironment(string) error { p.destroys++; return nil }
func (p *eventAgentPlatform) SpawnEnvironmentAgent(_ string, req sdk.EnvironmentAgentSpawnRequest) (*sdk.EnvironmentAgent, error) {
	p.starts++
	if req.SourceAgentID != 123 {
		p.t.Fatal("wrong source agent")
	}
	return &sdk.EnvironmentAgent{AgentID: 456}, nil
}
func (p *eventAgentPlatform) SendEvent(int64, string) error { return nil }
func (p *eventAgentPlatform) CallEnvironmentAppResult(_ string, app, tool string, args map[string]any, out any) error {
	if app != "trading" || tool != "backtest_agent_exchange" {
		p.t.Fatalf("unexpected tool %s/%s", app, tool)
	}
	if args["operation"] == "stage" {
		p.box = agentMailbox{}
		raw, _ := json.Marshal(args["turn"])
		if err := json.Unmarshal(raw, &p.box); err != nil {
			return err
		}
		p.observations = append(p.observations, p.box.Observation)
		return nil
	}
	if p.onRead != nil {
		p.onRead()
	}
	p.box.Done = !p.waitForFinish
	p.box.Memory = "remember " + p.box.Observation.Event.ID
	p.box.Rationale = "Evaluate only the information that has arrived."
	if p.box.Observation.Event.Type == "feature.sentiment" {
		p.box.Commands = []sim.Command{{Order: &sim.Order{Symbol: "AAPL", Side: "buy", Type: "market", Qty: 2, TIF: "gtc"}}}
	}
	raw, _ := json.Marshal(p.box)
	return json.Unmarshal(raw, out)
}
func seedEventAgentRun(t *testing.T, p *eventAgentPlatform) (*sdk.AppCtx, *BacktestRun) {
	ctx := newHardeningCtx(t, p)
	pid := mustCreatePortfolio(t, ctx, "event agent", []string{"equity"})
	run := &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, Name: "agent", RunKind: "agent", SourceAgentID: 123, StartingCash: 1000, Symbols: []string{"AAPL"}, Interval: "1m", Status: "queued", Summary: map[string]any{}}
	id, err := dbCreateBacktestRun(ctx.AppDB(), run)
	if err != nil {
		t.Fatal(err)
	}
	run.ID = id
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	tape := []sim.Input{
		{ID: "quote-1", Type: "market.quote", Symbol: "AAPL", EventTime: at, AvailableAt: at, Data: map[string]float64{"price": 100}},
		{ID: "sentiment", Type: "feature.sentiment", Symbol: "AAPL", EventTime: at, AvailableAt: at.Add(time.Minute), Data: map[string]float64{"score": 0.8}},
		{ID: "quote-2", Type: "market.quote", Symbol: "AAPL", EventTime: at.Add(2 * time.Minute), AvailableAt: at.Add(2 * time.Minute), Data: map[string]float64{"price": 110}},
		{ID: "news", Type: "news.article", Symbol: "AAPL", EventTime: at.Add(2 * time.Minute), AvailableAt: at.Add(3 * time.Minute), Metadata: map[string]string{"headline": "An earnings announcement"}},
	}
	spec := simulationSpec{Version: sim.Version, SourceHash: simulationSourceHash(), DecisionMode: "agent", Config: sim.Config{StartingCash: 1000, Seed: 42, MaxFillQty: 1, BenchmarkSymbol: "AAPL"}, Symbols: run.Symbols, Interval: "1m", Agent: &agentSimulationConfig{Directive: "Use prices, sentiment and news to decide."}}
	if err := storeSimulation(ctx.AppDB(), run, spec, tape); err != nil {
		t.Fatal(err)
	}
	run, err = dbGetBacktestRun(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, run
}

func TestAgentEventReplayUsesSameEngineAndRecordedDecisions(t *testing.T) {
	p := &eventAgentPlatform{t: t}
	ctx, run := seedEventAgentRun(t, p)
	if err := dbSetBacktestStatus(ctx.AppDB(), run.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runEventSimulation(context.Background(), run, false); err != nil {
		t.Fatal(err)
	}
	if p.starts != 5 || p.destroys != 5 {
		t.Fatalf("turns starts=%d cleanup=%d", p.starts, p.destroys)
	}
	for _, o := range p.observations {
		for _, in := range o.History {
			if in.AvailableAt.After(o.At) {
				t.Fatal("future input leaked")
			}
		}
	}
	if p.observations[0].State.Features["AAPL/sentiment"] != nil {
		t.Fatal("future sentiment leaked")
	}
	if p.observations[1].Memory != "remember quote-1" {
		t.Fatal("agent memory did not survive turn")
	}
	bundle, err := simulationBundle(ctx.AppDB(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.AgentDecisions) != 5 || bundle.AgentDecisionsHash == "" || bundle.ResultHash == "" {
		t.Fatal("agent recording absent")
	}
	if bundle.Result["cash"] != 890 {
		t.Fatalf("partial fill engine not used: %v", bundle.Result)
	}
	clone, err := importSimulation(ctx.AppDB(), "test-proj", run.PortfolioID, "exact recorded replay", bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbSetBacktestStatus(ctx.AppDB(), clone.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runEventSimulation(context.Background(), clone, false); err != nil {
		t.Fatal(err)
	}
	reproduced, err := simulationBundle(ctx.AppDB(), clone)
	if err != nil {
		t.Fatal(err)
	}
	if reproduced.ResultHash != bundle.ResultHash {
		t.Fatalf("agent replay diverged %s vs %s", reproduced.ResultHash, bundle.ResultHash)
	}
	if p.starts != 5 {
		t.Fatal("recorded replay queried the agent")
	}
	perf, err := backtestPerformance(run)
	if err != nil || perf.Current == nil || perf.Current.Cash != 890 {
		t.Fatalf("live portfolio snapshot %v %v", perf, err)
	}
	bundle.AgentDecisions[0].Memory = "tampered"
	if _, err := importSimulation(ctx.AppDB(), "test-proj", run.PortfolioID, "tamper", bundle); err == nil {
		t.Fatal("tampered recording accepted")
	}
	// Even a correctly hashed bundle must contain valid trading commands.
	bundle.AgentDecisions[0].Commands = []sim.Command{{Order: &sim.Order{Symbol: "AAPL", Side: "buy", Type: "market", Qty: -1, TIF: "gtc"}}}
	bundle.AgentDecisionsHash = sim.Hash(bundle.AgentDecisions)
	if _, err := importSimulation(ctx.AppDB(), "test-proj", run.PortfolioID, "invalid command", bundle); err == nil {
		t.Fatal("invalid recorded command accepted")
	}
}

func TestAgentReplayDecisionSurvivesUncommittedStep(t *testing.T) {
	p := &eventAgentPlatform{t: t}
	ctx, run := seedEventAgentRun(t, p)
	dbSetBacktestStatus(ctx.AppDB(), run.ID, "running", "")
	r, err := loadSimulation(ctx.AppDB(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	e, err := sim.New(r.Spec.Config, r.Inputs, nil, agentSimulationStrategy(context.Background(), run, r))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Advance(); err != nil {
		t.Fatal(err)
	} // simulate a crash before checkpoint commit
	if p.starts != 1 {
		t.Fatal("agent did not run")
	}
	if _, err := runEventSimulation(context.Background(), run, true); err != nil {
		t.Fatal(err)
	}
	if p.starts != 1 {
		t.Fatal("completed decision was regenerated on resume")
	}
}

func TestAgentReplayPauseAndCancelDiscardUnfinishedDecision(t *testing.T) {
	for _, status := range []string{"paused", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			p := &eventAgentPlatform{t: t, waitForFinish: true}
			ctx, run := seedEventAgentRun(t, p)
			if err := dbSetBacktestStatus(ctx.AppDB(), run.ID, "running", ""); err != nil {
				t.Fatal(err)
			}
			p.onRead = func() {
				if err := dbSetBacktestStatus(ctx.AppDB(), run.ID, status, ""); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := runEventSimulation(context.Background(), run, false); err != nil {
				t.Fatal(err)
			}
			fresh, err := dbGetBacktestRun(ctx.AppDB(), run.ProjectID, run.ID)
			if err != nil || fresh.Status != status || fresh.EnvironmentID != "" || fresh.Summary["agent_waiting"] != false || p.destroys != 1 {
				t.Fatalf("unfinished turn not cleaned up: %+v %v", fresh, err)
			}
			var decisions, outputs int
			if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM backtest_agent_decisions WHERE run_id=?`, run.ID).Scan(&decisions); err != nil {
				t.Fatal(err)
			}
			if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM backtest_simulation_outputs WHERE run_id=?`, run.ID).Scan(&outputs); err != nil {
				t.Fatal(err)
			}
			if decisions != 0 || outputs != 0 || fresh.CurrentStep != 0 {
				t.Fatal("unfinished decision or financial effects were committed")
			}
			if status == "paused" {
				p.onRead, p.waitForFinish = nil, false
				if err := dbSetBacktestStatus(ctx.AppDB(), run.ID, "running", ""); err != nil {
					t.Fatal(err)
				}
				if _, err := runEventSimulation(context.Background(), fresh, true); err != nil {
					t.Fatal(err)
				}
				if p.starts != 2 || p.observations[1].DecisionID != p.observations[0].DecisionID {
					t.Fatal("resume did not retry the interrupted event")
				}
			}
		})
	}
}

func TestAgentReplayToolsAreIsolatedAndFinishIsExplicit(t *testing.T) {
	t.Setenv("APTEVA_ENVIRONMENT_ID", "isolated")
	ctx := newTestCtx(t)
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	state := &sim.State{Cash: 1000, Positions: map[string]sim.Position{}, Quotes: map[string]sim.Quote{"AAPL": {Price: 100, At: at}}, Features: map[string]map[string]float64{}}
	box := agentMailbox{Project: "isolated", Seed: 7, Symbols: []string{"AAPL"}, Observation: agentObservation{DecisionID: "q1", PortfolioID: 1, At: at, State: state, History: []sim.Input{{ID: "q1", Type: "market.quote", AvailableAt: at}}}}
	if _, err := (&App{}).toolAgentExchange(ctx, map[string]any{"operation": "stage", "turn": box}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"portfolio_create", "market_calendar", "strategy_backtest_create", "backtest_market_step"} {
		if _, handled, err := replayAgentTool(ctx, name, nil); !handled || err == nil {
			t.Fatalf("%s escaped replay", name)
		}
	}
	if _, _, err := replayAgentTool(ctx, "market_quote", map[string]any{"symbol": "MSFT"}); err == nil {
		t.Fatal("unknown price fell through to live provider")
	}
	args := map[string]any{"portfolio_id": 1, "symbol": "AAPL", "side": "buy", "type": "market", "qty": 1, "rationale": "Buy one share based only on the available evidence.", "idempotency_key": "order-1"}
	first, _, err := replayAgentTool(ctx, "order_place", args)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := replayAgentTool(ctx, "order_place", args)
	if err != nil || sim.Hash(first) != sim.Hash(again) {
		t.Fatal("staged order retry duplicated")
	}
	b, err := readAgentMailbox(ctx.AppDB())
	if err != nil || b.Done || len(b.Commands) != 1 {
		t.Fatalf("turn incorrectly finished %v %v", b, err)
	}
	finish := map[string]any{"decision_id": "wrong", "memory": "m", "rationale": "done"}
	if _, _, err := replayAgentTool(ctx, "backtest_decision_finish", finish); err == nil {
		t.Fatal("stale turn finished")
	}
	finish["decision_id"] = "q1"
	if _, _, err := replayAgentTool(ctx, "backtest_decision_finish", finish); err != nil {
		t.Fatal(err)
	}
	if _, _, err := replayAgentTool(ctx, "order_cancel", map[string]any{"order_id": "anything"}); err == nil {
		t.Fatal("closed decision mutated")
	}
	b, _ = readAgentMailbox(ctx.AppDB())
	if !b.Done || b.Memory != "m" {
		t.Fatal("memory not sealed")
	}
	var orders int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&orders)
	if orders != 0 {
		t.Fatal("replay orders touched actual portfolio")
	}
}

func TestRecordedAgentReplayFailsClosedOnMissingDecision(t *testing.T) {
	p := &eventAgentPlatform{t: t}
	ctx, run := seedEventAgentRun(t, p)
	run.Summary["agent_replay_only"] = true
	dbSetBacktestStatus(ctx.AppDB(), run.ID, "running", "")
	_, err := runEventSimulation(context.Background(), run, false)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("missing decision accepted")
	}
	if p.starts != 0 {
		t.Fatal("recorded run invoked agent")
	}
}

func (p *eventAgentPlatform) GetAgent(id int64) (*sdk.PlatformAgent, error) {
	return &sdk.PlatformAgent{ID: id, Name: "Research trader", ProjectID: "test-proj"}, nil
}

func TestAgentSimulationCreateAcceptsGenericTapeWithoutMarketFetch(t *testing.T) {
	p := &eventAgentPlatform{t: t}
	ctx, old := seedEventAgentRun(t, p)
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	out, err := (&App{}).toolAgentBacktestCreate(ctx, map[string]any{"_project_id": "test-proj", "portfolio_id": old.PortfolioID, "agent_id": 123, "agent_simulation": map[string]any{"directive": "Trade only on the supplied events", "trigger_types": []string{"news.*", "execution.fill"}}, "inputs": []sim.Input{{ID: "q", Type: "market.quote", Symbol: "AAPL", EventTime: at, AvailableAt: at, Data: map[string]float64{"price": 100}}, {ID: "n", Type: "news.article", Symbol: "AAPL", EventTime: at, AvailableAt: at.Add(time.Minute), Metadata: map[string]string{"headline": "A new product announcement"}}}})
	if err != nil {
		t.Fatal(err)
	}
	run := out.(map[string]any)["backtest"].(*BacktestRun)
	if run.Status != "queued" || run.RunKind != "agent" || !eventBacktest(run) || p.starts != 0 {
		t.Fatalf("invalid creation %+v starts=%d", run, p.starts)
	}
	record, err := loadSimulation(ctx.AppDB(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Spec.Agent.SourceName != "Research trader" || len(record.Inputs) != 2 || record.Inputs[1].Metadata["headline"] == "" {
		t.Fatal("feed or agent provenance lost")
	}
}

func TestAgentDecisionTriggerSelectionIncludesExecutions(t *testing.T) {
	c := &agentSimulationConfig{TriggerTypes: []string{"news.*", "execution.fill"}}
	for _, kind := range []string{"news.article", "execution.fill"} {
		if !agentTriggered(c, sim.Input{Type: kind}) {
			t.Fatal("configured trigger missed")
		}
	}
	for _, kind := range []string{"market.quote", "order.intent", "feature.sentiment"} {
		if agentTriggered(c, sim.Input{Type: kind}) {
			t.Fatal("unsubscribed event triggered decision")
		}
	}
}
