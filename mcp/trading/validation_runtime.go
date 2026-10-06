package main

import (
	"errors"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

// Warm history advances indicators and feature/quote observations only. It
// never trades, invokes an agent, contributes returns, or starts the benchmark.
func newSimulationEngine(r *simulationRecord, strategy sim.Strategy) (*sim.Engine, error) {
	e, err := sim.New(r.Spec.Config, r.Inputs, r.State, strategy)
	if err != nil || r.State != nil || len(r.Spec.WarmupInputs) == 0 {
		return e, err
	}
	var observe sim.Strategy
	if r.Spec.DecisionMode != "agent" {
		rules := simulationStrategy(r.Spec)
		observe = func(s *sim.State, in sim.Input) ([]sim.Command, error) { _, err := rules(s, in); return nil, err }
	}
	config := r.Spec.Config
	config.NotifyFills = false
	warm, err := sim.New(config, r.Spec.WarmupInputs, nil, observe)
	if err != nil {
		return nil, err
	}
	for !warm.State.Finished {
		if _, err = warm.Advance(); err != nil {
			return nil, err
		}
	}
	e.State.Quotes = warm.State.Quotes
	e.State.Features = warm.State.Features
	e.State.StrategyState = warm.State.StrategyState
	return e, nil
}
func validateSimulationWarmup(spec simulationSpec, inputs []sim.Input) error {
	if len(spec.WarmupInputs) == 0 {
		return nil
	}
	if len(spec.WarmupInputs) > 2*defaultBacktestMaxMarketRows {
		return errors.New("warmup input budget exceeded")
	}
	warm, err := sim.New(spec.Config, spec.WarmupInputs, nil, nil)
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, in := range inputs {
		ids[in.ID] = true
	}
	for _, in := range warm.Inputs {
		if !in.AvailableAt.Before(inputs[0].AvailableAt) || ids[in.ID] || in.Type == "order.intent" || in.Type == "order.cancel_intent" {
			return errors.New("warmup must contain only distinct past observations")
		}
		if in.Type == "market.bar.close" && in.Data["step"] > 0 {
			return errors.New("warmup bar steps must be nonpositive")
		}
	}
	return nil
}
