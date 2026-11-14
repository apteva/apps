package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListLimit     = 50
	maxListLimit         = 200
	maxListResponseBytes = 512 << 10
	maxSummaryNameBytes  = 16 << 10
	maxSummaryIDBytes    = 512
	maxListCursorBytes   = 1024
	// Reserve space for pagination metadata, its escaped representation and the
	// MCP envelope. Account for both direct JSON and JSON-as-text below.
	listEnvelopeReserve = 8192
)

type ListOptions struct {
	Limit  *int    `json:"limit,omitempty"`
	Cursor *string `json:"cursor,omitempty"`
}

type EnvironmentSummary struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	DesiredState    string      `json:"desired_state"`
	ReconcileStatus string      `json:"reconcile_status"`
	SpecVersion     int         `json:"spec_version"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	ActiveRun       *RunSummary `json:"active_run"`
}

type RunSummary struct {
	ID        string     `json:"id"`
	RuntimeID string     `json:"runtime_id"`
	Status    string     `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
}

type EnvironmentPage struct {
	Items      []EnvironmentSummary `json:"items"`
	NextCursor *string              `json:"next_cursor"`
	HasMore    bool                 `json:"has_more"`
}

type listCursor struct {
	Version int    `json:"v"`
	Created string `json:"created"`
	ID      string `json:"id"`
}

func (o ListOptions) validate() (int, *listCursor, error) {
	limit := defaultListLimit
	if o.Limit != nil {
		limit = *o.Limit
	}
	if limit < 1 || limit > maxListLimit {
		return 0, nil, fmt.Errorf("limit must be between 1 and %d", maxListLimit)
	}
	if o.Cursor == nil {
		return limit, nil, nil
	}
	if len(*o.Cursor) == 0 || len(*o.Cursor) > maxListCursorBytes {
		return 0, nil, errors.New("invalid cursor")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(*o.Cursor)
	if err != nil {
		return 0, nil, errors.New("invalid cursor")
	}
	var cursor listCursor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return 0, nil, errors.New("invalid cursor")
	}
	canonical, _ := json.Marshal(cursor)
	if !bytes.Equal(raw, canonical) || cursor.Version != 1 || !validID(cursor.ID) || len(cursor.ID) > maxSummaryIDBytes {
		return 0, nil, errors.New("invalid cursor")
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.Created); err != nil || len(cursor.Created) > 64 {
		return 0, nil, errors.New("invalid cursor")
	}
	return limit, &cursor, nil
}

// CASE bounds text inside SQLite, before the driver allocates Go strings.
// Oversized legacy values cause an explicit error, never silent truncation.
func boundedListText(column string, maxBytes int) string {
	return fmt.Sprintf("CASE WHEN length(CAST(%s AS BLOB)) <= %d THEN %s ELSE NULL END", column, maxBytes, column)
}

func (s store) listDefinitionSummaries(options ListOptions) (*EnvironmentPage, error) {
	limit, cursor, err := options.validate()
	if err != nil {
		return nil, err
	}
	columns := []string{}
	for _, field := range []struct {
		name string
		max  int
	}{
		{"d.id", maxSummaryIDBytes}, {"d.name", maxSummaryNameBytes}, {"d.desired_state", 32}, {"d.reconcile_status", 32},
		{"d.created_at", 64}, {"d.updated_at", 64}, {"r.id", maxSummaryIDBytes}, {"r.runtime_id", maxSummaryIDBytes},
		{"r.status", 32}, {"r.started_at", 64}, {"r.stopped_at", 64},
	} {
		columns = append(columns, boundedListText(field.name, field.max))
	}
	// The partial unique index guarantees at most one active run per definition.
	// No spec, fixture, error or runtime-inventory payload is selected here.
	query := "SELECT " + strings.Join(columns, ",") + ",d.spec_version,r.environment_id IS NOT NULL FROM environment_definitions d LEFT JOIN environment_runs r ON r.environment_id=d.id AND r.environment_id<>'' AND r.status IN ('starting','running','stopping')"
	args := []any{}
	if cursor != nil {
		query += " WHERE (d.created_at,d.id) < (?,?)"
		args = append(args, cursor.Created, cursor.ID)
	}
	query += " ORDER BY d.created_at DESC,d.id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &EnvironmentPage{Items: []EnvironmentSummary{}}
	used := listEnvelopeReserve
	var last listCursor
	for rows.Next() {
		if len(page.Items) == limit {
			page.HasMore = true
			break
		}
		var values [11]sql.NullString
		var version int
		var hasRun bool
		if err := rows.Scan(&values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &values[6], &values[7], &values[8], &values[9], &values[10], &version, &hasRun); err != nil {
			return nil, err
		}
		for i, value := range values {
			if (i < 6 || (hasRun && i < 10)) && !value.Valid {
				return nil, errors.New("environment summary field exceeds list size limit; retrieve and update the definition by ID")
			}
		}
		created, err := time.Parse(time.RFC3339Nano, values[4].String)
		if err != nil {
			return nil, errors.New("invalid summary creation timestamp")
		}
		updated, err := time.Parse(time.RFC3339Nano, values[5].String)
		if err != nil {
			return nil, errors.New("invalid summary update timestamp")
		}
		item := EnvironmentSummary{ID: values[0].String, Name: values[1].String, DesiredState: values[2].String, ReconcileStatus: values[3].String, CreatedAt: created, UpdatedAt: updated, SpecVersion: version}
		if hasRun {
			started, err := time.Parse(time.RFC3339Nano, values[9].String)
			if err != nil {
				return nil, errors.New("invalid run start timestamp")
			}
			item.ActiveRun = &RunSummary{ID: values[6].String, RuntimeID: values[7].String, Status: values[8].String, StartedAt: started, StoppedAt: parseOptionalTime(values[10])}
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		escaped, _ := json.Marshal(string(raw))
		size := len(raw) + len(escaped) + 4
		if used+size > maxListResponseBytes {
			if len(page.Items) == 0 {
				return nil, errors.New("one environment summary exceeds list response budget")
			}
			page.HasMore = true
			break
		}
		used += size
		page.Items = append(page.Items, item)
		last = listCursor{Version: 1, Created: values[4].String, ID: item.ID}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if page.HasMore {
		raw, _ := json.Marshal(last)
		encoded := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &encoded
	}
	return page, nil
}

func listOptionsFromHTTP(r *http.Request) (ListOptions, error) {
	options := ListOptions{}
	query := r.URL.Query()
	if values, exists := query["limit"]; exists {
		if len(values) != 1 {
			return options, errors.New("invalid limit")
		}
		limit, err := strconv.Atoi(values[0])
		if err != nil {
			return options, errors.New("invalid limit")
		}
		options.Limit = &limit
	}
	if values, exists := query["cursor"]; exists {
		if len(values) != 1 {
			return options, errors.New("invalid cursor")
		}
		options.Cursor = &values[0]
	}
	_, _, err := options.validate()
	return options, err
}
