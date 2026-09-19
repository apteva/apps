// market-intel combines a cross-source research gateway with an audited,
// provider-neutral signal pipeline. Signals are read-only recommendations;
// execution authority remains in the trading app.
//
// Read-only by design — market-intel reads prices, depth, stats, and
// history; the agent decides; the trading app executes. No order-placing
// authority lives here.

package main

import (
	"context"
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
version: 0.3.2
description: Generic temporal intelligence platform with market and trading domain packs.
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
      compatible_slugs: [tavily, perplexity, exa, newsapi, gnews, gdelt, sec-edgar, wikipedia]
    - role: crypto_data
      kind: integration
      required: false
      compatible_slugs: [coingecko, etherscan, polygonscan, whale-alert, defillama]
    - role: market_data_paid
      kind: integration
      required: false
      compatible_slugs: [alpaca-market-data, finnhub, alpha-vantage, yahoo-finance]
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
    - name: search
      description: "Generic bitemporal search across recorded evidence with date and as-of filters."
    - name: evidence_record
      description: "Record immutable source-attributed evidence."
    - name: timeline
      description: "Chronological evidence timeline for a topic or entity."
    - name: replay
      description: "Deterministic point-in-time snapshot for analysis and backtesting."
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
    - name: signals_list
      description: "List normalized, cost-adjusted trading signals with provenance and outcomes."
    - name: signal_feeds
      description: "List signal products/feeds and their quality gates."
    - name: signal_metrics
      description: "Audited performance metrics from matured signals."
    - name: signal_scan_now
      description: "Run signal discovery now."
    - name: signal_backtest
      description: "Walk-forward historical replay with explicit cost assumptions."
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
	if pid := scanProjectID(); pid != "" {
		if _, err := ensureDefaultFeed(ctx.AppDB(), pid); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "signal_scan", Schedule: "@every 5m", Run: func(ctx context.Context, app *sdk.AppCtx) error {
		pid := scanProjectID()
		if pid == "" {
			app.Logger().Info("signal scan skipped for global install without project context")
			return nil
		}
		_, err := a.runSignalScan(ctx, app, pid)
		return err
	}}}
}

// ─── HTTP routes (panel + REST mirror) ─────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/sources", Handler: a.handleHTTPSources},
		{Pattern: "/markets", Handler: a.handleHTTPMarkets},
		{Pattern: "/search", Handler: a.handleHTTPSearch},
		{Pattern: "/timeline", Handler: a.handleHTTPTimeline},
		{Pattern: "/replay", Handler: a.handleHTTPReplay},
		{Pattern: "/evidence", Handler: a.handleHTTPEvidence},
		{Pattern: "/enrich/", Handler: a.handleHTTPEnrich},
		{Pattern: "/query/", Handler: a.handleHTTPQuery},
		{Pattern: "/signals", Handler: a.handleHTTPSignals},
		{Pattern: "/signal-feeds", Handler: a.handleHTTPFeeds},
		{Pattern: "/signal-metrics", Handler: a.handleHTTPMetrics},
		{Pattern: "/signal-scan", Handler: a.handleHTTPScan},
	}
}

func intelligenceArgs(r *http.Request) map[string]any {
	args := map[string]any{"_project_id": r.URL.Query().Get("project_id")}
	for _, key := range []string{"query", "kind", "source", "entity", "event_from", "event_to", "published_from", "published_to", "as_of", "from", "to", "limit"} {
		if v := r.URL.Query().Get(key); v != "" {
			args[key] = v
		}
	}
	return args
}
func (a *App) handleHTTPSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	out, err := a.toolSearch(globalCtx, intelligenceArgs(r))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, 200, out)
}
func (a *App) handleHTTPTimeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	out, err := a.toolTimeline(globalCtx, intelligenceArgs(r))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, 200, out)
}
func (a *App) handleHTTPReplay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	out, err := a.toolReplay(globalCtx, intelligenceArgs(r))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, 200, out)
}
func (a *App) handleHTTPEvidence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, 405, "POST only")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, 400, "invalid JSON body")
		return
	}
	body["_project_id"] = r.URL.Query().Get("project_id")
	out, err := a.toolEvidenceRecord(globalCtx, body)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, 201, out)
}

func (a *App) handleHTTPSignals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	args := map[string]any{"_project_id": r.URL.Query().Get("project_id"), "feed": r.URL.Query().Get("feed"), "status": r.URL.Query().Get("status"), "limit": r.URL.Query().Get("limit")}
	out, err := a.toolSignalsList(globalCtx, args)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, 200, out)
}
func (a *App) handleHTTPFeeds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	out, err := a.toolSignalFeeds(globalCtx, map[string]any{"_project_id": r.URL.Query().Get("project_id")})
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, 200, out)
}
func (a *App) handleHTTPMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	out, err := a.toolSignalMetrics(globalCtx, map[string]any{"_project_id": r.URL.Query().Get("project_id"), "feed": r.URL.Query().Get("feed")})
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, 200, out)
}
func (a *App) handleHTTPScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, 405, "POST only")
		return
	}
	out, err := a.toolSignalScan(globalCtx, map[string]any{"_project_id": r.URL.Query().Get("project_id")})
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	httpJSON(w, 200, out)
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
	out, err := a.toolEnrich(globalCtx, map[string]any{"market": market, "_project_id": r.URL.Query().Get("project_id")})
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
