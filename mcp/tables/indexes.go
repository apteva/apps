package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type IndexColumn struct {
	Col   string `json:"col"`
	Order string `json:"order"`
}

type TableIndex struct {
	Name      string        `json:"name"`
	Columns   []IndexColumn `json:"columns"`
	Unique    bool          `json:"unique"`
	Managed   bool          `json:"managed"`
	CreatedAt string        `json:"created_at,omitempty"`
}

func (a *App) toolIndexesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "indexes_create", true)
	if err != nil {
		return nil, err
	}
	defer finish()

	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tableName := strArg(args, "table")
	if err := validateIdentifier("table", tableName); err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if err := validateIdentifier("index", name); err != nil {
		return nil, err
	}
	if strings.HasPrefix(name, "managed_") {
		return nil, errf("index names beginning with managed_ are reserved")
	}
	table, err := a.loadTableSchema(ctx, pid, tableName)
	if err != nil {
		return nil, err
	}
	columns, err := parseIndexColumns(table, sliceArg(args, "columns"))
	if err != nil {
		return nil, err
	}
	unique := boolArg(args, "unique")
	physical := physicalIndexName(table.ID, name, unique)

	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existing int64
	if err := tx.QueryRow(`SELECT id FROM indexes_meta WHERE table_id = ? AND name = ?`, table.ID, name).Scan(&existing); err == nil {
		return nil, errf("index %q already exists on table %q", name, tableName)
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	var indexCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM indexes_meta WHERE table_id = ?`, table.ID).Scan(&indexCount); err != nil {
		return nil, err
	}
	if indexCount >= 64 {
		return nil, errf("table %q already has the maximum of 64 indexes", tableName)
	}
	res, err := tx.Exec(`INSERT INTO indexes_meta(table_id, name, physical_name, unique_index, managed)
		VALUES (?, ?, ?, ?, 0)`, table.ID, name, physical, boolToInt(unique))
	if err != nil {
		return nil, err
	}
	indexID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := insertIndexColumns(tx, indexID, columns); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(buildCreateIndexSQL(physical, table.PhysicalName, columns, unique)); err != nil {
		if unique {
			return nil, errf("cannot create unique index %q; remove duplicate rows first: %v", name, err)
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	index := TableIndex{Name: name, Columns: columns, Unique: unique, Managed: false}
	return map[string]any{"index": index}, nil
}

func (a *App) toolIndexesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "indexes_list", false)
	if err != nil {
		return nil, err
	}
	defer finish()

	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tableName := strArg(args, "table")
	if err := validateIdentifier("table", tableName); err != nil {
		return nil, err
	}
	table, err := a.loadTableSchema(ctx, pid, tableName)
	if err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(requestContext(ctx), time.Duration(maxQueryMs(ctx))*time.Millisecond)
	defer cancel()
	rows, err := ctx.AppReadDB().QueryContext(qctx, `SELECT
		i.id, i.name, i.unique_index, i.managed, i.created_at,
		c.column_name, c.direction
		FROM indexes_meta i
		JOIN index_columns c ON c.index_id = i.id
		WHERE i.table_id = ?
		ORDER BY i.name, c.position`, table.ID)
	if err != nil {
		return nil, queryStageErr("metadata", tableName, err)
	}
	defer rows.Close()
	indexes := []TableIndex{}
	var currentID int64
	for rows.Next() {
		var id int64
		var name, created, column, direction string
		var unique, managed int
		if err := rows.Scan(&id, &name, &unique, &managed, &created, &column, &direction); err != nil {
			return nil, err
		}
		if len(indexes) == 0 || id != currentID {
			indexes = append(indexes, TableIndex{Name: name, Unique: unique != 0, Managed: managed != 0, CreatedAt: created})
			currentID = id
		}
		indexes[len(indexes)-1].Columns = append(indexes[len(indexes)-1].Columns, IndexColumn{Col: column, Order: direction})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"indexes": indexes}, nil
}

func (a *App) toolIndexesDrop(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(ctx, args, "indexes_drop", true)
	if err != nil {
		return nil, err
	}
	defer finish()

	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tableName := strArg(args, "table")
	name := strArg(args, "name")
	if err := validateIdentifier("table", tableName); err != nil {
		return nil, err
	}
	if err := validateIdentifier("index", name); err != nil {
		return nil, err
	}
	if !boolArg(args, "confirm") {
		return nil, errf("confirm=true required to drop index %q", name)
	}
	table, err := a.loadTableSchema(ctx, pid, tableName)
	if err != nil {
		return nil, err
	}
	tx, err := beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var indexID int64
	var physical string
	var managed int
	if err := tx.QueryRow(`SELECT id, physical_name, managed FROM indexes_meta WHERE table_id = ? AND name = ?`, table.ID, name).
		Scan(&indexID, &physical, &managed); err == sql.ErrNoRows {
		return nil, notFound("index %q not found on table %q", name, tableName)
	} else if err != nil {
		return nil, err
	}
	if managed != 0 && !boolArg(args, "release_managed") {
		return nil, errf("index %q is managed by rows_upsert; pass release_managed=true to remove its uniqueness guarantee", name)
	}
	if _, err := tx.Exec("DROP INDEX " + quote(physical)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM indexes_meta WHERE id = ?`, indexID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"dropped": name}, nil
}

func parseIndexColumns(table *Table, raw []any) ([]IndexColumn, error) {
	if len(raw) == 0 {
		return nil, errf("columns is required and must be non-empty")
	}
	if len(raw) > 16 {
		return nil, errf("columns exceeds maximum of 16")
	}
	valid := map[string]bool{"id": true, "created_at": true, "updated_at": true, "_revision": true}
	for _, column := range table.Columns {
		valid[column.Name] = true
	}
	seen := map[string]bool{}
	columns := make([]IndexColumn, 0, len(raw))
	for i, item := range raw {
		column := IndexColumn{Order: "asc"}
		switch value := item.(type) {
		case string:
			column.Col = value
		case map[string]any:
			column.Col = strArg(value, "col")
			if order := strings.ToLower(strArg(value, "order")); order != "" {
				column.Order = order
			}
		default:
			return nil, errf("columns[%d] must be a column name or object", i)
		}
		if !valid[column.Col] {
			return nil, errf("columns[%d]: unknown column %q", i, column.Col)
		}
		if seen[column.Col] {
			return nil, errf("columns[%d]: duplicate column %q", i, column.Col)
		}
		if column.Order != "asc" && column.Order != "desc" {
			return nil, errf("columns[%d]: order must be asc or desc", i)
		}
		seen[column.Col] = true
		columns = append(columns, column)
	}
	return columns, nil
}

func physicalIndexName(tableID int64, name string, unique bool) string {
	sum := sha256.Sum256([]byte(name))
	prefix := "ix"
	if unique {
		prefix = "ux"
	}
	return fmt.Sprintf("%s_t_%d_%x", prefix, tableID, sum[:8])
}

func buildCreateIndexSQL(physical, tablePhysical string, columns []IndexColumn, unique bool) string {
	parts := make([]string, len(columns))
	for i, column := range columns {
		parts[i] = quote(column.Col) + " " + strings.ToUpper(column.Order)
	}
	modifier := ""
	if unique {
		modifier = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", modifier, quote(physical), quote(tablePhysical), strings.Join(parts, ", "))
}

func insertIndexColumns(tx *writeTx, indexID int64, columns []IndexColumn) error {
	for position, column := range columns {
		if _, err := tx.Exec(`INSERT INTO index_columns(index_id, column_name, direction, position) VALUES (?, ?, ?, ?)`,
			indexID, column.Col, column.Order, position); err != nil {
			return err
		}
	}
	return nil
}
