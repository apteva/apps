package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	sim "github.com/apteva/apps/mcp/strategies/internal/backtest"
	rules "github.com/apteva/apps/mcp/strategies/internal/ruleengine"
)

// presetTape is a seeded daily market for a preset's symbols plus the
// forecast and sentiment features the research presets read. KO tracks PEP
// with a mean-reverting spread so the pairs preset has something to trade.
func presetTape(symbols []string, days int) []sim.Input {
	rng := rand.New(rand.NewSource(7))
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	price := map[string]float64{}
	drift := map[string]float64{}
	for i, s := range symbols {
		price[s] = 50 + float64(i)*10
		drift[s] = (float64(i%5) - 2) * 0.0008
	}
	spread := 0.0
	var tape []sim.Input
	for d := 0; d < days; d++ {
		at := start.AddDate(0, 0, d)
		spread = 0.9*spread + rng.NormFloat64()*0.02
		for _, s := range symbols {
			prev := price[s]
			ret := drift[s] + rng.NormFloat64()*0.015
			if s == "KO" {
				ret = math.Log(price["PEP"]*0.4*math.Exp(spread)/prev) + rng.NormFloat64()*0.001
			}
			close := prev * math.Exp(ret)
			price[s] = close
			o := prev
			h, l := math.Max(o, close)*1.003, math.Min(o, close)*0.997
			tape = append(tape,
				sim.Input{ID: fmt.Sprintf("q/%s/%d", s, d), Type: "market.quote", Symbol: s, Source: "synthetic", EventTime: at, AvailableAt: at, Data: map[string]float64{"price": o, "volume": 1e6}},
				sim.Input{ID: fmt.Sprintf("b/%s/%d", s, d), Type: "market.bar.close", Symbol: s, Source: "synthetic", EventTime: at, AvailableAt: at.Add(24 * time.Hour), Data: map[string]float64{"price": close, "open": o, "high": h, "low": l, "volume": 1e6}, Metadata: map[string]string{"timeframe": "1d"}},
				// Features published an hour before the close they inform.
				sim.Input{ID: fmt.Sprintf("f/%s/%d", s, d), Type: "feature.forecast", Symbol: s, Source: "synthetic", EventTime: at, AvailableAt: at.Add(23 * time.Hour), Data: map[string]float64{"value": drift[s]*100 + rng.NormFloat64()*0.05}},
				sim.Input{ID: fmt.Sprintf("n/%s/%d", s, d), Type: "feature.sentiment", Symbol: s, Source: "synthetic", EventTime: at, AvailableAt: at.Add(23 * time.Hour), Data: map[string]float64{"score": math.Tanh(drift[s]*500 + rng.NormFloat64())}},
			)
		}
	}
	return tape
}

func runPreset(t *testing.T, p strategyPreset, days int) (*sim.Engine, bool) {
	t.Helper()
	program := p.Definition.Program
	config := sim.Config{Seed: 1, StartingCash: 1e6, Costs: sim.Costs{QtyStep: 0.0001, FeeBps: 1, SlippageBps: 2}, BenchmarkSymbol: program.Symbols[0], NotifyFills: true, PruneResolved: true}
	if p.Shorts {
		config.Shorts = sim.DefaultShorts()
	}
	strategy, err := rules.Strategy(program, config)
	if err != nil {
		t.Fatalf("%s: %v", p.ID, err)
	}
	inputs, err := rules.ScheduleInputs(program, presetTape(program.Symbols, days))
	if err != nil {
		t.Fatalf("%s: %v", p.ID, err)
	}
	e, err := sim.New(config, inputs, nil, strategy)
	if err != nil {
		t.Fatalf("%s: %v", p.ID, err)
	}
	sawShort := false
	for !e.State.Finished {
		if _, err := e.Advance(); err != nil {
			t.Fatalf("%s at %s: %v", p.ID, e.State.Now, err)
		}
		for _, pos := range e.State.Positions {
			sawShort = sawShort || pos.Qty < 0
		}
	}
	return e, sawShort
}

func TestStrategyPresetsBacktest(t *testing.T) {
	presets := strategyPresets(nil)
	if len(presets) < 6 {
		t.Fatalf("presets = %d", len(presets))
	}
	for _, p := range presets {
		p := p
		t.Run(p.ID, func(t *testing.T) {
			def, _, err := validateStrategyDefinition(mustJSONMap(t, map[string]any{"program": p.Definition.Program}))
			if err != nil {
				t.Fatalf("preset does not validate: %v", err)
			}
			// Sign-driven books (TSMOM) are refused live when a weight turns
			// negative; dollar-neutral legs are refused up front.
			if live := liveSupport(def); !p.Shorts && live != nil {
				t.Fatalf("long-only preset refused live: %v", live)
			}
			e, sawShort := runPreset(t, p, 420)
			m := e.Metrics()
			if e.State.Fees <= 0 {
				t.Fatalf("preset never traded: %v", m)
			}
			if p.Shorts && !sawShort {
				t.Fatalf("long/short preset never held a short: %v", m)
			}
			if !p.Shorts && sawShort {
				t.Fatal("long-only preset went short")
			}
			if p.Shorts {
				if _, ok := m["short_pnl"]; !ok {
					t.Fatalf("short metrics missing: %v", m)
				}
			}
		})
	}
}

func TestTrendRotationPresetRunsLive(t *testing.T) {
	newTestCtx(t)
	globalEngine.provider = &recordingStrategyProvider{}
	p := strategyPresets([]string{"BTC-USD", "ETH-USD", "SOL-USD", "XRP-USD", "ADA-USD"})[0]
	if p.ID != "trend_rotation" {
		t.Fatalf("first preset = %s", p.ID)
	}
	def, _, err := validateStrategyDefinition(mustJSONMap(t, map[string]any{"program": p.Definition.Program}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluateRulesLive(def, cashState(100000), nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// The month variable starts at 0, so the first live bar rebalances.
	if result.Targets == nil || len(result.Targets.TargetAllocations) == 0 {
		t.Fatalf("live rotation produced no targets: %#v", result)
	}
	for _, a := range result.Targets.TargetAllocations {
		if a.Weight > 0.25+1e-9 {
			t.Fatalf("weight %v over the 25%% cap", a.Weight)
		}
	}
}

// The catalog's simulation block names only what a template changes, and a
// validated target action carries no empty sizing.
func TestCatalogJSONIsMinimal(t *testing.T) {
	raw, err := json.Marshal(strategyCatalog(nil))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Presets []struct {
			ID         string                     `json:"id"`
			Simulation map[string]json.RawMessage `json:"simulation"`
		} `json:"presets"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	shorts := 0
	for _, p := range catalog.Presets {
		if p.Simulation == nil {
			continue
		}
		shorts++
		if len(p.Simulation) != 1 || p.Simulation["shorts"] == nil {
			t.Fatalf("%s simulation = %v, want only shorts", p.ID, p.Simulation)
		}
	}
	if shorts == 0 {
		t.Fatal("no preset carries a simulation block")
	}
	def := mustTargetDefinition([]string{"BTC-USD", "ETH-USD"}, "1d", 0.5)
	out, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"sizing"`) {
		t.Fatalf("target definition echoes empty sizing: %s", out)
	}
}
