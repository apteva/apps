package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

func (a *App) validateProjection(ctx *sdk.AppCtx, p *projectionDefinition) error {
	scopes := map[string]bool{}
	cols := map[string]Column{}
	for _, c := range p.ResultCols {
		cols[c.Name] = c
	}
	for _, s := range p.ScopeCols {
		if _, ok := cols[s]; !ok || scopes[s] {
			return errf("scope column %q must be a unique result column", s)
		}
		scopes[s] = true
	}
	for _, s := range p.Options.ScopeParams {
		if !scopes[s.Column] {
			return errf("scope_params must reference scope_columns")
		}
	}
	for _, param := range p.Options.ScopeParams {
		if param.Boundary != "" {
			if param.Boundary != "start" && param.Boundary != "end" {
				return errf("scope boundary must be start or end")
			}
			if param.Timezone == "" {
				return errf("day boundary requires timezone")
			}
			if _, err := time.LoadLocation(param.Timezone); err != nil {
				return err
			}
		} else if param.Timezone != "" {
			return errf("scope timezone requires boundary")
		}
	}
	if p.Options.ScopeSQL != "" && len(p.ScopeCols) == 0 {
		return errf("scope_sql requires scope_columns")
	}
	sources := map[string]*Table{}
	for _, name := range p.SourceTables {
		if sources[name] != nil {
			return errf("duplicate source %q", name)
		}
		table, err := a.loadTableSchema(ctx, p.ProjectID, name)
		if err != nil {
			return err
		}
		sources[name] = table
		p.SourceIDs = append(p.SourceIDs, table.ID)
	}
	// Watch lists are opt-in. Mapping inputs are mandatory; callers must also
	// include every calculation/filter/join input used by their SQL.
	watched := map[string]map[string]bool{}
	for _, dependency := range p.Options.SourceDependencies {
		source := sources[dependency.Table]
		if source == nil || watched[dependency.Table] != nil {
			return errf("source_dependencies must reference unique declared sources: %q", dependency.Table)
		}
		if len(dependency.WatchedColumns) == 0 || len(dependency.WatchedColumns) > 260 {
			return errf("watched_columns requires 1..260 columns")
		}
		set := map[string]bool{}
		for _, col := range dependency.WatchedColumns {
			if set[col] || (columnIndex(source.Columns, col) < 0 && !reservedColumns[col]) {
				return errf("duplicate or unknown watched column %q on %q", col, dependency.Table)
			}
			set[col] = true
		}
		watched[dependency.Table] = set
	}
	queries := []struct {
		text   string
		bound  []any
		result bool
	}{{p.SQL, p.Options.Params, true}}
	if p.Options.ScopeSQL != "" {
		queries = append(queries, struct {
			text   string
			bound  []any
			result bool
		}{p.Options.ScopeSQL, append(make([]any, len(p.Options.ScopeParams)), p.Options.Params...), true})
	}
	mapped := map[string]bool{}
	for _, rule := range p.Options.ScopeRules {
		source := sources[rule.Source]
		if source == nil {
			return errf("scope rule references undeclared source %q", rule.Source)
		}
		mapped[rule.Source] = true
		if len(rule.Values) != len(p.ScopeCols) {
			return errf("scope rule must map every scope column")
		}
		for name, spec := range rule.Values {
			if !scopes[name] {
				return errf("unknown scope %q", name)
			}
			if err := validateIdentifier("mapping input", spec.Column); err != nil {
				return err
			}
			if spec.Bucket != "" {
				if spec.Bucket != "day" && spec.Bucket != "month" {
					return errf("bucket must be day or month")
				}
				if spec.Timezone == "" {
					return errf("derived date scope requires timezone")
				}
				if _, err := time.LoadLocation(spec.Timezone); err != nil {
					return err
				}
				if cols[name].Type != "text" {
					return errf("derived date scope must be text")
				}
			}
			if rule.SQL == "" && columnIndex(source.Columns, spec.Column) < 0 && !reservedColumns[spec.Column] {
				return errf("unknown mapping source column %q", spec.Column)
			}
		}
		for _, param := range rule.Params {
			if columnIndex(source.Columns, param) < 0 && !reservedColumns[param] {
				return errf("unknown mapping parameter %q", param)
			}
		}
		if rule.SQL != "" {
			if len(rule.SQL) > 64<<10 {
				return errf("mapping SQL too long")
			}
			if err := validateReadOnlySQL(rule.SQL); err != nil {
				return err
			}
			n, err := projectionParameterCount(rule.SQL)
			if err != nil {
				return err
			}
			if n != len(rule.Params) {
				return errf("mapping SQL parameters must match params")
			}
			queries = append(queries, struct {
				text   string
				bound  []any
				result bool
			}{rule.SQL, make([]any, len(rule.Params)), false})
		} else if len(rule.Params) > 0 {
			return errf("mapping params requires SQL")
		}
	}
	// Direct column scopes remain the default. A source without those columns
	// requires an explicit generic mapping instead of silently losing changes.
	for name, source := range sources {
		if mapped[name] {
			continue
		}
		for _, scope := range p.ScopeCols {
			if columnIndex(source.Columns, scope) < 0 {
				return errf("source %q needs a scope_rules mapping for %q", name, scope)
			}
		}
	}
	for name, set := range watched {
		required := []string{}
		if !mapped[name] {
			required = append(required, p.ScopeCols...)
		}
		for _, rule := range p.Options.ScopeRules {
			if rule.Source != name {
				continue
			}
			required = append(required, rule.Params...)
			if rule.SQL == "" {
				for _, value := range rule.Values {
					required = append(required, value.Column)
				}
			}
		}
		for _, col := range required {
			if !set[col] {
				return errf("watched_columns for %q must include scope/mapping input %q", name, col)
			}
		}
	}
	for _, q := range queries {
		tokens, err := a.cachedProjectionSQL(ctx, q.text)
		if err != nil {
			return err
		}
		ph := placeholderNamesFromTokens(tokens)
		for _, name := range ph {
			if sources[name] == nil {
				return errf("SQL references undeclared source %q", name)
			}
		}
		resolved, err := a.substitutePlaceholders(ctx, p.ProjectID, q.text)
		if err != nil {
			return err
		}
		read, err := acquireReadConn(ctx, p.Name)
		if err != nil {
			return err
		}
		qctx, cancel := queryTimeoutContext(ctx)
		err = authorizeQuery(qctx, read.conn, ctx, a, p.ProjectID, q.text, resolved, q.bound)
		if err == nil {
			rows, e := read.conn.QueryContext(qctx, `SELECT * FROM (`+strings.TrimSuffix(strings.TrimSpace(resolved), ";")+`) LIMIT 0`, q.bound...)
			if e != nil {
				err = e
			} else {
				if q.result {
					err = validateProjectionResultSchema(rows, p.ResultCols)
				} else {
					labels, e := rows.Columns()
					err = e
					seen := map[string]bool{}
					for _, c := range labels {
						seen[c] = true
					}
					for _, r := range p.Options.ScopeRules {
						if r.SQL == q.text {
							for _, spec := range r.Values {
								if !seen[spec.Column] {
									err = errf("mapping SQL must return %q", spec.Column)
								}
							}
						}
					}
				}
				rows.Close()
			}
		}
		cancel()
		read.close()
		if err != nil {
			return fmt.Errorf("projection SQL validation: %w", err)
		}
	}
	return nil
}
func rebuildProjectionTriggersTx(tx *writeTx, tableID int64) error {
	var physical, pid string
	if err := tx.QueryRow(`SELECT physical_name,project_id FROM tables_meta WHERE id=?`, tableID).Scan(&physical, &pid); err != nil {
		return err
	}
	for _, suffix := range []string{"insert", "update", "delete"} {
		if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ` + quote(fmt.Sprintf("projection_change_%d_%s", tableID, suffix))); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT p.id,p.scope_columns,p.options,t.name,p.sql_text FROM projection_definitions p JOIN projection_sources s ON s.projection_id=p.id JOIN tables_meta t ON t.id=s.table_id WHERE s.table_id=? AND p.status IN ('active','paused','building')`, tableID)
	if err != nil {
		return err
	}
	fields := map[string]bool{"id": true}
	dependent := false
	type dependencyPredicate struct {
		id      int64
		columns []string
	}
	var dependencies []dependencyPredicate
	for rows.Next() {
		var raw, opts, name, sqlText string
		var id int64
		if err := rows.Scan(&id, &raw, &opts, &name, &sqlText); err != nil {
			rows.Close()
			return err
		}
		var scopes []string
		var options projectionOptions
		if err := projectionDecode(raw, &scopes); err != nil {
			rows.Close()
			return err
		}
		if err := projectionDecode(opts, &options); err != nil {
			rows.Close()
			return err
		}
		dependent = true
		dep := dependencyPredicate{id: id}
		for _, config := range options.SourceDependencies {
			if config.Table == name {
				dep.columns = config.WatchedColumns
			}
		}
		dependencies = append(dependencies, dep)
		queries := []string{sqlText, options.ScopeSQL}
		for _, r := range options.ScopeRules {
			queries = append(queries, r.SQL)
		}
		for _, query := range queries {
			tokens, err := sqlTokens(query)
			if err != nil {
				rows.Close()
				return err
			}
			for _, token := range tokens {
				if token.kind == "identifier" && strings.EqualFold(token.value, "_revision") {
					fields["_revision"] = true
				}
			}
		}
		mapped := false
		for _, rule := range options.ScopeRules {
			if rule.Source != name {
				continue
			}
			mapped = true
			for _, param := range rule.Params {
				fields[param] = true
			}
			if rule.SQL == "" {
				for _, spec := range rule.Values {
					fields[spec.Column] = true
				}
			}
		}
		if !mapped {
			for _, col := range scopes {
				fields[col] = true
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !dependent {
		return nil
	}
	jsonExpr := func(prefix string) string {
		parts := []string{}
		for field := range fields {
			parts = append(parts, "'"+field+"'", prefix+"."+quote(field))
		}
		return "json_object(" + strings.Join(parts, ",") + ")"
	}
	// Capture user fields and updated_at changes. Revision-only bookkeeping
	// is relevant only when a definition or mapping actually reads _revision.
	cols, err := tx.Query(`SELECT name FROM columns_meta WHERE table_id=?`, tableID)
	if err != nil {
		return err
	}
	parts := []string{`OLD.updated_at IS NOT NEW.updated_at`, `OLD.created_at IS NOT NEW.created_at`}
	if fields["_revision"] {
		parts = append(parts, `OLD._revision IS NOT NEW._revision`)
	}
	for cols.Next() {
		var name string
		if err := cols.Scan(&name); err != nil {
			cols.Close()
			return err
		}
		parts = append(parts, "OLD."+quote(name)+" IS NOT NEW."+quote(name))
	}
	err = cols.Err()
	cols.Close()
	if err != nil {
		return err
	}
	// Persist relevance per immutable version, in the same transaction as the
	// source mutation. Different watch lists can share one source trigger without
	// making each other's freshness stale. Old log records use NULL and remain
	// conservatively relevant after an automatic upgrade.
	predicates := []string{}
	cases := []string{}
	for _, dep := range dependencies {
		checks := parts
		if dep.columns != nil {
			checks = nil
			for _, col := range dep.columns {
				checks = append(checks, "OLD."+quote(col)+" IS NOT NEW."+quote(col))
			}
		}
		predicate := "(" + strings.Join(checks, " OR ") + ")"
		predicates = append(predicates, predicate)
		cases = append(cases, fmt.Sprintf("WHEN %d THEN %s", dep.id, predicate))
	}
	for _, op := range []string{"insert", "update", "delete"} {
		old, next, rowID := "NULL", jsonExpr("NEW"), "NEW.id"
		when := ""
		if op == "update" {
			old = jsonExpr("OLD")
			when = " WHEN " + strings.Join(predicates, " OR ")
		}
		if op == "delete" {
			old, next, rowID = jsonExpr("OLD"), "NULL", "OLD.id"
		}
		filter := "1"
		if op == "update" {
			filter = "CASE p.id " + strings.Join(cases, " ") + " ELSE 0 END"
		}
		relevance := fmt.Sprintf(`(SELECT json_group_array(p.id) FROM projection_definitions p JOIN projection_sources s ON s.projection_id=p.id WHERE s.table_id=%d AND p.status IN ('active','paused','building') AND (%s))`, tableID, filter)
		capture := fmt.Sprintf(`INSERT INTO projection_changes(project_id,table_id,row_id,operation,old_values,new_values,relevant_projection_ids) VALUES('%s',%d,%s,'%s',%s,%s,%s);`, strings.ReplaceAll(pid, "'", "''"), tableID, rowID, op, old, next, relevance)
		relevant := fmt.Sprintf(`UPDATE projection_definitions SET latest_relevant_change=(SELECT MAX(change_id) FROM projection_changes WHERE table_id=%d) WHERE id IN (SELECT value FROM json_each((SELECT relevant_projection_ids FROM projection_changes WHERE table_id=%d ORDER BY change_id DESC LIMIT 1)));`, tableID, tableID)
		q := fmt.Sprintf("CREATE TRIGGER %s AFTER %s ON %s%s BEGIN %s %s END", quote(fmt.Sprintf("projection_change_%d_%s", tableID, op)), strings.ToUpper(op), quote(physical), when, capture, relevant)
		if _, err := tx.Exec(q); err != nil {
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
	rows, err := tx.Query(`SELECT id FROM tables_meta WHERE project_id<>''`)
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
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := rebuildProjectionTriggersTx(tx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type projectionInvalidationCacheKey struct{}

func (a *App) projectionChangeScopes(ctx context.Context, app *sdk.AppCtx, p *projectionDefinition, source string, old, next sql.NullString) (map[string]bool, error) {
	keys := map[string]bool{}
	if len(p.ScopeCols) == 0 {
		keys[projectionAllScope] = true
		return keys, nil
	}
	for _, raw := range []sql.NullString{old, next} {
		if !raw.Valid {
			continue
		}
		var payload map[string]any
		if err := projectionDecode(raw.String, &payload); err != nil {
			return nil, err
		}
		mapped := false
		for _, rule := range p.Options.ScopeRules {
			if rule.Source != source {
				continue
			}
			mapped = true
			if rule.SQL == "" {
				key, err := projectionMappedScope(payload, p, rule.Values)
				if err != nil {
					return nil, err
				}
				keys[key] = true
				continue
			}
			bound := make([]any, len(rule.Params))
			for i, param := range rule.Params {
				v, ok := payload[param]
				if !ok {
					return nil, errf("missing captured dependency field %q", param)
				}
				bound[i] = v
			}
			bound, err := projectionBoundValues(bound)
			if err != nil {
				return nil, err
			}
			cache, _ := ctx.Value(projectionInvalidationCacheKey{}).(map[string][]map[string]any)
			encoded, err := json.Marshal(bound)
			if err != nil {
				return nil, err
			}
			lookupKey := rule.SQL + "\x00" + string(encoded)
			rows, found := cache[lookupKey]
			if !found {
				rows, err = a.projectionSQLRows(ctx, app, p, rule.SQL, bound, false)
				if err != nil {
					return nil, err
				}
				if cache != nil {
					cache[lookupKey] = rows
				}
			}

			for _, row := range rows {
				key, err := projectionMappedScope(row, p, rule.Values)
				if err != nil {
					return nil, err
				}
				keys[key] = true
			}
		}
		if !mapped {
			key, err := projectionScopeKey(p, payload)
			if err != nil {
				return nil, err
			}
			keys[key] = true
		}
	}
	if len(keys) > 4096 {
		return nil, errf("invalidation exceeds 4096 scopes")
	}
	return keys, nil
}
func loadActiveProjections(app *sdk.AppCtx, pid string) ([]*projectionDefinition, error) {
	rows, err := app.AppReadDB().QueryContext(requestContext(app), projectionSelect+`WHERE project_id=? AND status IN ('active','paused','building') ORDER BY id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*projectionDefinition
	for rows.Next() {
		p, err := decodeProjection(app, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (a *App) consumeProjectionChanges(ctx context.Context, app *sdk.AppCtx, pid string) error {
	defs, err := loadActiveProjections(app, pid)
	if err != nil {
		return err
	}
	for _, p := range defs {
		var cursor int64
		if err := app.AppReadDB().QueryRowContext(ctx, `SELECT last_change_id FROM projection_cursors WHERE projection_id=?`, p.ID).Scan(&cursor); err != nil {
			return err
		}
		rows, err := app.AppReadDB().QueryContext(ctx, `SELECT c.change_id,t.name,c.old_values,c.new_values,EXISTS(SELECT 1 FROM projection_sources s WHERE s.projection_id=? AND s.table_id=c.table_id) AND (c.relevant_projection_ids IS NULL OR EXISTS(SELECT 1 FROM json_each(c.relevant_projection_ids) WHERE value=?)) FROM projection_changes c LEFT JOIN tables_meta t ON t.id=c.table_id WHERE c.project_id=? AND c.change_id>? ORDER BY c.change_id LIMIT ?`, p.ID, p.ID, pid, cursor, projectionChangeBatch)
		if err != nil {
			return err
		}
		type change struct {
			id        int64
			source    sql.NullString
			old, next sql.NullString
			relevant  bool
		}
		var changes []change
		for rows.Next() {
			var c change
			if err := rows.Scan(&c.id, &c.source, &c.old, &c.next, &c.relevant); err != nil {
				rows.Close()
				return err
			}
			changes = append(changes, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			continue
		}
		pending := map[string]int64{}
		var mappingFailure string
		coalesced := false
		invalidationCtx := context.WithValue(ctx, projectionInvalidationCacheKey{}, map[string][]map[string]any{})
		for _, c := range changes {
			if !c.relevant || c.id <= p.Published {
				continue
			}
			if mappingFailure != "" || coalesced {
				if c.id > pending[projectionAllScope] {
					pending[projectionAllScope] = c.id
				}
				continue
			}
			keys, err := a.projectionChangeScopes(invalidationCtx, app, p, c.source.String, c.old, c.next)
			if err != nil {
				// A bounded lookup may fail or exceed fan-out. Conservatively invalidate
				// the complete result rather than lose an event or retry consumption in a
				// tight loop. The ordinary refresh budget and retry backoff still apply.
				mappingFailure = err.Error()
				if len(mappingFailure) > 2048 {
					mappingFailure = mappingFailure[:2048]
				}
				pending[projectionAllScope] = c.id
				continue
			}
			for key := range keys {
				pending[key] = c.id
			}
			if len(pending) > projectionScopeCoalesceThreshold {
				// Once fan-out is broad, one complete snapshot is cheaper and safer
				// than retaining hundreds of independent generations. The complete
				// rebuild carries the newest watermark and coalesces later changes.
				pending = map[string]int64{projectionAllScope: c.id}
				coalesced = true
			}
		}
		tx, err := app.AppDB().BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// Serialize duplicate consumers before advancing the cursor. Work computed
		// from a stale cursor is discarded instead of scheduling duplicate refreshes.
		if _, err := tx.ExecContext(ctx, `UPDATE projection_cursors SET last_change_id=last_change_id WHERE projection_id=?`, p.ID); err != nil {
			tx.Rollback()
			return err
		}
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT last_change_id FROM projection_cursors WHERE projection_id=?`, p.ID).Scan(&current); err != nil {
			tx.Rollback()
			return err
		}
		if current != cursor {
			tx.Rollback()
			continue
		}
		if mappingFailure != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE projection_definitions SET last_failure=? WHERE id=?`, mappingFailure, p.ID); err != nil {
				tx.Rollback()
				return err
			}
		}
		for key, watermark := range pending {
			// A publication may already include events whose log records have not
			// been consumed yet. Check again under the writer lock to also cover a
			// publication that completed while dependency mappings were calculated.
			// Explicit refresh requests still enqueue normally, regardless of watermark.
			var covered int64
			if err := tx.QueryRowContext(ctx, `SELECT MAX(published_change,COALESCE((SELECT MAX(watermark) FROM `+quote(projectionHeads(p))+` WHERE scope_key IN (?,?)),0)) FROM projection_definitions WHERE id=?`, key, projectionAllScope, p.ID).Scan(&covered); err != nil {
				tx.Rollback()
				return err
			}
			if watermark <= covered {
				continue
			}
			if err := enqueueProjectionTx(ctx, tx, p, key, watermark, a.projectionTime().UnixMilli(), false); err != nil {
				tx.Rollback()
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE projection_cursors SET last_change_id=? WHERE projection_id=?`, changes[len(changes)-1].id, p.ID); err != nil {
			tx.Rollback()
			return err
		}
		// A relevant source change with no dependent results needs no publication;
		// mark the complete result current only if no queued scope remains.
		if _, err := tx.ExecContext(ctx, `UPDATE projection_definitions SET published_change=latest_relevant_change WHERE id=? AND built=1 AND latest_relevant_change<=? AND NOT EXISTS(SELECT 1 FROM projection_queue WHERE projection_id=?)`, p.ID, changes[len(changes)-1].id, p.ID); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	_, err = app.AppDB().ExecContext(ctx, `DELETE FROM projection_changes WHERE change_id IN (SELECT change_id FROM projection_changes WHERE project_id=? AND change_id <= (SELECT MIN(c.last_change_id) FROM projection_cursors c JOIN projection_definitions p ON p.id=c.projection_id WHERE c.project_id=? AND p.status IN ('active','paused','building')) LIMIT 2048)`, pid, pid)
	return err
}
