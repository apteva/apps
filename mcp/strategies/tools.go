package main

import (
	"context"

	"errors"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) researchTools() []sdk.Tool {
	definitions := []sdk.Tool{
		{Name: "strategy_catalog", Description: "Discover the rule engine: syntax, capabilities, live limits, workflow and complete programs. presets cover allocation and research books (trend rotation, decile spread, TSMOM, factor momentum, news sentiment, pairs) with the simulation they assume; rule_presets are single-instrument trade examples. Call before composing a strategy.", InputSchema: schemaObject(map[string]any{"symbols": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, nil), Handler: a.toolStrategyCatalog},

		{Name: "portfolio_list", Description: "List portfolios visible in this project.",
			InputSchema: schemaObject(nil, nil),
			Handler:     nil},

		{Name: "portfolio_get", Description: "Snapshot of one portfolio (mandate, equity, cash, status, watchlist).",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: nil},

		{Name: "account_summary", Description: "Equity, cash, buying power, day + open P&L for one portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: nil},

		{Name: "positions_list", Description: "Open positions for one portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"asset_class":  map[string]any{"type": "string"},
			}, []string{"portfolio_id"}),
			Handler: nil},

		{Name: "orders_list", Description: "Working / filled / cancelled orders for one portfolio. Default status filter = working.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"status":       map[string]any{"type": "string"},
				"limit":        map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: nil},

		{Name: "market_quote", Description: "Latest mark for a symbol — equity/crypto USD price or polymarket YES/NO probabilities.",
			InputSchema: schemaObject(map[string]any{
				"symbol": map[string]any{"type": "string"},
			}, []string{"symbol"}),
			Handler: nil},

		{Name: "market_history", Description: "OHLCV bars / probability history for a symbol. range: 1D | 5D | 1M | 3M | 1Y | ALL.",
			InputSchema: schemaObject(map[string]any{
				"symbol": map[string]any{"type": "string"},
				"range":  map[string]any{"type": "string"},
			}, []string{"symbol"}),
			Handler: nil},

		{Name: "market_source", Description: "Report the live data source per asset class — name (binance-public / polymarket-public / mock), last_ok_at, errors_60s, stale. Lets the agent self-report whether it's reasoning on real prices or the offline mock.",
			InputSchema: schemaObject(nil, nil),
			Handler:     nil},

		{Name: "journal_read", Description: "Read recent journal entries for a portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"kind":         map[string]any{"type": "string"},
				"since":        map[string]any{"type": "string"},
				"limit":        map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: nil},

		{Name: "strategy_create", Description: "Create a versioned strategy from a trading-rules/1 program. The same program backtests and runs live. Call strategy_catalog for syntax and complete programs, and strategy_validate to check a definition before saving.",
			InputSchema: schemaObject(map[string]any{
				"name":                map[string]any{"type": "string"},
				"description":         map[string]any{"type": "string"},
				"status":              map[string]any{"type": "string"},
				"definition":          map[string]any{"type": []string{"object", "string"}, "description": "{program:{version:trading-rules/1, symbols:[1-500], timeframe, timezone, rules, ...}}. Complete examples and authoring guidance are returned by strategy_catalog."},
				"created_by_agent_id": map[string]any{"type": "integer"},
			}, []string{"name", "definition"}),
			Handler: a.toolStrategyCreate},

		{Name: "strategy_update", Description: "Update a deterministic strategy. Supplying definition creates a new strategy version.",
			InputSchema: schemaObject(map[string]any{
				"strategy_id": map[string]any{"type": "integer"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"status":      map[string]any{"type": "string"},
				"definition":  map[string]any{"type": []string{"object", "string"}, "description": "Complete definition object or JSON object text; both use the same strict program validation."},
			}, []string{"strategy_id"}),
			Handler: a.toolStrategyUpdate},

		{Name: "strategy_get", Description: "Fetch one saved deterministic strategy.",
			InputSchema: schemaObject(map[string]any{
				"strategy_id": map[string]any{"type": "integer"},
			}, []string{"strategy_id"}),
			Handler: a.toolStrategyGet},

		{Name: "strategy_list", Description: "List saved deterministic strategies in this project.",
			InputSchema: schemaObject(map[string]any{
				"status": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolStrategyList},

		{Name: "strategy_validate", Description: "Validate a deterministic strategy definition without saving it.",
			InputSchema: schemaObject(map[string]any{
				"definition": map[string]any{"type": []string{"object", "string"}, "description": "Complete definition object or JSON object text; both use the same strict program validation."},
			}, []string{"definition"}),
			Handler: a.toolStrategyValidate},

		{Name: "strategy_evaluate", Description: "Preview what a rule program would do on the latest closed bar: target weights and orders, against a portfolio's current cash and positions (portfolio_id) or a $100k cash account. Judge historical performance with strategy_backtest_create, strategy_scorecard_evaluate and validation suites.",
			InputSchema: schemaObject(map[string]any{
				"strategy_id": map[string]any{"type": "integer"},
			}, []string{"strategy_id"}),
			Handler: a.toolStrategyEvaluate},

		{Name: "strategy_assign", Description: "Assign a saved strategy to a portfolio for strategy or hybrid control. Live runs evaluate each closed bar of the program timeframe; programs that short, use protective stops, limit entries, features or non-bar triggers stay backtest-only.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"strategy_id":  map[string]any{"type": "integer"},
				"control_mode": map[string]any{"type": "string"},
				"cadence":      map[string]any{"type": "string"},
			}, []string{"portfolio_id", "strategy_id"}),
			Handler: a.toolStrategyAssign},

		{Name: "market_data_import", Description: "Normalize supplied historical CSV exports into sourced inputs for rule backtests. Supports any symbol, observed bid/ask quotes and closed OHLC bars, explicit column maps, timestamp units/layouts/timezones, and price basis. Reports gaps and byte hashes; never fabricates prices. No download or broker connection. Use returned inputs with strategy_backtest_create; contract/fee/FX configuration remains explicit.", InputSchema: schemaObject(map[string]any{"symbol": map[string]any{"type": "string"}, "streams": map[string]any{"type": "array", "items": map[string]any{"type": "object", "description": "kind=quotes|bars, csv with header, source; optional columns field-to-header map, delimiter, timestamp_format (RFC3339, unix_s/ms/us or Go layout), timezone. Bars require timeframe and price_basis=bid|ask|mid|last; timestamp is candle start. Quotes require timestamp,bid,ask; bars timestamp,open,high,low,close; optional volume."}}}, []string{"symbol", "streams"}), Handler: a.toolMarketDataImport},

		{Name: "strategy_backtest_create", Description: "Create a deterministic event-driven strategy backtest. Either pass start_at/end_at to capture history for the program symbols at the program timeframe (optionally plus feature events in inputs), or pass inputs as a complete sourced OHLC/quote tape. Set simulation.shorts to allow short positions.",
			InputSchema: schemaObject(map[string]any{
				"simulation":      map[string]any{"type": "object", "description": "Execution configuration: seed, latency, fill limits, benchmark_symbol, costs, symbol_costs, explicit contracts (multiplier, currency_rate, margin_fraction) and shorts {enabled, initial_margin, maintenance_margin, borrow_bps_annual, symbol_borrow_bps, unshortable}."},
				"inputs":          map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "Events with id, type, symbol, source, event_time, available_at and data. Rule strategies require complete market.bar.close OHLC candles (metadata.timeframe) and executable market.quote events. Sentiment uses feature.sentiment with data.score."},
				"portfolio_id":    map[string]any{"type": "integer"},
				"strategy_id":     map[string]any{"type": "integer"},
				"name":            map[string]any{"type": "string"},
				"start_at":        map[string]any{"type": "string"},
				"end_at":          map[string]any{"type": "string"},
				"interval":        map[string]any{"type": "string"},
				"starting_cash":   map[string]any{"type": "number"},
				"fee_bps":         map[string]any{"type": "number"},
				"slippage_bps":    map[string]any{"type": "number"},
				"adjustment_mode": map[string]any{"type": "string", "enum": []string{"provider_adjusted", "raw", "split_adjusted", "price_return", "total_return"}},
			}, []string{"portfolio_id", "strategy_id"}),
			Handler: a.toolStrategyBacktestCreate},

		{Name: "backtest_control", Description: "Read, run, step, pause or cancel an event simulation. Running returns immediately; progress is persisted and streamed.", InputSchema: schemaObject(map[string]any{"backtest_id": map[string]any{"type": "integer"}, "action": map[string]any{"type": "string", "enum": []string{"status", "run", "step", "pause", "cancel"}}}, []string{"backtest_id", "action"}), Handler: a.toolBacktestControl},
		{Name: "backtest_artifact", Description: "Export a portable reproducible result bundle by backtest_id, or import an artifact into portfolio_id as a fresh queued replay.", InputSchema: schemaObject(map[string]any{"backtest_id": map[string]any{"type": "integer"}, "portfolio_id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"}, "artifact": map[string]any{"type": "object"}}, nil), Handler: a.toolBacktestArtifact},
		{Name: "strategy_validate_backtest", Description: "Run fixed-parameter strategy validation with in-sample and out-of-sample backtests.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id":    map[string]any{"type": "integer"},
				"strategy_id":     map[string]any{"type": "integer"},
				"name":            map[string]any{"type": "string"},
				"start_at":        map[string]any{"type": "string"},
				"end_at":          map[string]any{"type": "string"},
				"interval":        map[string]any{"type": "string"},
				"split_pct":       map[string]any{"type": "number"},
				"starting_cash":   map[string]any{"type": "number"},
				"fee_bps":         map[string]any{"type": "number"},
				"slippage_bps":    map[string]any{"type": "number"},
				"adjustment_mode": map[string]any{"type": "string", "enum": []string{"provider_adjusted", "raw", "split_adjusted", "price_return", "total_return"}},
			}, []string{"portfolio_id", "strategy_id"}),
			Handler: a.toolStrategyValidateBacktest},

		{Name: "strategy_scorecard_get", Description: "Get a portfolio strategy's scorecard policy, promotion stage, and durable evaluation history.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"}, "strategy_id": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"},
			}, []string{"portfolio_id", "strategy_id"}), Handler: a.toolStrategyScorecardGet},

		{Name: "strategy_scorecard_update", Description: "Configure generic metric thresholds and enforcement for a portfolio strategy scorecard.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"}, "strategy_id": map[string]any{"type": "integer"},
				"criteria":           map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"min_completed_runs": map[string]any{"type": "integer"}, "require_out_of_sample": map[string]any{"type": "boolean"},
				"enforcement_enabled": map[string]any{"type": "boolean"},
			}, []string{"portfolio_id", "strategy_id"}), Handler: a.toolStrategyScorecardUpdate},

		{Name: "strategy_scorecard_evaluate", Description: "Evaluate and durably snapshot a completed strategy backtest against its current scorecard policy.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"}, "strategy_id": map[string]any{"type": "integer"}, "backtest_run_id": map[string]any{"type": "integer"},
			}, []string{"portfolio_id", "strategy_id", "backtest_run_id"}), Handler: a.toolStrategyScorecardEvaluate},

		{Name: "strategy_promotion_update", Description: "Promote one scorecard stage at a time after passing the required distinct runs, demote it, or suspend it.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"}, "strategy_id": map[string]any{"type": "integer"},
				"promotion_stage": map[string]any{"type": "string", "enum": []string{"research", "paper_candidate", "paper", "live_candidate", "live", "suspended"}},
			}, []string{"portfolio_id", "strategy_id", "promotion_stage"}), Handler: a.toolStrategyPromotionUpdate},

		{Name: "agent_backtest_create", Description: "Create an agent simulation from a complete timestamped event tape (quotes, sentiment, news and custom events). Uses the shared event engine; returns queued without running the model.", InputSchema: schemaObject(map[string]any{"portfolio_id": map[string]any{"type": "integer"}, "agent_id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"}, "starting_cash": map[string]any{"type": "number"}, "symbols": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "inputs": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, "agent_simulation": map[string]any{"type": "object", "description": "directive, trigger_types (optional wildcard suffix), max_decisions, timeout_seconds, history_limit"}, "simulation": map[string]any{"type": "object"}}, []string{"portfolio_id", "inputs", "agent_simulation"}), Handler: a.toolAgentBacktestCreate},

		{Name: "backtest_observation", Description: "Read the current event replay observation, simulated portfolio, past events, and agent memory.", InputSchema: schemaObject(nil, nil), Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			return nil, errors.New("active agent replay required")
		}},
		{Name: "backtest_events", Description: "Read only events already observed in this replay turn, optionally filtered by symbol.", InputSchema: schemaObject(map[string]any{"symbol": map[string]any{"type": "string"}}, nil), Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			return nil, errors.New("active agent replay required")
		}},
		{Name: "backtest_decision_finish", Description: "Seal this replay decision and checkpoint memory for the next event. Always call this even when making no trades.", InputSchema: schemaObject(map[string]any{"decision_id": map[string]any{"type": "string"}, "rationale": map[string]any{"type": "string"}, "memory": map[string]any{"type": "string"}}, []string{"decision_id", "rationale", "memory"}), Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			return nil, errors.New("active agent replay required")
		}},
		{Name: "backtest_agent_exchange", Exposure: sdk.ToolExposureAppOnly, Description: "Internal event replay mailbox.", InputSchema: schemaObject(map[string]any{"operation": map[string]any{"type": "string"}, "turn": map[string]any{"type": "object"}, "decision_id": map[string]any{"type": "string"}}, []string{"operation"}), HandlerCtx: func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			caller := sdk.CallerFrom(call)
			if caller == nil || caller.AgentID != 0 || caller.AppName != "strategies" || caller.AppInstallID == 0 {
				return nil, errors.New("authenticated trading runner required")
			}
			return a.toolAgentExchange(ctx, args)
		}},
	}
	definitions = append(definitions, a.validationTools()...)
	for i := range definitions {
		original := definitions[i]
		name := original.Name
		if name == "backtest_agent_exchange" {
			continue
		}
		definitions[i].Handler = nil
		definitions[i].HandlerCtx = func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			cloned := map[string]any{}
			for k, v := range args {
				cloned[k] = v
			}
			if caller := sdk.CallerFrom(call); caller != nil {
				if caller.ProjectID != "" {
					cloned["_project_id"] = caller.ProjectID
				}
				if strArg(cloned, "idempotency_key") == "" && caller.ToolCallID != "" {
					cloned["idempotency_key"] = "mcp:" + caller.ToolCallID
				}
			}
			if out, handled, err := replayAgentTool(ctx, name, cloned); handled {
				return out, err
			}
			if original.HandlerCtx != nil {
				return original.HandlerCtx(call, ctx, args)
			}
			return original.Handler(ctx, args)
		}
	}
	return definitions

}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
