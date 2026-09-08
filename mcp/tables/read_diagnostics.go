package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	sqlite "modernc.org/sqlite"
)

type readObservationKey struct{}

var (
	errReadQueueDeadline     = errors.New("read connection queue deadline")
	errReadExecutionDeadline = errors.New("read execution deadline")
	errReadMetadataDeadline  = errors.New("read metadata deadline")
	errReadOperationDeadline = errors.New("read operation deadline")
)

// Observations belong to one synchronous tool invocation, never to the shared AppCtx.
// The outer defer runs after rows, connections and operation locks are released.
type readObservation struct {
	app                                              *sdk.AppCtx
	started, phaseStarted                            time.Time
	phase, failureStage                              string
	operation, callID, requestID, projectID, queryID string
	phases                                           map[string]time.Duration
	poolStart, poolAcquired                          sql.DBStats
	acquired                                         bool
	deadlineSource                                   string
	expiredDeadline                                  time.Time
	executionBudget                                  time.Duration
	partialRows                                      int
}

func startReadObservation(ctx *sdk.AppCtx, args map[string]any, operation string) (map[string]any, *readObservation) {
	parent, _ := args["_request_context"].(context.Context)
	if parent == nil {
		parent = context.Background()
	}
	now := time.Now()
	pid, _ := resolveProjectFromArgs(args)
	d := &readObservation{app: ctx, started: now, phaseStarted: now,
		phase: "prepare", operation: operation, callID: rand.Text(), requestID: diagnosticRequestID(strArg(args, "request_id")),
		projectID: pid, queryID: readFingerprint(operation, args), phases: map[string]time.Duration{}, poolStart: ctx.AppReadDB().Stats()}
	cp := make(map[string]any, len(args)+1)
	for k, v := range args {
		cp[k] = v
	}
	cp["_request_context"] = context.WithValue(parent, readObservationKey{}, d)
	return cp, d
}
func readObservationFor(ctx *sdk.AppCtx) *readObservation {
	d, _ := requestContext(ctx).Value(readObservationKey{}).(*readObservation)
	return d
}
func (d *readObservation) setPhase(phase string) {
	now := time.Now()
	if phase == "cleanup" && d.phase != "cleanup" && d.failureStage == "" {
		d.failureStage = d.phase
	}
	d.phases[d.phase] += now.Sub(d.phaseStarted)
	d.phase, d.phaseStarted = phase, now
}
func readPhase(ctx *sdk.AppCtx, phase string) {
	if d := readObservationFor(ctx); d != nil {
		d.setPhase(phase)
	}
}

// Capture cancellation before deferred cancel functions change successful contexts.
func observeReadCancellation(ctx *sdk.AppCtx, call context.Context) {
	d := readObservationFor(ctx)
	if d == nil || call.Err() == nil || d.deadlineSource != "" {
		return
	}
	d.expiredDeadline, _ = call.Deadline()
	switch context.Cause(call) {
	case errReadMetadataDeadline:
		d.deadlineSource = "metadata"
	case errReadQueueDeadline:
		d.deadlineSource = "read_queue"
	case errReadExecutionDeadline:
		d.deadlineSource = "execution"
	case errReadOperationDeadline:
		d.deadlineSource = "operation"
	default:
		if errors.Is(call.Err(), context.DeadlineExceeded) {
			d.deadlineSource = "upstream"
		} else {
			d.deadlineSource = "caller_cancel"
		}
	}
}
func diagnosticRequestID(s string) string {
	if len(s) == 0 || len(s) > 128 {
		return ""
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:/", c)) {
			return ""
		}
	}
	return s
}
func (d *readObservation) finish(result any, err error) {
	if d.phase != "cleanup" {
		d.failureStage = d.phase
	}
	d.setPhase("done")
	elapsed := time.Since(d.started)
	if err == nil && elapsed < time.Duration(slowQueryMs(d.app))*time.Millisecond && d.app.Config().Get("log_all_reads") != "true" {
		return
	}
	var sqliteErr *sqlite.Error
	interrupted := errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 9 // SQLITE_INTERRUPT
	outcome := "ok"
	stage := ""
	if err != nil {
		outcome, stage = "error", d.failureStage
		if stage == "" {
			stage = "prepare"
		}
		var staged *stagedQueryError
		if errors.As(err, &staged) {
			stage = staged.stage
		}
		if errors.Is(err, context.Canceled) || interrupted && d.deadlineSource == "caller_cancel" {
			outcome = "canceled"
		} else if errors.Is(err, context.DeadlineExceeded) || interrupted && d.deadlineSource != "" {
			outcome = "timeout"
		}
	}
	returnedRows, truncated := readResultSummary(result)
	end := d.app.AppReadDB().Stats()
	var overrun time.Duration
	if !d.expiredDeadline.IsZero() && d.deadlineSource != "caller_cancel" {
		overrun = max(time.Duration(0), time.Since(d.expiredDeadline))
	}
	fields := []any{"operation", d.operation, "call_id", d.callID, "request_id", d.requestID, "project_id", d.projectID, "query_id", d.queryID,
		"sql_ms", durationMS(d.phases["count"] + d.phases["select"] + d.phases["scan"]), "execution_budget_ms", durationMS(d.executionBudget), "deadline_overrun_ms", durationMS(overrun), "outcome", outcome, "stage", stage, "deadline_source", d.deadlineSource, "total_ms", durationMS(elapsed),
		"rows_returned", returnedRows, "rows_materialized", max(returnedRows, d.partialRows), "truncated", truncated,
		"pool_max_open", end.MaxOpenConnections, "pool_in_use_start", d.poolStart.InUse, "pool_in_use_end", end.InUse, "pool_idle_end", end.Idle,
		"pool_wait_count_total", end.WaitCount, "pool_wait_ms_total", durationMS(end.WaitDuration), "connection_acquired", d.acquired,
		"shared_writer_pool", d.app.AppReadDB() == d.app.AppDB(), "max_query_ms", maxQueryMs(d.app), "max_read_queue_ms", maxReadQueueMs(d.app)}
	if d.acquired {
		fields = append(fields, "pool_in_use_acquired", d.poolAcquired.InUse)
	}
	for _, phase := range []string{"prepare", "schema_queue", "metadata", "read_queue", "connection_setup", "authorization", "count", "select", "scan", "cleanup", "hydrate"} {
		fields = append(fields, phase+"_ms", durationMS(d.phases[phase]))
	}
	if sqliteErr != nil {
		fields = append(fields, "sqlite_error_code", sqliteErr.Code())
	}
	// No SQL, parameters, row values or raw error strings: SQLite errors can contain literals.
	if err != nil {
		fields = append(fields, "error_type", fmt.Sprintf("%T", err))
		d.app.Logger().Warn("tables read completed", fields...)
	} else {
		d.app.Logger().Info("tables read completed", fields...)
	}
}
func durationMS(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func readResultSummary(result any) (int, bool) {
	m, ok := result.(map[string]any)
	if !ok {
		return 0, false
	}
	truncated, _ := m["truncated"].(bool)
	if rows, ok := m["rows"].([]map[string]any); ok {
		return len(rows), truncated
	}
	if found, _ := m["found"].(bool); found {
		return 1, truncated
	}
	if _, ok := m["count"]; ok {
		return 1, truncated
	}
	if rows, ok := m["tables"].([]map[string]any); ok {
		return len(rows), truncated
	}
	if indexes, ok := m["indexes"].([]TableIndex); ok {
		return len(indexes), truncated
	}
	if _, ok := m["columns"]; ok {
		return 1, truncated
	}
	return 0, truncated
}

// Fingerprint SQL tokens, not regex-substituted SQL text: quoted strings and
// comments must not accidentally become identifiers. Only the hash is logged.
var sqlBind = regexp.MustCompile(`^(?:\?[0-9]*|[:@$][a-zA-Z_][a-zA-Z_0-9]*)`)
var sqlNumber = regexp.MustCompile(`(?i)^(?:0x[0-9a-f_]+|(?:[0-9][0-9_]*(?:\.[0-9_]*)?|\.[0-9][0-9_]*)(?:e[+-]?[0-9][0-9_]*)?)`)

func normalizedSQL(sqlText string) string {
	if len(sqlText) > 64<<10 {
		return "oversized"
	}
	tokens, err := sqlTokens(sqlText)
	if err != nil {
		return "invalid"
	}
	parts := make([]string, 0, len(tokens))
	skipUntil := 0
	for i, t := range tokens {
		if t.start < skipUntil {
			continue
		}
		if t.kind == "string" {
			parts = append(parts, "?")
			continue
		}
		if n := sqlNumber.FindString(sqlText[t.start:]); n != "" && t.kind == "symbol" {
			parts = append(parts, "?")
			skipUntil = t.start + len(n)
			continue
		}
		if t.kind == "identifier" && strings.EqualFold(t.value, "x") && i+1 < len(tokens) && tokens[i+1].kind == "string" && t.end == tokens[i+1].start {
			parts = append(parts, "?")
			skipUntil = tokens[i+1].end
			continue
		}
		if bind := sqlBind.FindString(sqlText[t.start:]); bind != "" && t.kind == "symbol" {
			parts = append(parts, "?")
			skipUntil = t.start + len(bind)
			continue
		}
		parts = append(parts, strings.ToLower(t.value))
	}
	if len(parts) > 0 && parts[len(parts)-1] == ";" {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, " ")
}
func readFingerprint(operation string, args map[string]any) string {
	var shape string
	if operation == "tables_query" {
		shape = normalizedSQL(strArg(args, "sql"))
	} else {
		// Include query structure; omit IDs, filter values and pagination cursor contents.
		filtered := map[string]any{}
		for _, k := range []string{"table", "name", "where", "select", "order_by", "include_total", "hydrate_files", "group_by", "metrics", "summary"} {
			if v, ok := args[k]; ok {
				filtered[k] = v
			}
		}
		budget := 4096
		b, _ := json.Marshal(queryArgumentShape(filtered, "", 0, &budget))
		shape = string(b)
	}
	sum := sha256.Sum256([]byte(operation + "\n" + shape))
	return fmt.Sprintf("v1:%x", sum[:16])
}
func queryArgumentShape(v any, key string, depth int, budget *int) any {
	*budget -= 1
	if depth > 16 || *budget < 0 {
		return "bounded"
	}
	if key == "value" {
		return "?"
	}
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			if *budget < 0 {
				break
			}
			out[k] = queryArgumentShape(v, k, depth+1, budget)
		}
		return out
	case []any:
		out := make([]any, 0, min(len(x), 4096))
		for _, v := range x {
			if *budget < 0 {
				break
			}
			out = append(out, queryArgumentShape(v, key, depth+1, budget))
		}
		return out
	case string:
		if len(x) > 64<<10 {
			return "oversized"
		}
		return x
	case bool:
		return x
	default:
		return "?"
	}
}
