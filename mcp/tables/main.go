// Tables v0.1 — typed-row database app.
//
// User-defined tables are persisted as physical sqlite tables named
// t_<id>. Every physical table has three reserved columns the user
// can't override: id (INTEGER PRIMARY KEY), created_at, updated_at.
// User columns are validated against a closed type set (text, number,
// bool, datetime, json, file_id) on every insert/update.
//
// Identifiers (table + column names) are restricted to the regex
// `^[a-z][a-z0-9_]*$` and a max length of 64. The platform never sees
// raw user-supplied identifiers in SQL; they round-trip through the
// metadata tables, so generated SQL only quotes names already known
// to match the regex.
package main

import (
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: tables
display_name: Tables
version: 0.1.4
description: Typed-row database for Apteva agents and human teams.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: tables_create,    description: "Create a new table with typed columns." }
    - { name: tables_list,      description: "List tables visible to this install." }
    - { name: tables_describe,  description: "Schema + row count for one table." }
    - { name: tables_alter,     description: "Add/rename/drop columns." }
    - { name: tables_drop,      description: "Delete a table and its rows." }
    - { name: rows_insert,      description: "Insert one or many rows; atomic." }
    - { name: rows_get,         description: "Fetch one row by id." }
    - { name: rows_update,      description: "Patch fields on a row." }
    - { name: rows_delete,      description: "Delete by id, or by filter + confirm." }
    - { name: rows_search,      description: "Filtered list with typed predicates." }
    - { name: rows_count,       description: "Count rows matching a filter." }
    - { name: tables_query,     description: "Read-only SELECT escape hatch." }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/tables
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/tables.db
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
		return errors.New("tables requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("tables mounted",
		"max_rows_per_table", maxRowsPerTable(ctx),
		"max_query_rows", maxQueryRows(ctx),
		"max_query_ms", maxQueryMs(ctx))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/tables", Handler: a.handleTablesCollection},
		{Pattern: "/tables/", Handler: a.handleTablesItem},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	colSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":     map[string]any{"type": "string"},
			"type":     map[string]any{"type": "string", "enum": []string{"text", "number", "bool", "datetime", "json", "file_id"}},
			"nullable": map[string]any{"type": "boolean"},
			"default":  map[string]any{},
		},
		"required": []string{"name", "type"},
	}
	whereSchema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"col":   map[string]any{"type": "string"},
				"op":    map[string]any{"type": "string", "enum": []string{"eq", "neq", "lt", "lte", "gt", "gte", "contains", "in", "between", "is_null", "is_not_null"}},
				"value": map[string]any{},
			},
			"required": []string{"col", "op"},
		},
	}
	return []sdk.Tool{
		{
			Name:        "tables_create",
			Description: "Create a new typed table. Args: name, columns ([{name, type, nullable?, default?}]), scope? (project|global, default project). Reserved column names: id, created_at, updated_at. Returns {id, name, columns}.",
			InputSchema: schemaObject(map[string]any{
				"name":    map[string]any{"type": "string"},
				"columns": map[string]any{"type": "array", "items": colSchema},
				"scope":   map[string]any{"type": "string", "enum": []string{"project", "global"}},
			}, []string{"name", "columns"}),
			Handler: a.toolTablesCreate,
		},
		{
			Name:        "tables_list",
			Description: "List tables visible to this install. No args. Returns [{id, name, scope, columns, row_count, created_at}].",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolTablesList,
		},
		{
			Name:        "tables_describe",
			Description: "Full schema + row count for one table. Args: name.",
			InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string"}}, []string{"name"}),
			Handler:     a.toolTablesDescribe,
		},
		{
			Name:        "tables_alter",
			Description: "Mutate a table's schema. Args: name, plus one of add ({name, type, nullable?, default?}), rename ({from, to}), drop (column name). Adding non-nullable requires default.",
			InputSchema: schemaObject(map[string]any{
				"name":   map[string]any{"type": "string"},
				"add":    colSchema,
				"rename": map[string]any{"type": "object", "properties": map[string]any{"from": map[string]any{"type": "string"}, "to": map[string]any{"type": "string"}}, "required": []string{"from", "to"}},
				"drop":   map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolTablesAlter,
		},
		{
			Name:        "tables_drop",
			Description: "Delete a table and all its rows. Args: name, confirm (must be true).",
			InputSchema: schemaObject(map[string]any{
				"name":    map[string]any{"type": "string"},
				"confirm": map[string]any{"type": "boolean"},
			}, []string{"name", "confirm"}),
			Handler: a.toolTablesDrop,
		},
		{
			Name:        "rows_insert",
			Description: "Insert one or many rows. Args: table, rows (array of objects). Atomic: first failing row aborts the whole call. Returns {ids, inserted}.",
			InputSchema: schemaObject(map[string]any{
				"table": map[string]any{"type": "string"},
				"rows":  map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			}, []string{"table", "rows"}),
			Handler: a.toolRowsInsert,
		},
		{
			Name:        "rows_get",
			Description: "Fetch one row by id. Args: table, id, hydrate_files? (resolve file_id columns to {id, url}). Returns {row, found}.",
			InputSchema: schemaObject(map[string]any{
				"table":         map[string]any{"type": "string"},
				"id":            map[string]any{"type": "integer"},
				"hydrate_files": map[string]any{"type": "boolean"},
			}, []string{"table", "id"}),
			Handler: a.toolRowsGet,
		},
		{
			Name:        "rows_update",
			Description: "Patch fields on a row. Args: table, id, fields (object keyed by column name). Touches updated_at. Returns the new row.",
			InputSchema: schemaObject(map[string]any{
				"table":  map[string]any{"type": "string"},
				"id":     map[string]any{"type": "integer"},
				"fields": map[string]any{"type": "object"},
			}, []string{"table", "id", "fields"}),
			Handler: a.toolRowsUpdate,
		},
		{
			Name:        "rows_delete",
			Description: "Delete by id, or by filter when where is supplied + confirm=true. Returns {deleted}.",
			InputSchema: schemaObject(map[string]any{
				"table":   map[string]any{"type": "string"},
				"id":      map[string]any{"type": "integer"},
				"where":   whereSchema,
				"confirm": map[string]any{"type": "boolean"},
			}, []string{"table"}),
			Handler: a.toolRowsDelete,
		},
		{
			Name:        "rows_search",
			Description: "Filter, sort, paginate. Args: table, where? (array of {col, op, value}), order_by? (\"col\" | \"col desc\"), limit?, offset?. Returns {rows, total}.",
			InputSchema: schemaObject(map[string]any{
				"table":    map[string]any{"type": "string"},
				"where":    whereSchema,
				"order_by": map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
				"offset":   map[string]any{"type": "integer"},
			}, []string{"table"}),
			Handler: a.toolRowsSearch,
		},
		{
			Name:        "rows_count",
			Description: "Count rows matching a filter. Args: table, where?. Returns {count}.",
			InputSchema: schemaObject(map[string]any{
				"table": map[string]any{"type": "string"},
				"where": whereSchema,
			}, []string{"table"}),
			Handler: a.toolRowsCount,
		},
		{
			Name:        "tables_query",
			Description: "Read-only SELECT escape hatch. Args: sql, params? (array). Refused on anything other than a single SELECT or WITH ... SELECT. Row + duration caps enforced. Returns {columns, rows, truncated}.",
			InputSchema: schemaObject(map[string]any{
				"sql":    map[string]any{"type": "string"},
				"params": map[string]any{"type": "array"},
			}, []string{"sql"}),
			Handler: a.toolTablesQuery,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── shared helpers shared with the *.go files in this package ─────

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func errf(format string, args ...any) error { return fmt.Errorf(format, args...) }
