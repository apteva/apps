package main

import (
	"context"

	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) researchTools() []sdk.Tool {
	definitions := []sdk.Tool{
		{Name: "strategy_catalog", Description: "Discover allocation and generic trading-rule syntax, capabilities, workflow and complete examples. Call before composing a strategy; rule_presets include the three trade examples and simulation specifications.", InputSchema: schemaObject(map[string]any{"symbols": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, nil), Handler: a.toolStrategyCatalog},

		{Name: "portfolio_list", Description: "List portfolios visible in this project.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolPortfolioList},

		{Name: "portfolio_get", Description: "Snapshot of one portfolio (mandate, equity, cash, status, watchlist).",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: a.toolPortfolioGet},

		{Name: "account_summary", Description: "Equity, cash, buying power, day + open P&L for one portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: a.toolAccountSummary},

		{Name: "positions_list", Description: "Open positions for one portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"asset_class":  map[string]any{"type": "string"},
			}, []string{"portfolio_id"}),
			Handler: a.toolPositionsList},

		{Name: "orders_list", Description: "Working / filled / cancelled orders for one portfolio. Default status filter = working.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"status":       map[string]any{"type": "string"},
				"limit":        map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: a.toolOrdersList},

		{Name: "market_quote", Description: "Latest mark for a symbol — equity/crypto USD price or polymarket YES/NO probabilities.",
			InputSchema: schemaObject(map[string]any{
				"symbol": map[string]any{"type": "string"},
			}, []string{"symbol"}),
			Handler: a.toolMarketQuote},

		{Name: "market_history", Description: "OHLCV bars / probability history for a symbol. range: 1D | 5D | 1M | 3M | 1Y | ALL.",
			InputSchema: schemaObject(map[string]any{
				"symbol": map[string]any{"type": "string"},
				"range":  map[string]any{"type": "string"},
			}, []string{"symbol"}),
			Handler: a.toolMarketHistory},

		{Name: "market_source", Description: "Report the live data source per asset class — name (binance-public / polymarket-public / mock), last_ok_at, errors_60s, stale. Lets the agent self-report whether it's reasoning on real prices or the offline mock.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolMarketSource},

		{Name: "journal_read", Description: "Read recent journal entries for a portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"kind":         map[string]any{"type": "string"},
				"since":        map[string]any{"type": "string"},
				"limit":        map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: a.toolJournalRead},

		{Name: "strategy_create", Description: "Create a versioned strategy: allocation rules or engine=rules with a trading-rules/1 program. Call strategy_catalog for generic trade examples and syntax, and strategy_validate to check a definition before saving.",
			InputSchema: schemaObject(map[string]any{
				"name":                map[string]any{"type": "string"},
				"description":         map[string]any{"type": "string"},
				"status":              map[string]any{"type": "string"},
				"definition":          map[string]any{"type": []string{"object", "string"}, "description": "For trade rules: engine=rules, universe=[program.symbol], cadence=program.timeframe, program={version:trading-rules/1,symbol,timeframe,timezone,rules,...}. Complete examples and authoring guidance are returned by strategy_catalog."},
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

		{Name: "strategy_evaluate", Description: "Evaluate an allocation strategy against current market data and return target allocations. For engine=rules historical performance use completed strategy_backtest_create runs, strategy_scorecard_evaluate and validation suites.",
			InputSchema: schemaObject(map[string]any{
				"strategy_id": map[string]any{"type": "integer"},
			}, []string{"strategy_id"}),
			Handler: a.toolStrategyEvaluate},

		{Name: "strategy_assign", Description: "Assign a saved strategy to a portfolio for strategy or hybrid control.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"strategy_id":  map[string]any{"type": "integer"},
				"control_mode": map[string]any{"type": "string"},
				"cadence":      map[string]any{"type": "string"},
			}, []string{"portfolio_id", "strategy_id"}),
			Handler: a.toolStrategyAssign},

		{Name: "market_data_import", Description: "Normalize supplied historical CSV exports into sourced inputs for rule backtests. Supports any symbol, observed bid/ask quotes and closed OHLC bars, explicit column maps, timestamp units/layouts/timezones, and price basis. Reports gaps and byte hashes; never fabricates prices. No download or broker connection. Use returned inputs with strategy_backtest_create; contract/fee/FX configuration remains explicit.", InputSchema: schemaObject(map[string]any{"symbol": map[string]any{"type": "string"}, "streams": map[string]any{"type": "array", "items": map[string]any{"type": "object", "description": "kind=quotes|bars, csv with header, source; optional columns field-to-header map, delimiter, timestamp_format (RFC3339, unix_s/ms/us or Go layout), timezone. Bars require timeframe and price_basis=bid|ask|mid|last; timestamp is candle start. Quotes require timestamp,bid,ask; bars timestamp,open,high,low,close; optional volume."}}}, []string{"symbol", "streams"}), Handler: a.toolMarketDataImport},

		{Name: "strategy_backtest_create", Description: "Create a deterministic event-driven strategy backtest. engine=rules requires a complete sourced OHLC/quote tape; allocation strategies can add feature events to fetched history.",
			InputSchema: schemaObject(map[string]any{
				"simulation":      map[string]any{"type": "object", "description": "Execution configuration: seed, latency, fill limits, benchmark_symbol, costs, symbol_costs and explicit contracts (multiplier, currency_rate, margin_fraction)."},
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

func (a *App) toolPortfolioList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	pfs, err := dbListPortfolios(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(pfs))
	for _, p := range pfs {
		snap, _ := snapshotPortfolio(ctx.AppDB(), p)
		out = append(out, map[string]any{
			"id": snap.ID, "name": snap.Name, "mandate": snap.Mandate,
			"allowed_classes": snap.AllowedClasses, "status": snap.Status,
			"mode": snap.Mode, "execution_environment": normalizeExecutionEnvironment(snap.ExecutionEnvironment, snap.Mode, snap.BrokerSlug),
			"live_armed": snap.LiveArmed, "broker_slug": snap.BrokerSlug,
			"equity": snap.Equity, "cash": snap.Cash,
			"day_pnl": snap.DayPnL, "day_pnl_pct": snap.DayPnLPct,
			"open_pnl": snap.OpenPnL, "open_pnl_pct": snap.OpenPnLPct,
			"realized_pnl": snap.RealizedPnL, "fees_paid": snap.FeesPaid, "funding_paid": snap.FundingPaid,
			"total_pnl": snap.TotalPnL, "total_pnl_pct": snap.TotalPnLPct,
		})
	}
	return map[string]any{"portfolios": out}, nil
}

func (a *App) toolPortfolioGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	if id == 0 {
		return nil, errors.New("portfolio_id required")
	}
	p, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("portfolio %d not found in project", id)
	}
	snap, _ := snapshotPortfolio(ctx.AppDB(), p)
	return map[string]any{"portfolio": snap}, nil
}

func (a *App) toolAccountSummary(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	p, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("portfolio %d not found", id)
	}
	snap, _ := snapshotPortfolio(ctx.AppDB(), p)
	return map[string]any{
		"equity": snap.Equity, "cash": snap.Cash, "buying_power": snap.BuyingPower,
		"day_pnl": snap.DayPnL, "day_pnl_pct": snap.DayPnLPct,
		"open_pnl": snap.OpenPnL, "open_pnl_pct": snap.OpenPnLPct,
		"realized_pnl": snap.RealizedPnL, "fees_paid": snap.FeesPaid, "funding_paid": snap.FundingPaid,
		"total_pnl": snap.TotalPnL, "total_pnl_pct": snap.TotalPnLPct,
		"status": snap.Status, "mode": snap.Mode,
	}, nil
}

func (a *App) toolPositionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	p, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("portfolio %d not found", id)
	}
	snap, _ := snapshotPortfolio(ctx.AppDB(), p)
	pos, _ := dbListPositions(ctx.AppDB(), id)
	classFilter, _ := args["asset_class"].(string)
	out := make([]*Position, 0, len(pos))
	for _, q := range pos {

		mark, _ := dbGetMark(ctx.AppDB(), q.Symbol)
		if mark != nil {
			q.MarketPrice = markPriceForSide(mark, q.Outcome)
		} else {
			q.MarketPrice = q.AvgCost
		}
		q.MarketValue = q.MarketPrice * q.Qty
		q.UnrealizedPnL = (q.MarketPrice - q.AvgCost) * q.Qty
		if q.AvgCost > 0 && q.Qty > 0 {
			q.UnrealizedPnLPct = (q.MarketPrice/q.AvgCost - 1) * 100
		}
		if snap.Equity > 0 {
			q.WeightPct = q.MarketValue / snap.Equity * 100
		}
		if classFilter != "" && q.AssetClass != classFilter {
			continue
		}
		out = append(out, q)
	}
	return map[string]any{"positions": out}, nil
}

func (a *App) toolOrdersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	if _, err := dbGetPortfolio(ctx.AppDB(), pid, id); err != nil {
		return nil, fmt.Errorf("portfolio %d not found", id)
	}
	status, _ := args["status"].(string)
	if status == "" {
		status = "working"
	}
	limit := intArg(args, "limit", 50)
	rows, err := dbListOrders(ctx.AppDB(), id, status, limit)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []*Order{}
	}
	return map[string]any{"orders": rows, "count": len(rows)}, nil
}

func (a *App) toolMarketQuote(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	symbol := canonicalSymbol(strArg(args, "symbol"))
	if symbol == "" {
		return nil, errors.New("symbol required")
	}
	mark, err := dbGetMark(ctx.AppDB(), symbol)
	if err != nil {

		if globalEngine != nil {
			mark, err = globalEngine.provider.Quote(symbol)
			if err == nil && mark != nil {
				err = dbUpsertMark(ctx.AppDB(), mark)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	out := map[string]any{
		"symbol": mark.Symbol, "asset_class": mark.AssetClass,
		"marked_at": mark.MarkedAt, "received_at": mark.ReceivedAt,
		"timestamp_kind": mark.TimestampKind, "source": mark.Source,
	}
	if mark.Instrument != nil {
		out["instrument"] = mark.Instrument
	}
	if mark.AssetClass == "polymarket" {
		out["yes_price"] = mark.Price
		if mark.NoPrice != nil {
			out["no_price"] = *mark.NoPrice
		}
	} else {
		out["price"] = mark.Price
		if mark.PrevClose != nil {
			out["prev_close"] = *mark.PrevClose
			out["change_pct_24h"] = (mark.Price/(*mark.PrevClose) - 1) * 100
		}
	}
	if mark.Volume24h != nil {
		out["volume_24h"] = *mark.Volume24h
		out["volume_unit"] = mark.VolumeUnit
	}
	return out, nil
}

func (a *App) toolMarketHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if globalEngine == nil {
		return nil, errors.New("engine not ready")
	}
	symbol := canonicalSymbol(strArg(args, "symbol"))
	rng, _ := args["range"].(string)
	if rng == "" {
		rng = "1D"
	}
	bars, err := globalEngine.provider.Bars(symbol, rng)
	if err != nil {
		return nil, err
	}
	instrument, _ := dbGetInstrument(ctx.AppDB(), symbol)
	source := "mock"
	if instrument != nil && instrument.Source != "" {
		source = instrument.Source
	}
	bars, err = normalizeBars(symbol, source, bars)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"symbol": symbol, "range": rng, "bars": bars, "source": source,
		"timestamp_format": "unix_seconds_utc", "adjustment_mode": "provider_adjusted",
	}
	if source == alpacaMarketDataSlug {
		out["adjustment_mode"] = "total_return"
	}
	if instrument != nil {
		out["instrument"] = instrument
		out["volume_unit"] = instrument.VolumeUnit
	}
	return out, nil
}

func (a *App) toolMarketSource(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out := map[string]any{
		"providers": providerHealthSnapshot(),
	}
	if globalEngine != nil {
		metrics := globalEngine.snapshotMetrics()
		out["last_tick_at"] = metrics["last_tick_at"]
		out["ticks"] = metrics["ticks"]
	}
	return out, nil
}

func (a *App) toolJournalRead(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	if _, err := dbGetPortfolio(ctx.AppDB(), pid, id); err != nil {
		return nil, fmt.Errorf("portfolio %d not found", id)
	}
	kind, _ := args["kind"].(string)
	since, _ := args["since"].(string)
	limit := intArg(args, "limit", 50)
	entries, err := dbReadJournal(ctx.AppDB(), id, kind, since, limit)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []*JournalEntry{}
	}
	return map[string]any{"entries": entries}, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
