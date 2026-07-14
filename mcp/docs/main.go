// Docs v0.2 — generated client documents stored in storage.
//
// Templates live in this app's DB; agents call docs_render(template,
// data) to produce a PDF that lands in storage as a real file. All
// the URL/visibility/sharing machinery flows through storage's
// surface — docs is purely the renderer + audit log.
//
// Files in this package:
//
//	main.go         — App, manifest, OnMount, route + tool wiring
//	store.go        — DB queries (templates + renders tables)
//	render.go       — markdown + Go-template + maroto → PDF bytes
//	storageclient.go — CallApp wrapper for uploading PDFs to storage
//	handlers.go     — HTTP handlers (panel-facing)
//	tools.go        — MCP tool handlers
package main

import (
	"context"
	_ "embed"
	"errors"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// Embed the install manifest as the runtime manifest too. One source of
// truth prevents the sidecar /manifest response from drifting away from
// the marketplace/install contract.
//
//go:embed apteva.yaml
var manifestYAML string

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
	if strings.TrimSpace(ctx.CurrentProject()) == "" {
		return errors.New("docs v0.3 requires a project-scoped install; reinstall global Docs installs per project")
	}
	globalCtx = ctx
	version := a.Manifest().Version
	ctx.Logger().Info("docs mounted",
		"version", version,
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
			HandlerCtx:  a.toolListTemplatesCtx,
		},
		{
			Name:        "docs_get_template",
			Description: "Fetch one template by id or slug.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"slug": map[string]any{"type": "string"},
			}, nil),
			HandlerCtx: a.toolGetTemplateCtx,
		},
		{
			Name:        "docs_create_template",
			Description: "Create a template. Args: slug, name, body, description?, default_folder?.",
			InputSchema: schemaObject(map[string]any{
				"slug":           map[string]any{"type": "string"},
				"name":           map[string]any{"type": "string"},
				"body":           map[string]any{"type": "string"},
				"description":    map[string]any{"type": "string"},
				"default_folder": map[string]any{"type": "string"},
			}, []string{"slug", "name", "body"}),
			HandlerCtx: a.toolCreateTemplateCtx,
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
			HandlerCtx: a.toolUpdateTemplateCtx,
		},
		{
			Name:        "docs_delete_template",
			Description: "Remove a template.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			HandlerCtx:  a.toolDeleteTemplateCtx,
		},
		{
			Name:        "docs_render",
			Description: "Render a template into a file in storage. Markdown supports headings, lists, GFM tables, and images (storage:<id> or data: URI, e.g. a logo). Args: template_id or template_slug, data, output_name?, output_folder?.",
			InputSchema: schemaObject(map[string]any{
				"template_id":   map[string]any{"type": "integer"},
				"template_slug": map[string]any{"type": "string"},
				"data":          map[string]any{"type": "object"},
				"output_name":   map[string]any{"type": "string"},
				"output_folder": map[string]any{"type": "string"},
				"page_size":     map[string]any{"type": "string", "enum": []string{"A4", "letter", "legal"}},
			}, []string{"data"}),
			HandlerCtx: a.toolRenderCtx,
		},
		{
			Name:        "docs_preview",
			Description: "Render but don't persist. Returns base64 PDF bytes (images render too).",
			InputSchema: schemaObject(map[string]any{
				"template_id":   map[string]any{"type": "integer"},
				"template_slug": map[string]any{"type": "string"},
				"body":          map[string]any{"type": "string"},
				"data":          map[string]any{"type": "object"},
				"page_size":     map[string]any{"type": "string", "enum": []string{"A4", "letter", "legal"}},
			}, []string{"data"}),
			HandlerCtx: a.toolPreviewCtx,
		},
		{
			Name:        "docs_list_renders",
			Description: "Audit trail. Args: template_id?, since?, limit?, offset?.",
			InputSchema: schemaObject(map[string]any{
				"template_id": map[string]any{"type": "integer"},
				"since":       map[string]any{"type": "string"},
				"limit":       map[string]any{"type": "integer"},
				"offset":      map[string]any{"type": "integer"},
			}, nil),
			HandlerCtx: a.toolListRendersCtx,
		},
		{
			Name:        "docs_get_render",
			Description: "Replay one render. Args: render_id.",
			InputSchema: schemaObject(map[string]any{"render_id": map[string]any{"type": "integer"}}, []string{"render_id"}),
			HandlerCtx:  a.toolGetRenderCtx,
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
