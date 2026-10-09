package main

// Projection definitions, durable invalidations and publication are owned by
// Tables. Heavy SQL never runs in a source-write transaction.
import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

const (
	projectionAllScope               = "__all__"
	projectionChangeBatch            = 512
	projectionQueueBatch             = 64
	projectionWorkerEvery            = "@every 1s"
	projectionScopeCoalesceThreshold = 256
	projectionWorkerBudget           = 900 * time.Millisecond
	projectionWorkerGlobalBudget     = 2 * time.Second
	projectionEventBatch             = 32
	projectionEventBudget            = 100 * time.Millisecond
)

type projectionDefinition struct {
	ID                                                                                                   int64
	ProjectID, Name, Status, SQL, ResultTable                                                            string
	Version                                                                                              int
	SourceTables                                                                                         []string
	SourceIDs                                                                                            []int64
	ResultCols                                                                                           []Column
	ScopeCols                                                                                            []string
	Options                                                                                              projectionOptions
	Current, Built                                                                                       bool
	Latest, Published                                                                                    int64
	PublishedAt                                                                                          sql.NullInt64
	LastFailure                                                                                          sql.NullString
	QueueMs, WorkerQueueMs, ReadQueueMs, CalculationMs, WriteLockMs, StagingMs, PublicationMs, CleanupMs int64
	Format                                                                                               int
}
type projectionPhaseMetrics struct {
	Queue, WorkerQueue, ReadQueue, Calculation, WriteLock, Staging, Publication, Cleanup int64
}
type projectionQueuedRevision struct{ Revision, Pending int64 }
type projectionQueueItem struct {
	CoveredScopes                                         map[string]projectionQueuedRevision
	ProjectionID                                          int64
	ProjectID, ScopeKey                                   string
	PendingID, Revision                                   int64
	Attempts                                              int
	LeaseToken                                            string
	QueuedAtMs, QueueWaitMs, CalculationMs, PublicationMs int64
}

func projectionLeaseToken() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func (a *App) projectionTime() time.Time {
	if a.projectionNow != nil {
		return a.projectionNow()
	}
	return time.Now()
}

func (a *App) setProjectionMetrics(id int64, update projectionPhaseMetrics) {
	a.projectionMetricsMu.Lock()
	if a.projectionMetrics == nil {
		a.projectionMetrics = make(map[int64]projectionPhaseMetrics)
	}
	a.projectionMetrics[id] = update
	a.projectionMetricsMu.Unlock()
}

func (a *App) projectionMetricsFor(id int64) projectionPhaseMetrics {
	a.projectionMetricsMu.RLock()
	m := a.projectionMetrics[id]
	a.projectionMetricsMu.RUnlock()
	return m
}

func (a *App) recordProjectionMetrics(ctx *sdk.AppCtx, id int64, m projectionPhaseMetrics) {
	a.setProjectionMetrics(id, m)
	// 0.2.4 databases may be inspected before migration 011 has run. The
	// in-memory value remains available in that case; upgraded databases retain
	// the latest timings across restarts.
	_, _ = ctx.AppDB().ExecContext(requestContext(ctx), `UPDATE projection_definitions SET last_queue_ms=?,last_worker_queue_ms=?,last_read_queue_ms=?,last_calculation_ms=?,last_write_lock_ms=?,last_staging_ms=?,last_publication_ms=?,last_cleanup_ms=? WHERE id=?`, m.Queue, m.WorkerQueue, m.ReadQueue, m.Calculation, m.WriteLock, m.Staging, m.Publication, m.Cleanup, id)
}

func (a *App) loadStoredProjectionMetrics(ctx *sdk.AppCtx, id int64) projectionPhaseMetrics {
	var m projectionPhaseMetrics
	err := metadataReaderFor(ctx).QueryRowContext(requestContext(ctx), `SELECT last_queue_ms,last_worker_queue_ms,last_read_queue_ms,last_calculation_ms,last_write_lock_ms,last_staging_ms,last_publication_ms,last_cleanup_ms FROM projection_definitions WHERE id=?`, id).Scan(&m.Queue, &m.WorkerQueue, &m.ReadQueue, &m.Calculation, &m.WriteLock, &m.Staging, &m.Publication, &m.Cleanup)
	if err != nil {
		return a.projectionMetricsFor(id)
	}
	a.setProjectionMetrics(id, m)
	return m
}

const projectionSelect = `SELECT id,project_id,name,version,status,sql_text,source_tables,result_columns,scope_columns,result_table,options,is_current,built,latest_relevant_change,published_change,published_at_ms,last_failure,storage_format FROM projection_definitions `

func decodeProjection(ctx *sdk.AppCtx, row interface{ Scan(...any) error }) (*projectionDefinition, error) {
	p := &projectionDefinition{Options: defaultProjectionOptions(ctx)}
	var sources, cols, scopes, opts string
	if err := row.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Version, &p.Status, &p.SQL, &sources, &cols, &scopes, &p.ResultTable, &opts, &p.Current, &p.Built, &p.Latest, &p.Published, &p.PublishedAt, &p.LastFailure, &p.Format); err != nil {
		return nil, err
	}
	for _, v := range []struct {
		raw string
		out any
	}{{sources, &p.SourceTables}, {cols, &p.ResultCols}, {scopes, &p.ScopeCols}, {opts, &p.Options}} {
		if err := projectionDecode(v.raw, v.out); err != nil {
			return nil, err
		}
	}
	return p, nil
}
func loadProjectionWhere(ctx *sdk.AppCtx, clause string, args ...any) (*projectionDefinition, error) {
	rows, err := metadataReaderFor(ctx).QueryContext(requestContext(ctx), projectionSelect+clause, args...)
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		err := rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		return nil, notFound("projection not found")
	}
	p, err := decodeProjection(ctx, rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	deps, err := metadataReaderFor(ctx).QueryContext(requestContext(ctx), `SELECT table_id FROM projection_sources WHERE projection_id=?`, p.ID)
	if err != nil {
		return nil, err
	}
	defer deps.Close()
	for deps.Next() {
		var id int64
		if err := deps.Scan(&id); err != nil {
			return nil, err
		}
		p.SourceIDs = append(p.SourceIDs, id)
	}
	return p, deps.Err()
}
func (a *App) loadProjection(ctx *sdk.AppCtx, pid, name string) (*projectionDefinition, error) {
	return loadProjectionWhere(ctx, `WHERE project_id=? AND name=? AND is_current=1`, pid, name)
}
func projectionFromArgs(ctx *sdk.AppCtx, pid string, args map[string]any) (*projectionDefinition, error) {
	name := strArg(args, "name")
	if name == "" {
		name = strArg(args, "table")
	}
	if err := validateIdentifier("projection", name); err != nil {
		return nil, err
	}
	if v := intArg(args, "version", 0); v > 0 {
		return loadProjectionWhere(ctx, `WHERE project_id=? AND name=? AND version=?`, pid, name, v)
	}
	return loadProjectionWhere(ctx, `WHERE project_id=? AND name=? AND is_current=1`, pid, name)
}
func projectionTable(p *projectionDefinition) *Table {
	return &Table{ID: -p.ID, Name: p.Name, Scope: "project", PhysicalName: projectionVisibleTable(p), Columns: p.ResultCols, ProjectionID: p.ID}
}
func (a *App) loadQueryTable(ctx *sdk.AppCtx, pid, name string) (*Table, error) {
	key := schemaCacheKey{pid, name}
	if schemas, ok := requestContext(ctx).Value(batchSchemaCacheKey{}).(map[schemaCacheKey]*Table); ok {
		if t := schemas[key]; t != nil {
			if t.ProjectionID != 0 && !sdk.CallerFrom(requestContext(ctx)).Allows("projections.read", name) {
				return nil, &statusError{403, "projection permission denied"}
			}
			return cloneTable(t), nil
		}
	}
	a.projectionMu.Lock()
	if a.projectionGeneration != ctx.AppDBGeneration() {
		a.projectionCache = nil
		a.projectionGeneration = ctx.AppDBGeneration()
	}
	cached := cloneTable(a.projectionCache[key])
	a.projectionMu.Unlock()
	if cached != nil {
		if !sdk.CallerFrom(requestContext(ctx)).Allows("projections.read", name) {
			return nil, &statusError{403, "projection permission denied"}
		}
		return cached, nil
	}
	table, err := a.loadTableSchema(ctx, pid, name)
	if err == nil {
		return table, nil
	}
	var e *statusError
	if !errors.As(err, &e) || e.status != 404 {
		return nil, err
	}
	p, err := a.loadProjection(ctx, pid, name)
	if err != nil {
		return nil, err
	}
	if err := projectionIndexPermission(ctx, p, false); err != nil {
		return nil, err
	}
	table = projectionTable(p)
	a.projectionMu.Lock()
	if a.projectionCache == nil || len(a.projectionCache) >= maxSchemaCacheEntries {
		a.projectionCache = map[schemaCacheKey]*Table{}
	}
	a.projectionCache[key] = cloneTable(table)
	a.projectionMu.Unlock()
	return table, nil
}
func (a *App) invalidateProjection(pid, name string) {
	a.projectionMu.Lock()
	delete(a.projectionCache, schemaCacheKey{pid, name})
	a.projectionMu.Unlock()
	// Activation can replace the physical table behind a name, so invalidate
	// every prepared projection plan rather than only the new version's id.
	a.invalidateSQLCaches()
}
func projectionNameSchema() map[string]any {
	return map[string]any{"name": map[string]any{"type": "string"}, "version": map[string]any{"type": "integer", "minimum": 1}}
}
func (a *App) projectionTools() []sdk.Tool {
	columns := map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "type": map[string]any{"type": "string", "enum": []string{"text", "number", "bool", "datetime", "json", "file_id"}}, "nullable": map[string]any{"type": "boolean"}}, "required": []string{"name", "type"}}}
	create := projectionNameSchema()
	create["sql"] = map[string]any{"type": "string"}
	create["source_tables"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	create["result_columns"] = columns
	create["scope_columns"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	create["inherit_indexes"] = map[string]any{"type": "boolean", "description": "Copy the current version's index definitions and layouts to this replacement before building. Incompatible columns/unique scope constraints fail atomically. Default false."}
	create["activate"] = map[string]any{"type": "boolean", "description": "First version becomes readable; replacements require projections_activate after building."}
	for k, v := range projectionOptionSchema() {
		create[k] = v
	}
	refresh := projectionNameSchema()
	refresh["scope"] = map[string]any{"type": "object"}
	refresh["rebuild"] = map[string]any{"type": "boolean"}
	refresh["force"] = map[string]any{"type": "boolean"}
	status := projectionNameSchema()
	status["scope"] = map[string]any{"type": "object"}
	pause := projectionNameSchema()
	pause["paused"] = map[string]any{"type": "boolean"}
	remove := projectionNameSchema()
	remove["confirm"] = map[string]any{"type": "boolean"}
	update := projectionNameSchema()
	update["min_refresh_interval_seconds"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 86400}
	return []sdk.Tool{
		{Name: "projections_create", Description: "Create an immutable SQL projection version. Replacements build alongside current readers. Supports scoped SQL parameters, generic source mappings, watched source columns, coverage and limits. Include all calculation, filter, join and mapping inputs in watched_columns; omitted dependencies retain all-column invalidation.", InputSchema: schemaObject(create, []string{"name", "version", "sql", "source_tables", "result_columns"}), Handler: a.toolProjectionsCreate},
		{Name: "projections_list", Description: "List project projection versions and readiness.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolProjectionsList},
		{Name: "projections_describe", Description: "Describe a current or specified version and options.", InputSchema: schemaObject(projectionNameSchema(), []string{"name"}), Handler: a.toolProjectionsDescribe},
		{Name: "projections_refresh", Description: "Queue a scope or full rebuild. force=true bypasses the persisted interval, without bypassing pause or resource limits.", InputSchema: schemaObject(refresh, []string{"name"}), Handler: a.toolProjectionsRefresh},
		{Name: "projections_status", Description: "Inspect relevant changes, published watermarks, readiness, running work and next refresh; scope optional.", InputSchema: schemaObject(status, []string{"name"}), Handler: a.toolProjectionsStatus},
		{Name: "projections_pause", Description: "Pause or resume processing for a selected version; change capture continues.", InputSchema: schemaObject(pause, []string{"name", "paused"}), Handler: a.toolProjectionsPause},
		{Name: "projections_activate", Description: "Atomically switch to a fully built and current replacement version.", InputSchema: schemaObject(projectionNameSchema(), []string{"name", "version"}), Handler: a.toolProjectionsActivate},
		{Name: "projections_update", Description: "Persist a selected projection's minimum refresh interval and reschedule dirty scopes.", InputSchema: schemaObject(update, []string{"name", "min_refresh_interval_seconds"}), Handler: a.toolProjectionsUpdate},
		{Name: "projections_delete", Description: "Delete a selected version and its results.", InputSchema: schemaObject(remove, []string{"name", "confirm"}), Handler: a.toolProjectionsDelete},
	}
}
func projectionOptionSchema() map[string]any {
	out := map[string]any{"params": map[string]any{"type": "array"}, "scope_sql": map[string]any{"type": "string"}, "scope_params": map[string]any{"type": "array", "items": map[string]any{"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "object", "properties": map[string]any{"scope_column": map[string]any{"type": "string"}, "boundary": map[string]any{"type": "string", "enum": []string{"start", "end"}}, "timezone": map[string]any{"type": "string"}}, "required": []string{"scope_column", "boundary", "timezone"}}}}}, "scope_rules": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, "coverage_from": map[string]any{"type": "string"}, "coverage_to": map[string]any{"type": "string"}}
	out["source_dependencies"] = map[string]any{
		"type": "array", "maxItems": 64,
		"description": "Optional watched inputs per declared source; omitted sources keep all-column invalidation. Include every calculation, filter, join and scope/mapping input. Changes require a replacement projection version.",
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"table", "watched_columns"},
			"properties": map[string]any{
				"table":           map[string]any{"type": "string"},
				"watched_columns": map[string]any{"type": "array", "minItems": 1, "maxItems": 260, "uniqueItems": true, "items": map[string]any{"type": "string"}},
			},
		},
	}

	for _, k := range []string{"min_refresh_interval_seconds", "max_refresh_ms", "max_result_rows", "max_result_bytes", "max_publication_ms", "publication_batch_rows", "publication_batch_bytes"} {
		out[k] = map[string]any{"type": "integer", "minimum": 0}
	}
	return out
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
	version := intArg(args, "version", 0)
	if version < 1 {
		return nil, errf("version must be positive")
	}
	text := strings.TrimSpace(strArg(args, "sql"))
	opts, err := parseProjectionOptions(ctx, args, text)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, errf("sql is required")
	}
	sources, err := strictStringSliceArg(args, "source_tables")
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 || len(sources) > 64 {
		return nil, errf("source_tables requires 1..64 tables")
	}
	cols, err := parseColumnDefs(sliceArg(args, "result_columns"))
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, errf("result_columns is required")
	}
	scopes, err := strictStringSliceArg(args, "scope_columns")
	if err != nil {
		return nil, err
	}
	if len(scopes) > 32 {
		return nil, errf("scope_columns exceeds 32")
	}
	p := &projectionDefinition{ProjectID: pid, Name: name, Version: version, SQL: text, SourceTables: sources, ResultCols: cols, ScopeCols: scopes, Options: opts, Format: 1}
	if err := a.validateProjection(ctx, p); err != nil {
		return nil, err
	}
	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Acquire the writer before checking both namespaces and creating triggers.
	if _, err := tx.Exec(`UPDATE table_identity SET last_id=last_id`); err != nil {
		return nil, err
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM tables_meta WHERE project_id=? AND name=?`, pid, name).Scan(&count); err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, errf("user table %q already exists", name)
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM projection_definitions WHERE project_id=? AND name=? AND is_current=1`, pid, name).Scan(&count); err != nil {
		return nil, err
	}
	p.Current = count == 0
	if v, ok := args["activate"]; ok && !v.(bool) {
		p.Current = false
	}
	p.Status = "building"
	if p.Current {
		p.Status = "active"
	}
	rawSources, _ := json.Marshal(sources)
	rawCols, _ := json.Marshal(cols)
	rawScopes, _ := json.Marshal(scopes)
	rawOpts, _ := json.Marshal(opts)
	token, err := projectionLeaseToken()
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(`INSERT INTO projection_definitions(project_id,name,version,status,sql_text,source_tables,result_columns,scope_columns,result_table,is_current,options,storage_format) VALUES(?,?,?,?,?,?,?,?,?,?,?,2)`, pid, name, version, p.Status, text, string(rawSources), string(rawCols), string(rawScopes), "pending_"+token, p.Current, string(rawOpts))
	if err != nil {
		return nil, err
	}
	p.ID, err = res.LastInsertId()
	if err != nil {
		return nil, err
	}
	p.ResultTable = fmt.Sprintf("p_%d", p.ID)
	p.Format = 2
	if _, err := tx.Exec(`UPDATE projection_definitions SET result_table=? WHERE id=?`, p.ResultTable, p.ID); err != nil {
		return nil, err
	}
	if err := createProjectionStorage(tx, p); err != nil {
		return nil, err
	}
	if boolArg(args, "inherit_indexes") {
		if err := inheritProjectionIndexesTx(tx, p, ctx); err != nil {
			return nil, err
		}
	}
	for _, id := range p.SourceIDs {
		if _, err := tx.Exec(`INSERT INTO projection_sources(projection_id,table_id) VALUES(?,?)`, p.ID, id); err != nil {
			return nil, err
		}
	}
	var watermark int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(change_id),0) FROM projection_changes WHERE project_id=?`, pid).Scan(&watermark); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO projection_cursors(projection_id,project_id,last_change_id) VALUES(?,?,?)`, p.ID, pid, watermark); err != nil {
		return nil, err
	}
	if err := enqueueProjectionTx(requestContext(ctx), tx.Tx, p, projectionAllScope, 0, a.projectionTime().UnixMilli(), false); err != nil {
		return nil, err
	}
	for _, id := range p.SourceIDs {
		if err := rebuildProjectionTriggersTx(tx, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.invalidateProjection(pid, name)
	return map[string]any{"id": p.ID, "name": name, "version": version, "status": p.Status, "ready": false, "queued": true}, nil
}
func projectionDefinitionMap(p *projectionDefinition) map[string]any {
	out := map[string]any{"id": p.ID, "name": p.Name, "version": p.Version, "status": p.Status, "is_current": p.Current, "sql": p.SQL, "source_tables": p.SourceTables, "result_columns": p.ResultCols, "scope_columns": p.ScopeCols, "result_table": p.Name}
	raw, _ := json.Marshal(p.Options)
	var opts map[string]any
	_ = projectionDecode(string(raw), &opts)
	for k, v := range opts {
		out[k] = v
	}
	return out
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
	rows, err := ctx.AppReadDB().QueryContext(requestContext(ctx), projectionSelect+`WHERE project_id=? ORDER BY name,version`, pid)
	if err != nil {
		return nil, err
	}
	var defs []*projectionDefinition
	for rows.Next() {
		p, err := decodeProjection(ctx, rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		defs = append(defs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, p := range defs {
		s, err := a.projectionStatus(ctx, p, "")
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return map[string]any{"projections": out}, nil
}
func (a *App) toolProjectionsDescribe(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.describeProjection(ctx, args, true)
}
func (a *App) toolProjectionsStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.describeProjection(ctx, args, false)
}
func (a *App) describeProjection(ctx *sdk.AppCtx, args map[string]any, definition bool) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_status", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := projectionFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	key := ""
	if scope := mapArg(args, "scope"); scope != nil {
		key, err = projectionScopeKey(p, scope)
		if err != nil {
			return nil, err
		}
	}
	out, err := a.projectionStatus(ctx, p, key)
	if err != nil {
		return nil, err
	}
	if definition {
		for k, v := range projectionDefinitionMap(p) {
			out[k] = v
		}
	}
	return out, nil
}
func (a *App) toolProjectionsRefresh(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_refresh", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := projectionFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	key := projectionAllScope
	if !boolArg(args, "rebuild") {
		if scope := mapArg(args, "scope"); scope != nil {
			key, err = projectionScopeKey(p, scope)
			if err != nil {
				return nil, err
			}
		}
	}
	if err := a.queueProjectionScope(ctx, p, key, p.Latest, boolArg(args, "force")); err != nil {
		return nil, err
	}
	return map[string]any{"queued": true, "name": p.Name, "version": p.Version, "scope": key}, nil
}
func (a *App) toolProjectionsPause(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_pause", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := projectionFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if p.Status == "retired" {
		return nil, errf("retired versions must be rebuilt before resuming")
	}
	status := "paused"
	if !boolArg(args, "paused") {
		status = "building"
		if p.Current {
			status = "active"
		}
	}
	_, err = ctx.AppDB().ExecContext(requestContext(ctx), `UPDATE projection_definitions SET status=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, p.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": p.Name, "version": p.Version, "status": status}, nil
}
func (a *App) toolProjectionsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_update", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := projectionFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	interval, err := exactInteger(args["min_refresh_interval_seconds"])
	if err != nil || interval < 0 || interval > 86400 {
		return nil, errf("min_refresh_interval_seconds must be 0..86400")
	}
	p.Options.Interval = interval
	raw, _ := json.Marshal(p.Options)
	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE projection_definitions SET options=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, string(raw), p.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE projection_queue SET due_at_ms=MAX(?,COALESCE((SELECT computed_at_ms FROM `+quote(projectionHeads(p))+` h WHERE h.scope_key=projection_queue.scope_key),(SELECT published_at_ms FROM projection_definitions WHERE id=?),0)+?) WHERE projection_id=? AND claimed_until IS NULL AND forced=0`, a.projectionTime().UnixMilli(), p.ID, interval*1000, p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return projectionDefinitionMap(p), nil
}
func (a *App) toolProjectionsActivate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "projections_activate", true)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := projectionFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE projection_definitions SET id=id WHERE id=?`, p.ID); err != nil {
		return nil, err
	}
	var built, queued int
	var latest, published int64
	if err := tx.QueryRow(`SELECT built,latest_relevant_change,published_change FROM projection_definitions WHERE id=?`, p.ID).Scan(&built, &latest, &published); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE projection_id=?`, p.ID).Scan(&queued); err != nil {
		return nil, err
	}
	if built == 0 || queued > 0 || latest > published || p.Status == "retired" {
		return nil, errf("projection version is not ready; wait for a successful build and all relevant changes")
	}
	if _, err := tx.Exec(`UPDATE projection_definitions SET is_current=0,status='retired',updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND name=? AND id<>? AND is_current=1`, pid, p.Name, p.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE projection_definitions SET is_current=1,status='active',updated_at=CURRENT_TIMESTAMP WHERE id=?`, p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.invalidateProjection(pid, p.Name)
	return map[string]any{"name": p.Name, "version": p.Version, "ready": true}, nil
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
	p, err := projectionFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var resultKind string
	_ = tx.QueryRow(`SELECT type FROM sqlite_master WHERE name=?`, p.ResultTable).Scan(&resultKind)
	resultDrop := `DROP TABLE IF EXISTS ` + quote(p.ResultTable)
	if resultKind == "view" {
		resultDrop = `DROP VIEW ` + quote(p.ResultTable)
	}
	for _, q := range []string{resultDrop, `DROP VIEW IF EXISTS ` + quote(projectionVisibleTable(p)), `DROP TABLE IF EXISTS ` + quote(projectionData(p)), `DROP TABLE IF EXISTS ` + quote(projectionHeads(p)), `DELETE FROM projection_definitions WHERE id=?`} {
		var vals []any
		if strings.Contains(q, "?") {
			vals = []any{p.ID}
		}
		if _, err := tx.Exec(q, vals...); err != nil {
			return nil, err
		}
	}
	for _, id := range p.SourceIDs {
		if err := rebuildProjectionTriggersTx(tx, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.invalidateProjection(pid, p.Name)
	return map[string]any{"deleted": p.Name, "version": p.Version}, nil
}
func stringSliceArg(args map[string]any, key string) []string {
	out, _ := strictStringSliceArg(args, key)
	return out
}
func strictStringSliceArg(args map[string]any, key string) ([]string, error) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, errf("%s must be an array", key)
	}
	out := make([]string, len(values))
	for i, v := range values {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, errf("%s[%d] must be a nonempty string", key, i)
		}
		out[i] = s
	}
	return out, nil
}
func makeScopeKey(scope map[string]any, columns []string) (string, error) {
	if len(columns) == 0 {
		return projectionAllScope, nil
	}
	ordered := map[string]any{}
	for _, c := range columns {
		v, ok := scope[c]
		if !ok {
			return "", errf("scope column %q is required", c)
		}
		ordered[c] = v
	}
	b, err := json.Marshal(ordered)
	return string(b), err
}
func makeScopeKeyFromRow(row map[string]any, cols []string) (string, bool) {
	key, err := makeScopeKey(row, cols)
	return key, err == nil
}
func scopeValues(key string, cols []string) ([]any, error) {
	var values map[string]any
	if err := projectionDecode(key, &values); err != nil {
		return nil, err
	}
	out := make([]any, len(cols))
	for i, col := range cols {
		v, ok := values[col]
		if !ok {
			return nil, errf("scope key missing %q", col)
		}
		out[i] = v
	}
	return projectionBoundValues(out)
}
func projectionResultValues(row map[string]any, cols []Column) ([]any, error) {
	out := make([]any, len(cols))
	for i, col := range cols {
		v, ok := row[col.Name]
		if !ok {
			return nil, errf("projection missing %q", col.Name)
		}
		value, err := coerceForStorage(col, v)
		if err != nil {
			return nil, err
		}
		out[i] = value
	}
	return out, nil
}
func maxProjectionMs(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "max_projection_ms", 30000, 100, 300000))
}
func maxProjectionRows(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "max_projection_rows", 100000, 1, 1000000))
}
func maxProjectionTotalRows(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "max_projection_total_rows", 1000000, 1, 5000000))
}
func maxProjectionBytes(ctx *sdk.AppCtx) int {
	return int(cfgInt64Range(ctx, "max_projection_bytes", 64<<20, 1<<20, 512<<20))
}
