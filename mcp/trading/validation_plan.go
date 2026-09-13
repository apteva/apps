package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

const validationVersion = "validation/1"

type validationCandidate struct {
	Name      string         `json:"name"`
	Strategy  map[string]any `json:"strategy,omitempty"`
	Directive string         `json:"directive,omitempty"`
}
type validationShock struct {
	Name              string   `json:"name"`
	CostMultiplier    float64  `json:"cost_multiplier,omitempty"`
	ExtraFeeBPS       float64  `json:"extra_fee_bps,omitempty"`
	ExtraSlippageBPS  float64  `json:"extra_slippage_bps,omitempty"`
	LatencyMS         int64    `json:"latency_ms,omitempty"`
	LiquidityFraction *float64 `json:"liquidity_fraction,omitempty"`
	PriceShockPct     float64  `json:"price_shock_pct,omitempty"`
	FeatureDelayMS    int64    `json:"feature_delay_ms,omitempty"`
}
type validationConfig struct {
	ParameterGrid        map[string][]any      `json:"parameter_grid,omitempty"`
	Mode                 string                `json:"mode"`
	Seed                 uint64                `json:"seed"`
	TrainSteps           int                   `json:"train_steps,omitempty"`
	TestSteps            int                   `json:"test_steps,omitempty"`
	StepSteps            int                   `json:"step_steps,omitempty"`
	Expanding            bool                  `json:"expanding"`
	WarmupSteps          int                   `json:"warmup_steps"`
	SelectionMetric      string                `json:"selection_metric"`
	Candidates           []validationCandidate `json:"candidates"`
	Scenarios            []validationShock     `json:"scenarios,omitempty"`
	Samples              int                   `json:"samples,omitempty"`
	MaxExtraFeeBPS       float64               `json:"max_extra_fee_bps,omitempty"`
	MaxExtraSlippageBPS  float64               `json:"max_extra_slippage_bps,omitempty"`
	MaxLatencyMS         int64                 `json:"max_latency_ms,omitempty"`
	MaxAgentDecisions    int                   `json:"max_agent_decisions"`
	LossThresholdPct     float64               `json:"loss_threshold_pct"`
	DrawdownThresholdPct float64               `json:"drawdown_threshold_pct"`
}
type validationCase struct {
	Ordinal     int             `json:"ordinal"`
	Fold        int             `json:"fold"`
	Phase       string          `json:"phase"`
	Candidate   int             `json:"candidate"`
	Start       time.Time       `json:"start"`
	End         time.Time       `json:"end"` // exclusive
	WarmupStart time.Time       `json:"warmup_start"`
	Seed        uint64          `json:"seed"`
	Shock       validationShock `json:"shock"`
}
type validationSource struct {
	Spec      simulationSpec `json:"spec"`
	Inputs    []sim.Input    `json:"inputs"`
	InputHash string         `json:"input_sha256"`
}

func cloneValidation[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}

func planValidation(source validationSource, cfg *validationConfig) ([]validationCase, error) {
	if source.Spec.DecisionMode != "agent" && source.Spec.DecisionMode != "strategy" && source.Spec.DecisionMode != "" {
		return nil, errors.New("validation requires strategy or fresh agent decisions, not recorded order intentions")
	}
	if source.Spec.DecisionMode == "agent" && source.Spec.Agent == nil {
		return nil, errors.New("source agent configuration missing")
	}
	if len(source.Inputs) == 0 {
		return nil, errors.New("source tape is empty")
	}
	for _, in := range source.Inputs {
		if strings.HasPrefix(in.Type, "order.") {
			return nil, errors.New("validation cannot reuse external order intentions")
		}
	}
	if !oneOfString(cfg.Mode, "out_of_sample", "walk_forward", "robustness", "stress", "monte_carlo") {
		return nil, errors.New("unknown validation mode")
	}
	if cfg.Seed == 0 {
		cfg.Seed = 1
	}
	automaticWarmup := cfg.WarmupSteps == 0
	if cfg.WarmupSteps == 0 {
		cfg.WarmupSteps = 100
	}
	if cfg.WarmupSteps < 0 || cfg.WarmupSteps > 10000 {
		return nil, errors.New("warmup_steps must be 0–10000")
	}
	if cfg.SelectionMetric == "" {
		cfg.SelectionMetric = "return_pct"
	}
	if !oneOfString(cfg.SelectionMetric, "return_pct", "excess_return_pct", "max_drawdown_pct") {
		return nil, errors.New("unsupported selection metric")
	}
	if cfg.MaxAgentDecisions == 0 {
		cfg.MaxAgentDecisions = 1000
	}
	if cfg.MaxAgentDecisions < 1 || cfg.MaxAgentDecisions > 100000 {
		return nil, errors.New("max_agent_decisions must be 1–100000")
	}
	if cfg.DrawdownThresholdPct == 0 {
		cfg.DrawdownThresholdPct = 20
	}
	if !finite(cfg.LossThresholdPct) || cfg.LossThresholdPct < 0 || cfg.LossThresholdPct > 100 || !finite(cfg.DrawdownThresholdPct) || cfg.DrawdownThresholdPct <= 0 || cfg.DrawdownThresholdPct > 100 {
		return nil, errors.New("invalid loss or drawdown threshold")
	}
	if len(cfg.ParameterGrid) > 0 {
		if len(cfg.Candidates) > 0 || source.Spec.DecisionMode == "agent" {
			return nil, errors.New("parameter_grid requires a strategy and cannot be combined with candidates")
		}
		var err error
		cfg.Candidates, err = validationGrid(source.Spec.Strategy, cfg.ParameterGrid)
		cfg.ParameterGrid = nil
		if err != nil {
			return nil, err
		}
	}
	if len(cfg.Candidates) == 0 {
		cfg.Candidates = []validationCandidate{{Name: "Source"}}
	}
	if len(cfg.Candidates) > 16 {
		return nil, errors.New("at most 16 candidates")
	}
	names := map[string]bool{}
	for i := range cfg.Candidates {
		c := &cfg.Candidates[i]
		if strings.TrimSpace(c.Name) == "" || names[c.Name] {
			return nil, errors.New("candidate names must be unique and nonempty")
		}
		names[c.Name] = true
		if source.Spec.DecisionMode == "agent" {
			if c.Strategy != nil {
				return nil, errors.New("agent candidates use directive, not strategy")
			}
			if c.Directive == "" {
				c.Directive = source.Spec.Agent.Directive
			}
			if len(c.Directive) > 32000 {
				return nil, errors.New("candidate directive too long")
			}
		} else {
			if c.Directive != "" {
				return nil, errors.New("strategy candidates use strategy definitions")
			}
			if c.Strategy == nil {
				c.Strategy = cloneValidation(source.Spec.Strategy)
			}
			def, _, err := validateStrategyDefinition(c.Strategy)
			if err != nil {
				return nil, err
			}
			if automaticWarmup {
				cfg.WarmupSteps = maxInt(cfg.WarmupSteps, strategyReplayWarmupSteps(def, source.Spec.Interval))
			}
			if cfg.WarmupSteps > 10000 {
				return nil, errors.New("candidate requires more than 10000 warmup steps")
			}
			if err := validateStrategyReplayInterval(def, source.Spec.Interval); err != nil {
				return nil, err
			}
			for _, s := range def.Universe {
				if !contains(source.Spec.Symbols, s) {
					return nil, fmt.Errorf("candidate symbol %s is not in captured data", s)
				}
			}
		}
	}
	var times []time.Time
	for _, in := range source.Inputs {
		if in.Type == "market.quote" && (len(times) == 0 || !times[len(times)-1].Equal(in.AvailableAt)) {
			times = append(times, in.AvailableAt)
		}
	}
	if len(times) < 4 {
		return nil, errors.New("validation requires at least four market timestamps")
	}
	n := len(times)
	end := source.Inputs[len(source.Inputs)-1].AvailableAt.Add(time.Nanosecond)
	times = append(times, end)
	var plan []validationCase
	add := func(fold int, phase string, candidate, from, to int, shock validationShock) {
		warmStart := times[maxInt(0, from-cfg.WarmupSteps)]
		if from-cfg.WarmupSteps <= 0 {
			warmStart = source.Inputs[0].AvailableAt
			if len(source.Spec.WarmupInputs) > 0 && source.Spec.WarmupInputs[0].AvailableAt.Before(warmStart) {
				warmStart = source.Spec.WarmupInputs[0].AvailableAt
			}
		}
		runSeed := cfg.Seed + uint64(fold)
		if cfg.Mode == "monte_carlo" && phase == "scenario" {
			sample, _ := strconv.Atoi(strings.TrimPrefix(shock.Name, "sample-"))
			runSeed = cfg.Seed + uint64(sample)
		}
		plan = append(plan, validationCase{Ordinal: len(plan), Fold: fold, Phase: phase, Candidate: candidate, Start: times[from], End: times[to], WarmupStart: warmStart, Seed: runSeed, Shock: shock})
	}
	if cfg.Mode == "out_of_sample" || cfg.Mode == "walk_forward" {
		if cfg.TrainSteps == 0 {
			cfg.TrainSteps = n * 7 / 10
			if cfg.Mode == "walk_forward" {
				cfg.TrainSteps = n / 2
			}
		}
		if cfg.TestSteps == 0 {
			cfg.TestSteps = n - cfg.TrainSteps
			if cfg.Mode == "walk_forward" {
				cfg.TestSteps = maxInt(2, n/5)
			}
		}
		if cfg.StepSteps == 0 {
			cfg.StepSteps = cfg.TestSteps
		}
		if cfg.TrainSteps < 2 || cfg.TestSteps < 2 || cfg.TrainSteps+cfg.TestSteps > n || cfg.StepSteps < cfg.TestSteps || cfg.StepSteps > n {
			return nil, errors.New("need >=2 train/test market steps, fitting data; step_steps must be >= test_steps to avoid overlapping test returns")
		}
		fold := 0
		for testStart := cfg.TrainSteps; testStart+cfg.TestSteps <= n; testStart += cfg.StepSteps {
			from := testStart - cfg.TrainSteps
			if cfg.Expanding {
				from = 0
			}
			for ci := range cfg.Candidates {
				add(fold, "train", ci, from, testStart, validationShock{Name: "baseline"})
			}
			add(fold, "test", -1, testStart, testStart+cfg.TestSteps, validationShock{Name: "baseline"})
			fold++
			if len(plan) > 512 {
				return nil, errors.New("validation exceeds 512 runs")
			}
			if cfg.Mode == "out_of_sample" {
				break
			}
		}
	} else {
		shocks := []validationShock{{Name: "baseline"}}
		switch cfg.Mode {
		case "monte_carlo":
			if cfg.Samples == 0 {
				cfg.Samples = 100
			}
			if cfg.Samples < 2 || cfg.Samples > 500 {
				return nil, errors.New("samples must be 2–500")
			}
			if cfg.MaxExtraFeeBPS == 0 {
				cfg.MaxExtraFeeBPS = 10
			}
			if cfg.MaxExtraSlippageBPS == 0 {
				cfg.MaxExtraSlippageBPS = 25
			}
			if cfg.MaxLatencyMS == 0 {
				cfg.MaxLatencyMS = 1500
			}
			if !finite(cfg.MaxExtraFeeBPS) || cfg.MaxExtraFeeBPS < 0 || cfg.MaxExtraFeeBPS > 1000 || !finite(cfg.MaxExtraSlippageBPS) || cfg.MaxExtraSlippageBPS < 0 || cfg.MaxExtraSlippageBPS > 1000 || cfg.MaxLatencyMS < 0 || cfg.MaxLatencyMS > 86400000 {
				return nil, errors.New("invalid Monte Carlo execution bounds")
			}
			rng := rand.New(rand.NewSource(int64(cfg.Seed)))
			for i := 0; i < cfg.Samples; i++ {
				shocks = append(shocks, validationShock{Name: fmt.Sprintf("sample-%03d", i+1), ExtraFeeBPS: rng.Float64() * cfg.MaxExtraFeeBPS, ExtraSlippageBPS: rng.Float64() * cfg.MaxExtraSlippageBPS, LatencyMS: int64(rng.Float64() * float64(cfg.MaxLatencyMS))})
			}
		case "stress":
			if len(cfg.Scenarios) == 0 {
				liquidity := 0.1
				cfg.Scenarios = []validationShock{{Name: "high costs", CostMultiplier: 3, ExtraSlippageBPS: 25}, {Name: "slow execution", LatencyMS: 5000}, {Name: "liquidity drought", LiquidityFraction: &liquidity}, {Name: "market crash", PriceShockPct: -30}, {Name: "late features", FeatureDelayMS: 3600000}}
			}
			shocks = append(shocks, cfg.Scenarios...)
		case "robustness":
			if len(cfg.Scenarios) == 0 {
				cfg.Scenarios = []validationShock{{Name: "higher friction", CostMultiplier: 1.5, ExtraSlippageBPS: 5}, {Name: "slower execution", LatencyMS: 500}}
			}
			shocks = append(shocks, cfg.Scenarios...)
		}
		if len(shocks)*len(cfg.Candidates) > 512 {
			return nil, errors.New("validation exceeds 512 runs")
		}
		for ci := range cfg.Candidates {
			for si, shock := range shocks {
				phase := "scenario"
				if si == 0 {
					phase = "baseline"
				}
				add(0, phase, ci, 0, n, shock)
			}
		}
	}
	total := 0
	for _, c := range plan {
		spec, tape, warm, err := validationCaseData(source, *cfg, c, maxInt(c.Candidate, 0))
		if err != nil {
			return nil, err
		}
		total += len(tape) + len(warm)
		if total > 10000000 {
			return nil, errors.New("validation exceeds 10 million replay inputs; reduce windows, candidates or samples")
		}
		if _, err = sim.New(spec.Config, tape, nil, nil); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

func validationCaseData(source validationSource, cfg validationConfig, c validationCase, ci int) (simulationSpec, []sim.Input, []sim.Input, error) {
	spec := cloneValidation(source.Spec)
	candidate := cfg.Candidates[ci]
	if spec.DecisionMode == "agent" {
		spec.Agent.Directive = candidate.Directive
	} else {
		spec.Strategy = cloneValidation(candidate.Strategy)
	}
	spec.Config.Seed = c.Seed
	shock := c.Shock
	if shock.CostMultiplier == 0 {
		shock.CostMultiplier = 1
	}
	if !finite(shock.CostMultiplier) || shock.CostMultiplier < 1 || shock.CostMultiplier > 100 || !finite(shock.ExtraFeeBPS) || shock.ExtraFeeBPS < 0 || shock.ExtraFeeBPS > 1000 || !finite(shock.ExtraSlippageBPS) || shock.ExtraSlippageBPS < 0 || shock.ExtraSlippageBPS > 1000 || shock.LatencyMS < 0 || shock.LatencyMS > 86400000 || shock.FeatureDelayMS < 0 || shock.FeatureDelayMS > 7*86400000 || !finite(shock.PriceShockPct) || shock.PriceShockPct < -95 || shock.PriceShockPct > 95 {
		return spec, nil, nil, errors.New("invalid stress scenario")
	}
	adjust := func(p sim.Costs) sim.Costs {
		p.FeeBps = p.FeeBps*shock.CostMultiplier + shock.ExtraFeeBPS
		p.SlippageBps = p.SlippageBps*shock.CostMultiplier + shock.ExtraSlippageBPS
		p.SpreadBps *= shock.CostMultiplier
		p.ImpactBps *= shock.CostMultiplier
		return p
	}
	spec.Config.Costs = adjust(spec.Config.Costs)
	for s, p := range spec.Config.SymbolCosts {
		spec.Config.SymbolCosts[s] = adjust(p)
	}
	spec.Config.SubmissionLatencyMS += shock.LatencyMS
	spec.Config.CancellationLatencyMS += shock.LatencyMS
	if shock.LiquidityFraction != nil {
		f := *shock.LiquidityFraction
		if !finite(f) || f < 0 || f > 1 {
			return spec, nil, nil, errors.New("liquidity_fraction must be 0–1")
		}
		if spec.Config.ParticipationRate == 0 {
			spec.Config.ParticipationRate = 1
		}
		if spec.Config.MaxFillQty > 0 {
			spec.Config.MaxFillQty *= math.Max(f, 1e-12)
		}
	}
	tape := []sim.Input{}
	warm := []sim.Input{}
	midpoint := c.Start.Add(c.End.Sub(c.Start) / 2)
	lower := c.WarmupStart.Add(-time.Duration(shock.FeatureDelayMS) * time.Millisecond)
	from := sort.Search(len(source.Inputs), func(i int) bool { return !source.Inputs[i].AvailableAt.Before(lower) })
	to := sort.Search(len(source.Inputs), func(i int) bool { return !source.Inputs[i].AvailableAt.Before(c.End) })
	all := append(cloneValidation(source.Spec.WarmupInputs), cloneValidation(source.Inputs[from:to])...)
	for _, in := range all {
		if !strings.HasPrefix(in.Type, "market.") {
			in.AvailableAt = in.AvailableAt.Add(time.Duration(shock.FeatureDelayMS) * time.Millisecond)
		}
		if in.AvailableAt.Before(c.WarmupStart) || !in.AvailableAt.Before(c.End) {
			continue
		}
		if strings.HasPrefix(in.Type, "market.") {
			if in.AvailableAt.Compare(midpoint) >= 0 {
				for _, key := range []string{"price", "bid", "ask"} {
					if in.Data[key] > 0 {
						in.Data[key] *= 1 + shock.PriceShockPct/100
					}
				}
			}
			if shock.LiquidityFraction != nil {
				in.Data["volume"] *= *shock.LiquidityFraction
			}
		}
		if in.AvailableAt.Before(c.Start) {
			if in.Type == "market.bar.close" {
				in.Data["step"] = 0
			}
			warm = append(warm, in)
		} else {
			tape = append(tape, in)
		}
	}
	// Preserve canonical ordering after feature delays and renumber completed bars
	// for the window's signal cadence. Sim.New copies the maps before use.
	e, err := sim.New(spec.Config, tape, nil, nil)
	if err != nil {
		return spec, nil, nil, err
	}
	tape = e.Inputs
	step := 0
	var last time.Time
	for i := range tape {
		if tape[i].Type == "market.bar.close" {
			if !tape[i].AvailableAt.Equal(last) {
				step++
				last = tape[i].AvailableAt
			}
			tape[i].Data["step"] = float64(step)
		}
	}
	hasBenchmark := false
	for _, in := range tape {
		if in.Type == "market.quote" && in.Symbol == spec.Config.BenchmarkSymbol {
			hasBenchmark = true
		}
	}
	if !hasBenchmark {
		return spec, nil, nil, errors.New("each validation window requires benchmark quotes")
	}
	if len(warm) > 0 {
		sorted, err := sim.New(spec.Config, warm, nil, nil)
		if err != nil {
			return spec, nil, nil, err
		}
		warm = sorted.Inputs
	}
	spec.WarmupInputs = warm
	return spec, tape, warm, nil
}

type validationDistribution struct {
	Count int     `json:"count"`
	Mean  float64 `json:"mean"`
	P05   float64 `json:"p05"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	Worst float64 `json:"worst"`
}

func validationStats(xs []float64) validationDistribution {
	if len(xs) == 0 {
		return validationDistribution{}
	}
	xs = append([]float64(nil), xs...)
	sort.Float64s(xs)
	q := func(p float64) float64 {
		x := p * float64(len(xs)-1)
		i := int(x)
		j := min(i+1, len(xs)-1)
		return xs[i] + (xs[j]-xs[i])*(x-float64(i))
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return validationDistribution{Count: len(xs), Mean: sum / float64(len(xs)), P05: q(.05), P50: q(.5), P95: q(.95), Worst: xs[0]}
}

// Expand explicit parameter values in stable path order; training selection
// uses the resulting fixed candidates, never parameters fitted on a test fold.
func validationGrid(base map[string]any, grid map[string][]any) ([]validationCandidate, error) {
	paths := []string{}
	count := 1
	for path, values := range grid {
		if path == "" || len(values) == 0 || len(values) > 16 {
			return nil, errors.New("parameter grid paths need 1–16 values")
		}
		count *= len(values)
		if count > 16 {
			return nil, errors.New("parameter grid exceeds 16 combinations")
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := []validationCandidate{{Name: "grid", Strategy: cloneValidation(base)}}
	for _, path := range paths {
		next := []validationCandidate{}
		for _, c := range out {
			for _, v := range grid[path] {
				switch v.(type) {
				case float64, int, string, bool:
				default:
					return nil, errors.New("parameter values must be scalars")
				}
				strategy := cloneValidation(c.Strategy)
				if err := setValidationParameter(strategy, strings.Split(path, "."), v); err != nil {
					return nil, err
				}
				next = append(next, validationCandidate{Name: fmt.Sprintf("%s · %s=%v", c.Name, path, v), Strategy: strategy})
			}
		}
		out = next
	}
	return out, nil
}
func setValidationParameter(node any, path []string, value any) error {
	if len(path) == 0 {
		return errors.New("empty parameter path")
	}
	var child any
	switch n := node.(type) {
	case map[string]any:
		var ok bool
		child, ok = n[path[0]]
		if !ok {
			return fmt.Errorf("unknown parameter path %s", path[0])
		}
		if len(path) == 1 {
			n[path[0]] = value
			return nil
		}
	case []any:
		i, err := strconv.Atoi(path[0])
		if err != nil || i < 0 || i >= len(n) {
			return errors.New("invalid parameter array index")
		}
		child = n[i]
		if len(path) == 1 {
			n[i] = value
			return nil
		}
	default:
		return errors.New("parameter path does not point to an object or array")
	}
	return setValidationParameter(child, path[1:], value)
}
