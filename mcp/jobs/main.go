// Jobs v0.1 — scheduled-job runner.
//
// Other apps and agents enqueue work to be delivered later: at a fixed
// time, on an interval, on a cron expression, or at deterministic
// randomized times within a local-day window. Targets are sibling
// app tools, external HTTP endpoints, or instance events. Jobs never
// knows what the work is; it only knows how to deliver the payload.
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
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
version: 0.1.13
description: |
  Scheduled-job runner. Other apps and agents enqueue work; jobs
  delivers it later via platform-mediated app tools, external HTTP,
  or instance events.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.apps.call
    - platform.instances.read
    - platform.instances.write
  dynamic_app_calls: true
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: jobs_schedule
      description: Schedule a job with an app-tool, external HTTP, or event target.
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
    - name: jobs_preview
      description: Preview deterministic randomized schedule occurrences.
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
config_schema:
  - name: history_retention_days
    type: text
    default: "30"
    label: Run history retention (days)
    description: How long to keep entries in job_runs before pruning. 0 disables pruning.
  - name: dispatch_batch_size
    type: text
    default: "20"
    label: Dispatch batch size
    description: Maximum jobs claimed per ticker tick. Tune up for very high schedule density.
  - name: dispatch_concurrency
    type: text
    default: "8"
    label: Dispatch concurrency
    description: Maximum jobs executed concurrently within one dispatcher tick. Max 50.
  - name: http_dispatch_timeout_seconds
    type: text
    default: "180"
    label: HTTP dispatch timeout (seconds)
    description: Default deadline for HTTP target calls. Per-job targets can override with timeout_seconds or timeout_ms. Max 300 seconds.
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
		{Pattern: "/preview", Handler: a.handleHTTPPreview},
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
	normalizeTargetArgs(body, 0)
	job, err := dbScheduleJob(ctx.AppDB(), pid, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitJob(ctx, "job.scheduled", job)
	httpJSON(w, map[string]any{"job": job})
}

func (a *App) handleHTTPPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	out, err := buildSchedulePreview(body, time.Now().UTC())
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
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
	if ctx != nil {
		ctx.Emit("job.cancelled", map[string]any{"id": id})
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
	if ctx != nil {
		ctx.Emit("job.queued", map[string]any{"id": id})
	}
	httpJSON(w, map[string]any{"queued": true, "id": id})
}

// ─── MCP tools (the agent's surface) ───────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "jobs_schedule",
			Description: "Schedule a job to run later (once, on an interval, on a cron expression, or at deterministic random times in a daily window). Use target.kind=event to wake an agent, target.kind=app_tool with {app, tool, input?} to invoke a sibling app through the platform, or target.kind=http with url=<absolute> for an external webhook. Functions use app_tool with app=functions, tool=functions_invoke, input={name,event}.",
			InputSchema: schemaObject(map[string]any{
				"name": map[string]any{"type": "string", "description": "Human-readable job name."},
				"schedule": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind":                map[string]any{"type": "string", "enum": []any{"once", "every", "cron", "random"}},
						"run_at":              map[string]any{"type": "string", "description": "RFC3339 timestamp; required when kind=once."},
						"every_seconds":       map[string]any{"type": "integer", "description": "Interval in seconds; required when kind=every."},
						"cron":                map[string]any{"type": "string", "description": "5-field cron 'M H DOM MON DOW'; required when kind=cron."},
						"period":              map[string]any{"type": "string", "enum": []any{"day"}, "description": "Calendar period for kind=random. Currently day."},
						"runs_per_period":     map[string]any{"type": "integer", "description": "Number of randomized runs in each period."},
						"window_start":        map[string]any{"type": "string", "description": "Inclusive local-time window start in HH:MM."},
						"window_end":          map[string]any{"type": "string", "description": "Inclusive local-time window end in HH:MM."},
						"min_spacing_minutes": map[string]any{"type": "integer", "description": "Minimum minutes between randomized runs."},
					},
					"required": []any{"kind"},
				},
				"target": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind":     map[string]any{"type": "string", "enum": []any{"event", "http", "app_tool"}},
						"agent_id": map[string]any{"description": "For kind=event: numeric agent id (instance) to wake. Pass 'self' to target the calling agent."},
						"message":  map[string]any{"type": "string", "description": "For kind=event: the message to deliver."},
						"url":      map[string]any{"type": "string", "description": "For kind=http: absolute URL (requires net.egress)."},
						"app":      map[string]any{"type": "string", "description": "For kind=app_tool: target app slug."},
						"tool":     map[string]any{"type": "string", "description": "For kind=app_tool: MCP tool name."},
						"input":    map[string]any{"description": "For kind=app_tool: tool input object."},
						"method":   map[string]any{"type": "string", "description": "For kind=http: HTTP method (default POST)."},
						"body":     map[string]any{"description": "For kind=http: JSON body to POST."},
						"headers":  map[string]any{"type": "object", "description": "For kind=http: explicit request headers. Platform credentials are never added."},
						"timeout_seconds": map[string]any{
							"type":        "integer",
							"description": "For kind=http: request timeout in seconds. Default comes from app config, max 300.",
						},
						"timeout_ms": map[string]any{
							"type":        "integer",
							"description": "For kind=http: request timeout in milliseconds. Takes precedence over timeout_seconds, max 300000.",
						},
					},
					"required": []any{"kind"},
				},
				"idempotency_key": map[string]any{"type": "string"},
				"max_retries":     map[string]any{"type": "integer"},
				"backoff_seconds": map[string]any{"type": "integer"},
				"timezone":        map[string]any{"type": "string", "description": "IANA tz name; cron is evaluated in this tz. Default UTC."},
				"owner_app":       map[string]any{"type": "string"},
				"schedule_seed":   map[string]any{"type": "string", "description": "Optional seed returned by jobs_preview so creation matches the preview."},
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
		{
			Name:        "jobs_preview",
			Description: "Preview the next randomized schedule occurrences. Returns runs and schedule_seed; pass the seed to jobs_schedule to preserve the preview.",
			InputSchema: schemaObject(map[string]any{
				"schedule":      map[string]any{"type": "object"},
				"timezone":      map[string]any{"type": "string"},
				"schedule_seed": map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
			}, []string{"schedule"}),
			Handler: a.toolPreview,
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
	// Explicit _project_id wins; empty string is a valid value —
	// global-scope callers (like backup) intentionally pass "" to
	// record a project-less job. Distinguish present-with-empty
	// from absent by checking the map directly, not just the value.
	if raw, has := args["_project_id"]; has {
		if v, ok := raw.(string); ok {
			return v, nil
		}
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
	ID             int64           `json:"id"`
	ProjectID      string          `json:"project_id,omitempty"`
	Name           string          `json:"name"`
	OwnerApp       string          `json:"owner_app,omitempty"`
	OwnerInstance  *int64          `json:"owner_instance,omitempty"`
	ScheduleKind   string          `json:"schedule_kind"`
	CronExpr       string          `json:"cron_expr,omitempty"`
	EverySeconds   *int64          `json:"every_seconds,omitempty"`
	RunAt          string          `json:"run_at,omitempty"`
	Timezone       string          `json:"timezone"`
	Random         *RandomSchedule `json:"random,omitempty"`
	Target         map[string]any  `json:"target"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	MaxRetries     int             `json:"max_retries"`
	BackoffSeconds int             `json:"backoff_seconds"`
	Status         string          `json:"status"`
	NextRunAt      string          `json:"next_run_at,omitempty"`
	ScheduledFor   string          `json:"scheduled_for,omitempty"`
	LastRunAt      string          `json:"last_run_at,omitempty"`
	LastStatus     string          `json:"last_status,omitempty"`
	LastError      string          `json:"last_error,omitempty"`
	Attempt        int             `json:"attempt"`
	CreatedAt      string          `json:"created_at,omitempty"`
	UpdatedAt      string          `json:"updated_at,omitempty"`
	CancelledAt    string          `json:"cancelled_at,omitempty"`
	LeaseToken     string          `json:"-"`
	ScheduleSeed   string          `json:"-"`
}

type JobRun struct {
	ID             int64  `json:"id"`
	JobID          int64  `json:"job_id"`
	StartedAt      string `json:"started_at"`
	FinishedAt     string `json:"finished_at,omitempty"`
	DurationMS     int64  `json:"duration_ms"`
	Status         string `json:"status"`
	HTTPStatus     int    `json:"http_status,omitempty"`
	ResponseBody   string `json:"response_body,omitempty"`
	Error          string `json:"error,omitempty"`
	Attempt        int    `json:"attempt"`
	ScheduledFor   string `json:"scheduled_for,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
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
	normalizeTargetArgs(args, callerInstance)
	job, err := dbScheduleJob(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	emitJob(ctx, "job.scheduled", job)
	return map[string]any{"job": job}, nil
}

// normalizeTargetArgs translates the LLM/UI-facing event target into the
// stable wire shape shared by MCP and HTTP scheduling.
func normalizeTargetArgs(args map[string]any, callerInstance int64) {
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

func (a *App) toolPreview(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return buildSchedulePreview(args, time.Now().UTC())
}

func buildSchedulePreview(args map[string]any, now time.Time) (map[string]any, error) {
	schedule, _ := args["schedule"].(map[string]any)
	if schedule == nil {
		return nil, errors.New("schedule required")
	}
	if kind := strings.ToLower(strArg(schedule, "kind")); kind != "random" {
		return nil, fmt.Errorf("schedule.kind %q cannot be previewed; use random", kind)
	}
	tz := strArg(args, "timezone")
	if tz == "" {
		tz = "UTC"
	}
	runs, seed, err := previewRandomSchedule(schedule, tz, strArg(args, "schedule_seed"), now, intArg(args, "limit", 5))
	if err != nil {
		return nil, err
	}
	return map[string]any{"runs": runs, "schedule_seed": seed, "timezone": tz}, nil
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
		randomConfig *RandomSchedule
		scheduleSeed string
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

	case "random":
		cfg, err := parseRandomSchedule(schedule)
		if err != nil {
			return nil, err
		}
		randomConfig = &cfg
		scheduleSeed = strings.TrimSpace(strArg(args, "schedule_seed"))
		if scheduleSeed == "" {
			scheduleSeed, err = newRandomScheduleSeed()
			if err != nil {
				return nil, fmt.Errorf("generate schedule seed: %w", err)
			}
		} else if err := validateRandomScheduleSeed(scheduleSeed); err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("schedule.kind %q must be once|every|cron|random", kind)
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

	// First fire-time. Existing schedule kinds retain their established
	// arithmetic; random schedules advance through logical occurrences.
	next := computeNextRun(kind, runAt, everySeconds, cronExpr, loc, now)
	if randomConfig != nil {
		next, err = nextRandomRunAfter(*randomConfig, scheduleSeed, loc, now)
		if err != nil {
			return nil, err
		}
	}
	var randomConfigJSON string
	if randomConfig != nil {
		encoded, err := json.Marshal(randomConfig)
		if err != nil {
			return nil, err
		}
		randomConfigJSON = string(encoded)
	}

	res, err := db.Exec(
		`INSERT INTO jobs (
			project_id, name, owner_app, owner_instance,
			schedule_kind, cron_expr, every_seconds, run_at, timezone, random_config_json, schedule_seed,
			target_kind, target_json,
			idempotency_key, max_retries, backoff_seconds,
			status, next_run_at, scheduled_for,
			created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?)`,
		pid, name, strArg(args, "owner_app"), nullableInt64(int64Arg(args, "owner_instance")),
		kind, nullStr(cronExpr), nullableInt64Ptr(everySeconds), nullableTime(runAt), tz, nullStr(randomConfigJSON), nullStr(scheduleSeed),
		targetKind, string(targetJSON),
		nullStr(strArg(args, "idempotency_key")), maxRetries, backoff,
		nullableTime(next), nullableTime(next), now.Format(time.RFC3339), now.Format(time.RFC3339))
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
			COALESCE(random_config_json,''), COALESCE(schedule_seed,''),
			target_json,
			COALESCE(idempotency_key,''), max_retries, backoff_seconds,
			status, next_run_at, scheduled_for, last_run_at, COALESCE(last_status,''), COALESCE(last_error,''),
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
	var runAt, nextRun, scheduledFor, lastRun, cancelledAt sql.NullString
	var targetJSON, randomConfigJSON string
	err := row.Scan(
		&j.ID, &j.ProjectID, &j.Name, &j.OwnerApp, &ownerInst,
		&j.ScheduleKind, &j.CronExpr, &everySecs, &runAt, &j.Timezone,
		&randomConfigJSON, &j.ScheduleSeed,
		&targetJSON,
		&j.IdempotencyKey, &j.MaxRetries, &j.BackoffSeconds,
		&j.Status, &nextRun, &scheduledFor, &lastRun, &j.LastStatus, &j.LastError,
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
	j.ScheduledFor = scheduledFor.String
	j.LastRunAt = lastRun.String
	j.CancelledAt = cancelledAt.String
	if targetJSON != "" {
		_ = json.Unmarshal([]byte(targetJSON), &j.Target)
	}
	if randomConfigJSON != "" {
		j.Random = &RandomSchedule{}
		if err := json.Unmarshal([]byte(randomConfigJSON), j.Random); err != nil {
			return nil, fmt.Errorf("decode random schedule for job %d: %w", j.ID, err)
		}
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
			COALESCE(random_config_json,''), COALESCE(schedule_seed,''),
			target_json,
			COALESCE(idempotency_key,''), max_retries, backoff_seconds,
			status, next_run_at, scheduled_for, last_run_at, COALESCE(last_status,''), COALESCE(last_error,''),
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
	res, err := db.Exec(
		`UPDATE jobs SET next_run_at = ?, updated_at = ?
		 WHERE id = ? AND project_id = ? AND status = 'pending'`,
		now, now, id, pid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM jobs WHERE id = ? AND project_id = ?`, id, pid).Scan(&status); err == sql.ErrNoRows {
		return errors.New("job not found")
	} else if err != nil {
		return err
	}
	if status == "running" {
		return errors.New("job is already running")
	}
	return fmt.Errorf("cannot run job with terminal status %q", status)
}

func dbJobRuns(db *sql.DB, pid string, jobID int64, limit int) ([]*JobRun, error) {
	rows, err := db.Query(
		`SELECT id, job_id, started_at, COALESCE(finished_at,''),
			COALESCE(duration_ms,0), status, COALESCE(http_status,0),
			COALESCE(response_body,''), COALESCE(error,''), attempt,
			COALESCE(scheduled_for,''), COALESCE(idempotency_key,'')
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
			&r.DurationMS, &r.Status, &r.HTTPStatus, &r.ResponseBody, &r.Error, &r.Attempt,
			&r.ScheduledFor, &r.IdempotencyKey); err == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func dbRecentRuns(db *sql.DB, pid string, limit int) ([]*JobRun, error) {
	rows, err := db.Query(
		`SELECT id, job_id, started_at, COALESCE(finished_at,''),
			COALESCE(duration_ms,0), status, COALESCE(http_status,0),
			COALESCE(response_body,''), COALESCE(error,''), attempt,
			COALESCE(scheduled_for,''), COALESCE(idempotency_key,'')
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
			&r.DurationMS, &r.Status, &r.HTTPStatus, &r.ResponseBody, &r.Error, &r.Attempt,
			&r.ScheduledFor, &r.IdempotencyKey); err == nil {
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
// Lease tokens make claims safe across replicas and let a later worker
// reclaim rows whose prior owner crashed. The TTL exceeds every supported
// HTTP/function timeout.

const (
	leaseTTL                   = 10 * time.Minute
	defaultHTTPDispatchTimeout = 180 * time.Second
	minHTTPDispatchTimeout     = 1 * time.Millisecond
	maxHTTPDispatchTimeout     = 300 * time.Second
)

// dispatchClient is the HTTP client used for HTTP-target dispatch.
// Package-level so tests can substitute a stub via setDispatchClient.
var dispatchClient = &http.Client{}
var dispatchClientMu sync.RWMutex
var retentionMu sync.Mutex
var retentionLastRun = map[string]time.Time{}

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
	pid := app.CurrentProject()
	batch := atoiDefault(app.Config().Get("dispatch_batch_size"), 20, 200)
	concurrency := atoiDefault(app.Config().Get("dispatch_concurrency"), 8, 50)
	if err := maybePruneRunHistory(db, pid, app.Config(), now); err != nil {
		app.Logger().Warn("prune run history", "project_id", pid, "err", err)
	}

	// A unique token distinguishes this claim from every other worker's
	// claim, even when their lease timestamps happen to match.
	leaseToken, err := newLeaseToken()
	if err != nil {
		return fmt.Errorf("lease token: %w", err)
	}
	leaseUntil := now.Add(leaseTTL).Format(time.RFC3339)
	res, err := db.Exec(
		`UPDATE jobs SET status = 'running', lease_until = ?, lease_token = ?, updated_at = ?
		 WHERE id IN (
			SELECT id FROM jobs
			WHERE project_id = ? AND (
				(status = 'pending' AND next_run_at <= ?)
				OR (status = 'running' AND (lease_until IS NULL OR lease_until < ?))
			)
			ORDER BY next_run_at ASC, id ASC LIMIT ?
		 )`,
		leaseUntil, leaseToken, now.Format(time.RFC3339),
		pid, now.Format(time.RFC3339), now.Format(time.RFC3339), batch)
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
			COALESCE(random_config_json,''), COALESCE(schedule_seed,''),
			target_json,
			COALESCE(idempotency_key,''), max_retries, backoff_seconds,
			status, next_run_at, scheduled_for, last_run_at, COALESCE(last_status,''), COALESCE(last_error,''),
			attempt, created_at, updated_at, cancelled_at
		 FROM jobs WHERE project_id = ? AND status = 'running' AND lease_token = ?`,
		pid, leaseToken)
	if err != nil {
		return err
	}
	jobs := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err == nil {
			j.LeaseToken = leaseToken
			jobs = append(jobs, j)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(job *Job) {
			defer wg.Done()
			defer func() { <-sem }()
			dispatchOne(ctx, app, job)
		}(j)
	}
	wg.Wait()
	return ctx.Err()
}

// dispatchOne runs a single job target, records a run row, then
// reschedules / retires the job. Errors here never propagate up — a
// failure in one job must not break the tick for siblings.
func dispatchOne(ctx context.Context, app *sdk.AppCtx, j *Job) {
	db := app.AppDB()
	attempt := j.Attempt + 1
	started := time.Now().UTC()
	scheduledTime := jobScheduledTime(j, started)
	j.ScheduledFor = scheduledTime.Format(time.RFC3339)
	effectiveIdempotencyKey := occurrenceIdempotencyKey(j)

	status, httpCode, body, dispatchErr := runTarget(ctx, app, j)

	finished := time.Now().UTC()
	duration := finished.Sub(started).Milliseconds()
	errStr := ""
	if dispatchErr != nil {
		errStr = truncate(dispatchErr.Error(), 1024)
	}
	respBody := truncate(body, 2048)

	loc, _ := time.LoadLocation(j.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	nextStatus := "pending"
	nextRun := time.Time{}
	nextScheduled := scheduledTime
	nextAttempt := 0
	lastStatus := "error"
	if status == "ok" {
		lastStatus = "ok"
		if j.ScheduleKind == "random" {
			nextRun, dispatchErr = computeNextRandomOccurrence(j, loc, scheduledTime)
		} else {
			// Preserve the established once/every/cron cadence semantics.
			nextRun = computeNextRunAfter(j, loc, finished)
		}
		if nextRun.IsZero() {
			if dispatchErr != nil {
				nextStatus = "failed"
			} else {
				nextStatus = "done"
			}
		} else {
			nextScheduled = nextRun
		}
	} else if attempt <= j.MaxRetries {
		delay := time.Duration(j.BackoffSeconds) * time.Second
		for i := 1; i < attempt; i++ {
			delay *= 2
		}
		nextRun = finished.Add(delay)
		nextAttempt = attempt
	} else if j.ScheduleKind == "once" {
		nextStatus = "failed"
	} else {
		if j.ScheduleKind == "random" {
			nextRun, dispatchErr = computeNextRandomOccurrence(j, loc, scheduledTime)
		} else {
			nextRun = computeNextRunAfter(j, loc, finished)
		}
		if nextRun.IsZero() {
			nextStatus = "failed"
		} else {
			nextScheduled = nextRun
		}
	}
	if dispatchErr != nil && errStr == "" {
		errStr = truncate(dispatchErr.Error(), 1024)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		app.Logger().Warn("begin run transaction", "job_id", j.ID, "err", err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO job_runs (project_id, job_id, started_at, finished_at, duration_ms,
			status, http_status, response_body, error, attempt, scheduled_for, idempotency_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ProjectID, j.ID,
		started.Format(time.RFC3339Nano), finished.Format(time.RFC3339Nano), duration,
		status, httpCode, respBody, errStr, attempt, j.ScheduledFor, nullStr(effectiveIdempotencyKey)); err != nil {
		app.Logger().Warn("record run", "job_id", j.ID, "err", err)
		return
	}
	var nextValue any
	if !nextRun.IsZero() {
		nextValue = nextRun.Format(time.RFC3339)
	}
	var lastError any
	if errStr != "" {
		lastError = errStr
	}
	res, err := tx.Exec(
		`UPDATE jobs SET status = ?, last_run_at = ?, last_status = ?,
			last_error = ?, attempt = ?, lease_until = NULL, lease_token = NULL,
			next_run_at = ?, scheduled_for = ?, updated_at = ?
		 WHERE id = ? AND project_id = ? AND status = 'running' AND lease_token = ?`,
		nextStatus, finished.Format(time.RFC3339Nano), lastStatus,
		lastError, nextAttempt, nextValue, nextScheduled.Format(time.RFC3339), finished.Format(time.RFC3339Nano),
		j.ID, j.ProjectID, j.LeaseToken)
	if err != nil {
		app.Logger().Warn("finalize run", "job_id", j.ID, "err", err)
		return
	}
	if err := tx.Commit(); err != nil {
		app.Logger().Warn("commit run", "job_id", j.ID, "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		app.Emit("job.updated", map[string]any{"id": j.ID, "status": nextStatus, "last_status": lastStatus})
	}
}

func jobScheduledTime(j *Job, fallback time.Time) time.Time {
	for _, raw := range []string{j.ScheduledFor, j.NextRunAt} {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return parsed
		}
	}
	return fallback
}

func computeNextRandomOccurrence(j *Job, loc *time.Location, after time.Time) (time.Time, error) {
	if j.Random == nil {
		return time.Time{}, errors.New("random job is missing its schedule configuration")
	}
	return nextRandomRunAfter(*j.Random, j.ScheduleSeed, loc, after)
}

func occurrenceIdempotencyKey(j *Job) string {
	if j == nil || j.IdempotencyKey == "" || j.ScheduledFor == "" {
		return ""
	}
	return j.IdempotencyKey + ":" + j.ScheduledFor
}

func newLeaseToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func pruneRunHistory(db *sql.DB, pid string, cfg sdk.Config, now time.Time) error {
	days := 30
	if raw := strings.TrimSpace(cfg.Get("history_retention_days")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("history_retention_days: %w", err)
		}
		if n <= 0 {
			return nil
		}
		days = n
	}
	_, err := db.Exec(`DELETE FROM job_runs WHERE project_id = ? AND started_at < ?`,
		pid, now.AddDate(0, 0, -days).Format(time.RFC3339Nano))
	return err
}

func maybePruneRunHistory(db *sql.DB, pid string, cfg sdk.Config, now time.Time) error {
	retentionMu.Lock()
	last := retentionLastRun[pid]
	if now.Sub(last) < time.Hour {
		retentionMu.Unlock()
		return nil
	}
	retentionLastRun[pid] = now
	retentionMu.Unlock()
	if err := pruneRunHistory(db, pid, cfg, now); err != nil {
		retentionMu.Lock()
		delete(retentionLastRun, pid)
		retentionMu.Unlock()
		return err
	}
	return nil
}

// runTarget dispatches one job's target. Returns (status, http_code,
// body, err). status is "ok" / "error" / "timeout".
func runTarget(ctx context.Context, app *sdk.AppCtx, j *Job) (string, int, string, error) {
	switch strings.ToLower(strKey(j.Target, "kind")) {
	case "http":
		if target, ok := legacyFunctionsTarget(j.Target); ok {
			return runAppToolTarget(app, j, target)
		}
		return runHTTPTarget(ctx, j, app.Config())
	case "app_tool":
		return runAppToolTarget(app, j, j.Target)
	case "event":
		return runEventTarget(app, j)
	default:
		return "error", 0, "", fmt.Errorf("unknown target kind %q", strKey(j.Target, "kind"))
	}
}

// runHTTPTarget sends to an absolute external URL gated by net.egress.
// Sibling apps use runAppToolTarget so the platform can authorize them.
func runHTTPTarget(ctx context.Context, j *Job, cfg sdk.Config) (string, int, string, error) {
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

	timeout := httpTargetTimeout(j.Target, cfg)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, url, body)
	if err != nil {
		return "error", 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if hdrs, ok := j.Target["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if s, ok := v.(string); ok {
				req.Header.Set(k, s)
			}
		}
	}
	if key := occurrenceIdempotencyKey(j); key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	req.Header.Set("X-Apteva-Job-ID", strconv.FormatInt(j.ID, 10))
	req.Header.Set("X-Apteva-Job-Attempt", strconv.Itoa(j.Attempt+1))
	if j.ScheduledFor != "" {
		req.Header.Set("X-Apteva-Job-Scheduled-For", j.ScheduledFor)
	}

	resp, err := getDispatchClient().Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "timeout", 0, "", err
		}
		return "error", 0, "", err
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode/100 != 2 {
		return "error", resp.StatusCode, string(respBytes),
			fmt.Errorf("non-2xx: %d", resp.StatusCode)
	}
	return "ok", resp.StatusCode, string(respBytes), nil
}

func runAppToolTarget(app *sdk.AppCtx, job *Job, target map[string]any) (string, int, string, error) {
	appName := strKey(target, "app")
	tool := strKey(target, "tool")
	storedInput, _ := target["input"].(map[string]any)
	input := make(map[string]any, len(storedInput)+1)
	for key, value := range storedInput {
		input[key] = value
	}
	metadata := map[string]any{
		"id":            job.ID,
		"scheduled_for": job.ScheduledFor,
		"attempt":       job.Attempt + 1,
	}
	if key := occurrenceIdempotencyKey(job); key != "" {
		metadata["idempotency_key"] = key
	}
	input["_job"] = metadata
	if app == nil || app.PlatformAPI() == nil {
		return "error", 0, "", errors.New("app_tool target requires platform API")
	}
	var out any
	if err := app.PlatformAPI().CallAppResult(appName, tool, input, &out); err != nil {
		return "error", 0, "", err
	}
	body, _ := json.Marshal(out)
	if result, ok := out.(map[string]any); ok {
		switch strings.ToLower(strKey(result, "status")) {
		case "error", "failed", "timeout":
			msg := strKey(result, "error")
			if msg == "" {
				msg = "app tool returned status " + strKey(result, "status")
			}
			return "error", 0, string(body), errors.New(msg)
		}
	}
	return "ok", 0, string(body), nil
}

// Existing Jobs versions documented Functions as an app-relative HTTP route.
// Preserve those stored schedules while routing them through the platform's
// authorized app-call broker.
func legacyFunctionsTarget(target map[string]any) (map[string]any, bool) {
	if !strings.EqualFold(strKey(target, "app"), "functions") {
		return nil, false
	}
	rawPath := strKey(target, "path")
	if !strings.HasPrefix(rawPath, "/fn/") {
		return nil, false
	}
	path := strings.TrimPrefix(rawPath, "/fn/")
	if path == "" || strings.Contains(path, "/") {
		return nil, false
	}
	return map[string]any{
		"kind": "app_tool",
		"app":  "functions",
		"tool": "functions_invoke",
		"input": map[string]any{
			"name":  path,
			"event": target["body"],
		},
	}, true
}

func httpTargetTimeout(target map[string]any, cfg sdk.Config) time.Duration {
	timeout := defaultHTTPDispatchTimeout
	if sec := atoiDefault(cfg.Get("http_dispatch_timeout_seconds"), int(defaultHTTPDispatchTimeout/time.Second), int(maxHTTPDispatchTimeout/time.Second)); sec > 0 {
		timeout = time.Duration(sec) * time.Second
	}
	if sec := intArg(target, "timeout_seconds", 0); sec > 0 {
		timeout = time.Duration(sec) * time.Second
	}
	if ms := intArg(target, "timeout_ms", 0); ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	if timeout < minHTTPDispatchTimeout {
		return minHTTPDispatchTimeout
	}
	if timeout > maxHTTPDispatchTimeout {
		return maxHTTPDispatchTimeout
	}
	return timeout
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

// resolveTargetURL accepts external HTTP(S) URLs only. Cross-app calls must
// use app_tool so authorization stays inside the platform broker.
func resolveTargetURL(target map[string]any) (string, error) {
	raw := strKey(target, "url")
	if raw == "" {
		return "", errors.New("http target requires an absolute url; use app_tool for sibling apps")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("http target url must be an absolute http or https URL")
	}
	return u.String(), nil
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
		if _, ok := legacyFunctionsTarget(t); ok {
			return nil
		}
		_, err := resolveTargetURL(t)
		return err
	case "app_tool":
		if strKey(t, "app") == "" || strKey(t, "tool") == "" {
			return errors.New("app_tool target needs app and tool")
		}
		if input, ok := t["input"]; ok && input != nil {
			if _, ok := input.(map[string]any); !ok {
				return errors.New("app_tool target input must be an object")
			}
		}
		return nil
	case "event":
		if toInt64(t["instance_id"]) == 0 || strKey(t, "message") == "" {
			return errors.New("event target needs instance_id and message")
		}
		return nil
	default:
		return fmt.Errorf("target.kind %q must be http, app_tool, or event", kind)
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
