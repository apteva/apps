// stocks — explore stocks, backed by Yahoo Finance.
//
// Read-only research surface: list/filter a universe of tickers, open one
// stock for its quote + key data, price chart, and dividend history. It
// never places orders and holds no portfolio/ledger state — that belongs
// to the trading and finance apps. Designed so finance can call it for
// in-context enrichment (finance → stocks, one-directional) and so a
// future market-intel equity domain can register it as a data source.
//
// All data comes from Yahoo's public /chart endpoint (see yahoo.go); the
// local DB is a thin TTL cache plus a seeded ticker universe (store.go,
// migrations/). Stock data is universal, so nothing here is
// project-scoped.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"golang.org/x/sync/singleflight"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: stocks
display_name: Stocks
version: 0.7.3
description: Explore + screen the S&P 1500 backed by Yahoo Finance — filter by yield/payout/P-E/dividend-growth, quote, price chart, dividend history. Read-only.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/stocks
tags: [finance, stocks, equities, dividends, research]
scopes: [project, global]
min_apteva_version: "0.10.0"
requires:
  permissions:
    - db.write.app
    - net.egress
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: search
      description: "Find stocks by symbol or name. Unknown-but-valid tickers are auto-added to the universe. Args: query, limit?."
    - name: list
      description: "Paginated stock screen by query, sector, yield, payout, P/E, growth, and market cap. Args: query?, filters?, sort?, limit? (max 200), offset?."
    - name: get
      description: "One stock's quote and fundamentals. Args: symbol, refresh?."
    - name: chart
      description: "OHLCV price history. Args: symbol, range? (1mo|6mo|1y|5y|max), interval? (1d|1wk|1mo), refresh?."
    - name: dividends
      description: "Dividend history + summary. Args: symbol, refresh?."
    - name: sync_status
      description: "Background stock-refresh progress, current batch progress, and fundamentals feed health."
    - name: watchlists_list
      description: "List saved watchlists (dynamic = rule-driven, manual = pinned)."
    - name: watchlist_save
      description: "Create/update a watchlist (rules and/or pinned symbols). Args: name, rules?, id?."
    - name: watchlist_delete
      description: "Delete a watchlist. Args: id."
    - name: watchlist_member
      description: "Add/remove a symbol on a watchlist. Args: id, symbol, state (include|exclude|auto)."
    - name: watchlist_get
      description: "Resolve a watchlist to paginated current members (rules ∪ pins − excludes). Args: id, sort?, limit?, offset?."
  ui_panels:
    - slot: project.page
      label: Stocks
      icon: line-chart
      entry: /ui/StocksPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/stocks
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/stocks.db
  migrations: migrations/
config_schema:
  - name: cache_ttl_seconds
    type: text
    default: "3600"
    label: Source cache TTL (seconds)
    description: How long a fetched quote/chart/dividend response is reused before re-fetching from Yahoo.
upgrade_policy: auto-patch
`

// App carries the runtime handles set up in OnMount. HTTP handlers are
// methods on *App, so they read these directly — no global ctx needed,
// since the SDK calls OnMount before collecting HTTPRoutes()/MCPTools()
// on the same instance.
type App struct {
	st  *store
	y   *yahooClient
	ttl time.Duration

	// warmMu/lastWarm gate the background warming worker so overlapping
	// dispatches don't stack Yahoo traffic. The remaining fields expose
	// live per-batch progress to the panel. See warmBatch.
	warmMu        sync.Mutex
	lastWarm      time.Time
	warmRunning   bool
	warmTotal     int
	warmCompleted int
	warmCancel    context.CancelFunc

	refreshGroup singleflight.Group
}

// globalCtx is stashed in OnMount so HTTP handlers (which the SDK invokes
// without an AppCtx) can resolve project context for watchlist ops. On
// project-scoped installs it carries the pinned project; on global installs
// the panel passes ?project_id and resolveProject reads it from args.
var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("stocks: invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("stocks requires a db block")
	}
	a.st = &store{db: ctx.AppDB()}
	a.y = newYahoo()
	a.ttl = time.Hour
	globalCtx = ctx
	if v := ctx.Config().Get("cache_ttl_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			a.ttl = time.Duration(n) * time.Second
		}
	}
	ctx.Logger().Info("stocks mounted", "cache_ttl", a.ttl.String())
	_ = a.st.cachePrune(cacheRetention, cacheMaxRows)
	// Kick an immediate first warm so a fresh install shows prices/yields
	// without waiting a full worker interval; the worker keeps it fresh.
	warmCtx, warmCancel := context.WithCancel(context.Background())
	a.warmCancel = warmCancel
	go a.warmBatch(warmCtx)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.warmCancel != nil {
		a.warmCancel()
	}
	if a.y != nil {
		a.y.close()
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory { return nil }

// Workers — one background warmer that refreshes a paced batch of the
// stalest universe symbols each tick (price/yield/growth + P/E/payout),
// staying under Yahoo's rate ceiling. The list tool reads the snapshot
// this maintains rather than fetching inline.
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "warm-universe",
		Schedule: "@every 10m",
		Run: func(ctx context.Context, _ *sdk.AppCtx) error {
			a.warmBatch(ctx)
			return nil
		},
	}}
}

func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "search",
			Description: "Find stocks by symbol or name. Unknown-but-valid tickers are auto-added to the universe.",
			InputSchema: schemaObject(map[string]any{
				"query": map[string]any{"type": "string", "description": "Symbol or name fragment"},
				"limit": map[string]any{"type": "integer", "description": "Max results (default 20)"},
			}, []string{"query"}),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.toolSearch(args) },
		},
		{
			Name:        "list",
			Description: "List/filter/screen the S&P 1500 universe by dividend yield, payout ratio, P/E, and 5-year dividend growth.",
			InputSchema: schemaObject(map[string]any{
				"query":      map[string]any{"type": "string", "description": "Symbol prefix or company-name fragment"},
				"sector":     map[string]any{"type": "string", "description": "Filter by GICS sector (exact, case-insensitive)"},
				"min_yield":  map[string]any{"type": "number", "description": "Min dividend yield %"},
				"max_yield":  map[string]any{"type": "number", "description": "Max dividend yield %"},
				"min_payout": map[string]any{"type": "number", "description": "Min payout ratio %"},
				"max_payout": map[string]any{"type": "number", "description": "Max payout ratio %"},
				"min_pe":     map[string]any{"type": "number", "description": "Min trailing P/E"},
				"max_pe":     map[string]any{"type": "number", "description": "Max trailing P/E (> 0)"},
				"min_growth": map[string]any{"type": "number", "description": "Min 5yr dividend CAGR %"},
				"max_growth": map[string]any{"type": "number", "description": "Max 5yr dividend CAGR %"},
				"min_mcap":   map[string]any{"type": "number", "description": "Min market cap (billions)"},
				"max_mcap":   map[string]any{"type": "number", "description": "Max market cap (billions)"},
				"sort":       map[string]any{"type": "string", "enum": []string{"name", "price", "change", "yield", "pe", "payout", "growth"}, "description": "Sort key (default name)"},
				"limit":      map[string]any{"type": "integer", "description": "Page size (default 100, maximum 200)"},
				"offset":     map[string]any{"type": "integer", "description": "Zero-based page offset"},
			}, nil),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.toolList(args) },
		},
		{
			Name:        "get",
			Description: "One stock's data — quote, day + 52-week range, volume, dividend yield + frequency, last dividend.",
			InputSchema: schemaObject(map[string]any{
				"symbol":  map[string]any{"type": "string", "description": "Ticker, e.g. AAPL"},
				"refresh": map[string]any{"type": "boolean", "description": "Bypass the response cache"},
			}, []string{"symbol"}),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.toolGet(args) },
		},
		{
			Name:        "chart",
			Description: "OHLCV price history for a stock.",
			InputSchema: schemaObject(map[string]any{
				"symbol":   map[string]any{"type": "string", "description": "Ticker, e.g. AAPL"},
				"range":    map[string]any{"type": "string", "enum": []string{"1mo", "6mo", "1y", "5y", "max"}, "description": "History range (default 1y)"},
				"interval": map[string]any{"type": "string", "enum": []string{"1d", "1wk", "1mo"}, "description": "Bar interval (default 1d)"},
				"refresh":  map[string]any{"type": "boolean", "description": "Bypass the response cache"},
			}, []string{"symbol"}),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.toolChart(args) },
		},
		{
			Name:        "dividends",
			Description: "Dividend history + summary (trailing-12mo total, yield, frequency, growth) for a stock.",
			InputSchema: schemaObject(map[string]any{
				"symbol":  map[string]any{"type": "string", "description": "Ticker, e.g. AAPL"},
				"refresh": map[string]any{"type": "boolean", "description": "Bypass the response cache"},
			}, []string{"symbol"}),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.toolDividends(args) },
		},
		{
			Name:        "sync_status",
			Description: "Background stock-refresh progress, current batch progress, and fundamentals feed health.",
			InputSchema: schemaObject(nil, nil),
			Handler:     func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.toolSyncStatus(args) },
		},
		{
			Name:        "watchlists_list",
			Description: "List saved watchlists (dynamic = rule-driven, manual = pinned). Args: none.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolWatchlistsList,
		},
		{
			Name:        "watchlist_save",
			Description: "Create or update a watchlist. A watchlist = rules (a screen) and/or pinned symbols. Args: name, rules? ({sector,min_yield,max_payout,max_pe,min_growth,sort}), id? (to update).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer", "description": "Existing watchlist id to update; omit to create"},
				"name":  map[string]any{"type": "string", "description": "Watchlist name"},
				"rules": map[string]any{"type": "object", "description": "Optional filter rules; empty = manual list"},
			}, []string{"name"}),
			Handler: a.toolWatchlistSave,
		},
		{
			Name:        "watchlist_delete",
			Description: "Delete a watchlist. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolWatchlistDelete,
		},
		{
			Name:        "watchlist_member",
			Description: "Add/remove a symbol on a watchlist. state: include (force in), exclude (force out), auto (clear pin → rule-driven). Args: id, symbol, state.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"symbol": map[string]any{"type": "string"},
				"state":  map[string]any{"type": "string", "enum": []string{"include", "exclude", "auto"}},
			}, []string{"id", "symbol", "state"}),
			Handler: a.toolWatchlistMember,
		},
		{
			Name:        "watchlist_get",
			Description: "Resolve a watchlist to its current member stocks (rules ∪ pins − excludes) with full snapshot data. Args: id, sort?.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"sort":   map[string]any{"type": "string", "enum": []string{"name", "price", "change", "yield", "pe", "payout", "growth"}},
				"limit":  map[string]any{"type": "integer", "description": "Page size (default 100, maximum 200)"},
				"offset": map[string]any{"type": "integer", "description": "Zero-based page offset"},
			}, []string{"id"}),
			Handler: a.toolWatchlistGet,
		},
	}
}

// ─── HTTP routes (panel + REST mirror; GET-only reads) ─────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: "GET", Pattern: "/stocks", Handler: a.handleList},
		{Method: "GET", Pattern: "/search", Handler: a.handleSearch},
		{Method: "GET", Pattern: "/stock/{symbol}", Handler: a.handleGet},
		{Method: "GET", Pattern: "/chart/{symbol}", Handler: a.handleChart},
		{Method: "GET", Pattern: "/dividends/{symbol}", Handler: a.handleDividends},
		{Method: "GET", Pattern: "/status", Handler: a.handleStatus},
		{Method: "GET", Pattern: "/watchlists", Handler: a.handleWatchlists},
		{Method: "POST", Pattern: "/watchlists", Handler: a.handleWatchlistSave},
		{Method: "GET", Pattern: "/watchlists/{id}", Handler: a.handleWatchlistGet},
		{Method: "DELETE", Pattern: "/watchlists/{id}", Handler: a.handleWatchlistDelete},
		{Method: "POST", Pattern: "/watchlists/{id}/member", Handler: a.handleWatchlistMember},
	}
}

func (a *App) handleStatus(w http.ResponseWriter, _ *http.Request) {
	body, err := a.toolSyncStatus(nil)
	respond(w, body, err)
}

// ─── Watchlist HTTP handlers (panel) ───────────────────────────────
// Project comes from globalCtx (project-scoped installs) or ?project_id=
// (global installs); the panel always passes it.

func (a *App) handleWatchlists(w http.ResponseWriter, r *http.Request) {
	body, err := a.toolWatchlistsList(globalCtx, map[string]any{"project_id": r.URL.Query().Get("project_id")})
	respond(w, body, err)
}

func (a *App) handleWatchlistSave(w http.ResponseWriter, r *http.Request) {
	args := readJSONBody(r)
	args["project_id"] = r.URL.Query().Get("project_id")
	body, err := a.toolWatchlistSave(globalCtx, args)
	respond(w, body, err)
}

func (a *App) handleWatchlistGet(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	body, err := a.toolWatchlistGet(globalCtx, map[string]any{
		"project_id": r.URL.Query().Get("project_id"),
		"id":         id,
		"sort":       r.URL.Query().Get("sort"),
		"limit":      r.URL.Query().Get("limit"),
		"offset":     r.URL.Query().Get("offset"),
	})
	respond(w, body, err)
}

func (a *App) handleWatchlistDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	body, err := a.toolWatchlistDelete(globalCtx, map[string]any{
		"project_id": r.URL.Query().Get("project_id"), "id": id,
	})
	respond(w, body, err)
}

func (a *App) handleWatchlistMember(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	args := readJSONBody(r)
	args["project_id"] = r.URL.Query().Get("project_id")
	args["id"] = id
	body, err := a.toolWatchlistMember(globalCtx, args)
	respond(w, body, err)
}

// readJSONBody decodes a JSON object request body into args (empty on
// missing/invalid body).
func readJSONBody(r *http.Request) map[string]any {
	args := map[string]any{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&args)
	}
	return args
}

func (a *App) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	args := map[string]any{}
	if v := q.Get("q"); v != "" {
		args["query"] = v
	}
	if v := q.Get("sector"); v != "" {
		args["sector"] = v
	}
	if v := q.Get("sort"); v != "" {
		args["sort"] = v
	}
	for _, k := range []string{"min_yield", "max_yield", "min_payout", "max_payout", "min_pe", "max_pe", "min_growth", "max_growth", "min_mcap", "max_mcap"} {
		if v := q.Get(k); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				args[k] = f
			}
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			args["limit"] = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			args["offset"] = n
		}
	}
	body, err := a.toolList(args)
	respond(w, body, err)
}

func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	body, err := a.toolSearch(map[string]any{"query": r.URL.Query().Get("q"), "limit": r.URL.Query().Get("limit")})
	respond(w, body, err)
}

func (a *App) handleGet(w http.ResponseWriter, r *http.Request) {
	body, err := a.toolGet(map[string]any{"symbol": r.PathValue("symbol"), "refresh": r.URL.Query().Get("refresh")})
	respond(w, body, err)
}

func (a *App) handleChart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	body, err := a.toolChart(map[string]any{
		"symbol":   r.PathValue("symbol"),
		"range":    q.Get("range"),
		"interval": q.Get("interval"),
		"refresh":  q.Get("refresh"),
	})
	respond(w, body, err)
}

func (a *App) handleDividends(w http.ResponseWriter, r *http.Request) {
	body, err := a.toolDividends(map[string]any{"symbol": r.PathValue("symbol"), "refresh": r.URL.Query().Get("refresh")})
	respond(w, body, err)
}

// ─── Helpers ───────────────────────────────────────────────────────

func respond(w http.ResponseWriter, body any, err error) {
	if err != nil {
		httpJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	httpJSON(w, http.StatusOK, body)
}

func httpJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if props == nil {
		out["properties"] = map[string]any{}
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func strArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func floatArg(args map[string]any, key string) (float64, bool) {
	switch v := args[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && b
	case float64:
		return v != 0
	case int:
		return v != 0
	default:
		return false
	}
}

func orStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func orFloat(a, b float64) float64 {
	if a != 0 {
		return a
	}
	return b
}

func main() { sdk.Run(&App{}) }
