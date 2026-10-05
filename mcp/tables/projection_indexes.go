package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

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
		rows, err := metadataReaderFor(app).QueryContext(requestContext(app), `SELECT name,columns_json,unique_index,created_at FROM projection_indexes WHERE projection_id=? ORDER BY name`, p.ID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		indexes := []TableIndex{}
		for rows.Next() {
			var ix TableIndex
			var raw string
			if err := rows.Scan(&ix.Name, &raw, &ix.Unique, &ix.CreatedAt); err != nil {
				return nil, err
			}
			if err := json.Unmarshal([]byte(raw), &ix.Columns); err != nil {
				return nil, err
			}
			indexes = append(indexes, ix)
		}
		return map[string]any{"indexes": indexes}, rows.Err()
	}
	name := strArg(args, "name")
	tx, err := beginWrite(app)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if operation == "drop" {
		var unique bool
		if err := tx.QueryRow(`SELECT unique_index FROM projection_indexes WHERE projection_id=? AND name=?`, p.ID, name).Scan(&unique); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DROP INDEX ` + quote(projectionIndexPhysical(p, name, unique))); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM projection_indexes WHERE projection_id=? AND name=?`, p.ID, name); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		a.plans.invalidateTable(-p.ID)
		return map[string]any{"dropped": name}, nil
	}
	cols, err := parseIndexColumns(projectionTable(p), sliceArg(args, "columns"))
	if err != nil {
		return nil, err
	}
	unique := boolArg(args, "unique")
	if unique {
		for _, scope := range p.ScopeCols {
			found := false
			for _, c := range cols {
				if c.Col == scope {
					found = true
				}
			}
			if !found {
				return nil, errf("unique projection indexes must include every scope column")
			}
		}
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM projection_indexes WHERE projection_id=?`, p.ID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= 64 {
		return nil, errf("projection has the maximum of 64 indexes")
	}
	raw, _ := json.Marshal(cols)
	if _, err := tx.Exec(`INSERT INTO projection_indexes(projection_id,name,columns_json,unique_index) VALUES(?,?,?,?)`, p.ID, name, string(raw), unique); err != nil {
		return nil, err
	}
	physicalCols := append([]IndexColumn{}, cols...)
	if unique {
		physicalCols = append([]IndexColumn{{Col: "_projection_generation", Order: "asc"}}, physicalCols...)
	}
	if _, err := tx.Exec(buildCreateIndexSQL(projectionIndexPhysical(p, name, unique), projectionData(p), physicalCols, unique)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.plans.invalidateTable(-p.ID)
	return map[string]any{"index": TableIndex{Name: name, Columns: cols, Unique: unique}}, nil
}
