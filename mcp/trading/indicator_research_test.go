//go:build research

package main

// Opt-in historical research, separate from tests and live execution. Reuses an
// immutable market tape, explicitly records its provenance, and recomputes all
// decisions/results under the current engine. Never imports old result hashes.
import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndicatorResearch(t *testing.T) {
	input, outdir := os.Getenv("INDICATOR_RESEARCH_INPUT"), os.Getenv("INDICATOR_RESEARCH_OUTPUT")
	if input == "" || outdir == "" {
		t.Skip("set INDICATOR_RESEARCH_INPUT and INDICATOR_RESEARCH_OUTPUT")
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var old simulationArtifact
	if err = json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(outdir, 0700); err != nil {
		t.Fatal(err)
	}
	origin := fmt.Sprintf("%x", sha256.Sum256(raw))
	spec := cloneValidation(old.Spec)
	spec.SourceHash = simulationSourceHash()
	spec.ReferenceManifest = map[string]any{"research_origin_file_sha256": origin, "research_origin_engine_sha256": old.Spec.SourceHash, "market_reference": old.Spec.ReferenceManifest, "experiment": "New indicator experiment using previously captured prices. All decisions recomputed. July-August dates previously inspected; not a fresh holdout."}
	symbols := []string{}
	for _, s := range spec.Symbols {
		if s != "SPY" {
			symbols = append(symbols, s)
		}
	}
	presets := strategyPresets(symbols)
	passive := StrategyDefinition{Universe: symbols, Cadence: "1h", RebalanceEvery: 24, Risk: StrategyRisk{MaxPositionWeight: 0.15}, Rules: []StrategyRule{{Name: "Equal exposure reference"}}}
	for _, s := range symbols {
		passive.Rules[0].Allocate = append(passive.Rules[0].Allocate, StrategyAllocation{Symbol: s, Weight: 0.6 / float64(len(symbols))})
	}
	presets = append(presets, strategyPreset{ID: "passive60", Name: "Diversified 60%", Definition: passive})
	source := validationSource{Spec: spec, Inputs: old.Inputs}
	start, _ := time.Parse(time.RFC3339, "2026-07-01T00:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-09-01T00:00:00Z")
	results := []map[string]any{}
	write := func(name string, v any) {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(outdir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, ms := range []int64{0, 250} {
		for _, preset := range presets {
			source.Spec.Config.SubmissionLatencyMS = ms
			source.Spec.Config.CancellationLatencyMS = 0
			source.Spec.Config.LatencyJitterMS = 0
			data, _ := json.Marshal(preset.Definition)
			var definition map[string]any
			json.Unmarshal(data, &definition)
			if _, _, err := validateStrategyDefinition(definition); err != nil {
				t.Fatal(err)
			}
			cfg := validationConfig{Candidates: []validationCandidate{{Name: preset.Name, Strategy: definition}}}
			c := validationCase{Phase: "test", Start: start, End: end, WarmupStart: old.Inputs[0].AvailableAt, Seed: 42}
			runSpec, tape, _, err := validationCaseData(source, cfg, c, 0)
			if err != nil {
				t.Fatal(err)
			}
			a := simulationArtifact{Schema: "apteva.backtest/v1", Spec: runSpec, Inputs: tape, ExecutionNotes: simulationExecutionNotes(runSpec, tape)}
			a.InputHash = sim.Hash(struct {
				Spec   simulationSpec `json:"spec"`
				Inputs []sim.Input    `json:"inputs"`
			}{a.Spec, a.Inputs})
			e, err := newSimulationEngine(&simulationRecord{Spec: a.Spec, Inputs: a.Inputs}, simulationStrategy(a.Spec))
			if err != nil {
				t.Fatal(err)
			}
			began := time.Now()
			a.Outputs = []sim.Output{}
			orders, fills, warnings := 0, 0, 0
			for !e.State.Finished {
				out, err := e.Advance()
				if err != nil {
					t.Fatal(err)
				}
				for _, v := range out {
					if v.Type == "order.submitted" {
						orders++
					}
					if v.Type == "fill" {
						fills++
					}
					if v.Type == "strategy.decision" {
						var report map[string]any
						json.Unmarshal(v.Data, &report)
						if w, ok := report["warnings"].([]any); ok && len(w) > 0 {
							warnings++
						}
					}
				}
				a.Outputs = append(a.Outputs, out...)
			}
			a.Result = e.Metrics()
			a.ResultHash = sim.Hash(struct {
				Outputs []sim.Output       `json:"outputs"`
				Result  map[string]float64 `json:"result"`
			}{a.Outputs, a.Result})
			filename := fmt.Sprintf("%s-%dms.json", preset.ID, ms)
			write(filename, a)
			row := map[string]any{"preset": preset.ID, "name": preset.Name, "latency_ms": ms, "metrics": a.Result, "orders": orders, "fills": fills, "warning_reports": warnings, "artifact": filename, "result_sha256": a.ResultHash, "duration_seconds": time.Since(began).Seconds()}
			results = append(results, row)
			write("results.json", results)
			fmt.Printf("%s latency=%d return=%.4f drawdown=%.4f turnover=%.2f fills=%d warnings=%d (%s)\n", preset.ID, ms, a.Result["return_pct"], a.Result["max_drawdown_pct"], a.Result["turnover_pct"], fills, warnings, time.Since(began))
		}
	}
}

func TestIndicatorResearchReplay(t *testing.T) {
	path := os.Getenv("INDICATOR_REPLAY_ARTIFACT")
	if path == "" {
		t.Skip("set INDICATOR_REPLAY_ARTIFACT")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var a simulationArtifact
	if err = json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	if a.Spec.SourceHash != simulationSourceHash() {
		t.Fatal("engine fingerprint differs")
	}
	e, err := newSimulationEngine(&simulationRecord{Spec: a.Spec, Inputs: a.Inputs}, simulationStrategy(a.Spec))
	if err != nil {
		t.Fatal(err)
	}
	outputs := []sim.Output{}
	for !e.State.Finished {
		batch, err := e.Advance()
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, batch...)
	}
	got := sim.Hash(struct {
		Outputs []sim.Output       `json:"outputs"`
		Result  map[string]float64 `json:"result"`
	}{outputs, e.Metrics()})
	if got != a.ResultHash {
		t.Fatalf("replay %s differs from %s", got, a.ResultHash)
	}
	fmt.Println("Exact ledger/result replay verified:", filepath.Base(path), got)
}
