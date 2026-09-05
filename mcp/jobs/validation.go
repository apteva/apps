package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const maxRequestBytes = 128 << 10
const maxTargetBytes = 64 << 10
const maxActiveJobs = 10000

type apiError struct {
	code    int
	message string
}

func (e *apiError) Error() string { return e.message }
func badRequest(s string) error   { return &apiError{400, s} }
func conflict(s string) error     { return &apiError{409, s} }
func writeError(w http.ResponseWriter, err error) {
	var e *apiError
	if errors.As(err, &e) {
		httpErr(w, e.code, e.message)
		return
	}
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		httpErr(w, 413, "request body too large")
		return
	}
	httpErr(w, 500, "jobs operation failed")
}
func decodeRequest(w http.ResponseWriter, r *http.Request, out any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		var e *http.MaxBytesError
		if errors.As(err, &e) {
			return err
		}
		return badRequest("invalid JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return badRequest("expected exactly one JSON object")
	}
	return nil
}
func integer(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case json.Number:
		return strconv.ParseInt(string(n), 10, 64)
	case string:
		return strconv.ParseInt(strings.TrimSpace(n), 10, 64)
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < -9007199254740991 || n > 9007199254740991 {
			return 0, errors.New("invalid integer")
		}
		return int64(n), nil
	default:
		return 0, errors.New("integer required")
	}
}
func boundedInteger(m map[string]any, k string, min, max int64) error {
	if v, ok := m[k]; ok {
		n, err := integer(v)
		if err != nil || n < min || n > max {
			return badRequest(fmt.Sprintf("%s must be an integer between %d and %d", k, min, max))
		}
	}
	return nil
}
func validateScheduleArgs(args map[string]any) error {
	name := strings.TrimSpace(strArg(args, "name"))
	if name == "" || len(name) > 200 {
		return badRequest("name must contain 1–200 bytes")
	}
	schedule, ok := args["schedule"].(map[string]any)
	if !ok {
		return badRequest("schedule required")
	}
	target, ok := args["target"].(map[string]any)
	if !ok {
		return badRequest("target required")
	}
	for k, b := range map[string][2]int64{"max_retries": {0, 20}, "backoff_seconds": {1, 86400}, "owner_instance": {1, 9007199254740991}} {
		if err := boundedInteger(args, k, b[0], b[1]); err != nil {
			return err
		}
	}
	if len(strArg(args, "idempotency_key")) > 128 {
		return badRequest("idempotency_key is limited to 128 bytes")
	}
	if len(strArg(args, "owner_app")) > 128 {
		return badRequest("owner_app is too long")
	}
	if tz := strArg(args, "timezone"); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return badRequest("unknown timezone")
		}
	}
	switch strArg(schedule, "kind") {
	case "once":
		if _, err := parseTime(strArg(schedule, "run_at"), time.UTC); err != nil {
			return badRequest("invalid schedule.run_at")
		}
	case "every":
		if err := boundedInteger(schedule, "every_seconds", 1, 31536000); err != nil {
			return err
		}
		if _, ok := schedule["every_seconds"]; !ok {
			d, err := time.ParseDuration(strArg(schedule, "every"))
			if err != nil || d < time.Second || d > 365*24*time.Hour || d%time.Second != 0 {
				return badRequest("interval must be whole seconds between 1 second and 365 days")
			}
		}
	case "cron":
		if len(strArg(schedule, "cron")) > 256 {
			return badRequest("cron is too long")
		}
		if _, err := parseCron(strArg(schedule, "cron")); err != nil {
			return badRequest(err.Error())
		}
	case "random":
		for k, b := range map[string][2]int64{"runs_per_period": {1, 100}, "min_spacing_minutes": {0, 1440}} {
			if err := boundedInteger(schedule, k, b[0], b[1]); err != nil {
				return err
			}
		}
		if _, err := parseRandomSchedule(schedule); err != nil {
			return badRequest(err.Error())
		}
	default:
		return badRequest("schedule.kind must be once, every, cron, or random")
	}
	if err := validateTarget(target); err != nil {
		return badRequest(err.Error())
	}
	for k, b := range map[string][2]int64{"timeout_seconds": {1, 300}, "timeout_ms": {1, 300000}, "instance_id": {1, 9007199254740991}} {
		if err := boundedInteger(target, k, b[0], b[1]); err != nil {
			return err
		}
	}
	data, err := json.Marshal(target)
	if err != nil || len(data) > maxTargetBytes {
		return badRequest("target must be valid JSON of at most 64 KiB")
	}
	if h, ok := target["headers"]; ok {
		headers, ok := h.(map[string]any)
		if !ok || len(headers) > 32 {
			return badRequest("headers must be an object with at most 32 entries")
		}
		for k, v := range headers {
			s, ok := v.(string)
			if !ok || len(k) > 128 || len(s) > 8192 || strings.ContainsAny(k+s, "\r\n") {
				return badRequest("invalid HTTP header")
			}
		}
	}
	if method := strKey(target, "method"); method != "" {
		switch strings.ToUpper(method) {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		default:
			return badRequest("unsupported HTTP method")
		}
	}
	return nil
}

func (a *App) httpContext(r *http.Request) *sdk.AppCtx {
	ctx := a.requestCtx
	if ctx != nil {
		if pid, err := resolveProjectFromRequest(r); err == nil && pid != "" {
			return ctx.WithProject(pid)
		}
	}
	return ctx
}
func escapeLike(s string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s)
}
func jobsPage(jobs []*Job, limit int) map[string]any {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	more := len(jobs) > limit
	if more {
		jobs = jobs[:limit]
	}
	out := map[string]any{"jobs": jobs, "count": len(jobs), "has_more": more}
	if more {
		out["next_cursor"] = jobs[len(jobs)-1].ID
	}
	return out
}

func queueManualRun(db *sql.DB, pid string, id int64, parents ...context.Context) error {
	ctx, cancel := operationContext(parents)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var nextID int64
	if err := tx.QueryRowContext(ctx, `UPDATE job_sequence SET value=MAX(value,(SELECT COALESCE(MAX(id),0) FROM jobs))+1 RETURNING value`).Scan(&nextID); err != nil {
		return err
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM jobs WHERE project_id=? AND id=? AND parent_job_id IS NULL`, pid, id).Scan(&status); err == sql.ErrNoRows {
		return &apiError{404, "job not found"}
	} else if err != nil {
		return err
	}
	if status == "running" {
		return conflict("job is already running")
	}
	if status == "cancelled" {
		return conflict("cannot run a cancelled job")
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE parent_job_id=? AND status IN ('pending','running')`, id).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return conflict("a manual run is already queued")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE project_id=? AND status IN ('pending','running')`, pid).Scan(&active); err != nil {
		return err
	}
	if active >= maxActiveJobs {
		return conflict("active job limit reached")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,project_id,name,owner_app,owner_instance,schedule_kind,run_at,timezone,target_kind,target_json,idempotency_key,max_retries,backoff_seconds,status,next_run_at,scheduled_for,created_at,updated_at,parent_job_id)
 SELECT ?,project_id,name,owner_app,owner_instance,'once',?,'UTC',target_kind,target_json,
 COALESCE(idempotency_key,'job-'||id)||':manual-'||?,max_retries,backoff_seconds,'pending',?,?,?,?,id
 FROM jobs WHERE id=? AND project_id=?`, nextID, now, nextID, now, now, now, now, id, pid)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) handleHTTPScope(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		httpErr(w, 405, "method not allowed")
		return
	}
	httpJSON(w, map[string]any{"global_install": strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")) == ""})
}

func cursorValue(v []int64) int64 {
	if len(v) > 0 && v[0] > 0 {
		return v[0]
	}
	return 0
}
func runsPage(runs []*JobRun, limit int) map[string]any {
	more := len(runs) > limit
	if more {
		runs = runs[:limit]
	}
	out := map[string]any{"runs": runs, "has_more": more}
	if more {
		out["next_cursor"] = runs[len(runs)-1].ID
	}
	return out
}

func operationContext(parents []context.Context) (context.Context, context.CancelFunc) {
	parent := context.Background()
	if len(parents) > 0 && parents[0] != nil {
		parent = parents[0]
	}
	return context.WithTimeout(parent, 5*time.Second)
}
func argsContext(args map[string]any) context.Context {
	if ctx, ok := args["_request_context"].(context.Context); ok {
		return ctx
	}
	return context.Background()
}

func validateListQuery(r *http.Request, maxLimit int) error {
	q := r.URL.Query()
	for key, upper := range map[string]int64{"limit": int64(maxLimit), "before": 9007199254740991, "owner_instance": 9007199254740991} {
		if v, ok := q[key]; ok {
			if len(v) != 1 {
				return badRequest("duplicate query parameter")
			}
			if err := boundedInteger(map[string]any{key: v[0]}, key, 1, upper); err != nil {
				return err
			}
		}
	}
	if len(q.Get("search")) > 200 || len(q.Get("owner_app")) > 128 {
		return badRequest("filter is too long")
	}
	switch q.Get("status") {
	case "", "pending", "running", "done", "failed", "cancelled":
	default:
		return badRequest("invalid status")
	}
	return nil
}
func (a *App) handleHTTPStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		httpErr(w, 405, "method not allowed")
		return
	}
	if _, err := resolveProjectFromRequest(r); err != nil {
		writeError(w, err)
		return
	}
	if a.dispatcher == nil {
		httpErr(w, 503, "dispatcher unavailable")
		return
	}
	d := a.dispatcher
	httpJSON(w, map[string]any{"last_scan_at": time.Unix(d.lastTick.Load(), 0).UTC().Format(time.RFC3339), "active_deliveries": len(d.slots), "capacity": cap(d.slots), "infrastructure_failures": d.failures.Load()})
}
