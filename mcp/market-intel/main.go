// market-intel — cross-source market intelligence gateway for trading
// agents. v0.1 ships the gateway face: unified, normalized queries over
// every bound data source (prediction markets, sports, macro, news,
// crypto). The signal engine (background scanners + arbitrage events)
// lands in v0.2.
//
// Read-only by design — market-intel reads prices, depth, stats, and
// history; the agent decides; the trading app executes. No order-placing
// authority lives here.

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: market-intel
display_name: Market Intelligence
version: 0.1.2
description: Cross-source market-intelligence gateway for trading agents (unified data queries; signals in v0.2).
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
    - role: prediction_market_polymarket
      kind: integration
      required: false
      compatible_slugs: [polymarket, polymarket-data, polymarket-clob]
    - role: prediction_market_other
      kind: integration
      required: false
      compatible_slugs: [kalshi, manifold-markets]
    - role: sports_data
      kind: integration
      required: false
      compatible_slugs: [the-odds-api, the-sports-db, api-sports, tennis-abstract]
    - role: macro_data
      kind: integration
      required: false
      compatible_slugs: [fred, bls, eia, finnhub]
    - role: news_data
      kind: integration
      required: false
      compatible_slugs: [tavily, perplexity, exa, newsapi, gnews, gdelt, wikipedia]
    - role: crypto_data
      kind: integration
      required: false
      compatible_slugs: [coingecko, etherscan, polygonscan, whale-alert, defillama]
    - role: llm
      kind: integration
      required: false
      compatible_slugs: [anthropic-api, openai-api]
  apps:
    - name: trading
      optional: true
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: markets
      description: "Live volume-ranked markets across public prediction venues (Polymarket, Kalshi, Manifold)."
    - name: enrich
      description: "Everything relevant to a prediction market in one normalized blob."
    - name: stats
      description: "Normalized profile for an entity (player/team/ticker/crypto)."
    - name: history
      description: "Head-to-head between two entities, or one entity's time series."
    - name: context
      description: "Deduped news + sentiment + event-volume for a topic or entity."
    - name: probability
      description: "Best ground-truth probability for an event."
    - name: resolve_entity
      description: "Resolve a name to a canonical entity id across sources."
    - name: sources_status
      description: "Which data sources are bound + healthy right now, by domain."
  ui_panels:
    - slot: project.page
      label: Market Intel
      icon: activity
      entry: /ui/MarketIntelPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/market-intel
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/market-intel.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

// globalCtx — stashed so HTTP handlers (invoked by the SDK without an
// AppCtx) can reach the DB + platform client. Same pattern trading,
// crm, storage use.
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
		return errors.New("market-intel requires a db block")
	}
	ctx.AppDB().SetMaxOpenConns(1)
	globalCtx = ctx
	ctx.Logger().Info("market-intel mounted",
		"project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error            { return nil }
func (a *App) Channels() []sdk.ChannelFactory         { return nil }
func (a *App) EventHandlers() []sdk.EventHandler      { return nil }

// Workers — none in v0.1. The signal-engine scanners (venue_sync,
// signal_scan, discovery, resolution_watch) land in v0.2.
func (a *App) Workers() []sdk.Worker { return nil }

// ─── HTTP routes (panel + REST mirror) ─────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/sources", Handler: a.handleHTTPSources},
		{Pattern: "/markets", Handler: a.handleHTTPMarkets},
		{Pattern: "/enrich/", Handler: a.handleHTTPEnrich},
		{Pattern: "/query/", Handler: a.handleHTTPQuery},
	}
}

func (a *App) handleHTTPMarkets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	limit := 30
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	rows := gwListMarkets(a.client(), limit)
	httpJSON(w, 200, map[string]any{"markets": rows, "count": len(rows)})
}

// handleHTTPQuery — query-param front door for the panel's test
// console. /query/<tool>?<params> maps to the same gateway functions
// the MCP tools use. GET-only; everything here is a read.
func (a *App) handleHTTPQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	tool := strings.TrimPrefix(r.URL.Path, "/query/")
	q := r.URL.Query()
	// Carry _project_id through so resolveProjectFromArgs is satisfied
	// on global-scope installs (env wins when present).
	args := map[string]any{"_project_id": q.Get("project_id")}
	for k := range q {
		args[k] = q.Get(k)
	}

	var (
		out any
		err error
	)
	switch tool {
	case "enrich":
		out, err = a.toolEnrich(globalCtx, args)
	case "probability":
		out, err = a.toolProbability(globalCtx, args)
	case "stats":
		out, err = a.toolStats(globalCtx, args)
	case "context":
		out, err = a.toolContext(globalCtx, args)
	case "history":
		out, err = a.toolHistory(globalCtx, args)
	default:
		httpErr(w, 404, "unknown query tool: "+tool)
		return
	}
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, 200, out)
}

func (a *App) handleHTTPSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	out, err := a.toolSourcesStatus(globalCtx, map[string]any{})
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	httpJSON(w, 200, out)
}

func (a *App) handleHTTPEnrich(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	market := strings.TrimPrefix(r.URL.Path, "/enrich/")
	if market == "" {
		httpErr(w, 400, "market id required")
		return
	}
	out, err := a.toolEnrich(globalCtx, map[string]any{"market": market})
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	httpJSON(w, 200, out)
}

// ─── Helpers ───────────────────────────────────────────────────────

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func strArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		return map[string]any{"type": "object"}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func httpJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func httpErr(w http.ResponseWriter, status int, msg string) {
	httpJSON(w, status, map[string]any{"error": msg})
}

func main() { sdk.Run(&App{}) }
