package main

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── rows_insert ───────────────────────────────────────────────────

func (a *App) toolRowsInsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tableName := strArg(args, "table")
	if err := validateIdentifier("table", tableName); err != nil {
		return nil, err
	}
	rawRows := sliceArg(args, "rows")
	if len(rawRows) == 0 {
		return nil, errf("rows is required and must be non-empty")
	}
	if len(rawRows) > maxBatchRows(ctx) {
		return nil, errf("rows exceeds max_batch_rows (%d)", maxBatchRows(ctx))
	}
	t, err := loadTable(ctx.AppDB(), pid, tableName)
	if err != nil {
		return nil, err
	}
	prepared := make([][]any, len(rawRows))
	colsUsed := make([][]string, len(rawRows))
	for i, r := range rawRows {
		obj, ok := r.(map[string]any)
		if !ok {
			return nil, errf("rows[%d]: must be an object", i)
		}
		// Reject unknown + reserved keys.
		for k := range obj {
			if reservedColumns[k] {
				return nil, errf("rows[%d]: %q is reserved and managed automatically", i, k)
			}
			if columnIndex(t.Columns, k) < 0 {
				return nil, errf("rows[%d]: unknown column %q", i, k)
			}
		}
		// Build per-row column list + values, applying defaults for
		// missing fields and erroring on missing-required-no-default.
		usedCols := make([]string, 0, len(t.Columns))
		usedVals := make([]any, 0, len(t.Columns))
		for _, col := range t.Columns {
			v, present := obj[col.Name]
			if !present {
				if col.Default != nil {
					v = col.Default
					present = true
				} else if !col.Nullable {
					return nil, errf("rows[%d]: column %q is required", i, col.Name)
				} else {
					continue // skip — sqlite stores NULL implicitly
				}
			}
			coerced, err := coerceForStorage(col, v)
			if err != nil {
				return nil, errf("rows[%d]: %v", i, err)
			}
			if err := validateStoredValueSize(ctx, col, coerced); err != nil {
				return nil, errf("rows[%d]: %v", i, err)
			}
			usedCols = append(usedCols, col.Name)
			usedVals = append(usedVals, coerced)
		}
		prepared[i] = usedVals
		colsUsed[i] = usedCols
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := reserveRows(tx, t, int64(len(rawRows)), maxRowsPerTable(ctx)); err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(rawRows))
	for i, vals := range prepared {
		colNames := colsUsed[i]
		var sqlText string
		if len(colNames) == 0 {
			sqlText = fmt.Sprintf("INSERT INTO %s DEFAULT VALUES", quote(t.PhysicalName))
		} else {
			placeholders := strings.Repeat("?,", len(vals))
			placeholders = placeholders[:len(placeholders)-1]
			cols := make([]string, len(colNames))
			for j, n := range colNames {
				cols[j] = quote(n)
			}
			sqlText = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
				quote(t.PhysicalName), strings.Join(cols, ", "), placeholders)
		}
		res, err := tx.Exec(sqlText, vals...)
		if err != nil {
			return nil, errf("rows[%d]: insert failed: %v", i, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	emit(ctx, topicRowInserted, map[string]any{
		"table": tableName,
		"ids":   ids,
		"count": len(ids),
	})
	return map[string]any{"ids": ids, "inserted": len(ids)}, nil
}

// ─── rows_upsert ────────────────────────────────────────────────────

func (a *App) toolRowsUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tableName := strArg(args, "table")
	if err := validateIdentifier("table", tableName); err != nil {
		return nil, err
	}
	rawKey := sliceArg(args, "key")
	if len(rawKey) == 0 {
		return nil, errf("key is required and must be non-empty")
	}
	if len(rawKey) > 32 {
		return nil, errf("key exceeds maximum of 32 columns")
	}
	keyCols := make([]string, 0, len(rawKey))
	seenKey := map[string]bool{}
	for i, v := range rawKey {
		name, ok := v.(string)
		if !ok {
			return nil, errf("key[%d]: must be a column name string", i)
		}
		if reservedColumns[name] {
			return nil, errf("key[%d]: %q is reserved and cannot be used as an upsert key", i, name)
		}
		if err := validateIdentifier("key column", name); err != nil {
			return nil, fmt.Errorf("key[%d]: %w", i, err)
		}
		if seenKey[name] {
			return nil, errf("key[%d]: duplicate column %q", i, name)
		}
		seenKey[name] = true
		keyCols = append(keyCols, name)
	}
	sort.Strings(keyCols)
	rawRows := sliceArg(args, "rows")
	if len(rawRows) == 0 {
		return nil, errf("rows is required and must be non-empty")
	}
	if len(rawRows) > maxBatchRows(ctx) {
		return nil, errf("rows exceeds max_batch_rows (%d)", maxBatchRows(ctx))
	}
	t, err := loadTable(ctx.AppDB(), pid, tableName)
	if err != nil {
		return nil, err
	}
	colByName := map[string]Column{}
	for _, col := range t.Columns {
		colByName[col.Name] = col
	}
	for _, name := range keyCols {
		if _, ok := colByName[name]; !ok {
			return nil, errf("key column %q does not exist", name)
		}
	}

	type preparedRow struct {
		obj     map[string]any
		keyVals []any
	}
	prepared := make([]preparedRow, len(rawRows))
	for i, r := range rawRows {
		obj, ok := r.(map[string]any)
		if !ok {
			return nil, errf("rows[%d]: must be an object", i)
		}
		for k := range obj {
			if reservedColumns[k] {
				return nil, errf("rows[%d]: %q is reserved and managed automatically", i, k)
			}
			if _, ok := colByName[k]; !ok {
				return nil, errf("rows[%d]: unknown column %q", i, k)
			}
		}
		keyVals := make([]any, 0, len(keyCols))
		for _, key := range keyCols {
			v, present := obj[key]
			if !present || v == nil {
				return nil, errf("rows[%d]: key column %q is required", i, key)
			}
			coerced, err := coerceForStorage(colByName[key], v)
			if err != nil {
				return nil, errf("rows[%d] key %q: %v", i, key, err)
			}
			if err := validateStoredValueSize(ctx, colByName[key], coerced); err != nil {
				return nil, errf("rows[%d] key %q: %v", i, key, err)
			}
			keyVals = append(keyVals, coerced)
		}
		prepared[i] = preparedRow{obj: obj, keyVals: keyVals}
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE tables_meta SET updated_at = updated_at WHERE id = ?`, t.ID); err != nil {
		return nil, err
	}
	if err := ensureUniqueUpsertIndex(tx, t, keyCols); err != nil {
		return nil, err
	}

	var whereParts []string
	for _, key := range keyCols {
		whereParts = append(whereParts, quote(key)+" = ?")
	}
	where := strings.Join(whereParts, " AND ")
	ids := make([]int64, 0, len(prepared))
	inserted, updated := 0, 0

	for i, row := range prepared {
		var existingID int64
		err := tx.QueryRow(
			fmt.Sprintf("SELECT id FROM %s WHERE %s ORDER BY id LIMIT 1", quote(t.PhysicalName), where),
			row.keyVals...,
		).Scan(&existingID)
		if err != nil && err != sql.ErrNoRows {
			return nil, errf("rows[%d]: lookup failed: %v", i, err)
		}
		if err == nil {
			setCols := make([]string, 0, len(row.obj)+1)
			vals := make([]any, 0, len(row.obj)+1)
			for _, col := range t.Columns {
				v, present := row.obj[col.Name]
				if !present {
					continue
				}
				coerced, err := coerceForStorage(col, v)
				if err != nil {
					return nil, errf("rows[%d]: %v", i, err)
				}
				if err := validateStoredValueSize(ctx, col, coerced); err != nil {
					return nil, errf("rows[%d]: %v", i, err)
				}
				setCols = append(setCols, quote(col.Name)+" = ?")
				vals = append(vals, coerced)
			}
			if len(setCols) > 0 {
				setCols = append(setCols, `"updated_at" = CURRENT_TIMESTAMP`)
				vals = append(vals, existingID)
				stmt := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", quote(t.PhysicalName), strings.Join(setCols, ", "))
				if _, err := tx.Exec(stmt, vals...); err != nil {
					return nil, errf("rows[%d]: update failed: %v", i, err)
				}
			}
			ids = append(ids, existingID)
			updated++
			continue
		}

		usedCols := make([]string, 0, len(t.Columns))
		usedVals := make([]any, 0, len(t.Columns))
		for _, col := range t.Columns {
			v, present := row.obj[col.Name]
			if !present {
				if col.Default != nil {
					v = col.Default
					present = true
				} else if !col.Nullable {
					return nil, errf("rows[%d]: column %q is required", i, col.Name)
				} else {
					continue
				}
			}
			coerced, err := coerceForStorage(col, v)
			if err != nil {
				return nil, errf("rows[%d]: %v", i, err)
			}
			if err := validateStoredValueSize(ctx, col, coerced); err != nil {
				return nil, errf("rows[%d]: %v", i, err)
			}
			usedCols = append(usedCols, col.Name)
			usedVals = append(usedVals, coerced)
		}
		var sqlText string
		if len(usedCols) == 0 {
			sqlText = fmt.Sprintf("INSERT INTO %s DEFAULT VALUES", quote(t.PhysicalName))
		} else {
			placeholders := strings.Repeat("?,", len(usedVals))
			placeholders = placeholders[:len(placeholders)-1]
			cols := make([]string, len(usedCols))
			for j, n := range usedCols {
				cols[j] = quote(n)
			}
			sqlText = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
				quote(t.PhysicalName), strings.Join(cols, ", "), placeholders)
		}
		if err := reserveRows(tx, t, 1, maxRowsPerTable(ctx)); err != nil {
			return nil, err
		}
		res, err := tx.Exec(sqlText, usedVals...)
		if err != nil {
			return nil, errf("rows[%d]: insert failed: %v", i, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
		inserted++
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if inserted > 0 {
		emit(ctx, topicRowInserted, map[string]any{
			"table": tableName,
			"count": inserted,
		})
	}
	if updated > 0 {
		emit(ctx, topicRowUpdated, map[string]any{
			"table": tableName,
			"count": updated,
		})
	}
	return map[string]any{"ids": ids, "inserted": inserted, "updated": updated}, nil
}

func ensureUniqueUpsertIndex(tx *sql.Tx, t *Table, keyCols []string) error {
	sum := sha256.Sum256([]byte(strings.Join(keyCols, "\x00")))
	indexName := fmt.Sprintf("ux_%s_%x", t.PhysicalName, sum[:8])
	quotedCols := make([]string, len(keyCols))
	for i, col := range keyCols {
		quotedCols[i] = quote(col)
	}
	stmt := fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s)",
		quote(indexName), quote(t.PhysicalName), strings.Join(quotedCols, ", "))
	if _, err := tx.Exec(stmt); err != nil {
		return errf("cannot enforce upsert key (%s); remove duplicate key rows first: %v", strings.Join(keyCols, ", "), err)
	}
	return nil
}

// ─── rows_get ──────────────────────────────────────────────────────

func (a *App) toolRowsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tableName := strArg(args, "table")
	if err := validateIdentifier("table", tableName); err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errf("id required")
	}
	t, err := loadTable(ctx.AppDB(), pid, tableName)
	if err != nil {
		return nil, err
	}
	selectClause, err := parseSelect(args, t)
	if err != nil {
		return nil, err
	}
	row, found, err := fetchRowByID(ctx.AppDB(), t, id, selectClause)
	if err != nil {
		return nil, err
	}
	if !found {
		return map[string]any{"row": nil, "found": false}, nil
	}
	if boolArg(args, "hydrate_files") {
		hydrateFileColumns(ctx, t, row)
	}
	return map[string]any{"row": row, "found": true}, nil
}

// ─── rows_update ───────────────────────────────────────────────────

func (a *App) toolRowsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tableName := strArg(args, "table")
	if err := validateIdentifier("table", tableName); err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errf("id required")
	}
	fields := mapArg(args, "fields")
	if len(fields) == 0 {
		return nil, errf("fields must be a non-empty object")
	}
	t, err := loadTable(ctx.AppDB(), pid, tableName)
	if err != nil {
		return nil, err
	}

	setCols := make([]string, 0, len(fields))
	vals := make([]any, 0, len(fields))
	for k, v := range fields {
		if reservedColumns[k] {
			return nil, errf("%q is reserved and managed automatically", k)
		}
		idx := columnIndex(t.Columns, k)
		if idx < 0 {
			return nil, errf("unknown column %q", k)
		}
		coerced, err := coerceForStorage(t.Columns[idx], v)
		if err != nil {
			return nil, err
		}
		if err := validateStoredValueSize(ctx, t.Columns[idx], coerced); err != nil {
			return nil, err
		}
		setCols = append(setCols, fmt.Sprintf("%s = ?", quote(k)))
		vals = append(vals, coerced)
	}
	setCols = append(setCols, `"updated_at" = CURRENT_TIMESTAMP`)
	vals = append(vals, id)

	stmt := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", quote(t.PhysicalName), strings.Join(setCols, ", "))
	res, err := ctx.AppDB().Exec(stmt, vals...)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errf("row id=%d not found in table %q", id, tableName)
	}
	row, _, err := fetchRowByID(ctx.AppDB(), t, id, "")
	if err != nil {
		return nil, err
	}
	emit(ctx, topicRowUpdated, map[string]any{
		"table":  tableName,
		"id":     id,
		"fields": fields,
		"row":    row,
	})
	return map[string]any{"row": row}, nil
}

// ─── rows_delete ───────────────────────────────────────────────────

func (a *App) toolRowsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tableName := strArg(args, "table")
	if err := validateIdentifier("table", tableName); err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	wherePreds := sliceArg(args, "where")
	if id == 0 && len(wherePreds) == 0 {
		return nil, errf("either id or where is required")
	}
	if id != 0 && len(wherePreds) > 0 {
		return nil, errf("pass id or where, not both")
	}
	t, err := loadTable(ctx.AppDB(), pid, tableName)
	if err != nil {
		return nil, err
	}

	if id != 0 {
		return deleteRows(ctx, t, tableName, id,
			fmt.Sprintf("DELETE FROM %s WHERE id = ?", quote(t.PhysicalName)), []any{id})
	}

	if !boolArg(args, "confirm") {
		return nil, errf("filter delete requires confirm=true")
	}
	clause, vals, err := buildWhere(t, wherePreds)
	if err != nil {
		return nil, err
	}
	stmt := "DELETE FROM " + quote(t.PhysicalName)
	if clause != "" {
		stmt += " " + clause
	}
	return deleteRows(ctx, t, tableName, 0, stmt, vals)
}

func deleteRows(ctx *sdk.AppCtx, t *Table, tableName string, id int64, stmt string, vals []any) (any, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(stmt, vals...)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		if _, err := tx.Exec(`UPDATE tables_meta SET row_count = MAX(0, row_count - ?) WHERE id = ?`, n, t.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if n > 0 {
		data := map[string]any{
			"table":   tableName,
			"deleted": n,
		}
		if id != 0 {
			data["id"] = id
		}
		emit(ctx, topicRowDeleted, data)
	}
	return map[string]any{"deleted": n}, nil
}

// ─── rows_search ───────────────────────────────────────────────────

func (a *App) toolRowsSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tableName := strArg(args, "table")
	if err := validateIdentifier("table", tableName); err != nil {
		return nil, err
	}
	t, err := loadTable(ctx.AppDB(), pid, tableName)
	if err != nil {
		return nil, err
	}
	clause, vals, err := buildWhere(t, sliceArg(args, "where"))
	if err != nil {
		return nil, err
	}
	orderBy, err := buildOrderBy(t, strArg(args, "order_by"))
	if err != nil {
		return nil, err
	}

	limit := intArg(args, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := intArg(args, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	qctx, cancel := queryTimeoutContext(ctx)
	defer cancel()
	var total int64
	if clause == "" {
		total = t.RowCount
	} else {
		totalSQL := "SELECT COUNT(*) FROM " + quote(t.PhysicalName) + " " + clause
		if err := ctx.AppDB().QueryRowContext(qctx, totalSQL, vals...).Scan(&total); err != nil {
			return nil, err
		}
	}

	selectClause, err := parseSelect(args, t)
	if err != nil {
		return nil, err
	}
	stmt := selectClause + " FROM " + quote(t.PhysicalName)
	if clause != "" {
		stmt += " " + clause
	}
	stmt += " " + orderBy
	stmt += fmt.Sprintf(" LIMIT %d OFFSET %d", limit+1, offset)
	rows, err := ctx.AppDB().QueryContext(qctx, stmt, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, truncated, err := scanRowsBudget(rows, t, maxQueryBytes(ctx), limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"rows": out, "total": total, "truncated": truncated}, nil
}

// ─── rows_count ────────────────────────────────────────────────────

func (a *App) toolRowsCount(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tableName := strArg(args, "table")
	if err := validateIdentifier("table", tableName); err != nil {
		return nil, err
	}
	t, err := loadTable(ctx.AppDB(), pid, tableName)
	if err != nil {
		return nil, err
	}
	clause, vals, err := buildWhere(t, sliceArg(args, "where"))
	if err != nil {
		return nil, err
	}
	if clause == "" {
		return map[string]any{"count": t.RowCount}, nil
	}
	stmt := "SELECT COUNT(*) FROM " + quote(t.PhysicalName)
	if clause != "" {
		stmt += " " + clause
	}
	var n int64
	qctx, cancel := queryTimeoutContext(ctx)
	defer cancel()
	if err := ctx.AppDB().QueryRowContext(qctx, stmt, vals...).Scan(&n); err != nil {
		return nil, err
	}
	return map[string]any{"count": n}, nil
}

// ─── rows_aggregate ────────────────────────────────────────────────

func (a *App) toolRowsAggregate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tableName := strArg(args, "table")
	if err := validateIdentifier("table", tableName); err != nil {
		return nil, err
	}
	t, err := loadTable(ctx.AppDB(), pid, tableName)
	if err != nil {
		return nil, err
	}
	clause, vals, err := buildWhere(t, sliceArg(args, "where"))
	if err != nil {
		return nil, err
	}
	groups, err := buildAggregateGroups(t, sliceArg(args, "group_by"))
	if err != nil {
		return nil, err
	}
	metrics, err := buildAggregateMetrics(t, sliceArg(args, "metrics"))
	if err != nil {
		return nil, err
	}
	if err := ensureUniqueAggregateOutputs(groups, metrics); err != nil {
		return nil, err
	}
	selectParts := make([]string, 0, len(groups)+len(metrics))
	for _, g := range groups {
		selectParts = append(selectParts, g.SelectExpr)
	}
	for _, m := range metrics {
		selectParts = append(selectParts, m.SelectExpr)
	}
	if len(selectParts) == 0 {
		return nil, errf("at least one metric is required")
	}

	stmt := "SELECT " + strings.Join(selectParts, ", ") + " FROM " + quote(t.PhysicalName)
	if clause != "" {
		stmt += " " + clause
	}
	if len(groups) > 0 {
		groupParts := make([]string, 0, len(groups))
		for _, g := range groups {
			groupParts = append(groupParts, g.GroupExpr)
		}
		stmt += " GROUP BY " + strings.Join(groupParts, ", ")
	}
	orderBy, err := buildAggregateOrderBy(groups, metrics, strArg(args, "order_by"))
	if err != nil {
		return nil, err
	}
	if orderBy != "" {
		stmt += " " + orderBy
	}

	limit := intArg(args, "limit", maxQueryRows(ctx))
	if limit <= 0 {
		limit = maxQueryRows(ctx)
	}
	if max := maxQueryRows(ctx); limit > max {
		limit = max
	}
	stmt += fmt.Sprintf(" LIMIT %d", limit+1)

	qctx, cancel := queryTimeoutContext(ctx)
	defer cancel()
	rows, err := ctx.AppDB().QueryContext(qctx, stmt, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, truncatedByBytes, err := scanAggregateRows(rows, maxQueryBytes(ctx))
	if err != nil {
		return nil, err
	}
	truncated := truncatedByBytes
	if len(out) > limit {
		truncated = true
		out = out[:limit]
	}
	return map[string]any{"rows": out, "truncated": truncated}, nil
}

// ─── shared row machinery ──────────────────────────────────────────

func buildSelectAll(t *Table) string {
	cols := []string{`"id"`, `"created_at"`, `"updated_at"`}
	for _, c := range t.Columns {
		cols = append(cols, quote(c.Name))
	}
	return "SELECT " + strings.Join(cols, ", ")
}

// buildSelect returns a SELECT clause restricted to picks. Each name
// must be either a reserved column (id, created_at, updated_at) or a
// user column declared on t. Duplicates are silently deduped. Empty
// picks is an error — callers should fall through to buildSelectAll
// when select is omitted, not pass an empty list.
func buildSelect(t *Table, picks []string) (string, error) {
	if len(picks) == 0 {
		return "", errf("select must be non-empty if provided")
	}
	valid := map[string]bool{"id": true, "created_at": true, "updated_at": true}
	for _, c := range t.Columns {
		valid[c.Name] = true
	}
	seen := map[string]bool{}
	cols := make([]string, 0, len(picks))
	for _, p := range picks {
		if !valid[p] {
			return "", errf("select: unknown column %q", p)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		cols = append(cols, quote(p))
	}
	return "SELECT " + strings.Join(cols, ", "), nil
}

// parseSelect resolves the optional `select` arg into a SELECT clause.
// When the arg is absent or null, returns buildSelectAll (full row,
// matches pre-projection behavior). When present, validates each name
// against the table schema via buildSelect.
func parseSelect(args map[string]any, t *Table) (string, error) {
	raw, ok := args["select"]
	if !ok || raw == nil {
		return buildSelectAll(t), nil
	}
	arr, isArr := raw.([]any)
	if !isArr {
		return "", errf("select: must be array of column names")
	}
	picks := make([]string, 0, len(arr))
	for i, v := range arr {
		s, ok := v.(string)
		if !ok {
			return "", errf("select[%d]: must be string", i)
		}
		picks = append(picks, s)
	}
	return buildSelect(t, picks)
}

func scanRows(rows *sql.Rows, t *Table) ([]map[string]any, error) {
	out, _, err := scanRowsBudget(rows, t, 0, 0)
	return out, err
}

func scanRowsBudget(rows *sql.Rows, t *Table, byteCap int64, rowCap int) ([]map[string]any, bool, error) {
	out := []map[string]any{}
	var usedBytes int64
	truncated := false
	cols, err := rows.Columns()
	if err != nil {
		return nil, false, err
	}
	colIdx := map[string]int{}
	for i, c := range cols {
		colIdx[c] = i
	}
	for rows.Next() {
		if rowCap > 0 && len(out) >= rowCap {
			truncated = true
			break
		}
		dest := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, false, err
		}
		row := map[string]any{}
		// Reserved columns are populated only if actually present in
		// the result set — column projection (rows_search/rows_get's
		// `select` arg) may legitimately omit them. Indexing colIdx
		// unconditionally would silently read dest[0] when missing.
		if i, ok := colIdx["id"]; ok {
			row["id"] = scalarOrInt(dest[i])
		}
		if i, ok := colIdx["created_at"]; ok {
			row["created_at"] = scalarString(dest[i])
		}
		if i, ok := colIdx["updated_at"]; ok {
			row["updated_at"] = scalarString(dest[i])
		}
		for _, c := range t.Columns {
			i, ok := colIdx[c.Name]
			if !ok {
				continue
			}
			row[c.Name] = hydrateForResult(c, dest[i])
		}
		for k, v := range row {
			usedBytes += int64(len(k)) + valueSize(v)
		}
		if byteCap > 0 && usedBytes > byteCap {
			truncated = true
			break
		}
		out = append(out, row)
	}
	return out, truncated, rows.Err()
}

func scalarOrInt(v any) any {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return v
}

func scalarString(v any) any {
	switch n := v.(type) {
	case []byte:
		return string(n)
	case string:
		return n
	}
	return v
}

// fetchRowByID runs the supplied selectClause (e.g. buildSelectAll(t)
// or a projection produced by buildSelect) against t for the given id.
// Pass "" to default to a full-row select.
func fetchRowByID(db *sql.DB, t *Table, id int64, selectClause string) (map[string]any, bool, error) {
	if selectClause == "" {
		selectClause = buildSelectAll(t)
	}
	stmt := selectClause + " FROM " + quote(t.PhysicalName) + " WHERE id = ?"
	rows, err := db.Query(stmt, id)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out, err := scanRows(rows, t)
	if err != nil {
		return nil, false, err
	}
	if len(out) == 0 {
		return nil, false, nil
	}
	return out[0], true, nil
}

// hydrateFileColumns swaps file_id integer values for {id, url} maps
// by calling the storage app's files_get_url tool through the
// platform's app-to-app surface. Best-effort: any lookup failure
// leaves the integer in place.
func hydrateFileColumns(ctx *sdk.AppCtx, t *Table, row map[string]any) {
	api := ctx.PlatformAPI()
	if api == nil {
		return
	}
	for _, c := range t.Columns {
		if c.Type != "file_id" {
			continue
		}
		v, ok := row[c.Name]
		if !ok || v == nil {
			continue
		}
		var id int64
		switch n := v.(type) {
		case int64:
			id = n
		case float64:
			id = int64(n)
		default:
			continue
		}
		var resp struct {
			URL       string `json:"url"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := api.CallAppResult("storage", "files_get_url", map[string]any{"id": id}, &resp); err != nil {
			continue
		}
		row[c.Name] = map[string]any{"id": id, "url": resp.URL, "expires_at": resp.ExpiresAt}
	}
}

// ─── filter / order_by builders ────────────────────────────────────

type predicate struct {
	Col   string
	Op    string
	Value any
}

func buildWhere(t *Table, raw []any) (string, []any, error) {
	if len(raw) == 0 {
		return "", nil, nil
	}
	if len(raw) > 100 {
		return "", nil, errf("where exceeds maximum of 100 predicates")
	}
	allowed := map[string]Column{
		"id":         {Name: "id", Type: "number"},
		"created_at": {Name: "created_at", Type: "datetime"},
		"updated_at": {Name: "updated_at", Type: "datetime"},
	}
	for _, c := range t.Columns {
		allowed[c.Name] = c
	}

	var clauses []string
	var args []any
	for i, r := range raw {
		obj, ok := r.(map[string]any)
		if !ok {
			return "", nil, errf("where[%d]: must be an object", i)
		}
		p := predicate{
			Col:   strArg(obj, "col"),
			Op:    strArg(obj, "op"),
			Value: obj["value"],
		}
		col, ok := allowed[p.Col]
		if !ok {
			return "", nil, errf("where[%d]: unknown column %q", i, p.Col)
		}
		q := quote(col.Name)
		switch p.Op {
		case "eq", "neq", "lt", "lte", "gt", "gte":
			coerced, err := coerceForStorage(col, p.Value)
			if err != nil {
				return "", nil, errf("where[%d]: %w", i, err)
			}
			clauses = append(clauses, q+" "+sqlOp(p.Op)+" ?")
			args = append(args, coerced)
		case "contains":
			s, ok := p.Value.(string)
			if !ok {
				return "", nil, errf("where[%d]: op contains needs string value", i)
			}
			clauses = append(clauses, q+" LIKE ?")
			args = append(args, "%"+s+"%")
		case "in":
			arr, ok := p.Value.([]any)
			if !ok || len(arr) == 0 {
				return "", nil, errf("where[%d]: op in needs non-empty array", i)
			}
			if len(arr) > 1000 {
				return "", nil, errf("where[%d]: op in exceeds maximum of 1000 values", i)
			}
			placeholders := strings.Repeat("?,", len(arr))
			placeholders = placeholders[:len(placeholders)-1]
			clauses = append(clauses, q+" IN ("+placeholders+")")
			for _, v := range arr {
				cv, err := coerceForStorage(col, v)
				if err != nil {
					return "", nil, errf("where[%d]: %w", i, err)
				}
				args = append(args, cv)
			}
		case "between":
			arr, ok := p.Value.([]any)
			if !ok || len(arr) != 2 {
				return "", nil, errf("where[%d]: op between needs [low, high]", i)
			}
			lo, err := coerceForStorage(col, arr[0])
			if err != nil {
				return "", nil, errf("where[%d]: %w", i, err)
			}
			hi, err := coerceForStorage(col, arr[1])
			if err != nil {
				return "", nil, errf("where[%d]: %w", i, err)
			}
			clauses = append(clauses, q+" BETWEEN ? AND ?")
			args = append(args, lo, hi)
		case "is_null":
			clauses = append(clauses, q+" IS NULL")
		case "is_not_null":
			clauses = append(clauses, q+" IS NOT NULL")
		default:
			return "", nil, errf("where[%d]: unknown op %q", i, p.Op)
		}
	}
	return "WHERE " + strings.Join(clauses, " AND "), args, nil
}

type aggregateGroup struct {
	Name       string
	SelectExpr string
	GroupExpr  string
}

type aggregateMetric struct {
	Name       string
	SelectExpr string
}

func buildAggregateGroups(t *Table, raw []any) ([]aggregateGroup, error) {
	if len(raw) > 16 {
		return nil, errf("group_by exceeds maximum of 16 columns")
	}
	groups := make([]aggregateGroup, 0, len(raw))
	seen := map[string]bool{}
	for i, item := range raw {
		var colName, bucket, alias string
		switch v := item.(type) {
		case string:
			colName = v
		case map[string]any:
			colName = strArg(v, "col")
			bucket = strArg(v, "bucket")
			alias = strArg(v, "name")
		default:
			return nil, errf("group_by[%d]: must be a column name or object", i)
		}
		col, err := aggregateColumn(t, colName)
		if err != nil {
			return nil, errf("group_by[%d]: %w", i, err)
		}
		expr := quote(col.Name)
		if bucket != "" {
			if col.Type != "datetime" {
				return nil, errf("group_by[%d]: bucket requires datetime column, got %s", i, col.Type)
			}
			expr, err = bucketExpr(col.Name, bucket)
			if err != nil {
				return nil, errf("group_by[%d]: %w", i, err)
			}
			if alias == "" {
				alias = col.Name + "_" + bucket
			}
		}
		if alias == "" {
			alias = col.Name
		}
		if err := validateIdentifier("group alias", alias); err != nil {
			return nil, errf("group_by[%d]: %w", i, err)
		}
		if seen[alias] {
			return nil, errf("group_by[%d]: duplicate output name %q", i, alias)
		}
		seen[alias] = true
		groups = append(groups, aggregateGroup{
			Name:       alias,
			SelectExpr: expr + " AS " + quote(alias),
			GroupExpr:  expr,
		})
	}
	return groups, nil
}

func buildAggregateMetrics(t *Table, raw []any) ([]aggregateMetric, error) {
	if len(raw) == 0 {
		return nil, errf("metrics is required and must be non-empty")
	}
	if len(raw) > 32 {
		return nil, errf("metrics exceeds maximum of 32")
	}
	metrics := make([]aggregateMetric, 0, len(raw))
	seen := map[string]bool{}
	for i, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, errf("metrics[%d]: must be an object", i)
		}
		name := strArg(obj, "name")
		if err := validateIdentifier("metric name", name); err != nil {
			return nil, errf("metrics[%d]: %w", i, err)
		}
		if seen[name] {
			return nil, errf("metrics[%d]: duplicate output name %q", i, name)
		}
		seen[name] = true
		op := strArg(obj, "op")
		var expr string
		switch op {
		case "count":
			colName := strArg(obj, "col")
			if colName == "" {
				expr = "COUNT(*)"
			} else {
				col, err := aggregateColumn(t, colName)
				if err != nil {
					return nil, errf("metrics[%d]: %w", i, err)
				}
				if boolArg(obj, "distinct") {
					expr = "COUNT(DISTINCT " + quote(col.Name) + ")"
				} else {
					expr = "COUNT(" + quote(col.Name) + ")"
				}
			}
		case "sum", "avg":
			col, err := aggregateColumn(t, strArg(obj, "col"))
			if err != nil {
				return nil, errf("metrics[%d]: %w", i, err)
			}
			if col.Type != "number" {
				return nil, errf("metrics[%d]: op %s requires number column, got %s", i, op, col.Type)
			}
			expr = strings.ToUpper(op) + "(" + quote(col.Name) + ")"
		case "min", "max":
			col, err := aggregateColumn(t, strArg(obj, "col"))
			if err != nil {
				return nil, errf("metrics[%d]: %w", i, err)
			}
			expr = strings.ToUpper(op) + "(" + quote(col.Name) + ")"
		case "avg_ratio":
			num, err := aggregateColumn(t, strArg(obj, "numerator"))
			if err != nil {
				return nil, errf("metrics[%d]: numerator: %w", i, err)
			}
			den, err := aggregateColumn(t, strArg(obj, "denominator"))
			if err != nil {
				return nil, errf("metrics[%d]: denominator: %w", i, err)
			}
			if num.Type != "number" || den.Type != "number" {
				return nil, errf("metrics[%d]: avg_ratio requires number numerator and denominator", i)
			}
			expr = "AVG(CASE WHEN " + quote(den.Name) + " IS NOT NULL AND " + quote(den.Name) + " != 0 THEN " + quote(num.Name) + " / " + quote(den.Name) + " END)"
		default:
			return nil, errf("metrics[%d]: unknown op %q", i, op)
		}
		metrics = append(metrics, aggregateMetric{
			Name:       name,
			SelectExpr: expr + " AS " + quote(name),
		})
	}
	return metrics, nil
}

func ensureUniqueAggregateOutputs(groups []aggregateGroup, metrics []aggregateMetric) error {
	seen := map[string]bool{}
	for _, g := range groups {
		seen[g.Name] = true
	}
	for _, m := range metrics {
		if seen[m.Name] {
			return errf("duplicate aggregate output name %q", m.Name)
		}
		seen[m.Name] = true
	}
	return nil
}

func aggregateColumn(t *Table, name string) (Column, error) {
	switch name {
	case "id":
		return Column{Name: "id", Type: "number"}, nil
	case "created_at":
		return Column{Name: "created_at", Type: "datetime"}, nil
	case "updated_at":
		return Column{Name: "updated_at", Type: "datetime"}, nil
	}
	if err := validateIdentifier("column", name); err != nil {
		return Column{}, err
	}
	for _, c := range t.Columns {
		if c.Name == name {
			return c, nil
		}
	}
	return Column{}, errf("unknown column %q", name)
}

func bucketExpr(col, bucket string) (string, error) {
	q := quote(col)
	switch bucket {
	case "day":
		return "strftime('%Y-%m-%d', " + q + ")", nil
	case "week":
		return "strftime('%G-W%V', " + q + ")", nil
	case "month":
		return "strftime('%Y-%m', " + q + ")", nil
	case "year":
		return "strftime('%Y', " + q + ")", nil
	}
	return "", errf("bucket must be day, week, month, or year, got %q", bucket)
}

func buildAggregateOrderBy(groups []aggregateGroup, metrics []aggregateMetric, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 || len(parts) > 2 {
		return "", errf("order_by must be 'name' or 'name asc|desc'")
	}
	name := parts[0]
	allowed := map[string]bool{}
	for _, g := range groups {
		allowed[g.Name] = true
	}
	for _, m := range metrics {
		allowed[m.Name] = true
	}
	if !allowed[name] {
		return "", errf("order_by: unknown aggregate output %q", name)
	}
	dir := "ASC"
	if len(parts) == 2 {
		switch strings.ToLower(parts[1]) {
		case "asc":
			dir = "ASC"
		case "desc":
			dir = "DESC"
		default:
			return "", errf("order_by direction must be asc or desc, got %q", parts[1])
		}
	}
	return "ORDER BY " + quote(name) + " " + dir, nil
}

func scanAggregateRows(rows *sql.Rows, byteCap int64) ([]map[string]any, bool, error) {
	out := []map[string]any{}
	var usedBytes int64
	cols, err := rows.Columns()
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		dest := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, false, err
		}
		row := map[string]any{}
		for i, c := range cols {
			v := normaliseScanValue(dest[i])
			usedBytes += int64(len(c)) + valueSize(v)
			if byteCap > 0 && usedBytes > byteCap {
				return out, true, nil
			}
			row[c] = v
		}
		out = append(out, row)
	}
	return out, false, rows.Err()
}

func reserveRows(tx *sql.Tx, t *Table, delta, cap int64) error {
	if delta <= 0 {
		return nil
	}
	stmt := `UPDATE tables_meta SET row_count = row_count + ? WHERE id = ?`
	args := []any{delta, t.ID}
	if cap > 0 {
		stmt += ` AND row_count + ? <= ?`
		args = append(args, delta, cap)
	}
	res, err := tx.Exec(stmt, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		t.RowCount += delta
		return nil
	}
	var current int64
	if err := tx.QueryRow(`SELECT row_count FROM tables_meta WHERE id = ?`, t.ID).Scan(&current); err != nil {
		return err
	}
	return errf("would exceed max_rows_per_table (%d): table %q has %d rows, inserting %d", cap, t.Name, current, delta)
}

func sqlOp(op string) string {
	switch op {
	case "eq":
		return "="
	case "neq":
		return "!="
	case "lt":
		return "<"
	case "lte":
		return "<="
	case "gt":
		return ">"
	case "gte":
		return ">="
	}
	return "="
}

func buildOrderBy(t *Table, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return `ORDER BY "id" DESC`, nil
	}
	parts := strings.Fields(raw)
	col := parts[0]
	dir := "ASC"
	if len(parts) > 1 {
		switch strings.ToLower(parts[1]) {
		case "asc":
			dir = "ASC"
		case "desc":
			dir = "DESC"
		default:
			return "", errf("order_by direction must be asc or desc, got %q", parts[1])
		}
	}
	if col != "id" && col != "created_at" && col != "updated_at" && columnIndex(t.Columns, col) < 0 {
		return "", errf("order_by: unknown column %q", col)
	}
	return "ORDER BY " + quote(col) + " " + dir, nil
}
