package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── tables_create ─────────────────────────────────────────────────

func (a *App) toolTablesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "tables_create", true)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if err := validateIdentifier("table", name); err != nil {
		return nil, err
	}
	scope := strArg(args, "scope")
	if scope == "" {
		scope = "project"
	}
	if scope != "project" {
		return nil, errf("table scope must be 'project'; install the app globally to serve multiple projects while keeping each project's tables isolated")
	}

	rawCols := sliceArg(args, "columns")
	if len(rawCols) == 0 {
		return nil, errf("at least one column is required")
	}
	if err := checkBatchBytes(ctx, rawCols); err != nil {
		return nil, err
	}
	cols, err := parseColumnDefs(rawCols)
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		if c.Default == nil {
			continue
		}
		v, _ := coerceForStorage(c, c.Default)
		if err := validateStoredValueSize(ctx, c, v); err != nil {
			return nil, errf("column %q default: %v", c.Name, err)
		}
	}

	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var existing int64
	if err := tx.QueryRow(`SELECT id FROM tables_meta WHERE project_id = ? AND name = ?`, pid, name).Scan(&existing); err == nil {
		return nil, errf("table %q already exists", name)
	} else if err != sql.ErrNoRows {
		return nil, err
	}

	var tableCount int
	if err := tx.QueryRow("SELECT COUNT(*) FROM tables_meta WHERE project_id=?", pid).Scan(&tableCount); err != nil {
		return nil, err
	}
	if int64(tableCount) >= cfgInt64Range(ctx, "max_tables_per_project", 1000, 1, 100000) {
		return nil, errf("max_tables_per_project exceeded")
	}
	var nextID int64
	if err := tx.QueryRow("UPDATE table_identity SET last_id=MAX(last_id,COALESCE((SELECT MAX(id) FROM tables_meta),0))+1 RETURNING last_id").Scan(&nextID); err != nil {
		return nil, err
	}
	res, err := tx.Exec(`INSERT INTO tables_meta(id,project_id, scope, name, physical_name, row_count, storage_version) VALUES (?, ?, 'project', ?, '', 0, 1)`, nextID, pid, name)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	physical := fmt.Sprintf("t_%d", id)
	if _, err := tx.Exec(`UPDATE tables_meta SET physical_name = ? WHERE id = ?`, physical, id); err != nil {
		return nil, err
	}

	for i, c := range cols {
		dv, err := jsonStringify(c.Default)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO columns_meta(table_id, name, type, nullable, default_value, position) VALUES (?, ?, ?, ?, ?, ?)`,
			id, c.Name, c.Type, boolToInt(c.Nullable), dv, i); err != nil {
			return nil, err
		}
	}

	createSQL, err := buildCreateTableSQL(physical, cols)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(createSQL); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.cache.invalidate(pid, name)

	emit(ctx, topicTableCreated, map[string]any{
		"id":      id,
		"name":    name,
		"scope":   scope,
		"columns": cols,
	})
	return map[string]any{
		"id":      id,
		"name":    name,
		"scope":   scope,
		"columns": cols,
	}, nil
}

func parseColumnDefs(raw []any) ([]Column, error) {
	if len(raw) > 256 {
		return nil, errf("columns exceeds maximum of 256")
	}
	out := make([]Column, 0, len(raw))
	seen := map[string]bool{}
	for i, r := range raw {
		obj, ok := r.(map[string]any)
		if !ok {
			return nil, errf("columns[%d]: must be an object", i)
		}
		c := Column{
			Name: strArg(obj, "name"),
			Type: strArg(obj, "type"),
		}
		if err := validateIdentifier("column", c.Name); err != nil {
			return nil, fmt.Errorf("columns[%d]: %w", i, err)
		}
		if reservedColumns[c.Name] {
			return nil, errf("columns[%d]: %q is a reserved column name", i, c.Name)
		}
		if seen[c.Name] {
			return nil, errf("columns[%d]: duplicate column %q", i, c.Name)
		}
		seen[c.Name] = true
		if !validColumnTypes[c.Type] {
			return nil, errf("columns[%d]: type %q must be one of text|number|bool|datetime|json|file_id", i, c.Type)
		}
		// nullable defaults to true when omitted; explicit false means
		// the column is required (and a default may stand in).
		if v, ok := obj["nullable"]; ok {
			b, ok := v.(bool)
			if !ok {
				return nil, errf("columns[%d]: nullable must be boolean", i)
			}
			c.Nullable = b
		} else {
			c.Nullable = true
		}
		c.Default = obj["default"]
		if c.Default != nil {
			if _, err := coerceForStorage(c, c.Default); err != nil {
				return nil, errf("columns[%d] default: %v", i, err)
			}
		}
		if !c.Nullable && c.Default == nil {
			// Allowed: agent must supply a value on every insert.
		}
		out = append(out, c)
	}
	return out, nil
}

func buildCreateTableSQL(physical string, cols []Column) (string, error) {
	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(quote(physical))
	b.WriteString(" (")
	b.WriteString(`"id" INTEGER PRIMARY KEY AUTOINCREMENT, `)
	b.WriteString(`"created_at" TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f','now') || '000000Z'), `)
	b.WriteString(`"updated_at" TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f','now') || '000000Z'), "_revision" INTEGER NOT NULL DEFAULT 1`)
	for _, c := range cols {
		st, err := sqliteType(c.Type)
		if err != nil {
			return "", err
		}
		b.WriteString(", ")
		b.WriteString(quote(c.Name))
		b.WriteString(" ")
		b.WriteString(st)
		if !c.Nullable {
			b.WriteString(" NOT NULL")
		}
		// Defaults are enforced in the Go layer (coerceForStorage
		// substitutes when nil), not via sqlite DEFAULT clauses, so
		// type/JSON defaults round-trip identically to user-supplied
		// values.
	}
	b.WriteString(")")
	return b.String(), nil
}

// ─── tables_list ───────────────────────────────────────────────────

func (a *App) toolTablesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "tables_list", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	qctx, cancel := queryTimeoutContext(ctx)
	defer cancel()
	if boolArg(args, "summary") {
		return listTableSummaries(ctx, pid, args)
	}
	limit := intArg(args, "limit", 100)
	if limit < 1 || limit > 1000 {
		return nil, errf("limit must be 1..1000")
	}
	offset := intArg(args, "offset", 0)
	tables, err := loadTablesPage(qctx, ctx.AppReadDB(), pid, limit+1, offset, maxQueryBytes(ctx))
	if err != nil {
		return nil, queryStageErr("metadata", "<tables>", err)
	}
	more := len(tables) > limit
	if more {
		tables = tables[:limit]
	}
	for i := range tables {
		if !tables[i].RowCountKnown {
			count, err := currentRowCount(ctx, &tables[i])
			if err != nil {
				return nil, err
			}
			tables[i].RowCount = count
			tables[i].RowCountKnown = true
		}

	}
	out := make([]map[string]any, 0, len(tables))
	for _, t := range tables {
		out = append(out, map[string]any{
			"id":         t.ID,
			"name":       t.Name,
			"scope":      t.Scope,
			"columns":    t.Columns,
			"row_count":  t.RowCount,
			"created_at": t.CreatedAt,
		})
	}
	return map[string]any{"tables": out, "has_more": more, "next_offset": offset + len(out)}, nil
}

// ─── tables_describe ───────────────────────────────────────────────

func (a *App) toolTablesDescribe(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "tables_describe", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if err := validateIdentifier("table", name); err != nil {
		return nil, err
	}
	t, err := a.loadTableWithCount(ctx, pid, name)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":         t.ID,
		"name":       t.Name,
		"scope":      t.Scope,
		"columns":    t.Columns,
		"row_count":  t.RowCount,
		"created_at": t.CreatedAt,
	}, nil
}

// ─── tables_alter ──────────────────────────────────────────────────

func (a *App) toolTablesAlter(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "tables_alter", true)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if err := validateIdentifier("table", name); err != nil {
		return nil, err
	}
	t, err := a.loadTableWithCount(ctx, pid, name)
	if err != nil {
		return nil, err
	}

	add := mapArg(args, "add")
	rename := mapArg(args, "rename")
	drop := strArg(args, "drop")
	provided := 0
	if add != nil {
		provided++
	}
	if rename != nil {
		provided++
	}
	if drop != "" {
		provided++
	}
	if provided != 1 {
		return nil, errf("exactly one of add / rename / drop must be supplied")
	}

	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// changeKind + changeCol are populated by the executed branch so we
	// can emit a single typed table.altered event after commit.
	var changeKind, changeCol string

	switch {
	case add != nil:
		if len(t.Columns) >= 256 {
			return nil, errf("table already has the maximum of 256 user columns")
		}
		cols, err := parseColumnDefs([]any{add})
		if err != nil {
			return nil, err
		}
		c := cols[0]
		if c.Default != nil {
			v, _ := coerceForStorage(c, c.Default)
			if err := validateStoredValueSize(ctx, c, v); err != nil {
				return nil, errf("column %q default: %v", c.Name, err)
			}
		}
		if columnIndex(t.Columns, c.Name) >= 0 {
			return nil, errf("column %q already exists", c.Name)
		}
		if !c.Nullable && c.Default == nil && t.RowCount > 0 {
			return nil, errf("non-nullable column needs a default when the table already has rows")
		}
		st, err := sqliteType(c.Type)
		if err != nil {
			return nil, err
		}
		ddl := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", quote(t.PhysicalName), quote(c.Name), st)
		if !c.Nullable {
			ddl += " NOT NULL"
		}
		if c.Default != nil {
			lit, err := sqlLiteral(c.Type, c.Default)
			if err != nil {
				return nil, err
			}
			ddl += " DEFAULT " + lit
		}
		if _, err := tx.Exec(ddl); err != nil {
			return nil, err
		}
		dv, err := jsonStringify(c.Default)
		if err != nil {
			return nil, err
		}
		var nextPos int
		if err := tx.QueryRow("SELECT COALESCE(MAX(position),-1)+1 FROM columns_meta WHERE table_id=?", t.ID).Scan(&nextPos); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO columns_meta(table_id, name, type, nullable, default_value, position) VALUES (?, ?, ?, ?, ?, ?)`,
			t.ID, c.Name, c.Type, boolToInt(c.Nullable), dv, nextPos); err != nil {
			return nil, err
		}
		t.Columns = append(t.Columns, c)
		changeKind, changeCol = "add", c.Name

	case rename != nil:
		from := strArg(rename, "from")
		to := strArg(rename, "to")
		if err := validateIdentifier("column", from); err != nil {
			return nil, fmt.Errorf("rename.from: %w", err)
		}
		if err := validateIdentifier("column", to); err != nil {
			return nil, fmt.Errorf("rename.to: %w", err)
		}
		if reservedColumns[from] || reservedColumns[to] {
			return nil, errf("cannot rename reserved columns")
		}
		if columnIndex(t.Columns, from) < 0 {
			return nil, errf("column %q not found", from)
		}
		if columnIndex(t.Columns, to) >= 0 {
			return nil, errf("column %q already exists", to)
		}
		ddl := fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", quote(t.PhysicalName), quote(from), quote(to))
		if _, err := tx.Exec(ddl); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE columns_meta SET name = ? WHERE table_id = ? AND name = ?`, to, t.ID, from); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE index_columns SET column_name = ?
			WHERE column_name = ? AND index_id IN (SELECT id FROM indexes_meta WHERE table_id = ?)`, to, from, t.ID); err != nil {
			return nil, err
		}
		t.Columns[columnIndex(t.Columns, from)].Name = to
		changeKind, changeCol = "rename", to

	case drop != "":
		if err := validateIdentifier("column", drop); err != nil {
			return nil, err
		}
		if reservedColumns[drop] {
			return nil, errf("cannot drop reserved column %q", drop)
		}
		if columnIndex(t.Columns, drop) < 0 {
			return nil, errf("column %q not found", drop)
		}
		if err := prepareIndexesForColumnDrop(tx, t, drop); err != nil {
			return nil, err
		}
		ddl := fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", quote(t.PhysicalName), quote(drop))
		if _, err := tx.Exec(ddl); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM columns_meta WHERE table_id = ? AND name = ?`, t.ID, drop); err != nil {
			return nil, err
		}
		i := columnIndex(t.Columns, drop)
		t.Columns = append(t.Columns[:i], t.Columns[i+1:]...)
		changeKind, changeCol = "drop", drop
	}

	if _, err := tx.Exec(`UPDATE tables_meta SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, t.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.cache.invalidate(pid, name)

	updated := t
	emit(ctx, topicTableAltered, map[string]any{
		"id":      updated.ID,
		"name":    updated.Name,
		"change":  changeKind,
		"column":  changeCol,
		"columns": updated.Columns,
	})
	return map[string]any{
		"id":        updated.ID,
		"name":      updated.Name,
		"scope":     updated.Scope,
		"columns":   updated.Columns,
		"row_count": updated.RowCount,
	}, nil
}

func prepareIndexesForColumnDrop(tx *writeTx, table *Table, column string) error {
	if err := reconcileIndexes(tx, table); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT i.id, i.name, i.physical_name, i.managed
		FROM indexes_meta i
		JOIN index_columns c ON c.index_id = i.id
		WHERE i.table_id = ? AND c.column_name = ?
		ORDER BY i.name`, table.ID, column)
	if err != nil {
		return err
	}
	type indexRef struct {
		id       int64
		name     string
		physical string
		managed  bool
	}
	var managed []indexRef
	var userNames []string
	for rows.Next() {
		var ref indexRef
		var isManaged int
		if err := rows.Scan(&ref.id, &ref.name, &ref.physical, &isManaged); err != nil {
			rows.Close()
			return err
		}
		ref.managed = isManaged != 0
		if ref.managed {
			managed = append(managed, ref)
		} else {
			userNames = append(userNames, ref.name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(userNames) > 0 {
		return errf("column %q is used by indexes (%s); drop those indexes first", column, strings.Join(userNames, ", "))
	}
	for _, ref := range managed {
		if _, err := tx.Exec("DROP INDEX " + quote(ref.physical)); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM indexes_meta WHERE id = ?`, ref.id); err != nil {
			return err
		}
	}
	return nil
}

// sqlLiteral produces a safe inline SQL literal for the small subset
// of types ALTER TABLE ADD COLUMN ... DEFAULT can accept. The agent
// can't smuggle SQL because the type set is closed and each branch
// formats deterministically.
func sqlLiteral(t string, v any) (string, error) {
	if v == nil {
		return "NULL", nil
	}
	switch t {
	case "datetime":
		value, err := coerceForStorage(Column{Name: "default", Type: "datetime", Nullable: true}, v)
		if err != nil {
			return "", err
		}
		return sqlLiteral("text", value)
	case "text":
		s, ok := v.(string)
		if !ok {
			return "", errf("default for %s must be string", t)
		}
		return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
	case "json":
		b, err := jsonStringify(v)
		if err != nil {
			return "", err
		}
		return "'" + strings.ReplaceAll(b, "'", "''") + "'", nil
	case "number":
		switch n := v.(type) {
		case float64:
			return fmt.Sprintf("%v", n), nil
		case int:
			return fmt.Sprintf("%d", n), nil
		case int64:
			return fmt.Sprintf("%d", n), nil
		case json.Number:
			if _, err := n.Float64(); err != nil {
				return "", errf("default for number must be numeric")
			}
			return n.String(), nil
		}
		return "", errf("default for number must be numeric")
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return "", errf("default for bool must be boolean")
		}
		if b {
			return "1", nil
		}
		return "0", nil
	case "file_id":
		switch n := v.(type) {
		case float64:
			return fmt.Sprintf("%d", int64(n)), nil
		case int:
			return fmt.Sprintf("%d", n), nil
		case int64:
			return fmt.Sprintf("%d", n), nil
		case json.Number:
			i, err := n.Int64()
			if err != nil {
				return "", errf("default for file_id must be integer")
			}
			return fmt.Sprintf("%d", i), nil
		}
		return "", errf("default for file_id must be integer")
	}
	return "", errf("unsupported default type %q", t)
}

// ─── tables_drop ───────────────────────────────────────────────────

func (a *App) toolTablesDrop(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "tables_drop", true)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if err := validateIdentifier("table", name); err != nil {
		return nil, err
	}
	if !boolArg(args, "confirm") {
		return nil, errf("confirm=true required to drop %q", name)
	}
	t, err := a.loadTableSchema(ctx, pid, name)
	if err != nil {
		if errorStatus(err) == 404 {
			return map[string]any{"dropped": name}, nil
		}
		return nil, err
	}

	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", quote(t.PhysicalName))); err != nil {
		return nil, err
	}
	// columns_meta cascades via the FK ON DELETE CASCADE in the schema.
	if _, err := tx.Exec(`DELETE FROM tables_meta WHERE id = ?`, t.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.cache.invalidate(pid, name)
	emit(ctx, topicTableDropped, map[string]any{
		"id":   t.ID,
		"name": name,
	})
	return map[string]any{"dropped": name}, nil
}

// ─── shared loaders ────────────────────────────────────────────────

func loadTable(db *sql.DB, projectID, name string) (*Table, error) {
	t := &Table{Name: name}
	var rowCount sql.NullInt64
	err := db.QueryRow(`SELECT id, scope, physical_name, created_at, row_count
		FROM tables_meta
		WHERE name = ? AND project_id = ?
		LIMIT 1`, name, projectID).
		Scan(&t.ID, &t.Scope, &t.PhysicalName, &t.CreatedAt, &rowCount)
	if err == sql.ErrNoRows {
		return nil, notFound("table %q not found", name)
	}
	if err != nil {
		return nil, err
	}
	cols, err := loadColumns(db, t.ID)
	if err != nil {
		return nil, err
	}
	t.Columns = cols
	if rowCount.Valid {
		t.RowCount = rowCount.Int64
		t.RowCountKnown = true
	} else if err := initialiseRowCount(db, t); err != nil {
		return nil, err
	}
	return t, nil
}

func loadTables(db *sql.DB, projectID string) ([]Table, error) {
	return loadTablesContext(context.Background(), db, projectID)
}

func loadTablesContext(ctx context.Context, db *sql.DB, projectID string) ([]Table, error) {
	return loadTablesPage(ctx, db, projectID, -1, 0, 0)
}

// One statement gives the table list and its columns the same SQLite snapshot.
func loadTablesPage(ctx context.Context, db *sql.DB, projectID string, limit, offset int, byteCap int64) ([]Table, error) {
	rows, err := db.QueryContext(ctx, `WITH chosen AS (
 SELECT * FROM tables_meta WHERE project_id=? ORDER BY name LIMIT ? OFFSET ?)
 SELECT t.id,t.name,t.scope,t.physical_name,t.created_at,t.row_count,c.name,c.type,c.nullable,c.default_value
 FROM chosen t LEFT JOIN columns_meta c ON c.table_id=t.id ORDER BY t.name,c.position`, projectID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Table{}
	var used int64
	for rows.Next() {
		var id int64
		var name, scope, physical, created string
		var count, nullable sql.NullInt64
		var colName, colType, def sql.NullString
		if err := rows.Scan(&id, &name, &scope, &physical, &created, &count, &colName, &colType, &nullable, &def); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != id {
			out = append(out, Table{ID: id, Name: name, Scope: scope, PhysicalName: physical, CreatedAt: created, RowCount: count.Int64, RowCountKnown: count.Valid, Columns: []Column{}})
			if byteCap > 0 && limit > 0 && len(out) == limit {
				break
			} // final table only signals another page
			used += int64(len(name) + len(scope) + len(created) + 128)
		}
		if colName.Valid {
			col := Column{Name: colName.String, Type: colType.String, Nullable: nullable.Int64 != 0}
			if def.Valid && def.String != "" {
				col.Default, err = jsonParse(def.String)
				if err != nil {
					return nil, err
				}
			}
			encoded, err := json.Marshal(col)
			if err != nil {
				return nil, err
			}
			used += int64(len(encoded) + 1)
			out[len(out)-1].Columns = append(out[len(out)-1].Columns, col)
		}
		if byteCap > 0 && used > byteCap {
			return nil, &statusError{413, "schema page exceeds max_query_bytes; use summary=true and tables_describe, or a smaller limit"}
		}
	}
	return out, rows.Err()
}

func initialiseRowCount(db *sql.DB, t *Table) error {
	if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", quote(t.PhysicalName))).Scan(&t.RowCount); err != nil {
		return err
	}
	t.RowCountKnown = true
	_, err := db.Exec(`UPDATE tables_meta SET row_count = ? WHERE id = ? AND row_count IS NULL`, t.RowCount, t.ID)
	return err
}

func loadColumns(db *sql.DB, tableID int64) ([]Column, error) {
	rows, err := db.Query(`SELECT name, type, nullable, default_value FROM columns_meta WHERE table_id = ? ORDER BY position`, tableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Column{}
	for rows.Next() {
		var c Column
		var nullable int
		var defaultRaw sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &nullable, &defaultRaw); err != nil {
			return nil, err
		}
		c.Nullable = nullable != 0
		if defaultRaw.Valid && defaultRaw.String != "" {
			v, err := jsonParse(defaultRaw.String)
			if err != nil {
				return nil, err
			}
			c.Default = v
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func columnIndex(cols []Column, name string) int {
	for i, c := range cols {
		if c.Name == name {
			return i
		}
	}
	return -1
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func listTableSummaries(ctx *sdk.AppCtx, pid string, args map[string]any) (any, error) {
	limit := intArg(args, "limit", 100)
	if limit < 1 || limit > 1000 {
		return nil, errf("limit must be 1..1000")
	}
	offset := intArg(args, "offset", 0)
	qctx, cancel := queryTimeoutContext(ctx)
	defer cancel()
	rows, err := ctx.AppReadDB().QueryContext(qctx, `SELECT id,name,scope,COALESCE(row_count,0),created_at FROM tables_meta WHERE project_id=? ORDER BY name LIMIT ? OFFSET ?`, pid, limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, count int64
		var name, scope, created string
		if err := rows.Scan(&id, &name, &scope, &count, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "name": name, "scope": scope, "row_count": count, "created_at": created})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return map[string]any{"tables": out, "has_more": more, "next_offset": offset + len(out)}, nil
}
