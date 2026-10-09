package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

const projectionIndexGenerationFirst = "generation_first"
const projectionIndexFilterFirst = "filter_first"

func projectionIndexDefinition(app *sdk.AppCtx, pid string, args map[string]any) (*projectionDefinition, error) {
	cp := map[string]any{"name": strArg(args, "table")}
	if v, ok := args["version"]; ok {
		cp["version"] = v
	}
	return projectionFromArgs(app, pid, cp)
}
func projectionIndexPhysical(p *projectionDefinition, name string, unique bool) string {
	return fmt.Sprintf("pi_%d_%s", p.ID, physicalIndexName(p.ID, name, unique))
}
func projectionIndexPermission(app *sdk.AppCtx, p *projectionDefinition, manage bool) error {
	permission := "projections.read"
	if manage {
		permission = "projections.manage"
	}
	if !sdk.CallerFrom(requestContext(app)).Allows(permission, p.Name) {
		return &statusError{403, "projection permission denied"}
	}
	return nil
}
func projectionIndexColumns(columns []IndexColumn, layout string) []IndexColumn {
	generation := IndexColumn{Col: "_projection_generation", Order: "asc"}
	if layout == projectionIndexFilterFirst {
		return append(append([]IndexColumn{}, columns...), generation)
	}
	return append([]IndexColumn{generation}, columns...)
}
func validateProjectionIndex(p *projectionDefinition, ix *TableIndex) error {
	if ix.Layout != projectionIndexGenerationFirst && ix.Layout != projectionIndexFilterFirst {
		return errf("layout must be generation_first or filter_first")
	}
	if ix.Unique {
		if ix.Layout != projectionIndexGenerationFirst {
			return errf("filter_first is only supported for nonunique projection indexes")
		}
		for _, scope := range p.ScopeCols {
			found := false
			for _, col := range ix.Columns {
				if col.Col == scope {
					found = true
				}
			}
			if !found {
				return errf("unique projection indexes must include every scope column")
			}
		}
	}
	ix.PhysicalColumns = projectionIndexColumns(ix.Columns, ix.Layout)
	return nil
}

const projectionIndexSelect = `SELECT name,columns_json,unique_index,created_at,layout,physical_name FROM projection_indexes WHERE projection_id=? `

type storedProjectionIndex struct {
	index    TableIndex
	physical string
}

func scanProjectionIndex(row interface{ Scan(...any) error }, p *projectionDefinition) (storedProjectionIndex, error) {
	var stored storedProjectionIndex
	var raw string
	ix := &stored.index
	if err := row.Scan(&ix.Name, &raw, &ix.Unique, &ix.CreatedAt, &ix.Layout, &stored.physical); err != nil {
		return stored, err
	}
	if err := json.Unmarshal([]byte(raw), &ix.Columns); err != nil {
		return stored, err
	}
	if err := validateProjectionIndex(p, ix); err != nil {
		return stored, err
	}
	if stored.physical == "" {
		stored.physical = projectionIndexPhysical(p, ix.Name, ix.Unique)
	}
	return stored, nil
}
func createProjectionIndexTx(tx *writeTx, p *projectionDefinition, ix TableIndex) error {
	if err := validateProjectionIndex(p, &ix); err != nil {
		return err
	}
	physical := projectionIndexPhysical(p, ix.Name, ix.Unique)
	if _, err := tx.Exec(buildCreateIndexSQL(physical, projectionData(p), ix.PhysicalColumns, ix.Unique)); err != nil {
		return err
	}
	raw, _ := json.Marshal(ix.Columns)
	_, err := tx.Exec(`INSERT INTO projection_indexes(projection_id,name,columns_json,unique_index,layout,physical_name) VALUES(?,?,?,?,?,?)`, p.ID, ix.Name, string(raw), ix.Unique, ix.Layout, physical)
	return err
}
func inheritProjectionIndexesTx(tx *writeTx, p *projectionDefinition, app *sdk.AppCtx) error {
	var previousID int64
	if err := tx.QueryRow(`SELECT id FROM projection_definitions WHERE project_id=? AND name=? AND is_current=1 AND id<>?`, p.ProjectID, p.Name, p.ID).Scan(&previousID); err != nil {
		if err == sql.ErrNoRows {
			return errf("inherit_indexes requires a current projection version")
		}
		return err
	}
	// Validate against the previous schema while reading, then against the new
	// schema/scope constraints before creation. Copy definitions, never identities.
	previous, err := decodeProjection(app, tx.QueryRow(projectionSelect+`WHERE id=?`, previousID))
	if err != nil {
		return err
	}
	rows, err := tx.Query(projectionIndexSelect+`ORDER BY name`, previousID)
	if err != nil {
		return err
	}
	var indexes []TableIndex
	for rows.Next() {
		stored, err := scanProjectionIndex(rows, previous)
		if err != nil {
			rows.Close()
			return err
		}
		indexes = append(indexes, stored.index)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ix := range indexes {
		raw := make([]any, len(ix.Columns))
		for i, col := range ix.Columns {
			raw[i] = map[string]any{"col": col.Col, "order": col.Order}
		}
		columns, err := parseIndexColumns(projectionTable(p), raw)
		if err != nil {
			return fmt.Errorf("cannot inherit index %q: %w", ix.Name, err)
		}
		ix.Columns = columns
		if err := createProjectionIndexTx(tx, p, ix); err != nil {
			return fmt.Errorf("cannot inherit index %q: %w", ix.Name, err)
		}
	}
	return nil
}
func (a *App) projectionIndexTool(app *sdk.AppCtx, pid string, args map[string]any, operation string, original error) (any, error) {
	var e *statusError
	if !errors.As(original, &e) || e.status != 404 {
		return nil, original
	}
	p, err := projectionIndexDefinition(app, pid, args)
	if err != nil {
		return nil, err
	}
	if err := projectionIndexPermission(app, p, operation != "list"); err != nil {
		return nil, err
	}
	if operation == "list" {
		rows, err := metadataReaderFor(app).QueryContext(requestContext(app), projectionIndexSelect+`ORDER BY name`, p.ID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		indexes := []TableIndex{}
		for rows.Next() {
			stored, err := scanProjectionIndex(rows, p)
			if err != nil {
				return nil, err
			}
			indexes = append(indexes, stored.index)
		}
		return map[string]any{"indexes": indexes}, rows.Err()
	}
	name := strArg(args, "name")
	tx, err := beginWrite(app)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stored, lookupErr := scanProjectionIndex(tx.QueryRow(projectionIndexSelect+`AND name=?`, p.ID, name), p)
	if lookupErr != nil && lookupErr != sql.ErrNoRows {
		return nil, lookupErr
	}
	if operation == "drop" {
		if lookupErr != nil {
			return nil, lookupErr
		}
		if _, err := tx.Exec(`DROP INDEX ` + quote(stored.physical)); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM projection_indexes WHERE projection_id=? AND name=?`, p.ID, name); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		a.invalidateSQLCaches()
		return map[string]any{"dropped": name}, nil
	}
	replace := boolArg(args, "replace")
	if replace && lookupErr == sql.ErrNoRows {
		return nil, errf("cannot replace missing projection index %q", name)
	}
	if !replace && lookupErr == nil {
		return nil, errf("index %q already exists on projection %q", name, p.Name)
	}
	cols, err := parseIndexColumns(projectionTable(p), sliceArg(args, "columns"))
	if err != nil {
		return nil, err
	}
	layout := projectionIndexGenerationFirst
	if replace {
		layout = stored.index.Layout
	}
	if raw, ok := args["layout"]; ok {
		var valid bool
		layout, valid = raw.(string)
		if !valid {
			return nil, errf("layout must be a string")
		}
	}
	ix := TableIndex{Name: name, Columns: cols, Unique: boolArg(args, "unique"), Layout: layout}
	if err := validateProjectionIndex(p, &ix); err != nil {
		return nil, err
	}
	if replace && (stored.index.Unique || ix.Unique) {
		return nil, errf("replace is only supported for nonunique projection indexes")
	}
	if replace {
		token, err := projectionLeaseToken()
		if err != nil {
			return nil, err
		}
		physical := projectionIndexPhysical(p, name, false) + "_r" + token
		// SQLite builds an index under the writer lock. Retain the old index until
		// this build succeeds; timeout/cancellation rolls back the complete swap.
		if _, err := tx.Exec(buildCreateIndexSQL(physical, projectionData(p), ix.PhysicalColumns, false)); err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(cols)
		if _, err := tx.Exec(`UPDATE projection_indexes SET columns_json=?,layout=?,physical_name=? WHERE projection_id=? AND name=?`, string(raw), ix.Layout, physical, p.ID, name); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DROP INDEX ` + quote(stored.physical)); err != nil {
			return nil, err
		}
	} else {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM projection_indexes WHERE projection_id=?`, p.ID).Scan(&count); err != nil {
			return nil, err
		}
		if count >= 64 {
			return nil, errf("projection has the maximum of 64 indexes")
		}
		if err := createProjectionIndexTx(tx, p, ix); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.invalidateSQLCaches()
	return map[string]any{"index": ix}, nil
}
