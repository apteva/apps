// Docs v0.1 — generated client documents stored in storage.
//
// Templates live in this app's DB; agents call docs_render(template,
// data) to produce a PDF that lands in storage as a real file. All
// the URL/visibility/sharing machinery flows through storage's
// surface — docs is purely the renderer + audit log.
//
// Files in this package:
//   main.go         — App, manifest, OnMount, route + tool wiring
//   store.go        — DB queries (templates + renders tables)
//   render.go       — markdown + Go-template + maroto → PDF bytes
//   storageclient.go — CallApp wrapper for uploading PDFs to storage
//   handlers.go     — HTTP handlers (panel-facing)
//   tools.go        — MCP tool handlers
package main

import (
	"context"
	"errors"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ────────────────────────────────────────────────
//
// Mirrors apteva.yaml. manifest_test.go enforces they stay in sync.

const manifestYAML = `schema: apteva-app/v1
name: docs
display_name: Documents
version: 0.1.1
description: |
  Generate client-facing PDFs from markdown templates and store them
  in storage. Pure-Go render pipeline (no Chromium). Audit trail on
  every render.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - name: storage
      version: ">=0.8.1"
      reason: rendered PDFs are uploaded to storage; the file's URL/visibility/sharing all flow through storage's surface.
provides:
  http_routes:
    - prefix: /
  resources:
    - name: template
      label: "Document template"
      list_endpoint: /templates
      matcher: id_set
      picker: list
  permissions:
    - { name: docs.read,   resource: template, description: "List + read templates." }
    - { name: docs.write,  resource: template, description: "Create / update / delete templates." }
    - { name: docs.render, resource: template, description: "Render a document from a template." }
  mcp_tools:
    - { name: docs_list_templates,  description: "List templates available in this install.", requires: docs.read }
    - { name: docs_get_template,    description: "Fetch one template's full body + metadata. Args - id (int) OR slug (string).", requires: docs.read }
    - { name: docs_create_template, description: "Create a template. Args - slug, name, body, description?, source_format? (markdown), output_format? (pdf), default_folder?.", requires: docs.write }
    - { name: docs_update_template, description: "Partial update. Args - id, plus any of name/description/body/default_folder.", requires: docs.write }
    - { name: docs_delete_template, description: "Remove a template. Past renders keep working.", requires: docs.write }
    - { name: docs_render,          description: "Render a template with data into a file in storage. Args - template_id (int) or template_slug (string), data (object), output_name?, output_folder?.", requires: docs.render }
    - { name: docs_preview,         description: "Render but do not persist. Returns base64 PDF bytes.", requires: docs.render }
    - { name: docs_list_renders,    description: "Audit trail. Filter by template_id, since, limit.", requires: docs.read }
    - { name: docs_get_render,      description: "Replay one render - data snapshot, output file_id, timestamps.", requires: docs.read }
  ui_panels:
    - slot: project.page
      label: Documents
      icon: file-text
      entry: /ui/DocsPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/docs
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/docs.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// globalCtx — set in OnMount so HTTP handlers can read AppDB() +
// logger without threading the ctx through every call site.
var globalCtx *sdk.AppCtx

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
		return errors.New("docs requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("docs mounted",
		"version", "0.1.0",
		"default_folder", ctx.Config().Get("default_output_folder"),
	)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		// Optional retention sweep: prune renders older than the
		// install's prune_renders_older_than_days. The bytes in
		// storage stay; only the audit row + data_snapshot expire.
		// 0 in config = no pruning.
		{
			Name:     "audit-prune",
			Schedule: "@every 24h",
			Run:      runAuditPrune,
		},
	}
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ──────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/templates", Handler: a.handleTemplatesCollection},
		{Pattern: "/templates/", Handler: a.handleTemplatesItem},
		{Pattern: "/renders", Handler: a.handleRendersCollection},
		{Pattern: "/renders/", Handler: a.handleRendersItem},
	}
}

// ─── MCP tools ────────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "docs_list_templates",
			Description: "List templates in this install.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolListTemplates,
		},
		{
			Name:        "docs_get_template",
			Description: "Fetch one template by id or slug.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"slug": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolGetTemplate,
		},
		{
			Name:        "docs_create_template",
			Description: "Create a template. Args: slug, name, body, description?, source_format?, output_format?, default_folder?.",
			InputSchema: schemaObject(map[string]any{
				"slug":           map[string]any{"type": "string"},
				"name":           map[string]any{"type": "string"},
				"body":           map[string]any{"type": "string"},
				"description":    map[string]any{"type": "string"},
				"source_format":  map[string]any{"type": "string"},
				"output_format":  map[string]any{"type": "string"},
				"default_folder": map[string]any{"type": "string"},
			}, []string{"slug", "name", "body"}),
			Handler: a.toolCreateTemplate,
		},
		{
			Name:        "docs_update_template",
			Description: "Partial update. Args: id, plus any of name/description/body/default_folder.",
			InputSchema: schemaObject(map[string]any{
				"id":             map[string]any{"type": "integer"},
				"name":           map[string]any{"type": "string"},
				"description":    map[string]any{"type": "string"},
				"body":           map[string]any{"type": "string"},
				"default_folder": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolUpdateTemplate,
		},
		{
			Name:        "docs_delete_template",
			Description: "Remove a template.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolDeleteTemplate,
		},
		{
			Name:        "docs_render",
			Description: "Render a template into a file in storage. Args: template_id or template_slug, data, output_name?, output_folder?.",
			InputSchema: schemaObject(map[string]any{
				"template_id":   map[string]any{"type": "integer"},
				"template_slug": map[string]any{"type": "string"},
				"data":          map[string]any{"type": "object"},
				"output_name":   map[string]any{"type": "string"},
				"output_folder": map[string]any{"type": "string"},
			}, []string{"data"}),
			Handler: a.toolRender,
		},
		{
			Name:        "docs_preview",
			Description: "Render but don't persist. Returns base64 PDF bytes.",
			InputSchema: schemaObject(map[string]any{
				"template_id":   map[string]any{"type": "integer"},
				"template_slug": map[string]any{"type": "string"},
				"body":          map[string]any{"type": "string"},
				"data":          map[string]any{"type": "object"},
			}, []string{"data"}),
			Handler: a.toolPreview,
		},
		{
			Name:        "docs_list_renders",
			Description: "Audit trail. Args: template_id?, since?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"template_id": map[string]any{"type": "integer"},
				"since":       map[string]any{"type": "string"},
				"limit":       map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolListRenders,
		},
		{
			Name:        "docs_get_render",
			Description: "Replay one render. Args: render_id.",
			InputSchema: schemaObject(map[string]any{"render_id": map[string]any{"type": "integer"}}, []string{"render_id"}),
			Handler:     a.toolGetRender,
		},
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────

func runAuditPrune(ctx context.Context, app *sdk.AppCtx) error {
	days := configIntDefault(app.Config().Get("prune_renders_older_than_days"), 365)
	if days <= 0 {
		return nil
	}
	res, err := app.AppDB().Exec(
		`DELETE FROM renders WHERE rendered_at < datetime('now', '-' || ? || ' days')`,
		days,
	)
	if err != nil {
		app.Logger().Warn("audit prune failed", "err", err)
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		app.Logger().Info("pruned old render audit rows", "count", n, "older_than_days", days)
	}
	return nil
}

func main() {
	sdk.Run(&App{})
}

// schemaObject is the same helper every other app uses — wraps a
// JSON-Schema object descriptor for InputSchema.
func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	o := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

