// content v1 — block-based CMS for Apteva.
//
// One install = one site. Posts and pages share a single table
// (`kind` distinguishes them); page hierarchy lives in posts.parent_id.
// The canonical body is `body_blocks` (JSON tree); `body_html` is the
// rendered cache invalidated on update.
//
// Three delivery modes, all from the same database:
//
//   server   — themed HTML at /, /posts/:slug, /:slug, term archives,
//              /feed.xml, /sitemap.xml. Uses the embedded default theme
//              or a custom theme loaded from the bound storage app.
//   headless — JSON REST under /api/* for external frontends.
//   hybrid   — both, controlled by the `render_mode` config knob.
//
// The agent's surface is structured block manipulation:
// blocks_insert/update/move/delete reference blocks by stable id so
// edits survive reorders and revisions.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct{}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("content requires a db block")
	}
	globalCtx = ctx

	// Load + cache the active theme. Falls back to embedded default
	// when no storage app is bound or the configured theme is missing.
	if err := loadActiveTheme(ctx); err != nil {
		ctx.Logger().Warn("theme load failed; using embedded default", "err", err.Error())
	}

	// Seed bundled templates for project-scoped installs. Global
	// installs seed lazily on first templates_list call per project
	// (handled inside the tool).
	if pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); pid != "" {
		if err := seedBundledTemplates(ctx, pid); err != nil {
			ctx.Logger().Warn("template seed failed", "err", err.Error())
		}
	}

	ctx.Logger().Info("content mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"),
		"active_theme", currentThemeName())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// Workers: scheduled-publishing tick. Walks rows with
// status='scheduled' and scheduled_at <= now, flips them to
// 'published'. Cheap to run; SQLite handles the predicate well.
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "scheduled_publisher",
			Schedule: "@every 1m",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return runScheduledPublisher(app)
			},
		},
	}
}

// HTTPRoutes wires every public + REST endpoint. The framework
// reverse-proxies to /api/apps/content/<pattern>; when a host is
// domain-linked via the deploy app, public paths (/, /posts/*, etc.)
// arrive here too.
// HTTP routes split into three buckets:
//
//   /admin/*    — REST surface for the dashboard panel + headless
//                 consumers. The platform proxy mounts the sidecar at
//                 /api/apps/content/<path>; callers reach `/admin/posts`
//                 as `/api/apps/content/admin/posts`. Namespacing under
//                 /admin/ keeps the bare `/posts` URL free for public
//                 rendering.
//
//   /_theme/*, /_media/*, /preview/, /feed.xml, /sitemap.xml — public
//                 framework-internal endpoints (underscore-prefix flags
//                 "not for editorial use as a post slug"). NoAuth so
//                 visitors can reach them without an install token.
//
//   /             — public catch-all that renders posts/pages/term
//                 archives. Must register last; ServeMux longest-prefix
//                 routing puts the others in front.
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// ── /admin/* REST surface ──────────────────────────────
		{Pattern: "/admin/posts", Handler: a.handleHTTPPostsCollection},
		{Pattern: "/admin/posts/", Handler: a.handleHTTPPostItem},
		{Pattern: "/admin/terms", Handler: a.handleHTTPTermsCollection},
		{Pattern: "/admin/media", Handler: a.handleHTTPMedia},
		{Pattern: "/admin/menus", Handler: a.handleHTTPMenus},
		{Pattern: "/admin/redirects", Handler: a.handleHTTPRedirects},
		{Pattern: "/admin/settings", Handler: a.handleHTTPSettings},
		{Pattern: "/admin/themes", Handler: a.handleHTTPThemes},
		{Pattern: "/admin/block-types", Handler: a.handleHTTPBlockTypes},
		{Pattern: "/admin/templates", Handler: a.handleHTTPTemplates},
		{Pattern: "/admin/templates/", Handler: a.handleHTTPTemplateItem},

		// ── public render surface ───────────────────────────────
		{Pattern: "/_theme/", Handler: a.handleThemeAsset, NoAuth: true},
		{Pattern: "/_media/", Handler: a.handleMediaAsset, NoAuth: true},
		{Pattern: "/preview/", Handler: a.handlePreview, NoAuth: true},
		{Pattern: "/feed.xml", Handler: a.handleFeed, NoAuth: true},
		{Pattern: "/sitemap.xml", Handler: a.handleSitemap, NoAuth: true},
		{Pattern: "/", Handler: a.handlePublic, NoAuth: true},
	}
}

// MCPTools is the agent's surface — defined in tools.go so this file
// stays focused on app lifecycle.
func (a *App) MCPTools() []sdk.Tool { return a.mcpTools() }

// ── project resolution ────────────────────────────────────────────
//
// project-scoped installs: APTEVA_PROJECT_ID is set by the platform,
// every row's project_id equals that.
//
// global-scoped installs: APTEVA_PROJECT_ID is empty; the caller MUST
// supply project_id explicitly. Agents send `_project_id` in their
// MCP args (the platform threads it for ctx.WithProject); the
// dashboard sends ?project_id on every URL; public render falls back
// to a single-project assumption since the public host is bound to
// one project anyway (the platform routes the request based on the
// linked domain).

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
	if v := r.Header.Get("X-Apteva-Project-ID"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required when install scope=global")
}

// ── helpers ───────────────────────────────────────────────────────

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// globalCtx is stashed at OnMount time so HTTP handlers (which the SDK
// invokes with just (w, r)) can reach the AppDB and PlatformAPI. CRM
// uses the same pattern — see CRM main.go's globalCtx for the rationale.
var globalCtx *sdk.AppCtx

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

// nowRFC3339 — single source of truth for "now" in serialized form.
// Wrapping it makes test injection trivial later.
func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// nowStamp — DB-friendly UTC timestamp.
func nowStamp() string { return time.Now().UTC().Format("2006-01-02 15:04:05") }

// asInt64 coerces JSON-derived numbers (float64) and strings into int64.
// MCP args arrive as map[string]any; integers may be float64 (JSON) or
// stringified (some clients).
func asInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	case string:
		var n int64
		if _, err := fmt.Sscan(t, &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

// asString coerces an arg into a string; missing → "".
func asString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
