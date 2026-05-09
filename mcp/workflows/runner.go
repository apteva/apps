package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// runOptions controls one synchronous run. fromStep skips ahead in
// the step list (used by replay); skipped steps are recorded with
// status="skipped" rather than re-executed, which keeps the
// per-run audit trail complete.
type runOptions struct {
	fromStep    string // skip step ids until reaching this one (empty = start at first step)
	triggerKind string
}

// RunWorkflow executes a workflow synchronously. All step
// outputs and the final run state are persisted; the returned Run
// has its Steps slice populated for the caller to inspect / serve
// back to the agent / dashboard.
//
// Errors from individual steps don't propagate as Go errors — they
// land on the run row's Status/Error fields. Only setup errors
// (definition parse, DB write fail) come back here.
func RunWorkflow(ctx context.Context, app *sdk.AppCtx, pid string, w *Workflow, input any, opts runOptions) (*Run, error) {
	if w.Definition == nil {
		def, err := ParseDefinition([]byte(w.Source))
		if err != nil {
			return nil, fmt.Errorf("parse definition: %w", err)
		}
		w.Definition = def
	}

	// Per-workflow concurrency cap. Stops a runaway trigger from
	// fork-bombing one workflow into 1000 parallel invocations.
	sem := semForWorkflow(w.ID)
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-sem }()

	inputJSON := mustJSON(input)
	now := time.Now().UTC()

	// Insert the run row up-front so observers see "running" while
	// we're walking steps. The status flips at the end.
	runID, err := dbInsertRun(app.AppDB(), pid, &Run{
		WorkflowID:      w.ID,
		WorkflowName:    w.Name,
		WorkflowVersion: w.Version,
		TriggerKind:     opts.triggerKind,
		InputJSON:       truncate(inputJSON, maxInputJSON),
		Status:          "running",
		StartedAt:       now.Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, fmt.Errorf("insert run: %w", err)
	}

	tctx := TemplateContext{
		Input: input,
		Steps: map[string]any{},
		Env:   readEnv(),
		Now:   now.Format(time.RFC3339),
	}

	// Walk the steps. The next-index logic supports goto / on_error
	// jumps without recursion — keeps stack depth bounded regardless
	// of workflow size.
	idx := 0
	if opts.fromStep != "" {
		if i := w.Definition.stepIndex(opts.fromStep); i >= 0 {
			// Record the steps we're skipping so the audit trail
			// shows what was bypassed.
			for j := 0; j < i; j++ {
				_, _ = dbInsertStepExecution(app.AppDB(), runID, &StepExecution{
					StepID:    w.Definition.Steps[j].ID,
					StepKind:  w.Definition.Steps[j].Kind,
					Attempt:   1,
					StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
					Status:    "skipped",
				})
			}
			idx = i
		}
	}

	finalStatus := "completed"
	finalErr := ""

	for idx < len(w.Definition.Steps) {
		if ctx.Err() != nil {
			finalStatus = "cancelled"
			finalErr = ctx.Err().Error()
			break
		}
		step := &w.Definition.Steps[idx]
		_ = dbUpdateRunState(app.AppDB(), pid, runID, "running", step.ID, "", false)
		emitStepLifecycle(app, "workflow.step.started", runID, w, step, 1, "running", "")

		// Branches don't consume retry budget and don't go through
		// runStep — they decide control flow only.
		if step.Kind == "branch" {
			next, branchErr := runBranchStep(w, step, tctx, idx)
			recordBranch(app, runID, step, branchErr, next)
			completedStatus := "ok"
			completedErr := ""
			if branchErr != nil {
				completedStatus = "error"
				completedErr = branchErr.Error()
			}
			emitStepLifecycle(app, "workflow.step.completed", runID, w, step, 1, completedStatus, completedErr)
			if branchErr != nil {
				finalStatus = "failed"
				finalErr = branchErr.Error()
				break
			}
			if next.exit {
				if next.fail {
					finalStatus = "failed"
					finalErr = next.message
				}
				break
			}
			idx = next.idx
			continue
		}

		// Render input against the template context now (after any
		// prior step's output has been added to tctx.Steps).
		rendered, err := Render(step.Input, tctx)
		if err != nil {
			recordError(app, runID, step, "render input: "+err.Error())
			finalStatus = "failed"
			finalErr = "step " + step.ID + ": render input: " + err.Error()
			break
		}

		// Retry loop with exponential backoff. backoff_seconds
		// defaults to 0 (immediate retry) — same as functions /
		// jobs use a real default; here we keep it lightweight
		// because most workflow retries should be cheap.
		max, backoff := retryConfig(step)
		var res stepResult
		var lastAttempt int
		for attempt := 1; attempt <= max+1; attempt++ {
			lastAttempt = attempt
			res = runStep(ctx, app, step, rendered)
			recordStep(app, runID, step, attempt, rendered, res)
			if res.Status == "ok" || ctx.Err() != nil {
				break
			}
			if attempt > max {
				break
			}
			delay := time.Duration(backoff) * time.Second
			for i := 1; i < attempt; i++ {
				delay *= 2
			}
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
				}
			}
		}
		emitStepLifecycle(app, "workflow.step.completed", runID, w, step, lastAttempt, res.Status, res.Error)

		if res.Status != "ok" {
			// step.OnError tells us where to jump (or whether to fail
			// the run with a custom message).
			if step.OnError != nil {
				next, branchErr := jumpTo(w, step.OnError, idx)
				if branchErr != nil {
					finalStatus = "failed"
					finalErr = "step " + step.ID + ": " + branchErr.Error()
					break
				}
				if next.exit {
					if next.fail {
						finalStatus = "failed"
						finalErr = next.message
						if finalErr == "" {
							finalErr = res.Error
						}
					}
					break
				}
				idx = next.idx
				continue
			}
			finalStatus = "failed"
			finalErr = "step " + step.ID + ": " + res.Error
			break
		}

		// Successful step: stash output for downstream templating.
		tctx.Steps[step.ID] = res.Output
		idx++
	}

	if err := dbUpdateRunState(app.AppDB(), pid, runID, finalStatus, "", finalErr, true); err != nil {
		app.Logger().Warn("update run state", "run_id", runID, "err", err)
	}
	out, err := dbGetRun(app.AppDB(), pid, runID)
	if err != nil {
		app.Logger().Warn("read final run", "run_id", runID, "err", err)
	}
	if out != nil {
		out.Steps, _ = dbListStepExecutions(app.AppDB(), runID)
	}

	// Lifecycle event for dashboard / downstream workflows.
	// duration computed locally so the emit doesn't depend on the
	// just-read row (which can be nil if the DB write raced).
	durationMS := time.Since(now).Milliseconds()
	app.Emit("workflow.run.finished", map[string]any{
		"run_id":      runID,
		"workflow_id": w.ID,
		"name":        w.Name,
		"status":      finalStatus,
		"duration_ms": durationMS,
	})
	return out, nil
}

// ─── Branch control flow ───────────────────────────────────────────

// branchOutcome — where the runner should advance to. Mutually
// exclusive flags: idx is set when we want to jump to a specific
// step; exit is set when we should stop walking (success or
// failure).
type branchOutcome struct {
	idx     int
	exit    bool
	fail    bool
	message string
}

// runBranchStep evaluates a branch's `when`. Truthy → fall through
// to the next step (idx+1). Falsy → take the `else` (jump / end /
// fail). Defaults to "advance" if else is unset, so a branch with
// only `when` becomes an effective skip-on-falsy.
func runBranchStep(w *Workflow, step *StepDef, ctx TemplateContext, idx int) (branchOutcome, error) {
	matched, err := EvalCondition(step.When, ctx)
	if err != nil {
		return branchOutcome{}, err
	}
	if matched {
		return branchOutcome{idx: idx + 1}, nil
	}
	if step.Else == nil {
		return branchOutcome{idx: idx + 1}, nil
	}
	return jumpTo(w, step.Else, idx)
}

// jumpTo resolves a Goto into a concrete advance/exit. Errors only
// when the target step doesn't exist (caught at validation time
// usually, but we re-check defensively).
func jumpTo(w *Workflow, g *Goto, fromIdx int) (branchOutcome, error) {
	if g.End {
		return branchOutcome{exit: true, fail: false, message: g.Message}, nil
	}
	if g.Fail {
		return branchOutcome{exit: true, fail: true, message: g.Message}, nil
	}
	if g.StepID == "" {
		return branchOutcome{idx: fromIdx + 1}, nil
	}
	target := w.Definition.stepIndex(g.StepID)
	if target < 0 {
		return branchOutcome{}, fmt.Errorf("goto target %q not found", g.StepID)
	}
	return branchOutcome{idx: target}, nil
}

// ─── Step recorders ────────────────────────────────────────────────

// recordStep writes a workflow_step_executions row. Best-effort —
// DB failures get logged via the AppCtx logger, never bubble up
// since they shouldn't take the whole run down.
func recordStep(app *sdk.AppCtx, runID int64, step *StepDef, attempt int, input any, res stepResult) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	exec := &StepExecution{
		StepID:     step.ID,
		StepKind:   step.Kind,
		Attempt:    attempt,
		StartedAt:  now,
		FinishedAt: now,
		Status:     res.Status,
		InputJSON:  mustJSON(input),
		OutputJSON: mustJSON(res.Output),
		Error:      res.Error,
	}
	if _, err := dbInsertStepExecution(app.AppDB(), runID, exec); err != nil {
		app.Logger().Warn("record step", "step_id", step.ID, "err", err)
	}
}

func recordBranch(app *sdk.AppCtx, runID int64, step *StepDef, branchErr error, out branchOutcome) {
	status := "ok"
	errStr := ""
	if branchErr != nil {
		status = "error"
		errStr = branchErr.Error()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = dbInsertStepExecution(app.AppDB(), runID, &StepExecution{
		StepID:     step.ID,
		StepKind:   "branch",
		Attempt:    1,
		StartedAt:  now,
		FinishedAt: now,
		Status:     status,
		OutputJSON: mustJSON(map[string]any{"taken": out.idx, "exit": out.exit, "fail": out.fail}),
		Error:      errStr,
	})
}

func recordError(app *sdk.AppCtx, runID int64, step *StepDef, msg string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = dbInsertStepExecution(app.AppDB(), runID, &StepExecution{
		StepID:     step.ID,
		StepKind:   step.Kind,
		Attempt:    1,
		StartedAt:  now,
		FinishedAt: now,
		Status:     "error",
		Error:      msg,
	})
}

// emitStepLifecycle publishes workflow.step.started /
// workflow.step.completed events. Best-effort; the bus is best-
// effort by design (see app-event docs in CLAUDE.md). The
// dashboard panel uses these to animate the running step + paint
// statuses as steps land. attempt + status let consumers
// distinguish first-try from retry.
func emitStepLifecycle(app *sdk.AppCtx, topic string, runID int64, w *Workflow, step *StepDef, attempt int, status, errStr string) {
	if app == nil {
		return
	}
	app.Emit(topic, map[string]any{
		"run_id":      runID,
		"workflow_id": w.ID,
		"name":        w.Name,
		"step_id":     step.ID,
		"step_kind":   step.Kind,
		"attempt":     attempt,
		"status":      status,
		"error":       truncate(errStr, 256),
	})
}

// ─── Misc ──────────────────────────────────────────────────────────

// retryConfig pulls (max, backoff_seconds). Defaults match what
// jobs uses for its dispatcher (max=3, backoff=30) — but workflow
// authors that don't think about retry get max=0 (no retry); the
// inner if-step.Retry-nil keeps the default conservative.
func retryConfig(step *StepDef) (int, int) {
	if step.Retry == nil {
		return 0, 0
	}
	max := step.Retry.Max
	if max < 0 {
		max = 0
	}
	if max > 10 {
		max = 10
	}
	backoff := step.Retry.BackoffSeconds
	if backoff < 0 {
		backoff = 0
	}
	return max, backoff
}

// readEnv copies the spawn environment into a map[string]string for
// templating. Done once per run (TemplateContext.Env is frozen
// thereafter), matching the determinism contract.
func readEnv() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		out[kv[:i]] = kv[i+1:]
	}
	return out
}

// ─── Per-workflow concurrency ──────────────────────────────────────

const wfDefaultConcur = 8

var (
	wfSemsMu sync.Mutex
	wfSems   = map[int64]chan struct{}{}
)

func semForWorkflow(id int64) chan struct{} {
	wfSemsMu.Lock()
	defer wfSemsMu.Unlock()
	if s, ok := wfSems[id]; ok {
		return s
	}
	s := make(chan struct{}, wfDefaultConcur)
	wfSems[id] = s
	return s
}

// ─── Source resolution (inline / repo) ─────────────────────────────

// resolveWorkflowSource returns the workflow body bytes — inline
// from the row or fetched from the code app via CallAppResult.
// Cached in-memory by source_hash, same pattern as functions.
func resolveWorkflowSource(app *sdk.AppCtx, w *Workflow) ([]byte, error) {
	if w.SourceKind == "inline" {
		return []byte(w.Source), nil
	}
	if w.RepoID == nil || w.RepoPath == "" {
		return nil, errors.New("repo source missing repo_id or repo_path")
	}
	if cached, ok := wfSourceCache.get(*w.RepoID, w.RepoPath, w.SourceHash); ok {
		return cached, nil
	}
	if app == nil || app.PlatformAPI() == nil {
		return nil, errors.New("repo source requires PlatformAPI")
	}
	var resp struct {
		Content string `json:"content"`
	}
	if err := app.PlatformAPI().CallAppResult("code", "code_read_file", map[string]any{
		"repo_id": *w.RepoID,
		"path":    w.RepoPath,
	}, &resp); err != nil {
		return nil, err
	}
	bytes := []byte(resp.Content)
	if w.SourceHash != "" {
		wfSourceCache.put(*w.RepoID, w.RepoPath, w.SourceHash, bytes)
	}
	return bytes, nil
}

type wfRepoCache struct {
	mu sync.RWMutex
	m  map[wfRepoKey]wfRepoEntry
}
type wfRepoKey struct {
	repoID int64
	path   string
}
type wfRepoEntry struct {
	hash  string
	bytes []byte
}

func (c *wfRepoCache) get(repoID int64, path, hash string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[wfRepoKey{repoID, path}]
	if !ok || e.hash != hash {
		return nil, false
	}
	return e.bytes, true
}
func (c *wfRepoCache) put(repoID int64, path, hash string, bytes []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[wfRepoKey]wfRepoEntry{}
	}
	c.m[wfRepoKey{repoID, path}] = wfRepoEntry{hash, bytes}
}

var wfSourceCache = &wfRepoCache{}

// jsonOK formats an object's JSON for the audit log without
// erroring — fallback to empty string. Used by the runner when
// stuffing output into TemplateContext.
func jsonOK(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
