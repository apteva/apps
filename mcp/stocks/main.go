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
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: stocks
display_name: Stocks
version: 0.1.0
description: Explore stocks backed by Yahoo Finance — list/filter, quote + key data, price chart, dividend history. Read-only.
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
      description: "List/filter the universe with live price, day change, and dividend yield. Args: sector?, min_yield?, sort? (name|price|change|yield), limit?."
    - name: get
      description: "One stock's data — name, exchange, price, day + 52-week range, volume, dividend yield + frequency, last dividend. Args: symbol."
    - name: chart
      description: "OHLCV price history for a stock. Args: symbol, range? (1mo|6mo|1y|5y|max), interval? (1d|1wk|1mo)."
    - name: dividends
      description: "Dividend history + summary (trailing-12mo total, yield, frequency, growth) for a stock. Args: symbol."
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
}

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
	if v := ctx.Config().Get("cache_ttl_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			a.ttl = time.Duration(n) * time.Second
		}
	}
	ctx.Logger().Info("stocks mounted", "cache_ttl", a.ttl.String())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	symbolReq := schemaObject(map[string]any{
		"symbol": map[string]any{"type": "string", "description": "Ticker, e.g. AAPL"},
	}, []string{"symbol"})

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
			Description: "List/filter the universe with live price, day change, and dividend yield.",
			InputSchema: schemaObject(map[string]any{
				"sector":    map[string]any{"type": "string", "description": "Filter by sector (exact, case-insensitive)"},
				"min_yield": map[string]any{"type": "number", "description": "Only stocks with dividend yield ≥ this %"},
				"sort":      map[string]any{"type": "string", "enum": []string{"name", "price", "change", "yield"}, "description": "Sort key (default name)"},
				"limit":     map[string]any{"type": "integer", "description": "Max results (default 100)"},
			}, nil),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.toolList(args) },
		},
		{
			Name:        "get",
			Description: "One stock's data — quote, day + 52-week range, volume, dividend yield + frequency, last dividend.",
			InputSchema: symbolReq,
			Handler:     func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.toolGet(args) },
		},
		{
			Name:        "chart",
			Description: "OHLCV price history for a stock.",
			InputSchema: schemaObject(map[string]any{
				"symbol":   map[string]any{"type": "string", "description": "Ticker, e.g. AAPL"},
				"range":    map[string]any{"type": "string", "enum": []string{"1mo", "6mo", "1y", "5y", "max"}, "description": "History range (default 1y)"},
				"interval": map[string]any{"type": "string", "enum": []string{"1d", "1wk", "1mo"}, "description": "Bar interval (default 1d)"},
			}, []string{"symbol"}),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.toolChart(args) },
		},
		{
			Name:        "dividends",
			Description: "Dividend history + summary (trailing-12mo total, yield, frequency, growth) for a stock.",
			InputSchema: symbolReq,
			Handler:     func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.toolDividends(args) },
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
	}
}

func (a *App) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	args := map[string]any{}
	if v := q.Get("sector"); v != "" {
		args["sector"] = v
	}
	if v := q.Get("sort"); v != "" {
		args["sort"] = v
	}
	if v := q.Get("min_yield"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			args["min_yield"] = f
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			args["limit"] = n
		}
	}
	body, err := a.toolList(args)
	respond(w, body, err)
}

func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	body, err := a.toolSearch(map[string]any{"query": r.URL.Query().Get("q")})
	respond(w, body, err)
}

func (a *App) handleGet(w http.ResponseWriter, r *http.Request) {
	body, err := a.toolGet(map[string]any{"symbol": r.PathValue("symbol")})
	respond(w, body, err)
}

func (a *App) handleChart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	body, err := a.toolChart(map[string]any{
		"symbol":   r.PathValue("symbol"),
		"range":    q.Get("range"),
		"interval": q.Get("interval"),
	})
	respond(w, body, err)
}

func (a *App) handleDividends(w http.ResponseWriter, r *http.Request) {
	body, err := a.toolDividends(map[string]any{"symbol": r.PathValue("symbol")})
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
