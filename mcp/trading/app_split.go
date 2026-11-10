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
	out := a.paperDeskTools()
	for _, tool := range a.executionTools() {
		if isResearchTool(tool.Name) && tool.Name != "backtest_market_step" {
			continue
		}
		out = append(out, tool)
	}
	out = append(out,
		sdk.Tool{Name: "portfolio_research_snapshot", Exposure: sdk.ToolExposureAppOnly, Description: "Read a bounded portfolio observation. Use paged=true and continue with the returned revision and next page number.", InputSchema: schemaObject(map[string]any{
			"portfolio_id":  map[string]any{"type": "integer"},
			"symbols":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"session_start": map[string]any{"type": "string"}, "session_end": map[string]any{"type": "string"},
			"paged": map[string]any{"type": "boolean"}, "revision": map[string]any{"type": "string"}, "page": map[string]any{"type": "integer"},
		}, []string{"portfolio_id"}), HandlerCtx: func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			if err := requireStrategiesCaller(call); err != nil {
				return nil, err
			}
			if c := sdk.CallerFrom(call); c.ProjectID != "" {
				args["_project_id"] = c.ProjectID
			}
			return exportPortfolioObservation(call, ctx, args, sdk.CallerFrom(call).AppInstallID)
		}},
		sdk.Tool{Name: "research_legacy_export", Exposure: sdk.ToolExposureAppOnly, Description: "Export preserved legacy strategy and research data; never resume execution.", InputSchema: schemaObject(map[string]any{"phase": map[string]any{"type": "string"}, "table": map[string]any{"type": "string"}, "after_row_id": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"}}, nil), HandlerCtx: func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			if err := requireStrategiesCaller(call); err != nil {
				return nil, err
			}
			if c := sdk.CallerFrom(call); c.ProjectID != "" {
				args["_project_id"] = c.ProjectID
			}
			return exportLegacyResearchSnapshot(ctx, args)
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

func exportLegacyResearchSnapshot(ctx *sdk.AppCtx, args map[string]any) (*researchSnapshot, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	installID, _ := strconv.ParseInt(os.Getenv("APTEVA_INSTALL_ID"), 10, 64)
	snap := &researchSnapshot{Version: 1, SourceInstallID: installID, ProjectID: pid, Tables: map[string][]map[string]any{}}
	queries := map[string]string{}
	for _, table := range []string{"strategies", "portfolio_strategy_assignments", "strategy_run_events", "strategy_scorecard_policies", "strategy_scorecard_evaluations", "backtest_runs", "validation_suites"} {
		queries[table] = "SELECT * FROM " + table + " WHERE project_id=?"
	}
	for _, table := range []string{"backtest_market_bars", "backtest_simulations", "backtest_simulation_outputs", "backtest_agent_decisions", "backtest_events", "backtest_snapshots"} {
		queries[table] = "SELECT * FROM " + table + " WHERE run_id IN (SELECT id FROM backtest_runs WHERE project_id=?)"
	}
	queries["strategy_versions"] = "SELECT * FROM strategy_versions WHERE strategy_id IN (SELECT id FROM strategies WHERE project_id=?)"
	queries["strategy_rebalances"] = "SELECT * FROM strategy_rebalances WHERE assignment_id IN (SELECT id FROM portfolio_strategy_assignments WHERE project_id=?)"
	queries["validation_cases"] = "SELECT * FROM validation_cases WHERE suite_id IN (SELECT id FROM validation_suites WHERE project_id=?)"
	if strArg(args, "phase") != "" {
		return exportLegacyPage(ctx, pid, installID, queries, args)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for table, query := range queries {
		rows, err := readResearchRows(tx, query, pid)
		if err != nil {
			return nil, err
		}
		snap.Tables[table] = rows
	}
	snap.LegacySecurities, err = legacyBarSecurities(tx, snap.Tables["backtest_market_bars"])
	if err != nil {
		return nil, err
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
