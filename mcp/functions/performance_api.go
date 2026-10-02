package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Reports read retained invocation metadata only. Never load payloads, logs,
// principals or environment values to rank functions or calls.
type performanceFilter struct {
	Since, Until time.Time
	FunctionID   int64
	Sort         string
	Limit        int
	MinMS        int64
	Status       string
	Cursor       *slowCallCursor
}

type functionPerformance struct {
	FunctionID       int64    `json:"function_id"`
	FunctionName     string   `json:"function_name"`
	Runtime          string   `json:"runtime"`
	Calls            int64    `json:"calls"`
	Completed        int64    `json:"completed"`
	Running          int64    `json:"running"`
	Errors           int64    `json:"errors"`
	Canceled         int64    `json:"canceled"`
	ErrorRate        float64  `json:"error_rate"`
	CallsPerMinute   float64  `json:"calls_per_minute"`
	AvgDurationMS    *float64 `json:"avg_duration_ms"`
	P95DurationMS    *float64 `json:"p95_duration_ms"`
	MaxDurationMS    *float64 `json:"max_duration_ms"`
	AvgExecutionMS   *float64 `json:"avg_execution_ms"`
	P95ExecutionMS   *float64 `json:"p95_execution_ms"`
	MaxExecutionMS   *float64 `json:"max_execution_ms"`
	AvgQueueMS       *float64 `json:"avg_queue_ms"`
	TotalExecutionMS int64    `json:"total_execution_ms"`
}

type performanceTotals struct {
	Calls            int64 `json:"calls"`
	Completed        int64 `json:"completed"`
	Running          int64 `json:"running"`
	Errors           int64 `json:"errors"`
	Canceled         int64 `json:"canceled"`
	TotalExecutionMS int64 `json:"total_execution_ms"`
}

type performanceReport struct {
	Since         string                 `json:"since"`
	Until         string                 `json:"until"`
	Sort          string                 `json:"sort"`
	Functions     []*functionPerformance `json:"functions"`
	FunctionCount int                    `json:"function_count"`
	HasMore       bool                   `json:"has_more"`
	Totals        performanceTotals      `json:"totals"`
	TimingNotes   string                 `json:"timing_notes"`
}

const performanceTimingNotes = "Retained invocations started in [since, until); exact nearest-rank p95 over completed calls, including failures. Running calls have no finalized latency. Execution includes downstream calls and is wall time, not CPU. Errors exclude caller cancellations. Historical timing breakdowns may be zero when not recorded."

var performanceSorts = []string{"total_execution_ms", "calls", "p95_execution_ms", "p95_duration_ms", "avg_execution_ms", "avg_duration_ms", "avg_queue_ms", "errors", "error_rate"}
var slowCallSorts = []string{"duration_ms", "execution_ms", "queue_ms"}

func containsSort(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func performanceNumber(args map[string]any, key string, fallback, min, max int64) (int64, error) {
	v, ok := args[key]
	if !ok || v == nil || v == "" {
		return fallback, nil
	}
	// MCP JSON decoders commonly use float64, including exponent notation for
	// large IDs. Accept integral JSON numbers without truncating fractions.
	if number, ok := v.(json.Number); ok {
		parsed, err := number.Float64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer between %d and %d", key, min, max)
		}
		v = parsed
	}
	if number, ok := v.(float64); ok {
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < float64(min) || number > float64(max) {
			return 0, fmt.Errorf("%s must be an integer between %d and %d", key, min, max)
		}
		return int64(number), nil
	}
	n, err := strconv.ParseInt(fmt.Sprint(v), 10, 64)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, min, max)
	}
	return n, nil
}

func parsePerformanceFilter(args map[string]any, now time.Time, slow bool) (performanceFilter, error) {
	f := performanceFilter{Until: now.UTC()}
	window := strArg(args, "window")
	if window == "" {
		window = "24h"
	}
	durations := map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}
	d, ok := durations[window]
	if !ok {
		return f, errors.New("window must be 1h, 6h, 24h, 7d or 30d")
	}
	f.Since = f.Until.Add(-d)
	if raw := strArg(args, "until"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return f, errors.New("until must be an RFC3339 timestamp")
		}
		f.Until = t.UTC()
		f.Since = f.Until.Add(-d)
	}
	if raw := strArg(args, "since"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return f, errors.New("since must be an RFC3339 timestamp")
		}
		f.Since = t.UTC()
	}
	if !f.Since.Before(f.Until) || f.Until.Sub(f.Since) > 30*24*time.Hour {
		return f, errors.New("time range must be positive and at most 30 days")
	}
	sorts := performanceSorts
	f.Sort = "total_execution_ms"
	if slow {
		sorts, f.Sort = slowCallSorts, "duration_ms"
	}
	if raw := strArg(args, "sort"); raw != "" {
		f.Sort = raw
	}
	if !containsSort(sorts, f.Sort) {
		return f, fmt.Errorf("sort must be one of %v", sorts)
	}
	n, err := performanceNumber(args, "limit", 50, 1, 200)
	if err != nil {
		return f, err
	}
	f.Limit = int(n)
	if slow {
		f.MinMS, err = performanceNumber(args, "min_ms", 0, 0, 1<<53-1)
		if err != nil {
			return f, err
		}
		f.Status = strArg(args, "status")
		if f.Status != "" && !containsSort([]string{"ok", "error", "timeout", "upstream_timeout", "canceled"}, f.Status) {
			return f, errors.New("status must be ok, error, timeout, upstream_timeout or canceled")
		}
	}
	return f, nil
}

func performanceFunctionID(ctx *sdk.AppCtx, pid string, args map[string]any) (int64, error) {
	id, err := performanceNumber(args, "id", 0, 0, 1<<53-1)
	if err != nil {
		return 0, err
	}
	name := strArg(args, "name")
	if id == 0 && name == "" {
		return 0, nil
	}
	fn, err := dbGetFunction(ctx.AppDB(), pid, id, name)
	if err != nil {
		return 0, err
	}
	if fn == nil || name != "" && fn.Name != name {
		return 0, errors.New("function not found")
	}
	return fn.ID, nil
}

// Use the existing (project_id, started_at) index to restrict candidate days,
// then compare actual timestamps. This also handles legacy SQLite timestamps
// and RFC3339Nano strings with different fractional-second precision.
const performanceWhere = `i.project_id=? AND i.started_at>=? AND i.started_at<?
	AND julianday(i.started_at)>=julianday(?) AND julianday(i.started_at)<julianday(?)
	AND (?=0 OR i.function_id=?)`

func (f performanceFilter) queryArgs(pid string) []any {
	return []any{pid, f.Since.Format("2006-01-02"), f.Until.AddDate(0, 0, 1).Format("2006-01-02"), f.Since.Format(time.RFC3339Nano), f.Until.Format(time.RFC3339Nano), f.FunctionID, f.FunctionID}
}

func dbFunctionPerformance(parent context.Context, db *sql.DB, pid string, f performanceFilter) (*performanceReport, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `WITH selected AS (
		SELECT i.function_id, i.status, i.duration_ms, i.execution_ms, i.queue_ms,
			(i.status!='running' AND COALESCE(i.finished_at,'')!='') AS complete
		FROM function_invocations i WHERE `+performanceWhere+`
	), ranked AS (
		SELECT function_id, duration_ms, execution_ms,
			ROW_NUMBER() OVER (PARTITION BY function_id ORDER BY duration_ms) AS dr,
			ROW_NUMBER() OVER (PARTITION BY function_id ORDER BY execution_ms) AS er,
			COUNT(*) OVER (PARTITION BY function_id) AS n
		FROM selected WHERE complete
	), percentiles AS (
		SELECT function_id,
			MAX(CASE WHEN dr=(95*n+99)/100 THEN duration_ms END) AS p95_duration,
			MAX(CASE WHEN er=(95*n+99)/100 THEN execution_ms END) AS p95_execution
		FROM ranked GROUP BY function_id
	), grouped AS (
		SELECT function_id, COUNT(*) AS calls, SUM(complete) AS completed,
			SUM(status='running') AS running,
			SUM(complete AND status NOT IN ('ok','canceled')) AS errors,
			SUM(complete AND status='canceled') AS canceled,
			AVG(CASE WHEN complete THEN duration_ms END) AS avg_duration,
			MAX(CASE WHEN complete THEN duration_ms END) AS max_duration,
			AVG(CASE WHEN complete THEN execution_ms END) AS avg_execution,
			MAX(CASE WHEN complete THEN execution_ms END) AS max_execution,
			AVG(CASE WHEN complete THEN queue_ms END) AS avg_queue,
			COALESCE(SUM(CASE WHEN complete THEN execution_ms END),0) AS total_execution
		FROM selected GROUP BY function_id
	)
	SELECT g.function_id, f.name, f.runtime, g.calls, g.completed, g.running, g.errors, g.canceled,
		g.avg_duration, p.p95_duration, g.max_duration, g.avg_execution, p.p95_execution,
		g.max_execution, g.avg_queue, g.total_execution
	FROM grouped g JOIN functions f ON f.id=g.function_id AND f.project_id=?
	LEFT JOIN percentiles p ON p.function_id=g.function_id`, append(f.queryArgs(pid), pid)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	report := &performanceReport{Since: f.Since.Format(time.RFC3339Nano), Until: f.Until.Format(time.RFC3339Nano), Sort: f.Sort, Functions: []*functionPerformance{}, TimingNotes: performanceTimingNotes}
	for rows.Next() {
		g := &functionPerformance{}
		if err := rows.Scan(&g.FunctionID, &g.FunctionName, &g.Runtime, &g.Calls, &g.Completed, &g.Running, &g.Errors, &g.Canceled, &g.AvgDurationMS, &g.P95DurationMS, &g.MaxDurationMS, &g.AvgExecutionMS, &g.P95ExecutionMS, &g.MaxExecutionMS, &g.AvgQueueMS, &g.TotalExecutionMS); err != nil {
			return nil, err
		}
		g.CallsPerMinute = float64(g.Calls) / f.Until.Sub(f.Since).Minutes()
		if g.Completed > 0 {
			g.ErrorRate = float64(g.Errors) / float64(g.Completed)
		}
		report.Functions = append(report.Functions, g)
		report.Totals.Calls += g.Calls
		report.Totals.Completed += g.Completed
		report.Totals.Running += g.Running
		report.Totals.Errors += g.Errors
		report.Totals.Canceled += g.Canceled
		report.Totals.TotalExecutionMS += g.TotalExecutionMS
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(report.Functions, func(i, j int) bool {
		a, b := report.Functions[i], report.Functions[j]
		av, bv := performanceSortValue(a, f.Sort), performanceSortValue(b, f.Sort)
		if av == bv {
			return a.FunctionName < b.FunctionName
		}
		return av > bv
	})
	report.FunctionCount = len(report.Functions)
	report.HasMore = len(report.Functions) > f.Limit
	if report.HasMore {
		report.Functions = report.Functions[:f.Limit]
	}
	return report, nil
}

func performanceSortValue(g *functionPerformance, key string) float64 {
	var value *float64
	switch key {
	case "calls":
		return float64(g.Calls)
	case "errors":
		return float64(g.Errors)
	case "error_rate":
		return g.ErrorRate
	case "p95_execution_ms":
		value = g.P95ExecutionMS
	case "p95_duration_ms":
		value = g.P95DurationMS
	case "avg_execution_ms":
		value = g.AvgExecutionMS
	case "avg_duration_ms":
		value = g.AvgDurationMS
	case "avg_queue_ms":
		value = g.AvgQueueMS
	default:
		return float64(g.TotalExecutionMS)
	}
	if value == nil {
		return -1 // No completed measurements sorts after measured zero.
	}
	return *value
}

type slowInvocation struct {
	ID           int64  `json:"id"`
	FunctionID   int64  `json:"function_id"`
	FunctionName string `json:"function_name"`
	VersionID    *int64 `json:"version_id,omitempty"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
	Status       string `json:"status"`
	TriggerKind  string `json:"trigger_kind"`
	DurationMS   int64  `json:"duration_ms"`
	ExecutionMS  int64  `json:"execution_ms"`
	QueueMS      int64  `json:"queue_ms"`
	BuildMS      int64  `json:"build_ms"`
	ColdStartMS  int64  `json:"cold_start_ms"`
}

type slowInvocationsPage struct {
	Since       string            `json:"since"`
	Until       string            `json:"until"`
	Sort        string            `json:"sort"`
	Invocations []*slowInvocation `json:"invocations"`
	NextCursor  string            `json:"next_cursor"`
}

// Cursor fixes the time window and binds the filters, so equal timings are
// paginated by ID without repeats and other projects cannot reuse a cursor.
type slowCallCursor struct {
	Since string `json:"since"`
	Until string `json:"until"`
	Scope string `json:"scope"`
	Value int64  `json:"value"`
	ID    int64  `json:"id"`
}

func slowCursorScope(pid string, f performanceFilter) string {
	return hashSource([]byte(fmt.Sprintf("%s\x00%d\x00%s\x00%d\x00%s", pid, f.FunctionID, f.Sort, f.MinMS, f.Status)))
}

func slowFilter(ctx *sdk.AppCtx, pid string, args map[string]any) (performanceFilter, error) {
	copyArgs := make(map[string]any, len(args))
	for k, v := range args {
		copyArgs[k] = v
	}
	var cursor *slowCallCursor
	if raw := strArg(args, "cursor"); raw != "" {
		if len(raw) > 2048 {
			return performanceFilter{}, errors.New("invalid cursor")
		}
		bytes, err := base64.RawURLEncoding.DecodeString(raw)
		cursor = &slowCallCursor{}
		if err != nil || json.Unmarshal(bytes, cursor) != nil || cursor.ID <= 0 || cursor.Value < 0 || cursor.Since == "" || cursor.Until == "" {
			return performanceFilter{}, errors.New("invalid cursor")
		}
		for key, value := range map[string]string{"since": cursor.Since, "until": cursor.Until} {
			if requested := strArg(args, key); requested != "" && requested != value {
				return performanceFilter{}, errors.New("cursor time range differs from requested time range")
			}
			copyArgs[key] = value
		}
	}
	f, err := parsePerformanceFilter(copyArgs, time.Now(), true)
	if err != nil {
		return f, err
	}
	f.FunctionID, err = performanceFunctionID(ctx, pid, args)
	if err != nil {
		return f, err
	}
	if cursor != nil && cursor.Scope != slowCursorScope(pid, f) {
		return f, errors.New("cursor filters differ from requested filters")
	}
	f.Cursor = cursor
	return f, nil
}

func dbSlowInvocations(parent context.Context, db *sql.DB, pid string, f performanceFilter) (*slowInvocationsPage, error) {
	if !containsSort(slowCallSorts, f.Sort) {
		return nil, errors.New("invalid slow-call sort")
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	args := f.queryArgs(pid)
	query := `SELECT i.id,i.function_id,f.name,i.version_id,i.started_at,i.finished_at,i.status,i.trigger_kind,
		COALESCE(i.duration_ms,0),i.execution_ms,i.queue_ms,i.build_ms,i.cold_start_ms
		FROM function_invocations i JOIN functions f ON f.id=i.function_id AND f.project_id=i.project_id
		WHERE ` + performanceWhere + ` AND i.status!='running' AND COALESCE(i.finished_at,'')!=''
		AND i.` + f.Sort + `>=? AND (?='' OR i.status=?)`
	args = append(args, f.MinMS, f.Status, f.Status)
	if f.Cursor != nil {
		query += ` AND (i.` + f.Sort + `<? OR (i.` + f.Sort + `=? AND i.id<?))`
		args = append(args, f.Cursor.Value, f.Cursor.Value, f.Cursor.ID)
	}
	query += ` ORDER BY i.` + f.Sort + ` DESC,i.id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &slowInvocationsPage{Since: f.Since.Format(time.RFC3339Nano), Until: f.Until.Format(time.RFC3339Nano), Sort: f.Sort, Invocations: []*slowInvocation{}}
	for rows.Next() {
		i := &slowInvocation{}
		if err := rows.Scan(&i.ID, &i.FunctionID, &i.FunctionName, &i.VersionID, &i.StartedAt, &i.FinishedAt, &i.Status, &i.TriggerKind, &i.DurationMS, &i.ExecutionMS, &i.QueueMS, &i.BuildMS, &i.ColdStartMS); err != nil {
			return nil, err
		}
		page.Invocations = append(page.Invocations, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Invocations) > f.Limit {
		page.Invocations = page.Invocations[:f.Limit]
		last := page.Invocations[len(page.Invocations)-1]
		value := last.DurationMS
		if f.Sort == "execution_ms" {
			value = last.ExecutionMS
		} else if f.Sort == "queue_ms" {
			value = last.QueueMS
		}
		b, _ := json.Marshal(slowCallCursor{Since: page.Since, Until: page.Until, Scope: slowCursorScope(pid, f), Value: value, ID: last.ID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return page, nil
}

func (a *App) toolPerformance(parent context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	f, err := parsePerformanceFilter(args, time.Now(), false)
	if err != nil {
		return nil, err
	}
	f.FunctionID, err = performanceFunctionID(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	return dbFunctionPerformance(parent, ctx.AppDB(), pid, f)
}

func (a *App) toolSlowInvocations(parent context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	f, err := slowFilter(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	return dbSlowInvocations(parent, ctx.AppDB(), pid, f)
}

func (a *App) handleHTTPPerformance(w http.ResponseWriter, r *http.Request) {
	a.handleHTTPPerformanceReport(w, r, false)
}

func (a *App) handleHTTPSlowInvocations(w http.ResponseWriter, r *http.Request) {
	a.handleHTTPPerformanceReport(w, r, true)
}

func (a *App) handleHTTPPerformanceReport(w http.ResponseWriter, r *http.Request, slow bool) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := map[string]any{}
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			args[key] = values[0]
		}
	}
	var f performanceFilter
	if slow {
		f, err = slowFilter(globalCtx, pid, args)
	} else {
		f, err = parsePerformanceFilter(args, time.Now(), false)
		if err == nil {
			f.FunctionID, err = performanceFunctionID(globalCtx, pid, args)
		}
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var report any
	if slow {
		report, err = dbSlowInvocations(r.Context(), globalCtx.AppDB(), pid, f)
	} else {
		report, err = dbFunctionPerformance(r.Context(), globalCtx.AppDB(), pid, f)
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "could not load performance report")
		return
	}
	httpJSON(w, report)
}

func performanceTool(a *App, slow bool) sdk.Tool {
	properties := map[string]any{
		"_project_id": map[string]any{"type": "string", "description": "Required for a global installation; project installations use their own scope."},
		"id":          map[string]any{"type": "integer", "minimum": 1, "description": "Optional function ID; omit id/name to compare all functions in the project."},
		"name":        map[string]any{"type": "string", "description": "Optional function name."},
		"window":      map[string]any{"type": "string", "enum": []string{"1h", "6h", "24h", "7d", "30d"}, "default": "24h"},
		"since":       map[string]any{"type": "string", "description": "Inclusive RFC3339 start; overrides window. Range must be at most 30 days."},
		"until":       map[string]any{"type": "string", "description": "Exclusive RFC3339 end, default now."},
		"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 50},
		"sort":        map[string]any{"type": "string", "enum": performanceSorts, "default": "total_execution_ms"},
	}
	tool := sdk.Tool{Name: "functions_performance", Description: "Find hot, busy, slow or failing functions over a time window. Sort descending by total_execution_ms (wall time including downstream calls, not CPU), calls, p95_execution_ms, p95_duration_ms, avg_execution_ms, avg_duration_ms, avg_queue_ms, errors or error_rate. Exact p95 over retained completed calls; totals cover all matching functions even when limit truncates the ranking (has_more). Use functions_slow_invocations with a returned function_id as id to investigate, then functions_logs for timings and resources.", HandlerCtx: a.toolPerformance}
	if slow {
		tool.Name = "functions_slow_invocations"
		tool.Description = "Find slow completed calls across a project or for one function, sorted descending by duration_ms (total), execution_ms (handler plus downstream calls) or queue_ms. Filter by window/since/until, min_ms on the selected sort metric, and status. Returns safe timing summaries and invocation IDs; use functions_logs for output, timing breakdown and downstream resources. Pass next_cursor as cursor with the same filters; the cursor preserves the original time window."
		tool.HandlerCtx = a.toolSlowInvocations
		properties["sort"] = map[string]any{"type": "string", "enum": slowCallSorts, "default": "duration_ms"}
		properties["min_ms"] = map[string]any{"type": "integer", "minimum": 0, "description": "Inclusive minimum for the selected sort metric, in milliseconds."}
		properties["status"] = map[string]any{"type": "string", "enum": []string{"ok", "error", "timeout", "upstream_timeout", "canceled"}}
		properties["cursor"] = map[string]any{"type": "string", "description": "next_cursor from previous page; keep function, sort, status and min_ms filters unchanged."}
	}
	tool.InputSchema = schemaObject(properties, nil)
	return tool
}
