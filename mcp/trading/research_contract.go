package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// researchSnapshot is a versioned transfer format, not a shared database.
// Execution rows are read-only observations in Strategies. Trading remains
// authoritative and never reads Strategies' database.
type researchSnapshot struct {
	Version         int                         `json:"version"`
	SourceInstallID int64                       `json:"source_install_id"`
	ProjectID       string                      `json:"project_id"`
	Tables          map[string][]map[string]any `json:"tables"`
}

type researchQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func readResearchRows(db researchQueryer, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err = rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, key := range columns {
			value := values[i]
			if b, ok := value.([]byte); ok {
				value = string(b)
			}
			row[key] = value
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func writeResearchRows(tx *sql.Tx, table string, records []map[string]any) error {
	rows, err := tx.Query(`PRAGMA table_info("` + table + `")`)
	if err != nil {
		return err
	}
	columns := []string{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		columns = append(columns, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return fmt.Errorf("unknown research table %s", table)
	}
	for _, record := range records {
		names := []string{}
		slots := []string{}
		values := []any{}
		updates := []string{}
		for _, column := range columns {
			if value, ok := record[column]; ok {
				names = append(names, `"`+column+`"`)
				slots = append(slots, "?")
				values = append(values, value)
				updates = append(updates, `"`+column+`"=excluded."`+column+`"`)
			}
		}
		if len(names) == 0 {
			return fmt.Errorf("empty research record in %s", table)
		}
		query := `INSERT INTO "` + table + `" (` + strings.Join(names, ",") + `) VALUES (` + strings.Join(slots, ",") + `) ON CONFLICT DO UPDATE SET ` + strings.Join(updates, ",")
		if _, err = tx.Exec(query, values...); err != nil {
			return fmt.Errorf("import %s: %w", table, err)
		}
	}
	return nil
}

func decodeResearchValue(value any, out any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
