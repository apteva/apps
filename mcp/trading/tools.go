package main

// MCP tool surface. Every tool listed in apteva.yaml has a handler
// here; manifest_test.go enforces parity. Handlers do argument parsing,
// project resolution, and call into store.go — no business logic
// inline beyond the pre-trade checks in toolOrderPlace.

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

var orderPlacementMu sync.Mutex

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		// ─── Lifecycle ────────────────────────────────────────────
		{Name: "portfolio_create", Description: "Create a portfolio. execution_environment is simulation, broker_paper, or broker_live. Legacy mode=paper|live remains accepted. Broker portfolios pull balances from the bound broker.",
			InputSchema: schemaObject(map[string]any{
				"name":                  map[string]any{"type": "string"},
				"mandate":               map[string]any{"type": "string"},
				"allowed_classes":       map[string]any{"type": "array"},
				"starting_cash":         map[string]any{"type": "number"},
				"mode":                  map[string]any{"type": "string", "enum": []string{"paper", "live"}},
				"execution_environment": map[string]any{"type": "string", "enum": []string{"simulation", "broker_paper", "broker_live"}},
				"broker_slug":           map[string]any{"type": "string"},
				"fee_bps":               map[string]any{"type": "number"},
				"slippage_bps":          map[string]any{"type": "number"},
			}, []string{"name"}),
			Handler: a.toolPortfolioCreate},

		{Name: "brokers_list", Description: "List broker adapters registered in this build and their currently-bound connections. Use before portfolio_create with mode=live to pick a broker_slug.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolBrokersList},

		{Name: "venue_profiles_list", Description: "List effective generic execution profiles: fees, spread/slippage, sessions, funding, minimums, precision, and venue health.",
			InputSchema: schemaObject(map[string]any{"venue_slug": map[string]any{"type": "string"}, "asset_class": map[string]any{"type": "string"}}, nil),
			Handler:     a.toolVenueProfilesList},

		{Name: "venue_profile_update", Description: "Create or update a venue-wide or symbol-specific execution profile used by paper and live pre-trade checks.",
			InputSchema: schemaObject(map[string]any{
				"venue_slug": map[string]any{"type": "string"}, "asset_class": map[string]any{"type": "string"}, "symbol": map[string]any{"type": "string"},
				"status":   map[string]any{"type": "string", "enum": []string{"active", "degraded", "maintenance", "outage"}},
				"calendar": map[string]any{"type": "string"}, "session_policy": map[string]any{"type": "string", "enum": []string{"continuous", "regular_only", "venue_managed"}},
				"maker_fee_bps": map[string]any{"type": "number"}, "taker_fee_bps": map[string]any{"type": "number"}, "fee_currency": map[string]any{"type": "string"},
				"spread_model": map[string]any{"type": "string", "enum": []string{"quote", "fixed_bps", "none"}}, "fallback_spread_bps": map[string]any{"type": "number"},
				"slippage_model": map[string]any{"type": "string", "enum": []string{"fixed_bps", "none"}}, "slippage_bps": map[string]any{"type": "number"},
				"min_qty": map[string]any{"type": "number"}, "min_notional": map[string]any{"type": "number"}, "qty_step": map[string]any{"type": "number"}, "price_tick": map[string]any{"type": "number"},
				"funding_rate_bps": map[string]any{"type": "number"}, "funding_interval_hours": map[string]any{"type": "integer"},
				"supports_post_only": map[string]any{"type": "boolean"}, "supports_reduce_only": map[string]any{"type": "boolean"},
			}, []string{"venue_slug", "asset_class"}), Handler: a.toolVenueProfileUpdate},

		// ─── Reads ────────────────────────────────────────────────
		{Name: "portfolio_list", Description: "List portfolios visible in this project.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolPortfolioList},

		{Name: "portfolio_get", Description: "Snapshot of one portfolio (mandate, equity, cash, status, watchlist).",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: a.toolPortfolioGet},

		{Name: "risk_profiles_list", Description: "List Trading's conservative, balanced, and aggressive risk presets with their enforced percentage limits.",
			InputSchema: schemaObject(nil, nil), Handler: a.toolRiskProfilesList},

		{Name: "portfolio_risk_get", Description: "Get one portfolio's resolved risk policy and current high-water/drawdown state.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}), Handler: a.toolPortfolioRiskGet},

		{Name: "portfolio_risk_update", Description: "Set an enforceable risk preset or custom percentage limits for a portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id":           map[string]any{"type": "integer"},
				"risk_level":             map[string]any{"type": "string", "enum": []string{"conservative", "balanced", "aggressive", "custom"}},
				"max_daily_loss_pct":     map[string]any{"type": "number"},
				"max_drawdown_pct":       map[string]any{"type": "number"},
				"max_position_pct":       map[string]any{"type": "number"},
				"max_gross_exposure_pct": map[string]any{"type": "number"},
				"max_order_pct":          map[string]any{"type": "number"},
			}, []string{"portfolio_id", "risk_level"}), Handler: a.toolPortfolioRiskUpdate},

		{Name: "portfolio_universe_get", Description: "Get the portfolio's enforced tradable universe policy and allowed asset classes.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}), Handler: a.toolPortfolioUniverseGet},

		{Name: "portfolio_universe_update", Description: "Set a hard portfolio universe using all allowed classes, a symbol allowlist, or a dated reference universe.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id":           map[string]any{"type": "integer"},
				"selection_mode":         map[string]any{"type": "string", "enum": []string{"all_allowed_classes", "symbol_allowlist", "reference_universe"}},
				"include_symbols":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"exclude_symbols":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"reference_universe_id":  map[string]any{"type": "string"},
				"require_active_listing": map[string]any{"type": "boolean"},
				"enforcement_enabled":    map[string]any{"type": "boolean"},
			}, []string{"portfolio_id"}), Handler: a.toolPortfolioUniverseUpdate},

		{Name: "portfolio_objective_create", Description: "Create a native Trading percentage objective for portfolio, day, period, or drawdown performance.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"},
				"metric":     map[string]any{"type": "string", "enum": []string{"period_return_pct", "total_return_pct", "day_return_pct", "drawdown_pct"}},
				"target_pct": map[string]any{"type": "number"}, "direction": map[string]any{"type": "string", "enum": []string{"at_least", "at_most"}},
				"starts_at": map[string]any{"type": "string"}, "deadline_at": map[string]any{"type": "string"},
			}, []string{"portfolio_id", "name", "metric", "target_pct"}), Handler: a.toolPortfolioObjectiveCreate},

		{Name: "portfolio_objectives_list", Description: "List a portfolio's Trading objectives with live actual and progress percentages.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"}, "include_archived": map[string]any{"type": "boolean"},
			}, []string{"portfolio_id"}), Handler: a.toolPortfolioObjectivesList},

		{Name: "portfolio_objective_update", Description: "Update, pause, achieve, expire, or archive a Trading objective.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"}, "objective_id": map[string]any{"type": "integer"},
				"name": map[string]any{"type": "string"}, "metric": map[string]any{"type": "string"}, "target_pct": map[string]any{"type": "number"},
				"direction": map[string]any{"type": "string"}, "starts_at": map[string]any{"type": "string"}, "deadline_at": map[string]any{"type": "string"},
				"status": map[string]any{"type": "string"},
			}, []string{"portfolio_id", "objective_id"}), Handler: a.toolPortfolioObjectiveUpdate},

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

		{Name: "market_calendar", Description: "Return normalized exchange-calendar state for a symbol at an optional RFC3339 timestamp.",
			InputSchema: schemaObject(map[string]any{
				"symbol": map[string]any{"type": "string"},
				"at":     map[string]any{"type": "string"},
			}, []string{"symbol"}),
			Handler: a.toolMarketCalendar},

		{Name: "reference_data_status", Description: "Report security-master, corporate-action, exchange-session, point-in-time universe, checkpoint, and quality coverage.",
			InputSchema: schemaObject(nil, nil), Handler: a.toolReferenceDataStatus},

		{Name: "security_resolve", Description: "Resolve a symbol or name to canonical security and listing identities at an optional point in time.",
			InputSchema: schemaObject(map[string]any{
				"query": map[string]any{"type": "string"}, "as_of": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
			}, []string{"query"}), Handler: a.toolSecurityResolve},

		{Name: "corporate_actions_list", Description: "List normalized, latest-revision corporate actions with optional symbol, type, and date filters.",
			InputSchema: schemaObject(map[string]any{
				"symbol": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"}, "since": map[string]any{"type": "string"}, "until": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
			}, nil), Handler: a.toolCorporateActionsList},

		{Name: "exchange_sessions_list", Description: "List authoritative normalized exchange sessions for a venue and date range.",
			InputSchema: schemaObject(map[string]any{
				"venue": map[string]any{"type": "string"}, "start": map[string]any{"type": "string"}, "end": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
			}, nil), Handler: a.toolExchangeSessionsList},

		{Name: "journal_read", Description: "Read recent journal entries for a portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"kind":         map[string]any{"type": "string"},
				"since":        map[string]any{"type": "string"},
				"limit":        map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: a.toolJournalRead},

		{Name: "execution_costs_list", Description: "List attributed fee, spread, slippage, funding, and rebate entries for a portfolio.",
			InputSchema: schemaObject(map[string]any{"portfolio_id": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"}}, []string{"portfolio_id"}),
			Handler:     a.toolExecutionCostsList},

		// ─── Writes ───────────────────────────────────────────────
		{Name: "order_place", Description: "Place a paper order. Required rationale ≥ 30 chars; subject to mandate + size cap + daily-loss halt.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"symbol":       map[string]any{"type": "string"},
				"side":         map[string]any{"type": "string", "enum": []string{"buy", "sell", "yes", "no"}},
				"outcome":      map[string]any{"type": "string", "enum": []string{"yes", "no"}, "description": "Polymarket outcome. Required when side=sell; inferred for side=yes|no."},
				"type":         map[string]any{"type": "string", "enum": []string{"market", "limit", "stop"}},
				"qty":          map[string]any{"type": "number"},
				"limit_price":  map[string]any{"type": "number"},
				"stop_price":   map[string]any{"type": "number"},
				"tif":          map[string]any{"type": "string"},
				"rationale":    map[string]any{"type": "string"},
			}, []string{"portfolio_id", "symbol", "side", "type", "qty", "rationale"}),
			Handler: a.toolOrderPlace},

		{Name: "order_cancel", Description: "Cancel a working order. No-op on already-resolved orders.",
			InputSchema: schemaObject(map[string]any{
				"order_id": map[string]any{"type": "string"},
				"reason":   map[string]any{"type": "string"},
			}, []string{"order_id"}),
			Handler: a.toolOrderCancel},

		{Name: "journal_write", Description: "Append a thesis / rationale / note to a portfolio's journal.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"kind":         map[string]any{"type": "string"},
				"body":         map[string]any{"type": "string"},
				"metadata":     map[string]any{"type": "object"},
			}, []string{"portfolio_id", "kind", "body"}),
			Handler: a.toolJournalWrite},

		{Name: "funding_payment_record", Description: "Idempotently record a signed funding payment from a venue feed. Positive amount is paid; negative amount is received.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"}, "symbol": map[string]any{"type": "string"}, "venue_slug": map[string]any{"type": "string"},
				"provider_event_id": map[string]any{"type": "string"}, "amount": map[string]any{"type": "number"}, "currency": map[string]any{"type": "string"},
				"rate_bps": map[string]any{"type": "number"}, "occurred_at": map[string]any{"type": "string"}, "metadata": map[string]any{"type": "object"},
			}, []string{"portfolio_id", "symbol", "provider_event_id", "amount"}), Handler: a.toolFundingPaymentRecord},

		{Name: "watchlist_add", Description: "Track a symbol on a portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"symbol":       map[string]any{"type": "string"},
			}, []string{"portfolio_id", "symbol"}),
			Handler: a.toolWatchlistAdd},

		{Name: "watchlist_remove", Description: "Stop tracking a symbol.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"symbol":       map[string]any{"type": "string"},
			}, []string{"portfolio_id", "symbol"}),
			Handler: a.toolWatchlistRemove},

		// ─── Governance ───────────────────────────────────────────
		{Name: "alert_create", Description: "Create a price/probability/PNL alert. Fires SendEvent to the bound instances on match.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"symbol":       map[string]any{"type": "string"},
				"rule":         map[string]any{"type": "string", "enum": []string{"mark_above", "mark_below", "yes_above", "yes_below", "day_pnl_below"}},
				"threshold":    map[string]any{"type": "number"},
				"expires_at":   map[string]any{"type": "string"},
			}, []string{"portfolio_id", "symbol", "rule", "threshold"}),
			Handler: a.toolAlertCreate},

		{Name: "portfolio_pause", Description: "Pause this portfolio (no new orders accepted). Self-only — resume requires the dashboard.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"reason":       map[string]any{"type": "string"},
			}, []string{"portfolio_id", "reason"}),
			Handler: a.toolPortfolioPause},

		{Name: "portfolio_arm_live", Description: "Arm or disarm automated execution for a broker_live portfolio. Requires the exact confirmation LIVE MONEY when arming.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"armed":        map[string]any{"type": "boolean"},
				"confirmation": map[string]any{"type": "string"},
			}, []string{"portfolio_id", "armed"}),
			Handler: a.toolPortfolioArmLive},

		// ─── Strategies ───────────────────────────────────────────
		{Name: "strategy_create", Description: "Create a deterministic strategy definition. Supports indicator conditions, ranking, and allocation rules.",
			InputSchema: schemaObject(map[string]any{
				"name":                map[string]any{"type": "string"},
				"description":         map[string]any{"type": "string"},
				"status":              map[string]any{"type": "string"},
				"definition":          map[string]any{"type": "object"},
				"created_by_agent_id": map[string]any{"type": "integer"},
			}, []string{"name", "definition"}),
			Handler: a.toolStrategyCreate},

		{Name: "strategy_update", Description: "Update a deterministic strategy. Supplying definition creates a new strategy version.",
			InputSchema: schemaObject(map[string]any{
				"strategy_id": map[string]any{"type": "integer"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"status":      map[string]any{"type": "string"},
				"definition":  map[string]any{"type": "object"},
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
				"definition": map[string]any{"type": "object"},
			}, []string{"definition"}),
			Handler: a.toolStrategyValidate},

		{Name: "strategy_evaluate", Description: "Evaluate a saved strategy against current market data and return target allocations.",
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

		{Name: "strategy_backtest_create", Description: "Create a strategy backtest that reuses backtest_runs/events/snapshots with run_kind=strategy.",
			InputSchema: schemaObject(map[string]any{
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

		{Name: "backtest_market_step", Description: "Internal runner tool: load replay prices into an isolated backtest environment.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"run_id":       map[string]any{"type": "integer"},
				"step":         map[string]any{"type": "integer"},
				"prices":       map[string]any{"type": "array"},
			}, []string{"portfolio_id", "step", "prices"}),
			Handler: a.toolBacktestMarketStep},
	}
}

// ─── Lifecycle handlers ───────────────────────────────────────────

func (a *App) toolPortfolioCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	mandate := strArg(args, "mandate")

	executionEnvironment := strings.ToLower(strings.TrimSpace(strArg(args, "execution_environment")))
	explicitExecutionEnvironment := executionEnvironment != ""
	mode := strings.ToLower(strings.TrimSpace(strArg(args, "mode")))
	if executionEnvironment == "" {
		if mode == "live" {
			executionEnvironment = "broker_live"
		} else {
			executionEnvironment = "simulation"
		}
	}
	if executionEnvironment != "simulation" && executionEnvironment != "broker_paper" && executionEnvironment != "broker_live" {
		return rejectStruct("invalid_execution_environment", "execution_environment must be simulation|broker_paper|broker_live"), nil
	}
	if executionEnvironment == "simulation" {
		mode = "paper"
	} else {
		mode = "live"
	}
	if mode != "paper" && mode != "live" {
		return rejectStruct("invalid_mode", fmt.Sprintf("mode must be paper|live, got %q", mode)), nil
	}

	classesAny, _ := args["allowed_classes"].([]any)
	classes := make([]string, 0, len(classesAny))
	for _, c := range classesAny {
		if s, ok := c.(string); ok {
			classes = append(classes, s)
		}
	}
	if len(classes) == 0 {
		if mode == "live" {
			classes = []string{"crypto"}
		} else {
			classes = []string{"equity", "etf"}
		}
	}

	// ─── Live: broker required, cash + holdings come from the broker ──
	if mode == "live" {
		brokerSlug := strings.TrimSpace(strArg(args, "broker_slug"))
		if brokerSlug == "" {
			// Single-broker-bound installs can default. Multi-broker
			// installs surface an error so the agent picks explicitly.
			defaulted, err := defaultBrokerSlug(ctx)
			if err != nil {
				return rejectStruct("broker_slug_required",
					"mode=live requires broker_slug; "+err.Error()), nil
			}
			brokerSlug = defaulted
		}
		adapter := adapterBySlug(brokerSlug)
		if adapter == nil {
			return rejectStruct("unsupported_broker",
				fmt.Sprintf("no adapter registered for %q (known: %v)", brokerSlug, registeredSlugs())), nil
		}
		// Validate every requested class against the adapter's capabilities.
		for _, c := range classes {
			if !adapterSupportsClass(adapter, c) {
				return rejectStruct("class_unsupported_by_broker",
					fmt.Sprintf("broker %s supports %v; cannot trade class %q",
						brokerSlug, adapter.Capabilities().AssetClasses, c)), nil
			}
		}
		// Resolve a live connection of this slug + pull initial account.
		// brokerFor needs a Portfolio to know the slug, so we build a
		// throwaway and reuse the same lookup at runtime later.
		probe := &Portfolio{Mode: "live", ExecutionEnvironment: executionEnvironment, BrokerSlug: brokerSlug}
		bb, ferr := brokerFor(ctx, probe)
		if ferr != nil {
			return rejectStruct("broker_unbound",
				fmt.Sprintf("broker %s has no active connection bound; bind one in app settings", brokerSlug)), nil
		}
		if brokerSlug == "alpaca-trading" {
			actual, verified := alpacaConnectionEnvironment(ctx, bb.ConnectionID)
			if !verified {
				return rejectStruct("broker_environment_unverified", "Alpaca connection host is not exposed as public metadata; reconnect or upgrade the integration before broker execution"), nil
			}
			if explicitExecutionEnvironment && actual != executionEnvironment {
				return rejectStruct("broker_environment_mismatch", fmt.Sprintf("portfolio requested %s but Alpaca connection is %s", executionEnvironment, actual)), nil
			}
			if !explicitExecutionEnvironment {
				executionEnvironment = actual
			}
		}
		acctRaw, callErr := ctx.PlatformAPI().ExecuteIntegrationTool(
			bb.ConnectionID, bb.toolFor("account.summary"), map[string]any{},
		)
		if callErr != nil || acctRaw == nil || !acctRaw.Success {
			code, detail := adapter.ErrText(acctRaw, callErr)
			noteVenueCall(adapter.Slug(), fmt.Errorf("%s: %s", code, detail))
			return rejectStruct("broker_error",
				fmt.Sprintf("could not read broker account (%s): %s", code, detail)), nil
		}
		acct, parseErr := adapter.ParseAccount(acctRaw.Data)
		if parseErr != nil {
			noteVenueCall(adapter.Slug(), parseErr)
			return rejectStruct("broker_error",
				"could not parse broker account: "+parseErr.Error()), nil
		}
		// Adapters with a separate holdings call (Alpaca) — issue the
		// second tool now and merge.
		if tool := adapter.HoldingsTool(); tool != "" {
			posRaw, perr := ctx.PlatformAPI().ExecuteIntegrationTool(
				bb.ConnectionID, tool, map[string]any{},
			)
			if perr == nil && posRaw != nil && posRaw.Success {
				if holdings, herr := adapter.ParseHoldings(posRaw.Data); herr == nil {
					if acct.Holdings == nil {
						acct.Holdings = map[string]brokerBalance{}
					}
					for k, v := range holdings {
						acct.Holdings[k] = v
					}
				}
			}
		}
		noteVenueCall(adapter.Slug(), nil)
		cash := acct.QuoteCash
		id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{
			ProjectID: pid, Name: name, Mandate: mandate,
			AllowedClasses: classes, StartingCash: cash,
			Mode: "live", ExecutionEnvironment: executionEnvironment, BrokerSlug: brokerSlug,
		})
		if err != nil {
			return nil, err
		}
		_, _ = ctx.AppDB().Exec(`UPDATE portfolios SET available_cash = ? WHERE id = ?`, acct.QuoteAvailable, id)

		// Seed positions from broker holdings. Symbols arrive in the
		// adapter's canonical form (BTC-USD for binance, AAPL / BTC-USD
		// for alpaca via fromAlpacaSymbol). For Alpaca, bal.AvgCost
		// carries the broker's real cost basis (avg_entry_price); for
		// Binance, bal.AvgCost is 0 (not published by get_account) and
		// we fall back to the current mark so unrealized P&L is 0 at
		// seed time instead of bogus.
		seeded := 0
		seededWithCost := 0
		for canonical, bal := range acct.Holdings {
			cls := inferAssetClass(canonical)
			cost := bal.AvgCost
			if cost <= 0 {
				if mark, _ := dbGetMark(ctx.AppDB(), canonical); mark != nil {
					cost = mark.Price
				}
			} else {
				seededWithCost++
			}
			_ = dbInsertPositionRaw(ctx.AppDB(), pid, id, canonical, cls, "", brokerBalanceTotal(bal), cost)
			seeded++
		}
		if seeded > 0 {
			body := ""
			if seededWithCost == seeded {
				body = fmt.Sprintf("Seeded %d position(s) from %s holdings on portfolio create with broker-reported avg_entry_price.", seeded, brokerSlug)
			} else if seededWithCost > 0 {
				body = fmt.Sprintf("Seeded %d position(s) from %s holdings on portfolio create. %d had broker-reported cost basis; the rest defaulted to current mark.", seeded, brokerSlug, seededWithCost)
			} else {
				body = fmt.Sprintf("Seeded %d position(s) from %s holdings on portfolio create. %s doesn't publish cost basis — seeded to current mark; sell-side P&L will be off by the actual cost basis until you reset it.", seeded, brokerSlug, brokerSlug)
			}
			if entryID, jerr := dbInsertJournal(ctx.AppDB(), pid, id, "note", body, map[string]any{
				"source": "broker_reconcile", "kind": "create_seed",
				"broker_slug": brokerSlug, "broker_connection_id": bb.ConnectionID,
				"seeded": seeded, "seeded_with_cost": seededWithCost,
			}); jerr == nil {
				emit("journal.appended", map[string]any{
					"id": entryID, "portfolio_id": id, "kind": "note", "body": body,
				})
			}
		}
		// Backfill order + fill history from the broker. Best-effort
		// — failures here don't block portfolio creation; the next
		// reconcile cycle will eventually fill the gap. Adapters that
		// don't support history backfill (Binance, Polymarket today)
		// return ("", nil) and we silently skip.
		backfilled, openSynced := backfillBrokerHistory(ctx, pid, id, bb)
		if backfilled+openSynced > 0 {
			body := fmt.Sprintf("Backfilled %d historical order(s) and %d open order(s) from %s.", backfilled, openSynced, brokerSlug)
			if entryID, jerr := dbInsertJournal(ctx.AppDB(), pid, id, "note", body, map[string]any{
				"source":               "broker_backfill",
				"kind":                 "history_backfill",
				"broker_slug":          brokerSlug,
				"broker_connection_id": bb.ConnectionID,
				"historical_orders":    backfilled,
				"open_orders_synced":   openSynced,
			}); jerr == nil {
				emit("journal.appended", map[string]any{
					"id": entryID, "portfolio_id": id, "kind": "note", "body": body,
				})
			}
		}

		emit("portfolio.created", map[string]any{
			"id": id, "name": name, "mandate": mandate,
			"allowed_classes": classes, "starting_cash": cash,
			"mode": "live", "project_id": pid,
			"execution_environment": executionEnvironment, "live_armed": false,
			"broker_slug": brokerSlug, "broker_connection_id": bb.ConnectionID,
		})
		return map[string]any{
			"portfolio_id":          id,
			"name":                  name,
			"starting_cash":         cash,
			"mode":                  "live",
			"execution_environment": executionEnvironment,
			"live_armed":            false,
			"broker_slug":           brokerSlug,
			"seeded_positions":      seeded,
		}, nil
	}

	// ─── Paper (default) ─────────────────────────────────────────────
	cash := floatArg(args, "starting_cash", 0)
	if cash <= 0 {
		def := ctx.Config().Get("starting_cash")
		if v, parseErr := strconv.ParseFloat(def, 64); parseErr == nil && v > 0 {
			cash = v
		} else {
			cash = 100_000
		}
	}
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{
		ProjectID: pid, Name: name, Mandate: mandate,
		AllowedClasses: classes, StartingCash: cash, Mode: "paper", ExecutionEnvironment: "simulation",
	})
	if err != nil {
		return nil, err
	}
	configUpdates := map[string]any{}
	if strings.TrimSpace(strArg(args, "source_override")) == "backtest" {
		configUpdates["source_override"] = "backtest"
		configUpdates["pricing_mode"] = "backtest"
	}
	if _, ok := args["fee_bps"]; ok {
		configUpdates["fee_bps"] = math.Max(0, anyFloat(args["fee_bps"]))
	}
	if _, ok := args["slippage_bps"]; ok {
		configUpdates["slippage_bps"] = math.Max(0, anyFloat(args["slippage_bps"]))
	}
	if len(configUpdates) > 0 {
		if err := dbUpdatePortfolioConfig(ctx.AppDB(), id, configUpdates); err != nil {
			return nil, err
		}
	}
	emit("portfolio.created", map[string]any{
		"id": id, "name": name, "mandate": mandate,
		"allowed_classes": classes, "starting_cash": cash,
		"mode": "paper", "project_id": pid,
	})
	return map[string]any{"portfolio_id": id, "name": name, "starting_cash": cash, "mode": "paper", "execution_environment": "simulation"}, nil
}

// defaultBrokerSlug — when portfolio_create's `broker_slug` arg is
// absent, infer it from what's actually bound. If exactly one broker
// slug has an active connection, use it. Otherwise the agent must pick
// explicitly to avoid binding crypto to an equity-only broker by
// accident.
func defaultBrokerSlug(ctx *sdk.AppCtx) (string, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return "", errors.New("platform unavailable")
	}
	seen := map[string]bool{}
	for _, a := range allAdapters() {
		conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{AppSlug: a.Slug()})
		if err != nil {
			continue
		}
		for _, c := range conns {
			if c.Status != "" && c.Status != "active" && c.Status != "connected" {
				continue
			}
			seen[a.Slug()] = true
		}
	}
	switch len(seen) {
	case 0:
		return "", fmt.Errorf("no broker connection bound (known adapters: %v)", registeredSlugs())
	case 1:
		for slug := range seen {
			return slug, nil
		}
	}
	// Multiple bound: ambiguous, force the agent to choose.
	bound := make([]string, 0, len(seen))
	for slug := range seen {
		bound = append(bound, slug)
	}
	return "", fmt.Errorf("multiple brokers bound (%v) — pass broker_slug to disambiguate", bound)
}

// backfillBrokerHistory — best-effort sync of historical closed orders
// + currently-open orders from the broker into the local DB. Called
// once at portfolio_create. Returns (historicalCount, openCount) for
// the audit journal line.
//
// Failures are swallowed — backfill is a quality-of-life surface, not
// a correctness one. The next reconcile tick still picks up open
// orders via their broker_order_id; missing history just means the
// orders/fills tables start sparse.
func backfillBrokerHistory(ctx *sdk.AppCtx, projectID string, portfolioID int64, bb *boundBroker) (int, int) {
	historical := 0
	open := 0

	// ─── Historical (closed) orders ─────────────────────────────
	if tool, args := bb.Adapter.OrdersHistoryTool(); tool != "" {
		historical = importBrokerOrders(ctx, projectID, portfolioID, bb, tool, args, "backfill")
	}

	// ─── Currently-open broker orders ───────────────────────────
	if tool, args := bb.Adapter.OpenOrdersTool(); tool != "" {
		open = importBrokerOrders(ctx, projectID, portfolioID, bb, tool, args, "open_sync")
	}

	return historical, open
}

// importBrokerOrders — shared body for both backfill paths. Fetches +
// parses + inserts; returns how many rows it actually wrote (skipping
// dupes when the broker's order_id is already in our journal).
//
// `kind` flavors the local Order.ID prefix and the journal source so
// audit queries can tell history-fill from open-order-sync apart:
//
//	kind=backfill  → o-bf-<broker_id_short>, source=broker_backfill
//	kind=open_sync → o-os-<broker_id_short>, source=broker_open_sync
func importBrokerOrders(ctx *sdk.AppCtx, projectID string, portfolioID int64, bb *boundBroker, tool string, args map[string]any, kind string) int {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bb.ConnectionID, tool, args)
	if err != nil || res == nil || !res.Success {
		return 0
	}
	rows, err := bb.Adapter.ParseOrders(res.Data)
	if err != nil || len(rows) == 0 {
		return 0
	}

	prefix := "o-bf-"
	source := "broker_backfill:live"
	if kind == "open_sync" {
		prefix = "o-os-"
		source = "broker_open_sync:live"
	}

	wrote := 0
	for _, r := range rows {
		// Idempotency: skip if we already imported this broker_order_id
		// (re-runs of portfolio_create against the same connection
		// shouldn't double-insert).
		if existing, _ := dbOrderIDByBrokerID(ctx.AppDB(), r.BrokerOrderID); existing != "" {
			continue
		}
		// Skip ClientOrderIDs that match our local format (orders
		// originally placed via this app). If they're already in
		// `orders` we'd hit primary-key conflicts; if they're not
		// (orphaned), they likely belong to a different install — not
		// our story to tell.
		if r.ClientOrderID != "" && strings.HasPrefix(r.ClientOrderID, "o-") {
			if existing, _ := dbGetOrder(ctx.AppDB(), projectID, r.ClientOrderID); existing != nil {
				continue
			}
		}

		localID := prefix + safeBrokerIDShort(r.BrokerOrderID)
		rationale := "Imported from broker on portfolio_create — no operator rationale captured. Trust the broker's record of intent."

		if err := dbInsertBackfilledOrder(
			ctx.AppDB(), projectID, portfolioID, localID,
			r.Symbol, r.AssetClass, r.Side, r.Type,
			r.Qty, r.FilledQty, r.AvgFillPrice,
			r.LimitPrice, r.StopPrice, r.TIF,
			r.Status, rationale, source,
			r.PlacedAt, r.ResolvedAt,
		); err != nil {
			ctx.Logger().Warn("backfill: insert order failed",
				"broker_order_id", r.BrokerOrderID, "err", err)
			continue
		}

		// Fill row for filled qty. Use the broker's resolved_at as the
		// fill timestamp when available so equity curves line up.
		if r.FilledQty > 0 && r.AvgFillPrice > 0 {
			fillAt := r.ResolvedAt
			if fillAt == "" {
				fillAt = r.PlacedAt
			}
			_ = dbInsertBackfilledFill(ctx.AppDB(), projectID, localID, portfolioID,
				r.FilledQty, r.AvgFillPrice, 0 /* fee unknown */, fillAt)
		}

		// Rationale journal row carrying the broker_order_id so the
		// existing cancel + status-poll paths can resolve it. Without
		// this row, dbBrokerOrderIDFor would return "" and live
		// cancel against an open-sync'd order would fall to local-only.
		_, _ = dbInsertJournal(ctx.AppDB(), projectID, portfolioID, "rationale", rationale, map[string]any{
			"order_id":             localID,
			"symbol":               r.Symbol,
			"side":                 r.Side,
			"qty":                  r.Qty,
			"type":                 r.Type,
			"broker_slug":          bb.Adapter.Slug(),
			"broker_connection_id": bb.ConnectionID,
			"broker_order_id":      r.BrokerOrderID,
			"client_order_id":      r.ClientOrderID,
			"source":               source,
			"backfill_status":      r.BrokerStatus,
		})

		wrote++
	}
	return wrote
}

// safeBrokerIDShort — crops a long broker id (Alpaca UUIDs are 36 chars)
// to something the orders.id column can carry alongside our prefix.
// Strip non-alphanumeric so the resulting id is URL-safe for the
// /orders/{id}/cancel route.
func safeBrokerIDShort(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			b.WriteRune(c)
			if b.Len() >= 16 {
				break
			}
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func registeredSlugs() []string {
	as := allAdapters()
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Slug())
	}
	return out
}

// toolBrokersList — enumerate registered adapters + bound connections,
// so the agent can decide which slug to pass to portfolio_create. Pure
// read; safe to call without a portfolio.
func (a *App) toolBrokersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profiles, _ := effectiveVenueProfiles(ctx.AppDB())
	out := make([]map[string]any, 0)
	for _, ad := range allAdapters() {
		caps := ad.Capabilities()
		row := map[string]any{
			"slug":          ad.Slug(),
			"asset_classes": caps.AssetClasses,
			"order_types":   caps.OrderTypes,
			"tifs":          caps.TIFs,
			"fractional":    caps.Fractional,
			"quote":         caps.QuoteCurrency,
			"connections":   []map[string]any{},
			"runtime":       venueRuntimeSnapshot(ad.Slug()),
		}
		var venueProfiles []VenueExecutionProfile
		for _, profile := range profiles {
			if profile.VenueSlug == ad.Slug() {
				venueProfiles = append(venueProfiles, profile)
			}
		}
		row["execution_profiles"] = venueProfiles
		if ctx != nil && ctx.PlatformAPI() != nil {
			conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{AppSlug: ad.Slug()})
			if err == nil {
				rows := make([]map[string]any, 0, len(conns))
				for _, c := range conns {
					rows = append(rows, map[string]any{
						"id": c.ID, "name": c.Name, "status": c.Status,
					})
				}
				row["connections"] = rows
				row["bound"] = len(rows) > 0
			}
		}
		out = append(out, row)
	}
	return map[string]any{"brokers": out}, nil
}

func (a *App) toolVenueProfilesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profiles, err := effectiveVenueProfiles(ctx.AppDB())
	if err != nil {
		return nil, err
	}
	venue, class := strings.ToLower(strings.TrimSpace(strArg(args, "venue_slug"))), strings.ToLower(strings.TrimSpace(strArg(args, "asset_class")))
	filtered := make([]VenueExecutionProfile, 0, len(profiles))
	for _, p := range profiles {
		if venue != "" && p.VenueSlug != venue {
			continue
		}
		if class != "" && p.AssetClass != class {
			continue
		}
		filtered = append(filtered, p)
	}
	return map[string]any{"profiles": filtered, "count": len(filtered)}, nil
}

func (a *App) toolVenueProfileUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	venue := strings.ToLower(strings.TrimSpace(strArg(args, "venue_slug")))
	class := strings.ToLower(strings.TrimSpace(strArg(args, "asset_class")))
	symbol := canonicalSymbol(strArg(args, "symbol"))
	if symbol == "" {
		symbol = "*"
	}
	p := defaultVenueProfile(venue, class)
	if existing, err := dbVenueProfile(ctx.AppDB(), venue, class, symbol); err == nil && existing.Symbol == symbol {
		p = *existing
	}
	p.VenueSlug, p.AssetClass, p.Symbol, p.Source = venue, class, symbol, "operator"
	setString := func(key string, target *string) {
		if _, ok := args[key]; ok {
			*target = strArg(args, key)
		}
	}
	setFloat := func(key string, target *float64) {
		if _, ok := args[key]; ok {
			*target = anyFloat(args[key])
		}
	}
	setString("status", &p.Status)
	setString("calendar", &p.Calendar)
	setString("session_policy", &p.SessionPolicy)
	setString("fee_currency", &p.FeeCurrency)
	setString("spread_model", &p.SpreadModel)
	setString("slippage_model", &p.SlippageModel)
	setFloat("maker_fee_bps", &p.MakerFeeBps)
	setFloat("taker_fee_bps", &p.TakerFeeBps)
	setFloat("fallback_spread_bps", &p.FallbackSpreadBps)
	setFloat("slippage_bps", &p.SlippageBps)
	setFloat("min_qty", &p.MinQty)
	setFloat("min_notional", &p.MinNotional)
	setFloat("qty_step", &p.QtyStep)
	setFloat("price_tick", &p.PriceTick)
	setFloat("funding_rate_bps", &p.FundingRateBps)
	if _, ok := args["funding_interval_hours"]; ok {
		p.FundingIntervalHours = int(int64Arg(args, "funding_interval_hours", 0))
	}
	if v, ok := args["supports_post_only"].(bool); ok {
		p.SupportsPostOnly = v
	}
	if v, ok := args["supports_reduce_only"].(bool); ok {
		p.SupportsReduceOnly = v
	}
	if err := dbUpsertVenueProfile(ctx.AppDB(), &p); err != nil {
		return nil, err
	}
	emit("venue.profile.changed", map[string]any{"venue_slug": p.VenueSlug, "asset_class": p.AssetClass, "symbol": p.Symbol, "profile": p})
	return map[string]any{"profile": p}, nil
}

func (a *App) toolExecutionCostsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolioID := int64Arg(args, "portfolio_id", 0)
	if _, err := dbGetPortfolio(ctx.AppDB(), pid, portfolioID); err != nil {
		return nil, fmt.Errorf("portfolio %d not found", portfolioID)
	}
	costs, err := dbExecutionCosts(ctx.AppDB(), portfolioID, int(int64Arg(args, "limit", 100)))
	if err != nil {
		return nil, err
	}
	totals := map[string]float64{}
	for _, cost := range costs {
		totals[cost.Kind] += cost.Amount
	}
	return map[string]any{"costs": costs, "totals": totals, "count": len(costs)}, nil
}

func (a *App) toolFundingPaymentRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolioID := int64Arg(args, "portfolio_id", 0)
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, portfolioID)
	if err != nil {
		return nil, fmt.Errorf("portfolio %d not found", portfolioID)
	}
	symbol := canonicalSymbol(strArg(args, "symbol"))
	eventID := strings.TrimSpace(strArg(args, "provider_event_id"))
	amount := anyFloat(args["amount"])
	if symbol == "" || eventID == "" || !finite(amount) || amount == 0 {
		return nil, errors.New("symbol, provider_event_id, and a finite non-zero amount are required")
	}
	venue := strings.ToLower(strings.TrimSpace(strArg(args, "venue_slug")))
	if venue == "" {
		venue = executionVenue(pf)
	}
	profile := resolveVenueProfile(ctx.AppDB(), pf, symbol, inferAssetClass(symbol))
	currency := strings.ToUpper(strings.TrimSpace(strArg(args, "currency")))
	if currency == "" {
		currency = profile.FeeCurrency
	}
	var rate *float64
	if _, ok := args["rate_bps"]; ok {
		v := anyFloat(args["rate_bps"])
		if !finite(v) {
			return nil, errors.New("rate_bps must be finite")
		}
		rate = &v
	}
	metadata, _ := args["metadata"].(map[string]any)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, inserted, err := dbInsertExecutionCostTx(tx, pid, portfolioID, "", nil, venue, symbol, "funding", amount, currency, rate, "", eventID, metadata, strArg(args, "occurred_at"))
	if err != nil {
		return nil, err
	}
	if inserted && pf.Mode != "live" {
		if _, err := tx.Exec(`UPDATE portfolios SET cash=cash-?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, amount, portfolioID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if inserted {
		body := fmt.Sprintf("Funding %+.8f %s for %s at %s", -amount, currency, symbol, venue)
		_, _ = dbInsertJournal(ctx.AppDB(), pid, portfolioID, "funding", body, map[string]any{"execution_cost_id": id, "provider_event_id": eventID, "amount": amount, "currency": currency, "rate_bps": rate, "venue_slug": venue})
		emit("funding.applied", map[string]any{"portfolio_id": portfolioID, "symbol": symbol, "venue_slug": venue, "amount": amount, "currency": currency, "provider_event_id": eventID})
	}
	return map[string]any{"execution_cost_id": id, "recorded": inserted, "duplicate": !inserted}, nil
}

// ─── Read handlers ────────────────────────────────────────────────

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
	snap, _ := snapshotPortfolio(ctx.AppDB(), p) // computes weights + marks
	pos, _ := dbListPositions(ctx.AppDB(), id)
	classFilter, _ := args["asset_class"].(string)
	out := make([]*Position, 0, len(pos))
	for _, q := range pos {
		// Re-merge the snapshot's computed fields (weight, market_value, etc.)
		// onto the bare row.
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
		// Fall back to live provider if engine hasn't ticked yet.
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

func (a *App) toolMarketCalendar(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	symbol := canonicalSymbol(strArg(args, "symbol"))
	if symbol == "" {
		return nil, errors.New("symbol required")
	}
	instrument, err := dbGetInstrument(ctx.AppDB(), symbol)
	if err != nil && globalEngine != nil {
		mark, quoteErr := globalEngine.provider.Quote(symbol)
		if quoteErr != nil {
			return nil, quoteErr
		}
		if err := dbUpsertMark(ctx.AppDB(), mark); err != nil {
			return nil, err
		}
		instrument, err = dbGetInstrument(ctx.AppDB(), symbol)
	}
	if err != nil {
		return nil, err
	}
	at := time.Now().UTC()
	if raw := strings.TrimSpace(strArg(args, "at")); raw != "" {
		at, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, fmt.Errorf("at must be RFC3339: %w", err)
		}
	}
	session, err := marketSessionAt(instrument.Calendar, at)
	if err != nil {
		return nil, err
	}
	return map[string]any{"symbol": symbol, "instrument": instrument, "session": session}, nil
}

func (a *App) toolReferenceDataStatus(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return referenceDataStatus(ctx.AppDB()), nil
}

func (a *App) toolSecurityResolve(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	query := strings.TrimSpace(strArg(args, "query"))
	if query == "" {
		return nil, errors.New("query required")
	}
	rows, err := dbListSecurities(ctx.AppDB(), query, strArg(args, "as_of"), intArg(args, "limit", 25))
	if err != nil {
		return nil, err
	}
	return map[string]any{"securities": rows, "count": len(rows), "as_of": strArg(args, "as_of")}, nil
}

func (a *App) toolCorporateActionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := dbListCorporateActions(ctx.AppDB(), strArg(args, "symbol"), strArg(args, "type"), strArg(args, "since"), strArg(args, "until"), intArg(args, "limit", 200))
	if err != nil {
		return nil, err
	}
	return map[string]any{"corporate_actions": rows, "count": len(rows)}, nil
}

func (a *App) toolExchangeSessionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := dbListExchangeSessions(ctx.AppDB(), strArg(args, "venue"), strArg(args, "start"), strArg(args, "end"), intArg(args, "limit", 500))
	if err != nil {
		return nil, err
	}
	return map[string]any{"sessions": rows, "count": len(rows)}, nil
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

// ─── Write handlers ───────────────────────────────────────────────

func (a *App) toolRiskProfilesList(_ *sdk.AppCtx, _ map[string]any) (any, error) {
	return map[string]any{"profiles": riskProfiles()}, nil
}

func portfolioForRiskArgs(ctx *sdk.AppCtx, args map[string]any) (*Portfolio, string, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, "", err
	}
	id := int64Arg(args, "portfolio_id", 0)
	portfolio, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, "", fmt.Errorf("portfolio %d not found", id)
	}
	return portfolio, pid, nil
}

func (a *App) toolPortfolioRiskGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	portfolio, _, err := portfolioForRiskArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	policy, err := dbGetPortfolioRiskPolicy(ctx.AppDB(), portfolio)
	if err != nil {
		return nil, err
	}
	equity, err := computeEquity(ctx.AppDB(), portfolio)
	if err != nil {
		return nil, err
	}
	state, err := dbUpdatePortfolioRiskState(ctx.AppDB(), portfolio, equity)
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": policy, "state": state}, nil
}

func (a *App) toolPortfolioRiskUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	portfolio, pid, err := portfolioForRiskArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	current, err := dbGetPortfolioRiskPolicy(ctx.AppDB(), portfolio)
	if err != nil {
		return nil, err
	}
	requested := *current
	requested.RiskLevel = strings.ToLower(strings.TrimSpace(strArg(args, "risk_level")))
	if requested.RiskLevel == "" {
		requested.RiskLevel = current.RiskLevel
	}
	if requested.RiskLevel == "custom" {
		for key, target := range map[string]*float64{
			"max_daily_loss_pct": &requested.MaxDailyLossPct, "max_drawdown_pct": &requested.MaxDrawdownPct,
			"max_position_pct": &requested.MaxPositionPct, "max_gross_exposure_pct": &requested.MaxGrossExposurePct,
			"max_order_pct": &requested.MaxOrderPct,
		} {
			if _, ok := args[key]; ok {
				*target = floatArg(args, key, math.NaN())
			}
		}
	}
	policy, err := dbUpsertPortfolioRiskPolicy(ctx.AppDB(), portfolio, requested)
	if err != nil {
		return nil, err
	}
	emit("risk.policy.changed", map[string]any{"portfolio_id": portfolio.ID, "project_id": pid, "policy": policy})
	return map[string]any{"policy": policy}, nil
}

func (a *App) toolPortfolioObjectiveCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	portfolio, pid, err := portfolioForRiskArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	objective := PortfolioObjective{
		Name: strArg(args, "name"), Metric: strArg(args, "metric"), TargetPct: floatArg(args, "target_pct", math.NaN()),
		Direction: strArg(args, "direction"), StartsAt: strArg(args, "starts_at"), DeadlineAt: strArg(args, "deadline_at"), Status: "active",
	}
	equity, err := computeEquity(ctx.AppDB(), portfolio)
	if err != nil {
		return nil, err
	}
	created, err := dbCreatePortfolioObjective(ctx.AppDB(), portfolio, objective, equity)
	if err != nil {
		return nil, err
	}
	snap, _ := snapshotPortfolio(ctx.AppDB(), portfolio)
	objectiveProgress(ctx.AppDB(), portfolio, created, snap)
	emit("portfolio.objective.changed", map[string]any{"portfolio_id": portfolio.ID, "project_id": pid, "objective": created, "action": "created"})
	return map[string]any{"objective": created}, nil
}

func (a *App) toolPortfolioObjectivesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	portfolio, _, err := portfolioForRiskArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	includeArchived, _ := args["include_archived"].(bool)
	objectives, err := objectivesWithProgress(ctx.AppDB(), portfolio, includeArchived)
	if err != nil {
		return nil, err
	}
	return map[string]any{"objectives": objectives}, nil
}

func (a *App) toolPortfolioObjectiveUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	portfolio, pid, err := portfolioForRiskArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "objective_id", 0)
	current, err := dbGetPortfolioObjective(ctx.AppDB(), portfolio, id)
	if err != nil {
		return nil, fmt.Errorf("objective %d not found", id)
	}
	updated := *current
	oldMetric, oldStart := updated.Metric, updated.StartsAt
	if _, ok := args["name"]; ok {
		updated.Name = strArg(args, "name")
	}
	if _, ok := args["metric"]; ok {
		updated.Metric = strArg(args, "metric")
	}
	if _, ok := args["target_pct"]; ok {
		updated.TargetPct = floatArg(args, "target_pct", math.NaN())
	}
	if _, ok := args["direction"]; ok {
		updated.Direction = strArg(args, "direction")
	}
	if _, ok := args["starts_at"]; ok {
		updated.StartsAt = strArg(args, "starts_at")
	}
	if _, ok := args["deadline_at"]; ok {
		updated.DeadlineAt = strArg(args, "deadline_at")
	}
	if _, ok := args["status"]; ok {
		updated.Status = strArg(args, "status")
	}
	if oldMetric != updated.Metric || oldStart != updated.StartsAt {
		updated.BaselineEquity = nil
		start, _ := time.Parse(time.RFC3339, updated.StartsAt)
		if updated.Metric == "period_return_pct" && !start.After(time.Now().UTC()) {
			equity, equityErr := computeEquity(ctx.AppDB(), portfolio)
			if equityErr != nil {
				return nil, equityErr
			}
			updated.BaselineEquity = &equity
		}
	}
	saved, err := dbUpdatePortfolioObjective(ctx.AppDB(), portfolio, updated)
	if err != nil {
		return nil, err
	}
	snap, _ := snapshotPortfolio(ctx.AppDB(), portfolio)
	objectiveProgress(ctx.AppDB(), portfolio, saved, snap)
	emit("portfolio.objective.changed", map[string]any{"portfolio_id": portfolio.ID, "project_id": pid, "objective": saved, "action": "updated"})
	return map[string]any{"objective": saved}, nil
}

const minRationaleLen = 30

func (a *App) toolOrderPlace(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("portfolio %d not found", id)
	}

	// Pre-trade pipeline. Each rejection returns a structured status —
	// the agent should reason about it, not raise.
	rationale, _ := args["rationale"].(string)
	if len(strings.TrimSpace(rationale)) < minRationaleLen {
		return rejectStruct("rationale_required",
			fmt.Sprintf("rationale must be ≥ %d chars; got %d", minRationaleLen, len(rationale))), nil
	}
	if pf.Status != "active" {
		return rejectStruct("portfolio_not_active",
			fmt.Sprintf("portfolio status is %q", pf.Status)), nil
	}
	if normalizeExecutionEnvironment(pf.ExecutionEnvironment, pf.Mode, pf.BrokerSlug) == "broker_live" && !pf.LiveArmed {
		return rejectStruct("live_not_armed", "broker_live portfolio is disarmed; arm it explicitly before placing real-money orders"), nil
	}
	symbol := canonicalSymbol(strArg(args, "symbol"))
	side := strings.ToLower(strings.TrimSpace(strArg(args, "side")))
	otype := strings.ToLower(strings.TrimSpace(strArg(args, "type")))
	qty := floatArg(args, "qty", 0)
	if symbol == "" || side == "" || otype == "" || qty <= 0 {
		return rejectStruct("invalid_args", "symbol, side, type, qty are required and qty > 0"), nil
	}
	class := inferAssetClass(symbol)
	if !contains(pf.AllowedClasses, class) {
		return rejectStruct("asset_class_blocked",
			fmt.Sprintf("class %q not in allowed_classes %v", class, pf.AllowedClasses)), nil
	}
	// Side / class consistency. Prediction-market outcome is independent
	// from direction so SELL can close either an existing YES or NO leg.
	outcome := strings.ToLower(strings.TrimSpace(strArg(args, "outcome")))
	if class == "polymarket" {
		if side == "yes" || side == "no" {
			if outcome == "" {
				outcome = side
			}
		} else if side != "sell" {
			return rejectStruct("invalid_side", "polymarket side must be 'yes', 'no', or 'sell'"), nil
		}
		if outcome != "yes" && outcome != "no" {
			return rejectStruct("invalid_outcome", "polymarket outcome must be 'yes' or 'no'; it is required for sell orders"), nil
		}
		if otype == "stop" {
			return rejectStruct("invalid_type", "stop orders not supported on polymarket"), nil
		}
	} else {
		if side != "buy" && side != "sell" {
			return rejectStruct("invalid_side", "equity/crypto side must be 'buy' or 'sell'"), nil
		}
	}
	if violation, universeErr := portfolioUniverseViolation(ctx.AppDB(), pf, symbol); universeErr != nil {
		return nil, universeErr
	} else if violation != nil && !(side == "sell" && portfolioCanReducePosition(ctx.AppDB(), pf.ID, symbol, outcome, qty)) {
		emit("order.rejected", map[string]any{"portfolio_id": pf.ID, "symbol": symbol, "code": violation.Code, "detail": violation.Detail})
		return rejectStruct(violation.Code, violation.Detail), nil
	}
	tif, _ := args["tif"].(string)
	if tif == "" {
		tif = "day"
	}
	var lp, sp *float64
	if v := floatArg(args, "limit_price", 0); v > 0 {
		lp = &v
	}
	if v := floatArg(args, "stop_price", 0); v > 0 {
		sp = &v
	}
	if otype == "limit" && lp == nil {
		return rejectStruct("invalid_args", "limit orders require limit_price"), nil
	}
	if otype == "stop" && sp == nil {
		return rejectStruct("invalid_args", "stop orders require stop_price"), nil
	}
	if class == "polymarket" && lp != nil {
		if *lp <= 0 || *lp >= 1 {
			return rejectStruct("invalid_args", "polymarket limit_price must be in (0, 1)"), nil
		}
	}
	mark, err := ensureExecutableMark(ctx, pf, symbol)
	if err != nil {
		return rejectStruct("market_data_unavailable", err.Error()), nil
	}
	riskPrice := markPriceForSide(mark, strings.ToUpper(outcome))
	if lp != nil {
		riskPrice = *lp
	} else if sp != nil {
		riskPrice = *sp
	} else if isBuySide(side) && mark.AskPrice != nil && *mark.AskPrice > 0 {
		riskPrice = *mark.AskPrice
	}
	profile := resolveVenueProfile(ctx.AppDB(), pf, symbol, class)
	if violation := validateExecutionOrder(profile, mark.Instrument, mark, qty, riskPrice); violation != nil {
		emit("order.rejected", map[string]any{"portfolio_id": pf.ID, "symbol": symbol, "code": violation.Code, "detail": violation.Detail, "venue_slug": profile.VenueSlug})
		return rejectStruct(violation.Code, violation.Detail), nil
	}
	if lp != nil {
		if violation := validatePriceTick(profile, *lp, "limit_price"); violation != nil {
			return rejectStruct(violation.Code, violation.Detail), nil
		}
	}
	if sp != nil {
		if violation := validatePriceTick(profile, *sp, "stop_price"); violation != nil {
			return rejectStruct(violation.Code, violation.Detail), nil
		}
	}
	if pf.Mode == "live" {
		if adapter := adapterBySlug(pf.BrokerSlug); adapter != nil {
			caps := adapter.Capabilities()
			if !contains(caps.OrderTypes, otype) {
				return rejectStruct("broker_unsupported_order_type", fmt.Sprintf("%s does not support %s orders", adapter.Slug(), otype)), nil
			}
			if !contains(caps.TIFs, strings.ToLower(tif)) && !(strings.EqualFold(tif, "day") && contains(caps.TIFs, "gtc")) {
				return rejectStruct("broker_unsupported_tif", fmt.Sprintf("%s does not support tif %s", adapter.Slug(), tif)), nil
			}
		}
	}
	// Serialize the policy snapshot and local order insert. Without this
	// narrow critical section, concurrent callers could each pass against
	// the same pre-order exposure and collectively exceed the limit.
	orderPlacementMu.Lock()
	if breach, checkErr := preTradeRiskCheck(ctx.AppDB(), pf, symbol, side, qty, riskPrice); checkErr != nil {
		orderPlacementMu.Unlock()
		return nil, checkErr
	} else if breach != nil {
		orderPlacementMu.Unlock()
		emit("risk.limit.breached", map[string]any{
			"portfolio_id": pf.ID, "project_id": pid, "symbol": symbol, "side": side,
			"code": breach.Code, "detail": breach.Detail, "actual_pct": breach.ActualPct,
			"limit_pct": breach.LimitPct, "source": strArg(args, "source_override"),
		})
		return rejectStruct(breach.Code, breach.Detail), nil
	}

	// Source — "agent" by default; HTTP path overrides via source_override.
	// Live mode appends ":live" so audit queries can split paper from live
	// without joining onto the portfolio row.
	source := "agent"
	if so, ok := args["source_override"].(string); ok && so != "" {
		source = so
	}
	if pf.Mode == "live" {
		source += ":live"
	}
	strategyID := int64Arg(args, "strategy_id", 0)
	assignmentID := int64Arg(args, "assignment_id", 0)
	targetWeight := floatArg(args, "target_weight", 0)

	// Build + write the order. Status begins working — for paper, the
	// engine picks it up on the next tick; for live, we forward to the
	// broker below and reflect the response inline.
	o := &Order{
		ID:            "o-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		PortfolioID:   pf.ID,
		Symbol:        symbol,
		AssetClass:    class,
		Side:          side,
		Outcome:       strings.ToUpper(outcome),
		Type:          otype,
		Qty:           qty,
		LimitPrice:    lp,
		StopPrice:     sp,
		TIF:           tif,
		Status:        "working",
		Rationale:     rationale,
		Source:        source,
		LiquidityRole: orderLiquidityAtPlacement(mark, side, otype, lp),
	}
	o.VenueFeeBps = profileFeeBps(profile, o.LiquidityRole)
	if err := dbInsertOrder(ctx.AppDB(), o, pid); err != nil {
		orderPlacementMu.Unlock()
		return nil, err
	}
	orderPlacementMu.Unlock()

	// ─── Live: forward to broker, apply response inline ─────────────
	if pf.Mode == "live" {
		bb, ferr := brokerFor(ctx, pf)
		if ferr != nil {
			// Unbound mid-flight (operator unbound after portfolio create),
			// or the slug isn't registered. Local order flips rejected; agent
			// gets a clear code so they can stop retrying.
			code := "broker_unbound"
			detail := "portfolio is mode=live but no broker is bound; rebind to resume"
			if errors.Is(ferr, errBrokerUnbound) {
				detail = fmt.Sprintf("broker %s has no active connection bound; rebind in app settings", pf.BrokerSlug)
			} else if ferr != errPaper {
				code = "broker_lookup_failed"
				detail = ferr.Error()
			}
			_ = dbRejectOrder(ctx.AppDB(), o.ID, code, detail)
			emit("order.rejected", map[string]any{
				"order_id": o.ID, "portfolio_id": pf.ID,
				"code": code, "detail": detail,
			})
			return rejectStruct(code, detail), nil
		}
		adapter := bb.Adapter
		// Capability check — defense in depth; portfolio_create already
		// validated allowed_classes against the adapter, but a watchlist
		// add could have widened the agent's reach.
		if !adapterSupportsClass(adapter, class) {
			_ = dbRejectOrder(ctx.AppDB(), o.ID, "broker_unsupported_class",
				fmt.Sprintf("%s adapter does not support %q (supports %v)", adapter.Slug(), class, adapter.Capabilities().AssetClasses))
			emit("order.rejected", map[string]any{"order_id": o.ID, "code": "broker_unsupported_class"})
			return rejectStruct("broker_unsupported_class",
				fmt.Sprintf("%s adapter does not support %q", adapter.Slug(), class)), nil
		}
		// Pre-trade cash check (live). Mirrors the paper-engine check in
		// tryFill. Uses broker-reported available cash when present and
		// the best price estimate we have. Broker will reject if we
		// somehow get past this, but burning an API call on an obviously
		// underfunded order isn't worth it.
		if isBuySide(side) {
			estPrice := 0.0
			if lp != nil {
				estPrice = *lp
			} else if mark, mErr := dbGetMark(ctx.AppDB(), symbol); mErr == nil && mark != nil {
				estPrice = mark.Price
				if mark.AskPrice != nil && *mark.AskPrice > 0 {
					estPrice = *mark.AskPrice
				}
			}
			if estPrice > 0 {
				needed := qty * estPrice * 1.005 // 0.5% buffer for slippage + fees
				available := pf.Cash
				if pf.AvailableCash != nil {
					available = *pf.AvailableCash
				}
				if available < needed {
					detail := fmt.Sprintf("estimated need %.2f, available cash %.2f (broker-synced ≤60s ago)", needed, available)
					_ = dbRejectOrder(ctx.AppDB(), o.ID, "insufficient_cash", detail)
					emit("order.rejected", map[string]any{
						"order_id": o.ID, "portfolio_id": pf.ID,
						"code": "insufficient_cash", "detail": detail,
					})
					return rejectStruct("insufficient_cash", detail), nil
				}
			}
		}
		brokerArgs, terr := adapter.TranslateOrder(o)
		if terr != nil {
			_ = dbRejectOrder(ctx.AppDB(), o.ID, "translate_failed", terr.Error())
			emit("order.rejected", map[string]any{"order_id": o.ID, "code": "translate_failed", "detail": terr.Error()})
			return rejectStruct("translate_failed", terr.Error()), nil
		}
		res, callErr := ctx.PlatformAPI().ExecuteIntegrationTool(
			bb.ConnectionID, bb.placeToolFor(o), brokerArgs,
		)
		if callErr != nil {
			noteVenueCall(adapter.Slug(), callErr)
		} else if res == nil || !res.Success {
			code, detail := adapter.ErrText(res, nil)
			noteVenueCall(adapter.Slug(), fmt.Errorf("%s: %s", code, detail))
		} else {
			noteVenueCall(adapter.Slug(), nil)
		}
		// Ambiguous broker outcomes (network error, non-success response,
		// parse failure of a successful response) leave the local order in
		// 'working' state instead of marking it rejected. The broker may
		// have accepted the order even when the response didn't make it
		// back cleanly — rejecting locally while real money is working at
		// the broker is the unrecoverable case. tryReconcile polls by
		// client id next tick; if the broker reports "unknown order" the
		// reconciler rejects locally then (exec.go).
		uncertain := func(code, detail string) (any, error) {
			if _, jerr := dbInsertJournal(ctx.AppDB(), pid, pf.ID, "rationale", rationale, map[string]any{
				"order_id": o.ID, "symbol": symbol, "side": side, "qty": qty, "type": otype,
				"source": source, "strategy_id": strategyID, "assignment_id": assignmentID, "target_weight": targetWeight,
				"broker_slug":          adapter.Slug(),
				"broker_connection_id": bb.ConnectionID,
				"client_order_id":      o.ID,
				"broker_call_status":   "uncertain",
				"broker_call_code":     code,
				"broker_call_detail":   detail,
			}); jerr == nil {
				emit("journal.appended", map[string]any{
					"portfolio_id": pf.ID, "kind": "rationale", "body": rationale,
				})
			}
			emit("order.placed", map[string]any{
				"order_id": o.ID, "portfolio_id": pf.ID, "symbol": symbol, "asset_class": class,
				"side": side, "type": otype, "qty": qty,
				"limit_price": o.LimitPrice, "stop_price": o.StopPrice,
				"status": "working", "rationale": rationale, "mode": "live",
				"broker_slug": adapter.Slug(), "source": source,
				"uncertain": true, "broker_call_code": code, "broker_call_detail": detail,
			})
			ctx.Logger().Warn("broker call uncertain — leaving order working for reconciler",
				"order_id", o.ID, "broker", adapter.Slug(), "code", code, "detail", detail)
			return map[string]any{
				"order_id":  o.ID,
				"status":    "working",
				"uncertain": true,
				"code":      code,
				"detail":    detail,
				"note":      "broker response was ambiguous; local order kept working — reconciler will poll by client_order_id",
			}, nil
		}
		if callErr != nil || res == nil || !res.Success {
			code, detail := adapter.ErrText(res, callErr)
			return uncertain(code, detail)
		}
		br, perr := adapter.ParseOrder(res.Data)
		if perr != nil {
			return uncertain("broker_parse_failed", perr.Error())
		}

		// Persist broker linkage in the rationale journal row before any
		// fill row lands, so an audit can join order → broker_order_id by
		// kind='rationale' alone.
		if entryID, jerr := dbInsertJournal(ctx.AppDB(), pid, pf.ID, "rationale", rationale, map[string]any{
			"order_id": o.ID, "symbol": symbol, "side": side, "qty": qty, "type": otype,
			"source": source, "strategy_id": strategyID, "assignment_id": assignmentID, "target_weight": targetWeight,
			"broker_slug":          adapter.Slug(),
			"broker_connection_id": bb.ConnectionID,
			"broker_order_id":      br.BrokerOrderID,
			"client_order_id":      o.ID,
		}); jerr == nil {
			emit("journal.appended", map[string]any{
				"id": entryID, "portfolio_id": pf.ID, "kind": "rationale", "body": rationale,
			})
		}

		// emit order.placed before applying fills so subscribers see
		// {placed, filled} in order rather than just a sudden FILLED.
		emit("order.placed", map[string]any{
			"order_id":        o.ID,
			"portfolio_id":    pf.ID,
			"symbol":          symbol,
			"asset_class":     class,
			"side":            side,
			"type":            otype,
			"qty":             qty,
			"limit_price":     o.LimitPrice,
			"stop_price":      o.StopPrice,
			"status":          "working",
			"rationale":       rationale,
			"mode":            "live",
			"broker_slug":     adapter.Slug(),
			"liquidity_role":  o.LiquidityRole,
			"broker_order_id": br.BrokerOrderID,
			"source":          source,
			"strategy_id":     strategyID,
			"assignment_id":   assignmentID,
		})

		// Apply any inline fills (e.g. Binance market orders return them
		// synchronously) + reflect terminal status.
		previousFilled := o.FilledQty
		changed, ferr := applyBrokerProgress(ctx.AppDB(), pid, pf, o, br)
		if ferr != nil {
			ctx.Logger().Warn("apply broker progress failed", "order_id", o.ID, "err", ferr)
		} else if changed && br.ExecutedQty > previousFilled+1e-9 && globalEngine != nil {
			globalEngine.bumpFillCounter()
		}
		return map[string]any{
			"order_id":        o.ID,
			"broker_order_id": br.BrokerOrderID,
			"status":          o.Status, // updated by applyBrokerProgress
			"filled_qty":      o.FilledQty,
			"avg_fill_price":  o.AvgFillPrice,
		}, nil
	}

	// ─── Paper (default) — engine fills on next tick ────────────────
	emit("order.placed", map[string]any{
		"order_id":       o.ID,
		"portfolio_id":   pf.ID,
		"symbol":         symbol,
		"asset_class":    class,
		"side":           side,
		"type":           otype,
		"qty":            qty,
		"limit_price":    o.LimitPrice,
		"stop_price":     o.StopPrice,
		"status":         "working",
		"rationale":      rationale,
		"mode":           "paper",
		"source":         source,
		"strategy_id":    strategyID,
		"assignment_id":  assignmentID,
		"liquidity_role": o.LiquidityRole,
	})
	// Auto-attach a rationale row to the journal for audit.
	if entryID, err := dbInsertJournal(ctx.AppDB(), pid, pf.ID, "rationale", rationale, map[string]any{
		"order_id": o.ID, "symbol": symbol, "side": side, "qty": qty, "type": otype,
		"source": source, "strategy_id": strategyID, "assignment_id": assignmentID, "target_weight": targetWeight,
	}); err == nil {
		emit("journal.appended", map[string]any{
			"id": entryID, "portfolio_id": pf.ID, "kind": "rationale", "body": rationale,
		})
	}
	return map[string]any{"order_id": o.ID, "status": "working"}, nil
}

func (a *App) toolOrderCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, _ := args["order_id"].(string)
	reason, _ := args["reason"].(string)
	if id == "" {
		return nil, errors.New("order_id required")
	}

	// Look the order up first so we know whether it's a live order that
	// needs a broker cancel before the local row flips.
	o, oerr := dbGetOrder(ctx.AppDB(), pid, id)
	if oerr != nil {
		return nil, fmt.Errorf("order %s not found", id)
	}
	if o.Status != "working" {
		return map[string]any{"status": "already_resolved", "order_id": id, "current_status": o.Status}, nil
	}
	pf, perr := dbGetPortfolio(ctx.AppDB(), pid, o.PortfolioID)
	if perr != nil {
		return nil, fmt.Errorf("portfolio %d not found for order %s", o.PortfolioID, id)
	}

	// Live: cancel upstream first. If the broker says "already filled" or
	// "not found", we still flip locally — the reconciler would have
	// caught it on the next tick anyway.
	if pf.Mode == "live" {
		bb, ferr := brokerFor(ctx, pf)
		if ferr != nil {
			return rejectStruct("broker_unbound",
				fmt.Sprintf("live cancel needs broker bound (slug=%s): %s", pf.BrokerSlug, ferr.Error())), nil
		}
		adapter := bb.Adapter
		brokerOrderID, _ := dbBrokerOrderIDFor(ctx.AppDB(), id)
		caps := adapter.Capabilities()
		if brokerOrderID == "" && !caps.CancelByClientID {
			// We cannot prove that the exchange order was cancelled. Keep it
			// working locally so reconciliation continues supervising it.
			ctx.Logger().Warn("live order missing broker_order_id; keeping order supervised",
				"order_id", id, "broker", adapter.Slug())
			return rejectStruct("broker_cancel_unverifiable",
				"broker order id is missing and this broker cannot cancel by client id; order remains working"), nil
		} else {
			cancelArgs := adapter.CancelArgs(o, brokerOrderID)
			res, cerr := ctx.PlatformAPI().ExecuteIntegrationTool(
				bb.ConnectionID, bb.toolFor("order.cancel"), cancelArgs,
			)
			if cerr != nil || res == nil || !res.Success {
				code, detail := adapter.ErrText(res, cerr)
				noteVenueCall(adapter.Slug(), fmt.Errorf("%s: %s", code, detail))
				// Unknown/not-cancelable does not prove whether the order filled,
				// expired, or is still active under a different identifier. Leave
				// the local row working; the status reconciler will resolve truth.
				return rejectStruct("broker_cancel_unconfirmed", code+": "+detail), nil
			}
			noteVenueCall(adapter.Slug(), nil)
		}
	}

	status, err := dbCancelOrder(ctx.AppDB(), pid, id, reason)
	if err != nil {
		return nil, err
	}
	if status == "cancelled" {
		emit("order.cancelled", map[string]any{"order_id": id, "reason": reason, "mode": pf.Mode})
		return map[string]any{"status": "cancelled", "order_id": id}, nil
	}
	return map[string]any{"status": "already_resolved", "order_id": id, "current_status": status}, nil
}

func (a *App) toolJournalWrite(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolioID := int64Arg(args, "portfolio_id", 0)
	if _, err := dbGetPortfolio(ctx.AppDB(), pid, portfolioID); err != nil {
		return nil, fmt.Errorf("portfolio %d not found", portfolioID)
	}
	kind, _ := args["kind"].(string)
	body, _ := args["body"].(string)
	if kind == "" || body == "" {
		return nil, errors.New("kind and body required")
	}
	meta, _ := args["metadata"].(map[string]any)
	entryID, err := dbInsertJournal(ctx.AppDB(), pid, portfolioID, kind, body, meta)
	if err != nil {
		return nil, err
	}
	emit("journal.appended", map[string]any{
		"id": entryID, "portfolio_id": portfolioID, "kind": kind, "body": body, "metadata": meta,
	})
	return map[string]any{"entry_id": entryID}, nil
}

func (a *App) toolWatchlistAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	symbol := canonicalSymbol(strArg(args, "symbol"))
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("portfolio %d not found", id)
	}
	class := inferAssetClass(symbol)
	if !contains(pf.AllowedClasses, class) {
		return nil, fmt.Errorf("asset class %q not in portfolio's allowed_classes", class)
	}
	if _, err := ensureExecutableMark(ctx, pf, symbol); err != nil {
		return nil, err
	}
	added, err := dbWatchlistAdd(ctx.AppDB(), pid, id, symbol)
	if err != nil {
		return nil, err
	}
	wl, _ := dbWatchlist(ctx.AppDB(), id)
	if added {
		emit("watchlist.changed", map[string]any{"portfolio_id": id, "watchlist": wl, "added": symbol})
	}
	return map[string]any{"added": added, "watchlist": wl}, nil
}

func ensureExecutableMark(ctx *sdk.AppCtx, pf *Portfolio, symbol string) (*Mark, error) {
	if strings.TrimSpace(symbol) == "" {
		return nil, errors.New("symbol required")
	}
	// Isolated backtests receive immutable replay marks from the parent and
	// must never contact the current live market.
	if pf != nil && dbHasBacktestPortfolio(ctx.AppDB(), pf.ProjectID) {
		mark, err := dbGetMark(ctx.AppDB(), symbol)
		if err != nil {
			return nil, fmt.Errorf("no replay mark available for %s", symbol)
		}
		return mark, nil
	}
	if globalEngine == nil || globalEngine.provider == nil {
		return nil, errors.New("market data engine not ready")
	}
	class := inferAssetClass(symbol)
	strict := !strings.EqualFold(strings.TrimSpace(ctx.Config().Get("strict_broker_market_data")), "false")
	if cached, err := dbGetMark(ctx.AppDB(), symbol); err == nil && cached != nil && markFresh(cached, time.Now().UTC()) {
		brokerEquity := pf != nil && portfolioBrokerBacked(pf) && (class == "equity" || class == "etf")
		if !brokerEquity || !strict || cached.Source == alpacaMarketDataSlug {
			return cached, nil
		}
	}
	mark, err := globalEngine.provider.Quote(symbol)
	if err != nil {
		return nil, fmt.Errorf("live quote for %s: %w", symbol, err)
	}
	if mark == nil || mark.Price <= 0 {
		return nil, fmt.Errorf("live quote for %s is empty", symbol)
	}
	if strict && pf != nil && portfolioBrokerBacked(pf) && (class == "equity" || class == "etf") && mark.Source != alpacaMarketDataSlug {
		return nil, fmt.Errorf("strict broker market data requires Alpaca for %s; got %s", symbol, mark.Source)
	}
	if err := dbUpsertMark(ctx.AppDB(), mark); err != nil {
		return nil, err
	}
	return mark, nil
}

func (a *App) toolWatchlistRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	if _, err := dbGetPortfolio(ctx.AppDB(), pid, id); err != nil {
		return nil, fmt.Errorf("portfolio %d not found", id)
	}
	symbol := canonicalSymbol(strArg(args, "symbol"))
	removed, err := dbWatchlistRemove(ctx.AppDB(), id, symbol)
	if err != nil {
		return nil, err
	}
	wl, _ := dbWatchlist(ctx.AppDB(), id)
	if removed {
		emit("watchlist.changed", map[string]any{"portfolio_id": id, "watchlist": wl, "removed": symbol})
	}
	return map[string]any{"removed": removed, "watchlist": wl}, nil
}

func (a *App) toolAlertCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	a2 := &Alert{
		PortfolioID: int64Arg(args, "portfolio_id", 0),
		Symbol:      strArg(args, "symbol"),
		Rule:        strArg(args, "rule"),
		Threshold:   floatArg(args, "threshold", 0),
	}
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, a2.PortfolioID)
	if err != nil {
		return nil, fmt.Errorf("portfolio %d not found", a2.PortfolioID)
	}
	a2.Symbol = canonicalSymbol(a2.Symbol)
	if _, err := ensureExecutableMark(ctx, pf, a2.Symbol); err != nil {
		return nil, err
	}
	if exp, ok := args["expires_at"].(string); ok && exp != "" {
		// Validate RFC3339; if it parses, store it verbatim.
		if _, err := time.Parse(time.RFC3339, exp); err == nil {
			a2.ExpiresAt = exp
		}
	}
	id, err := dbInsertAlert(ctx.AppDB(), pid, a2)
	if err != nil {
		return nil, err
	}
	return map[string]any{"alert_id": id}, nil
}

func (a *App) toolPortfolioPause(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	reason := strArg(args, "reason")
	if reason == "" {
		return nil, errors.New("reason required")
	}
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("portfolio %d not found", id)
	}
	if err := dbSetPortfolioStatus(ctx.AppDB(), pf.ID, "paused"); err != nil {
		return nil, err
	}
	emit("portfolio.status.changed", map[string]any{
		"id": pf.ID, "status": "paused", "reason": reason,
	})
	if entryID, jerr := dbInsertJournal(ctx.AppDB(), pid, pf.ID, "alert",
		"Portfolio paused by agent. Reason: "+reason,
		map[string]any{"action": "pause", "reason": reason}); jerr == nil {
		emit("journal.appended", map[string]any{
			"id": entryID, "portfolio_id": pf.ID, "kind": "alert",
			"body": "Portfolio paused by agent. Reason: " + reason,
		})
	}
	return map[string]any{"status": "paused"}, nil
}

func (a *App) toolPortfolioArmLive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "portfolio_id", 0)
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("portfolio %d not found", id)
	}
	armed, _ := args["armed"].(bool)
	if armed && strings.TrimSpace(strArg(args, "confirmation")) != "LIVE MONEY" {
		return rejectStruct("confirmation_required", "arming broker_live requires confirmation exactly LIVE MONEY"), nil
	}
	if normalizeExecutionEnvironment(pf.ExecutionEnvironment, pf.Mode, pf.BrokerSlug) != "broker_live" {
		return rejectStruct("not_broker_live", "only broker_live portfolios can be armed"), nil
	}
	if err := dbSetPortfolioLiveArmed(ctx.AppDB(), pid, id, armed); err != nil {
		return nil, err
	}
	emit("portfolio.live_armed.changed", map[string]any{
		"portfolio_id": id, "execution_environment": "broker_live", "live_armed": armed,
	})
	return map[string]any{"portfolio_id": id, "execution_environment": "broker_live", "live_armed": armed}, nil
}

// ─── Helpers ───────────────────────────────────────────────────────

func rejectStruct(code, detail string) map[string]any {
	return map[string]any{"status": "rejected", "code": code, "detail": detail}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
