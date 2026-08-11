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
	"sync"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: tables
display_name: Tables
version: 0.1.14
description: Typed-row database for Apteva agents and human teams.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
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
    - { name: indexes_create,   description: "Create a validated composite index." }
    - { name: indexes_list,     description: "List a table's indexes." }
    - { name: indexes_drop,     description: "Drop a user-managed index." }
    - { name: rows_insert,      description: "Insert one or many rows; atomic." }
    - { name: rows_upsert,      description: "Insert or update rows by key; atomic." }
    - { name: rows_get,         description: "Fetch one row by id." }
    - { name: rows_update,      description: "Patch fields on a row." }
    - { name: rows_delete,      description: "Delete by id, or by filter + confirm." }
    - { name: rows_search,      description: "Filtered list with typed predicates." }
    - { name: rows_count,       description: "Count rows matching a filter." }
    - { name: rows_aggregate,   description: "Single-table grouped aggregations." }
    - { name: tables_query,     description: "Read-only SELECT escape hatch." }
  publishes:
    - name: table.created
      description: A table was created.
      payload:
        id: integer
        name: string
        scope: string
        columns: array
    - name: table.altered
      description: A table schema changed.
      payload:
        name: string
        columns: array
    - name: table.dropped
      description: A table was dropped.
      payload:
        name: string
    - name: row.inserted
      description: Rows were inserted into a table.
      payload:
        table: string
        ids: array
        count: integer
    - name: row.updated
      description: Rows were updated in a table. Single-row updates include the changed fields and current row.
      payload:
        table: string
        id: integer
        count: integer
        fields: object
        row: object
    - name: row.deleted
      description: Rows were deleted from a table.
      payload:
        table: string
        id: integer
        deleted: integer
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
config_schema:
  - { name: max_rows_per_table, type: text, default: "1000000" }
  - { name: max_query_rows, type: text, default: "1000" }
  - { name: max_query_ms, type: text, default: "2000" }
  - { name: max_read_queue_ms, type: text, default: "1000" }
  - { name: max_read_conns, type: text, default: "4" }
  - { name: slow_query_ms, type: text, default: "250" }
  - { name: max_query_bytes, type: text, default: "4194304" }
  - { name: max_value_bytes, type: text, default: "1048576" }
  - { name: max_batch_rows, type: text, default: "1000" }
upgrade_policy: auto-patch
`

type App struct {
	schemaMu sync.RWMutex
	cache    schemaCache
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
		return errors.New("tables requires a db block")
	}
	globalCtx = ctx
	if ctx.AppReadDB() != ctx.AppDB() {
		ctx.AppReadDB().SetMaxOpenConns(maxReadConns(ctx))
		ctx.AppReadDB().SetMaxIdleConns(maxReadConns(ctx))
	}
	ctx.Logger().Info("tables mounted",
		"max_rows_per_table", maxRowsPerTable(ctx),
		"max_query_rows", maxQueryRows(ctx),
		"max_query_ms", maxQueryMs(ctx),
		"max_read_queue_ms", maxReadQueueMs(ctx),
		"max_read_conns", maxReadConns(ctx),
		"max_query_bytes", maxQueryBytes(ctx),
		"max_value_bytes", maxValueBytes(ctx),
		"max_batch_rows", maxBatchRows(ctx))
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
	groupBySchema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"oneOf": []any{
				map[string]any{"type": "string"},
				map[string]any{
					"type": "object",
					"properties": map[string]any{
						"col":    map[string]any{"type": "string"},
						"bucket": map[string]any{"type": "string", "enum": []string{"day", "week", "month", "year"}},
						"name":   map[string]any{"type": "string"},
					},
					"required": []string{"col"},
				},
			},
		},
	}
	metricSchema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"op":          map[string]any{"type": "string", "enum": []string{"count", "sum", "avg", "min", "max", "avg_ratio"}},
				"col":         map[string]any{"type": "string"},
				"distinct":    map[string]any{"type": "boolean"},
				"numerator":   map[string]any{"type": "string"},
				"denominator": map[string]any{"type": "string"},
			},
			"required": []string{"name", "op"},
		},
	}
	indexColumnSchema := map[string]any{
		"oneOf": []any{
			map[string]any{"type": "string"},
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"col":   map[string]any{"type": "string"},
					"order": map[string]any{"type": "string", "enum": []string{"asc", "desc"}},
				},
				"required": []string{"col"},
			},
		},
	}
	return []sdk.Tool{
		{
			Name:        "tables_create",
			Description: "Create a new project-confined typed table. Args: name, columns ([{name, type, nullable?, default?}]). Reserved column names: id, created_at, updated_at. Returns {id, name, columns}.",
			InputSchema: schemaObject(map[string]any{
				"name":    map[string]any{"type": "string"},
				"columns": map[string]any{"type": "array", "items": colSchema},
				"scope":   map[string]any{"type": "string", "enum": []string{"project"}, "description": "Deprecated compatibility field; tables are always project-confined."},
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
			Name:        "indexes_create",
			Description: "Create a safe composite index. Args: table, name, columns ([name | {col, order?}]), unique?. Arbitrary SQL expressions are not accepted. Returns {index}.",
			InputSchema: schemaObject(map[string]any{
				"table":   map[string]any{"type": "string"},
				"name":    map[string]any{"type": "string"},
				"columns": map[string]any{"type": "array", "items": indexColumnSchema},
				"unique":  map[string]any{"type": "boolean"},
			}, []string{"table", "name", "columns"}),
			Handler: a.toolIndexesCreate,
		},
		{
			Name:        "indexes_list",
			Description: "List user and rows_upsert-managed indexes for one table. Args: table. Returns {indexes}.",
			InputSchema: schemaObject(map[string]any{
				"table": map[string]any{"type": "string"},
			}, []string{"table"}),
			Handler: a.toolIndexesList,
		},
		{
			Name:        "indexes_drop",
			Description: "Drop a user-managed index. Args: table, name, confirm=true. Managed upsert indexes cannot be dropped.",
			InputSchema: schemaObject(map[string]any{
				"table":   map[string]any{"type": "string"},
				"name":    map[string]any{"type": "string"},
				"confirm": map[string]any{"type": "boolean"},
			}, []string{"table", "name", "confirm"}),
			Handler: a.toolIndexesDrop,
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
			Name:        "rows_upsert",
			Description: "Insert or update rows by a caller-supplied key. Args: table, key (array of column names), rows (array of objects). Existing rows matching the key are patched; missing rows are inserted. Returns {ids, inserted, updated}.",
			InputSchema: schemaObject(map[string]any{
				"table": map[string]any{"type": "string"},
				"key":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"rows":  map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			}, []string{"table", "key", "rows"}),
			Handler: a.toolRowsUpsert,
		},
		{
			Name:        "rows_get",
			Description: "Fetch one row by id. Args: table, id, select? (list of column names — defaults to all columns; reserved id/created_at/updated_at are valid picks), hydrate_files? (resolve file_id columns to {id, url}). Returns {row, found}.",
			InputSchema: schemaObject(map[string]any{
				"table":         map[string]any{"type": "string"},
				"id":            map[string]any{"type": "integer"},
				"select":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
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
			Description: "Filter, sort, paginate. Args: table, where?, order_by?, limit?, offset?, select?, include_total? (default true). Set include_total=false to skip COUNT and return has_more. Returns {rows, total?, has_more, truncated}.",
			InputSchema: schemaObject(map[string]any{
				"table":         map[string]any{"type": "string"},
				"where":         whereSchema,
				"order_by":      map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
				"offset":        map[string]any{"type": "integer"},
				"select":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"include_total": map[string]any{"type": "boolean"},
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
			Name:        "rows_aggregate",
			Description: "Single-table grouped aggregations. Args: table, where?, group_by? (column names or {col, bucket: day|week|month|year, name?}), metrics ([{name, op, col?, distinct?, numerator?, denominator?}]), order_by?, limit?. Ops: count, sum, avg, min, max, avg_ratio. Returns {rows, truncated}.",
			InputSchema: schemaObject(map[string]any{
				"table":    map[string]any{"type": "string"},
				"where":    whereSchema,
				"group_by": groupBySchema,
				"metrics":  metricSchema,
				"order_by": map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
			}, []string{"table", "metrics"}),
			Handler: a.toolRowsAggregate,
		},
		{
			Name:        "tables_query",
			Description: "Read-only SELECT escape hatch. Args: sql, params? (array). User tables must use {table_name}; internal and physical tables are inaccessible. SQLite read-only mode plus row, byte, and duration caps are enforced. Returns {columns, rows, truncated}.",
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
