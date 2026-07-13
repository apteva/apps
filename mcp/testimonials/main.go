// Testimonials is a lightweight customer proof store.
//
// V1 deliberately owns one table only. It stores text testimonials,
// reviews, attribution, consent, status, and optional media references.
// Storage is an optional dependency, but this app does not call it yet:
// media_file_id is just a reference field for files created elsewhere.
package main

import (
	"database/sql"
	"errors"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: testimonials
display_name: Testimonials
version: 0.1.3
description: |
  Lightweight customer proof store for Apteva. Keep text testimonials,
  reviews, ratings, attribution, consent, publication status, and an
  optional media reference for later video/audio/image testimonials.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/testimonials
icon: https://raw.githubusercontent.com/apteva/apps/main/mcp/testimonials/icon.svg
tags: [testimonials, reviews, social-proof, marketing]
scopes: [project, global]
min_apteva_version: "0.11.0"
requires:
  permissions:
    - db.write.app
  apps:
    - name: storage
      optional: true
      reason: Store optional media files for testimonials.
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: testimonials_create, description: "Create a testimonial or review. Args: title?, quote?, body?, rating?, author fields?, status?, kind?, source?, media_file_id?, media_url?, tags?, metadata?." }
    - { name: testimonials_list, description: "List testimonials with exact tag filtering and pagination. Args: status?, kind?, source?, tag?, published_only?, include_archived?, q?, limit?, offset?. Published-only results omit private fields." }
    - { name: testimonials_get, description: "Fetch one testimonial. Args: id." }
    - { name: testimonials_update, description: "Patch testimonial fields. Args: id plus editable fields." }
    - { name: testimonials_set_status, description: "Set lifecycle status. Args: id, status (draft|submitted|approved|rejected|published|archived)." }
    - { name: testimonials_delete, description: "Archive a testimonial by default. Args: id, hard?." }
  ui_panels:
    - slot: project.page
      label: Testimonials
      icon: message-square-quote
      entry: /ui/TestimonialsPanel.mjs
  publishes:
    - name: testimonial.created
      description: A testimonial was created.
      payload:
        id: integer
        status: string
        kind: string
    - name: testimonial.updated
      description: A testimonial was updated.
      payload:
        id: integer
        status: string
        kind: string
    - name: testimonial.status_changed
      description: A testimonial status changed.
      payload:
        id: integer
        status: string
    - name: testimonial.deleted
      description: A testimonial was archived or hard-deleted.
      payload:
        id: integer
        hard: boolean
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/testimonials
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/testimonials.db
  migrations: migrations/
upgrade_policy: auto-patch
`

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
		return errors.New("testimonials requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("testimonials mounted", "version", "0.1.3")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/testimonials", Handler: a.handleTestimonialsCollection},
		{Pattern: "/testimonials/", Handler: a.handleTestimonialsItem},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "testimonials_create", Description: "Create a testimonial or review.", InputSchema: schemaObject(testimonialCreateSchema(), nil), Handler: a.toolTestimonialsCreate},
		{Name: "testimonials_list", Description: "List testimonials.", InputSchema: schemaObject(map[string]any{
			"status":           enumSchema(statusValues()),
			"kind":             enumSchema(kindValues()),
			"source":           map[string]any{"type": "string"},
			"tag":              map[string]any{"type": "string"},
			"published_only":   map[string]any{"type": "boolean"},
			"include_archived": map[string]any{"type": "boolean"},
			"q":                map[string]any{"type": "string"},
			"limit":            map[string]any{"type": "integer"},
			"offset":           map[string]any{"type": "integer", "minimum": 0},
		}, nil), Handler: a.toolTestimonialsList},
		{Name: "testimonials_get", Description: "Fetch one testimonial.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolTestimonialsGet},
		{Name: "testimonials_update", Description: "Patch testimonial fields.", InputSchema: schemaObject(testimonialUpdateSchema(), []string{"id"}), Handler: a.toolTestimonialsUpdate},
		{Name: "testimonials_set_status", Description: "Set lifecycle status.", InputSchema: schemaObject(map[string]any{
			"id":     map[string]any{"type": "integer"},
			"status": enumSchema(statusValues()),
		}, []string{"id", "status"}), Handler: a.toolTestimonialsSetStatus},
		{Name: "testimonials_delete", Description: "Archive a testimonial by default; hard delete when hard=true.", InputSchema: schemaObject(map[string]any{
			"id":   map[string]any{"type": "integer"},
			"hard": map[string]any{"type": "boolean"},
		}, []string{"id"}), Handler: a.toolTestimonialsDelete},
	}
}

func main() {
	sdk.Run(&App{})
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func enumSchema(values []string) map[string]any {
	items := make([]any, 0, len(values))
	for _, v := range values {
		items = append(items, v)
	}
	return map[string]any{"type": "string", "enum": items}
}

func testimonialCreateSchema() map[string]any {
	s := testimonialUpdateSchema()
	delete(s, "id")
	return s
}

func testimonialUpdateSchema() map[string]any {
	return map[string]any{
		"id":               map[string]any{"type": "integer"},
		"status":           enumSchema(statusValues()),
		"kind":             enumSchema(kindValues()),
		"source":           map[string]any{"type": "string"},
		"title":            map[string]any{"type": "string"},
		"quote":            map[string]any{"type": "string"},
		"body":             map[string]any{"type": "string"},
		"rating":           map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
		"author_name":      map[string]any{"type": "string"},
		"author_role":      map[string]any{"type": "string"},
		"author_company":   map[string]any{"type": "string"},
		"author_email":     map[string]any{"type": "string"},
		"media_file_id":    map[string]any{"type": "string"},
		"media_url":        map[string]any{"type": "string"},
		"consent_status":   enumSchema(consentValues()),
		"permission_scope": enumSchema(permissionValues()),
		"tags":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"metadata":         map[string]any{"type": "object"},
	}
}

var errNotFound = sql.ErrNoRows
