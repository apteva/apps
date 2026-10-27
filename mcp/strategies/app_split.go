package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var tradingObservationMu sync.Mutex
var legacyImportMu sync.Mutex

func isResearchTool(name string) bool {
	return strings.HasPrefix(name, "strategy_") || strings.HasPrefix(name, "backtest_") || strings.HasPrefix(name, "validation_") || strings.HasPrefix(name, "agent_backtest_") || name == "market_data_import"
}
func isTradingRead(name string) bool {
	switch name {
	case "portfolio_list", "portfolio_get", "account_summary", "positions_list", "orders_list", "market_quote", "market_history", "market_source", "journal_read":
		return true
	}
	return false
}

func callTrading(ctx *sdk.AppCtx, name string, args map[string]any, out any) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("Trading integration is unavailable")
	}
	return ctx.PlatformAPI().CallAppResult("trading", name, args, out)
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("Strategies requires a database")
	}
	ctx.AppDB().SetMaxOpenConns(1)
	globalCtx = ctx
	globalEngine = &engine{db: ctx.AppDB(), provider: &tradingProvider{ctx: ctx}, logger: ctx.Logger(), platform: ctx.PlatformAPI()}
	if _, err := ctx.AppDB().Exec(`UPDATE backtest_runs SET status='paused',error='Process restarted; explicitly resume from checkpoint' WHERE status='running'`); err != nil {
		return err
	}
	if _, err := ctx.AppDB().Exec(`UPDATE validation_suites SET status='paused',error='Process restarted; explicitly resume validation' WHERE status='running'`); err != nil {
		return err
	}
	validationWorkers.Lock()
	validationWorkers.stopping = false
	validationWorkers.Unlock()
	return nil
}

func (a *App) Workers() []sdk.Worker {
	tickEvery := "@every 5s"
	if globalCtx != nil {
		if n, err := strconv.Atoi(globalCtx.Config().Get("tick_seconds")); err == nil && n > 0 {
			tickEvery = fmt.Sprintf("@every %ds", n)
		}
	}
	return []sdk.Worker{{Name: "strategy_tick", Schedule: tickEvery, Run: func(call context.Context, ctx *sdk.AppCtx) error {
		if sdk.CurrentEnvironmentID() != "" {
			return nil
		}
		assignments, err := dbActiveStrategyAssignments(ctx.AppDB())
		if err != nil {
			return err
		}

		for _, assignment := range assignments {
			if err := syncTradingPortfolio(ctx, assignment.ProjectID, assignment.PortfolioID); err != nil {
				ctx.Logger().Warn("Trading observation unavailable", "portfolio_id", assignment.PortfolioID, "err", err)
				_, _ = ctx.AppDB().Exec(`UPDATE portfolios SET status='paused' WHERE id=?`, assignment.PortfolioID)
			}
		}
		evaluateLiveStrategyAssignments(globalEngine, ctx, time.Now().UTC())
		return nil
	}}}
}

func (a *App) MCPTools() []sdk.Tool {
	out := []sdk.Tool{}
	for _, tool := range a.researchTools() {
		if !isResearchTool(tool.Name) && !isTradingRead(tool.Name) {
			continue
		}
		if tool.Name == "backtest_market_step" {
			continue
		}
		original := tool
		name := tool.Name
		tool.Handler = nil
		tool.HandlerCtx = func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			cloned := map[string]any{}
			for k, v := range args {
				cloned[k] = v
			}
			if caller := sdk.CallerFrom(call); caller != nil && caller.ProjectID != "" {
				cloned["_project_id"] = caller.ProjectID
			}
			if out, handled, err := replayAgentTool(ctx, name, cloned); handled {
				return out, err
			}
			if name == "backtest_agent_exchange" {
				return original.HandlerCtx(call, ctx, cloned)
			}
			if isTradingRead(name) {
				var out any
				err := callTrading(ctx, name, cloned, &out)
				return out, err
			}
			pid, err := resolveProjectFromArgs(cloned)
			if err != nil {
				return nil, err
			}
			if id := int64Arg(cloned, "portfolio_id", 0); id > 0 {
				if err = syncTradingPortfolio(ctx, pid, id); err != nil {
					return nil, err
				}
			}
			if original.HandlerCtx != nil {
				return original.HandlerCtx(call, ctx, cloned)
			}
			return original.Handler(ctx, cloned)
		}
		out = append(out, tool)
	}
	out = append(out, sdk.Tool{Name: "legacy_import", Description: "Import legacy research from Trading into an empty Strategies app; assignments import paused.", InputSchema: schemaObject(nil, nil), HandlerCtx: func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
		if c := sdk.CallerFrom(call); c != nil && c.ProjectID != "" {
			args["_project_id"] = c.ProjectID
		}
		return a.importLegacyResearch(ctx, args)
	}})
	for _, name := range []string{"replay_order_place", "replay_order_cancel"} {
		name := name
		out = append(out, sdk.Tool{Name: name, Description: "Stage an order intent in an isolated agent replay; unavailable outside replay.", InputSchema: schemaObject(map[string]any{"portfolio_id": map[string]any{"type": "integer"}, "symbol": map[string]any{"type": "string"}, "side": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"}, "qty": map[string]any{"type": "number"}, "limit_price": map[string]any{"type": "number"}, "stop_price": map[string]any{"type": "number"}, "tif": map[string]any{"type": "string"}, "rationale": map[string]any{"type": "string"}, "expires_at": map[string]any{"type": "string"}, "order_id": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"}}, nil), HandlerCtx: func(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			result, handled, err := replayAgentTool(ctx, strings.TrimPrefix(name, "replay_"), args)
			if !handled {
				return nil, errors.New("active agent replay required")
			}
			return result, err
		}})
	}
	return out
}

func (a *App) toolOrderPlace(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	strategyID := int64Arg(args, "strategy_id", 0)
	portfolioID := int64Arg(args, "portfolio_id", 0)
	if strategyID <= 0 {
		return nil, errors.New("automated order requires a strategy")
	}

	if err = syncTradingPortfolio(ctx, pid, portfolioID); err != nil {
		return nil, err
	}
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, portfolioID)
	if err != nil {
		return nil, err
	}
	if allowed, why := scorecardAllowsExecution(ctx.AppDB(), pf, strategyID); !allowed {
		return nil, fmt.Errorf("strategy scorecard gate: %s", why)
	}
	strategy, err := dbGetStrategy(ctx.AppDB(), pid, strategyID)
	if err != nil {
		return nil, err
	}
	forwarded := map[string]any{}
	for _, key := range []string{"_project_id", "portfolio_id", "symbol", "side", "outcome", "type", "qty", "limit_price", "stop_price", "tif", "rationale", "idempotency_key"} {
		if v, ok := args[key]; ok {
			forwarded[key] = v
		}
	}
	forwarded["external_strategy_id"] = strategy.ID
	forwarded["external_strategy_version"] = strategy.Version
	forwarded["decision_ref"] = fmt.Sprintf("assignment:%d:%s", int64Arg(args, "assignment_id", 0), strArg(args, "idempotency_key"))
	assignmentID := int64Arg(args, "assignment_id", 0)
	signal := strArg(args, "signal_at")
	if signal == "" {
		_ = ctx.AppDB().QueryRow(`SELECT signal_bar_at FROM strategy_run_events WHERE assignment_id=? ORDER BY signal_bar_at DESC LIMIT 1`, assignmentID).Scan(&signal)
	}
	if signal == "" {
		signal = "manual"
	}
	key := strArg(forwarded, "idempotency_key")
	if key == "" {
		key = fmt.Sprintf("portfolio:%d:assignment:%d:strategy:%d:v%d:signal:%s:%s:%s", portfolioID, assignmentID, strategy.ID, strategy.Version, signal, strArg(forwarded, "symbol"), strArg(forwarded, "side"))
	}
	forwarded["idempotency_key"] = key
	forwarded["decision_ref"] = key
	raw, err := json.Marshal(forwarded)
	if err != nil {
		return nil, err
	}
	if _, err = ctx.AppDB().Exec(`INSERT INTO strategy_order_intents(request_key,project_id,portfolio_id,request_json) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, key, pid, portfolioID, string(raw)); err != nil {
		return nil, err
	}
	var stored string
	if err = ctx.AppDB().QueryRow(`SELECT request_json FROM strategy_order_intents WHERE request_key=? AND project_id=? AND portfolio_id=?`, key, pid, portfolioID).Scan(&stored); err != nil {
		return nil, err
	}
	forwarded = map[string]any{}
	if err = json.Unmarshal([]byte(stored), &forwarded); err != nil {
		return nil, err
	}
	var out map[string]any
	if err = callTrading(ctx, "order_place", forwarded, &out); err != nil {
		return nil, err
	}

	if fmt.Sprint(out["status"]) != "rejected" {
		if err = syncTradingPortfolio(ctx, pid, portfolioID); err != nil {
			return nil, fmt.Errorf("order submitted; refresh Trading observation before retry: %w", err)
		}
	}
	return out, nil
}

func syncTradingPortfolio(ctx *sdk.AppCtx, pid string, id int64) error {
	tradingObservationMu.Lock()
	defer tradingObservationMu.Unlock()
	var snap researchSnapshot
	if err := callTrading(ctx, "portfolio_research_snapshot", map[string]any{"_project_id": pid, "portfolio_id": id}, &snap); err != nil {
		return err
	}
	if snap.Version != 1 || snap.ProjectID != pid || len(snap.Tables["portfolios"]) != 1 {
		return errors.New("invalid Trading portfolio observation")
	}
	pf := snap.Tables["portfolios"][0]
	if int64Arg(pf, "id", 0) != id || strArg(pf, "project_id") != pid {
		return errors.New("Trading observation belongs to a different portfolio")
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var source int64
	err = tx.QueryRow(`SELECT source_install_id FROM trading_observation_sources WHERE project_id=?`, pid).Scan(&source)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && source != snap.SourceInstallID {
		return errors.New("Trading binding changed; use a new Strategies installation to preserve research portfolio identities")
	}
	if _, err = tx.Exec(`INSERT INTO trading_observation_sources(project_id,source_install_id) VALUES(?,?) ON CONFLICT DO NOTHING`, pid, snap.SourceInstallID); err != nil {
		return err
	}
	var legacyImported int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM research_imports WHERE project_id=? AND source_install_id=?`, pid, snap.SourceInstallID).Scan(&legacyImported); err != nil {
		return err
	}

	tables := []string{"portfolios", "securities", "security_identifiers", "security_listings", "exchange_sessions", "universe_memberships", "marks", "instruments", "venue_execution_profiles", "portfolio_risk_policies", "portfolio_risk_state", "portfolio_universe_policies", "positions", "orders", "fills", "execution_costs", "position_accounting", "watchlist", "day_baselines"}
	for _, table := range tables {
		records := snap.Tables[table]
		if table == "positions" || table == "watchlist" || table == "day_baselines" {
			if _, err = tx.Exec(`DELETE FROM "`+table+`" WHERE portfolio_id=?`, id); err != nil {
				return err
			}
		}
		if table == "orders" {
			for _, order := range records {

				localInstall, _ := strconv.ParseInt(os.Getenv("APTEVA_INSTALL_ID"), 10, 64)
				oldStrategy := order["strategy_id"]
				order["strategy_id"] = 0
				oldVersion := order["strategy_version"]
				order["strategy_version"] = 0
				if legacyImported > 0 && strArg(order, "origin_app") == "" {
					order["strategy_id"] = oldStrategy
					order["strategy_version"] = oldVersion
				}
				if strArg(order, "origin_app") == "strategies" && int64Arg(order, "origin_install_id", 0) == localInstall {
					order["strategy_id"] = order["external_strategy_id"]
					order["strategy_version"] = order["external_strategy_version"]
				}
			}
		}
		if err = writeResearchRows(tx, table, records); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return dbRebuildStrategyAttribution(ctx.AppDB())
}

func (a *App) HTTPRoutes() []sdk.Route {

	return []sdk.Route{
		{Pattern: "/portfolios", Handler: a.handleResearchPortfolios},
		{Pattern: "/portfolios/", Handler: a.handleResearchPortfolio},
		{Pattern: "/strategies", Handler: a.observeResearchHTTP(a.handleHTTPStrategies)},
		{Pattern: "/strategies/", Handler: a.observeResearchHTTP(a.handleHTTPStrategies)},
		{Pattern: "/backtests", Handler: a.observeResearchHTTP(a.handleHTTPBacktests)},
		{Pattern: "/backtests/", Handler: a.observeResearchHTTP(a.handleHTTPBacktests)},
		{Pattern: "/validations", Handler: a.observeResearchHTTP(a.handleHTTPValidation)},
		{Pattern: "/validations/", Handler: a.observeResearchHTTP(a.handleHTTPValidation)},
		{Pattern: "/legacy/import", Handler: func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				httpErr(w, 405, "POST only")
				return
			}
			pid, err := resolveProjectFromRequest(r)
			if err != nil {
				httpErr(w, 400, err.Error())
				return
			}
			out, err := a.importLegacyResearch(globalCtx, map[string]any{"_project_id": pid})
			if err != nil {
				httpErr(w, 400, err.Error())
				return
			}
			httpJSON(w, 200, out)
		}},
	}
}

func (a *App) handleResearchPortfolios(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "Manage portfolios in Trading")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	var out any
	if err = callTrading(globalCtx, "portfolio_list", map[string]any{"_project_id": pid}, &out); err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	httpJSON(w, 200, out)
}
func (a *App) handleResearchPortfolio(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/portfolios/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, 400, "portfolio id required")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if len(parts) != 2 || parts[1] != "backtests" {
		httpErr(w, 404, "Use Trading for portfolio operations")
		return
	}
	if err = syncTradingPortfolio(globalCtx, pid, id); err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	pf, err := dbGetPortfolio(globalCtx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	a.handleHTTPPortfolioBacktests(w, r, pf, pid)
}

func (a *App) importLegacyResearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	legacyImportMu.Lock()
	defer legacyImportMu.Unlock()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	var snap researchSnapshot
	if err = callTrading(ctx, "research_legacy_export", map[string]any{"_project_id": pid}, &snap); err != nil {
		return nil, err
	}
	if snap.Version != 1 || snap.ProjectID != pid {
		return nil, errors.New("invalid legacy research export")
	}
	var existing int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM research_imports WHERE project_id=? AND source_install_id=?`, pid, snap.SourceInstallID).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return map[string]any{"status": "already_imported"}, nil
	}
	if err = ctx.AppDB().QueryRow(`SELECT (SELECT COUNT(*) FROM strategies)+(SELECT COUNT(*) FROM backtest_runs)+(SELECT COUNT(*) FROM validation_suites)`).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, errors.New("legacy import requires an empty Strategies app; existing research will not be overwritten")
	}
	portfolios := map[int64]bool{}
	for _, table := range []string{"portfolio_strategy_assignments", "backtest_runs", "strategy_scorecard_policies", "strategy_scorecard_evaluations"} {
		for _, row := range snap.Tables[table] {
			if id := int64Arg(row, "portfolio_id", 0); id > 0 {
				portfolios[id] = true
			}
		}
	}
	for id := range portfolios {
		if err = syncTradingPortfolio(ctx, pid, id); err != nil {
			return nil, err
		}
	}
	tradingObservationMu.Lock()
	defer tradingObservationMu.Unlock()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM strategies)+(SELECT COUNT(*) FROM backtest_runs)+(SELECT COUNT(*) FROM validation_suites)`).Scan(&count); err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, errors.New("research was created while importing; legacy import did not overwrite it")
	}

	for _, table := range []string{"strategies", "strategy_versions", "portfolio_strategy_assignments", "strategy_scorecard_policies", "backtest_runs", "backtest_events", "backtest_snapshots", "backtest_market_bars", "backtest_simulations", "backtest_simulation_outputs", "backtest_agent_decisions", "strategy_scorecard_evaluations", "strategy_run_events", "strategy_rebalances", "validation_suites", "validation_cases"} {
		records := snap.Tables[table]
		for _, row := range records {
			if table == "portfolio_strategy_assignments" {
				row["status"] = "paused"
			}
			if (table == "backtest_runs" || table == "validation_suites") && strArg(row, "status") == "running" {
				row["status"] = "paused"
			}
		}
		if err = writeResearchRows(tx, table, records); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(`INSERT INTO research_imports(source_install_id,project_id)VALUES(?,?)`, snap.SourceInstallID, pid); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"status": "imported", "strategies": len(snap.Tables["strategies"]), "backtests": len(snap.Tables["backtest_runs"]), "assignments": "paused"}, nil
}

// Strategies has no direct market-data credentials or network provider.
type tradingProvider struct{ ctx *sdk.AppCtx }

func (p *tradingProvider) Quote(symbol string) (*Mark, error) {
	var mark Mark
	if err := callTrading(p.ctx, "market_quote", map[string]any{"symbol": symbol}, &mark); err != nil {
		return nil, err
	}
	return &mark, nil
}
func (p *tradingProvider) Universe() []*Mark { return nil }
func (p *tradingProvider) Bars(symbol, rng string) ([]Bar, error) {
	var out struct {
		Bars []Bar `json:"bars"`
	}
	err := callTrading(p.ctx, "market_history", map[string]any{"symbol": symbol, "range": rng}, &out)
	return out.Bars, err
}
func (p *tradingProvider) StrategyBars(symbol, interval string, limit int) ([]Bar, error) {
	var out struct {
		Bars []Bar `json:"bars"`
	}
	err := callTrading(p.ctx, "market_bars_capture", map[string]any{"symbols": []string{symbol}, "interval": interval, "limit": limit, "latest": true}, &out)
	return out.Bars, err
}

func (a *App) observeResearchHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid, err := resolveProjectFromRequest(r)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		id, _ := strconv.ParseInt(r.URL.Query().Get("portfolio_id"), 10, 64)
		if r.Method != "GET" && r.Body != nil {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<20))
			if err != nil {
				httpErr(w, 400, err.Error())
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var body map[string]any
			if json.Unmarshal(raw, &body) == nil {
				if n := int64Arg(body, "portfolio_id", 0); n > 0 {
					id = n
				}
			}
		}
		if id > 0 {
			if err = syncTradingPortfolio(globalCtx, pid, id); err != nil {
				httpErr(w, 502, err.Error())
				return
			}
		}
		if r.URL.Path == "/strategies/live" {
			observed, err := dbListPortfolios(globalCtx.AppDB(), pid)
			if err != nil {
				httpErr(w, 500, err.Error())
				return
			}
			for _, portfolio := range observed {
				if err = syncTradingPortfolio(globalCtx, pid, portfolio.ID); err != nil {
					httpErr(w, 502, err.Error())
					return
				}
			}
		}
		next(w, r)
	}
}
