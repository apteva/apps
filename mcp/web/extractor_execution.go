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

var extractorNumberPattern = regexp.MustCompile(`[-+]?\d[\d\s.,]*`)

type extractorExecution struct {
	app            *App
	ctx            *sdk.AppCtx
	run            *extractorQueuedRun
	definition     extractorDefinition
	variables      map[string]any
	deadline       time.Time
	maxPages       int
	maxItems       int
	retries        int
	session        *browserSession
	items          []map[string]any
	pageCount      int
	trace          []map[string]any
	lastExtract    *extractorStep
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

func (a *App) toolExtractorRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "extractor_id")
	rec, err := getExtractor(ctx, id, "")
	if err != nil || rec == nil {
		if err == nil {
			err = errors.New("extractor not found")
		}
		return nil, err
	}
	if !rec.Enabled {
		return nil, errors.New("extractor is disabled")
	}
	trigger := extractorTrigger(args)
	preset := strings.TrimSpace(stringArg(args, "preset"))
	presetPool, err := extractorPresetPool(args["preset_pool"], rec.Definition.Presets)
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
		"preset": preset, "preset_pool": presetPool, "schedule_overrides": mapFromAny(args["schedule_overrides"]), "input": mapFromAny(args["input"]),
	})
	if len(inputJSON) > maxExtractorInputBytes {
		return nil, fmt.Errorf("run input exceeds %d bytes", maxExtractorInputBytes)
	}
	defJSON, _ := json.Marshal(rec.Definition)
	runID, status, duplicate, err := enqueueExtractorSnapshot(ctx, rec.ID, rec.Revision, string(inputJSON), string(defJSON), trigger)
	if err != nil {
		return nil, err
	}
	if !duplicate {
		ctx.Emit("extractor.run.queued", map[string]any{"run_id": runID, "extractor_id": rec.ID, "revision": rec.Revision})
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

func extractorTrigger(args map[string]any) map[string]any {
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
	trigger["trigger_key"] = scheduleKey + ":" + bucket
	return trigger
}

func enqueueExtractorSnapshot(ctx *sdk.AppCtx, extractorID int64, revision int, inputJSON, definitionJSON string, trigger map[string]any) (int64, string, bool, error) {
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
		err := tx.QueryRow(`SELECT id,status FROM web_runs WHERE project_id=? AND json_extract(trigger_json,'$.trigger_key')=? LIMIT 1`, projectID(ctx), triggerKey).Scan(&existing, &status)
		if err == nil {
			return existing, status, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, "", false, err
		}
	}
	res, err := tx.Exec(`INSERT INTO web_runs(project_id,kind,input_json,status,extractor_id,extractor_revision,definition_snapshot_json,trigger_json) VALUES(?,?,?,'queued',?,?,?,?)`,
		projectID(ctx), "extractor", inputJSON, extractorID, revision, definitionJSON, string(triggerJSON))
	if err != nil {
		if triggerKey != "" {
			_ = tx.Rollback()
			var existing int64
			var status string
			if findErr := ctx.AppDB().QueryRow(`SELECT id,status FROM web_runs WHERE project_id=? AND json_extract(trigger_json,'$.trigger_key')=? LIMIT 1`, projectID(ctx), triggerKey).Scan(&existing, &status); findErr == nil {
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

func (a *App) toolWebRunGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	run, err := getWebRun(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"run": run, "found": run != nil}, nil
}

func getWebRun(ctx *sdk.AppCtx, id int64) (map[string]any, error) {
	var kind, status, inputJSON, outputJSON, errText, summary, definitionJSON, triggerJSON string
	var extractorID sql.NullInt64
	var revision sql.NullInt64
	var created time.Time
	var completed, cancel sql.NullTime
	err := ctx.AppDB().QueryRow(`SELECT kind,status,input_json,COALESCE(output_json,'{}'),COALESCE(error,''),COALESCE(summary,''),COALESCE(extractor_id,0),COALESCE(extractor_revision,0),COALESCE(definition_snapshot_json,'{}'),COALESCE(trigger_json,'{}'),created_at,completed_at,cancel_requested_at FROM web_runs WHERE id=? AND project_id=?`, id, projectID(ctx)).Scan(
		&kind, &status, &inputJSON, &outputJSON, &errText, &summary, &extractorID, &revision, &definitionJSON, &triggerJSON, &created, &completed, &cancel)
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
	if extractorID.Int64 > 0 {
		run["extractor_id"] = extractorID.Int64
		run["extractor_revision"] = revision.Int64
	}
	if completed.Valid {
		run["completed_at"] = completed.Time.UTC().Format(time.RFC3339)
		run["duration_ms"] = maxInt64(0, completed.Time.Sub(created).Milliseconds())
	}
	if cancel.Valid {
		run["cancel_requested_at"] = cancel.Time.UTC().Format(time.RFC3339)
	}
	rows, err := ctx.AppDB().Query(`SELECT id,kind,COALESCE(title,''),COALESCE(storage_id,0),COALESCE(storage_url,''),content_type,bytes,created_at FROM web_artifacts WHERE project_id=? AND run_id=? ORDER BY id`, projectID(ctx), id)
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

func (a *App) toolWebRunCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	now := time.Now().UTC()
	res, err := ctx.AppDB().Exec(`UPDATE web_runs SET cancel_requested_at=?,status=CASE WHEN status='queued' THEN 'cancelled' ELSE status END,completed_at=CASE WHEN status='queued' THEN ? ELSE completed_at END,error=CASE WHEN status='queued' THEN 'cancelled before execution' ELSE error END WHERE id=? AND project_id=? AND status IN ('queued','running')`, now, now, id, projectID(ctx))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	run, err := getWebRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		topic := "extractor.run.cancel_requested"
		if run != nil && run["status"] == "cancelled" {
			topic = "extractor.run.cancelled"
		}
		ctx.Emit(topic, map[string]any{"run_id": id})
	}
	return map[string]any{"cancel_requested": n > 0, "run": run}, nil
}

func (a *App) toolWebRunRetry(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	var extractorID int64
	var revision int
	var inputJSON, definitionJSON, status string
	err := ctx.AppDB().QueryRow(`SELECT COALESCE(extractor_id,0),COALESCE(extractor_revision,0),input_json,COALESCE(definition_snapshot_json,''),status FROM web_runs WHERE id=? AND project_id=?`, id, projectID(ctx)).Scan(&extractorID, &revision, &inputJSON, &definitionJSON, &status)
	if err != nil {
		return nil, err
	}
	if extractorID == 0 || definitionJSON == "" {
		return nil, errors.New("only extractor runs can be retried")
	}
	if status == "queued" || status == "running" {
		return nil, errors.New("active run cannot be retried")
	}
	newID, newStatus, _, err := enqueueExtractorSnapshot(ctx, extractorID, revision, inputJSON, definitionJSON, map[string]any{"kind": "retry", "retry_of": id, "queued_at": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return nil, err
	}
	ctx.Emit("extractor.run.queued", map[string]any{"run_id": newID, "extractor_id": extractorID, "retry_of": id})
	return map[string]any{"run_id": newID, "status": newStatus, "retry_of": id}, nil
}

func (a *App) executeExtractorRun(workerCtx context.Context, ctx *sdk.AppCtx, queued *extractorQueuedRun) error {
	exec, err := newExtractorExecution(workerCtx, a, ctx, queued)
	if err != nil {
		if finishErr := finishExtractorRun(ctx, queued.ID, "failed", nil, err); finishErr != nil {
			return finishErr
		}
		ctx.Emit("extractor.run.failed", map[string]any{"run_id": queued.ID, "error": err.Error()})
		return nil
	}
	defer func() {
		if exec.session != nil {
			a.closeBrowser(ctx, exec.session.SessionID)
			exec.session = nil
		}
	}()
	out, runErr := exec.runSteps()
	if runErr == nil {
		runErr = exec.checkpoint()
	}
	status := "completed"
	if errors.Is(runErr, errExtractorCancelled) {
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
	if trace, traceErr := storeBytesArtifact(ctx, queued.ID, "trace", "extractor trace", "application/json", mustJSON(exec.trace), ".json"); traceErr == nil {
		exec.traceArtifact = trace
	} else if runErr == nil {
		runErr = fmt.Errorf("persist trace: %w", traceErr)
		status = "failed"
	}
	if out == nil {
		out = map[string]any{}
	}
	out["run_id"] = queued.ID
	out["extractor_id"] = queued.ExtractorID
	out["extractor_revision"] = queued.ExtractorRevision
	out["item_count"] = len(exec.items)
	out["page_count"] = exec.pageCount
	out["items"] = previewExtractorItems(exec.items)
	out["trace_preview"] = previewExtractorTrace(exec.trace)
	out["current_url"] = exec.currentURL
	if exec.session != nil {
		out["proxy"] = exec.session.Proxy
	}
	if exec.selectedPreset != "" {
		out["preset"] = exec.selectedPreset
	}
	if browser := extractorBrowserAudit(exec.definition.Browser); len(browser) > 0 {
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
	if err := finishExtractorRun(ctx, queued.ID, status, out, runErr); err != nil {
		return err
	}
	topic := "extractor.run." + status
	payload := map[string]any{"run_id": queued.ID, "extractor_id": queued.ExtractorID, "status": status, "item_count": len(exec.items), "page_count": exec.pageCount}
	if runErr != nil {
		payload["error"] = runErr.Error()
	}
	ctx.Emit(topic, payload)
	return nil
}

var errExtractorCancelled = errors.New("extractor run cancelled")

func newExtractorExecution(workerCtx context.Context, app *App, ctx *sdk.AppCtx, run *extractorQueuedRun) (*extractorExecution, error) {
	var def extractorDefinition
	if err := json.Unmarshal([]byte(run.DefinitionSnapshot), &def); err != nil {
		return nil, fmt.Errorf("decode definition snapshot: %w", err)
	}
	if err := validateExtractorDefinition(def); err != nil {
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
	vars := mergeExtractorMaps(nil, def.Defaults)
	if runInput.Preset != "" {
		preset, ok := def.Presets[runInput.Preset]
		if !ok {
			return nil, fmt.Errorf("preset %q not found", runInput.Preset)
		}
		vars = mergeExtractorMaps(vars, preset)
	}
	vars = mergeExtractorMaps(vars, runInput.ScheduleOverrides)
	vars = mergeExtractorMaps(vars, runInput.Input)
	rendered, err := renderExtractorDefinition(def, vars)
	if err != nil {
		return nil, err
	}
	maxPages := boundedInt(templateInt(rendered.Limits.MaxPages), defaultExtractorMaxPages, 1, maxExtractorPages)
	maxItems := boundedInt(templateInt(rendered.Limits.MaxItems), defaultExtractorMaxItems, 1, maxExtractorItems)
	maxSeconds := boundedInt(templateInt(rendered.Limits.MaxDurationSeconds), defaultExtractorMaxSeconds, 1, maxExtractorSeconds)
	retries := defaultExtractorRetries
	if rendered.Limits.StepRetries != nil {
		retries = clampInt(templateInt(rendered.Limits.StepRetries), 0, 10)
	}
	now := time.Now().UTC()
	return &extractorExecution{app: app, ctx: ctx, run: run, definition: rendered, variables: vars, deadline: now.Add(time.Duration(maxSeconds) * time.Second), maxPages: maxPages, maxItems: maxItems, retries: retries, startedAt: now, workerCtx: workerCtx, items: []map[string]any{}, trace: []map[string]any{}, selectedPreset: runInput.Preset}, nil
}

func extractorBrowserAudit(browser extractorBrowser) map[string]any {
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

func (e *extractorExecution) runSteps() (map[string]any, error) {
	for index := range e.definition.Steps {
		if err := e.checkpoint(); err != nil {
			return nil, err
		}
		step := e.definition.Steps[index]
		started := time.Now()
		var err error
		for attempt := 0; attempt <= e.retries; attempt++ {
			err = e.runStep(step)
			if err == nil || errors.Is(err, errExtractorCancelled) || !retryableExtractorError(err) {
				break
			}
			if attempt < e.retries {
				e.addTrace(step.Action, "retry", err.Error(), map[string]any{"step": index, "attempt": attempt + 1})
			}
		}
		if err != nil && step.Optional && !errors.Is(err, errExtractorCancelled) {
			e.addTrace(step.Action, "skipped", err.Error(), map[string]any{"step": index, "duration_ms": time.Since(started).Milliseconds()})
			continue
		}
		if err != nil {
			e.addTrace(step.Action, "failed", err.Error(), map[string]any{"step": index, "duration_ms": time.Since(started).Milliseconds()})
			return nil, err
		}
		e.addTrace(step.Action, "completed", "", map[string]any{"step": index, "duration_ms": time.Since(started).Milliseconds(), "item_count": len(e.items), "page_count": e.pageCount})
		e.persistProgress(fmt.Sprintf("step %d/%d: %s", index+1, len(e.definition.Steps), step.Action))
		e.ctx.Emit("extractor.run.progress", map[string]any{"run_id": e.run.ID, "step": index + 1, "step_count": len(e.definition.Steps), "action": step.Action, "item_count": len(e.items), "page_count": e.pageCount})
	}
	if err := e.checkpoint(); err != nil {
		return nil, err
	}
	if len(e.items) > 0 {
		jsonl := encodeJSONL(e.items)
		art, err := storeBytesArtifact(e.ctx, e.run.ID, "dataset-jsonl", "extractor dataset", "application/x-ndjson", jsonl, ".jsonl")
		if err != nil {
			return nil, fmt.Errorf("persist JSONL dataset: %w", err)
		}
		e.datasetJSONL = art
		csvBytes, err := encodeExtractorCSV(e.items, e.definition.OutputSchema)
		if err != nil {
			return nil, err
		}
		art, err = storeBytesArtifact(e.ctx, e.run.ID, "dataset-csv", "extractor dataset", "text/csv", csvBytes, ".csv")
		if err != nil {
			return nil, fmt.Errorf("persist CSV dataset: %w", err)
		}
		e.datasetCSV = art
	}
	return map[string]any{}, nil
}

func retryableExtractorError(err error) bool {
	if err == nil || errors.Is(err, errExtractorCancelled) {
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

func (e *extractorExecution) runStep(step extractorStep) error {
	switch step.Action {
	case "goto":
		return e.gotoURL(step.URL)
	case "click":
		return e.click(step.Locator)
	case "assert_url":
		return e.assertURL(step)
	case "wait":
		d := boundedInt(templateInt(step.Duration), 1000, 0, 30000)
		if e.session == nil {
			return errors.New("wait requires an open browser")
		}
		var out map[string]any
		return e.ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "wait", "duration": d}), &out)
	case "extract":
		e.lastExtract = &step
		return e.extractPage(step)
	case "paginate":
		return e.paginate(step)
	case "screenshot":
		_, err := e.storeCurrentScreenshot(firstNonEmpty(step.Label, "extractor screenshot"))
		return err
	default:
		return fmt.Errorf("unsupported action %q", step.Action)
	}
}

func (e *extractorExecution) gotoURL(target string) error {
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
		if e.definition.Browser.Backend != "" {
			args["backend"] = e.definition.Browser.Backend
		}
		if len(e.definition.Browser.Viewport) > 0 {
			args["viewport"] = e.definition.Browser.Viewport
		}
		if len(e.definition.Browser.Environment) > 0 {
			args["environment"] = e.definition.Browser.Environment
		}
		if mode := normalizedExtractorProxyMode(e.definition.Browser.ProxyMode); mode != "" {
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
		session, err := e.app.openBrowser(e.ctx, target, args)
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
	if err := e.ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "navigate", "url": target}), &out); err != nil {
		return err
	}
	e.currentURL = firstNonEmpty(stringFromAny(out["current_url"]), target)
	e.persistProgress("navigated")
	return nil
}

func (e *extractorExecution) click(locator extractorLocator) error {
	if e.session == nil {
		return errors.New("click requires an open browser")
	}
	if selector := strings.TrimSpace(locator.Selector); selector != "" {
		var out map[string]any
		if err := e.ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "click", "selector": selector}), &out); err != nil {
			return err
		}
		return e.finishInteraction(out)
	}
	var shot computerSOMScreenshot
	err := e.ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "screenshot", "annotate": true, "include_som": true}), &shot)
	if err == nil {
		for _, target := range shot.SOM {
			if locatorMatches(locator, target.Text, target.Role, "") {
				var out map[string]any
				if err := e.ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "click", "label": target.Label}), &out); err != nil {
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
		_ = e.ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "scroll", "direction": "down", "amount": amount}), &ignored)
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
	if err := e.ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "click", "coordinate": coordinate}), &out); err != nil {
		return err
	}
	return e.finishInteraction(out)
}

func (e *extractorExecution) waitAfterInteraction() error {
	var out map[string]any
	if err := e.ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "wait", "duration": 500}), &out); err != nil {
		return err
	}
	e.currentURL = firstNonEmpty(stringFromAny(out["current_url"]), e.currentURL)
	return nil
}

func (e *extractorExecution) finishInteraction(out map[string]any) error {
	e.currentURL = firstNonEmpty(stringFromAny(out["current_url"]), e.currentURL)
	if err := e.waitAfterInteraction(); err != nil {
		return err
	}
	if !hostAllowed(e.currentURL, e.definition.AllowedHosts) {
		return fmt.Errorf("browser navigated outside allowed_hosts: %s", e.currentURL)
	}
	return nil
}

func (e *extractorExecution) assertURL(step extractorStep) error {
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

func (e *extractorExecution) validateResolvedProxy(proxy browserProxyState) error {
	browser := e.definition.Browser
	expectedMode := normalizedExtractorProxyMode(browser.ProxyMode)
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

func locatorMatches(locator extractorLocator, text, role, selector string) bool {
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

func findLocatorRegion(locator extractorLocator, regions []browserRegion) *browserRegion {
	for i := range regions {
		if locatorMatches(locator, firstNonEmpty(regions[i].Heading, regions[i].Text), regions[i].Role, regions[i].Selector) {
			return &regions[i]
		}
	}
	return nil
}

func (e *extractorExecution) extractDOM() (*browserExtractResult, error) {
	if e.session == nil {
		return nil, errors.New("extract requires an open browser")
	}
	doc, err := e.app.extractBrowserDOM(e.ctx, e.session.SessionID, map[string]any{"formats": []string{"html", "regions", "metadata"}, "max_chars": 200000, "wait_ms": 250}, false)
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

func (e *extractorExecution) extractPage(step extractorStep) error {
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
		if e.datasetBytes+len(encoded)+1 > maxExtractorDatasetBytes {
			e.datasetFull = true
			e.addTrace("extract", "limit", "dataset byte limit reached", map[string]any{"max_dataset_bytes": maxExtractorDatasetBytes})
			break
		}
		e.datasetBytes += len(encoded) + 1
		pageItems = append(pageItems, item)
	}
	e.items = append(e.items, pageItems...)
	e.pageCount++
	return nil
}

func (e *extractorExecution) paginate(step extractorStep) error {
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
		e.ctx.Emit("extractor.run.progress", map[string]any{"run_id": e.run.ID, "action": "paginate", "page_count": e.pageCount, "item_count": len(e.items)})
	}
	return nil
}

func (e *extractorExecution) checkpoint() error {
	select {
	case <-e.workerCtx.Done():
		return e.workerCtx.Err()
	default:
	}
	if time.Now().After(e.deadline) {
		return errors.New("extractor max_duration_seconds exceeded")
	}
	var requested sql.NullTime
	if err := e.ctx.AppDB().QueryRow(`SELECT cancel_requested_at FROM web_runs WHERE id=? AND project_id=?`, e.run.ID, projectID(e.ctx)).Scan(&requested); err != nil {
		return err
	}
	if requested.Valid {
		return errExtractorCancelled
	}
	return nil
}

func (e *extractorExecution) addTrace(action, status, message string, data map[string]any) {
	if len(e.trace) >= maxExtractorTraceEvents {
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

func (e *extractorExecution) persistProgress(message string) {
	progress := map[string]any{
		"item_count": len(e.items), "page_count": e.pageCount, "current_url": e.currentURL,
		"message": message, "updated_at": time.Now().UTC().Format(time.RFC3339),
	}
	if e.session != nil {
		progress["session_id"] = e.session.SessionID
		progress["backend"] = e.session.Backend
	}
	b, _ := json.Marshal(progress)
	_, _ = e.ctx.AppDB().Exec(`UPDATE web_runs SET output_json=?,summary=? WHERE id=? AND project_id=? AND status='running'`, string(b), nullIfEmpty(message), e.run.ID, projectID(e.ctx))
}

func (e *extractorExecution) storeCurrentScreenshot(title string) (*artifactSummary, error) {
	if e.session == nil {
		return nil, errors.New("screenshot requires an open browser")
	}
	var shot browserScreenshot
	if err := e.ctx.PlatformAPI().CallAppResult("computer", "browser_screenshot", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID}), &shot); err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(shot.PNGB64)
	if err != nil {
		return nil, err
	}
	return storeBytesArtifact(e.ctx, e.run.ID, "screenshot", title, "image/png", b, ".png")
}

func extractNodeItem(node *html.Node, fields map[string]extractorField, baseURL string) (map[string]any, error) {
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
		value, err := coerceExtractorValue(raw, field.Type, baseURL)
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

func coerceExtractorValue(raw, typ, baseURL string) (any, error) {
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
		return parseExtractorNumber(raw)
	case "integer":
		n, err := parseExtractorNumber(raw)
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

func parseExtractorNumber(raw string) (float64, error) {
	match := extractorNumberPattern.FindString(raw)
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

func mergeExtractorMaps(base, overlay map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if existing, ok := out[k].(map[string]any); ok {
			if next, ok := v.(map[string]any); ok {
				out[k] = mergeExtractorMaps(existing, next)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func renderExtractorDefinition(def extractorDefinition, vars map[string]any) (extractorDefinition, error) {
	b, _ := json.Marshal(def)
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return def, err
	}
	rendered, err := renderExtractorValue(generic, vars)
	if err != nil {
		return def, err
	}
	b, _ = json.Marshal(rendered)
	if err := json.Unmarshal(b, &def); err != nil {
		return def, err
	}
	return def, validateExtractorDefinition(def)
}

func renderExtractorValue(value any, vars map[string]any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			rendered, err := renderExtractorValue(item, vars)
			if err != nil {
				return nil, err
			}
			out[k] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			rendered, err := renderExtractorValue(item, vars)
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
			resolved, ok := extractorVariable(vars, key)
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
			resolved, ok := extractorVariable(vars, key)
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

func extractorVariable(vars map[string]any, path string) (any, bool) {
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

func encodeExtractorCSV(items []map[string]any, schema map[string]string) ([]byte, error) {
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
				row[i] = extractorCSVCell(item[field])
			}
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return b.Bytes(), w.Error()
}

func extractorCSVCell(value any) string {
	text := fmt.Sprint(value)
	if _, isString := value.(string); isString && text != "" && strings.ContainsRune("=+-@\t\r", rune(text[0])) {
		return "'" + text
	}
	return text
}

func previewExtractorItems(items []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, minInt(len(items), maxExtractorPreviewItems))
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
		if totalBytes+len(encoded) > maxExtractorPreviewBytes {
			break
		}
		totalBytes += len(encoded)
		out = append(out, preview)
		if len(out) >= maxExtractorPreviewItems {
			break
		}
	}
	return out
}

func previewExtractorTrace(trace []map[string]any) []map[string]any {
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
		if totalBytes+len(encoded) > maxExtractorPreviewBytes {
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
	folder := "/.web/" + kind + "/" + time.Now().UTC().Format("2006-01")
	var up struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	name := safeFilename(firstNonEmpty(title, kind)) + "-" + randName() + extension
	args := withProjectID(ctx, map[string]any{"name": name, "content_base64": base64.StdEncoding.EncodeToString(data), "folder": folder, "content_type": contentType, "source": "web:" + kind})
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

func finishExtractorRun(ctx *sdk.AppCtx, runID int64, status string, output map[string]any, runErr error) error {
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
	res, err := ctx.AppDB().Exec(`UPDATE web_runs SET status=?,output_json=?,summary=?,error=?,completed_at=? WHERE id=? AND project_id=?`, status, outputJSON, nullIfEmpty(summary), nullIfEmpty(errText), time.Now().UTC(), runID, projectID(ctx))
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
