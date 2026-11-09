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
	TableNames      []string                    `json:"table_names,omitempty"`
	PortfolioIDs    []int64                     `json:"portfolio_ids,omitempty"`
	RowCounts       map[string]int64            `json:"row_counts,omitempty"`
	NextRowID       int64                       `json:"next_row_id,omitempty"`
	Done            bool                        `json:"done,omitempty"`
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
		changes := []string{}
		for _, column := range columns {
			if value, ok := record[column]; ok {
				name := `"` + column + `"`
				names = append(names, name)
				slots = append(slots, "?")
				values = append(values, value)
				// Updating the parent key makes SQLite check referencing rows,
				// including the backtest bars, even when the ID stays the same.
				if table == "securities" && column == "id" {
					continue
				}
				updates = append(updates, name+"=excluded."+name)
				changes = append(changes, `"`+table+`".`+name+" IS NOT excluded."+name)
			}
		}
		if len(names) == 0 {
			return fmt.Errorf("empty research record in %s", table)
		}
		query := `INSERT INTO "` + table + `" (` + strings.Join(names, ",") + `) VALUES (` + strings.Join(slots, ",") + `) ON CONFLICT `
		if len(updates) == 0 {
			query += "DO NOTHING"
		} else {
			query += "DO UPDATE SET " + strings.Join(updates, ",") + " WHERE " + strings.Join(changes, " OR ")
		}
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
