// rule-backtest runs a saved generic program against a supplied event tape.
// It does not require the Apteva server, an account, a broker, or credentials.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
	rules "github.com/apteva/apps/mcp/trading/internal/ruleengine"
)

func run() error {
	example := flag.String("example", "", "example JSON containing definition and simulation")
	input := flag.String("input", "", "event array or proof/artifact JSON containing inputs")
	configFile := flag.String("config", "", "optional execution configuration JSON overriding example simulation")
	output := flag.String("output", "", "result bundle path (default stdout)")
	flag.Parse()
	if *example == "" || *input == "" {
		return errors.New("usage: rule-backtest --example rule_examples/NAME.json --input tape.json [--config costs.json] [--output result.json]")
	}
	raw, err := os.ReadFile(*example)
	if err != nil {
		return err
	}
	var sample struct {
		Definition struct {
			Engine  string          `json:"engine"`
			Program json.RawMessage `json:"program"`
		} `json:"definition"`
		Simulation sim.Config `json:"simulation"`
	}
	if err = json.Unmarshal(raw, &sample); err != nil {
		return err
	}
	if sample.Definition.Engine != "rules" {
		return errors.New("example must use engine=rules")
	}
	var program rules.Program
	decoder := json.NewDecoder(bytes.NewReader(sample.Definition.Program))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&program); err != nil {
		return err
	}
	config := sample.Simulation
	if *configFile != "" {
		raw, err = os.ReadFile(*configFile)
		if err != nil {
			return err
		}
		decoder = json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&config); err != nil {
			return err
		}
	}
	raw, err = os.ReadFile(*input)
	if err != nil {
		return err
	}
	var tape []sim.Input
	if err = json.Unmarshal(raw, &tape); err != nil {
		var envelope struct {
			Inputs []sim.Input `json:"inputs"`
		}
		if err = json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		tape = envelope.Inputs
	}
	sources := map[string]bool{}
	synthetic := false
	for _, in := range tape {
		if in.Type == "market.quote" || in.Type == "market.bar.close" {
			if in.Symbol != program.Symbol || in.Source == "" {
				return errors.New("market events require program symbol and explicit source")
			}
			sources[in.Source] = true
			if in.Source == "synthetic_acceptance" {
				synthetic = true
			}
		}
	}
	tape, err = rules.ScheduleInputs(&program, tape)
	if err != nil {
		return err
	}
	config.NotifyFills = true
	strategy, err := rules.Strategy(&program, config)
	if err != nil {
		return err
	}
	engine, err := sim.New(config, tape, nil, strategy)
	if err != nil {
		return err
	}
	if err = rules.ValidateTape(&program, engine.Inputs); err != nil {
		return err
	}
	var outputs []sim.Output
	for !engine.State.Finished {
		out, err := engine.Advance()
		if err != nil {
			return err
		}
		outputs = append(outputs, out...)
	}
	entries := 0
	for _, o := range engine.State.Orders {
		if o.ParentID == "" && !o.ReduceOnly && o.FilledQty > 0 {
			entries++
		}
	}
	names := []string{}
	for source := range sources {
		names = append(names, source)
	}
	sort.Strings(names)
	dataClass := "supplied_event_tape"
	if synthetic {
		dataClass = "synthetic_acceptance"
	}
	result := map[string]any{"schema": "apteva.rule-proof/v1", "data_class": dataClass, "declared_sources": names, "engine_version": sim.Version, "rule_version": rules.Version, "execution_source_sha256": sim.SourceHash(), "rule_source_sha256": rules.SourceHash(), "program_sha256": sim.Hash(program), "definition": map[string]any{"engine": "rules", "universe": []string{program.Symbol}, "cadence": program.Timeframe, "program": program}, "simulation": config, "inputs": engine.Inputs, "input_sha256": sim.Hash(engine.Inputs), "outputs": outputs, "output_sha256": sim.Hash(outputs), "metrics": engine.Metrics(), "filled_entries": entries, "terminal_state": engine.State, "execution_notes": "Only observed quotes supply fills. Native OCO cancellation and linear contract accounting are simulation models. Fixed currency conversion; no financing or automatic margin liquidation. Supplied source labels are not independently verified."}
	raw, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if *output == "" {
		_, err = os.Stdout.Write(append(raw, '\n'))
		return err
	}
	if err = os.WriteFile(*output, append(raw, '\n'), 0644); err != nil {
		return err
	}
	fmt.Printf("%s: %d entries, realized P&L %.6f, equity %.6f (%s)\n", *output, entries, engine.Metrics()["realized_pnl"], engine.Metrics()["equity"], dataClass)
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
