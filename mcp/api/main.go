package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: api
display_name: API Gateway
version: 0.2.0
description: Lightweight API gateway for Apteva SaaS projects with streaming Function targets.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
    - platform.ingress.write
    - net.egress
  apps:
    - { name: functions, optional: true }
    - { name: domains, optional: true }
    - { name: auth, optional: true }
provides:
  http_routes:
    - prefix: /
    - prefix: /gw/
      no_auth: true
  mcp_tools:
    - { name: api_create, description: "Create an API." }
    - { name: api_get, description: "Fetch one API by id or slug." }
    - { name: api_list, description: "List APIs." }
    - { name: api_update, description: "Update an API." }
    - { name: api_delete, description: "Delete an API." }
    - { name: api_route_add, description: "Add or update an API route." }
    - { name: api_route_list, description: "List API routes." }
    - { name: api_route_delete, description: "Delete an API route." }
    - { name: api_key_create, description: "Create an API key." }
    - { name: api_key_list, description: "List API keys." }
    - { name: api_key_revoke, description: "Revoke an API key." }
    - { name: api_logs, description: "List API request logs." }
  ui_panels:
    - { slot: project.page, label: "API Gateway", icon: network, entry: /ui/ApiPanel.mjs }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/api
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/api.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct {
	httpClient *http.Client
}

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
		return errors.New("api requires a db block")
	}
	globalCtx = ctx
	if a.httpClient == nil {
		a.httpClient = http.DefaultClient
	}
	ctx.Logger().Info("api mounted", "project_id", ctx.CurrentProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/apis", Handler: a.handleAPIs},
		{Pattern: "/apis/", Handler: a.handleAPIItem},
		{Pattern: "/tools/call", Handler: a.handleToolsCall},
		{Pattern: "/gw/", Handler: a.handleGateway, NoAuth: true},
	}
}

func main() { sdk.Run(&App{}) }

func projectFromArgs(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	if s := strings.TrimSpace(stringArg(args, "project_id", "")); s != "" {
		return s, nil
	}
	if ctx != nil && strings.TrimSpace(ctx.CurrentProject()) != "" {
		return ctx.CurrentProject(), nil
	}
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	return "", errors.New("project_id required")
}

func projectFromRequest(r *http.Request) (string, error) {
	if s := strings.TrimSpace(r.URL.Query().Get("project_id")); s != "" {
		return s, nil
	}
	if globalCtx != nil && strings.TrimSpace(globalCtx.CurrentProject()) != "" {
		return globalCtx.CurrentProject(), nil
	}
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	return "", errors.New("project_id required")
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
	}
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func stringArg(args map[string]any, key, def string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func boolArg(args map[string]any, key string, def bool) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		case json.Number:
			i, _ := n.Int64()
			return int(i)
		}
	}
	return def
}

func jsonTextArg(args map[string]any, key string, def string) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return def, nil
	}
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			return def, nil
		}
		if !json.Valid([]byte(s)) {
			return "", fmt.Errorf("%s must be valid JSON", key)
		}
		return s, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
