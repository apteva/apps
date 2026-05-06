// Analytics v0.1 — generic event store for Apteva apps.
//
// V1 surface: explicit-tracking only. Apps call analytics_track when
// they want an event recorded; queries surface aggregates. Auto-
// capture from the platform event firehose is deferred to v0.2 (waits
// on a global app-events stream endpoint in apteva-server).
package main

import (
	"errors"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// Embedded manifest. Mirrors apteva.yaml so the binary can validate
// itself at boot and surface its identity over /api/manifest. Keep
// these two files in sync; CI compares.
const manifestYAML = `schema: apteva-app/v1
name: analytics
display_name: Analytics
version: 0.1.0
description: |
  Generic event analytics for Apteva apps. Other apps call
  analytics_track to record typed events; analytics_query / count /
  top / topics surface aggregates over JSON props. v0.1 is
  explicit-tracking only — auto-capture from the platform event
  firehose is deferred to v0.2.
author: Apteva
tags: [analytics, events, observability]
scopes: [global]
min_apteva_version: "0.10.0"
requires:
  permissions:
    - db.write.app
provides:
  http_routes:
    - prefix: /
  permissions:
    - name: events.write
      description: Record events via analytics_track.
    - name: events.read
      description: Query events.
  mcp_tools:
    - name: analytics_track
      description: Record one event. Args event (required), props?, app?, project_id?, user_id?, session_id?, ts?. Returns the new event id.
      requires: events.write
    - name: analytics_query
      description: Read events with optional filters and group_by.
      requires: events.read
    - name: analytics_count
      description: Count events matching filters.
      requires: events.read
    - name: analytics_top
      description: Top-N values for a JSON props key.
      requires: events.read
    - name: analytics_topics
      description: List distinct (app, topic) pairs seen.
      requires: events.read
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/analytics
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/analytics.db
  migrations: migrations/
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
		return errors.New("analytics requires a db block")
	}
	ctx.Logger().Info("analytics mounted (v0.1 — explicit-tracking only)")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) HTTPRoutes() []sdk.Route            { return nil }
func (a *App) Channels() []sdk.ChannelFactory     { return nil }
func (a *App) Workers() []sdk.Worker              { return nil }
func (a *App) EventHandlers() []sdk.EventHandler  { return nil }

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "analytics_track",
			Description: "Record one event. Args: event (required), props?, app?, project_id?, user_id?, session_id?, install_id?, ts? (unix ms; defaults to now). Returns {id, ts}.",
			InputSchema: schemaObject(map[string]any{
				"event":      map[string]any{"type": "string"},
				"props":      map[string]any{"type": "object"},
				"app":        map[string]any{"type": "string"},
				"project_id": map[string]any{"type": "string"},
				"user_id":    map[string]any{"type": "string"},
				"session_id": map[string]any{"type": "string"},
				"install_id": map[string]any{"type": "integer"},
				"ts":         map[string]any{"type": "integer"},
			}, []string{"event"}),
			Handler: a.toolTrack,
		},
		{
			Name: "analytics_query",
			Description: "Read events. Args: app?, topic?, project_id?, since? (unix ms), until?, where? (map of \"props.X\" → value, equality only), group_by? (array of \"props.X\" / app / topic / project_id / source), limit? (default 100, max 1000). Without group_by returns recent rows; with group_by returns aggregate buckets.",
			InputSchema: schemaObject(map[string]any{
				"app":        map[string]any{"type": "string"},
				"topic":      map[string]any{"type": "string"},
				"project_id": map[string]any{"type": "string"},
				"since":      map[string]any{"type": "integer"},
				"until":      map[string]any{"type": "integer"},
				"where":      map[string]any{"type": "object"},
				"group_by":   map[string]any{"type": "array"},
				"limit":      map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolQuery,
		},
		{
			Name:        "analytics_count",
			Description: "Count events matching filters. Args: app?, topic?, project_id?, since?, until?, where?. Returns {count}.",
			InputSchema: schemaObject(map[string]any{
				"app":        map[string]any{"type": "string"},
				"topic":      map[string]any{"type": "string"},
				"project_id": map[string]any{"type": "string"},
				"since":      map[string]any{"type": "integer"},
				"until":      map[string]any{"type": "integer"},
				"where":      map[string]any{"type": "object"},
			}, nil),
			Handler: a.toolCount,
		},
		{
			Name:        "analytics_top",
			Description: "Top-N values for a JSON props key. Args: by (required, e.g. \"props.platform\"), app?, topic?, project_id?, since?, until?, where?, limit? (default 10, max 200). Returns [{value, count}].",
			InputSchema: schemaObject(map[string]any{
				"by":         map[string]any{"type": "string"},
				"app":        map[string]any{"type": "string"},
				"topic":      map[string]any{"type": "string"},
				"project_id": map[string]any{"type": "string"},
				"since":      map[string]any{"type": "integer"},
				"until":      map[string]any{"type": "integer"},
				"where":      map[string]any{"type": "object"},
				"limit":      map[string]any{"type": "integer"},
			}, []string{"by"}),
			Handler: a.toolTop,
		},
		{
			Name:        "analytics_topics",
			Description: "List distinct (app, topic) pairs seen. Args: app?. Returns [{app, topic, last_ts, count}].",
			InputSchema: schemaObject(map[string]any{
				"app": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolTopics,
		},
	}
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

func main() {
	sdk.Run(&App{})
}
