package main

import (
	"encoding/json"
	"errors"
	"time"

	sdk "github.com/apteva/app-sdk"
	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
	rules "github.com/apteva/apps/mcp/trading/internal/ruleengine"
)

func validateRuleTape(program *rules.Program, inputs []sim.Input) error {
	return rules.ValidateTape(program, inputs)
}

func createRuleEventBacktest(ctx *sdk.AppCtx, pf *Portfolio, strategy *Strategy, name string, startingCash float64, options *sim.Config, inputs []sim.Input) (*BacktestRun, error) {
	def, _, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		return nil, err
	}
	if def.Engine != "rules" {
		return nil, errors.New("complete event tapes require engine=rules")
	}
	config := sim.Config{Seed: 1, StartingCash: startingCash, Costs: sim.Costs{QtyStep: 0.0001}, BenchmarkSymbol: def.Program.Symbol, NotifyFills: true}
	if options != nil {
		config = *options
		config.StartingCash = startingCash
		config.NotifyFills = true
	}
	if config.StartingCash <= 0 {
		config.StartingCash = pf.StartingCash
	}
	if config.BenchmarkSymbol == "" {
		config.BenchmarkSymbol = def.Program.Symbol
	}
	if config.BenchmarkSymbol != def.Program.Symbol {
		return nil, errors.New("rule benchmark must be the program instrument")
	}
	policy, err := captureReplayPolicy(ctx.AppDB(), pf, def.Universe)
	if err != nil {
		return nil, err
	}
	if !policy.Allowed[def.Program.Symbol] {
		return nil, errors.New("program instrument is excluded by the portfolio universe")
	}
	risk := policy.Risk
	config.Risk = sim.RiskLimits{MaxOrderPct: risk.MaxOrderPct, MaxPositionPct: risk.MaxPositionPct, MaxGrossExposurePct: risk.MaxGrossExposurePct, MaxDailyLossPct: risk.MaxDailyLossPct, MaxDrawdownPct: risk.MaxDrawdownPct}
	if _, err := rules.Strategy(def.Program, config); err != nil {
		return nil, err
	}
	if len(inputs) > 2*defaultBacktestMaxMarketRows {
		return nil, errors.New("rule input budget exceeded")
	}
	inputs, err = rules.ScheduleInputs(def.Program, inputs)
	if err != nil {
		return nil, err
	}
	engine, err := sim.New(config, inputs, nil, nil)
	if err != nil {
		return nil, err
	}
	inputs = engine.Inputs
	if err := validateRuleTape(def.Program, inputs); err != nil {
		return nil, err
	}
	start, end := inputs[0].AvailableAt, inputs[len(inputs)-1].AvailableAt
	run := &BacktestRun{ProjectID: pf.ProjectID, PortfolioID: pf.ID, StrategyID: strategy.ID, StrategyVersion: strategy.Version, RunKind: "strategy", Name: nonEmpty(name, strategy.Name+" rule backtest"), Status: "queued", Symbols: def.Universe, Interval: def.Program.Timeframe, StartingCash: config.StartingCash, StartAt: start.Format(time.RFC3339), EndAt: end.Format(time.RFC3339), TotalSteps: len(inputs), FeeBps: config.Costs.FeeBps, SlippageBps: config.Costs.SlippageBps, Summary: map[string]any{"strategy_name": strategy.Name, "market_source": "supplied_event_tape", "data_identity": sim.Hash(inputs)}}
	id, err := dbCreateBacktestRun(ctx.AppDB(), run)
	if err != nil {
		return nil, err
	}
	run.ID = id
	spec := simulationSpec{Version: sim.Version, SourceHash: simulationSourceHash(), Config: config, Strategy: strategy.Definition, StrategyVersion: strategy.Version, Symbols: def.Universe, Interval: def.Program.Timeframe, DecisionMode: "strategy", Policy: policy, ReferenceManifest: map[string]any{"source": "supplied_event_tape", "data_sha256": sim.Hash(inputs), "contract_specification": "explicit simulation configuration"}}
	if err := storeSimulation(ctx.AppDB(), run, spec, inputs); err != nil {
		_ = dbSetBacktestStatus(ctx.AppDB(), id, "failed", err.Error())
		return nil, err
	}
	return dbGetBacktestRun(ctx.AppDB(), pf.ProjectID, id)
}

func ruleExamplePresets() []map[string]any {
	out := []map[string]any{}
	for _, file := range []string{"donchian.json", "atr_candle.json", "session_range.json"} {
		raw, err := simulationSources.ReadFile("rule_examples/" + file)
		if err != nil {
			continue
		}
		var example map[string]any
		if json.Unmarshal(raw, &example) == nil {
			out = append(out, example)
		}
	}
	return out
}
