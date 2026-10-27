package main

import (
	"context"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"os"
	"strconv"
	"strings"
	"time"
)

func isResearchTool(name string) bool {
	return strings.HasPrefix(name, "strategy_") || strings.HasPrefix(name, "backtest_") || strings.HasPrefix(name, "validation_") || strings.HasPrefix(name, "agent_backtest_") || name == "market_data_import"
}

func requireStrategiesCaller(call context.Context) error {
	caller := sdk.CallerFrom(call)
	if caller == nil || caller.AgentID != 0 || caller.AppName != "strategies" || caller.AppInstallID == 0 {
		return fmt.Errorf("authenticated Strategies app required")
	}
	return nil
}

func (a *App) MCPTools() []sdk.Tool {
	out := []sdk.Tool{}
	for _, tool := range a.executionTools() {
		if isResearchTool(tool.Name) && tool.Name != "backtest_market_step" {
			continue
		}
		out = append(out, tool)
	}
	out = append(out,
		sdk.Tool{Name: "portfolio_research_snapshot", Exposure: sdk.ToolExposureAppOnly, Description: "Read a versioned portfolio observation for the Strategies integration.", InputSchema: schemaObject(map[string]any{"portfolio_id": map[string]any{"type": "integer"}}, []string{"portfolio_id"}), HandlerCtx: func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			if err := requireStrategiesCaller(call); err != nil {
				return nil, err
			}
			if c := sdk.CallerFrom(call); c.ProjectID != "" {
				args["_project_id"] = c.ProjectID
			}
			return exportResearchSnapshot(ctx, args, false)
		}},
		sdk.Tool{Name: "research_legacy_export", Exposure: sdk.ToolExposureAppOnly, Description: "Export preserved legacy strategy and research data; never resume execution.", InputSchema: schemaObject(nil, nil), HandlerCtx: func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			if err := requireStrategiesCaller(call); err != nil {
				return nil, err
			}
			if c := sdk.CallerFrom(call); c.ProjectID != "" {
				args["_project_id"] = c.ProjectID
			}
			return exportResearchSnapshot(ctx, args, true)
		}},
		sdk.Tool{Name: "market_bars_capture", Exposure: sdk.ToolExposureAppOnly, Description: "Capture sourced market bars for Strategies research.", InputSchema: schemaObject(map[string]any{"symbols": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "interval": map[string]any{"type": "string"}, "start_at": map[string]any{"type": "string"}, "end_at": map[string]any{"type": "string"}, "adjustment_mode": map[string]any{"type": "string"}, "latest": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer"}}, []string{"symbols", "interval"}), HandlerCtx: a.captureResearchBars},
	)
	return out
}

func (a *App) HTTPRoutes() []sdk.Route {
	out := []sdk.Route{}
	for _, route := range a.legacyHTTPRoutes() {
		if strings.HasPrefix(route.Pattern, "/strategies") || strings.HasPrefix(route.Pattern, "/backtests") || strings.HasPrefix(route.Pattern, "/validations") {
			continue
		}
		out = append(out, route)
	}
	return out
}

func exportResearchSnapshot(ctx *sdk.AppCtx, args map[string]any, legacy bool) (*researchSnapshot, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	if !legacy {
		if _, err = dbGetPortfolio(ctx.AppDB(), pid, id); err != nil {
			return nil, fmt.Errorf("portfolio not found")
		}
	}
	installID, _ := strconv.ParseInt(os.Getenv("APTEVA_INSTALL_ID"), 10, 64)
	snap := &researchSnapshot{Version: 1, SourceInstallID: installID, ProjectID: pid, Tables: map[string][]map[string]any{}}
	queries := map[string]string{}
	if legacy {
		for _, table := range []string{"strategies", "portfolio_strategy_assignments", "strategy_run_events", "strategy_scorecard_policies", "strategy_scorecard_evaluations", "backtest_runs", "validation_suites"} {
			queries[table] = "SELECT * FROM " + table + " WHERE project_id=?"
		}
		for _, table := range []string{"backtest_market_bars", "backtest_simulations", "backtest_simulation_outputs", "backtest_agent_decisions", "backtest_events", "backtest_snapshots"} {
			queries[table] = "SELECT * FROM " + table + " WHERE " + "run_id" + " IN (SELECT id FROM backtest_runs WHERE project_id=?)"
		}
		queries["strategy_versions"] = "SELECT * FROM strategy_versions WHERE strategy_id IN (SELECT id FROM strategies WHERE project_id=?)"
		queries["strategy_rebalances"] = "SELECT * FROM strategy_rebalances WHERE assignment_id IN (SELECT id FROM portfolio_strategy_assignments WHERE project_id=?)"
		queries["validation_cases"] = "SELECT * FROM validation_cases WHERE suite_id IN (SELECT id FROM validation_suites WHERE project_id=?)"
	} else {
		queries["portfolios"] = "SELECT * FROM portfolios WHERE project_id=? AND id=?"
		for _, table := range []string{"positions", "orders", "fills", "execution_costs", "position_accounting", "portfolio_risk_policies", "portfolio_risk_state", "portfolio_universe_policies", "watchlist", "day_baselines"} {
			queries[table] = "SELECT * FROM " + table + " WHERE portfolio_id=?"
		}
		for _, table := range []string{"marks", "instruments", "venue_execution_profiles", "securities", "security_identifiers", "security_listings", "exchange_sessions", "universe_memberships"} {
			queries[table] = "SELECT * FROM " + table
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for table, query := range queries {
		var params []any
		if legacy {
			params = []any{pid}
		} else if table == "portfolios" {
			params = []any{pid, id}
		} else if strings.Contains(query, "portfolio_id=?") {
			params = []any{id}
		}
		rows, err := readResearchRows(tx, query, params...)
		if err != nil {
			return nil, err
		}
		snap.Tables[table] = rows
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return snap, nil
}

// Keep Trading's legacy portfolio REST paths explicit: research has moved.
func researchPortfolioPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) > 2 && parts[0] == "portfolios" && (parts[2] == "strategies" || parts[2] == "strategy" || parts[2] == "backtests" || strings.HasPrefix(parts[2], "scorecard"))
}

func (a *App) captureResearchBars(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := requireStrategiesCaller(call); err != nil {
		return nil, err
	}
	symbols, ok := stringSliceValue(args["symbols"])
	if !ok {
		return nil, fmt.Errorf("symbols required")
	}
	interval, err := normalizeBacktestInterval(strArg(args, "interval"))
	if err != nil {
		return nil, err
	}
	if latest, _ := args["latest"].(bool); latest {
		if len(symbols) != 1 {
			return nil, fmt.Errorf("latest capture requires one symbol")
		}
		bars, err := loadLiveStrategyBars(globalEngine.provider, symbols[0], interval, intArg(args, "limit", 100))
		if err != nil {
			return nil, err
		}
		return map[string]any{"bars": bars}, nil
	}
	start, err := time.Parse(time.RFC3339, strArg(args, "start_at"))
	if err != nil {
		return nil, err
	}
	end, err := time.Parse(time.RFC3339, strArg(args, "end_at"))
	if err != nil {
		return nil, err
	}
	bars, steps, source, err := captureBacktestMarketBarsAdjusted(call, symbols, interval, start, end, strArg(args, "adjustment_mode"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"bars": bars, "steps": steps, "source": source}, nil
}
