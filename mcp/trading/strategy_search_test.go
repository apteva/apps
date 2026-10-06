//go:build research

package main

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

func TestStrategySearch(t *testing.T) {
	jobPath := os.Getenv("STRATEGY_SEARCH_JOB")
	if jobPath == "" {
		t.Skip("set STRATEGY_SEARCH_JOB")
	}
	var job struct {
		Input         string    `json:"input"`
		OutputDir     string    `json:"output_dir"`
		Start         time.Time `json:"start"`
		End           time.Time `json:"end"`
		Latencies     []int64   `json:"latencies"`
		ExtraFee      float64   `json:"extra_fee"`
		ExtraSlippage float64   `json:"extra_slippage"`
		SaveAll       bool      `json:"save_all"`
		Candidates    []struct {
			ID       string         `json:"id"`
			Name     string         `json:"name"`
			Strategy map[string]any `json:"strategy"`
		} `json:"candidates"`
	}
	read := func(path string, v any) []byte {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	read(jobPath, &job)
	var original simulationArtifact
	raw := read(job.Input, &original)
	if err := os.MkdirAll(job.OutputDir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(job.OutputDir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	source := validationSource{Spec: cloneValidation(original.Spec), Inputs: original.Inputs}
	source.Spec.SourceHash = simulationSourceHash()
	source.Spec.ReferenceManifest = map[string]any{"origin_artifact_sha256": fmt.Sprintf("%x", sha256.Sum256(raw)), "origin_engine_sha256": original.Spec.SourceHash, "experiment": "Fixed candidate search; all decisions and results recomputed with current source. See frozen protocol and job configuration.", "market_reference": original.Spec.ReferenceManifest}
	if len(job.Candidates) == 0 || !job.End.After(job.Start) {
		t.Fatal("candidates and valid start/end required")
	}
	for _, candidate := range job.Candidates {
		if _, _, err := validateStrategyDefinition(candidate.Strategy); err != nil {
			t.Fatalf("%s: %v", candidate.ID, err)
		}
	}
	results := []map[string]any{}
	if prior, err := os.ReadFile(filepath.Join(job.OutputDir, "results.json")); err == nil {
		if err = json.Unmarshal(prior, &results); err != nil {
			t.Fatal(err)
		}
	}
	best := -1e100
	for _, r := range results {
		if m, ok := r["metrics"].(map[string]any); ok {
			if v, ok := m["return_pct"].(float64); ok && v > best {
				best = v
			}
		}
	}
	type observations struct {
		Quotes        map[string]sim.Quote
		Features      map[string]map[string]float64
		StrategyState json.RawMessage
	}
	warmCache := map[string]observations{}

	for _, ms := range job.Latencies {
		for i, candidate := range job.Candidates {
			done := false
			for _, r := range results {
				if r["id"] == candidate.ID && r["latency_ms"] == float64(ms) {
					done = true
					break
				}
			}
			if done {
				fmt.Println("reuse", candidate.ID, ms)
				continue
			}
			began := time.Now()
			source.Spec.Config.SubmissionLatencyMS = ms
			source.Spec.Config.CancellationLatencyMS = 0
			source.Spec.Config.LatencyJitterMS = 0
			cfg := validationConfig{Candidates: []validationCandidate{{Name: candidate.Name, Strategy: candidate.Strategy}}}
			c := validationCase{Phase: "test", Start: job.Start, End: job.End, WarmupStart: original.Inputs[0].AvailableAt, Seed: 42, Shock: validationShock{ExtraFeeBPS: job.ExtraFee, ExtraSlippageBPS: job.ExtraSlippage}}
			spec, tape, _, err := validationCaseData(source, cfg, c, 0)
			if err != nil {
				t.Fatal(err)
			}
			a := simulationArtifact{Schema: "apteva.backtest/v1", Spec: spec, Inputs: tape, ExecutionNotes: simulationExecutionNotes(spec, tape)}
			a.InputHash = sim.Hash(struct {
				Spec   simulationSpec `json:"spec"`
				Inputs []sim.Input    `json:"inputs"`
			}{spec, tape})
			// Warmup only observes nonpositive-step closed bars. For equal cadence,
			// symbols and immutable warmup tape, observations are strategy-independent.
			// Cache only the three fields copied by newSimulationEngine; cash, counters,
			// orders and execution configuration always start fresh for each candidate.
			def, _, _ := validateStrategyDefinition(spec.Strategy)
			key := sim.Hash(struct {
				Inputs   []sim.Input
				Symbols  []string
				Interval string
				Cadence  string
			}{spec.WarmupInputs, spec.Symbols, spec.Interval, strategyHistoryInterval(def)})
			var e *sim.Engine
			if cached, ok := warmCache[key]; ok {
				e, err = sim.New(spec.Config, tape, nil, simulationStrategy(spec))
				if err == nil {
					copy := cloneValidation(cached)
					e.State.Quotes = copy.Quotes
					e.State.Features = copy.Features
					e.State.StrategyState = copy.StrategyState
				}
			} else {
				e, err = newSimulationEngine(&simulationRecord{Spec: spec, Inputs: tape}, simulationStrategy(spec))
				if err == nil {
					warmCache[key] = cloneValidation(observations{e.State.Quotes, e.State.Features, e.State.StrategyState})
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			a.Outputs = []sim.Output{}
			fills, warnings := 0, 0
			for !e.State.Finished {
				out, err := e.Advance()
				if err != nil {
					t.Fatal(err)
				}
				for _, v := range out {
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
			artifact := ""
			if job.SaveAll || i == 0 || a.Result["return_pct"] > best {
				artifact = fmt.Sprintf("%s-%dms.json", candidate.ID, ms)
				write(artifact, a)
			}
			if a.Result["return_pct"] > best {
				best = a.Result["return_pct"]
			}
			row := map[string]any{"id": candidate.ID, "name": candidate.Name, "latency_ms": ms, "metrics": a.Result, "fills": fills, "warnings": warnings, "artifact": artifact, "result_sha256": a.ResultHash, "seconds": time.Since(began).Seconds()}
			results = append(results, row)
			write("results.json", results)
			fmt.Printf("%d/%d %s latency=%d return=%.4f DD=%.4f turnover=%.1f fills=%d warnings=%d [%s]\n", i+1, len(job.Candidates), candidate.ID, ms, a.Result["return_pct"], a.Result["max_drawdown_pct"], a.Result["turnover_pct"], fills, warnings, time.Since(began))
		}
	}
	write("completed.json", map[string]any{"cases": len(results), "source_sha256": simulationSourceHash()})
}
