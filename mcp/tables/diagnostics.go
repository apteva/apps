package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
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

// History pages are live views of retained observations, not database snapshots.
// Permissions are checked on every call; cursors only carry paging boundaries.
type diagnosticCursor struct {
	Version  int    `json:"v"`
	Project  string `json:"p"`
	Filter   string `json:"f"`
	Recorded int64  `json:"t"`
	ID       int64  `json:"id"`
}
type diagnosticHistoryQuery struct {
	Limit, Offset  int
	IncludeSummary bool
	Filter         string
	Where          string
	Params         []any
	Boundary       *diagnosticCursor
}

func diagnosticInteger(args map[string]any, key string, fallback int) (int, error) {
	raw, exists := args[key]
	if !exists {
		return fallback, nil
	}
	n, integerErr := exactInteger(raw)
	if integerErr != nil || n < 0 || n > 1<<31-1 {
		return 0, errf("%s must be a nonnegative integer", key)
	}
	return int(n), nil
}
func parseDiagnosticHistory(pid string, args map[string]any) (diagnosticHistoryQuery, error) {
	q := diagnosticHistoryQuery{Where: " WHERE project_id=?", Params: []any{pid}}
	var err error
	q.Limit, err = diagnosticInteger(args, "limit", 50)
	if err != nil {
		return q, err
	}
	if q.Limit < 1 {
		q.Limit = 1
	}
	if q.Limit > 100 {
		q.Limit = 100
	}
	q.Offset, err = diagnosticInteger(args, "offset", 0)
	if err != nil {
		return q, err
	}
	if v, exists := args["include_summary"]; exists {
		b, ok := v.(bool)
		if !ok {
			return q, errf("include_summary must be boolean")
		}
		q.IncludeSummary = b
	}
	filters := map[string]any{}
	for _, key := range []string{"outcome", "operation", "request_id", "query_id", "call_id"} {
		raw, exists := args[key]
		if !exists {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return q, errf("%s must be a string", key)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len(value) > 128 {
			return q, errf("%s exceeds 128 bytes", key)
		}
		if key == "outcome" && value != "ok" && value != "error" && value != "timeout" && value != "canceled" {
			return q, errf("outcome must be ok, error, timeout, or canceled")
		}
		filters[key] = value
		q.Where += " AND " + key + "=?"
		q.Params = append(q.Params, value)
	}
	var from, to int64
	for _, key := range []string{"from", "to"} {
		raw, exists := args[key]
		if !exists {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return q, errf("%s must be an RFC3339 timestamp", key)
		}
		if value == "" {
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || timestamp.UnixMilli() < 0 {
			return q, errf("%s must be an RFC3339 timestamp at or after the Unix epoch", key)
		}
		ms := timestamp.UnixMilli()
		filters[key] = ms
		op := ">="
		if key == "from" {
			from = ms
		} else {
			op = "<"
			to = ms
		}
		q.Where += " AND recorded_at_ms" + op + "?"
		q.Params = append(q.Params, ms)
	}
	if _, hasFrom := filters["from"]; hasFrom {
		if _, hasTo := filters["to"]; hasTo && from >= to {
			return q, errf("from must precede to")
		}
	}
	q.Filter = digest(filters)
	if raw, exists := args["cursor"]; exists {
		value, ok := raw.(string)
		if !ok || len(value) > 2048 {
			return q, errf("invalid diagnostics cursor")
		}
		if value != "" {
			if q.Offset != 0 {
				return q, errf("cursor and offset cannot be combined")
			}
			decoded, err := base64.RawURLEncoding.DecodeString(value)
			if err != nil {
				return q, errf("invalid diagnostics cursor")
			}
			var cursor diagnosticCursor
			decoder := json.NewDecoder(strings.NewReader(string(decoded)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&cursor); err != nil {
				return q, errf("invalid diagnostics cursor")
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return q, errf("invalid diagnostics cursor")
			}
			if cursor.Version != 1 || cursor.ID < 1 || cursor.Recorded < 0 || cursor.Project != pid || cursor.Filter != q.Filter {
				return q, errf("cursor does not match project, filters or version")
			}
			q.Boundary = &cursor
		}
	}
	return q, nil
}
func (q diagnosticHistoryQuery) pageSQL() (string, []any) {
	where := q.Where
	params := append([]any{}, q.Params...)
	if q.Boundary != nil {
		// A row-value range allows SQLite to seek directly into the compound index.
		where += " AND (recorded_at_ms,id)<(?,?)"
		params = append(params, q.Boundary.Recorded, q.Boundary.ID)
	}
	query := `SELECT id,recorded_at_ms,operation,call_id,request_id,query_id,outcome,stage,deadline_source,total_ms,sql_ms,read_queue_ms,select_ms,scan_ms,rows_returned,rows_materialized,truncated,error_type,sqlite_error_code FROM read_diagnostics` + where + ` ORDER BY recorded_at_ms DESC,id DESC LIMIT ?`
	params = append(params, q.Limit+1)
	if q.Offset > 0 {
		query += " OFFSET ?"
		params = append(params, q.Offset)
	}
	return query, params
}
func encodeDiagnosticCursor(pid, filter string, timestamp, id int64) string {
	body, _ := json.Marshal(diagnosticCursor{1, pid, filter, timestamp, id})
	return base64.RawURLEncoding.EncodeToString(body)
}
func (a *App) toolDiagnosticsList(app *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(app, args, "diagnostics_list", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	q, err := parseDiagnosticHistory(pid, args)
	if err != nil {
		return nil, err
	}
	return a.readDiagnosticHistory(ctx, pid, q)
}
func (a *App) readDiagnosticHistory(app *sdk.AppCtx, pid string, q diagnosticHistoryQuery) (any, error) {
	// Honor the same interactive admission and connection/deadline limits as reads.
	read, err := acquireReadConn(app, "<diagnostics>")
	if err != nil {
		return nil, err
	}
	defer read.close()
	ctx, cancel := queryTimeoutContext(app)
	defer cancel()
	if q.IncludeSummary && read.tx == nil {
		tx, err := read.conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		read.tx = tx
		defer tx.Rollback()
	}
	query, params := q.pageSQL()
	rows, err := read.queryer().QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]readDiagnostic, 0, q.Limit)
	var lastRecorded int64
	hasMore := false
	for rows.Next() {
		if len(items) == q.Limit {
			hasMore = true
			break
		}
		var item readDiagnostic
		var recorded int64
		var truncated int
		var code sql.NullInt64
		if err := rows.Scan(&item.ID, &recorded, &item.Operation, &item.CallID, &item.RequestID, &item.QueryID, &item.Outcome, &item.Stage, &item.DeadlineSource, &item.TotalMS, &item.SQLMS, &item.ReadQueueMS, &item.SelectMS, &item.ScanMS, &item.RowsReturned, &item.RowsMaterialized, &truncated, &item.ErrorType, &code); err != nil {
			return nil, err
		}
		item.RecordedAt = projectionTimestamp(recorded)
		lastRecorded = recorded
		item.Truncated = truncated != 0
		if code.Valid {
			value := code.Int64
			item.SQLiteErrorCode = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Page summaries cost O(limit), and never scan earlier/older history.
	var failures, slow int
	threshold := slowQueryMs(app)
	for _, item := range items {
		if item.Outcome != "ok" {
			failures++
		}
		if item.TotalMS >= int64(threshold) {
			slow++
		}
	}
	out := map[string]any{"diagnostics": items, "has_more": hasMore, "next_offset": q.Offset + len(items), "slow_query_ms": threshold, "page_summary": map[string]any{"recorded": len(items), "failures": failures, "slow": slow}, "summary_included": q.IncludeSummary}
	if hasMore && len(items) > 0 {
		out["next_cursor"] = encodeDiagnosticCursor(pid, q.Filter, lastRecorded, items[len(items)-1].ID)
	}
	if q.IncludeSummary {
		var total, errorsCount, slowCount int64
		params := append([]any{threshold}, q.Params...)
		if err := read.queryer().QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(outcome IN ('error','timeout','canceled')),0),COALESCE(SUM(total_ms>=?),0) FROM read_diagnostics`+q.Where, params...).Scan(&total, &errorsCount, &slowCount); err != nil {
			return nil, err
		}
		out["total"] = total
		out["error_count"] = errorsCount
		out["slow_count"] = slowCount
		out["summary_scope"] = "filtered_history"
	}
	return out, nil
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
	args := injectProject(r, map[string]any{})
	// X-Request-ID identifies this API call; request_id in the URL filters history.
	delete(args, "request_id")
	for _, key := range []string{"outcome", "operation", "request_id", "query_id", "call_id", "from", "to", "cursor"} {
		if r.URL.Query().Has(key) {
			args[key] = r.URL.Query().Get(key)
		}
	}
	for _, key := range []string{"limit", "offset"} {
		if r.URL.Query().Has(key) {
			n, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 32)
			if err != nil {
				writeToolResult(w, nil, errf("%s must be a nonnegative integer", key))
				return
			}
			args[key] = n
		}
	}
	if r.URL.Query().Has("include_summary") {
		value, err := strconv.ParseBool(r.URL.Query().Get("include_summary"))
		if err != nil {
			writeToolResult(w, nil, errf("include_summary must be boolean"))
			return
		}
		args["include_summary"] = value
	}
	out, err := a.toolDiagnosticsList(requestAppCtx(r), args)
	writeToolResult(w, out, err)
}

// A single redacted record is useful when correlating a Function invocation.
func (a *App) toolDiagnosticsGet(app *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, finish, err := a.beginOperation(app, args, "diagnostics_get", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, integerErr := exactInteger(args["id"])
	if integerErr != nil || id < 1 {
		return nil, errf("id must be a positive integer")
	}
	q := diagnosticHistoryQuery{Limit: 1, Where: " WHERE project_id=? AND id=?", Params: []any{pid, id}}
	out, err := a.readDiagnosticHistory(ctx, pid, q)
	if err != nil {
		return nil, err
	}
	items := out.(map[string]any)["diagnostics"].([]readDiagnostic)
	if len(items) == 0 {
		return nil, &statusError{404, "diagnostic not found"}
	}
	return map[string]any{"diagnostic": items[0]}, nil
}
func (a *App) handleDiagnosticItem(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "app not yet mounted")
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/diagnostics/"), 10, 64)
	if err != nil {
		writeToolResult(w, nil, errf("id must be a positive integer"))
		return
	}
	out, err := a.toolDiagnosticsGet(requestAppCtx(r), injectProject(r, map[string]any{"id": id}))
	writeToolResult(w, out, err)
}
func (a *App) diagnosticTools() []sdk.Tool {
	properties := map[string]any{
		"_project_id":     map[string]any{"type": "string", "description": "Project scope for a global install."},
		"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50},
		"offset":          map[string]any{"type": "integer", "minimum": 0, "description": "Legacy offset pagination; prefer next_cursor."},
		"cursor":          map[string]any{"type": "string", "description": "next_cursor from the preceding page; keep the same filters."},
		"outcome":         map[string]any{"type": "string", "enum": []any{"ok", "error", "timeout", "canceled"}},
		"include_summary": map[string]any{"type": "boolean", "default": false, "description": "Opt in to counts across all matching retained history; incurs a scan."},
	}
	for _, key := range []string{"operation", "request_id", "query_id", "call_id", "from", "to"} {
		properties[key] = map[string]any{"type": "string"}
	}
	properties["from"].(map[string]any)["description"] = "Inclusive RFC3339 timestamp."
	properties["to"].(map[string]any)["description"] = "Exclusive RFC3339 timestamp."
	return []sdk.Tool{
		{Name: "diagnostics_list", Description: "Read redacted Tables timing history directly, without Functions. Indexed cursor pagination and request/query/operation/outcome/time filters; page summaries by default, full-history totals opt-in. Wall time, not CPU or physical I/O.", InputSchema: schemaObject(properties, nil), Handler: a.toolDiagnosticsList},
		{Name: "diagnostics_get", Description: "Inspect one redacted diagnostic record in the current project by id.", InputSchema: schemaObject(map[string]any{"id": positiveIDSchema(), "_project_id": map[string]any{"type": "string"}}, []string{"id"}), Handler: a.toolDiagnosticsGet},
	}
}
