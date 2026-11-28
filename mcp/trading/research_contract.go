package main

import (
	"database/sql"
)

// researchSnapshot is a versioned transfer format, not a shared database.
// Execution rows are read-only observations in Strategies. Trading remains
// authoritative and never reads Strategies' database.
type researchSnapshot struct {
	Version          int                         `json:"version"`
	SourceInstallID  int64                       `json:"source_install_id"`
	ProjectID        string                      `json:"project_id"`
	Tables           map[string][]map[string]any `json:"tables"`
	TableNames       []string                    `json:"table_names,omitempty"`
	PortfolioIDs     []int64                     `json:"portfolio_ids,omitempty"`
	RowCounts        map[string]int64            `json:"row_counts,omitempty"`
	NextRowID        int64                       `json:"next_row_id,omitempty"`
	Done             bool                        `json:"done,omitempty"`
	LegacySecurities []map[string]any            `json:"legacy_securities,omitempty"`
}

type researchQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func readResearchRows(db researchQueryer, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	err = scanResearchRows(rows, func(row map[string]any) error {
		out = append(out, row)
		return nil
	})
	return out, err
}

func scanResearchRows(rows *sql.Rows, visit func(map[string]any) error) error {
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	values := make([]any, len(columns))
	ptrs := make([]any, len(columns))
	for i := range values {
		ptrs[i] = &values[i]
	}
	for rows.Next() {
		if err = rows.Scan(ptrs...); err != nil {
			return err
		}
		row := map[string]any{}
		for i, key := range columns {
			value := values[i]
			if b, ok := value.([]byte); ok {
				value = string(b)
			}
			row[key] = value
		}
		if err := visit(row); err != nil {
			return err
		}
	}
	return rows.Err()
}
