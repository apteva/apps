// Games v0.1 — players and progression.
//
// The Games app is the game-domain layer of Apteva: the pieces a studio
// would otherwise get from PlayFab, Nakama, or Unity Gaming Services,
// composed from sibling apps wherever one already exists.
//
//   - Identity lives in the Auth app. Games logs players in by device or
//     custom ID through auth_public_login_identity, stores the resulting
//     auth user id on the player row, and verifies Auth-issued EdDSA JWTs
//     locally against Auth's JWKS on every /v1 request.
//   - Progression lives here: versioned player data (cloud save),
//     server-authoritative statistics, leaderboards with periods, and
//     achievements evaluated from statistics.
//   - Telemetry goes to Analytics when installed; every mutation also
//     publishes on the AppBus so an agent can react.
//
// Files:
//
//	main.go        — manifest, App, routes, tool wiring, helpers
//	store.go       — types + SQL
//	auth_client.go — Auth bridge (login, link, disable) + JWKS/JWT verify
//	progression.go — stat aggregation, leaderboards, periods, achievements
//	ops.go         — shared player operations (ban, erase, export, context)
//	public.go      — /v1 player API (JWT-gated)
//	admin.go       — dashboard REST under /admin
//	tools.go       — MCP tools
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// Mirrors apteva.yaml; manifest_test.go enforces the sync.
const manifestYAML = `schema: apteva-app/v1

name: games
display_name: Games
version: 0.1.0
description: |
  Game backend for Apteva: players and progression, run by an agent.
  v0.1 ships guest and custom-ID login through the Auth app, player
  profiles and bans, versioned player data (cloud save), server-authoritative
  statistics with last, max, min, and sum aggregation, leaderboards with
  daily, weekly, monthly, and season periods, and achievements evaluated
  from statistics. Game clients use the /v1 player API with Auth-issued
  JWTs; agents use the MCP tools; the dashboard panel shows players,
  leaderboards, and definitions.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/games
icon: /ui/icon.svg
icon_style: monochrome
tags: [games, players, progression, leaderboards, liveops, backend]

scopes: [project]
min_apteva_version: "0.14.1"

requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - name: auth
      version: ">=0.10.0"
      reason: Owns player accounts, sessions, and tokens. Games logs players in by device or custom ID through Auth and verifies Auth-issued JWTs on every player request.
    - name: analytics
      optional: true
      reason: Records session_start, stat, and achievement events for retention queries. Without it, telemetry is skipped.

provides:
  http_routes:
    - prefix: /v1/
      no_auth: true
    - prefix: /admin/
  mcp_tools:
    - name: players_search
      description: Search players by display name, player id, or auth user id; filter by status (active | banned). Paged.
    - name: players_get
      description: Fetch one player by id, auth_user_id, device_id, or custom_id (device and custom ids are resolved through Auth).
    - name: players_get_context
      description: Player plus statistics, every data key (including server-only), achievements, bans, and recent audit events.
    - name: players_update
      description: Update a player's display_name, avatar_url, region, locale, or public metadata.
    - name: players_ban
      description: Ban a player with a reason and optional expires_at (RFC3339). Disables the Auth user and revokes sessions.
    - name: players_unban
      description: Lift a player's active bans and re-enable the Auth user.
    - name: players_export
      description: Export everything stored about one player as JSON (data-subject access request).
    - name: players_erase
      description: Delete a player's rows and disable the Auth user. Requires confirm=true. Irreversible.
    - name: data_get
      description: Read a player's data keys (all visibilities), or one key.
    - name: data_set
      description: Write a player data key with an optional expected version (optimistic) and visibility (public | private | server).
    - name: data_delete
      description: Delete a player data key.
    - name: stats_define
      description: Define or update a statistic (aggregation last | max | min | sum, client_writable, description). Stats feeding leaderboards or the economy should stay server-only.
    - name: stats_list
      description: List statistic definitions.
    - name: stats_get
      description: Read one player's statistics.
    - name: stats_update
      description: Server-authoritative statistic update for a player; applies aggregation, leaderboards, and achievement rules. Undefined stats are auto-defined as server-only last-value stats.
    - name: leaderboards_create
      description: Create a leaderboard over a statistic with sort (desc | asc) and reset (none | daily | weekly | monthly | season) plus season_days.
    - name: leaderboards_list
      description: List leaderboards with their current period.
    - name: leaderboards_get
      description: Read a leaderboard page (current or past period) with ranks and display names.
    - name: leaderboards_around_player
      description: Entries around one player in a leaderboard, with the player's rank.
    - name: leaderboards_reset_now
      description: Start a fresh period on a leaderboard immediately; previous entries stay readable by period.
    - name: achievements_define
      description: Define or update an achievement unlocked when a statistic meets a threshold (op gte | gt | lte | lt | eq), or granted manually.
    - name: achievements_list
      description: List achievement definitions.
    - name: achievements_grant
      description: Manually unlock an achievement for a player.
  ui_panels:
    - slot: project.page
      label: Games
      icon: gamepad
      entry: /ui/GamesPanel.mjs
  skills:
    - name: games
      description: Use Games tools for players, bans, player data, statistics, leaderboards, and achievements. Activate when the user asks about players, scores, rankings, saves, or a game's backend.
      body_file: skills/games/SKILL.md
      metadata:
        category: games
        agent_plugins: "1.0.0"
  workers:
    - name: leaderboard-rollover
      schedule: "@every 1m"
  publishes:
    - name: player.created
      description: A player row was created on first login.
      payload:
        player_id: integer
        auth_user_id: integer
        provider: string
        kind: string
        display_name: string
    - name: player.linked
      description: A player linked an additional device or custom identity.
      payload:
        player_id: integer
        provider: string
    - name: player.banned
      description: A player was banned; the Auth user is disabled.
      payload:
        player_id: integer
        reason: string
        expires_at: string
        source: string
    - name: player.unbanned
      description: A player's bans were lifted; the Auth user is re-enabled.
      payload:
        player_id: integer
        source: string
    - name: player.erased
      description: A player's data was deleted and the Auth user disabled.
      payload:
        player_id: integer
        auth_user_id: integer
    - name: stat.updated
      description: A player's statistic changed after aggregation.
      payload:
        player_id: integer
        stat: string
        value: number
        previous: number
        source: string
    - name: leaderboard.reset
      description: A leaderboard started a new period, by schedule or by hand.
      payload:
        leaderboard: string
        previous_period: string
        period: string
        manual: boolean
    - name: achievement.unlocked
      description: A player unlocked an achievement.
      payload:
        player_id: integer
        key: string
        source: string

runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/games
  port: 8080
  health_check: /health

db:
  driver: sqlite
  path: /data/games.db
  migrations: migrations/

config_schema:
  - name: auth_organization_slug
    type: text
    default: "default"
    label: Auth organization
    description: Auth organization that holds this title's players. Games registers its own native OAuth client in it on first login.
  - name: analytics_enabled
    type: text
    default: "true"
    label: Send telemetry to Analytics
    description: When true and the Analytics app is installed, logins, stat updates, and achievement unlocks are tracked. true | false.
  - name: default_display_name_prefix
    type: text
    default: "Player"
    label: Default display name prefix
    description: New players without a display name are called "<prefix> <id>".

upgrade_policy: auto-patch
`

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("games requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("games mounted", "scope_project_id", envProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// Workers — one ticker moves periodic leaderboards to their next period
// on schedule so leaderboard.reset fires close to the boundary even
// when no player writes a score. Reads and writes also roll over
// lazily, so the worker is about timeliness, not correctness.
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "leaderboard-rollover",
		Schedule: "@every 1m",
		Run: func(_ context.Context, app *sdk.AppCtx) error {
			pid := envProject()
			if pid == "" {
				return nil
			}
			return rolloverLeaderboards(app, pid, time.Now())
		},
	}}
}

// ─── HTTP routes ─────────────────────────────────────────────────────
//
// /v1/*    player API — NoAuth at the SDK gate; every handler except the
//          login routes verifies an Auth-issued JWT itself.
// /admin/* dashboard REST — behind the platform bearer token.

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: "POST", Pattern: "/v1/login/device", Handler: a.handleLoginDevice, NoAuth: true},
		{Method: "POST", Pattern: "/v1/login/custom", Handler: a.handleLoginCustom, NoAuth: true},
		{Method: "POST", Pattern: "/v1/login/link", Handler: a.handleLoginLink, NoAuth: true},
		{Method: "POST", Pattern: "/v1/session/refresh", Handler: a.handleSessionRefresh, NoAuth: true},
		{Method: "GET", Pattern: "/v1/me", Handler: a.handleMe, NoAuth: true},
		{Method: "PATCH", Pattern: "/v1/me", Handler: a.handleMePatch, NoAuth: true},
		{Method: "GET", Pattern: "/v1/players/{id}", Handler: a.handlePublicPlayer, NoAuth: true},
		{Method: "GET", Pattern: "/v1/data", Handler: a.handleDataList, NoAuth: true},
		{Method: "GET", Pattern: "/v1/data/{key}", Handler: a.handleDataGet, NoAuth: true},
		{Method: "PUT", Pattern: "/v1/data/{key}", Handler: a.handleDataPut, NoAuth: true},
		{Method: "DELETE", Pattern: "/v1/data/{key}", Handler: a.handleDataDelete, NoAuth: true},
		{Method: "GET", Pattern: "/v1/stats", Handler: a.handleStatsGet, NoAuth: true},
		{Method: "POST", Pattern: "/v1/stats", Handler: a.handleStatsPost, NoAuth: true},
		{Method: "GET", Pattern: "/v1/leaderboards/{name}", Handler: a.handleLeaderboardGet, NoAuth: true},
		{Method: "GET", Pattern: "/v1/leaderboards/{name}/around", Handler: a.handleLeaderboardAround, NoAuth: true},
		{Method: "GET", Pattern: "/v1/achievements", Handler: a.handleAchievementsGet, NoAuth: true},

		{Method: "GET", Pattern: "/admin/stats", Handler: a.handleAdminStats},
		{Method: "GET", Pattern: "/admin/settings", Handler: a.handleAdminSettings},
		{Method: "GET", Pattern: "/admin/players", Handler: a.handleAdminPlayersList},
		{Method: "GET", Pattern: "/admin/players/{id}", Handler: a.handleAdminPlayerGet},
		{Method: "PATCH", Pattern: "/admin/players/{id}", Handler: a.handleAdminPlayerPatch},
		{Method: "POST", Pattern: "/admin/players/{id}/ban", Handler: a.handleAdminPlayerBan},
		{Method: "POST", Pattern: "/admin/players/{id}/unban", Handler: a.handleAdminPlayerUnban},
		{Method: "POST", Pattern: "/admin/players/{id}/stats", Handler: a.handleAdminPlayerStats},
		{Method: "GET", Pattern: "/admin/stat-defs", Handler: a.handleAdminStatDefsList},
		{Method: "POST", Pattern: "/admin/stat-defs", Handler: a.handleAdminStatDefsUpsert},
		{Method: "GET", Pattern: "/admin/leaderboards", Handler: a.handleAdminLeaderboardsList},
		{Method: "POST", Pattern: "/admin/leaderboards", Handler: a.handleAdminLeaderboardsCreate},
		{Method: "GET", Pattern: "/admin/leaderboards/{name}/entries", Handler: a.handleAdminLeaderboardEntries},
		{Method: "POST", Pattern: "/admin/leaderboards/{name}/reset", Handler: a.handleAdminLeaderboardReset},
		{Method: "GET", Pattern: "/admin/achievements", Handler: a.handleAdminAchievementsList},
		{Method: "POST", Pattern: "/admin/achievements", Handler: a.handleAdminAchievementsUpsert},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution ──────────────────────────────────────────────
//
// scope: project — APTEVA_PROJECT_ID is set at boot and wins. The arg /
// query fallbacks exist so a future global install needs no handler
// changes.

func envProject() string { return strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")) }

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if pid := envProject(); pid != "" {
		return pid, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if pid := envProject(); pid != "" {
		return pid, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required when install scope=global")
}

// ─── Stashed AppCtx for HTTP handlers (mirrors CRM / Auth) ───────────

var globalCtx *sdk.AppCtx

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

// ─── Config helpers ──────────────────────────────────────────────────

func cfgStr(ctx *sdk.AppCtx, name, dflt string) string {
	if ctx == nil || ctx.Config() == nil {
		return dflt
	}
	if v := strings.TrimSpace(ctx.Config().Get(name)); v != "" {
		return v
	}
	return dflt
}

func cfgBool(ctx *sdk.AppCtx, name string, dflt bool) bool {
	v := strings.ToLower(cfgStr(ctx, name, ""))
	if v == "" {
		return dflt
	}
	return v == "true" || v == "1" || v == "yes"
}

// ─── HTTP utilities ──────────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	httpStatus(w, code, map[string]string{"error": msg})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid json")
	}
	return nil
}

func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return xff
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func pathInt(r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(r.PathValue(name)), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func queryInt(r *http.Request, name string, dflt, min, max int) int {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return dflt
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return dflt
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// ─── Tool arg helpers ────────────────────────────────────────────────

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func stringArg(args map[string]any, key, dflt string) string {
	if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return dflt
}

func boolArg(args map[string]any, key string, dflt bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return dflt
}

func intReq(args map[string]any, key string) (int64, bool) {
	switch x := args[key].(type) {
	case float64:
		if x <= 0 {
			return 0, false
		}
		return int64(x), true
	case int:
		if x <= 0 {
			return 0, false
		}
		return int64(x), true
	case int64:
		if x <= 0 {
			return 0, false
		}
		return x, true
	case json.Number:
		n, err := x.Int64()
		if err != nil || n <= 0 {
			return 0, false
		}
		return n, true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil || n <= 0 {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

func intArg(args map[string]any, key string, dflt, min, max int) int {
	n, ok := intReq(args, key)
	if !ok {
		if v, isF := args[key].(float64); isF && v == 0 {
			return min
		}
		return dflt
	}
	if int(n) < min {
		return min
	}
	if int(n) > max {
		return max
	}
	return int(n)
}

func floatArg(args map[string]any, key string) (float64, bool) {
	switch x := args[key].(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}
