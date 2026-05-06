package main

import (
	"database/sql"
	"fmt"
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
	t, err := loadTable(ctx.AppDB(), pid, tableName)
	if err != nil {
		return nil, err
	}
	if cap := maxRowsPerTable(ctx); cap > 0 && t.RowCount+int64(len(rawRows)) > cap {
		return nil, errf("would exceed max_rows_per_table (%d): table %q has %d rows, inserting %d",
			cap, tableName, t.RowCount, len(rawRows))
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
	row, found, err := fetchRowByID(ctx.AppDB(), t, id)
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
	row, _, err := fetchRowByID(ctx.AppDB(), t, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, topicRowUpdated, map[string]any{
		"table": tableName,
		"id":    id,
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
		res, err := ctx.AppDB().Exec(fmt.Sprintf("DELETE FROM %s WHERE id = ?", quote(t.PhysicalName)), id)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			emit(ctx, topicRowDeleted, map[string]any{
				"table":   tableName,
				"id":      id,
				"deleted": n,
			})
		}
		return map[string]any{"deleted": n}, nil
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
	res, err := ctx.AppDB().Exec(stmt, vals...)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		emit(ctx, topicRowDeleted, map[string]any{
			"table":   tableName,
			"deleted": n,
		})
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

	var total int64
	totalSQL := "SELECT COUNT(*) FROM " + quote(t.PhysicalName)
	if clause != "" {
		totalSQL += " " + clause
	}
	if err := ctx.AppDB().QueryRow(totalSQL, vals...).Scan(&total); err != nil {
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

	stmt := buildSelectAll(t) + " FROM " + quote(t.PhysicalName)
	if clause != "" {
		stmt += " " + clause
	}
	stmt += " " + orderBy
	stmt += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	rows, err := ctx.AppDB().Query(stmt, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanRows(rows, t)
	if err != nil {
		return nil, err
	}
	return map[string]any{"rows": out, "total": total}, nil
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
	stmt := "SELECT COUNT(*) FROM " + quote(t.PhysicalName)
	if clause != "" {
		stmt += " " + clause
	}
	var n int64
	if err := ctx.AppDB().QueryRow(stmt, vals...).Scan(&n); err != nil {
		return nil, err
	}
	return map[string]any{"count": n}, nil
}

// ─── shared row machinery ──────────────────────────────────────────

func buildSelectAll(t *Table) string {
	cols := []string{`"id"`, `"created_at"`, `"updated_at"`}
	for _, c := range t.Columns {
		cols = append(cols, quote(c.Name))
	}
	return "SELECT " + strings.Join(cols, ", ")
}

func scanRows(rows *sql.Rows, t *Table) ([]map[string]any, error) {
	out := []map[string]any{}
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	colIdx := map[string]int{}
	for i, c := range cols {
		colIdx[c] = i
	}
	for rows.Next() {
		dest := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		row["id"] = scalarOrInt(dest[colIdx["id"]])
		row["created_at"] = scalarString(dest[colIdx["created_at"]])
		row["updated_at"] = scalarString(dest[colIdx["updated_at"]])
		for _, c := range t.Columns {
			i, ok := colIdx[c.Name]
			if !ok {
				continue
			}
			row[c.Name] = hydrateForResult(c, dest[i])
		}
		out = append(out, row)
	}
	return out, rows.Err()
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

func fetchRowByID(db *sql.DB, t *Table, id int64) (map[string]any, bool, error) {
	stmt := buildSelectAll(t) + " FROM " + quote(t.PhysicalName) + " WHERE id = ?"
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
		raw, err := api.CallApp("storage", "files_get_url", map[string]any{"id": id})
		if err != nil || raw == nil {
			continue
		}
		inner, err := mcpInnerJSON(raw)
		if err != nil {
			continue
		}
		var resp map[string]any
		if err := jsonUnmarshalRaw(inner, &resp); err == nil {
			row[c.Name] = map[string]any{"id": id, "url": resp["url"], "expires_at": resp["expires_at"]}
		}
	}
}

func jsonUnmarshalRaw(raw []byte, v any) error {
	if len(raw) == 0 {
		return errf("empty payload")
	}
	return jsonUnmarshalBytes(raw, v)
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
