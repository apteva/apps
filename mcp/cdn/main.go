// Apteva CDN v0.1 — public-facing edge for apps that emit URLs.
//
// v0.1 is local-mode only: no third-party CDN provider, no edge
// nodes, no caching. The apteva-server itself is always the origin.
// Creating a zone wires three pieces:
//
//   domains.domain_records_set  → DNS (A or CNAME at the registrar)
//   certs.cert_issue            → TLS material (served via CertCache)
//   routes.routes_register      → host→target reverse-proxy entry
//                                 (consumed by HostRouter)
//
// Once the route is registered, apteva-server's HostRouter sees the
// Host header on every inbound request and reverse-proxies to the
// stored origin_url. The cdn sidecar isn't in the request path —
// it's an orchestrator. cdn_url_for is pure string assembly.
//
// Mode B (self-hosted edges via instances) and regional origins
// land in v0.3.
package main

import (
	"errors"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ─────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: cdn
display_name: CDN
version: 0.1.0
description: |
  Public-facing edge for apps that emit URLs. v0.1: local-mode only
  — apteva-server is the origin, no third-party provider, no edge
  caching. Composes domains + certs + routes into one capability.
author: Apteva
scopes: [global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - { name: routes }
    - { name: domains, optional: true }
    - { name: certs,   optional: true }
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: cdn_zone_create, description: "Stand up a public hostname for an origin URL." }
    - { name: cdn_zone_get,    description: "Fetch one zone by id or hostname." }
    - { name: cdn_zone_list,   description: "List zones for this project." }
    - { name: cdn_zone_delete, description: "Tear down a zone." }
    - { name: cdn_url_for,     description: "Mint a public URL on a zone for an origin path." }
  ui_panels:
    - { slot: project.page, label: CDN, icon: cloud, entry: /ui/CdnPanel.mjs }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/cdn
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/cdn.db
  migrations: migrations/
config_schema:
  - { name: server_public_host,   type: text,   default: "", label: "Apteva-server public host" }
  - { name: record_type_default,  type: select, options: [A, CNAME], default: A, label: "Default DNS record type" }
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
		return errors.New("cdn requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("cdn mounted",
		"server_public_host", configOr(ctx, "server_public_host", ""),
		"record_type_default", configOr(ctx, "record_type_default", "A"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

// ─── Project resolution ────────────────────────────────────────────
//
// cdn is global-scoped: APTEVA_PROJECT_ID is unset on the sidecar.
// Callers must inject _project_id on cross-app calls; HTTP callers
// pass ?project_id=. Matches the domains/certs/routes pattern.

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

// ─── Small arg / config helpers ────────────────────────────────────

func configOr(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
		return v
	}
	return def
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

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

func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			return true
		}
	}
	return false
}

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
