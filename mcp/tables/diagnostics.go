package main

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
)

type readDiagnostic struct {
	ID               int64  `json:"id"`
	RecordedAt       string `json:"recorded_at"`
	Operation        string `json:"operation"`
	CallID           string `json:"call_id"`
	RequestID        string `json:"request_id,omitempty"`
	QueryID          string `json:"query_id"`
	Outcome          string `json:"outcome"`
	Stage            string `json:"stage,omitempty"`
	DeadlineSource   string `json:"deadline_source,omitempty"`
	TotalMS          int64  `json:"total_ms"`
	SQLMS            int64  `json:"sql_ms"`
	ReadQueueMS      int64  `json:"read_queue_ms"`
	SelectMS         int64  `json:"select_ms"`
	ScanMS           int64  `json:"scan_ms"`
	RowsReturned     int64  `json:"rows_returned"`
	RowsMaterialized int64  `json:"rows_materialized"`
	Truncated        bool   `json:"truncated"`
	ErrorType        string `json:"error_type,omitempty"`
	SQLiteErrorCode  *int64 `json:"sqlite_error_code,omitempty"`
}

func (a *App) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "app not yet mounted")
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	args := injectProject(r, map[string]any{"name": "read_diagnostics"})
	ctx, finish, err := a.beginOperation(requestAppCtx(r), args, "diagnostics_list", false)
	if err != nil {
		writeToolResult(w, nil, err)
		return
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		writeToolResult(w, nil, err)
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, parseErr := strconv.Atoi(raw); parseErr == nil {
			limit = n
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		if n, parseErr := strconv.Atoi(raw); parseErr == nil && n >= 0 {
			offset = n
		}
	}
	outcome := strings.TrimSpace(r.URL.Query().Get("outcome"))
	where := ` WHERE project_id=?`
	queryArgs := []any{pid}
	if outcome != "" {
		if outcome != "ok" && outcome != "error" && outcome != "timeout" && outcome != "canceled" {
			writeToolResult(w, nil, errf("outcome must be ok, error, timeout, or canceled"))
			return
		}
		where += ` AND outcome=?`
		queryArgs = append(queryArgs, outcome)
	}
	reader := metadataReaderFor(ctx)
	var total, errorsCount, slowCount int64
	summaryArgs := append([]any{slowQueryMs(ctx)}, queryArgs...)
	if err := reader.QueryRowContext(requestContext(ctx), `SELECT COUNT(*),COALESCE(SUM(outcome IN ('error','timeout','canceled')),0),COALESCE(SUM(total_ms>=?),0) FROM read_diagnostics`+where, summaryArgs...).Scan(&total, &errorsCount, &slowCount); err != nil {
		writeToolResult(w, nil, err)
		return
	}
	rows, err := reader.QueryContext(requestContext(ctx), `SELECT id,recorded_at_ms,operation,call_id,request_id,query_id,outcome,stage,deadline_source,total_ms,sql_ms,read_queue_ms,select_ms,scan_ms,rows_returned,rows_materialized,truncated,error_type,sqlite_error_code FROM read_diagnostics`+where+` ORDER BY recorded_at_ms DESC,id DESC LIMIT ? OFFSET ?`, append(queryArgs, limit+1, offset)...)
	if err != nil {
		writeToolResult(w, nil, err)
		return
	}
	defer rows.Close()
	items := make([]readDiagnostic, 0, limit)
	for rows.Next() {
		var item readDiagnostic
		var recorded int64
		var truncated int
		var sqliteCode sql.NullInt64
		if err := rows.Scan(&item.ID, &recorded, &item.Operation, &item.CallID, &item.RequestID, &item.QueryID, &item.Outcome, &item.Stage, &item.DeadlineSource, &item.TotalMS, &item.SQLMS, &item.ReadQueueMS, &item.SelectMS, &item.ScanMS, &item.RowsReturned, &item.RowsMaterialized, &truncated, &item.ErrorType, &sqliteCode); err != nil {
			writeToolResult(w, nil, err)
			return
		}
		item.RecordedAt = projectionTimestamp(recorded)
		item.Truncated = truncated != 0
		if sqliteCode.Valid {
			value := sqliteCode.Int64
			item.SQLiteErrorCode = &value
		}
		items = append(items, item)
		if len(items) == limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		writeToolResult(w, nil, err)
		return
	}
	writeToolResult(w, map[string]any{
		"diagnostics": items,
		"total":       total,
		"error_count": errorsCount,
		"slow_count":  slowCount,
		"has_more":    offset+len(items) < int(total),
		"next_offset": offset + len(items),
	}, nil)
}
