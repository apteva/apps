// Jobs v0.1 — scheduled-job runner.
//
// Other apps and agents enqueue work to be delivered later: at a fixed
// time, on an interval, or on a cron expression. Targets are HTTP
// routes (on another app, or absolute URLs gated by net.egress) or
// instance events (PlatformAPI.SendEvent). Jobs never knows what the
// work is — it only knows how to deliver a payload to one of two
// well-defined endpoints.
//
// At-least-once delivery with idempotency keys forwarded to HTTP
// targets, exponential backoff on failure, configurable max_retries.
// Single dispatcher goroutine driven by an SDK Worker (@every 5s);
// SQLite row-level lease prevents double-dispatch if a tick crashes
// mid-flight.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest (also lives in apteva.yaml; embedded so the running
// binary is self-describing for `jobs --help` etc.) ────────────────

const manifestYAML = `schema: apteva-app/v1
name: jobs
display_name: Jobs
version: 0.1.1
description: |
  Scheduled-job runner. Other apps and agents enqueue work; jobs
  delivers it later via HTTP or instance events.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.instances.read
    - platform.instances.write
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: jobs_schedule
      description: Schedule a job (once / every / cron) with an HTTP or event target.
    - name: jobs_cancel
      description: Cancel a scheduled job. Idempotent.
    - name: jobs_list
      description: List jobs filtered by owner_app, owner_instance, status.
    - name: jobs_get
      description: Fetch one job by id.
    - name: jobs_runs
      description: Fetch recent runs for a job.
    - name: jobs_run_now
      description: Trigger an immediate ad-hoc run of a scheduled job.
  ui_panels:
    - slot: project.page
      label: Jobs
      icon: clock
      entry: /ui/JobsPanel.mjs
  workers:
    - name: dispatcher
      schedule: "@every 5s"
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/jobs
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/jobs.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("jobs requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("jobs mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── Workers — the dispatcher tick. ────────────────────────────────

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "dispatcher",
			Schedule: "@every 5s",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return dispatchTick(ctx, app)
			},
		},
	}
}

// ─── HTTP routes (REST surface for the dashboard panel + other apps).
//
// Reverse-proxied at /api/apps/jobs/* by apteva-server. Other apps
// hit these to enqueue work; the dashboard panel uses them to render
// the jobs list and runs viewer.

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/jobs", Handler: a.handleHTTPJobsCollection},
		{Pattern: "/jobs/", Handler: a.handleHTTPJobItem},
		{Pattern: "/runs", Handler: a.handleHTTPRunsCollection},
	}
}

func (a *App) handleHTTPJobsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPList(w, r)
	case http.MethodPost:
		a.handleHTTPCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleHTTPJobItem dispatches /jobs/<id>, /jobs/<id>/runs,
// /jobs/<id>/run-now.
func (a *App) handleHTTPJobItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/jobs/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 2 && parts[1] == "runs" {
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.handleHTTPJobRuns(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "run-now" {
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.handleHTTPRunNow(w, r, parts[0])
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPGet(w, r, parts[0])
	case http.MethodDelete:
		a.handleHTTPCancel(w, r, parts[0])
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPRunsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 50, 200)
	rows, err := dbRecentRuns(ctx.AppDB(), pid, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"runs": rows})
}

func (a *App) handleHTTPCreate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	job, err := dbScheduleJob(ctx.AppDB(), pid, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"job": job})
}

func (a *App) handleHTTPList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	filter := JobFilter{
		OwnerApp:      q.Get("owner_app"),
		OwnerInstance: parseInt64(q.Get("owner_instance")),
		Status:        q.Get("status"),
		Limit:         atoiDefault(q.Get("limit"), 100, 500),
	}
	out, err := dbListJobs(ctx.AppDB(), pid, filter)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"jobs": out})
}

func (a *App) handleHTTPGet(w http.ResponseWriter, r *http.Request, idStr string) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	job, err := dbGetJob(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if job == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"job": job})
}

func (a *App) handleHTTPCancel(w http.ResponseWriter, r *http.Request, idStr string) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if err := dbCancelJob(ctx.AppDB(), pid, id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"cancelled": true, "id": id})
}

func (a *App) handleHTTPJobRuns(w http.ResponseWriter, r *http.Request, idStr string) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 50, 200)
	runs, err := dbJobRuns(ctx.AppDB(), pid, id, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"runs": runs})
}

func (a *App) handleHTTPRunNow(w http.ResponseWriter, r *http.Request, idStr string) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if err := dbRunNow(ctx.AppDB(), pid, id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"queued": true, "id": id})
}

// ─── MCP tools (the agent's surface) ───────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "jobs_schedule",
			Description: "Schedule a job to run later (once, on an interval, or on a cron expression). Use schedule.kind=once with run_at for one-shot, schedule.kind=every with every_seconds for intervals, schedule.kind=cron with cron='M H DOM MON DOW' for cron. Use target.kind=event with agent_id=<id> to wake an agent with a message, or target.kind=http with {app, path, body?} to call another app's HTTP route, or target.kind=http with url=<absolute> for an external webhook.",
			InputSchema: schemaObject(map[string]any{
				"name": map[string]any{"type": "string", "description": "Human-readable job name."},
				"schedule": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind":          map[string]any{"type": "string", "enum": []any{"once", "every", "cron"}},
						"run_at":        map[string]any{"type": "string", "description": "RFC3339 timestamp; required when kind=once."},
						"every_seconds": map[string]any{"type": "integer", "description": "Interval in seconds; required when kind=every."},
						"cron":          map[string]any{"type": "string", "description": "5-field cron 'M H DOM MON DOW'; required when kind=cron."},
					},
					"required": []any{"kind"},
				},
				"target": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind":     map[string]any{"type": "string", "enum": []any{"event", "http"}},
						"agent_id": map[string]any{"description": "For kind=event: numeric agent id (instance) to wake. Pass 'self' to target the calling agent."},
						"message":  map[string]any{"type": "string", "description": "For kind=event: the message to deliver."},
						"url":      map[string]any{"type": "string", "description": "For kind=http: absolute URL (requires net.egress)."},
						"app":      map[string]any{"type": "string", "description": "For kind=http: target app slug (e.g. 'crm')."},
						"path":     map[string]any{"type": "string", "description": "For kind=http: path on the target app (e.g. '/cron/foo')."},
						"method":   map[string]any{"type": "string", "description": "For kind=http: HTTP method (default POST)."},
						"body":     map[string]any{"description": "For kind=http: JSON body to POST."},
					},
					"required": []any{"kind"},
				},
				"idempotency_key": map[string]any{"type": "string"},
				"max_retries":     map[string]any{"type": "integer"},
				"backoff_seconds": map[string]any{"type": "integer"},
				"timezone":        map[string]any{"type": "string", "description": "IANA tz name; cron is evaluated in this tz. Default UTC."},
				"owner_app":       map[string]any{"type": "string"},
			}, []string{"name", "schedule", "target"}),
			Handler: a.toolSchedule,
		},
		{
			Name:        "jobs_cancel",
			Description: "Cancel a scheduled job by id. Idempotent — cancelling an already-terminal job returns ok.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCancel,
		},
		{
			Name:        "jobs_list",
			Description: "List jobs. Args: owner_app, owner_instance, status (pending|running|done|failed|cancelled), limit (default 100, max 500).",
			InputSchema: schemaObject(map[string]any{
				"owner_app":      map[string]any{"type": "string"},
				"owner_instance": map[string]any{"type": "integer"},
				"status":         map[string]any{"type": "string"},
				"limit":          map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolList,
		},
		{
			Name:        "jobs_get",
			Description: "Fetch one job by id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolGet,
		},
		{
			Name:        "jobs_runs",
			Description: "Fetch recent runs for a job. Args: id, limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"limit": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolRuns,
		},
		{
			Name:        "jobs_run_now",
			Description: "Queue an immediate ad-hoc run of a scheduled job. Bumps next_run_at to now without changing the schedule.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolRunNow,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution ────────────────────────────────────────────
//
// Same shape as crm. `scope: project` installs read APTEVA_PROJECT_ID
// from env; `scope: global` installs require the caller to pass it
// via _project_id (MCP) or ?project_id (HTTP).

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

// ─── Domain types ──────────────────────────────────────────────────

type Job struct {
	ID             int64          `json:"id"`
	ProjectID      string         `json:"project_id,omitempty"`
	Name           string         `json:"name"`
	OwnerApp       string         `json:"owner_app,omitempty"`
	OwnerInstance  *int64         `json:"owner_instance,omitempty"`
	ScheduleKind   string         `json:"schedule_kind"`
	CronExpr       string         `json:"cron_expr,omitempty"`
	EverySeconds   *int64         `json:"every_seconds,omitempty"`
	RunAt          string         `json:"run_at,omitempty"`
	Timezone       string         `json:"timezone"`
	Target         map[string]any `json:"target"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	MaxRetries     int            `json:"max_retries"`
	BackoffSeconds int            `json:"backoff_seconds"`
	Status         string         `json:"status"`
	NextRunAt      string         `json:"next_run_at,omitempty"`
	LastRunAt      string         `json:"last_run_at,omitempty"`
	LastStatus     string         `json:"last_status,omitempty"`
	LastError      string         `json:"last_error,omitempty"`
	Attempt        int            `json:"attempt"`
	CreatedAt      string         `json:"created_at,omitempty"`
	UpdatedAt      string         `json:"updated_at,omitempty"`
	CancelledAt    string         `json:"cancelled_at,omitempty"`
}

type JobRun struct {
	ID           int64  `json:"id"`
	JobID        int64  `json:"job_id"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at,omitempty"`
	DurationMS   int64  `json:"duration_ms"`
	Status       string `json:"status"`
	HTTPStatus   int    `json:"http_status,omitempty"`
	ResponseBody string `json:"response_body,omitempty"`
	Error        string `json:"error,omitempty"`
	Attempt      int    `json:"attempt"`
}

type JobFilter struct {
	OwnerApp      string
	OwnerInstance int64
	Status        string
	Limit         int
}

// ─── Tool handlers ─────────────────────────────────────────────────

func (a *App) toolSchedule(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	// Tag agent-scheduled jobs with the calling instance id, if the
	// platform passed it through. Apteva-core injects _instance_id on
	// every tool call; we don't fail if it's missing (CLI / tests).
	callerInstance := int64Arg(args, "_instance_id")
	if _, has := args["owner_instance"]; !has && callerInstance != 0 {
		args["owner_instance"] = callerInstance
	}
	// Translate the agent_id surface (LLM-facing) to instance_id (wire
	// format used by PlatformAPI.SendEvent + the rest of the SDK).
	// Scheduling agents typically want reminders sent to themselves;
	// accept agent_id="self" or 0 and substitute the caller's
	// _instance_id (when the platform injects it). When neither side
	// supplies an id the dispatcher will still fail at validation —
	// but with a clearer error than "missing instance_id".
	if t, ok := args["target"].(map[string]any); ok {
		if strings.EqualFold(strKey(t, "kind"), "event") {
			// Pull agent_id (preferred) or instance_id (legacy).
			raw, has := t["agent_id"]
			if !has {
				raw = t["instance_id"]
			}
			needsSelf := raw == nil
			if s, isStr := raw.(string); isStr && (s == "" || strings.EqualFold(s, "self") || s == "0") {
				needsSelf = true
			}
			if !needsSelf && toInt64(raw) == 0 {
				needsSelf = true
			}
			if needsSelf && callerInstance != 0 {
				t["instance_id"] = callerInstance
			} else if !needsSelf {
				t["instance_id"] = toInt64(raw)
			}
			// Drop the agent_id surface — wire format is instance_id.
			delete(t, "agent_id")
		}
	}
	job, err := dbScheduleJob(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	emitJob(ctx, "job.scheduled", job)
	return map[string]any{"job": job}, nil
}

func (a *App) toolCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := dbCancelJob(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("job.cancelled", map[string]any{"id": id})
	}
	return map[string]any{"cancelled": true, "id": id}, nil
}

// emitJob broadcasts a job lifecycle event. Best-effort fire-and-forget.
// Subscribers re-fetch the row themselves; payload is just enough for
// optimistic UI.
func emitJob(ctx *sdk.AppCtx, topic string, j *Job) {
	if ctx == nil || j == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"id":             j.ID,
		"name":           j.Name,
		"status":         j.Status,
		"owner_app":      j.OwnerApp,
		"owner_instance": j.OwnerInstance,
		"run_at":         j.RunAt,
	})
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	filter := JobFilter{
		OwnerApp:      strArg(args, "owner_app"),
		OwnerInstance: int64Arg(args, "owner_instance"),
		Status:        strArg(args, "status"),
		Limit:         intArg(args, "limit", 100),
	}
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	out, err := dbListJobs(ctx.AppDB(), pid, filter)
	if err != nil {
		return nil, err
	}
	return map[string]any{"jobs": out, "count": len(out)}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	job, err := dbGetJob(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return map[string]any{"job": nil, "found": false}, nil
	}
	return map[string]any{"job": job, "found": true}, nil
}

func (a *App) toolRuns(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	runs, err := dbJobRuns(ctx.AppDB(), pid, id, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"runs": runs}, nil
}

func (a *App) toolRunNow(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := dbRunNow(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("job.queued", map[string]any{"id": id})
	}
	return map[string]any{"queued": true, "id": id}, nil
}

// ─── DB helpers ────────────────────────────────────────────────────

func dbScheduleJob(db *sql.DB, pid string, args map[string]any) (*Job, error) {
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	schedule, _ := args["schedule"].(map[string]any)
	if schedule == nil {
		return nil, errors.New("schedule required")
	}
	target, _ := args["target"].(map[string]any)
	if target == nil {
		return nil, errors.New("target required")
	}

	kind := strArg(schedule, "kind")
	now := time.Now().UTC()
	tz := strArg(args, "timezone")
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q", tz)
	}

	var (
		cronExpr     string
		everySeconds *int64
		runAt        time.Time
	)

	switch kind {
	case "once":
		raw := strArg(schedule, "run_at")
		if raw == "" {
			return nil, errors.New("schedule.run_at required for once")
		}
		t, err := parseTime(raw, loc)
		if err != nil {
			return nil, fmt.Errorf("schedule.run_at: %w", err)
		}
		runAt = t

	case "every":
		var secs int64
		if v, ok := schedule["every_seconds"]; ok {
			secs = toInt64(v)
		} else if s := strArg(schedule, "every"); s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				return nil, fmt.Errorf("schedule.every: %w", err)
			}
			secs = int64(d.Seconds())
		}
		if secs <= 0 {
			return nil, errors.New("schedule.every_seconds must be > 0")
		}
		everySeconds = &secs

	case "cron":
		cronExpr = strArg(schedule, "cron")
		if cronExpr == "" {
			return nil, errors.New("schedule.cron required for cron")
		}
		if _, err := parseCron(cronExpr); err != nil {
			return nil, fmt.Errorf("schedule.cron: %w", err)
		}

	default:
		return nil, fmt.Errorf("schedule.kind %q must be once|every|cron", kind)
	}

	if err := validateTarget(target); err != nil {
		return nil, err
	}
	targetJSON, err := json.Marshal(target)
	if err != nil {
		return nil, err
	}
	targetKind := strArg(target, "kind")

	maxRetries := intArg(args, "max_retries", 3)
	if maxRetries < 0 {
		maxRetries = 0
	}
	backoff := intArg(args, "backoff_seconds", 30)
	if backoff < 1 {
		backoff = 30
	}

	// First fire-time.
	next := computeNextRun(kind, runAt, everySeconds, cronExpr, loc, now)

	res, err := db.Exec(
		`INSERT INTO jobs (
			project_id, name, owner_app, owner_instance,
			schedule_kind, cron_expr, every_seconds, run_at, timezone,
			target_kind, target_json,
			idempotency_key, max_retries, backoff_seconds,
			status, next_run_at,
			created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)`,
		pid, name, strArg(args, "owner_app"), nullableInt64(int64Arg(args, "owner_instance")),
		kind, nullStr(cronExpr), nullableInt64Ptr(everySeconds), nullableTime(runAt), tz,
		targetKind, string(targetJSON),
		nullStr(strArg(args, "idempotency_key")), maxRetries, backoff,
		nullableTime(next), now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetJob(db, pid, id)
}

func dbGetJob(db *sql.DB, pid string, id int64) (*Job, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, COALESCE(owner_app,''), owner_instance,
			schedule_kind, COALESCE(cron_expr,''), every_seconds, run_at, timezone,
			target_json,
			COALESCE(idempotency_key,''), max_retries, backoff_seconds,
			status, next_run_at, last_run_at, COALESCE(last_status,''), COALESCE(last_error,''),
			attempt, created_at, updated_at, cancelled_at
		 FROM jobs WHERE id = ? AND project_id = ?`,
		id, pid)
	job, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return job, err
}

// scanRow is the union over *sql.Row and *sql.Rows so scanJob can
// drive both.
type scanRow interface {
	Scan(dest ...any) error
}

func scanJob(row scanRow) (*Job, error) {
	j := &Job{}
	var ownerInst sql.NullInt64
	var everySecs sql.NullInt64
	var runAt, nextRun, lastRun, cancelledAt sql.NullString
	var targetJSON string
	err := row.Scan(
		&j.ID, &j.ProjectID, &j.Name, &j.OwnerApp, &ownerInst,
		&j.ScheduleKind, &j.CronExpr, &everySecs, &runAt, &j.Timezone,
		&targetJSON,
		&j.IdempotencyKey, &j.MaxRetries, &j.BackoffSeconds,
		&j.Status, &nextRun, &lastRun, &j.LastStatus, &j.LastError,
		&j.Attempt, &j.CreatedAt, &j.UpdatedAt, &cancelledAt)
	if err != nil {
		return nil, err
	}
	if ownerInst.Valid {
		v := ownerInst.Int64
		j.OwnerInstance = &v
	}
	if everySecs.Valid {
		v := everySecs.Int64
		j.EverySeconds = &v
	}
	j.RunAt = runAt.String
	j.NextRunAt = nextRun.String
	j.LastRunAt = lastRun.String
	j.CancelledAt = cancelledAt.String
	if targetJSON != "" {
		_ = json.Unmarshal([]byte(targetJSON), &j.Target)
	}
	return j, nil
}

func dbListJobs(db *sql.DB, pid string, f JobFilter) ([]*Job, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.OwnerApp != "" {
		where = append(where, "owner_app = ?")
		args = append(args, f.OwnerApp)
	}
	if f.OwnerInstance != 0 {
		where = append(where, "owner_instance = ?")
		args = append(args, f.OwnerInstance)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, project_id, name, COALESCE(owner_app,''), owner_instance,
			schedule_kind, COALESCE(cron_expr,''), every_seconds, run_at, timezone,
			target_json,
			COALESCE(idempotency_key,''), max_retries, backoff_seconds,
			status, next_run_at, last_run_at, COALESCE(last_status,''), COALESCE(last_error,''),
			attempt, created_at, updated_at, cancelled_at
		 FROM jobs WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY COALESCE(next_run_at, '9999') ASC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			continue
		}
		out = append(out, j)
	}
	return out, nil
}

func dbCancelJob(db *sql.DB, pid string, id int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`UPDATE jobs SET status = 'cancelled',
			cancelled_at = ?, updated_at = ?,
			next_run_at = NULL
		 WHERE id = ? AND project_id = ? AND status NOT IN ('done', 'failed', 'cancelled')`,
		now, now, id, pid)
	return err
}

func dbRunNow(db *sql.DB, pid string, id int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`UPDATE jobs SET next_run_at = ?, status = 'pending', updated_at = ?
		 WHERE id = ? AND project_id = ? AND status NOT IN ('cancelled', 'done', 'failed')`,
		now, now, id, pid)
	return err
}

func dbJobRuns(db *sql.DB, pid string, jobID int64, limit int) ([]*JobRun, error) {
	rows, err := db.Query(
		`SELECT id, job_id, started_at, COALESCE(finished_at,''),
			COALESCE(duration_ms,0), status, COALESCE(http_status,0),
			COALESCE(response_body,''), COALESCE(error,''), attempt
		 FROM job_runs WHERE project_id = ? AND job_id = ?
		 ORDER BY started_at DESC LIMIT ?`,
		pid, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*JobRun{}
	for rows.Next() {
		r := &JobRun{}
		if err := rows.Scan(&r.ID, &r.JobID, &r.StartedAt, &r.FinishedAt,
			&r.DurationMS, &r.Status, &r.HTTPStatus, &r.ResponseBody, &r.Error, &r.Attempt); err == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func dbRecentRuns(db *sql.DB, pid string, limit int) ([]*JobRun, error) {
	rows, err := db.Query(
		`SELECT id, job_id, started_at, COALESCE(finished_at,''),
			COALESCE(duration_ms,0), status, COALESCE(http_status,0),
			COALESCE(response_body,''), COALESCE(error,''), attempt
		 FROM job_runs WHERE project_id = ?
		 ORDER BY started_at DESC LIMIT ?`,
		pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*JobRun{}
	for rows.Next() {
		r := &JobRun{}
		if err := rows.Scan(&r.ID, &r.JobID, &r.StartedAt, &r.FinishedAt,
			&r.DurationMS, &r.Status, &r.HTTPStatus, &r.ResponseBody, &r.Error, &r.Attempt); err == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// ─── Dispatcher ────────────────────────────────────────────────────
//
// One tick per worker schedule (default 5s). Picks every pending job
// whose next_run_at is in the past, claims it (lease + status flip),
// dispatches its target, records a job_run row, then either
// reschedules (every/cron) or marks done/failed (once / retries
// exhausted).
//
// Single-replica today; the lease column is there so we can scale to
// N workers in the future without a migration. Lease TTL is short
// (60s) so a crashed dispatch reschedules itself quickly.

const (
	leaseTTL       = 60 * time.Second
	httpDispatchTimeout = 30 * time.Second
)

// dispatchClient is the HTTP client used for HTTP-target dispatch.
// Package-level so tests can substitute a stub via setDispatchClient.
var dispatchClient = &http.Client{Timeout: httpDispatchTimeout}
var dispatchClientMu sync.RWMutex

func setDispatchClient(c *http.Client) {
	dispatchClientMu.Lock()
	defer dispatchClientMu.Unlock()
	dispatchClient = c
}

func getDispatchClient() *http.Client {
	dispatchClientMu.RLock()
	defer dispatchClientMu.RUnlock()
	return dispatchClient
}

func dispatchTick(ctx context.Context, app *sdk.AppCtx) error {
	db := app.AppDB()
	if db == nil {
		return nil
	}
	now := time.Now().UTC()
	batch := atoiDefault(app.Config().Get("dispatch_batch_size"), 20, 200)

	// Claim due jobs in one query. Lease prevents the next tick from
	// re-picking the same row while we're working.
	leaseUntil := now.Add(leaseTTL).Format(time.RFC3339)
	res, err := db.Exec(
		`UPDATE jobs SET status = 'running', lease_until = ?, updated_at = ?
		 WHERE id IN (
			SELECT id FROM jobs
			WHERE status = 'pending' AND next_run_at <= ?
				AND (lease_until IS NULL OR lease_until < ?)
			ORDER BY next_run_at ASC LIMIT ?
		 )`,
		leaseUntil, now.Format(time.RFC3339),
		now.Format(time.RFC3339), now.Format(time.RFC3339),
		batch)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}

	// Reload claimed jobs.
	rows, err := db.Query(
		`SELECT id, project_id, name, COALESCE(owner_app,''), owner_instance,
			schedule_kind, COALESCE(cron_expr,''), every_seconds, run_at, timezone,
			target_json,
			COALESCE(idempotency_key,''), max_retries, backoff_seconds,
			status, next_run_at, last_run_at, COALESCE(last_status,''), COALESCE(last_error,''),
			attempt, created_at, updated_at, cancelled_at
		 FROM jobs WHERE status = 'running' AND lease_until = ?`,
		leaseUntil)
	if err != nil {
		return err
	}
	jobs := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err == nil {
			jobs = append(jobs, j)
		}
	}
	rows.Close()

	for _, j := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dispatchOne(ctx, app, j)
	}
	return nil
}

// dispatchOne runs a single job target, records a run row, then
// reschedules / retires the job. Errors here never propagate up — a
// failure in one job must not break the tick for siblings.
func dispatchOne(ctx context.Context, app *sdk.AppCtx, j *Job) {
	db := app.AppDB()
	attempt := j.Attempt + 1
	started := time.Now().UTC()

	status, httpCode, body, dispatchErr := runTarget(ctx, app, j)

	finished := time.Now().UTC()
	duration := finished.Sub(started).Milliseconds()
	errStr := ""
	if dispatchErr != nil {
		errStr = truncate(dispatchErr.Error(), 1024)
	}
	respBody := truncate(body, 2048)

	// Record the run row regardless of outcome.
	if _, err := db.Exec(
		`INSERT INTO job_runs (project_id, job_id, started_at, finished_at, duration_ms,
			status, http_status, response_body, error, attempt)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ProjectID, j.ID,
		started.Format(time.RFC3339Nano), finished.Format(time.RFC3339Nano), duration,
		status, httpCode, respBody, errStr, attempt); err != nil {
		app.Logger().Warn("record run", "job_id", j.ID, "err", err)
	}

	// Decide what's next.
	loc, _ := time.LoadLocation(j.Timezone)
	if loc == nil {
		loc = time.UTC
	}

	if status == "ok" {
		// Reset attempt counter and reschedule (or terminate for once).
		next := computeNextRunAfter(j, loc, finished)
		if next.IsZero() {
			db.Exec(
				`UPDATE jobs SET status = 'done', last_run_at = ?, last_status = 'ok',
					last_error = NULL, attempt = 0, lease_until = NULL,
					next_run_at = NULL, updated_at = ?
				 WHERE id = ?`,
				finished.Format(time.RFC3339), finished.Format(time.RFC3339), j.ID)
		} else {
			db.Exec(
				`UPDATE jobs SET status = 'pending', last_run_at = ?, last_status = 'ok',
					last_error = NULL, attempt = 0, lease_until = NULL,
					next_run_at = ?, updated_at = ?
				 WHERE id = ?`,
				finished.Format(time.RFC3339),
				next.Format(time.RFC3339), finished.Format(time.RFC3339), j.ID)
		}
		return
	}

	// Failure path. Retry up to max_retries with exponential backoff;
	// once exhausted, mark the job failed (or reschedule if it's a
	// recurring job — we don't want a transient outage to wipe out a
	// daily cron's future fires).
	if attempt <= j.MaxRetries {
		// Exponential: backoff_seconds * 2^(attempt-1).
		delay := time.Duration(j.BackoffSeconds) * time.Second
		for i := 1; i < attempt; i++ {
			delay *= 2
		}
		next := finished.Add(delay)
		db.Exec(
			`UPDATE jobs SET status = 'pending', last_run_at = ?, last_status = 'error',
				last_error = ?, attempt = ?, lease_until = NULL,
				next_run_at = ?, updated_at = ?
			 WHERE id = ?`,
			finished.Format(time.RFC3339), errStr, attempt,
			next.Format(time.RFC3339), finished.Format(time.RFC3339), j.ID)
		return
	}

	// Retries exhausted. Recurring jobs continue on their schedule
	// from the next due fire; one-shot jobs end as failed.
	if j.ScheduleKind == "once" {
		db.Exec(
			`UPDATE jobs SET status = 'failed', last_run_at = ?, last_status = 'error',
				last_error = ?, attempt = 0, lease_until = NULL,
				next_run_at = NULL, updated_at = ?
			 WHERE id = ?`,
			finished.Format(time.RFC3339), errStr,
			finished.Format(time.RFC3339), j.ID)
		return
	}
	next := computeNextRunAfter(j, loc, finished)
	if next.IsZero() {
		db.Exec(
			`UPDATE jobs SET status = 'failed', last_run_at = ?, last_status = 'error',
				last_error = ?, attempt = 0, lease_until = NULL,
				next_run_at = NULL, updated_at = ?
			 WHERE id = ?`,
			finished.Format(time.RFC3339), errStr,
			finished.Format(time.RFC3339), j.ID)
		return
	}
	db.Exec(
		`UPDATE jobs SET status = 'pending', last_run_at = ?, last_status = 'error',
			last_error = ?, attempt = 0, lease_until = NULL,
			next_run_at = ?, updated_at = ?
		 WHERE id = ?`,
		finished.Format(time.RFC3339), errStr,
		next.Format(time.RFC3339), finished.Format(time.RFC3339), j.ID)
}

// runTarget dispatches one job's target. Returns (status, http_code,
// body, err). status is "ok" / "error" / "timeout".
func runTarget(ctx context.Context, app *sdk.AppCtx, j *Job) (string, int, string, error) {
	switch strings.ToLower(strKey(j.Target, "kind")) {
	case "http":
		return runHTTPTarget(ctx, j)
	case "event":
		return runEventTarget(app, j)
	default:
		return "error", 0, "", fmt.Errorf("unknown target kind %q", strKey(j.Target, "kind"))
	}
}

// runHTTPTarget POSTs / GETs to the target URL. Two URL forms are
// supported: an absolute URL (gated by net.egress at the platform
// level) or app-relative {"app":"crm","path":"/cron/..."} which the
// platform routes through its own gateway.
func runHTTPTarget(ctx context.Context, j *Job) (string, int, string, error) {
	method := strings.ToUpper(strKey(j.Target, "method"))
	if method == "" {
		method = "POST"
	}
	url, err := resolveTargetURL(j.Target)
	if err != nil {
		return "error", 0, "", err
	}

	var body io.Reader
	if rawBody, ok := j.Target["body"]; ok && rawBody != nil {
		buf, err := json.Marshal(rawBody)
		if err != nil {
			return "error", 0, "", fmt.Errorf("encode body: %w", err)
		}
		body = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return "error", 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if j.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", j.IdempotencyKey)
	}
	req.Header.Set("X-Apteva-Job-ID", strconv.FormatInt(j.ID, 10))
	req.Header.Set("X-Apteva-Job-Attempt", strconv.Itoa(j.Attempt+1))
	// Inherit the install token so app-relative dispatches reach the
	// target sidecar with the platform's bearer.
	if t := os.Getenv("APTEVA_APP_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	if hdrs, ok := j.Target["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if s, ok := v.(string); ok {
				req.Header.Set(k, s)
			}
		}
	}

	resp, err := getDispatchClient().Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "timeout", 0, "", err
		}
		return "error", 0, "", err
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode/100 != 2 {
		return "error", resp.StatusCode, string(respBytes),
			fmt.Errorf("non-2xx: %d", resp.StatusCode)
	}
	return "ok", resp.StatusCode, string(respBytes), nil
}

// runEventTarget calls PlatformAPI.SendEvent. For unit tests where
// app.PlatformAPI() returns nil, treat as a no-op success — the test
// only cares that dispatch was attempted.
func runEventTarget(app *sdk.AppCtx, j *Job) (string, int, string, error) {
	instanceID := int64(toInt64(j.Target["instance_id"]))
	message := strKey(j.Target, "message")
	if instanceID == 0 || message == "" {
		return "error", 0, "", errors.New("event target requires instance_id and message")
	}
	if app.PlatformAPI() == nil {
		return "ok", 0, `{"sent":true,"mode":"test"}`, nil
	}
	if err := app.PlatformAPI().SendEvent(instanceID, message); err != nil {
		return "error", 0, "", err
	}
	return "ok", 0, "", nil
}

// resolveTargetURL builds the dispatch URL from a target spec. Either
// "url" (absolute) or {"app":"<slug>","path":"/..."} (relative,
// routed through the platform gateway).
func resolveTargetURL(target map[string]any) (string, error) {
	if u := strKey(target, "url"); u != "" {
		return u, nil
	}
	app := strKey(target, "app")
	path := strKey(target, "path")
	if app == "" || path == "" {
		return "", errors.New("http target needs either url or {app, path}")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	gateway := os.Getenv("APTEVA_GATEWAY_URL")
	if gateway == "" {
		return "", errors.New("APTEVA_GATEWAY_URL not set; cannot resolve app-relative target")
	}
	return strings.TrimRight(gateway, "/") + "/api/apps/" + app + path, nil
}

// ─── Schedule arithmetic ───────────────────────────────────────────

func computeNextRun(kind string, runAt time.Time, every *int64, cronExpr string, loc *time.Location, now time.Time) time.Time {
	switch kind {
	case "once":
		return runAt
	case "every":
		if every == nil || *every <= 0 {
			return time.Time{}
		}
		return now.Add(time.Duration(*every) * time.Second)
	case "cron":
		c, err := parseCron(cronExpr)
		if err != nil {
			return time.Time{}
		}
		return c.next(now.In(loc))
	}
	return time.Time{}
}

// computeNextRunAfter computes the next fire-time after the most
// recent run finished. Once-jobs return zero (no rescheduling).
func computeNextRunAfter(j *Job, loc *time.Location, after time.Time) time.Time {
	switch j.ScheduleKind {
	case "once":
		return time.Time{}
	case "every":
		if j.EverySeconds == nil {
			return time.Time{}
		}
		return after.Add(time.Duration(*j.EverySeconds) * time.Second)
	case "cron":
		c, err := parseCron(j.CronExpr)
		if err != nil {
			return time.Time{}
		}
		return c.next(after.In(loc))
	}
	return time.Time{}
}

// ─── Cron ──────────────────────────────────────────────────────────
//
// Compact 5-field cron: "minute hour day-of-month month day-of-week".
// Each field accepts:
//   *           any value
//   N           literal
//   N,M,...     comma list
//   A-B         range
//   */N         every N (any-step)
//   A-B/N       range with step
//
// Day-of-month / day-of-week are OR'd when both are constrained, like
// vixie cron.

type cronExpr struct {
	min, hour, dom, mon, dow []bool // bit-set per legal value
	domAny, dowAny           bool   // both wildcards → no DOW/DOM constraint
}

var cronRanges = []struct {
	lo, hi int
}{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // dom
	{1, 12}, // mon
	{0, 6},  // dow (0=Sun)
}

func parseCron(expr string) (*cronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("expected 5 fields, got %d", len(fields))
	}
	out := &cronExpr{}
	sets := make([][]bool, 5)
	for i, f := range fields {
		s, err := parseCronField(f, cronRanges[i].lo, cronRanges[i].hi)
		if err != nil {
			return nil, fmt.Errorf("field %d (%q): %w", i+1, f, err)
		}
		sets[i] = s
	}
	out.min, out.hour, out.dom, out.mon, out.dow = sets[0], sets[1], sets[2], sets[3], sets[4]
	out.domAny = fields[2] == "*"
	out.dowAny = fields[4] == "*"
	return out, nil
}

func parseCronField(f string, lo, hi int) ([]bool, error) {
	out := make([]bool, hi+1)
	for _, part := range strings.Split(f, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			s, err := strconv.Atoi(part[i+1:])
			if err != nil || s < 1 {
				return nil, fmt.Errorf("invalid step %q", part)
			}
			step = s
			part = part[:i]
		}
		var a, b int
		if part == "*" {
			a, b = lo, hi
		} else if i := strings.Index(part, "-"); i >= 0 {
			x, err := strconv.Atoi(part[:i])
			if err != nil {
				return nil, err
			}
			y, err := strconv.Atoi(part[i+1:])
			if err != nil {
				return nil, err
			}
			a, b = x, y
		} else {
			x, err := strconv.Atoi(part)
			if err != nil {
				return nil, err
			}
			a, b = x, x
		}
		if a < lo || b > hi || a > b {
			return nil, fmt.Errorf("out of range %d-%d (allowed %d-%d)", a, b, lo, hi)
		}
		for v := a; v <= b; v += step {
			out[v] = true
		}
	}
	return out, nil
}

// next finds the smallest minute-precision time strictly greater than
// `from` that satisfies the cron expression. Capped at +366 days so
// an impossible expression terminates instead of looping forever.
func (c *cronExpr) next(from time.Time) time.Time {
	t := from.Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(366 * 24 * time.Hour)
	for t.Before(limit) {
		if !c.mon[int(t.Month())] {
			// Jump to the first day of the next month.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !c.matchDay(t) {
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).Add(24 * time.Hour)
			continue
		}
		if !c.hour[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !c.min[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

func (c *cronExpr) matchDay(t time.Time) bool {
	dom := c.dom[t.Day()]
	dow := c.dow[int(t.Weekday())]
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dow
	case c.dowAny:
		return dom
	default:
		// Vixie semantics: OR.
		return dom || dow
	}
}

// ─── Target validation ─────────────────────────────────────────────

func validateTarget(t map[string]any) error {
	kind := strings.ToLower(strKey(t, "kind"))
	switch kind {
	case "http":
		if _, err := resolveTargetURL(t); err != nil {
			// resolveTargetURL needs APTEVA_GATEWAY_URL when using
			// app-relative form; we don't fail hard on that here —
			// the dispatcher does. We only reject if both url and
			// app/path are missing.
			if strKey(t, "url") == "" && (strKey(t, "app") == "" || strKey(t, "path") == "") {
				return errors.New("http target needs url or {app, path}")
			}
		}
		return nil
	case "event":
		if toInt64(t["instance_id"]) == 0 || strKey(t, "message") == "" {
			return errors.New("event target needs instance_id and message")
		}
		return nil
	default:
		return fmt.Errorf("target.kind %q must be http or event", kind)
	}
}

// ─── Tiny utils ─────────────────────────────────────────────────────

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	if v, ok := args[key].(int); ok {
		return v
	}
	if v, ok := args[key].(int64); ok {
		return int(v)
	}
	if s, ok := args[key].(string); ok && s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func strKey(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullableInt64(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: v != 0}
}

func nullableInt64Ptr(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func nullableTime(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(time.RFC3339), Valid: true}
}

func parseTime(s string, loc *time.Location) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised time format %q", s)
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func atoiDefault(s string, def, max int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	if max > 0 && n > max {
		return max
	}
	return n
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		// Sort for deterministic JSON output (helps assertions).
		sort.Strings(required)
		out["required"] = required
	}
	return out
}

// ─── HTTP utilities ────────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// getAppCtx fetches the AppCtx the SDK threaded into the request via
// a stable global. The SDK does not currently expose a public hook
// for HTTP handlers; we keep a pointer wired up at OnMount.
var globalCtx *sdk.AppCtx

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }
