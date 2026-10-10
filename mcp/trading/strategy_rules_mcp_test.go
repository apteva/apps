package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

// Exercise the registered, agent-exposed handlers with JSON wire values rather
// than calling database helpers or bypassing the MCP entry points.
func callRuleMCP(t *testing.T, ctx *sdk.AppCtx, name string, args map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name != name {
			continue
		}
		if tool.Exposure == sdk.ToolExposureAppOnly {
			t.Fatalf("%s is hidden from agents", name)
		}
		result, err := tool.HandlerCtx(context.Background(), ctx, wire)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		encoded, err = json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err := json.Unmarshal(encoded, &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	t.Fatalf("MCP tool missing: %s", name)
	return nil
}

func TestRuleMCPAgentWorkflow(t *testing.T) {
	ctx := newTestCtx(t)
	catalog := callRuleMCP(t, ctx, "strategy_catalog", map[string]any{})
	engine := catalog["rule_engine"].(map[string]any)
	if engine["authoring"] == nil || engine["mcp_workflow"] == nil || engine["live_assignment_supported"] != false {
		t.Fatal("agent discovery lacks authoring guidance or execution limits")
	}
	presets := catalog["rule_presets"].([]any)
	if len(presets) != 3 {
		t.Fatal("three screenshot examples are not discoverable")
	}
	var sessionID float64
	var sessionConfig any
	for _, raw := range presets {
		preset := raw.(map[string]any)
		definition := preset["definition"].(map[string]any)
		validation := callRuleMCP(t, ctx, "strategy_validate", map[string]any{"definition": definition})
		if validation["valid"] != true {
			t.Fatal("MCP rejected catalog definition", validation)
		}
		created := callRuleMCP(t, ctx, "strategy_create", map[string]any{"name": preset["name"], "definition": definition})["strategy"].(map[string]any)
		id := created["id"].(float64)
		if created["version"] != float64(1) || created["definition"].(map[string]any)["engine"] != "rules" {
			t.Fatal("rule program was not saved correctly")
		}
		definition["program"].(map[string]any)["initial"] = map[string]any{"agent_revision": 1}
		updated := callRuleMCP(t, ctx, "strategy_update", map[string]any{"strategy_id": id, "definition": definition})["strategy"].(map[string]any)
		if updated["version"] != float64(2) {
			t.Fatal("MCP rule revision was not versioned")
		}
		fetched := callRuleMCP(t, ctx, "strategy_get", map[string]any{"strategy_id": id})["strategy"].(map[string]any)
		if sim.Hash(fetched["definition"]) != sim.Hash(definition) {
			t.Fatal("saved program differs from agent definition")
		}
		if preset["id"] == "session_range_breakout" {
			sessionID, sessionConfig = id, preset["simulation"]
		}
	}
	if sessionID == 0 {
		t.Fatal("session example missing")
	}
	portfolio := callRuleMCP(t, ctx, "portfolio_create", map[string]any{"name": "MCP rule proof", "allowed_classes": []string{"equity"}, "starting_cash": 100000, "execution_environment": "simulation", "fee_bps": 0, "slippage_bps": 0})
	pid := portfolio["portfolio_id"].(float64)
	loc, _ := time.LoadLocation("Europe/Helsinki")
	start := time.Date(2026, 9, 14, 8, 0, 0, 0, loc)
	var tape []sim.Input
	for i := 0; i < 180; i++ {
		tape = append(tape, ruleBar(time.Duration(i).String(), "DE40", "1m", start.Add(time.Duration(i)*time.Minute), 16000, 16020, 15980, 16000))
	}
	tape = append(tape, ruleQuote("opening", "DE40", start.Add(3*time.Hour), 16000), ruleQuote("breakout", "DE40", start.Add(3*time.Hour+time.Minute), 16020), ruleQuote("stop", "DE40", start.Add(3*time.Hour+2*time.Minute), 15980))
	// Normalize broker-shaped exports through the registered agent tool before
	// testing the exact same strategy lifecycle. These prices are synthetic.
	var barsCSV, quotesCSV strings.Builder
	barsCSV.WriteString("timestamp,open,high,low,close\n")
	quotesCSV.WriteString("Time,Bid,Ask\n")
	for _, in := range tape {
		if in.Type == "market.bar.close" {
			fmt.Fprintf(&barsCSV, "%s,%g,%g,%g,%g\n", in.EventTime.Format(time.RFC3339), in.Data["open"], in.Data["high"], in.Data["low"], in.Data["price"])
		} else {
			fmt.Fprintf(&quotesCSV, "%s,%g,%g\n", in.EventTime.Format(time.RFC3339), in.Data["price"], in.Data["price"])
		}
	}
	datasetImported := callRuleMCP(t, ctx, "market_data_import", map[string]any{"symbol": "DE40", "streams": []map[string]any{
		{"kind": "bars", "csv": barsCSV.String(), "source": "synthetic_acceptance", "timeframe": "1m", "price_basis": "bid"},
		{"kind": "quotes", "csv": quotesCSV.String(), "source": "synthetic_acceptance", "columns": map[string]string{"timestamp": "Time", "bid": "Bid", "ask": "Ask"}},
	}})
	if datasetImported["input_sha256"] == "" || len(datasetImported["streams"].([]any)) != 2 {
		t.Fatal("import lacks immutable data identities")
	}
	raw, _ := json.Marshal(datasetImported["inputs"])
	if err := json.Unmarshal(raw, &tape); err != nil {
		t.Fatal(err)
	}
	// Import orders by availability; select the stop quote by timestamp rather
	// than assuming its old position in the unsorted construction above.
	sort.SliceStable(tape, func(i, j int) bool { return tape[i].AvailableAt.Before(tape[j].AvailableAt) })
	createdRun := callRuleMCP(t, ctx, "strategy_backtest_create", map[string]any{"portfolio_id": pid, "strategy_id": sessionID, "inputs": tape, "simulation": sessionConfig, "starting_cash": 100000})["backtest"].(map[string]any)
	runID := createdRun["id"].(float64)
	callRuleMCP(t, ctx, "backtest_control", map[string]any{"backtest_id": runID, "action": "run"})
	waitSimulationWorker(int64(runID))
	completed := callRuleMCP(t, ctx, "backtest_control", map[string]any{"backtest_id": runID, "action": "status"})["backtest"].(map[string]any)
	if completed["status"] != "completed" {
		t.Fatal("MCP backtest did not complete", completed)
	}
	callRuleMCP(t, ctx, "strategy_scorecard_update", map[string]any{
		"portfolio_id": pid, "strategy_id": sessionID, "require_out_of_sample": false,
		"criteria": []map[string]any{{"metric": "total_pnl", "operator": "min", "threshold": 0, "required": true}},
	})
	losing := callRuleMCP(t, ctx, "strategy_scorecard_evaluate", map[string]any{"portfolio_id": pid, "strategy_id": sessionID, "backtest_run_id": runID})["evaluation"].(map[string]any)
	if losing["passed"] != false || losing["verdict"] != "fail" {
		t.Fatal("MCP evaluation did not retain the losing result", losing)
	}
	// A second captured tape closes profitably at the session deadline. Both
	// verdicts must be measured from completed runs under the same policy.
	winningTape := append([]sim.Input(nil), tape[:len(tape)-1]...)
	deadline := start.Add(10 * time.Hour)
	winningTape = append(winningTape, ruleQuote("holding", "DE40", start.Add(3*time.Hour+2*time.Minute), 16025), ruleQuote("deadline", "DE40", deadline, 16030))
	winningRun := callRuleMCP(t, ctx, "strategy_backtest_create", map[string]any{"portfolio_id": pid, "strategy_id": sessionID, "inputs": winningTape, "simulation": sessionConfig, "starting_cash": 100000})["backtest"].(map[string]any)
	winningID := winningRun["id"].(float64)
	callRuleMCP(t, ctx, "backtest_control", map[string]any{"backtest_id": winningID, "action": "run"})
	waitSimulationWorker(int64(winningID))
	winning := callRuleMCP(t, ctx, "strategy_scorecard_evaluate", map[string]any{"portfolio_id": pid, "strategy_id": sessionID, "backtest_run_id": winningID})["evaluation"].(map[string]any)
	if winning["passed"] != true || winning["verdict"] != "pass" {
		t.Fatal("MCP evaluation did not measure the profitable result", winning)
	}
	scorecard := callRuleMCP(t, ctx, "strategy_scorecard_get", map[string]any{"portfolio_id": pid, "strategy_id": sessionID})
	if len(scorecard["evaluations"].([]any)) != 2 {
		t.Fatal("MCP scorecard history lost an evaluation")
	}
	suite := callRuleMCP(t, ctx, "validation_create", map[string]any{"source_backtest_id": winningID, "config": map[string]any{
		"mode": "robustness", "scenarios": []map[string]any{{"name": "expensive_fills", "extra_fee_bps": 100}},
	}})["suite"].(map[string]any)
	suiteID := suite["id"].(float64)
	callRuleMCP(t, ctx, "validation_control", map[string]any{"suite_id": suiteID, "action": "run"})
	until := time.Now().Add(10 * time.Second)
	for {
		suite = callRuleMCP(t, ctx, "validation_control", map[string]any{"suite_id": suiteID, "action": "status"})["suite"].(map[string]any)
		if suite["status"] == "completed" {
			break
		}
		if suite["status"] == "failed" || time.Now().After(until) {
			t.Fatal("MCP cost robustness did not complete", suite)
		}
		time.Sleep(10 * time.Millisecond)
	}
	report := suite["report"].(map[string]any)
	group := report["groups"].(map[string]any)["Source"].(map[string]any)
	if report["completed"] != float64(2) || report["failed"] != float64(0) || group["baseline_return_pct"].(float64) <= 0 || group["return_pct"].(map[string]any)["mean"].(float64) >= 0 {
		t.Fatal("MCP robustness did not retain baseline and cost-sensitive loss", report)
	}
	callRuleMCP(t, ctx, "validation_report", map[string]any{"suite_id": suiteID, "artifact": true})
	artifact := callRuleMCP(t, ctx, "backtest_artifact", map[string]any{"backtest_id": runID})
	if len(artifact["outputs"].([]any)) == 0 {
		t.Fatal("MCP export omitted event evidence")
	}
	imported := callRuleMCP(t, ctx, "backtest_artifact", map[string]any{"portfolio_id": pid, "artifact": artifact})["backtest"].(map[string]any)
	importID := imported["id"].(float64)
	callRuleMCP(t, ctx, "backtest_control", map[string]any{"backtest_id": importID, "action": "run"})
	waitSimulationWorker(int64(importID))
	reproduced := callRuleMCP(t, ctx, "backtest_artifact", map[string]any{"backtest_id": importID})
	if sim.Hash(artifact["outputs"]) != sim.Hash(reproduced["outputs"]) {
		t.Fatal("MCP export/import changed the replay")
	}
}

func TestSimulationResultHashIgnoresKeyOrderAndPreservesNumbers(t *testing.T) {
	result := map[string]float64{"equity": 100000}
	first := []sim.Output{{Data: json.RawMessage(`{"z":9007199254740993,"a":1}`)}}
	reordered := []sim.Output{{Data: json.RawMessage(`{"a":1,"z":9007199254740993}`)}}
	changed := []sim.Output{{Data: json.RawMessage(`{"a":1,"z":9007199254740992}`)}}
	if simulationResultHash(first, result) != simulationResultHash(reordered, result) {
		t.Fatal("object key order changed result identity")
	}
	if simulationResultHash(first, result) == simulationResultHash(changed, result) {
		t.Fatal("numeric precision was lost when validating artifact contents")
	}
}
