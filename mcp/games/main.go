// Games v0.2 — players and progression.
//
// The Games app is the game-domain layer of Apteva: the pieces a studio
// would otherwise get from PlayFab, Nakama, or Unity Gaming Services,
// composed from sibling apps wherever one already exists.
//
//   - Identity lives in the Auth app. Games logs players in by device or
//     custom ID through auth_public_login_identity, stores the resulting
//     auth user id on the player row, and verifies Auth-issued EdDSA JWTs
//     locally against Auth's JWKS on every player request.
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
//	public.go      — v1/v2 player APIs (JWT-gated)
//	admin.go       — dashboard REST under /admin
//	tools.go       — MCP tools
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML string

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
	if err := initializeGames(ctx); err != nil {
		return err
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
	return []sdk.Worker{{Name: "event-delivery", Schedule: "@every 1s", Run: func(_ context.Context, ctx *sdk.AppCtx) error { return drainOutbox(ctx) }}, {
		Name:     "leaderboard-rollover",
		Schedule: "@every 1m",
		Run: func(_ context.Context, app *sdk.AppCtx) error {
			pid := envProject()
			if pid != "" {
				return maintainGames(app, pid, time.Now())
			}
			rows, err := app.AppDB().Query(`SELECT DISTINCT project_id FROM games`)
			if err != nil {
				return err
			}
			projects := []string{}
			for rows.Next() {
				var p string
				if err := rows.Scan(&p); err != nil {
					rows.Close()
					return err
				}
				projects = append(projects, p)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, p := range projects {
				if err := maintainGames(app, p, time.Now()); err != nil {
					return err
				}
			}
			return nil
		},
	}}
}

// ─── HTTP routes ─────────────────────────────────────────────────────
//
// /v1/*    player API — NoAuth at the SDK gate; every handler except the
//          login routes verifies an Auth-issued JWT itself.
// /admin/* dashboard REST — behind the platform bearer token.

func (a *App) HTTPRoutes() []sdk.Route {
	routes := []sdk.Route{
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
	for _, route := range append([]sdk.Route(nil), routes...) {
		if strings.HasPrefix(route.Pattern, "/v1/") {
			route.Pattern = strings.Replace(route.Pattern, "/v1/", "/v2/games/{game_id}/", 1)
			routes = append(routes, route)
		}
	}
	for _, spec := range []struct{ method, path string }{{"GET", "/admin/games"}, {"POST", "/admin/games"}, {"GET", "/admin/games/{game_id}"}, {"PATCH", "/admin/games/{game_id}"}, {"POST", "/admin/games/{game_id}/archive"}, {"POST", "/admin/games/{game_id}/restore"}} {
		routes = append(routes, sdk.Route{Method: spec.method, Pattern: spec.path, Handler: a.handleGames})
	}
	routes = append(routes, sdk.Route{Method: "POST", Pattern: "/admin/games/{game_id}/login-ticket", Handler: a.handleLoginTicket})
	return routes
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

func httpJSON(w http.ResponseWriter, v any) { httpStatus(w, http.StatusOK, v) }
func httpStatus(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		code = http.StatusInternalServerError
		b = []byte(`{"error":"response encoding failed"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(append(b, '\n'))
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	httpStatus(w, code, map[string]string{"error": msg})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid json")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("request must contain one JSON value")
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
