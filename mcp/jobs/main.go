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
// An installation-wide dispatcher fills a bounded worker pool; renewable
// SQLite claims coordinate replicas and allow recovery after process failure.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
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

//go:embed apteva.yaml
var manifestYAML string

type App struct {
	dispatcher *dispatcher
	requestCtx *sdk.AppCtx
}

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
	a.requestCtx = ctx
	a.dispatcher = newDispatcher(ctx)
	a.dispatcher.start()
	ctx.Logger().Info("jobs mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.dispatcher != nil {
		a.dispatcher.stop()
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── Workers — the dispatcher tick. ────────────────────────────────

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "dispatcher",
			Schedule: "@every 5s",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				if a.dispatcher != nil {
					a.dispatcher.wake()
				}
				return nil
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
		{Pattern: "/scope", Handler: a.handleHTTPScope},
		{Pattern: "/status", Handler: a.handleHTTPStatus},
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
	if len(parts) != 1 {
		httpErr(w, http.StatusNotFound, "unknown job route")
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
	if err := validateListQuery(r, 200); err != nil {
		writeError(w, err)
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := a.httpContext(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 50, 200)
	rows, err := dbRecentRunsContext(r.Context(), ctx.AppDB(), pid, limit+1, parseInt64(r.URL.Query().Get("before")))
	if err != nil {
		writeError(w, err)
		return
	}
	httpJSON(w, runsPage(rows, limit))
}

func (a *App) handleHTTPCreate(w http.ResponseWriter, r *http.Request) {
	ctx := a.httpContext(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body map[string]any
	if err := decodeRequest(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	delete(body, "owner_instance")
	delete(body, "owner_app")
	if err := normalizeTargetArgs(body, 0); err != nil {
		writeError(w, err)
		return
	}
	job, err := dbScheduleJob(ctx.AppDB(), pid, body, r.Context())
	if err != nil {
		writeError(w, err)
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
	if err := decodeRequest(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	out, err := buildSchedulePreview(body, time.Now().UTC())
	if err != nil {
		writeError(w, err)
		return
	}
	httpJSON(w, out)
}

func (a *App) handleHTTPList(w http.ResponseWriter, r *http.Request) {
	if err := validateListQuery(r, 500); err != nil {
		writeError(w, err)
		return
	}
	ctx := a.httpContext(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	filter := JobFilter{
		Page:          true,
		BeforeID:      parseInt64(q.Get("before")),
		Search:        q.Get("search"),
		OwnerApp:      q.Get("owner_app"),
		OwnerInstance: parseInt64(q.Get("owner_instance")),
		Status:        q.Get("status"),
		Limit:         atoiDefault(q.Get("limit"), 100, 500),
	}
	out, err := dbListJobs(ctx.AppDB(), pid, filter, r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	httpJSON(w, jobsPage(out, filter.Limit))
}

func (a *App) handleHTTPGet(w http.ResponseWriter, r *http.Request, idStr string) {
	ctx := a.httpContext(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id, parseErr := strconv.ParseInt(idStr, 10, 64)
	if parseErr != nil || id <= 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	job, err := dbGetJob(ctx.AppDB(), pid, id, r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if job == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"job": job})
}

func (a *App) handleHTTPCancel(w http.ResponseWriter, r *http.Request, idStr string) {
	ctx := a.httpContext(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id, parseErr := strconv.ParseInt(idStr, 10, 64)
	if parseErr != nil || id <= 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if err := dbCancelJob(ctx.AppDB(), pid, id, r.Context()); err != nil {
		writeError(w, err)
		return
	}
	if ctx != nil {
		ctx.EmitWithProject("job.cancelled", pid, map[string]any{"id": id})
	}
	httpJSON(w, map[string]any{"cancelled": true, "id": id})
}

func (a *App) handleHTTPJobRuns(w http.ResponseWriter, r *http.Request, idStr string) {
	if err := validateListQuery(r, 200); err != nil {
		writeError(w, err)
		return
	}
	ctx := a.httpContext(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id, parseErr := strconv.ParseInt(idStr, 10, 64)
	if parseErr != nil || id <= 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 50, 200)
	runs, err := dbJobRunsContext(r.Context(), ctx.AppDB(), pid, id, limit+1, parseInt64(r.URL.Query().Get("before")))
	if err != nil {
		writeError(w, err)
		return
	}
	httpJSON(w, runsPage(runs, limit))
}

func (a *App) handleHTTPRunNow(w http.ResponseWriter, r *http.Request, idStr string) {
	ctx := a.httpContext(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id, parseErr := strconv.ParseInt(idStr, 10, 64)
	if parseErr != nil || id <= 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if err := queueManualRun(ctx.AppDB(), pid, id, r.Context()); err != nil {
		writeError(w, err)
		return
	}
	if ctx != nil {
		ctx.EmitWithProject("job.queued", pid, map[string]any{"id": id})
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
				"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 200, "description": "Human-readable job name."},
				"schedule": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind":                map[string]any{"type": "string", "enum": []any{"once", "every", "cron", "random"}},
						"run_at":              map[string]any{"type": "string", "description": "RFC3339 timestamp; required when kind=once."},
						"every_seconds":       map[string]any{"type": "integer", "minimum": 1, "maximum": 31536000, "description": "Interval in seconds; required when kind=every."},
						"cron":                map[string]any{"type": "string", "description": "5-field cron 'M H DOM MON DOW'; required when kind=cron."},
						"period":              map[string]any{"type": "string", "enum": []any{"day"}, "description": "Calendar period for kind=random. Currently day."},
						"runs_per_period":     map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Number of randomized runs in each period."},
						"window_start":        map[string]any{"type": "string", "description": "Inclusive local-time window start in HH:MM."},
						"window_end":          map[string]any{"type": "string", "description": "Inclusive local-time window end in HH:MM."},
						"min_spacing_minutes": map[string]any{"type": "integer", "minimum": 0, "maximum": 1440, "description": "Minimum minutes between randomized runs."},
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
				"max_retries":     map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
				"backoff_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 86400},
				"timezone":        map[string]any{"type": "string", "description": "IANA tz name; cron is evaluated in this tz. Default UTC."},
				"owner_app":       map[string]any{"type": "string"},
				"schedule_seed":   map[string]any{"type": "string", "description": "Optional seed returned by jobs_preview so creation matches the preview."},
			}, []string{"name", "schedule", "target"}),
			HandlerCtx: a.toolScheduleTrusted,
		},
		{
			Name:        "jobs_cancel",
			Description: "Cancel a scheduled job by id. Idempotent — cancelling an already-terminal job returns ok.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			HandlerCtx: a.scopedTool(a.toolCancel),
		},
		{
			Name:        "jobs_list",
			Description: "List jobs. Args: owner_app, owner_instance, status (pending|running|done|failed|cancelled), limit (default 100, max 500).",
			InputSchema: schemaObject(map[string]any{
				"owner_app":      map[string]any{"type": "string"},
				"owner_instance": map[string]any{"type": "integer"},
				"status":         map[string]any{"type": "string"},
				"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
				"before":         map[string]any{"type": "integer", "minimum": 1},
				"search":         map[string]any{"type": "string", "maxLength": 200},
			}, nil),
			HandlerCtx: a.scopedTool(a.toolList),
		},
		{
			Name:        "jobs_get",
			Description: "Fetch one job by id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			HandlerCtx: a.scopedTool(a.toolGet),
		},
		{
			Name:        "jobs_runs",
			Description: "Fetch recent runs for a job. Args: id, limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
				"before": map[string]any{"type": "integer", "minimum": 1},
			}, []string{"id"}),
			HandlerCtx: a.scopedTool(a.toolRuns),
		},
		{
			Name:        "jobs_run_now",
			Description: "Queue an immediate ad-hoc run of a scheduled job. Creates a separate occurrence without changing the regular schedule.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			HandlerCtx: a.scopedTool(a.toolRunNow),
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
			HandlerCtx: a.scopedTool(a.toolPreview),
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
	env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
	if r.URL.Query().Get("scope") == "global" {
		if env != "" || r.URL.Query().Get("project_id") != "" {
			return "", badRequest("global scope requires a global install and no project_id")
		}
		return "", nil
	}
	if env != "" {
		return env, nil
	}
	if values, ok := r.URL.Query()["project_id"]; ok && len(values) == 1 {
		return values[0], nil
	}
	return "", badRequest("project_id required; use scope=global for global jobs")
}

// ─── Domain types ──────────────────────────────────────────────────

type Job struct {
	ID             int64           `json:"id"`
	ParentJobID    int64           `json:"parent_job_id,omitempty"`
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
	ResponseBody   string `json:"-"`
	Error          string `json:"error,omitempty"`
	Attempt        int    `json:"attempt"`
	ScheduledFor   string `json:"scheduled_for,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type JobFilter struct {
	Page          bool
	BeforeID      int64
	Search        string
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
	if err := normalizeTargetArgs(args, callerInstance); err != nil {
		return nil, err
	}
	job, err := dbScheduleJob(ctx.AppDB(), pid, args, argsContext(args))
	if err != nil {
		return nil, err
	}
	emitJob(ctx, "job.scheduled", job)
	return map[string]any{"job": job}, nil
}

// normalizeTargetArgs translates the LLM/UI-facing event target into the
// stable wire shape shared by MCP and HTTP scheduling.
func normalizeTargetArgs(args map[string]any, callerInstance int64) error {
	t, ok := args["target"].(map[string]any)
	if !ok {
		return nil
	}
	if strings.EqualFold(strKey(t, "kind"), "event") {
		raw, has := t["agent_id"]
		if !has {
			raw = t["instance_id"]
		}
		self := raw == nil || raw == "" || raw == "self" || raw == "0"
		n, err := integer(raw)
		if !self && (err != nil || n < 0) {
			return badRequest("event agent_id must be a positive integer or self")
		}
		if self || n == 0 {
			n = callerInstance
		}
		if n <= 0 {
			return badRequest("event agent_id required; self requires a calling agent")
		}
		t["instance_id"] = n
		delete(t, "agent_id")
	}
	return nil
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
	if err := dbCancelJob(ctx.AppDB(), pid, id, argsContext(args)); err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.EmitWithProject("job.cancelled", pid, map[string]any{"id": id})
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
	ctx.EmitWithProject(topic, j.ProjectID, map[string]any{
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
		Page:          true,
		BeforeID:      int64Arg(args, "before"),
		Search:        strArg(args, "search"),
		OwnerApp:      strArg(args, "owner_app"),
		OwnerInstance: int64Arg(args, "owner_instance"),
		Status:        strArg(args, "status"),
		Limit:         intArg(args, "limit", 100),
	}
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	out, err := dbListJobs(ctx.AppDB(), pid, filter, argsContext(args))
	if err != nil {
		return nil, err
	}
	return jobsPage(out, filter.Limit), nil
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
	job, err := dbGetJob(ctx.AppDB(), pid, id, argsContext(args))
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
	runs, err := dbJobRunsContext(argsContext(args), ctx.AppDB(), pid, id, limit+1, int64Arg(args, "before"))
	if err != nil {
		return nil, err
	}
	return runsPage(runs, limit), nil
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
	if err := queueManualRun(ctx.AppDB(), pid, id, argsContext(args)); err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.EmitWithProject("job.queued", pid, map[string]any{"id": id})
	}
	return map[string]any{"queued": true, "id": id}, nil
}

func (a *App) toolPreview(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return buildSchedulePreview(args, time.Now().UTC())
}

func buildSchedulePreview(args map[string]any, now time.Time) (map[string]any, error) {
	if err := boundedInteger(args, "limit", 1, 50); err != nil {
		return nil, err
	}
	if schedule, ok := args["schedule"].(map[string]any); ok {
		for k, b := range map[string][2]int64{"runs_per_period": {1, 100}, "min_spacing_minutes": {0, 1440}} {
			if err := boundedInteger(schedule, k, b[0], b[1]); err != nil {
				return nil, err
			}
		}
	}

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

func dbScheduleJob(db *sql.DB, pid string, args map[string]any, parents ...context.Context) (*Job, error) {
	ctx, cancel := operationContext(parents)
	defer cancel()
	if err := validateScheduleArgs(args); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name"))
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
	if kind != "random" && next.IsZero() {
		return nil, badRequest("schedule has no possible next occurrence")
	}
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

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var nextID int64
	if err := tx.QueryRowContext(ctx, `UPDATE job_sequence SET value=MAX(value,(SELECT COALESCE(MAX(id),0) FROM jobs))+1 RETURNING value`).Scan(&nextID); err != nil {
		return nil, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE project_id=? AND status IN ('pending','running')`, pid).Scan(&active); err != nil {
		return nil, err
	}
	if active >= maxActiveJobs {
		return nil, conflict("active job limit reached")
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO jobs (
			id, project_id, name, owner_app, owner_instance,
			schedule_kind, cron_expr, every_seconds, run_at, timezone, random_config_json, schedule_seed,
			target_kind, target_json,
			idempotency_key, max_retries, backoff_seconds,
			status, next_run_at, scheduled_for,
			created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?)`,
		nextID, pid, name, strArg(args, "owner_app"), nullableInt64(int64Arg(args, "owner_instance")),
		kind, nullStr(cronExpr), nullableInt64Ptr(everySeconds), nullableTime(runAt), tz, nullStr(randomConfigJSON), nullStr(scheduleSeed),
		targetKind, string(targetJSON),
		nullStr(strArg(args, "idempotency_key")), maxRetries, backoff,
		nullableTime(next), nullableTime(next), now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetJob(db, pid, id, ctx)
}

func dbGetJob(db *sql.DB, pid string, id int64, parents ...context.Context) (*Job, error) {
	ctx, cancel := operationContext(parents)
	defer cancel()
	row := db.QueryRowContext(ctx,
		`SELECT id, project_id, name, COALESCE(owner_app,''), owner_instance,
			schedule_kind, COALESCE(cron_expr,''), every_seconds, run_at, timezone,
			COALESCE(random_config_json,''), COALESCE(schedule_seed,''),
			target_json,
			COALESCE(idempotency_key,''), max_retries, backoff_seconds,
			status, next_run_at, scheduled_for, last_run_at, COALESCE(last_status,''), COALESCE(last_error,''),
			attempt, created_at, updated_at, cancelled_at, COALESCE(parent_job_id,0)
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
		&j.Attempt, &j.CreatedAt, &j.UpdatedAt, &cancelledAt, &j.ParentJobID)
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
		if err := json.Unmarshal([]byte(targetJSON), &j.Target); err != nil {
			return nil, fmt.Errorf("decode target for job %d: %w", j.ID, err)
		}
	}
	if randomConfigJSON != "" {
		j.Random = &RandomSchedule{}
		if err := json.Unmarshal([]byte(randomConfigJSON), j.Random); err != nil {
			return nil, fmt.Errorf("decode random schedule for job %d: %w", j.ID, err)
		}
	}
	return j, nil
}

func dbListJobs(db *sql.DB, pid string, f JobFilter, parents ...context.Context) ([]*Job, error) {
	ctx, cancel := operationContext(parents)
	defer cancel()
	where := []string{"project_id = ? AND parent_job_id IS NULL"}
	args := []any{pid}
	if f.BeforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, f.BeforeID)
	}
	if f.Search != "" {
		where = append(where, "(name LIKE ? ESCAPE '\\' OR CAST(id AS TEXT)=?)")
		args = append(args, "%"+escapeLike(f.Search)+"%", f.Search)
	}
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
			json_object('kind',target_kind),
			'', max_retries, backoff_seconds,
			status, next_run_at, scheduled_for, last_run_at, COALESCE(last_status,''), COALESCE(last_error,''),
			attempt, created_at, updated_at, cancelled_at, COALESCE(parent_job_id,0)
		 FROM jobs WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY id DESC LIMIT ?`
	if f.Page {
		limit++
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, publicJob(j))
	}
	return out, rows.Err()
}

func dbCancelJob(db *sql.DB, pid string, id int64, parents ...context.Context) error {
	ctx, cancel := operationContext(parents)
	defer cancel()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.ExecContext(ctx,
		`UPDATE jobs SET status = 'cancelled',
			cancelled_at = ?, updated_at = ?,
			next_run_at = NULL
		 WHERE (id = ? OR parent_job_id = ?) AND project_id = ? AND status NOT IN ('done', 'failed', 'cancelled')`,
		now, now, id, id, pid)
	return err
}

func dbRunNow(db *sql.DB, pid string, id int64) error { return queueManualRun(db, pid, id) }

func dbJobRuns(db *sql.DB, pid string, jobID int64, limit int, before ...int64) ([]*JobRun, error) {
	return dbJobRunsContext(context.Background(), db, pid, jobID, limit, before...)
}
func dbJobRunsContext(parent context.Context, db *sql.DB, pid string, jobID int64, limit int, before ...int64) ([]*JobRun, error) {
	ctx, cancel := operationContext([]context.Context{parent})
	defer cancel()
	rows, err := db.QueryContext(ctx,
		`SELECT id, job_id, started_at, COALESCE(finished_at,''),
			COALESCE(duration_ms,0), status, COALESCE(http_status,0),
			COALESCE(response_body,''), COALESCE(error,''), attempt,
			COALESCE(scheduled_for,''), COALESCE(idempotency_key,'')
		 FROM job_runs WHERE project_id = ? AND job_id = ? AND (?=0 OR id<?)
		 ORDER BY id DESC LIMIT ?`,
		pid, jobID, cursorValue(before), cursorValue(before), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*JobRun{}
	for rows.Next() {
		r := &JobRun{}
		if err := rows.Scan(&r.ID, &r.JobID, &r.StartedAt, &r.FinishedAt,
			&r.DurationMS, &r.Status, &r.HTTPStatus, &r.ResponseBody, &r.Error, &r.Attempt,
			&r.ScheduledFor, &r.IdempotencyKey); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func dbRecentRuns(db *sql.DB, pid string, limit int, before ...int64) ([]*JobRun, error) {
	return dbRecentRunsContext(context.Background(), db, pid, limit, before...)
}
func dbRecentRunsContext(parent context.Context, db *sql.DB, pid string, limit int, before ...int64) ([]*JobRun, error) {
	ctx, cancel := operationContext([]context.Context{parent})
	defer cancel()
	rows, err := db.QueryContext(ctx,
		`SELECT id, job_id, started_at, COALESCE(finished_at,''),
			COALESCE(duration_ms,0), status, COALESCE(http_status,0),
			COALESCE(response_body,''), COALESCE(error,''), attempt,
			COALESCE(scheduled_for,''), COALESCE(idempotency_key,'')
		 FROM job_runs WHERE project_id = ? AND (?=0 OR id<?)
		 ORDER BY id DESC LIMIT ?`,
		pid, cursorValue(before), cursorValue(before), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*JobRun{}
	for rows.Next() {
		r := &JobRun{}
		if err := rows.Scan(&r.ID, &r.JobID, &r.StartedAt, &r.FinishedAt,
			&r.DurationMS, &r.Status, &r.HTTPStatus, &r.ResponseBody, &r.Error, &r.Attempt,
			&r.ScheduledFor, &r.IdempotencyKey); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─── Dispatcher ────────────────────────────────────────────────────
//
// The dispatcher claims due jobs only when an execution slot is available.
// Attempts are recorded before delivery and finalized with the schedule in
// one transaction. Workers that lose ownership cannot overwrite a successor.
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

// dispatchOne durably records the attempt before delivery. Lease loss cancels
// the actual network request; stale owners cannot change the schedule.
func dispatchOne(ctx context.Context, app *sdk.AppCtx, j *Job) error {
	db := app.AppDB()
	started := time.Now().UTC()
	scheduled := jobScheduledTime(j, started)
	// Keep the exact existing identity across retries, including pre-migration offsets.
	if j.ScheduledFor == "" {
		j.ScheduledFor = scheduled.UTC().Format(time.RFC3339)
	}
	runID, err := beginAttempt(ctx, db, j, started)
	if err != nil {
		return fmt.Errorf("start attempt: %w", err)
	}
	if runID == 0 {
		return nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	leaseDone := make(chan struct{})
	go renewLease(runCtx, db, j, cancel, leaseDone)
	status, code, _, dispatchErr := runTarget(runCtx, app, j)
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		status = "timeout"
		dispatchErr = runCtx.Err()
	}
	if errors.Is(runCtx.Err(), context.Canceled) {
		status = "interrupted"
		dispatchErr = errors.New("Delivery interrupted; outcome may be unknown")
	}
	cancel()
	<-leaseDone
	finished := time.Now().UTC()
	attempt := j.Attempt + 1
	errStr := safeDispatchError(dispatchErr, j.Target)
	loc, err := time.LoadLocation(j.Timezone)
	if err != nil {
		loc = time.UTC
	}
	nextStatus, nextAttempt := "pending", 0
	nextRun := time.Time{}
	nextScheduled := j.ScheduledFor
	advance := func() {
		if j.ScheduleKind == "random" {
			nextRun, err = computeNextRandomOccurrence(j, loc, scheduled)
		} else {
			nextRun = computeNextRunAfter(j, loc, finished)
		}
		if err != nil {
			errStr = safeDispatchError(err, j.Target)
		}
		if nextRun.IsZero() {
			if j.ScheduleKind == "once" && status == "ok" {
				nextStatus = "done"
			} else {
				nextStatus = "failed"
			}
		} else {
			nextScheduled = nextRun.UTC().Format(time.RFC3339)
		}
	}
	if status == "ok" {
		advance()
	} else if status == "interrupted" || attempt <= j.MaxRetries {
		nextRun = finished.Add(retryDelay(j.BackoffSeconds, attempt))
		nextAttempt = attempt
	} else {
		advance()
	}
	lastStatus := "error"
	if status == "ok" {
		lastStatus = "ok"
	}
	// Shutdown must still finish the durable state transition. A bounded detached
	// context prevents an already-cancelled request from discarding the audit log.
	finalCtx, finalCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finalCancel()
	tx, err := db.BeginTx(finalCtx, nil)
	if err != nil {
		return fmt.Errorf("finalize attempt: %w", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(finalCtx, `UPDATE job_runs SET finished_at=?,duration_ms=?,status=?,http_status=?,response_body=NULL,error=? WHERE id=? AND lease_token=? AND status='running'`, finished.Format(time.RFC3339Nano), finished.Sub(started).Milliseconds(), status, code, nullStr(errStr), runID, j.LeaseToken); err != nil {
		return fmt.Errorf("record outcome: %w", err)
	}
	res, err := tx.ExecContext(finalCtx, `UPDATE jobs SET status=?,last_run_at=?,last_status=?,last_error=?,attempt=?,lease_until=NULL,lease_token=NULL,next_run_at=?,scheduled_for=?,updated_at=? WHERE id=? AND project_id=? AND status='running' AND lease_token=?`, nextStatus, finished.Format(time.RFC3339Nano), lastStatus, nullStr(errStr), nextAttempt, nullableTime(nextRun), nextScheduled, finished.Format(time.RFC3339Nano), j.ID, j.ProjectID, j.LeaseToken)
	if err != nil {
		return fmt.Errorf("finalize schedule: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit outcome: %w", err)
	}
	if n > 0 {
		payload := map[string]any{"id": j.ID, "status": nextStatus, "last_status": lastStatus}
		if j.ParentJobID != 0 {
			payload = map[string]any{"id": j.ParentJobID, "manual_run_id": j.ID, "manual_run_status": nextStatus}
		}
		app.EmitWithProject("job.updated", j.ProjectID, payload)
	}
	return nil
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
	return pruneHistory(context.Background(), db, pid, true, cfg, now)
}
func pruneHistory(parent context.Context, db *sql.DB, pid string, scoped bool, cfg sdk.Config, now time.Time) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	for _, item := range []struct {
		table, column, config string
		days                  int
		condition             string
	}{
		{"job_runs", "started_at", "history_retention_days", 30, "status <> 'running'"},
		{"jobs", "updated_at", "terminal_job_retention_days", 90, "status IN ('done','failed','cancelled') AND NOT EXISTS(SELECT 1 FROM jobs child WHERE child.parent_job_id=jobs.id AND child.status IN ('pending','running'))"},
	} {
		days := item.days
		if raw := strings.TrimSpace(cfg.Get(item.config)); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || n > 36500 {
				return fmt.Errorf("invalid %s", item.config)
			}
			days = n
		}
		if days == 0 {
			continue
		}
		where := item.condition + " AND " + item.column + " < ?"
		args := []any{now.AddDate(0, 0, -days).Format(time.RFC3339)}
		if scoped {
			where += " AND project_id=?"
			args = append(args, pid)
		}
		// Bounded batches cap lock duration and memory even for a large backlog.
		for batch := 0; batch < 10; batch++ {
			res, err := db.ExecContext(ctx, "DELETE FROM "+item.table+" WHERE id IN (SELECT id FROM "+item.table+" WHERE "+where+" ORDER BY "+item.column+",id LIMIT 1000)", args...)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n < 1000 {
				break
			}
		}
	}
	return nil
}

// runTarget dispatches one job's target. Returns (status, http_code,
// body, err). status is "ok" / "error" / "timeout".
func runTarget(ctx context.Context, app *sdk.AppCtx, j *Job) (string, int, string, error) {
	if err := authorizeDispatch(ctx, app, j); err != nil {
		return "error", 0, "", err
	}
	if err := checkExecutionLease(ctx, app, j); err != nil {
		return "interrupted", 0, "", err
	}
	switch strings.ToLower(strKey(j.Target, "kind")) {
	case "http":
		if target, ok := legacyFunctionsTarget(j.Target); ok {
			return runAppToolTargetContext(ctx, app, j, target)
		}
		return runHTTPTarget(ctx, j, app.Config())
	case "app_tool":
		return runAppToolTargetContext(ctx, app, j, j.Target)
	case "event":
		return runEventTargetContext(ctx, app, j)
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

	resp, err := targetHTTPClient(cfg).Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "timeout", 0, "", err
		}
		return "error", 0, "", err
	}
	defer resp.Body.Close()
	respBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if readErr != nil {
		if errors.Is(readErr, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return "timeout", resp.StatusCode, "", readErr
		}
		return "error", resp.StatusCode, "", readErr
	}
	if len(respBytes) > 1<<20 {
		return "error", resp.StatusCode, "", errors.New("HTTP response exceeds 1 MiB")
	}
	if resp.StatusCode/100 != 2 {
		return "error", resp.StatusCode, string(respBytes),
			fmt.Errorf("non-2xx: %d", resp.StatusCode)
	}
	return "ok", resp.StatusCode, string(respBytes), nil
}

func runAppToolTarget(app *sdk.AppCtx, job *Job, target map[string]any) (string, int, string, error) {
	return runAppToolTargetContext(context.Background(), app, job, target)
}
func runAppToolTargetContext(ctx context.Context, app *sdk.AppCtx, job *Job, target map[string]any) (string, int, string, error) {
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
	for key := range input {
		if reservedInputKey(key) {
			delete(input, key)
		}
	}
	input["_project_id"] = job.ProjectID
	input["_job"] = metadata
	if app == nil || app.PlatformAPI() == nil {
		return "error", 0, "", errors.New("app_tool target requires platform API")
	}
	var out any
	if err := callAppContext(ctx, app, appName, tool, input, &out); err != nil {
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
		timeout = time.Duration(min(sec, int(maxHTTPDispatchTimeout/time.Second))) * time.Second
	}
	if sec := intArg(target, "timeout_seconds", 0); sec > 0 {
		timeout = time.Duration(min(sec, int(maxHTTPDispatchTimeout/time.Second))) * time.Second
	}
	if ms := intArg(target, "timeout_ms", 0); ms > 0 {
		timeout = time.Duration(min(ms, int(maxHTTPDispatchTimeout/time.Millisecond))) * time.Millisecond
	}
	if timeout < minHTTPDispatchTimeout {
		return minHTTPDispatchTimeout
	}
	if timeout > maxHTTPDispatchTimeout {
		return maxHTTPDispatchTimeout
	}
	return timeout
}

func runEventTarget(app *sdk.AppCtx, j *Job) (string, int, string, error) {
	return runEventTargetContext(context.Background(), app, j)
}
func runEventTargetContext(ctx context.Context, app *sdk.AppCtx, j *Job) (string, int, string, error) {
	id := toInt64(j.Target["instance_id"])
	message := strKey(j.Target, "message")
	if id <= 0 || message == "" {
		return "error", 0, "", errors.New("event target requires instance_id and message")
	}
	agent, err := getInstanceContext(ctx, app, id)
	if err != nil {
		return "error", 0, "", err
	}
	if agent == nil || agent.ProjectID != j.ProjectID {
		return "error", 0, "", errors.New("event target is outside the job project")
	}
	if err := checkExecutionLease(ctx, app, j); err != nil {
		return "interrupted", 0, "", err
	}
	if err := sendEventContext(ctx, app, id, message); err != nil {
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
		if every == nil || *every <= 0 || *every > 31536000 {
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
	out.domAny = strings.HasPrefix(fields[2], "*")
	out.dowAny = strings.HasPrefix(fields[4], "*")
	return out, nil
}

func parseCronField(f string, lo, hi int) ([]bool, error) {
	out := make([]bool, hi+1)
	for _, part := range strings.Split(f, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			s, err := strconv.Atoi(part[i+1:])
			if err != nil || s < 1 || s > hi-lo+1 {
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

// next advances through real instants on matching dates, including repeated
// local hours. Eight calendar years cover leap-day gaps across non-leap centuries.
func (c *cronExpr) next(from time.Time) time.Time {
	t := from.Truncate(time.Minute).Add(time.Minute)
	limit := from.AddDate(8, 0, 0)
	for !t.After(limit) {
		if !c.mon[int(t.Month())] || !c.matchDay(t) {
			next := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			if !next.After(t) {
				next = t.Add(time.Minute)
			}
			t = next
			continue
		}
		if c.hour[t.Hour()] && c.min[t.Minute()] {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

func (c *cronExpr) matchDay(t time.Time) bool {
	dom := c.dom[t.Day()]
	dow := c.dow[int(t.Weekday())]
	if c.domAny || c.dowAny {
		return dom && dow
	}
	return dom || dow
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
		if !validIdentifier(strKey(t, "app")) || !validToolName(strKey(t, "tool")) {
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
	if n, err := integer(args[key]); err == nil {
		return int(n)
	}
	return def
}
func int64Arg(args map[string]any, key string) int64 { n, _ := integer(args[key]); return n }

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

func toInt64(v any) int64 { n, _ := integer(v); return n }

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
