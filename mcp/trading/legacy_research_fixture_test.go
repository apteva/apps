package main

import (
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// Trading no longer authors strategies, assignments or backtests; those rows
// only exist as preserved legacy research awaiting export to Strategies.
// These fixtures write them with plain SQL so export and "never executes"
// tests do not depend on the removed research engine.

func legacyStrategy(t *testing.T, ctx *sdk.AppCtx, projectID, name, symbol string, weight float64) int64 {
	t.Helper()
	def, err := json.Marshal(map[string]any{
		"universe": []any{symbol}, "cadence": "1h",
		"rules": []any{map[string]any{"name": "fixed allocation", "allocate": []any{map[string]any{"symbol": symbol, "weight": weight}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO strategies(project_id,name,status,version,definition_json) VALUES(?,?,'active',1,?)`, projectID, name, string(def))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := ctx.AppDB().Exec(`INSERT INTO strategy_versions(strategy_id,version,definition_json) VALUES(?,1,?)`, id, string(def)); err != nil {
		t.Fatal(err)
	}
	return id
}

func legacyAssignment(t *testing.T, ctx *sdk.AppCtx, portfolioID, strategyID int64) int64 {
	t.Helper()
	res, err := ctx.AppDB().Exec(`INSERT INTO portfolio_strategy_assignments(project_id,portfolio_id,strategy_id,strategy_version,control_mode,cadence,status) VALUES('test-proj',?,?,1,'strategy','1h','active')`, portfolioID, strategyID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func legacyBacktestRun(t *testing.T, ctx *sdk.AppCtx, portfolioID, strategyID int64, name string) int64 {
	t.Helper()
	res, err := ctx.AppDB().Exec(`INSERT INTO backtest_runs(project_id,portfolio_id,source_agent_id,strategy_id,strategy_version,run_kind,name,status,start_at,end_at,starting_cash)
		VALUES('test-proj',?,0,?,1,'strategy',?,'completed','2026-01-01','2026-03-31',100000)`, portfolioID, strategyID, name)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}
