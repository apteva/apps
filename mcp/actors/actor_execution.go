package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	sdk "github.com/apteva/app-sdk"
	"golang.org/x/net/html"
)

var actorNumberPattern = regexp.MustCompile(`[-+]?\d[\d\s.,]*`)

type actorExecution struct {
	app            *App
	ctx            *sdk.AppCtx
	run            *actorQueuedRun
	definition     actorDefinition
	variables      map[string]any
	deadline       time.Time
	maxPages       int
	maxItems       int
	retries        int
	session        *browserSession
	items          []map[string]any
	pageCount      int
	trace          []map[string]any
	lastExtract    *actorStep
	currentURL     string
	startedAt      time.Time
	workerCtx      context.Context
	screenshot     *artifactSummary
	datasetJSONL   *artifactSummary
	datasetCSV     *artifactSummary
	traceArtifact  *artifactSummary
	datasetBytes   int
	datasetFull    bool
	selectedPreset string
}

func (a *App) toolActorRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "actor_id")
	rec, err := getActor(ctx, id, "")
	if err != nil || rec == nil {
		if err == nil {
			err = errors.New("actor not found")
		}
		return nil, err
	}
	if !rec.Enabled {
		return nil, errors.New("actor is disabled")
	}
	if revision := intArg(args, "revision"); revision > 0 && revision != rec.Revision {
		var definition string
		if err := ctx.AppDB().QueryRow(`SELECT definition_json FROM actors_versions WHERE project_id=? AND actor_id=? AND revision=?`, projectID(ctx), rec.ID, revision).Scan(&definition); err != nil {
			return nil, fmt.Errorf("actor revision not found: %w", err)
		}
		if err := json.Unmarshal([]byte(definition), &rec.Definition); err != nil {
			return nil, err
		}
		rec.Revision = revision
	}
	operation := firstNonEmpty(stringArg(args, "operation"), "run")
	selected, err := selectOperation(rec.Definition, operation)
	if err != nil {
		return nil, err
	}
	rec.Definition = selected
	trigger := actorTrigger(args)
	if key := strings.TrimSpace(stringArg(args, "idempotency_key")); key != "" {
		if len(key) > 200 || stringArg(args, "schedule_key") != "" {
			return nil, errors.New("idempotency_key must be at most 200 characters and cannot accompany schedule_key")
		}
		trigger["trigger_key"] = fmt.Sprintf("manual:%d:%s", rec.ID, key)
	}
	preset := strings.TrimSpace(stringArg(args, "preset"))
	presetPool, err := actorPresetPool(args["preset_pool"], rec.Definition.Presets)
	if err != nil {
		return nil, err
	}
	if preset != "" && len(presetPool) > 0 {
		return nil, errors.New("preset and preset_pool are mutually exclusive")
	}
	if len(presetPool) > 0 {
		if stringFromAny(trigger["kind"]) != "schedule" {
			return nil, errors.New("preset_pool requires a scheduled trigger")
		}
		preset = selectScheduledPreset(presetPool, stringFromAny(trigger["schedule_key"]), stringFromAny(trigger["bucket"]))
	}
	if preset != "" {
		if _, ok := rec.Definition.Presets[preset]; !ok {
			return nil, fmt.Errorf("preset %q not found", preset)
		}
	}
	inputJSON, _ := json.Marshal(map[string]any{
		"operation": operation, "preset": preset, "preset_pool": presetPool, "schedule_overrides": mapFromAny(args["schedule_overrides"]), "input": mapFromAny(args["input"]),
	})
	if len(inputJSON) > maxActorInputBytes {
		return nil, fmt.Errorf("run input exceeds %d bytes", maxActorInputBytes)
	}
	defJSON, _ := json.Marshal(rec.Definition)
	// Resolve templates and validate before accepting work or allocating a browser.
	if _, err := newActorExecution(context.Background(), a, ctx, &actorQueuedRun{InputJSON: string(inputJSON), DefinitionSnapshot: string(defJSON)}); err != nil {
		return nil, err
	}
	runID, status, duplicate, err := enqueueActorSnapshot(ctx, rec.ID, rec.Revision, string(inputJSON), string(defJSON), trigger)
	if err != nil {
		return nil, err
	}
	if !duplicate {
		ctx.Emit("actor.run.queued", map[string]any{"run_id": runID, "actor_id": rec.ID, "revision": rec.Revision})
	}
	return map[string]any{"run_id": runID, "status": status, "duplicate": duplicate}, nil
}

func selectScheduledPreset(pool []string, scheduleKey, bucket string) string {
	if len(pool) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(scheduleKey + ":" + bucket))
	index := binary.BigEndian.Uint64(sum[:8]) % uint64(len(pool))
	return pool[index]
}

func actorTrigger(args map[string]any) map[string]any {
	trigger := map[string]any{"kind": "manual", "queued_at": time.Now().UTC().Format(time.RFC3339)}
	scheduleKey := strings.TrimSpace(stringArg(args, "schedule_key"))
	if scheduleKey == "" {
		return trigger
	}
	trigger["kind"] = "schedule"
	trigger["schedule_key"] = scheduleKey
	bucket := strings.TrimSpace(stringArg(args, "trigger_bucket"))
	if job := mapFromAny(args["_job"]); bucket == "" && len(job) > 0 {
		bucket = stringFromAny(job["scheduled_for"])
		trigger["job"] = job
	}
	if bucket == "" {
		if seconds := intArg(args, "_schedule_every_seconds"); seconds > 0 {
			now := time.Now().UTC().Unix()
			bucket = time.Unix((now/int64(seconds))*int64(seconds), 0).UTC().Format(time.RFC3339)
		} else {
			bucket = time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)
		}
	}
	trigger["bucket"] = bucket
	trigger["trigger_key"] = fmt.Sprintf("schedule:%d:%s:%s", int64ArgLocal(args, "actor_id"), scheduleKey, bucket)
	return trigger
}

func enqueueActorSnapshot(ctx *sdk.AppCtx, actorID int64, revision int, inputJSON, definitionJSON string, trigger map[string]any) (int64, string, bool, error) {
	triggerJSON, _ := json.Marshal(trigger)
	triggerKey := stringFromAny(trigger["trigger_key"])
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return 0, "", false, err
	}
	defer tx.Rollback()
	if triggerKey != "" {
		var existing int64
		var status string
		var oldActor int64
		var oldRevision int
		var oldInput, oldDefinition string
		err := tx.QueryRow(`SELECT id,status,actor_id,actor_revision,input_json,definition_snapshot_json FROM actors_runs WHERE project_id=? AND json_extract(trigger_json,'$.trigger_key')=? LIMIT 1`, projectID(ctx), triggerKey).Scan(&existing, &status, &oldActor, &oldRevision, &oldInput, &oldDefinition)
		if err == nil {
			if oldActor != actorID || oldRevision != revision || oldInput != inputJSON || oldDefinition != definitionJSON {
				return 0, "", false, errors.New("idempotency conflict: key already used with different actor revision or inputs")
			}
			return existing, status, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, "", false, err
		}
	}
	res, err := tx.Exec(`INSERT INTO actors_runs(project_id,kind,input_json,status,actor_id,actor_revision,definition_snapshot_json,trigger_json) VALUES(?,?,?,'queued',?,?,?,?)`,
		projectID(ctx), "actor", inputJSON, actorID, revision, definitionJSON, string(triggerJSON))
	if err != nil {
		if triggerKey != "" {
			_ = tx.Rollback()
			var existing int64
			var status string
			var oldActor int64
			var oldRevision int
			var oldInput, oldDefinition string
			if findErr := ctx.AppDB().QueryRow(`SELECT id,status,actor_id,actor_revision,input_json,definition_snapshot_json FROM actors_runs WHERE project_id=? AND json_extract(trigger_json,'$.trigger_key')=? LIMIT 1`, projectID(ctx), triggerKey).Scan(&existing, &status, &oldActor, &oldRevision, &oldInput, &oldDefinition); findErr == nil {
				if oldActor != actorID || oldRevision != revision || oldInput != inputJSON || oldDefinition != definitionJSON {
					return 0, "", false, errors.New("idempotency conflict: key already used with different actor revision or inputs")
				}
				return existing, status, true, nil
			}
		}
		return 0, "", false, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return 0, "", false, err
	}
	return id, "queued", false, nil
}

func (a *App) toolActorRunGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	run, err := getActorRun(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"run": run, "found": run != nil}, nil
}

func getActorRun(ctx *sdk.AppCtx, id int64) (map[string]any, error) {
	var kind, status, inputJSON, outputJSON, errText, summary, definitionJSON, triggerJSON string
	var actorID sql.NullInt64
	var revision sql.NullInt64
	var created time.Time
	var completed, cancel sql.NullTime
	err := ctx.AppDB().QueryRow(`SELECT kind,status,input_json,COALESCE(output_json,'{}'),COALESCE(error,''),COALESCE(summary,''),COALESCE(actor_id,0),COALESCE(actor_revision,0),COALESCE(definition_snapshot_json,'{}'),COALESCE(trigger_json,'{}'),created_at,completed_at,cancel_requested_at FROM actors_runs WHERE id=? AND project_id=?`, id, projectID(ctx)).Scan(
		&kind, &status, &inputJSON, &outputJSON, &errText, &summary, &actorID, &revision, &definitionJSON, &triggerJSON, &created, &completed, &cancel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	run := map[string]any{"id": id, "kind": kind, "status": status, "error": errText, "summary": summary, "created_at": created.UTC().Format(time.RFC3339)}
	for key, raw := range map[string]string{"input": inputJSON, "output": outputJSON, "definition_snapshot": definitionJSON, "trigger": triggerJSON} {
		var decoded any
		if json.Unmarshal([]byte(raw), &decoded) == nil {
			run[key] = decoded
		}
	}
	if actorID.Int64 > 0 {
		run["actor_id"] = actorID.Int64
		run["actor_revision"] = revision.Int64
	}
	if completed.Valid {
		run["completed_at"] = completed.Time.UTC().Format(time.RFC3339)
		run["duration_ms"] = maxInt64(0, completed.Time.Sub(created).Milliseconds())
	}
	if cancel.Valid {
		run["cancel_requested_at"] = cancel.Time.UTC().Format(time.RFC3339)
	}
	rows, err := ctx.AppDB().Query(`SELECT id,kind,COALESCE(title,''),COALESCE(storage_id,0),COALESCE(storage_url,''),content_type,bytes,created_at FROM actors_artifacts WHERE project_id=? AND run_id=? ORDER BY id`, projectID(ctx), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	artifacts := []map[string]any{}
	for rows.Next() {
		var aid, sid int64
		var kind, title, storageURL, contentType string
		var size int
		var at time.Time
		if err := rows.Scan(&aid, &kind, &title, &sid, &storageURL, &contentType, &size, &at); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, map[string]any{"id": aid, "kind": kind, "title": title, "storage_id": sid, "url": storageURL, "content_type": contentType, "bytes": size, "created_at": at.UTC().Format(time.RFC3339)})
	}
	run["artifacts"] = artifacts
	return run, rows.Err()
}

func (a *App) toolActorRunCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	now := time.Now().UTC()
	res, err := ctx.AppDB().Exec(`UPDATE actors_runs SET cancel_requested_at=?,status=CASE WHEN status='queued' THEN 'cancelled' ELSE status END,completed_at=CASE WHEN status='queued' THEN ? ELSE completed_at END,error=CASE WHEN status='queued' THEN 'cancelled before execution' ELSE error END WHERE id=? AND project_id=? AND status IN ('queued','running')`, now, now, id, projectID(ctx))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	run, err := getActorRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		topic := "actor.run.cancel_requested"
		if run != nil && run["status"] == "cancelled" {
			topic = "actor.run.cancelled"
		}
		ctx.Emit(topic, map[string]any{"run_id": id})
	}
	return map[string]any{"cancel_requested": n > 0, "run": run}, nil
}

func (a *App) toolActorRunRetry(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	var actorID int64
	var revision int
	var inputJSON, definitionJSON, status string
	err := ctx.AppDB().QueryRow(`SELECT COALESCE(actor_id,0),COALESCE(actor_revision,0),input_json,COALESCE(definition_snapshot_json,''),status FROM actors_runs WHERE id=? AND project_id=?`, id, projectID(ctx)).Scan(&actorID, &revision, &inputJSON, &definitionJSON, &status)
	if err != nil {
		return nil, err
	}
	if actorID == 0 || definitionJSON == "" {
		return nil, errors.New("only actor runs can be retried")
	}
	if status == "queued" || status == "running" {
		return nil, errors.New("active run cannot be retried")
	}
	newID, newStatus, _, err := enqueueActorSnapshot(ctx, actorID, revision, inputJSON, definitionJSON, map[string]any{"kind": "retry", "retry_of": id, "queued_at": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return nil, err
	}
	ctx.Emit("actor.run.queued", map[string]any{"run_id": newID, "actor_id": actorID, "retry_of": id})
	return map[string]any{"run_id": newID, "status": newStatus, "retry_of": id}, nil
}

func (a *App) executeActorRun(workerCtx context.Context, ctx *sdk.AppCtx, queued *actorQueuedRun) error {
	exec, err := newActorExecution(workerCtx, a, ctx, queued)
	if err != nil {
		if finishErr := finishActorRun(ctx, queued.ID, "failed", nil, err); finishErr != nil {
			return finishErr
		}
		ctx.Emit("actor.run.failed", map[string]any{"run_id": queued.ID, "error": err.Error()})
		return nil
	}
	runCtx, cancel := context.WithDeadline(workerCtx, exec.deadline)
	defer cancel()
	exec.workerCtx = runCtx
	// Cancel in-flight Computer calls as well as checking between workflow steps.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				var requested sql.NullTime
				if err := ctx.AppDB().QueryRow(`SELECT cancel_requested_at FROM actors_runs WHERE id=? AND project_id=?`, queued.ID, projectID(ctx)).Scan(&requested); err == nil && requested.Valid {
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		if exec.session != nil {
			a.closeBrowser(ctx, exec.session.SessionID)
			exec.session = nil
		}
		_, _ = ctx.AppDB().Exec(`DELETE FROM actors_context_locks WHERE project_id=? AND run_id=?`, projectID(ctx), queued.ID)
	}()
	out, runErr := exec.runSteps()
	var requested sql.NullTime
	if err := ctx.AppDB().QueryRow(`SELECT cancel_requested_at FROM actors_runs WHERE id=? AND project_id=?`, queued.ID, projectID(ctx)).Scan(&requested); err == nil && requested.Valid {
		runErr = errActorCancelled
	}
	if runErr == nil {
		runErr = exec.checkpoint()
	}
	status := "completed"
	if errors.Is(runErr, errActorCancelled) {
		status = "cancelled"
	} else if runErr != nil {
		status = "failed"
	}
	if exec.session != nil {
		if shot, shotErr := exec.storeCurrentScreenshot("final browser state"); shotErr == nil {
			exec.screenshot = shot
		} else {
			exec.addTrace("screenshot", "warning", shotErr.Error(), nil)
		}
	}
	if trace, traceErr := storeBytesArtifact(ctx, queued.ID, "trace", "actor trace", "application/json", mustJSON(exec.trace), ".json"); traceErr == nil {
		exec.traceArtifact = trace
	} else if runErr == nil {
		runErr = fmt.Errorf("persist trace: %w", traceErr)
		status = "failed"
	}
	if out == nil {
		out = map[string]any{}
	}
	out["run_id"] = queued.ID
	out["actor_id"] = queued.ActorID
	out["actor_revision"] = queued.ActorRevision
	out["item_count"] = len(exec.items)
	out["page_count"] = exec.pageCount
	out["items"] = previewActorItems(exec.items)
	out["trace_preview"] = previewActorTrace(exec.trace)
	out["current_url"] = exec.currentURL
	if exec.session != nil {
		out["proxy"] = exec.session.Proxy
	}
	if exec.selectedPreset != "" {
		out["preset"] = exec.selectedPreset
	}
	if browser := actorBrowserAudit(exec.definition.Browser); len(browser) > 0 {
		out["browser_config"] = browser
	}
	if exec.datasetFull {
		out["dataset_truncated"] = true
	}
	if exec.datasetJSONL != nil {
		out["dataset_artifact_id"] = exec.datasetJSONL.ID
		out["dataset_url"] = exec.datasetJSONL.URL
	}
	if exec.datasetCSV != nil {
		out["csv_artifact_id"] = exec.datasetCSV.ID
		out["csv_url"] = exec.datasetCSV.URL
	}
	if exec.traceArtifact != nil {
		out["trace_artifact_id"] = exec.traceArtifact.ID
		out["trace_url"] = exec.traceArtifact.URL
	}
	if exec.screenshot != nil {
		out["screenshot_artifact_id"] = exec.screenshot.ID
		out["screenshot_url"] = exec.screenshot.URL
	}
	if err := finishActorRun(ctx, queued.ID, status, out, runErr); err != nil {
		return err
	}
	topic := "actor.run." + status
	payload := map[string]any{"run_id": queued.ID, "actor_id": queued.ActorID, "status": status, "item_count": len(exec.items), "page_count": exec.pageCount}
	if runErr != nil {
		payload["error"] = runErr.Error()
	}
	ctx.Emit(topic, payload)
	return nil
}

var errActorCancelled = errors.New("actor run cancelled")

func newActorExecution(workerCtx context.Context, app *App, ctx *sdk.AppCtx, run *actorQueuedRun) (*actorExecution, error) {
	var def actorDefinition
	if err := json.Unmarshal([]byte(run.DefinitionSnapshot), &def); err != nil {
		return nil, fmt.Errorf("decode definition snapshot: %w", err)
	}
	if err := validateActorDefinition(def); err != nil {
		return nil, err
	}
	var runInput struct {
		Preset            string         `json:"preset"`
		PresetPool        []string       `json:"preset_pool"`
		ScheduleOverrides map[string]any `json:"schedule_overrides"`
		Input             map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(run.InputJSON), &runInput); err != nil {
		return nil, fmt.Errorf("decode run input: %w", err)
	}
	vars := mergeActorMaps(nil, def.Defaults)
	if runInput.Preset != "" {
		preset, ok := def.Presets[runInput.Preset]
		if !ok {
			return nil, fmt.Errorf("preset %q not found", runInput.Preset)
		}
		vars = mergeActorMaps(vars, preset)
	}
	vars = mergeActorMaps(vars, runInput.ScheduleOverrides)
	vars = mergeActorMaps(vars, runInput.Input)
	rendered, err := renderActorDefinition(def, vars)
	if err != nil {
		return nil, err
	}
	maxPages := boundedInt(templateInt(rendered.Limits.MaxPages), defaultActorMaxPages, 1, maxActorPages)
	maxItems := boundedInt(templateInt(rendered.Limits.MaxItems), defaultActorMaxItems, 1, maxActorItems)
	maxSeconds := boundedInt(templateInt(rendered.Limits.MaxDurationSeconds), defaultActorMaxSeconds, 1, maxActorSeconds)
	retries := defaultActorRetries
	if rendered.Limits.StepRetries != nil {
		retries = clampInt(templateInt(rendered.Limits.StepRetries), 0, 10)
	}
	now := time.Now().UTC()
	return &actorExecution{app: app, ctx: ctx, run: run, definition: rendered, variables: vars, deadline: now.Add(time.Duration(maxSeconds) * time.Second), maxPages: maxPages, maxItems: maxItems, retries: retries, startedAt: now, workerCtx: workerCtx, items: []map[string]any{}, trace: []map[string]any{}, selectedPreset: runInput.Preset}, nil
}

func actorBrowserAudit(browser actorBrowser) map[string]any {
	out := map[string]any{}
	if browser.Backend != "" {
		out["backend"] = browser.Backend
	}
	if len(browser.Viewport) > 0 {
		out["viewport"] = browser.Viewport
	}
	if len(browser.Environment) > 0 {
		out["environment"] = browser.Environment
	}
	return out
}

func (e *actorExecution) runSteps() (map[string]any, error) {
	for index := range e.definition.Steps {
		if err := e.checkpoint(); err != nil {
			return nil, err
		}
		step := e.definition.Steps[index]
		started := time.Now()
		var err error
		for attempt := 0; attempt <= e.retries; attempt++ {
			err = e.runStep(step)
			if err == nil || errors.Is(err, errActorCancelled) || !retryableActorError(err) || step.Action == "click" || step.Action == "key" || step.Action == "paginate" {
				break
			}
			if attempt < e.retries {
				e.addTrace(step.Action, "retry", err.Error(), map[string]any{"step": index, "attempt": attempt + 1})
			}
		}
		if err != nil && step.Optional && !errors.Is(err, errActorCancelled) {
			e.addTrace(step.Action, "skipped", err.Error(), map[string]any{"step": index, "duration_ms": time.Since(started).Milliseconds()})
			continue
		}
		if err != nil {
			e.addTrace(step.Action, "failed", err.Error(), map[string]any{"step": index, "duration_ms": time.Since(started).Milliseconds()})
			return nil, err
		}
		e.addTrace(step.Action, "completed", "", map[string]any{"step": index, "duration_ms": time.Since(started).Milliseconds(), "item_count": len(e.items), "page_count": e.pageCount})
		e.persistProgress(fmt.Sprintf("step %d/%d: %s", index+1, len(e.definition.Steps), step.Action))
		e.ctx.Emit("actor.run.progress", map[string]any{"run_id": e.run.ID, "step": index + 1, "step_count": len(e.definition.Steps), "action": step.Action, "item_count": len(e.items), "page_count": e.pageCount})
	}
	if err := e.checkpoint(); err != nil {
		return nil, err
	}
	if len(e.items) > 0 {
		jsonl := encodeJSONL(e.items)
		art, err := storeBytesArtifact(e.ctx, e.run.ID, "dataset-jsonl", "actor dataset", "application/x-ndjson", jsonl, ".jsonl")
		if err != nil {
			return nil, fmt.Errorf("persist JSONL dataset: %w", err)
		}
		e.datasetJSONL = art
		csvBytes, err := encodeActorCSV(e.items, e.definition.OutputSchema)
		if err != nil {
			return nil, err
		}
		art, err = storeBytesArtifact(e.ctx, e.run.ID, "dataset-csv", "actor dataset", "text/csv", csvBytes, ".csv")
		if err != nil {
			return nil, fmt.Errorf("persist CSV dataset: %w", err)
		}
		e.datasetCSV = art
	}
	return map[string]any{}, nil
}

func retryableActorError(err error) bool {
	if err == nil || errors.Is(err, errActorCancelled) {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"allowed_hosts", "unsupported action", "invalid items selector", "required field", "output item", "requires an open browser", "locator not found", "computer returned backend", "computer returned proxy", "url assertion failed"} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	return true
}

func (e *actorExecution) runStep(step actorStep) error {
	switch step.Action {
	case "goto":
		return e.gotoURL(step.URL)
	case "click":
		return e.click(step.Locator)
	case "fill", "key", "scroll":
		return e.interact(step)
	case "assert_element":
		doc, err := e.extractDOM()
		if err != nil {
			return err
		}
		root, err := html.Parse(strings.NewReader(doc.HTML))
		if err != nil {
			return err
		}
		selector, err := cascadia.Compile(step.Locator.Selector)
		if err != nil {
			return err
		}
		if cascadia.Query(root, selector) == nil {
			return errors.New("assert_element failed: element not found; check account login and page layout")
		}
		return nil
	case "assert_url":
		return e.assertURL(step)
	case "wait":
		d := boundedInt(templateInt(step.Duration), 1000, 0, 30000)
		if e.session == nil {
			return errors.New("wait requires an open browser")
		}
		var out map[string]any
		return sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "wait", "duration": d}), &out)
	case "extract":
		e.lastExtract = &step
		return e.extractPage(step)
	case "paginate":
		return e.paginate(step)
	case "screenshot":
		_, err := e.storeCurrentScreenshot(firstNonEmpty(step.Label, "actor screenshot"))
		return err
	default:
		return fmt.Errorf("unsupported action %q", step.Action)
	}
}

func (e *actorExecution) gotoURL(target string) error {
	if !hostAllowed(target, e.definition.AllowedHosts) {
		return fmt.Errorf("URL host is not in allowed_hosts: %s", target)
	}
	if err := validateBrowserTarget(e.ctx, target); err != nil {
		return err
	}
	if e.session == nil {
		remainingSeconds := maxInt64(1, int64(math.Ceil(time.Until(e.deadline).Seconds())))
		if e.definition.Browser.Backend == "browserbase" {
			remainingSeconds = maxInt64(60, remainingSeconds)
		}
		args := map[string]any{"persist": e.definition.Browser.Persist, "timeout": remainingSeconds}
		if e.definition.Browser.ContextID != "" {
			if err := e.lockContext(); err != nil {
				return err
			}
			args["context_id"] = e.definition.Browser.ContextID
			args["persist"] = true
		}
		if e.definition.Browser.Backend != "" {
			args["backend"] = e.definition.Browser.Backend
		}
		if len(e.definition.Browser.Viewport) > 0 {
			args["viewport"] = e.definition.Browser.Viewport
		}
		if len(e.definition.Browser.Environment) > 0 {
			args["environment"] = e.definition.Browser.Environment
		}
		if mode := normalizedActorProxyMode(e.definition.Browser.ProxyMode); mode != "" {
			args["proxy_mode"] = mode
		}
		if e.definition.Browser.ProxyProfile != "" {
			args["proxy_profile"] = e.definition.Browser.ProxyProfile
		}
		if e.definition.Browser.ProxyCountry != "" {
			args["proxy_country"] = e.definition.Browser.ProxyCountry
		}
		if e.definition.Browser.ProxySticky != "" {
			args["proxy_sticky"] = e.definition.Browser.ProxySticky
		}
		session, err := e.app.openBrowser(e.workerCtx, e.ctx, target, args)
		if err != nil {
			return err
		}
		e.session = session
		e.currentURL = firstNonEmpty(session.CurrentURL, target)
		if e.definition.Browser.Backend != "" && session.Backend != e.definition.Browser.Backend {
			e.app.closeBrowser(e.ctx, session.SessionID)
			e.session = nil
			return fmt.Errorf("computer returned backend %q, expected %q", session.Backend, e.definition.Browser.Backend)
		}
		if err := e.validateResolvedProxy(session.Proxy); err != nil {
			e.app.closeBrowser(e.ctx, session.SessionID)
			e.session = nil
			return err
		}
		e.persistProgress("browser opened")
		return nil
	}
	var out map[string]any
	if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "navigate", "url": target}), &out); err != nil {
		return err
	}
	e.currentURL = firstNonEmpty(stringFromAny(out["current_url"]), target)
	e.persistProgress("navigated")
	return nil
}

func (e *actorExecution) click(locator actorLocator) error {
	if e.session == nil {
		return errors.New("click requires an open browser")
	}
	if selector := strings.TrimSpace(locator.Selector); selector != "" {
		var out map[string]any
		if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "click", "selector": selector}), &out); err != nil {
			return err
		}
		return e.finishInteraction(out)
	}
	var shot computerSOMScreenshot
	err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "screenshot", "annotate": true, "include_som": true}), &shot)
	if err == nil {
		for _, target := range shot.SOM {
			if locatorMatches(locator, target.Text, target.Role, "") {
				var out map[string]any
				if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "click", "label": target.Label}), &out); err != nil {
					return err
				}
				return e.finishInteraction(out)
			}
		}
	}
	doc, err := e.extractDOM()
	if err != nil {
		return err
	}
	region := findLocatorRegion(locator, doc.Regions)
	if region == nil {
		return fmt.Errorf("locator not found: text=%q role=%q selector=%q", locator.Text, locator.Role, locator.Selector)
	}
	if !region.Visible && region.Rect.Y > 0 {
		amount := boundedInt(int(region.Rect.Y)-100, 300, 100, 10000)
		var ignored map[string]any
		_ = sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "scroll", "direction": "down", "amount": amount}), &ignored)
		doc, err = e.extractDOM()
		if err == nil {
			region = findLocatorRegion(locator, doc.Regions)
		}
	}
	if region == nil {
		return errors.New("locator disappeared after scrolling")
	}
	rect := region.ViewportRect
	if rect.Width <= 0 || rect.Height <= 0 {
		rect = region.Rect
	}
	coordinate := fmt.Sprintf("%d,%d", int(rect.X+rect.Width/2), int(rect.Y+rect.Height/2))
	var out map[string]any
	if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "click", "coordinate": coordinate}), &out); err != nil {
		return err
	}
	return e.finishInteraction(out)
}

func (e *actorExecution) waitAfterInteraction() error {
	var out map[string]any
	if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "wait", "duration": 500}), &out); err != nil {
		return err
	}
	e.currentURL = firstNonEmpty(stringFromAny(out["current_url"]), e.currentURL)
	return nil
}

func (e *actorExecution) finishInteraction(out map[string]any) error {
	e.currentURL = firstNonEmpty(stringFromAny(out["current_url"]), e.currentURL)
	if err := e.waitAfterInteraction(); err != nil {
		return err
	}
	if !hostAllowed(e.currentURL, e.definition.AllowedHosts) {
		return fmt.Errorf("browser navigated outside allowed_hosts: %s", e.currentURL)
	}
	return nil
}

func (e *actorExecution) assertURL(step actorStep) error {
	if e.session == nil {
		return errors.New("assert_url requires an open browser")
	}
	if !hostAllowed(e.currentURL, []string{step.Host}) {
		return fmt.Errorf("URL assertion failed: host %q does not match %q", e.currentURL, step.Host)
	}
	parsed, err := url.Parse(e.currentURL)
	if err != nil {
		return fmt.Errorf("URL assertion failed: %w", err)
	}
	if step.PathPrefix != "" && !strings.HasPrefix(parsed.EscapedPath(), step.PathPrefix) {
		return fmt.Errorf("URL assertion failed: path %q does not start with %q", parsed.EscapedPath(), step.PathPrefix)
	}
	return nil
}

func (e *actorExecution) validateResolvedProxy(proxy browserProxyState) error {
	browser := e.definition.Browser
	expectedMode := normalizedActorProxyMode(browser.ProxyMode)
	if expectedMode != "" && proxy.Mode != expectedMode {
		return fmt.Errorf("computer returned proxy mode %q, expected %q", proxy.Mode, expectedMode)
	}
	if browser.ProxyCountry != "" && !strings.EqualFold(proxy.Country, browser.ProxyCountry) {
		return fmt.Errorf("computer returned proxy country %q, expected %q", proxy.Country, browser.ProxyCountry)
	}
	if browser.ProxyProfile != "" && proxy.ProfileID != browser.ProxyProfile && proxy.ProfileName != browser.ProxyProfile {
		return fmt.Errorf("computer returned proxy profile %q, expected %q", firstNonEmpty(proxy.ProfileName, proxy.ProfileID), browser.ProxyProfile)
	}
	if browser.ProxySticky != "" && proxy.StickyScope != browser.ProxySticky {
		return fmt.Errorf("computer returned proxy sticky policy %q, expected %q", proxy.StickyScope, browser.ProxySticky)
	}
	return nil
}

func locatorMatches(locator actorLocator, text, role, selector string) bool {
	if locator.Text != "" && !strings.Contains(strings.ToLower(strings.TrimSpace(text)), strings.ToLower(strings.TrimSpace(locator.Text))) {
		return false
	}
	if locator.Role != "" && !strings.EqualFold(strings.TrimSpace(role), strings.TrimSpace(locator.Role)) {
		return false
	}
	if locator.Selector != "" && strings.TrimSpace(selector) != strings.TrimSpace(locator.Selector) {
		return false
	}
	return locator.Text != "" || locator.Role != "" || locator.Selector != ""
}

func findLocatorRegion(locator actorLocator, regions []browserRegion) *browserRegion {
	for i := range regions {
		if locatorMatches(locator, firstNonEmpty(regions[i].Heading, regions[i].Text), regions[i].Role, regions[i].Selector) {
			return &regions[i]
		}
	}
	return nil
}

func (e *actorExecution) extractDOM() (*browserExtractResult, error) {
	if e.session == nil {
		return nil, errors.New("extract requires an open browser")
	}
	doc, err := e.app.extractBrowserDOM(e.workerCtx, e.ctx, e.session.SessionID, map[string]any{"formats": []string{"html", "regions", "metadata"}, "max_chars": 200000, "wait_ms": 250}, false)
	if err != nil {
		return nil, err
	}
	e.currentURL = firstNonEmpty(doc.CurrentURL, doc.URL, e.currentURL)
	if !hostAllowed(e.currentURL, e.definition.AllowedHosts) {
		return nil, fmt.Errorf("browser navigated outside allowed_hosts: %s", e.currentURL)
	}
	if !doc.Rendered {
		return nil, errors.New("computer did not return rendered browser content")
	}
	return doc, nil
}

func (e *actorExecution) extractPage(step actorStep) error {
	doc, err := e.extractDOM()
	if err != nil {
		return err
	}
	root, err := html.Parse(strings.NewReader(doc.HTML))
	if err != nil {
		return fmt.Errorf("parse rendered HTML: %w", err)
	}
	matcher, err := cascadia.Compile(step.Items)
	if err != nil {
		return fmt.Errorf("invalid items selector %q: %w", step.Items, err)
	}
	nodes := cascadia.QueryAll(root, matcher)
	pageItems := make([]map[string]any, 0, minInt(len(nodes), e.maxItems-len(e.items)))
	for _, node := range nodes {
		if len(e.items)+len(pageItems) >= e.maxItems {
			break
		}
		item, err := extractNodeItem(node, step.Fields, e.currentURL)
		if err != nil {
			return err
		}
		item, err = validateOutputItem(item, e.definition.OutputSchema)
		if err != nil {
			return fmt.Errorf("output item %d: %w", len(e.items)+len(pageItems)+1, err)
		}
		encoded, _ := json.Marshal(item)
		if len(encoded) > maxActorItemBytes {
			return fmt.Errorf("output item exceeds %d bytes", maxActorItemBytes)
		}
		if e.datasetBytes+len(encoded)+1 > maxActorDatasetBytes {
			e.datasetFull = true
			e.addTrace("extract", "limit", "dataset byte limit reached", map[string]any{"max_dataset_bytes": maxActorDatasetBytes})
			break
		}
		e.datasetBytes += len(encoded) + 1
		pageItems = append(pageItems, item)
	}
	if err := persistDatasetPage(e.ctx, e.run.ID, pageItems); err != nil {
		return err
	}
	e.items = append(e.items, pageItems...)
	e.pageCount++
	return nil
}

func (e *actorExecution) paginate(step actorStep) error {
	if e.lastExtract == nil {
		return errors.New("paginate requires a preceding extract step")
	}
	limit := boundedInt(templateInt(step.MaxPages), e.maxPages, 1, e.maxPages)
	for e.pageCount < limit && len(e.items) < e.maxItems && !e.datasetFull {
		if err := e.checkpoint(); err != nil {
			return err
		}
		if err := e.click(step.Locator); err != nil {
			if strings.Contains(err.Error(), "locator not found") {
				return nil
			}
			return err
		}
		if err := e.extractPage(*e.lastExtract); err != nil {
			return err
		}
		e.addTrace("paginate.page", "completed", "", map[string]any{"page_count": e.pageCount, "item_count": len(e.items)})
		e.ctx.Emit("actor.run.progress", map[string]any{"run_id": e.run.ID, "action": "paginate", "page_count": e.pageCount, "item_count": len(e.items)})
	}
	return nil
}

func (e *actorExecution) checkpoint() error {
	select {
	case <-e.workerCtx.Done():
		return e.workerCtx.Err()
	default:
	}
	if time.Now().After(e.deadline) {
		return errors.New("actor max_duration_seconds exceeded")
	}
	var requested sql.NullTime
	if err := e.ctx.AppDB().QueryRow(`SELECT cancel_requested_at FROM actors_runs WHERE id=? AND project_id=?`, e.run.ID, projectID(e.ctx)).Scan(&requested); err != nil {
		return err
	}
	if requested.Valid {
		return errActorCancelled
	}
	return nil
}

func (e *actorExecution) addTrace(action, status, message string, data map[string]any) {
	if len(e.trace) >= maxActorTraceEvents {
		return
	}
	event := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "elapsed_ms": time.Since(e.startedAt).Milliseconds(), "action": action, "status": status}
	if message != "" {
		event["message"] = truncateString(message, 2000)
	}
	for key, value := range data {
		event[key] = value
	}
	e.trace = append(e.trace, event)
}

func (e *actorExecution) persistProgress(message string) {
	progress := map[string]any{
		"item_count": len(e.items), "page_count": e.pageCount, "current_url": e.currentURL,
		"message": message, "updated_at": time.Now().UTC().Format(time.RFC3339),
	}
	if e.session != nil {
		progress["session_id"] = e.session.SessionID
		progress["backend"] = e.session.Backend
	}
	b, _ := json.Marshal(progress)
	_, _ = e.ctx.AppDB().Exec(`UPDATE actors_runs SET output_json=?,summary=? WHERE id=? AND project_id=? AND status='running'`, string(b), nullIfEmpty(message), e.run.ID, projectID(e.ctx))
}

func (e *actorExecution) storeCurrentScreenshot(title string) (*artifactSummary, error) {
	if e.session == nil {
		return nil, errors.New("screenshot requires an open browser")
	}
	var shot browserScreenshot
	if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "browser_screenshot", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID}), &shot); err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(shot.PNGB64)
	if err != nil {
		return nil, err
	}
	return storeBytesArtifact(e.ctx, e.run.ID, "screenshot", title, "image/png", b, ".png")
}

func extractNodeItem(node *html.Node, fields map[string]actorField, baseURL string) (map[string]any, error) {
	item := make(map[string]any, len(fields))
	for name, field := range fields {
		target := node
		if field.Selector != "" {
			matcher, err := cascadia.Compile(field.Selector)
			if err != nil {
				return nil, fmt.Errorf("field %s selector: %w", name, err)
			}
			target = cascadia.Query(node, matcher)
		}
		if target == nil {
			if field.Required {
				return nil, fmt.Errorf("required field %s was not found", name)
			}
			continue
		}
		var raw string
		if field.Attribute != "" {
			raw, _ = htmlAttribute(target, field.Attribute)
		} else if strings.HasPrefix(field.Type, "attr:") {
			raw, _ = htmlAttribute(target, strings.TrimPrefix(field.Type, "attr:"))
		} else {
			raw = htmlNodeText(target)
		}
		raw = strings.TrimSpace(raw)
		if raw == "" && field.Required {
			return nil, fmt.Errorf("required field %s is empty", name)
		}
		value, err := coerceActorValue(raw, field.Type, baseURL)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", name, err)
		}
		item[name] = value
	}
	return item, nil
}

func htmlNodeText(node *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "noscript") {
			return
		}
		if n.Type == html.TextNode {
			s := strings.TrimSpace(n.Data)
			if s != "" {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(s)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(b.String()), " ")
}

func htmlAttribute(node *html.Node, name string) (string, bool) {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val, true
		}
	}
	return "", false
}

func coerceActorValue(raw, typ, baseURL string) (any, error) {
	switch typ {
	case "", "text", "string":
		return raw, nil
	case "url":
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		if !u.IsAbs() {
			base, err := url.Parse(baseURL)
			if err != nil {
				return nil, err
			}
			u = base.ResolveReference(u)
		}
		return u.String(), nil
	case "money", "number":
		return parseActorNumber(raw)
	case "integer":
		n, err := parseActorNumber(raw)
		return int64(n), err
	case "boolean":
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true", "yes", "1", "on":
			return true, nil
		case "false", "no", "0", "off":
			return false, nil
		default:
			return nil, fmt.Errorf("%q is not a boolean", raw)
		}
	default:
		if strings.HasPrefix(typ, "attr:") {
			return raw, nil
		}
		return nil, fmt.Errorf("unsupported type %q", typ)
	}
}

func parseActorNumber(raw string) (float64, error) {
	match := actorNumberPattern.FindString(raw)
	if match == "" {
		return 0, fmt.Errorf("%q contains no number", raw)
	}
	s := strings.ReplaceAll(match, " ", "")
	lastComma, lastDot := strings.LastIndex(s, ","), strings.LastIndex(s, ".")
	if lastComma > lastDot {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	} else {
		s = strings.ReplaceAll(s, ",", "")
	}
	return strconv.ParseFloat(s, 64)
}

func validateOutputItem(item map[string]any, schema map[string]string) (map[string]any, error) {
	if len(schema) == 0 {
		return item, nil
	}
	out := make(map[string]any, len(schema))
	for name, typ := range schema {
		value, exists := item[name]
		if !exists || value == nil {
			continue
		}
		switch typ {
		case "string":
			if _, ok := value.(string); !ok {
				value = fmt.Sprint(value)
			}
		case "url":
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a URL string", name)
			}
			u, err := url.ParseRequestURI(s)
			if err != nil || u.Scheme == "" {
				return nil, fmt.Errorf("%s is not an absolute URL", name)
			}
		case "number":
			if _, ok := numericValue(value); !ok {
				return nil, fmt.Errorf("%s must be a number", name)
			}
		case "integer":
			n, ok := numericValue(value)
			if !ok || n != float64(int64(n)) {
				return nil, fmt.Errorf("%s must be an integer", name)
			}
			value = int64(n)
		case "boolean":
			if _, ok := value.(bool); !ok {
				return nil, fmt.Errorf("%s must be a boolean", name)
			}
		default:
			return nil, fmt.Errorf("unsupported output type %q", typ)
		}
		out[name] = value
	}
	return out, nil
}

func numericValue(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func mergeActorMaps(base, overlay map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if existing, ok := out[k].(map[string]any); ok {
			if next, ok := v.(map[string]any); ok {
				out[k] = mergeActorMaps(existing, next)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func renderActorDefinition(def actorDefinition, vars map[string]any) (actorDefinition, error) {
	b, _ := json.Marshal(def)
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return def, err
	}
	rendered, err := renderActorValue(generic, vars)
	if err != nil {
		return def, err
	}
	b, _ = json.Marshal(rendered)
	if err := json.Unmarshal(b, &def); err != nil {
		return def, err
	}
	return def, validateActorDefinition(def)
}

func renderActorValue(value any, vars map[string]any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			rendered, err := renderActorValue(item, vars)
			if err != nil {
				return nil, err
			}
			out[k] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			rendered, err := renderActorValue(item, vars)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	case string:
		trimmed := strings.TrimSpace(v)
		if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") && strings.Count(trimmed, "{{") == 1 {
			key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "{{"), "}}"))
			resolved, ok := actorVariable(vars, key)
			if !ok {
				return nil, fmt.Errorf("template variable %q is not defined", key)
			}
			return resolved, nil
		}
		out := v
		for start := strings.Index(out, "{{"); start >= 0; start = strings.Index(out, "{{") {
			end := strings.Index(out[start+2:], "}}")
			if end < 0 {
				return nil, fmt.Errorf("unterminated template in %q", v)
			}
			end += start + 2
			key := strings.TrimSpace(out[start+2 : end])
			resolved, ok := actorVariable(vars, key)
			if !ok {
				return nil, fmt.Errorf("template variable %q is not defined", key)
			}
			out = out[:start] + fmt.Sprint(resolved) + out[end+2:]
		}
		return out, nil
	default:
		return value, nil
	}
}

func actorVariable(vars map[string]any, path string) (any, bool) {
	var current any = vars
	for _, part := range strings.Split(path, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func templateInt(value any) int { return intFromAny(value) }

func encodeJSONL(items []map[string]any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, item := range items {
		_ = enc.Encode(item)
	}
	return b.Bytes()
}

func encodeActorCSV(items []map[string]any, schema map[string]string) ([]byte, error) {
	fields := sortedSchemaFields(schema)
	if len(fields) == 0 && len(items) > 0 {
		for key := range items[0] {
			fields = append(fields, key)
		}
		sort.Strings(fields)
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.Write(fields); err != nil {
		return nil, err
	}
	for _, item := range items {
		row := make([]string, len(fields))
		for i, field := range fields {
			if item[field] != nil {
				row[i] = actorCSVCell(item[field])
			}
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return b.Bytes(), w.Error()
}

func actorCSVCell(value any) string {
	text := fmt.Sprint(value)
	if _, isString := value.(string); isString && text != "" && strings.ContainsRune("=+-@\t\r", rune(text[0])) {
		return "'" + text
	}
	return text
}

func previewActorItems(items []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, minInt(len(items), maxActorPreviewItems))
	totalBytes := 0
	for _, item := range items {
		preview := make(map[string]any, len(item))
		for key, value := range item {
			if text, ok := value.(string); ok {
				preview[key] = truncateString(text, 2000)
			} else {
				preview[key] = value
			}
		}
		encoded, _ := json.Marshal(preview)
		if totalBytes+len(encoded) > maxActorPreviewBytes {
			break
		}
		totalBytes += len(encoded)
		out = append(out, preview)
		if len(out) >= maxActorPreviewItems {
			break
		}
	}
	return out
}

func previewActorTrace(trace []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, minInt(len(trace), 20))
	totalBytes := 0
	for _, event := range trace {
		preview := map[string]any{}
		for _, key := range []string{"at", "elapsed_ms", "action", "status", "message", "step", "attempt", "duration_ms", "item_count", "page_count"} {
			if value, ok := event[key]; ok {
				if text, isText := value.(string); isText {
					preview[key] = truncateString(text, 500)
				} else {
					preview[key] = value
				}
			}
		}
		encoded, _ := json.Marshal(preview)
		if totalBytes+len(encoded) > maxActorPreviewBytes {
			break
		}
		totalBytes += len(encoded)
		out = append(out, preview)
		if len(out) >= 20 {
			break
		}
	}
	return out
}

func storeBytesArtifact(ctx *sdk.AppCtx, runID int64, kind, title, contentType string, data []byte, extension string) (*artifactSummary, error) {
	folder := "/.actors/" + kind + "/" + time.Now().UTC().Format("2006-01")
	var up struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	name := safeFilename(firstNonEmpty(title, kind)) + "-" + randName() + extension
	args := withProjectID(ctx, map[string]any{"name": name, "content_base64": base64.StdEncoding.EncodeToString(data), "folder": folder, "content_type": contentType, "source": "actors:" + kind, "visibility": "private"})
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", args, &up); err != nil {
		return nil, fmt.Errorf("storage.files_upload: %w", err)
	}
	art, err := insertArtifact(ctx, runID, kind, "", title, up.ID, up.URL, contentType, len(data), compactArtifactMetadata(kind, "", title, contentType, len(data)))
	if err != nil {
		rollbackStoredFile(ctx, up.ID)
		return nil, err
	}
	return art, nil
}

func mustJSON(value any) []byte { b, _ := json.Marshal(value); return b }

func finishActorRun(ctx *sdk.AppCtx, runID int64, status string, output map[string]any, runErr error) error {
	var outputJSON any
	if output != nil {
		b, _ := json.Marshal(output)
		outputJSON = string(b)
	}
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	summary := ""
	if output != nil {
		summary = fmt.Sprintf("%d items across %d pages", intFromAny(output["item_count"]), intFromAny(output["page_count"]))
	}
	res, err := ctx.AppDB().Exec(`UPDATE actors_runs SET status=?,output_json=?,summary=?,error=?,completed_at=? WHERE id=? AND project_id=?`, status, outputJSON, nullIfEmpty(summary), nullIfEmpty(errText), time.Now().UTC(), runID, projectID(ctx))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("run %d could not be finalized", runID)
	}
	return nil
}
