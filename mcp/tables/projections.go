package main

// Persistent SQL projections. A projection is a read-only logical table whose
// rows are rebuilt asynchronously from one or more Tables-owned source tables.
// Source writes only append a compact change record through SQLite triggers;
// the worker coalesces those records by projection scope before executing SQL.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	projectionAllScope      = "__all__"
	projectionChangeBatch   = 512
	projectionQueueBatch    = 8
	projectionLeaseSeconds  = 60
	projectionWorkerEvery   = "@every 1s"
	projectionResultNameMax = 64
)

type projectionDefinition struct {
	ID           int64
	ProjectID    string
	Name         string
	Version      int
	Status       string
	SQL          string
	SourceTables []string
	SourceIDs    []int64
	ResultTable  string
	ResultCols   []Column
	ScopeCols    []string
}

type projectionQueueItem struct {
	ProjectionID int64
	ProjectID    string
	ScopeKey     string
	PendingID    int64
}

// loadQueryTable resolves ordinary user tables and read-only projection tables
// for the shared tables_query path. Projections intentionally do not enter the
// user-table metadata cache or row-write handlers.
func (a *App) loadQueryTable(ctx *sdk.AppCtx, projectID, name string) (*Table, error) {
	a.projectionMu.RLock()
	if cached := a.projectionCache[schemaCacheKey{projectID: projectID, tableName: name}]; cached != nil {
		t := cloneTable(cached)
		a.projectionMu.RUnlock()
		return t, nil
	}
	a.projectionMu.RUnlock()
	t, err := a.loadTableSchema(ctx, projectID, name)
	if err == nil {
		return t, nil
	}
	var status *statusError
	if !errors.As(err, &status) || status.status != 404 {
		return nil, err
	}
	p, err := a.loadProjection(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	t = &Table{ID: -p.ID, Name: p.Name, Scope: "project", PhysicalName: p.ResultTable, Columns: p.ResultCols}
	a.projectionMu.Lock()
	if a.projectionCache == nil {
		a.projectionCache = map[schemaCacheKey]*Table{}
	}
	a.projectionCache[schemaCacheKey{projectID: projectID, tableName: name}] = cloneTable(t)
	a.projectionMu.Unlock()
	return t, nil
}

func (a *App) projectionTools() []sdk.Tool {
	projectionColumnSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":     map[string]any{"type": "string"},
			"type":     map[string]any{"type": "string", "enum": []string{"text", "number", "bool", "datetime", "json", "file_id"}},
			"nullable": map[string]any{"type": "boolean"},
		},
		"required": []string{"name", "type"},
	}
	return []sdk.Tool{
		{
			Name:        "projections_create",
			Description: "Create a versioned read-only SQL projection. SQL must be SELECT/WITH only, source tables are explicit, and scope columns must be returned by the query.",
			InputSchema: schemaObject(map[string]any{
				"name":           map[string]any{"type": "string"},
				"version":        map[string]any{"type": "integer", "minimum": 1},
				"sql":            map[string]any{"type": "string"},
				"source_tables":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"result_columns": map[string]any{"type": "array", "items": projectionColumnSchema},
				"scope_columns":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}, []string{"name", "version", "sql", "source_tables", "result_columns"}),
			Handler: a.toolProjectionsCreate,
		},
		{
			Name:        "projections_list",
			Description: "List project-scoped SQL projections and refresh status.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolProjectionsList,
		},
		{
			Name:        "projections_describe",
			Description: "Describe one projection definition, dependencies, result schema, and status. Args: name.",
			InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string"}}, []string{"name"}),
			Handler:     a.toolProjectionsDescribe,
		},
		{
			Name:        "projections_refresh",
			Description: "Queue a projection rebuild or one affected scope. Args: name, scope? keyed by scope_columns, rebuild?.",
			InputSchema: schemaObject(map[string]any{
				"name":    map[string]any{"type": "string"},
				"scope":   map[string]any{"type": "object"},
				"rebuild": map[string]any{"type": "boolean"},
			}, []string{"name"}),
			Handler: a.toolProjectionsRefresh,
		},
		{
			Name:        "projections_status",
			Description: "Inspect projection backlog, freshness, watermarks, retries, and errors. Args: name.",
			InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string"}}, []string{"name"}),
			Handler:     a.toolProjectionsStatus,
		},
		{
			Name:        "projections_pause",
			Description: "Pause or resume projection processing. Args: name, paused.",
			InputSchema: schemaObject(map[string]any{
				"name":   map[string]any{"type": "string"},
				"paused": map[string]any{"type": "boolean"},
			}, []string{"name", "paused"}),
			Handler: a.toolProjectionsPause,
		},
		{
			Name:        "projections_delete",
			Description: "Delete a projection definition and its stored result rows. Args: name, confirm=true.",
			InputSchema: schemaObject(map[string]any{
				"name":    map[string]any{"type": "string"},
				"confirm": map[string]any{"type": "boolean"},
			}, []string{"name", "confirm"}),
			Handler: a.toolProjectionsDelete,
		},
	}
}

func (a *App) loadProjection(ctx *sdk.AppCtx, projectID, name string) (*projectionDefinition, error) {
	if err := validateIdentifier("projection", name); err != nil {
		return nil, err
	}
	var p projectionDefinition
	var sourceRaw, colsRaw, scopeRaw string
	err := ctx.AppReadDB().QueryRowContext(requestContext(ctx), `
		SELECT id, project_id, name, version, status, sql_text, source_tables,
		       result_columns, scope_columns, result_table
		FROM projection_definitions WHERE project_id=? AND name=?`, projectID, name).
		Scan(&p.ID, &p.ProjectID, &p.Name, &p.Version, &p.Status, &p.SQL, &sourceRaw, &colsRaw, &scopeRaw, &p.ResultTable)
	if err == sql.ErrNoRows {
		return nil, notFound("projection %q not found", name)
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(sourceRaw), &p.SourceTables); err != nil {
		return nil, fmt.Errorf("projection %q source metadata: %w", name, err)
	}
	if err := json.Unmarshal([]byte(colsRaw), &p.ResultCols); err != nil {
		return nil, fmt.Errorf("projection %q result schema: %w", name, err)
	}
	if err := json.Unmarshal([]byte(scopeRaw), &p.ScopeCols); err != nil {
		return nil, fmt.Errorf("projection %q scope schema: %w", name, err)
	}
	rows, err := ctx.AppReadDB().QueryContext(requestContext(ctx), `SELECT table_id FROM projection_sources WHERE projection_id=? ORDER BY table_id`, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		p.SourceIDs = append(p.SourceIDs, id)
	}
	return &p, rows.Err()
}

func projectionColumnsJSON(cols []Column) (string, error) {
	b, err := json.Marshal(cols)
	return string(b), err
}

func projectionStringSliceJSON(values []string) (string, error) {
	b, err := json.Marshal(values)
	return string(b), err
}

func (a *App) toolProjectionsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_create", true)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if err := validateIdentifier("projection", name); err != nil {
		return nil, err
	}
	if len(name) > projectionResultNameMax {
		return nil, errf("projection name too long")
	}
	version := intArg(args, "version", 0)
	if version < 1 {
		return nil, errf("version must be positive")
	}
	sqlText := strings.TrimSpace(strArg(args, "sql"))
	if err := validateReadOnlySQL(sqlText); err != nil {
		return nil, fmt.Errorf("projection sql: %w", err)
	}
	if projectionHasUnboundParameter(sqlText) {
		return nil, errf("projection sql cannot contain unbound parameters")
	}
	sourceNames := stringSliceArg(args, "source_tables")
	if len(sourceNames) == 0 || len(sourceNames) > 64 {
		return nil, errf("source_tables must contain 1..64 tables")
	}
	placeholders, err := placeholderNames(sqlText)
	if err != nil {
		return nil, err
	}
	seenSources := map[string]bool{}
	for _, source := range sourceNames {
		if err := validateIdentifier("source table", source); err != nil {
			return nil, err
		}
		if seenSources[source] {
			return nil, errf("duplicate source table %q", source)
		}
		seenSources[source] = true
	}
	for _, ph := range placeholders {
		if !seenSources[ph] {
			return nil, errf("sql references %q but it is not in source_tables", ph)
		}
	}
	resultCols, err := parseColumnDefs(sliceArg(args, "result_columns"))
	if err != nil {
		return nil, fmt.Errorf("result_columns: %w", err)
	}
	scopeCols := stringSliceArg(args, "scope_columns")
	if len(scopeCols) == 0 {
		scopeCols = nil
	}
	resultByName := map[string]bool{}
	for _, c := range resultCols {
		resultByName[c.Name] = true
	}
	for _, c := range scopeCols {
		if err := validateIdentifier("scope column", c); err != nil {
			return nil, err
		}
		if !resultByName[c] {
			return nil, errf("scope column %q must be present in result_columns", c)
		}
	}
	if len(scopeCols) > 32 {
		return nil, errf("scope_columns exceeds 32 columns")
	}
	for _, source := range sourceNames {
		table, err := a.loadTableSchema(ctx, pid, source)
		if err != nil {
			return nil, fmt.Errorf("source table %q: %w", source, err)
		}
		for _, scopeCol := range scopeCols {
			if columnIndex(table.Columns, scopeCol) < 0 {
				return nil, fmt.Errorf("source table %q does not contain scope column %q", source, scopeCol)
			}
		}
	}
	resolvedSQL, err := a.substitutePlaceholders(ctx, pid, sqlText)
	if err != nil {
		return nil, fmt.Errorf("projection sql: %w", err)
	}
	read, err := acquireReadConn(ctx, "projection")
	if err != nil {
		return nil, err
	}
	qctx, cancel := queryTimeoutContext(ctx)
	authErr := authorizeQuery(qctx, read.conn, ctx, a, pid, sqlText, resolvedSQL, nil)
	cancel()
	read.close()
	if authErr != nil {
		return nil, fmt.Errorf("projection sql authorization: %w", authErr)
	}
	if _, err := a.loadTableSchema(ctx, pid, name); err == nil {
		return nil, errf("a user table named %q already exists", name)
	} else if e, ok := err.(*statusError); !ok || e.status != 404 {
		return nil, err
	}
	sourcesJSON, err := projectionStringSliceJSON(sourceNames)
	if err != nil {
		return nil, err
	}
	colsJSON, err := projectionColumnsJSON(resultCols)
	if err != nil {
		return nil, err
	}
	scopeJSON, err := projectionStringSliceJSON(scopeCols)
	if err != nil {
		return nil, err
	}
	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM projection_definitions WHERE project_id=? AND name=?`, pid, name).Scan(&exists); err == nil {
		return nil, errf("projection %q already exists; create a new name for a new version", name)
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	res, err := tx.Exec(`INSERT INTO projection_definitions(project_id,name,version,status,sql_text,source_tables,result_columns,scope_columns,result_table) VALUES(?,?,?,?,?,?,?,?,?)`, pid, name, version, "active", sqlText, sourcesJSON, colsJSON, scopeJSON, "pending")
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	physical := fmt.Sprintf("p_%d", id)
	if _, err := tx.Exec(`UPDATE projection_definitions SET result_table=? WHERE id=?`, physical, id); err != nil {
		return nil, err
	}
	createSQL, err := buildCreateTableSQL(physical, resultCols)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(createSQL); err != nil {
		return nil, err
	}
	for _, source := range sourceNames {
		var tableID int64
		if err := tx.QueryRow(`SELECT id FROM tables_meta WHERE project_id=? AND name=?`, pid, source).Scan(&tableID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO projection_sources(projection_id,table_id) VALUES(?,?)`, id, tableID); err != nil {
			return nil, err
		}
	}
	var currentChange int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(change_id),0) FROM projection_changes WHERE project_id=?`, pid).Scan(&currentChange); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO projection_cursors(projection_id,project_id,last_change_id) VALUES(?,?,?)`, id, pid, currentChange); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO projection_queue(projection_id,project_id,scope_key,pending_change_id) VALUES(?,?,?,?)`, id, pid, projectionAllScope, currentChange); err != nil {
		return nil, err
	}
	for _, source := range sourceNames {
		var tableID int64
		if err := tx.QueryRow(`SELECT id FROM tables_meta WHERE project_id=? AND name=?`, pid, source).Scan(&tableID); err != nil {
			return nil, err
		}
		if err := rebuildProjectionTriggersTx(tx, tableID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "name": name, "version": version, "result_table": name, "status": "active", "queued": true}, nil
}

func (a *App) toolProjectionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_list", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppReadDB().QueryContext(requestContext(ctx), `SELECT id,name,version,status,created_at,updated_at FROM projection_definitions WHERE project_id=? ORDER BY name`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type projectionListItem struct {
		id      int64
		name    string
		version int64
		status  string
		created string
		updated string
	}
	var items []projectionListItem
	for rows.Next() {
		var id, version int64
		var name, status, created, updated string
		if err := rows.Scan(&id, &name, &version, &status, &created, &updated); err != nil {
			return nil, err
		}
		items = append(items, projectionListItem{id: id, name: name, version: version, status: status, created: created, updated: updated})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var queued int64
		_ = ctx.AppReadDB().QueryRowContext(requestContext(ctx), `SELECT COUNT(*) FROM projection_queue WHERE projection_id=? AND project_id=?`, item.id, pid).Scan(&queued)
		out = append(out, map[string]any{"id": item.id, "name": item.name, "version": item.version, "status": item.status, "queued": queued, "created_at": item.created, "updated_at": item.updated})
	}
	return map[string]any{"projections": out}, rows.Err()
}

func projectionDefinitionMap(p *projectionDefinition) map[string]any {
	return map[string]any{
		"id": p.ID, "name": p.Name, "version": p.Version, "status": p.Status,
		"sql": p.SQL, "source_tables": p.SourceTables, "result_columns": p.ResultCols,
		"scope_columns": p.ScopeCols, "result_table": p.Name,
	}
}

func (a *App) toolProjectionsDescribe(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_describe", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := a.loadProjection(ctx, pid, strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	out := projectionDefinitionMap(p)
	status, _ := a.projectionStatus(ctx, p)
	for k, v := range status {
		out[k] = v
	}
	return out, nil
}

func (a *App) toolProjectionsRefresh(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_refresh", true)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := a.loadProjection(ctx, pid, strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	scopeKey := projectionAllScope
	if !boolArg(args, "rebuild") {
		if scope := mapArg(args, "scope"); len(scope) > 0 {
			scopeKey, err = makeScopeKey(scope, p.ScopeCols)
			if err != nil {
				return nil, err
			}
		}
	}
	if err := queueProjectionScope(ctx, p, scopeKey, 0); err != nil {
		return nil, err
	}
	return map[string]any{"queued": true, "name": p.Name, "scope": scopeKey}, nil
}

func (a *App) toolProjectionsPause(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_pause", true)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	paused := boolArg(args, "paused")
	res, err := ctx.AppDB().ExecContext(requestContext(ctx), `UPDATE projection_definitions SET status=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND name=?`, map[bool]string{true: "paused", false: "active"}[paused], pid, name)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, notFound("projection %q not found", name)
	}
	return map[string]any{"name": name, "status": map[bool]string{true: "paused", false: "active"}[paused]}, nil
}

func (a *App) toolProjectionsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_delete", true)
	if err != nil {
		return nil, err
	}
	defer finish()
	if !boolArg(args, "confirm") {
		return nil, errf("confirm=true required")
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := a.loadProjection(ctx, pid, strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + quote(p.ResultTable)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM projection_definitions WHERE id=?`, p.ID); err != nil {
		return nil, err
	}
	for _, tableID := range p.SourceIDs {
		if err := rebuildProjectionTriggersTx(tx, tableID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.projectionMu.Lock()
	delete(a.projectionCache, schemaCacheKey{projectID: pid, tableName: p.Name})
	a.projectionMu.Unlock()
	return map[string]any{"deleted": p.Name}, nil
}

func (a *App) toolProjectionsStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_status", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := a.loadProjection(ctx, pid, strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	return a.projectionStatus(ctx, p)
}

func (a *App) projectionStatus(ctx *sdk.AppCtx, p *projectionDefinition) (map[string]any, error) {
	var queued, failed int64
	if err := ctx.AppReadDB().QueryRowContext(requestContext(ctx), `SELECT COUNT(*),COALESCE(SUM(CASE WHEN last_error IS NOT NULL THEN 1 ELSE 0 END),0) FROM projection_queue WHERE projection_id=? AND project_id=?`, p.ID, p.ProjectID).Scan(&queued, &failed); err != nil {
		return nil, err
	}
	var cursor int64
	_ = ctx.AppReadDB().QueryRowContext(requestContext(ctx), `SELECT last_change_id FROM projection_cursors WHERE projection_id=? AND project_id=?`, p.ID, p.ProjectID).Scan(&cursor)
	var latest int64
	_ = ctx.AppReadDB().QueryRowContext(requestContext(ctx), `SELECT COALESCE(MAX(change_id),0) FROM projection_changes WHERE project_id=?`, p.ProjectID).Scan(&latest)
	var computed sql.NullString
	_ = ctx.AppReadDB().QueryRowContext(requestContext(ctx), `SELECT MAX(computed_at) FROM projection_scopes WHERE projection_id=? AND project_id=?`, p.ID, p.ProjectID).Scan(&computed)
	out := map[string]any{"name": p.Name, "status": p.Status, "queued": queued, "failed": failed, "change_cursor": cursor, "latest_change": latest, "lag": latest - cursor}
	if computed.Valid {
		out["computed_at"] = computed.String
	}
	return out, nil
}

func stringSliceArg(args map[string]any, key string) []string {
	raw := sliceArg(args, key)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func projectionHasUnboundParameter(sqlText string) bool {
	tokens, err := sqlTokens(sqlText)
	if err != nil {
		return true
	}
	for _, token := range tokens {
		if token.kind == "symbol" && token.value == "?" {
			return true
		}
	}
	return false
}

func makeScopeKey(scope map[string]any, columns []string) (string, error) {
	if len(columns) == 0 {
		return projectionAllScope, nil
	}
	ordered := map[string]any{}
	for _, col := range columns {
		v, ok := scope[col]
		if !ok {
			return "", errf("scope column %q is required", col)
		}
		ordered[col] = v
	}
	b, err := json.Marshal(ordered)
	return string(b), err
}

func scopeKeyFromPayload(raw sql.NullString, columns []string) (string, bool) {
	if len(columns) == 0 || !raw.Valid || raw.String == "" {
		if len(columns) == 0 {
			return projectionAllScope, true
		}
		return "", false
	}
	var values map[string]any
	if json.Unmarshal([]byte(raw.String), &values) != nil {
		return "", false
	}
	for _, col := range columns {
		if _, ok := values[col]; !ok {
			return "", false
		}
	}
	key, err := makeScopeKey(values, columns)
	return key, err == nil
}

func scopeValues(scopeKey string, columns []string) ([]any, error) {
	var values map[string]any
	if err := json.Unmarshal([]byte(scopeKey), &values); err != nil {
		return nil, err
	}
	out := make([]any, len(columns))
	for i, c := range columns {
		v, ok := values[c]
		if !ok {
			return nil, errf("scope key missing %q", c)
		}
		out[i] = v
	}
	return out, nil
}

func queueProjectionScope(ctx *sdk.AppCtx, p *projectionDefinition, scopeKey string, changeID int64) error {
	_, err := ctx.AppDB().ExecContext(requestContext(ctx), `INSERT INTO projection_queue(projection_id,project_id,scope_key,pending_change_id) VALUES(?,?,?,?) ON CONFLICT(projection_id,project_id,scope_key) DO UPDATE SET pending_change_id=MAX(pending_change_id,excluded.pending_change_id),claimed_until=NULL,last_error=NULL,queued_at=CURRENT_TIMESTAMP`, p.ID, p.ProjectID, scopeKey, changeID)
	return err
}

func rebuildProjectionTriggersTx(tx *writeTx, tableID int64) error {
	var physical string
	if err := tx.QueryRow(`SELECT physical_name FROM tables_meta WHERE id=?`, tableID).Scan(&physical); err != nil {
		return err
	}
	for _, suffix := range []string{"insert", "update", "delete"} {
		if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ` + quote(fmt.Sprintf("projection_change_%d_%s", tableID, suffix))); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT DISTINCT c.name FROM projection_definitions p JOIN projection_sources s ON s.projection_id=p.id JOIN json_each(p.scope_columns) j JOIN columns_meta c ON c.table_id=? AND c.name=j.value WHERE s.table_id=? AND p.status='active' ORDER BY c.name`, tableID, tableID)
	if err != nil {
		return err
	}
	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			rows.Close()
			return err
		}
		cols = append(cols, col)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(cols) == 0 {
		var dependent int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM projection_sources s JOIN projection_definitions p ON p.id=s.projection_id WHERE s.table_id=? AND p.status='active'`, tableID).Scan(&dependent); err != nil {
			return err
		}
		if dependent == 0 {
			return nil
		}
	}
	jsonExpr := func(prefix string) string {
		parts := make([]string, 0, len(cols)*2)
		for _, col := range cols {
			parts = append(parts, fmt.Sprintf("'%s'", strings.ReplaceAll(col, "'", "''")), prefix+"."+quote(col))
		}
		return "json_object(" + strings.Join(parts, ",") + ")"
	}
	base := `INSERT INTO projection_changes(project_id,table_id,row_id,operation,old_values,new_values) SELECT project_id,%d,%s,%s,%s,%s FROM tables_meta WHERE id=%d`
	insertSQL := fmt.Sprintf("CREATE TRIGGER %s AFTER INSERT ON %s BEGIN "+base+"; END", quote(fmt.Sprintf("projection_change_%d_insert", tableID)), quote(physical), tableID, "NEW.id", "'insert'", "NULL", jsonExpr("NEW"), tableID)
	updateSQL := fmt.Sprintf("CREATE TRIGGER %s AFTER UPDATE ON %s BEGIN "+base+"; END", quote(fmt.Sprintf("projection_change_%d_update", tableID)), quote(physical), tableID, "NEW.id", "'update'", jsonExpr("OLD"), jsonExpr("NEW"), tableID)
	deleteSQL := fmt.Sprintf("CREATE TRIGGER %s AFTER DELETE ON %s BEGIN "+base+"; END", quote(fmt.Sprintf("projection_change_%d_delete", tableID)), quote(physical), tableID, "OLD.id", "'delete'", jsonExpr("OLD"), "NULL", tableID)
	for _, statement := range []string{insertSQL, updateSQL, deleteSQL} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) rebuildAllProjectionTriggers(ctx *sdk.AppCtx) error {
	tx, err := beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM tables_meta WHERE project_id<>'' ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := rebuildProjectionTriggersTx(tx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) projectionWorker(ctx context.Context, app *sdk.AppCtx) error {
	pid := app.CurrentProject()
	if pid == "" {
		return nil
	}
	if err := a.consumeProjectionChanges(ctx, app, pid); err != nil {
		return err
	}
	for i := 0; i < projectionQueueBatch; i++ {
		item, ok, err := claimProjectionQueue(ctx, app, pid)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if err := a.refreshProjectionScope(ctx, app, item); err != nil {
			_ = failProjectionQueue(app, item, err)
		}
	}
	return nil
}

func (a *App) consumeProjectionChanges(ctx context.Context, app *sdk.AppCtx, pid string) error {
	defs, err := loadActiveProjections(app, pid)
	if err != nil {
		return err
	}
	for _, p := range defs {
		var cursor int64
		if err := app.AppReadDB().QueryRowContext(ctx, `SELECT last_change_id FROM projection_cursors WHERE projection_id=? AND project_id=?`, p.ID, pid).Scan(&cursor); err != nil {
			continue
		}
		rows, err := app.AppReadDB().QueryContext(ctx, `SELECT c.change_id,c.old_values,c.new_values FROM projection_changes c JOIN projection_sources s ON s.table_id=c.table_id WHERE s.projection_id=? AND c.project_id=? AND c.change_id>? ORDER BY c.change_id LIMIT ?`, p.ID, pid, cursor, projectionChangeBatch)
		if err != nil {
			return err
		}
		type change struct {
			id        int64
			old, next sql.NullString
		}
		changes := []change{}
		for rows.Next() {
			var c change
			if err := rows.Scan(&c.id, &c.old, &c.next); err != nil {
				rows.Close()
				return err
			}
			changes = append(changes, c)
		}
		rows.Close()
		if len(changes) == 0 {
			continue
		}
		tx, err := app.AppDB().BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, c := range changes {
			keys := map[string]bool{}
			if key, ok := scopeKeyFromPayload(c.old, p.ScopeCols); ok {
				keys[key] = true
			}
			if key, ok := scopeKeyFromPayload(c.next, p.ScopeCols); ok {
				keys[key] = true
			}
			if len(keys) == 0 {
				keys[projectionAllScope] = true
			}
			for key := range keys {
				if _, err := tx.ExecContext(ctx, `INSERT INTO projection_queue(projection_id,project_id,scope_key,pending_change_id) VALUES(?,?,?,?) ON CONFLICT(projection_id,project_id,scope_key) DO UPDATE SET pending_change_id=MAX(pending_change_id,excluded.pending_change_id),claimed_until=NULL,last_error=NULL,queued_at=CURRENT_TIMESTAMP`, p.ID, pid, key, c.id); err != nil {
					tx.Rollback()
					return err
				}
			}
		}
		last := changes[len(changes)-1].id
		if _, err := tx.ExecContext(ctx, `UPDATE projection_cursors SET last_change_id=? WHERE projection_id=? AND project_id=?`, last, p.ID, pid); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func loadActiveProjections(ctx *sdk.AppCtx, pid string) ([]*projectionDefinition, error) {
	rows, err := ctx.AppReadDB().QueryContext(requestContext(ctx), `SELECT name FROM projection_definitions WHERE project_id=? AND status='active' ORDER BY id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []*projectionDefinition
	for _, name := range names {
		// The caller only needs the definition; loading by name keeps all JSON
		// decoding and source metadata in one place.
		p, err := loadProjectionFromDB(ctx, pid, name)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func loadProjectionFromDB(ctx *sdk.AppCtx, pid, name string) (*projectionDefinition, error) {
	var p projectionDefinition
	var sourceRaw, colsRaw, scopeRaw string
	err := ctx.AppReadDB().QueryRowContext(requestContext(ctx), `SELECT id,project_id,name,version,status,sql_text,source_tables,result_columns,scope_columns,result_table FROM projection_definitions WHERE project_id=? AND name=?`, pid, name).Scan(&p.ID, &p.ProjectID, &p.Name, &p.Version, &p.Status, &p.SQL, &sourceRaw, &colsRaw, &scopeRaw, &p.ResultTable)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(sourceRaw), &p.SourceTables); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(colsRaw), &p.ResultCols); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(scopeRaw), &p.ScopeCols); err != nil {
		return nil, err
	}
	return &p, nil
}

func claimProjectionQueue(ctx context.Context, app *sdk.AppCtx, pid string) (projectionQueueItem, bool, error) {
	tx, err := app.AppDB().BeginTx(ctx, nil)
	if err != nil {
		return projectionQueueItem{}, false, err
	}
	defer tx.Rollback()
	var item projectionQueueItem
	err = tx.QueryRowContext(ctx, `SELECT projection_id,project_id,scope_key,pending_change_id FROM projection_queue WHERE project_id=? AND (claimed_until IS NULL OR claimed_until<CURRENT_TIMESTAMP) ORDER BY queued_at LIMIT 1`, pid).Scan(&item.ProjectionID, &item.ProjectID, &item.ScopeKey, &item.PendingID)
	if err == sql.ErrNoRows {
		return projectionQueueItem{}, false, nil
	}
	if err != nil {
		return projectionQueueItem{}, false, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE projection_queue SET claimed_until=datetime('now',?),attempts=attempts+1 WHERE projection_id=? AND project_id=? AND scope_key=? AND (claimed_until IS NULL OR claimed_until<CURRENT_TIMESTAMP)`, fmt.Sprintf("+%d seconds", projectionLeaseSeconds), item.ProjectionID, item.ProjectID, item.ScopeKey)
	if err != nil {
		return projectionQueueItem{}, false, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return projectionQueueItem{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return projectionQueueItem{}, false, err
	}
	return item, true, nil
}

func failProjectionQueue(app *sdk.AppCtx, item projectionQueueItem, refreshErr error) error {
	_, err := app.AppDB().ExecContext(requestContext(app), `UPDATE projection_queue SET claimed_until=datetime('now','+5 seconds'),last_error=? WHERE projection_id=? AND project_id=? AND scope_key=?`, refreshErr.Error(), item.ProjectionID, item.ProjectID, item.ScopeKey)
	return err
}

func projectionResultValues(row map[string]any, cols []Column) ([]any, error) {
	values := make([]any, len(cols))
	for i, col := range cols {
		v, ok := row[col.Name]
		if !ok {
			if col.Nullable {
				values[i] = nil
				continue
			}
			return nil, errf("projection result missing column %q", col.Name)
		}
		coerced, err := coerceForStorage(col, v)
		if err != nil {
			return nil, err
		}
		values[i] = coerced
	}
	return values, nil
}

func (a *App) refreshProjectionScope(parent context.Context, app *sdk.AppCtx, item projectionQueueItem) error {
	p, err := loadProjectionFromDB(app, item.ProjectID, projectionNameByID(app, item.ProjectionID))
	if err != nil {
		return err
	}
	if p.Status != "active" {
		return nil
	}
	resolved, err := a.substitutePlaceholders(app, item.ProjectID, p.SQL)
	if err != nil {
		return err
	}
	query := resolved
	params := []any{}
	if item.ScopeKey != projectionAllScope && len(p.ScopeCols) > 0 {
		values, err := scopeValues(item.ScopeKey, p.ScopeCols)
		if err != nil {
			return err
		}
		parts := make([]string, 0, len(p.ScopeCols))
		for i, c := range p.ScopeCols {
			if values[i] == nil {
				parts = append(parts, quote(c)+" IS NULL")
				continue
			}
			parts = append(parts, quote(c)+" = ?")
			params = append(params, values[i])
		}
		query = `SELECT * FROM (` + resolved + `) AS __projection_scope WHERE ` + strings.Join(parts, " AND ")
	}
	qctx, cancel := context.WithTimeout(parent, time.Duration(maxProjectionMs(app))*time.Millisecond)
	defer cancel()
	read, err := acquireReadConn(app, p.Name)
	if err != nil {
		return err
	}
	if err := authorizeQuery(qctx, read.conn, app, a, item.ProjectID, p.SQL, query, params); err != nil {
		read.close()
		return fmt.Errorf("projection authorization: %w", err)
	}
	rows, err := read.queryer().QueryContext(qctx, query, params...)
	if err != nil {
		read.close()
		return err
	}
	columns, err := rows.Columns()
	if err != nil {
		rows.Close()
		read.close()
		return err
	}
	colIndex := map[string]int{}
	for i, name := range columns {
		colIndex[name] = i
	}
	for _, c := range p.ResultCols {
		if _, ok := colIndex[c.Name]; !ok {
			return errf("projection query did not return result column %q", c.Name)
		}
	}
	grouped := map[string][]map[string]any{}
	for rows.Next() {
		dest := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			read.close()
			return err
		}
		row := map[string]any{}
		for _, c := range p.ResultCols {
			row[c.Name] = hydrateForResult(c, dest[colIndex[c.Name]])
		}
		values, err := projectionResultValues(row, p.ResultCols)
		if err != nil {
			rows.Close()
			read.close()
			return err
		}
		for i, c := range p.ResultCols {
			row[c.Name] = values[i]
		}
		key := item.ScopeKey
		if item.ScopeKey == projectionAllScope {
			var ok bool
			key, ok = makeScopeKeyFromRow(row, p.ScopeCols)
			if !ok {
				return errf("projection row is missing scope columns")
			}
		}
		grouped[key] = append(grouped[key], row)
		if len(grouped[key]) > maxProjectionRows(app) {
			return errf("projection scope exceeds max_projection_rows")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		read.close()
		return err
	}
	if err := rows.Close(); err != nil {
		read.close()
		return err
	}
	read.close()
	if item.ScopeKey != projectionAllScope {
		if _, ok := grouped[item.ScopeKey]; !ok {
			grouped[item.ScopeKey] = []map[string]any{}
		}
	}
	return publishProjectionRows(app, p, item, grouped)
}

func makeScopeKeyFromRow(row map[string]any, columns []string) (string, bool) {
	if len(columns) == 0 {
		return projectionAllScope, true
	}
	values := map[string]any{}
	for _, c := range columns {
		v, ok := row[c]
		if !ok {
			return "", false
		}
		values[c] = v
	}
	key, err := makeScopeKey(values, columns)
	return key, err == nil
}

func publishProjectionRows(app *sdk.AppCtx, p *projectionDefinition, item projectionQueueItem, grouped map[string][]map[string]any) error {
	tx, err := app.AppDB().BeginTx(requestContext(app), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if item.ScopeKey == projectionAllScope {
		if _, err := tx.Exec(`DELETE FROM `+quote(p.ResultTable)+` WHERE id IN (SELECT result_id FROM projection_result_index WHERE projection_id=? AND project_id=?)`, p.ID, p.ProjectID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM projection_result_index WHERE projection_id=? AND project_id=?`, p.ID, p.ProjectID); err != nil {
			return err
		}
	}
	for scopeKey, rows := range grouped {
		if _, err := tx.Exec(`DELETE FROM `+quote(p.ResultTable)+` WHERE id IN (SELECT result_id FROM projection_result_index WHERE projection_id=? AND project_id=? AND scope_key=?)`, p.ID, p.ProjectID, scopeKey); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM projection_result_index WHERE projection_id=? AND project_id=? AND scope_key=?`, p.ID, p.ProjectID, scopeKey); err != nil {
			return err
		}
		for _, row := range rows {
			values, err := projectionResultValues(row, p.ResultCols)
			if err != nil {
				return err
			}
			quoted := make([]string, len(p.ResultCols))
			for i, c := range p.ResultCols {
				quoted[i] = quote(c.Name)
			}
			marks := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
			res, err := tx.Exec(`INSERT INTO `+quote(p.ResultTable)+` (`+strings.Join(quoted, ",")+") VALUES ("+marks+")", values...)
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO projection_result_index(projection_id,project_id,scope_key,result_id) VALUES(?,?,?,?)`, p.ID, p.ProjectID, scopeKey, id); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO projection_scopes(projection_id,project_id,scope_key,processed_change_id,computed_at,status,last_error) VALUES(?,?,?, ?,CURRENT_TIMESTAMP,'ready',NULL) ON CONFLICT(projection_id,project_id,scope_key) DO UPDATE SET processed_change_id=excluded.processed_change_id,computed_at=excluded.computed_at,status='ready',last_error=NULL`, p.ID, p.ProjectID, item.ScopeKey, item.PendingID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM projection_queue WHERE projection_id=? AND project_id=? AND scope_key=? AND pending_change_id<=?`, p.ID, p.ProjectID, item.ScopeKey, item.PendingID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE projection_queue SET claimed_until=NULL,last_error=NULL WHERE projection_id=? AND project_id=? AND scope_key=? AND pending_change_id>?`, p.ID, p.ProjectID, item.ScopeKey, item.PendingID); err != nil {
		return err
	}
	return tx.Commit()
}

func projectionNameByID(app *sdk.AppCtx, id int64) string {
	var name string
	_ = app.AppReadDB().QueryRowContext(requestContext(app), `SELECT name FROM projection_definitions WHERE id=?`, id).Scan(&name)
	return name
}

func maxProjectionMs(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "max_projection_ms", 30000, 100, 300000))
}

func maxProjectionRows(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "max_projection_rows", 100000, 1, 1000000))
}
