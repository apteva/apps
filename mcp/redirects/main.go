// Apteva Redirects v0.1.0 — branded short links and domain redirects.
//
// One sidecar per install. The catch-all HTTP handler on `/` looks up
// the inbound (Host, Path) in the redirects table and answers with a
// 30x + Location header. The /api/redirects/* surface is the panel +
// agent REST mirror.
//
// Boundary with routes: every redirect_add hits routes.routes_register
// to claim its hostname so apteva-server reverse-proxies inbound HTTP
// here. We never touch the routes table directly — that's the routes
// app's responsibility.
//
// Boundary with domains: when the hostname is registered in domains,
// redirect_add upserts a CNAME via domain_records_set so DNS points at
// the platform. When it isn't (user manages DNS elsewhere), we skip
// silently and just record the rule.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ─────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: redirects
display_name: Redirects
version: 0.1.1
description: |
  Branded short links and domain redirects. Each rule maps a
  (hostname, path) pair to an external URL and returns a 30x.
  Composes on top of routes (ingress) and domains (DNS).
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - { name: routes,  reason: "Hostname routing" }
    - { name: domains, optional: true, reason: "DNS auto-config (optional)" }
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: redirect_add,    description: "Create a redirect rule." }
    - { name: redirect_update, description: "Update a rule by id." }
    - { name: redirect_remove, description: "Delete a rule." }
    - { name: redirect_list,   description: "List rules." }
    - { name: redirect_get,    description: "Fetch one rule." }
    - { name: redirect_test,   description: "Dry-run a redirect lookup." }
  ui_panels:
    - { slot: project.page, label: "Redirects", icon: corner-up-right, entry: /ui/RedirectsPanel.mjs }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/redirects
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/redirects.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App ───────────────────────────────────────────────────────────

type App struct{}

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
		return errors.New("redirects requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("redirects mounted", "data_dir", ctx.DataDir())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

// ─── HTTP routes ───────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Admin / panel surface — auth required.
		{Pattern: "/api/redirects", Handler: a.handleRedirectsCollection},
		{Pattern: "/api/redirects/", Handler: a.handleRedirectItem},

		// Public catch-all that actually issues the 30x. Has to come
		// last; ServeMux longest-prefix routing keeps the /api/* paths
		// above this one. Public traffic from any browser, so NoAuth.
		{Pattern: "/", Handler: a.handlePublicRedirect, NoAuth: true},
	}
}

// ─── helpers shared with handlers + tools ──────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
	}
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg))
}

// callerInstallID resolves the calling sidecar's install id from the
// platform-set header. Manual panel entries get owner=0.
func callerInstallID(r *http.Request) int64 {
	v := r.Header.Get("X-Apteva-App-Install-ID")
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// myInstallID reads the platform-injected APTEVA_INSTALL_ID env. Used
// when calling routes_register from inside this sidecar — the routes
// app insists on a non-zero owner.
func myInstallID() int64 {
	v := os.Getenv("APTEVA_INSTALL_ID")
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
