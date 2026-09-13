package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

type simulationSpec struct {
	Agent             *agentSimulationConfig `json:"agent,omitempty"`
	DecisionMode      string                 `json:"decision_mode,omitempty"`
	SourceHash        string                 `json:"engine_source_sha256"`
	Version           string                 `json:"engine_version"`
	Config            sim.Config             `json:"config"`
	Strategy          map[string]any         `json:"strategy"`
	StrategyVersion   int                    `json:"strategy_version"`
	Symbols           []string               `json:"symbols"`
	Interval          string                 `json:"interval"`
	ReferenceManifest map[string]any         `json:"reference_manifest,omitempty"`
	Policy            *replayExecutionPolicy `json:"execution_policy,omitempty"`
}

// Pin local builds as well as release builds in portable artifacts.
//
//go:embed agent_event_backtest.go internal/backtest/engine.go event_backtest.go strategy.go strategy_replay.go market_calendar.go pricing.go integrity.go exec.go
var simulationSources embed.FS

func simulationSourceHash() string {
	files := map[string]string{}
	for _, name := range []string{"agent_event_backtest.go", "internal/backtest/engine.go", "event_backtest.go", "strategy.go", "strategy_replay.go", "market_calendar.go", "pricing.go", "integrity.go", "exec.go"} {
		data, _ := simulationSources.ReadFile(name)
		files[name] = string(data)
	}
	return sim.Hash(files)
}

type simulationRecord struct {
	Spec      simulationSpec
	Inputs    []sim.Input
	State     *sim.State
	Revision  int
	InputHash string
}

type simulationArtifact struct {
	AgentDecisions     []agentDecision    `json:"agent_decisions,omitempty"`
	AgentDecisionsHash string             `json:"agent_decisions_sha256,omitempty"`
	Schema             string             `json:"schema"`
	Spec               simulationSpec     `json:"spec"`
	Inputs             []sim.Input        `json:"inputs"`
	InputHash          string             `json:"input_sha256"`
	Outputs            []sim.Output       `json:"outputs,omitempty"`
	Result             map[string]float64 `json:"result,omitempty"`
	ResultHash         string             `json:"result_sha256,omitempty"`
}

type simulationStrategyState struct {
	Targets     []StrategyAllocation `json:"targets,omitempty"`
	History     map[string][]float64 `json:"history"`
	Prices      map[string]float64   `json:"prices"`
	AsOf        time.Time            `json:"as_of"`
	SignalStep  int                  `json:"signal_step"`
	SignalCount int                  `json:"signal_count"`
	Step        int                  `json:"step"`
	Day         string               `json:"day"`
	DayEquity   float64              `json:"day_equity"`
}

func eventBacktest(run *BacktestRun) bool {
	return run != nil && run.Summary["engine_version"] == sim.Version
}

func enableEventBacktest(db *sql.DB, project string, id int64, options *sim.Config, extra []sim.Input) error {
	return enableSimulation(db, project, id, options, extra, nil)
}
func enableSimulation(db *sql.DB, project string, id int64, options *sim.Config, extra []sim.Input, agent *agentSimulationConfig) error {
	run, err := dbGetBacktestRun(db, project, id)
	if err != nil {
		return err
	}
	var strategy *Strategy
	var def *StrategyDefinition
	if run.RunKind == "agent" {
		if err := normalizeAgentSimulation(agent, run); err != nil {
			return err
		}
		def = &StrategyDefinition{Universe: run.Symbols, Cadence: run.Interval}
		strategy = &Strategy{}
	} else {
		strategy, err = dbGetStrategyVersion(db, project, run.StrategyID, run.StrategyVersion)
		if err != nil {
			return err
		}
		def, _, err = validateStrategyDefinition(strategy.Definition)
		if err != nil {
			return err
		}
	}
	config := sim.Config{Seed: 1, StartingCash: run.StartingCash, Costs: sim.Costs{FeeBps: run.FeeBps, SlippageBps: run.SlippageBps, QtyStep: 0.0001}, BenchmarkSymbol: run.Symbols[0]}
	if options != nil {
		config = *options
		config.StartingCash = run.StartingCash
		if config.BenchmarkSymbol == "" {
			config.BenchmarkSymbol = run.Symbols[0]
		}
		config.Costs.FeeBps = math.Max(config.Costs.FeeBps, run.FeeBps)
		config.Costs.SlippageBps = math.Max(config.Costs.SlippageBps, run.SlippageBps)
	}
	if !contains(run.Symbols, config.BenchmarkSymbol) {
		return errors.New("benchmark must be included in the captured symbols")
	}
	policy := decodeReplayPolicy(run)
	if policy != nil {
		p := policy.Risk
		config.Risk = sim.RiskLimits{MaxOrderPct: p.MaxOrderPct, MaxPositionPct: p.MaxPositionPct, MaxGrossExposurePct: p.MaxGrossExposurePct, MaxDailyLossPct: p.MaxDailyLossPct, MaxDrawdownPct: p.MaxDrawdownPct}
	}
	config.SymbolCosts = map[string]sim.Costs{}
	if policy != nil {
		for symbol, p := range policy.Profiles {
			c := config.Costs
			c.FeeBps = math.Max(c.FeeBps, p.TakerFeeBps)
			c.SlippageBps = math.Max(c.SlippageBps, p.SlippageBps)
			c.SpreadBps = math.Max(c.SpreadBps, p.FallbackSpreadBps)
			c.QtyStep = math.Max(0.0001, p.QtyStep)
			c.MinQty = p.MinQty
			c.MinNotional = p.MinNotional
			config.SymbolCosts[symbol] = c
		}
	}
	spec := simulationSpec{SourceHash: simulationSourceHash(), Version: sim.Version, Config: config, Strategy: strategy.Definition, StrategyVersion: strategy.Version, Symbols: run.Symbols, Interval: run.Interval, Policy: policy, ReferenceManifest: run.ReferenceManifest}
	if agent != nil {
		spec.Agent = agent
		spec.DecisionMode = "agent"
	}
	bars, err := dbBacktestMarketHistory(db, id, run.TotalSteps)
	if err != nil {
		return err
	}
	inputs := make([]sim.Input, 0, 2*len(bars)+len(extra))
	previousVolume := map[string]float64{}
	for _, bar := range bars {
		at := time.Unix(bar.T, 0).UTC()
		closeAt := simulationBarClose(def, run.Interval, at)
		if bar.Step > 0 {
			inputs = append(inputs, sim.Input{ID: fmt.Sprintf("bar/%09d/%s/open", bar.Step, bar.Symbol), Type: "market.quote", Symbol: bar.Symbol, Source: bar.Source, EventTime: at, AvailableAt: at, Data: map[string]float64{"price": bar.O, "volume": previousVolume[bar.Symbol], "step": float64(bar.Step)}, Metadata: map[string]string{"liquidity_model": "previous_completed_bar_volume"}})
		}
		inputs = append(inputs, sim.Input{ID: fmt.Sprintf("bar/%09d/%s/close", bar.Step, bar.Symbol), Type: "market.bar.close", Symbol: bar.Symbol, Source: bar.Source, EventTime: at, AvailableAt: closeAt, Data: map[string]float64{"price": bar.C, "volume": bar.V, "step": float64(bar.Step)}})
		previousVolume[bar.Symbol] = bar.V
	}
	for _, in := range extra {
		if strings.HasPrefix(in.Type, "market.") {
			return errors.New("additional inputs must be feature or custom events; use artifact import for a complete custom market tape")
		}
	}
	inputs = append(inputs, extra...)
	return storeSimulation(db, run, spec, inputs)
}

func simulationBarClose(def *StrategyDefinition, interval string, at time.Time) time.Time {
	end := at.Add(backtestIntervalDuration(interval))
	if !strategyHasStocks(def) {
		return end
	}
	session := usEquitySessionAt(at)
	if interval == "1w" {
		local := at.In(mustEquityLocation())
		monday := local.AddDate(0, 0, -(int(local.Weekday())+6)%7)
		for i := 0; i < 5; i++ {
			s := usEquitySessionAt(monday.AddDate(0, 0, i))
			if s.OpenDay {
				end = s.Close
			}
		}
	} else if session.OpenDay && (interval == "1d" || end.After(session.Close)) {
		end = session.Close
	}
	return end.UTC()
}

func mustEquityLocation() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}

func storeSimulation(db *sql.DB, run *BacktestRun, spec simulationSpec, inputs []sim.Input) error {
	if spec.DecisionMode != "" && spec.DecisionMode != "strategy" && spec.DecisionMode != "recorded_orders" && spec.DecisionMode != "agent" {
		return errors.New("unknown simulation decision mode")
	}
	if len(spec.Symbols) == 0 {
		return errors.New("simulation requires captured symbols")
	}
	if spec.Policy != nil && spec.Policy.Risk == nil {
		return errors.New("captured execution policy requires risk limits")
	}

	if spec.Version != sim.Version || spec.SourceHash != simulationSourceHash() {
		return errors.New("unsupported simulation engine version")
	}
	if spec.DecisionMode == "agent" {
		spec.Config.NotifyFills = true
		if err := normalizeAgentSimulation(spec.Agent, run); err != nil {
			return err
		}
	} else if spec.DecisionMode != "recorded_orders" {
		if _, _, err := validateStrategyDefinition(spec.Strategy); err != nil {
			return err
		}
	}
	if len(inputs) > 2*defaultBacktestMaxMarketRows+100000 {
		return errors.New("simulation input budget exceeded")
	}
	engine, err := sim.New(spec.Config, inputs, nil, nil)
	if err != nil {
		return err
	}
	inputs = engine.Inputs
	if spec.DecisionMode == "agent" {
		quotes := map[string]bool{}
		for _, in := range inputs {
			if in.Type == "order.intent" || in.Type == "order.cancel_intent" {
				return errors.New("agent runs cannot contain external order intents")
			}
			if strings.HasPrefix(in.Type, "market.") && !contains(spec.Symbols, in.Symbol) {
				return errors.New("market input outside captured agent universe")
			}
			if in.Type == "market.quote" {
				quotes[in.Symbol] = true
			}
		}
		if len(quotes) == 0 || !quotes[spec.Config.BenchmarkSymbol] {
			return errors.New("agent simulation requires market quotes including its benchmark")
		}
	}
	hash := sim.Hash(struct {
		Spec   simulationSpec `json:"spec"`
		Inputs []sim.Input    `json:"inputs"`
	}{spec, inputs})
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	inputJSON, err := json.Marshal(inputs)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(`SELECT status FROM backtest_runs WHERE id=?`, run.ID).Scan(&status); err != nil {
		return err
	}
	if status != "queued" {
		return errors.New("simulation inputs are immutable after start")
	}
	if _, err = tx.Exec(`INSERT INTO backtest_simulations(run_id,spec_json,inputs_json,input_sha256) VALUES(?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET spec_json=excluded.spec_json,inputs_json=excluded.inputs_json,input_sha256=excluded.input_sha256 WHERE backtest_simulations.state_json IS NULL`, run.ID, string(specJSON), string(inputJSON), hash); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM backtest_agent_decisions WHERE run_id=?`, run.ID); err != nil {
		return err
	}
	if spec.DecisionMode == "agent" {
		if _, err := tx.Exec(`UPDATE backtest_runs SET total_steps=? WHERE id=?`, len(inputs), run.ID); err != nil {
			return err
		}
	}
	meta, _ := json.Marshal(map[string]any{"decision_mode": spec.DecisionMode, "engine_version": sim.Version, "input_sha256": hash, "input_events": len(inputs), "processed_events": 0, "benchmark_symbol": spec.Config.BenchmarkSymbol})
	if _, err = tx.Exec(`UPDATE backtest_runs SET summary_json=json_patch(summary_json,?) WHERE id=?`, string(meta), run.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func loadSimulation(db *sql.DB, id int64) (*simulationRecord, error) {
	var spec, inputs string
	var state sql.NullString
	r := &simulationRecord{}
	if err := db.QueryRow(`SELECT spec_json,inputs_json,state_json,revision,input_sha256 FROM backtest_simulations WHERE run_id=?`, id).Scan(&spec, &inputs, &state, &r.Revision, &r.InputHash); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(spec), &r.Spec); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(inputs), &r.Inputs); err != nil {
		return nil, err
	}
	if state.Valid {
		if err := json.Unmarshal([]byte(state.String), &r.State); err != nil {
			return nil, err
		}
	}
	if r.Spec.Version != sim.Version || r.Spec.SourceHash != simulationSourceHash() {
		return nil, errors.New("simulation checkpoint requires another engine version")
	}
	return r, nil
}

func updateSimulationInputs(db *sql.DB, run *BacktestRun, options *sim.Config, extra []sim.Input) error {
	if run.Summary["agent_replay_only"] == true {
		return errors.New("recorded agent replay inputs are immutable; create a new agent run to change the scenario")
	}
	r, err := loadSimulation(db, run.ID)
	if err != nil {
		return err
	}
	if options != nil {
		// Preserve the captured portfolio constraints when changing the fill model.
		options.Risk = r.Spec.Config.Risk
		options.StartingCash = r.Spec.Config.StartingCash
		options.SymbolCosts = r.Spec.Config.SymbolCosts
		r.Spec.Config = *options
	}
	if !contains(r.Spec.Symbols, r.Spec.Config.BenchmarkSymbol) {
		return errors.New("benchmark must be a captured symbol")
	}
	inputs := []sim.Input{}
	for _, in := range r.Inputs {
		if strings.HasPrefix(in.Type, "market.") {
			inputs = append(inputs, in)
		}
	}
	for _, in := range extra {
		if strings.HasPrefix(in.Type, "market.") {
			return errors.New("additional inputs cannot replace market data")
		}
		inputs = append(inputs, in)
	}
	return storeSimulation(db, run, r.Spec, inputs)
}

func simulationStrategy(spec simulationSpec) sim.Strategy {
	def, _, _ := validateStrategyDefinition(spec.Strategy)
	if def == nil {
		def = &StrategyDefinition{Universe: spec.Symbols, Cadence: spec.Interval}
	}
	symbols := append([]string(nil), spec.Symbols...)
	sort.Strings(symbols)
	lastSymbol := symbols[len(symbols)-1]
	return func(state *sim.State, in sim.Input) ([]sim.Command, error) {
		if in.Type == "order.intent" {
			if !contains(spec.Symbols, in.Symbol) {
				return nil, fmt.Errorf("order intent symbol %s is outside the captured universe", in.Symbol)
			}
			expires := time.Time{}
			if text := in.Metadata["expires_at"]; text != "" {
				var err error
				expires, err = time.Parse(time.RFC3339Nano, text)
				if err != nil {
					return nil, err
				}
			}
			if in.Metadata["side"] == "buy" && spec.Policy != nil && !spec.Policy.Allowed[in.Symbol] {
				return []sim.Command{{Report: map[string]any{"rejected_input": in.ID, "reason": "symbol excluded by portfolio universe"}}}, nil
			}
			return []sim.Command{{Order: &sim.Order{Symbol: in.Symbol, Side: in.Metadata["side"], Type: in.Metadata["order_type"], TIF: in.Metadata["tif"], Qty: in.Data["qty"], LimitPrice: in.Data["limit_price"], StopPrice: in.Data["stop_price"], ExpiresAt: expires}}}, nil
		}
		if in.Type == "order.cancel_intent" {
			return []sim.Command{{CancelID: in.Metadata["order_id"]}}, nil
		}

		if in.Type != "market.bar.close" {
			return nil, nil
		}
		m := simulationStrategyState{History: map[string][]float64{}, Prices: map[string]float64{}}
		if len(state.StrategyState) > 0 {
			if err := json.Unmarshal(state.StrategyState, &m); err != nil {
				return nil, err
			}
		}
		market := strategyMarket{history: m.History, prices: m.Prices, asOf: m.AsOf, signalStep: m.SignalStep, signalCount: m.SignalCount, features: state.Features}
		step := int(in.Data["step"])
		appendStrategyReplayBar(&BacktestRun{Symbols: spec.Symbols, Interval: spec.Interval}, def, &market, &BacktestMarketBar{Step: step, Symbol: in.Symbol, T: in.EventTime.Unix(), C: in.Data["price"]})
		for symbol, h := range market.history {
			if len(h) > 1000 {
				market.history[symbol] = h[len(h)-1000:]
			}
		}
		m.History, m.Prices, m.AsOf, m.SignalStep, m.SignalCount = market.history, market.prices, market.asOf, market.signalStep, market.signalCount
		m.Step = maxInt(m.Step, step)
		var commands []sim.Command
		if spec.DecisionMode != "recorded_orders" && in.Symbol == lastSymbol && strategyReplaySignalDue(def, step, market) {
			eval, err := evaluateStrategy(&Strategy{Version: spec.StrategyVersion, Definition: spec.Strategy}, market)
			if err != nil {
				return nil, err
			}
			commands = simulationTargets(spec, state, eval.TargetAllocations, m.Targets)
			m.Targets = eval.TargetAllocations
			commands = append(commands, sim.Command{Report: map[string]any{"decisions": eval.Decisions, "warnings": eval.Warnings, "targets": eval.TargetAllocations, "signal_as_of": eval.AsOf}})
		}
		state.StrategyState, _ = json.Marshal(m)
		return commands, nil
	}
}

func simulationTargets(spec simulationSpec, state *sim.State, targets, previous []StrategyAllocation) []sim.Command {
	var commands []sim.Command
	pending := map[string]bool{}
	targetWeights, previousWeights := map[string]float64{}, map[string]float64{}
	for _, t := range targets {
		targetWeights[t.Symbol] = t.Weight
	}
	for _, t := range previous {
		previousWeights[t.Symbol] = t.Weight
	}
	changed := sim.Hash(targetWeights) != sim.Hash(previousWeights)
	for _, o := range state.Orders {
		if o.Status == "submitted" || o.Status == "working" || o.Status == "partially_filled" {
			if changed {
				commands = append(commands, sim.Command{CancelID: o.ID})
			} else {
				pending[o.Symbol] = true
			}
		}
	}
	e := &sim.Engine{Config: spec.Config, State: state}
	equity := e.Metrics()["equity"]
	weights := map[string]float64{}
	for _, t := range targets {
		weights[t.Symbol] = t.Weight
	}
	symbols := append([]string(nil), spec.Symbols...)
	sort.Strings(symbols)
	gross := 0.0
	for _, symbol := range symbols {
		gross += state.Positions[symbol].Qty * state.Quotes[symbol].Price
	}
	for _, symbol := range symbols {
		if pending[symbol] {
			continue
		}
		price := state.Quotes[symbol].Price
		if price <= 0 {
			continue
		}
		pos := state.Positions[symbol]
		diff := equity*weights[symbol] - pos.Qty*price
		if math.Abs(diff) < math.Max(1, equity*0.001) {
			continue
		}
		side := "buy"
		qty := floor4(math.Abs(diff) / price)
		if diff < 0 {
			side = "sell"
			qty = math.Min(qty, pos.Qty)
		}
		if side == "buy" && spec.Policy != nil {
			if !spec.Policy.Allowed[symbol] || exposureBreach(spec.Policy.Risk, equity, qty*price, pos.Qty*price, gross) != nil {
				continue
			}
			if spec.Policy.Risk.MaxDrawdownPct > 0 && state.Peak > 0 && (equity/state.Peak-1)*100 < -spec.Policy.Risk.MaxDrawdownPct {
				continue
			}
		}
		if qty > 0 {
			commands = append(commands, sim.Command{Order: &sim.Order{Symbol: symbol, Side: side, Type: "market", Qty: qty, TIF: "gtc"}})
		}
	}
	return commands
}

func commitSimulation(db *sql.DB, run *BacktestRun, r *simulationRecord, e *sim.Engine, outputs []sim.Output) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stateJSON, err := json.Marshal(e.State)
	if err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE backtest_simulations SET state_json=?,revision=revision+1 WHERE run_id=? AND revision=? AND EXISTS(SELECT 1 FROM backtest_runs WHERE id=? AND status='running')`, string(stateJSON), run.ID, r.Revision, run.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("simulation paused, cancelled or advanced elsewhere")
	}
	for _, out := range outputs {
		if out.Type == "fill" || out.Type == "strategy.decision" || strings.HasPrefix(out.Type, "order.") {
			if _, err := tx.Exec(`INSERT INTO backtest_events(run_id,kind,message,data) VALUES(?,?,?,?)`, run.ID, out.Type, out.At.Format(time.RFC3339Nano)+" · "+out.Type, string(out.Data)); err != nil {
				return err
			}
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO backtest_simulation_outputs(run_id,sequence,output_json) VALUES(?,?,?)`, run.ID, out.Sequence, string(raw)); err != nil {
			return err
		}
	}
	var strategyState simulationStrategyState
	_ = json.Unmarshal(e.State.StrategyState, &strategyState)
	step := strategyState.Step
	if r.Spec.DecisionMode == "agent" {
		step = e.State.Cursor
	}
	status := "running"
	if e.State.Finished {
		status = "completed"
	}
	metrics := e.Metrics()
	summary := map[string]any{"processed_events": e.State.Cursor, "input_events": len(e.Inputs), "simulation_time": e.State.Now.Format(time.RFC3339Nano), "metrics": metrics, "last_step": step}
	raw, _ := json.Marshal(summary)
	if _, err = tx.Exec(`UPDATE backtest_runs SET current_step=?,status=?,summary_json=json_patch(summary_json,?),updated_at=CURRENT_TIMESTAMP,completed_at=CASE WHEN ?='completed' THEN CURRENT_TIMESTAMP ELSE completed_at END WHERE id=?`, step, status, string(raw), status, run.ID); err != nil {
		return err
	}
	// Keep the existing chart/API compatible. Event-level history remains in
	// the immutable output ledger; bar snapshots support existing scorecards.
	closedBar := e.State.Finished
	for _, out := range outputs {
		if out.Type == "input" {
			var in sim.Input
			_ = json.Unmarshal(out.Data, &in)
			if in.Type == "market.bar.close" {
				closedBar = true
			}
		}
	}
	if r.Spec.DecisionMode == "agent" {
		step = e.State.Cursor
		closedBar = true
	}
	if step > 0 && closedBar {
		snap := simulationSnapshot(run, e, step)
		if err := dbUpsertBacktestSnapshot(tx, snap); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.Revision++
	r.State = e.State
	return nil
}

func simulationSnapshot(run *BacktestRun, e *sim.Engine, step int) *BacktestSnapshot {
	positions := []*Position{}
	symbols := make([]string, 0, len(e.State.Positions))
	for symbol := range e.State.Positions {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	for _, symbol := range symbols {
		p := e.State.Positions[symbol]
		positions = append(positions, &Position{Symbol: symbol, AssetClass: inferAssetClass(symbol), Qty: p.Qty, AvgCost: p.AvgCost, MarketPrice: e.State.Quotes[symbol].Price})
	}
	orders := []*Order{}
	for _, o := range e.State.Orders {
		orders = append(orders, &Order{ID: o.ID, PortfolioID: run.PortfolioID, Symbol: o.Symbol, AssetClass: inferAssetClass(o.Symbol), Side: o.Side, Type: o.Type, Qty: o.Qty, FilledQty: o.FilledQty, AvgFillPrice: o.AvgFillPrice, Status: o.Status, TIF: o.TIF, PlacedAt: o.SubmittedAt.Format(time.RFC3339Nano), ResolvedAt: o.ResolvedAt.Format(time.RFC3339Nano), Source: "strategy"})
	}
	valueBacktestPositions(e.State.Cash, positions, nil)
	return &BacktestSnapshot{RunID: run.ID, Step: step, Cash: e.State.Cash, BuyingPower: e.State.Cash, Equity: e.Metrics()["equity"], OpenPnL: e.Metrics()["open_pnl"], RealizedPnL: e.State.RealizedPnL, Exposure: e.Metrics()["exposure"], Positions: positions, Orders: orders}
}

var simulationWorkers = struct {
	sync.Mutex
	running map[int64]bool
	done    map[int64]chan struct{}
}{running: map[int64]bool{}, done: map[int64]chan struct{}{}}

func runEventSimulation(ctx context.Context, run *BacktestRun, one bool) (map[string]any, error) {
	r, err := loadSimulation(globalCtx.AppDB(), run.ID)
	if err != nil {
		return nil, err
	}
	strategy := simulationStrategy(r.Spec)
	if r.Spec.DecisionMode == "agent" {
		strategy = agentSimulationStrategy(ctx, run, r)
	}
	e, err := sim.New(r.Spec.Config, r.Inputs, r.State, strategy)
	if err != nil {
		return nil, err
	}
	lastEmit := time.Time{}
	for !e.State.Finished {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fresh, err := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
		if err != nil {
			return nil, err
		}
		if fresh.Status != "running" {
			return map[string]any{"backtest": fresh}, nil
		}
		outputs, err := e.Advance()
		if err != nil {
			fresh, _ := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
			if fresh != nil && (fresh.Status == "paused" || fresh.Status == "cancelled") {
				return map[string]any{"backtest": fresh}, nil
			}
			return nil, err
		}
		if err := commitSimulation(globalCtx.AppDB(), run, r, e, outputs); err != nil {
			fresh, _ := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
			if fresh != nil && (fresh.Status == "paused" || fresh.Status == "cancelled") {
				return map[string]any{"backtest": fresh}, nil
			}
			return nil, err
		}
		if time.Since(lastEmit) > 250*time.Millisecond || one || e.State.Finished {
			emitBacktest("trading.backtest.tick", run.ID, map[string]any{"processed_events": e.State.Cursor, "input_events": len(e.Inputs), "simulation_time": e.State.Now.Format(time.RFC3339Nano), "metrics": e.Metrics()})
			lastEmit = time.Now()
		}
		if one {
			break
		}
	}
	if e.State.Finished {
		bundle, err := simulationBundle(globalCtx.AppDB(), run)
		if err != nil {
			return nil, err
		}
		meta := map[string]any{"result_sha256": bundle.ResultHash}
		if expected, ok := run.Summary["expected_result_sha256"].(string); ok && expected != "" {
			meta["reproduction_matches"] = expected == bundle.ResultHash
		}
		raw, _ := json.Marshal(meta)
		if _, err := globalCtx.AppDB().Exec(`UPDATE backtest_simulations SET result_sha256=? WHERE run_id=?`, bundle.ResultHash, run.ID); err != nil {
			return nil, err
		}
		if _, err := globalCtx.AppDB().Exec(`UPDATE backtest_runs SET summary_json=json_patch(summary_json,?) WHERE id=?`, string(raw), run.ID); err != nil {
			return nil, err
		}
		emitBacktest("trading.backtest.completed", run.ID, eMetricsAny(e.Metrics()))
	}
	fresh, err := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	return map[string]any{"backtest": fresh}, err
}

func eMetricsAny(m map[string]float64) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func startEventSimulation(run *BacktestRun, one bool) (map[string]any, error) {
	simulationWorkers.Lock()
	if simulationWorkers.running[run.ID] {
		simulationWorkers.Unlock()
		return map[string]any{"backtest": run, "runner_started": false}, nil
	}
	fresh, err := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	if err != nil {
		simulationWorkers.Unlock()
		return nil, err
	}
	if fresh.Status == "completed" || fresh.Status == "cancelled" {
		simulationWorkers.Unlock()
		return nil, fmt.Errorf("backtest is %s", fresh.Status)
	}
	if err := dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "running", ""); err != nil {
		simulationWorkers.Unlock()
		return nil, err
	}
	simulationWorkers.running[run.ID] = true
	simulationWorkers.done[run.ID] = make(chan struct{})
	simulationWorkers.Unlock()
	work := func() (map[string]any, error) {
		defer func() {
			simulationWorkers.Lock()
			delete(simulationWorkers.running, run.ID)
			close(simulationWorkers.done[run.ID])
			delete(simulationWorkers.done, run.ID)
			simulationWorkers.Unlock()
		}()
		out, err := runEventSimulation(context.Background(), fresh, one)
		if err != nil {
			_, _ = globalCtx.AppDB().Exec(`UPDATE backtest_runs SET status='failed',error=? WHERE id=? AND status='running'`, err.Error(), run.ID)
			emitBacktest("trading.backtest.failed", run.ID, map[string]any{"error": err.Error()})
		}
		return out, err
	}
	if one && fresh.RunKind != "agent" {
		return work()
	}
	go func() { _, _ = work() }()
	fresh, _ = dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	return map[string]any{"backtest": fresh, "runner_started": true}, nil
}

func simulationBundle(db *sql.DB, run *BacktestRun) (*simulationArtifact, error) {
	r, err := loadSimulation(db, run.ID)
	if err != nil {
		return nil, err
	}
	a := &simulationArtifact{Schema: "apteva.backtest/v1", Spec: r.Spec, Inputs: r.Inputs, InputHash: r.InputHash, Outputs: []sim.Output{}}
	rows, err := db.Query(`SELECT output_json FROM backtest_simulation_outputs WHERE run_id=? ORDER BY sequence`, run.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var out sim.Output
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, err
		}
		a.Outputs = append(a.Outputs, out)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if r.Spec.DecisionMode == "agent" {
		decisions, err := db.Query(`SELECT decision_json FROM backtest_agent_decisions WHERE run_id=? ORDER BY input_id`, run.ID)
		if err != nil {
			return nil, err
		}
		for decisions.Next() {
			var raw string
			if err := decisions.Scan(&raw); err != nil {
				decisions.Close()
				return nil, err
			}
			var d agentDecision
			if err := json.Unmarshal([]byte(raw), &d); err != nil {
				decisions.Close()
				return nil, err
			}
			a.AgentDecisions = append(a.AgentDecisions, d)
		}
		err = decisions.Err()
		decisions.Close()
		if err != nil {
			return nil, err
		}
		a.AgentDecisionsHash = sim.Hash(a.AgentDecisions)
	}
	if r.State != nil {
		a.Result = (&sim.Engine{Config: r.Spec.Config, State: r.State}).Metrics()
	}
	if r.State != nil && r.State.Finished {
		a.ResultHash = sim.Hash(struct {
			Outputs []sim.Output       `json:"outputs"`
			Result  map[string]float64 `json:"result"`
		}{a.Outputs, a.Result})
	}
	return a, nil
}

func (a *App) handleSimulationArtifact(w http.ResponseWriter, r *http.Request, run *BacktestRun) {
	bundle, err := simulationBundle(globalCtx.AppDB(), run)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="backtest-%d.json"`, run.ID))
	httpJSON(w, 200, bundle)
}

func importSimulation(db *sql.DB, project string, portfolioID int64, name string, a *simulationArtifact) (*BacktestRun, error) {
	if a == nil || a.Schema != "apteva.backtest/v1" || a.Spec.Version != sim.Version || a.Spec.SourceHash != simulationSourceHash() {
		return nil, errors.New("unsupported artifact schema or engine version")
	}
	if _, err := dbGetPortfolio(db, project, portfolioID); err != nil {
		return nil, err
	}
	if a.InputHash != sim.Hash(struct {
		Spec   simulationSpec `json:"spec"`
		Inputs []sim.Input    `json:"inputs"`
	}{a.Spec, a.Inputs}) {
		return nil, errors.New("artifact input hash mismatch")
	}
	if a.ResultHash != "" && a.ResultHash != sim.Hash(struct {
		Outputs []sim.Output       `json:"outputs"`
		Result  map[string]float64 `json:"result"`
	}{a.Outputs, a.Result}) {
		return nil, errors.New("artifact result hash mismatch")
	}
	if _, err := sim.New(a.Spec.Config, a.Inputs, nil, nil); err != nil {
		return nil, err
	}
	if len(a.Spec.Symbols) == 0 {
		return nil, errors.New("artifact requires symbols")
	}
	if a.Spec.DecisionMode != "recorded_orders" && a.Spec.DecisionMode != "agent" {
		if _, _, err := validateStrategyDefinition(a.Spec.Strategy); err != nil {
			return nil, err
		}
	}
	if a.Spec.DecisionMode == "agent" {
		if a.Spec.Agent == nil || a.AgentDecisionsHash == "" || sim.Hash(a.AgentDecisions) != a.AgentDecisionsHash {
			return nil, errors.New("agent decision hash mismatch")
		}
		seen := map[string]bool{}
		for _, d := range a.AgentDecisions {
			if d.InputID == "" || seen[d.InputID] || d.InputID != d.Observation.DecisionID || d.InputID != d.Observation.Event.ID || d.Observation.State == nil || sim.Hash(d.Observation) != d.ObservationHash {
				return nil, errors.New("invalid recorded agent observation")
			}
			if err := validateAgentDecision(a.Spec, d.Observation, &d); err != nil {
				return nil, fmt.Errorf("invalid recorded agent decision: %w", err)
			}
			seen[d.InputID] = true
		}
	}
	steps := 0
	for _, in := range a.Inputs {
		steps = maxInt(steps, int(in.Data["step"]))
	}
	run := &BacktestRun{ProjectID: project, PortfolioID: portfolioID, RunKind: "strategy", StrategyVersion: a.Spec.StrategyVersion, Name: nonEmpty(name, "Reproduced backtest"), Symbols: a.Spec.Symbols, Interval: a.Spec.Interval, StartingCash: a.Spec.Config.StartingCash, TotalSteps: steps, Status: "queued", StartAt: a.Inputs[0].EventTime.Format("2006-01-02"), EndAt: a.Inputs[len(a.Inputs)-1].AvailableAt.Format("2006-01-02"), Summary: map[string]any{"expected_result_sha256": a.ResultHash}}
	if a.Spec.DecisionMode == "agent" {
		run.RunKind = "agent"
		run.SourceAgentID = a.Spec.Agent.SourceAgentID
		// Set this before the run becomes visible, even if recording import fails.
		// An incomplete artifact must never fall back to live model decisions.
		run.Summary["agent_replay_only"] = true
	}
	id, err := dbCreateBacktestRun(db, run)
	if err != nil {
		return nil, err
	}
	run.ID = id
	if err := storeSimulation(db, run, a.Spec, a.Inputs); err != nil {
		return nil, err
	}
	if a.Spec.DecisionMode == "agent" {
		tx, err := db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		for _, d := range a.AgentDecisions {
			raw, _ := json.Marshal(d)
			if _, err := tx.Exec(`INSERT INTO backtest_agent_decisions(run_id,input_id,observation_sha256,decision_json) VALUES(?,?,?,?)`, id, d.InputID, d.ObservationHash, string(raw)); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(`UPDATE backtest_runs SET summary_json=json_patch(summary_json,'{"agent_replay_only":true}') WHERE id=?`, id); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	return dbGetBacktestRun(db, project, id)
}

// Legacy runs also execute outside the HTTP request, retaining their original
// numerical model while allowing the existing UI to pause or cancel them.
func startLegacyStrategyWorker(run *BacktestRun) (map[string]any, error) {
	simulationWorkers.Lock()
	if simulationWorkers.running[run.ID] {
		simulationWorkers.Unlock()
		return map[string]any{"backtest": run, "runner_started": false}, nil
	}
	simulationWorkers.running[run.ID] = true
	simulationWorkers.done[run.ID] = make(chan struct{})
	simulationWorkers.Unlock()
	go func() {
		defer func() {
			simulationWorkers.Lock()
			delete(simulationWorkers.running, run.ID)
			close(simulationWorkers.done[run.ID])
			delete(simulationWorkers.done, run.ID)
			simulationWorkers.Unlock()
		}()
		if _, err := runStrategyBacktestToEnd(run); err != nil {
			_, _ = globalCtx.AppDB().Exec(`UPDATE backtest_runs SET status='failed',error=? WHERE id=? AND status='running'`, err.Error(), run.ID)
			emitBacktest("trading.backtest.failed", run.ID, map[string]any{"error": err.Error()})
		}
	}()
	return map[string]any{"backtest": run, "runner_started": true}, nil
}

func waitSimulationWorker(id int64) {
	simulationWorkers.Lock()
	done := simulationWorkers.done[id]
	simulationWorkers.Unlock()
	if done != nil {
		<-done
	}
}

func (a *App) toolBacktestControl(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	run, err := dbGetBacktestRun(ctx.AppDB(), project, int64Arg(args, "backtest_id", 0))
	if err != nil {
		return nil, err
	}
	switch strArg(args, "action") {
	case "status":
		return map[string]any{"backtest": run}, nil
	case "run":
		if !eventBacktest(run) {
			return nil, errors.New("event simulation required")
		}
		return startEventSimulation(run, false)
	case "step":
		if !eventBacktest(run) {
			return nil, errors.New("event simulation required")
		}
		return startEventSimulation(run, true)
	case "pause":
		return pauseBacktestRun(run)
	case "cancel":
		if !eventBacktest(run) {
			return nil, errors.New("event simulation required")
		}
		_, err := ctx.AppDB().Exec(`UPDATE backtest_runs SET status='cancelled' WHERE id=? AND status IN ('queued','running','paused','failed')`, run.ID)
		waitSimulationWorker(run.ID)
		emitBacktest("trading.backtest.cancelled", run.ID, nil)
		return map[string]any{"status": "cancelled"}, err
	default:
		return nil, errors.New("action must be status, run, step, pause or cancel")
	}
}

func (a *App) toolBacktestArtifact(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if raw, ok := args["artifact"]; ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var artifact simulationArtifact
		if err := json.Unmarshal(data, &artifact); err != nil {
			return nil, err
		}
		run, err := importSimulation(ctx.AppDB(), project, int64Arg(args, "portfolio_id", 0), strArg(args, "name"), &artifact)
		return map[string]any{"backtest": run}, err
	}
	run, err := dbGetBacktestRun(ctx.AppDB(), project, int64Arg(args, "backtest_id", 0))
	if err != nil {
		return nil, err
	}
	return simulationBundle(ctx.AppDB(), run)
}
