// Apteva Routes — public hostname control for Apteva.
//
// The public tools expose a stable app-level API while the actual
// routing table and TLS lifecycle live in apteva-server's native
// ingress system.
//
// Boundary with apteva-server: this app is the source of truth for
// the routing table. Apteva-server holds an in-memory cache that
// hydrates from this app on boot and refreshes on routes.changed
// events. Reads in the hot path (every public request) hit the
// cache, never this app.
//
// Boundary with deploy / code / certs: deploy.attach_domain calls
// routes_register after the DNS + cert steps. code.repos_dev_start
// (with expose=true) registers a route for the dev process. certs
// is unaware of this app — but the server's TLS GetCertificate hook
// reads cert_fqdn from the routes table to decide which cert to
// fetch from certs.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ─────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: routes
display_name: Routes
version: 0.5.2
description: |
  Public hostname control for Apteva. Routes exposes a stable app-level
  API for registering hostnames and delegates the actual routing and TLS
  ownership to apteva-server's native ingress system.

  Apps can keep calling routes_register/routes_unregister without
  learning the lower-level PlatformAPI ingress callbacks directly.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
scopes: [global]
requires:
  permissions:
    - db.write.app
    - platform.ingress.read
    - platform.ingress.write
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: routes_register,   description: "Register a hostname → target route through server-native ingress. Args: hostname, target, cert_fqdn?, tls_mode?, allow_http?." }
    - { name: routes_unregister, description: "Remove a route by hostname. Args: hostname." }
    - { name: routes_list,       description: "List routes owned by this Routes install." }
    - { name: routes_get,        description: "Fetch one route by hostname. Args: hostname." }
  ui_panels:
    - { slot: project.page, label: "Routes", icon: route, entry: /ui/RoutesPanel.mjs }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/routes
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/routes.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App ───────────────────────────────────────────────────────────

type App struct {
	// routingMode: "hostrouter" (default — apteva-server's HostRouter
	// reads the table) or "proxy" (this app drives an external reverse
	// proxy: Caddy / nginx).
	routingMode string
	// certDir mirrors the certs app's cert_output_dir. When set, the
	// rendered proxy config points TLS at <certDir>/<fqdn>/*.pem.
	certDir string

	// proxy is the detected reverse proxy. nil in hostrouter mode, or
	// in proxy mode when nothing was detected (the app stays inert).
	proxy *proxyTarget

	syncMu      sync.Mutex // serialises renders
	lastInclude string     // last include content written — diff before reload
	stopCh      chan struct{}
}

var globalCtx *sdk.AppCtx

// configOr reads an install-config value, falling back to def.
func configOr(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
		return v
	}
	return def
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("routes requires a db block")
	}
	globalCtx = ctx
	a.routingMode = configOr(ctx, "routing_mode", "hostrouter")
	a.certDir = configOr(ctx, "cert_dir", "/var/lib/apteva/certs")
	a.stopCh = make(chan struct{})

	ctx.Logger().Info("routes mounted",
		"data_dir", ctx.DataDir(),
		"routing_mode", a.routingMode)

	// proxy mode: detect the reverse proxy, wire ourselves in, and
	// keep its config in sync. hostrouter mode is the default and
	// leaves everything to apteva-server's HostRouter — no change.
	if a.routingMode == "proxy" {
		a.startProxyMode(ctx)
	}
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.stopCh != nil {
		close(a.stopCh)
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

// ─── HTTP routes ───────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/routes", Handler: a.handleRoutesCollection},
		{Pattern: "/api/routes/", Handler: a.handleRouteItem},
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
// X-Apteva-App-Install-ID header set by the platform middleware on
// /api/apps/<name>/* and /api/apps/callback/* requests. Manual panel
// entries don't carry a sidecar install-id so they get owner=0.
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
