// Compute is a shared transient execution queue for Apteva apps.
//
// It intentionally sits above Instances: callers submit jobs to a
// resource-aware queue; Compute decides where and when to run them.
// v0.1 ships the local executor first so it is useful without
// Instances. The DB model already carries host/pool/executor fields so
// the Instances backend can be added without changing callers.
package main

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML string

const (
	statusQueued     = "queued"
	statusRunning    = "running"
	statusOK         = "ok"
	statusFailed     = "failed"
	statusCancelled  = "cancelled"
	statusCancelling = "cancelling"

	priorityHigh   = 10
	priorityNormal = 50
	priorityHeavy  = 80
)

type App struct {
	mu      sync.Mutex
	running map[int64]context.CancelFunc
}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("compute: invalid manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("compute requires a db block")
	}
	a.running = map[int64]context.CancelFunc{}
	globalCtx = ctx
	ctx.Logger().Info("compute mounted",
		"local_enabled", configBool(ctx, "local_enabled", true),
		"local_max_concurrency", configInt(ctx, "local_max_concurrency", 1))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, cancel := range a.running {
		cancel()
	}
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "dispatcher",
		Schedule: "@every 1s",
		Run: func(ctx context.Context, app *sdk.AppCtx) error {
			return a.dispatchTick(ctx, app)
		},
	}}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/jobs", Handler: a.handleJobs},
		{Method: http.MethodPost, Pattern: "/jobs", Handler: a.handleJobs},
		{Pattern: "/jobs/", Handler: a.handleJobItem},
		{Method: http.MethodGet, Pattern: "/summary", Handler: a.handleSummary},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "compute_submit",
			Description: "Submit a transient compute job to the shared queue. v0.1 supports kind='shell' commands. " +
				"Args: command (required), timeout_s?, priority? ('high'|'normal'|'heavy' or integer), owner_app?, owner_ref?, resource_class?, pool?, host_id?, idempotency_key?, cwd?, env? object.",
			InputSchema: schemaObject(map[string]any{
				"command":         map[string]any{"type": "string"},
				"kind":            map[string]any{"type": "string"},
				"timeout_s":       map[string]any{"type": "integer"},
				"priority":        map[string]any{},
				"owner_app":       map[string]any{"type": "string"},
				"owner_ref":       map[string]any{"type": "string"},
				"resource_class":  map[string]any{"type": "string"},
				"pool":            map[string]any{"type": "string"},
				"host_id":         map[string]any{"type": "integer"},
				"idempotency_key": map[string]any{"type": "string"},
				"cwd":             map[string]any{"type": "string"},
				"env":             map[string]any{"type": "object"},
			}, []string{"command"}),
			Handler: a.toolSubmit,
		},
		{
			Name:        "compute_get",
			Description: "Fetch one compute job by id. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolGet,
		},
		{
			Name: "compute_list",
			Description: "List compute jobs. Args: status?, owner_app?, resource_class?, pool?, host_id?, limit? " +
				"(default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"status":         map[string]any{"type": "string"},
				"owner_app":      map[string]any{"type": "string"},
				"resource_class": map[string]any{"type": "string"},
				"pool":           map[string]any{"type": "string"},
				"host_id":        map[string]any{"type": "integer"},
				"limit":          map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolList,
		},
		{
			Name:        "compute_cancel",
			Description: "Cancel a queued or running compute job. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolCancel,
		},
	}
}

func main() { sdk.Run(&App{}) }

// HTTP handlers.

func (a *App) handleJobs(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "compute not mounted")
		return
	}
	pid := projectIDFromRequest(r)
	if pid == "" {
		httpErr(w, http.StatusBadRequest, "project_id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		filter := JobFilter{
			Status:        r.URL.Query().Get("status"),
			OwnerApp:      r.URL.Query().Get("owner_app"),
			ResourceClass: r.URL.Query().Get("resource_class"),
			Pool:          r.URL.Query().Get("pool"),
			HostID:        parseInt64(r.URL.Query().Get("host_id")),
			Limit:         parseLimit(r.URL.Query().Get("limit")),
		}
		rows, err := listJobs(globalCtx.AppDB(), pid, filter)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"jobs": rows, "count": len(rows)})
	case http.MethodPost:
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		job, err := submitJob(globalCtx, pid, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		emitJob(globalCtx, "compute.queued", job)
		writeJSON(w, map[string]any{"job": job})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleJobItem(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "compute not mounted")
		return
	}
	pid := projectIDFromRequest(r)
	if pid == "" {
		httpErr(w, http.StatusBadRequest, "project_id required")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/jobs/")
	idPart, sub, _ := strings.Cut(rest, "/")
	id := parseInt64(idPart)
	if id <= 0 {
		httpErr(w, http.StatusBadRequest, "job id required")
		return
	}
	switch {
	case r.Method == http.MethodGet && sub == "":
		job, err := getJob(globalCtx.AppDB(), pid, id)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"job": job, "found": job != nil})
	case r.Method == http.MethodPost && sub == "cancel":
		job, err := a.cancelJob(globalCtx, pid, id)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"job": job, "cancelled": true})
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

func (a *App) handleSummary(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "compute not mounted")
		return
	}
	pid := projectIDFromRequest(r)
	if pid == "" {
		httpErr(w, http.StatusBadRequest, "project_id required")
		return
	}
	s, err := queueSummary(globalCtx.AppDB(), pid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, s)
}

// MCP tools.

func (a *App) toolSubmit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := contextProjectID(args)
	if pid == "" {
		return nil, errors.New("project_id missing - pass _project_id when scope=global")
	}
	job, err := submitJob(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	emitJob(ctx, "compute.queued", job)
	return map[string]any{"job_id": job.ID, "status": job.Status, "job": job}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := contextProjectID(args)
	if pid == "" {
		return nil, errors.New("project_id missing - pass _project_id when scope=global")
	}
	id := int64Arg(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	job, err := getJob(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"found": job != nil, "job": job}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := contextProjectID(args)
	if pid == "" {
		return nil, errors.New("project_id missing - pass _project_id when scope=global")
	}
	rows, err := listJobs(ctx.AppDB(), pid, JobFilter{
		Status:        strArg(args, "status"),
		OwnerApp:      strArg(args, "owner_app"),
		ResourceClass: strArg(args, "resource_class"),
		Pool:          strArg(args, "pool"),
		HostID:        int64Arg(args, "host_id"),
		Limit:         intArg(args, "limit", 50),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"jobs": rows, "count": len(rows)}, nil
}

func (a *App) toolCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := contextProjectID(args)
	if pid == "" {
		return nil, errors.New("project_id missing - pass _project_id when scope=global")
	}
	id := int64Arg(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	job, err := a.cancelJob(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"found": job != nil, "cancelled": job != nil, "job": job}, nil
}

// Queue worker.

func (a *App) dispatchTick(ctx context.Context, app *sdk.AppCtx) error {
	if !configBool(app, "local_enabled", true) {
		return nil
	}
	max := configInt(app, "local_max_concurrency", 1)
	if max < 1 {
		max = 1
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if a.runningCount() >= max {
			return nil
		}
		job, err := claimNextJob(app.AppDB())
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		emitJob(app, "compute.started", job)
		go a.runLocalJob(app, job)
	}
}

func (a *App) runningCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.running)
}

func (a *App) runLocalJob(app *sdk.AppCtx, job *ComputeJob) {
	timeout := time.Duration(job.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(configInt(app, "default_timeout_seconds", 1800)) * time.Second
	}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	a.mu.Lock()
	a.running[job.ID] = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.running, job.ID)
		a.mu.Unlock()
		cancel()
	}()

	output, exitCode, err := runShell(runCtx, job, configInt(app, "max_output_bytes", 1048576))
	if runCtx.Err() == context.Canceled {
		_ = markCancelled(app.AppDB(), job.ProjectID, job.ID, "cancelled")
		emitCurrentJob(app, "compute.cancelled", job.ProjectID, job.ID)
		return
	}
	if runCtx.Err() == context.DeadlineExceeded {
		_ = markFailed(app.AppDB(), job.ProjectID, job.ID, output, exitCode, "timeout")
		emitCurrentJob(app, "compute.failed", job.ProjectID, job.ID)
		return
	}
	if err != nil {
		_ = markFailed(app.AppDB(), job.ProjectID, job.ID, output, exitCode, err.Error())
		emitCurrentJob(app, "compute.failed", job.ProjectID, job.ID)
		return
	}
	_ = markOK(app.AppDB(), job.ProjectID, job.ID, output, exitCode)
	emitCurrentJob(app, "compute.completed", job.ProjectID, job.ID)
}

func runShell(ctx context.Context, job *ComputeJob, maxOutput int) (string, int, error) {
	if job.Kind != "" && job.Kind != "shell" {
		return "", -1, fmt.Errorf("unsupported compute kind %q", job.Kind)
	}
	shell, flag := "/bin/sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/C"
	}
	cmd := exec.CommandContext(ctx, shell, flag, job.Command)
	if job.CWD != "" {
		cmd.Dir = job.CWD
	}
	if len(job.Env) > 0 {
		env := make([]string, 0, len(job.Env))
		for k, v := range job.Env {
			if safeEnvKey(k) {
				env = append(env, k+"="+v)
			}
		}
		sort.Strings(env)
		cmd.Env = append(cmd.Environ(), env...)
	}
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	var buf limitedBuffer
	buf.limit = maxOutput
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	exit := 0
	if err != nil {
		exit = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		}
	}
	if ctx.Err() != nil && cmd.Process != nil && runtime.GOOS != "windows" {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return buf.String(), exit, err
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remain := b.limit - b.Buffer.Len()
	if remain > 0 {
		if len(p) > remain {
			_, _ = b.Buffer.Write(p[:remain])
		} else {
			_, _ = b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

func (a *App) cancelJob(ctx *sdk.AppCtx, pid string, id int64) (*ComputeJob, error) {
	job, err := getJob(ctx.AppDB(), pid, id)
	if err != nil || job == nil {
		return job, err
	}
	switch job.Status {
	case statusQueued:
		if err := markCancelled(ctx.AppDB(), pid, id, "cancelled before start"); err != nil {
			return nil, err
		}
	case statusRunning:
		a.mu.Lock()
		cancel := a.running[id]
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if err := markCancelling(ctx.AppDB(), pid, id); err != nil {
			return nil, err
		}
	case statusCancelling, statusOK, statusFailed, statusCancelled:
		// terminal or already on the way down
	default:
		return nil, fmt.Errorf("cannot cancel job in status %q", job.Status)
	}
	job, err = getJob(ctx.AppDB(), pid, id)
	if err == nil && job != nil {
		emitJob(ctx, "compute.cancelled", job)
	}
	return job, err
}
