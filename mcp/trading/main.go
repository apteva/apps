// Trading v0.1 — paper-only sidecar.
//
// Multi-portfolio, multi-asset (equity / crypto / polymarket).
// Deterministic paper-execution engine — no broker, no real money.
//
// Routes the dashboard panel + the kiosk SPA call:
//
//   GET  /portfolios                          — list (project-scoped)
//   GET  /portfolios/{id}                     — full snapshot
//   GET  /portfolios/{id}/positions
//   GET  /portfolios/{id}/orders?status=…
//   GET  /portfolios/{id}/journal?kind=…
//   GET  /quotes/{symbol}
//   POST /portfolios/{id}/orders              — body { side, type, qty, … rationale }
//   POST /portfolios/{id}/orders/{oid}/cancel
//
// MCP surface lives in tools.go; engine in exec.go; storage in store.go.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest ──────────────────────────────────────────────────────
//
// Embedded so the running binary is self-describing for CLI introspection,
// and so manifest_test.go can reach it without re-reading apteva.yaml.

const manifestYAML = `schema: apteva-app/v1
name: trading
display_name: Trading
version: 0.4.8
description: Trading desk for Apteva agents (paper + live via per-portfolio broker integration).
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.instances.read
    - net.egress
    - platform.connections.execute
    - platform.connections.read
  integrations:
    - role: broker
      kind: integration
      required: false
      compatible_slugs: [binance-trading, alpaca-trading, polymarket-clob]
      capabilities:
        - order.place
        - order.cancel
        - order.status
        - account.summary
        - positions.list
      tools:
        order.place: create_order
        order.cancel: cancel_order
        order.status: get_order
        account.summary: get_account
        positions.list: list_positions
      label: "Broker (optional — enables live trading)"
    - role: market_data_equity
      kind: integration
      required: false
      compatible_slugs: [alpaca-market-data]
      capabilities:
        - quotes.equity
      tools:
        quotes.equity: stock_snapshots
      label: "Market Data — Equity (optional — real prices for live equity portfolios)"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: portfolio_create
      description: "Create a paper or live portfolio (live: pass broker_slug)."
    - name: brokers_list
      description: "List registered broker adapters + bound connections."
    - name: portfolio_list
      description: "List portfolios visible to this agent."
    - name: portfolio_get
      description: "Snapshot of one portfolio."
    - name: account_summary
      description: "Equity, cash, day and open P&L."
    - name: positions_list
      description: "Open positions for one portfolio."
    - name: orders_list
      description: "Working / filled / cancelled orders."
    - name: order_place
      description: "Place a paper order."
    - name: order_cancel
      description: "Cancel a working order."
    - name: market_quote
      description: "Latest mark for a symbol."
    - name: market_history
      description: "OHLCV bars or probability history."
    - name: market_source
      description: "Report the live data source per asset class."
    - name: watchlist_add
      description: "Track a symbol on a portfolio."
    - name: watchlist_remove
      description: "Stop tracking a symbol."
    - name: alert_create
      description: "Price / probability / PNL alert."
    - name: journal_write
      description: "Append a thesis or rationale to the journal."
    - name: journal_read
      description: "Read journal entries for a portfolio."
    - name: portfolio_pause
      description: "Pause a portfolio (no new orders)."
  ui_panels:
    - slot: project.page
      label: Trading
      icon: trending-up
      entry: /ui/TradingPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/trading
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/trading.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

// globalCtx — stashed here so HTTP handlers (which the SDK invokes
// without an AppCtx) can reach the DB. Same workaround crm + storage
// use; revisit if the SDK grows a request-scoped accessor.
var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("trading requires a db block")
	}
	// Cap the SQL connection pool at 1. The engine and the MCP /
	// REST handlers all write to the same SQLite file; with multiple
	// pooled connections, the bursty per-tick mark refresh and
	// concurrent agent tool calls fight for the WAL writer lock and
	// surface as SQLITE_BUSY. Single-connection serialises every
	// write through one queue. WAL still gives concurrent reads.
	ctx.AppDB().SetMaxOpenConns(1)
	globalCtx = ctx

	// Engine bootstrap — pricing provider, then the shared engine
	// pointer the workers read.
	provider := newProvider(ctx.Config().Get("pricing_provider"))
	// Live provider needs the platform client to pull equity/etf
	// snapshots from a bound alpaca-market-data connection. Wired
	// post-construction so newProvider stays a pure factory.
	if lp, ok := provider.(*liveProvider); ok {
		lp.SetPlatform(ctx.PlatformAPI(), ctx.Logger())
	}
	globalEngine = &engine{
		db:       ctx.AppDB(),
		provider: provider,
		logger:   ctx.Logger(),
		platform: ctx.PlatformAPI(),
	}

	// Prime marks so the first market_quote call doesn't 404.
	for _, m := range provider.Universe() {
		_ = dbUpsertMark(ctx.AppDB(), m)
	}

	ctx.Logger().Info("trading mounted",
		"project_id", os.Getenv("APTEVA_PROJECT_ID"),
		"pricing_provider", ctx.Config().Get("pricing_provider"))

	// Auto-fill on first install. Idempotent — guards on "zero
	// portfolios in this project". Never fatal.
	if err := bootstrapIfEmpty(ctx); err != nil {
		ctx.Logger().Warn("bootstrap failed", "err", err)
	}
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error { return nil }
func (a *App) Channels() []sdk.ChannelFactory   { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// Workers — the paper-execution engine. The SDK supervises both;
// failures inside Run get logged but don't kill the sidecar.
func (a *App) Workers() []sdk.Worker {
	tickEvery := "@every 5s"
	if v := globalCtx.Config().Get("tick_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tickEvery = fmt.Sprintf("@every %ds", n)
		}
	}
	return []sdk.Worker{
		{Name: "mark_tick",  Schedule: tickEvery,    Run: markTick},
		{Name: "alert_tick", Schedule: "@every 60s", Run: alertTick},
	}
}

// newProvider picks the pricing path. v0.2 adds "live" which composes
// public Binance + Polymarket gamma-api with a mock fallback for
// equity/etf and on errors. "mock" stays for tests + offline demos.
func newProvider(name string) Provider {
	mock := newMockProvider()
	switch name {
	case "live":
		return newLiveProvider(mock)
	case "", "mock":
		return mock
	}
	if globalCtx != nil {
		globalCtx.Logger().Warn("unknown pricing_provider, falling back to mock", "requested", name)
	}
	return mock
}

// ─── HTTP routes — REST mirror for both UI surfaces ────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/portfolios",   Handler: a.handleHTTPPortfoliosCollection},
		{Pattern: "/portfolios/",  Handler: a.handleHTTPPortfolioItem},
		{Pattern: "/quotes/",      Handler: a.handleHTTPQuote},
		{Pattern: "/history/",     Handler: a.handleHTTPHistory},
		{Pattern: "/universe",     Handler: a.handleHTTPUniverse},
		{Pattern: "/brokers",      Handler: a.handleHTTPBrokers},
		{Pattern: "/healthz/details", Handler: a.handleHTTPHealthDetails},
	}
}

// /portfolios — GET list, POST create.
func (a *App) handleHTTPPortfoliosCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.httpListPortfolios(w, r)
	case http.MethodPost:
		a.httpCreatePortfolio(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// /portfolios/{id}[/<sub>] — many sub-paths, one handler.
func (a *App) handleHTTPPortfolioItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/portfolios/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		httpErr(w, http.StatusBadRequest, "portfolio id required")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "portfolio id must be integer")
		return
	}
	sub := ""
	if len(parts) > 1 { sub = parts[1] }
	subID := ""
	if len(parts) > 2 { subID = parts[2] }
	action := ""
	if len(parts) > 3 { action = parts[3] }

	pid, err := resolveProjectFromRequest(r)
	if err != nil { httpErr(w, http.StatusBadRequest, err.Error()); return }
	pf, err := dbGetPortfolio(globalCtx.AppDB(), pid, id)
	if err != nil { httpErr(w, http.StatusNotFound, fmt.Sprintf("portfolio %d not found", id)); return }

	switch {
	case sub == "" && r.Method == http.MethodGet:
		snap, _ := snapshotPortfolio(globalCtx.AppDB(), pf)
		httpJSON(w, 200, map[string]any{"portfolio": snap})

	case sub == "" && r.Method == http.MethodPatch:
		// limited patch — status only (operator action, e.g. resume)
		var body struct{ Status string `json:"status"` }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Status != "active" && body.Status != "paused" && body.Status != "halted" {
			httpErr(w, http.StatusBadRequest, "status must be active|paused|halted"); return
		}
		_ = dbSetPortfolioStatus(globalCtx.AppDB(), pf.ID, body.Status)
		httpJSON(w, 200, map[string]any{"status": body.Status})

	case sub == "positions" && r.Method == http.MethodGet:
		pos, _ := dbListPositions(globalCtx.AppDB(), pf.ID)
		snap, _ := snapshotPortfolio(globalCtx.AppDB(), pf)
		// Re-decorate from snapshot's mark pass.
		for _, q := range pos {
			mark, _ := dbGetMark(globalCtx.AppDB(), q.Symbol)
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
		}
		httpJSON(w, 200, map[string]any{"positions": pos})

	case sub == "orders" && r.Method == http.MethodGet:
		status := r.URL.Query().Get("status")
		if status == "" { status = "all" }
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := dbListOrders(globalCtx.AppDB(), pf.ID, status, limit)
		if err != nil { httpErr(w, 500, err.Error()); return }
		if rows == nil { rows = []*Order{} }
		httpJSON(w, 200, map[string]any{"orders": rows})

	case sub == "orders" && r.Method == http.MethodPost:
		// Place an order via REST — mirrors order_place. Used by the
		// desk UI's "Place" button. Same pre-trade pipeline as the MCP
		// path; we forward to the tool handler for one source of truth.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, "invalid json"); return
		}
		body["portfolio_id"] = float64(pf.ID)
		// Project resolution from the request, not args (env wins for project scope).
		_ = pid
		body["_project_id"] = pid
		body["source_override"] = "human"
		out, err := a.toolOrderPlace(globalCtx, body)
		if err != nil { httpErr(w, 400, err.Error()); return }
		httpJSON(w, 200, out)

	case sub == "journal" && r.Method == http.MethodGet:
		kind := r.URL.Query().Get("kind")
		since := r.URL.Query().Get("since")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		entries, err := dbReadJournal(globalCtx.AppDB(), pf.ID, kind, since, limit)
		if err != nil { httpErr(w, 500, err.Error()); return }
		if entries == nil { entries = []*JournalEntry{} }
		httpJSON(w, 200, map[string]any{"entries": entries})

	// POST /portfolios/{id}/orders/{oid}/cancel — UI cancel button.
	// Mirrors the order_cancel MCP tool; same broker-fan-out logic.
	case sub == "orders" && subID != "" && action == "cancel" && r.Method == http.MethodPost:
		reason := r.URL.Query().Get("reason")
		out, err := a.toolOrderCancel(globalCtx, map[string]any{
			"_project_id": pid, "order_id": subID, "reason": reason,
		})
		if err != nil { httpErr(w, 400, err.Error()); return }
		httpJSON(w, 200, out)

	// Watchlist — add via POST {symbol}, remove via DELETE ?symbol=X.
	case sub == "watchlist" && r.Method == http.MethodPost:
		var body struct{ Symbol string `json:"symbol"` }
		_ = json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolWatchlistAdd(globalCtx, map[string]any{
			"_project_id": pid, "portfolio_id": float64(pf.ID), "symbol": body.Symbol,
		})
		if err != nil { httpErr(w, 400, err.Error()); return }
		httpJSON(w, 200, out)

	case sub == "watchlist" && r.Method == http.MethodDelete:
		sym := r.URL.Query().Get("symbol")
		out, err := a.toolWatchlistRemove(globalCtx, map[string]any{
			"_project_id": pid, "portfolio_id": float64(pf.ID), "symbol": sym,
		})
		if err != nil { httpErr(w, 400, err.Error()); return }
		httpJSON(w, 200, out)

	default:
		httpErr(w, http.StatusNotFound, "no such route")
	}
}

// handleHTTPBrokers — list every registered broker adapter + its
// currently-bound connections. Mirrors the brokers_list MCP tool so the
// panel's Brokers tab + portfolio_create's broker_slug picker don't
// have to call MCP from the browser.
func (a *App) handleHTTPBrokers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { httpErr(w, 405, "GET only"); return }
	out, err := a.toolBrokersList(globalCtx, map[string]any{})
	if err != nil { httpErr(w, 500, err.Error()); return }
	httpJSON(w, 200, out)
}

func (a *App) httpListPortfolios(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil { httpErr(w, 400, err.Error()); return }
	pfs, err := dbListPortfolios(globalCtx.AppDB(), pid)
	if err != nil { httpErr(w, 500, err.Error()); return }
	out := make([]map[string]any, 0, len(pfs))
	for _, p := range pfs {
		snap, _ := snapshotPortfolio(globalCtx.AppDB(), p)
		out = append(out, map[string]any{
			"id": snap.ID, "name": snap.Name, "agent_id": snap.AgentID, "mandate": snap.Mandate,
			"allowed_classes": snap.AllowedClasses, "status": snap.Status, "mode": snap.Mode,
			"equity": snap.Equity, "cash": snap.Cash,
			"day_pnl": snap.DayPnL, "day_pnl_pct": snap.DayPnLPct,
			"open_pnl": snap.OpenPnL, "open_pnl_pct": snap.OpenPnLPct,
			"watchlist": snap.Watchlist, "buying_power": snap.BuyingPower,
		})
	}
	httpJSON(w, 200, map[string]any{"portfolios": out})
}

func (a *App) httpCreatePortfolio(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil { httpErr(w, 400, err.Error()); return }
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, 400, "invalid json"); return
	}
	if name, _ := body["name"].(string); name == "" {
		httpErr(w, 400, "name required"); return
	}
	// Route through the MCP tool handler so HTTP + agent paths share
	// the full pre-trade pipeline: broker validation for live mode,
	// allowed_classes ↔ adapter capability check, holdings seeding,
	// emit events. Paper mode falls through to the same code path
	// dbCreatePortfolio used to handle directly.
	body["_project_id"] = pid
	body["source_override"] = "human"
	out, err := a.toolPortfolioCreate(globalCtx, body)
	if err != nil { httpErr(w, 500, err.Error()); return }
	httpJSON(w, 201, out)
}

func (a *App) handleHTTPQuote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { httpErr(w, 405, "GET only"); return }
	symbol := strings.TrimPrefix(r.URL.Path, "/quotes/")
	if symbol == "" { httpErr(w, 400, "symbol required"); return }
	out, err := (&App{}).toolMarketQuote(globalCtx, map[string]any{"symbol": symbol})
	if err != nil { httpErr(w, 404, err.Error()); return }
	httpJSON(w, 200, out)
}

// handleHTTPHistory — OHLCV bars (or polymarket YES probability) for a
// symbol. Wraps the market_history MCP tool. UI uses this for the Trade
// tab's price chart and the Positions tab's sparklines.
func (a *App) handleHTTPHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { httpErr(w, 405, "GET only"); return }
	symbol := strings.TrimPrefix(r.URL.Path, "/history/")
	if symbol == "" { httpErr(w, 400, "symbol required"); return }
	rng := r.URL.Query().Get("range")
	if rng == "" { rng = "1D" }
	out, err := (&App{}).toolMarketHistory(globalCtx, map[string]any{
		"symbol": symbol, "range": rng,
	})
	if err != nil { httpErr(w, 404, err.Error()); return }
	httpJSON(w, 200, out)
}

func (a *App) handleHTTPUniverse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { httpErr(w, 405, "GET only"); return }
	if globalEngine == nil { httpErr(w, 503, "engine warming"); return }
	httpJSON(w, 200, map[string]any{"symbols": globalEngine.provider.Universe()})
}

func (a *App) handleHTTPHealthDetails(w http.ResponseWriter, r *http.Request) {
	if globalEngine == nil {
		httpJSON(w, 503, map[string]any{"error": "engine not initialised"})
		return
	}
	out := globalEngine.snapshotMetrics()
	out["providers"] = providerHealthSnapshot()
	httpJSON(w, 200, out)
}

// providerHealthSnapshot — read the live provider's per-class health
// when present; otherwise return a minimal "all-mock" view so the UI
// always gets a consistent shape.
func providerHealthSnapshot() map[string]any {
	if lp, ok := globalEngine.provider.(*liveProvider); ok {
		return lp.Health()
	}
	return map[string]any{
		"crypto":     map[string]any{"name": "mock", "errors_60s": 0, "stale": false},
		"polymarket": map[string]any{"name": "mock", "errors_60s": 0, "stale": false},
		"equity":     map[string]any{"name": "mock", "errors_60s": 0, "stale": false},
		"etf":        map[string]any{"name": "mock", "errors_60s": 0, "stale": false},
	}
}

// ─── Project resolution + arg helpers ──────────────────────────────

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

// intArg / int64Arg / floatArg also accept *string* inputs because the
// LLM behind opencode-go (Kimi K2.6) routinely emits JSON like
// {"portfolio_id": "1"} — sending integers as strings — even when the
// JSON-Schema declares the param as integer/number. Without these
// fallbacks every numeric arg silently became its zero value, which
// surfaced as "portfolio 0 not found" rejections; the agent then
// retried forever. Tier 1 + Tier 2 tests now cover this path.

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func int64Arg(args map[string]any, key string, def int64) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return def
}

func floatArg(args map[string]any, key string, def float64) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return n
		}
	}
	return def
}

func strArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// emit — fire-and-forget app-event publish. Goes through the SDK's
// httpEmitter to apteva-server, which fans out via SSE to any
// /api/app-events/trading subscribers (panel, desk SPA). Best-effort:
// errors are swallowed inside ctx.Emit, the app's DB stays the source
// of truth, and a UI reconnect with `since=` replays missed events.
//
// Call from anywhere — agent tool handlers (request-scoped), engine
// goroutines (background), HTTP handlers. Safe with no subscribers.
func emit(topic string, data any) {
	if globalCtx == nil {
		return
	}
	globalCtx.Emit(topic, data)
}

// schemaObject — JSON-schema {type:object, properties, required}.
// Same helper crm/storage have; will eventually move into the SDK.
func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		return map[string]any{"type": "object"}
	}
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// ─── HTTP helpers ──────────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func httpErr(w http.ResponseWriter, status int, msg string) {
	httpJSON(w, status, map[string]any{"error": msg})
}

// ─── main ──────────────────────────────────────────────────────────

func main() { sdk.Run(&App{}) }

// Compile-time consistency: silences "imported and not used" on context
// when builds drop the engine path.
var _ = context.Background
