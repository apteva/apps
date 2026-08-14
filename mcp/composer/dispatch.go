package main

import (
	"context"
	stdBase64 "encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func projectScope(ctx *sdk.AppCtx) string {
	if ctx != nil {
		if pid := strings.TrimSpace(ctx.CurrentProject()); pid != "" {
			return pid
		}
	}
	return strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
}

func projectScopeFromArgs(ctx *sdk.AppCtx, args map[string]any) string {
	if pid := strings.TrimSpace(strArg(args, "project_id", "")); pid != "" {
		return pid
	}
	if pid := strings.TrimSpace(strArg(args, "_project_id", "")); pid != "" {
		return pid
	}
	return projectScope(ctx)
}

// --- composition CRUD ---------------------------------------------

func (a *App) toolCompositionCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if editJSON, outputJSON, dur, version, ok, err := compositionPayloadFromV2Args(args); ok {
		if err != nil {
			return nil, err
		}
		pid := projectScopeFromArgs(ctx, args)
		name := strArg(args, "name", "")
		if name == "" {
			var spec V2Composition
			if json.Unmarshal([]byte(editJSON), &spec) == nil {
				name = spec.Name
			}
		}
		res, err := ctx.AppDB().Exec(
			`INSERT INTO compositions (project_id, name, edit_json, output_json, duration_seconds)
			 VALUES (?, ?, ?, ?, ?)`,
			pid, name, editJSON, outputJSON, dur,
		)
		if err != nil {
			return nil, fmt.Errorf("insert: %w", err)
		}
		id, _ := res.LastInsertId()
		ctx.EmitWithProject("composition.created", pid, map[string]any{
			"composition_id": id, "name": name, "duration_seconds": dur,
		})
		return map[string]any{"id": id, "version": version, "duration_seconds": dur}, nil
	}
	edit, err := editFromArgs(args)
	if err != nil {
		return nil, err
	}
	output := outputFromArgs(args)
	if err := validateEditOutput(edit, output); err != nil {
		return nil, err
	}
	applyTimelineTiming(edit)
	resolveRelativeClipStarts(edit)
	editJSON, _ := json.Marshal(edit)
	outputJSON, _ := json.Marshal(output)
	pid := projectScopeFromArgs(ctx, args)
	dur := editDurationSeconds(edit)
	name := strArg(args, "name", "")

	res, err := ctx.AppDB().Exec(
		`INSERT INTO compositions (project_id, name, edit_json, output_json, duration_seconds)
		 VALUES (?, ?, ?, ?, ?)`,
		pid, name, string(editJSON), string(outputJSON), dur,
	)
	if err != nil {
		return nil, fmt.Errorf("insert: %w", err)
	}
	id, _ := res.LastInsertId()
	ctx.EmitWithProject("composition.created", pid, map[string]any{
		"composition_id": id, "name": name, "duration_seconds": dur,
	})
	return map[string]any{"id": id, "version": "composer/v1", "duration_seconds": dur, "warnings": v1TypographyWarnings(edit)}, nil
}

func (a *App) toolCompositionUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	if id == 0 {
		return nil, errors.New("id required")
	}
	patch, _ := args["patch"].(map[string]any)
	if patch == nil {
		return nil, errors.New("patch (object) required")
	}

	// Load current row.
	var (
		name, editJSON, outputJSON, projectID string
	)
	if err := ctx.AppDB().QueryRow(
		`SELECT project_id, name, edit_json, output_json FROM compositions WHERE id=?`, id,
	).Scan(&projectID, &name, &editJSON, &outputJSON); err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	currentIsV2 := isV2EditJSON(editJSON)
	if currentIsV2 && !composerV2Enabled() {
		return nil, errors.New("composer/v2 is disabled in this public release")
	}
	if v := strArg(patch, "name", ""); v != "" {
		name = v
	}
	if nextEditJSON, nextOutputJSON, dur, version, ok, err := compositionPayloadFromV2Args(patch); ok {
		if err != nil {
			return nil, err
		}
		_, err := ctx.AppDB().Exec(
			`UPDATE compositions SET name=?, edit_json=?, output_json=?, duration_seconds=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			name, nextEditJSON, nextOutputJSON, dur, id,
		)
		if err != nil {
			return nil, fmt.Errorf("update: %w", err)
		}
		ctx.EmitWithProject("composition.updated", projectID, map[string]any{
			"composition_id": id, "name": name, "duration_seconds": dur,
		})
		return map[string]any{"id": id, "version": version, "duration_seconds": dur}, nil
	}
	if currentIsV2 {
		_, err := ctx.AppDB().Exec(
			`UPDATE compositions SET name=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			name, id,
		)
		if err != nil {
			return nil, fmt.Errorf("update: %w", err)
		}
		var spec V2Composition
		_ = json.Unmarshal([]byte(editJSON), &spec)
		dur := v2DurationSeconds(&spec)
		ctx.EmitWithProject("composition.updated", projectID, map[string]any{
			"composition_id": id, "name": name, "duration_seconds": dur,
		})
		return map[string]any{"id": id, "version": composerV2Version, "duration_seconds": dur}, nil
	}
	edit, _ := parseEditJSON(editJSON)
	var output Output
	_ = json.Unmarshal([]byte(outputJSON), &output)

	// Apply patch — only the fields the validator knows.
	if _, ok := patch["tracks"]; ok || patch["soundtrack"] != nil || patch["background"] != nil {
		// Compose a new edit from the supplied subset, falling back to
		// the current values for missing fields.
		next := map[string]any{}
		if v, ok := patch["tracks"]; ok {
			next["tracks"] = v
		} else {
			next["tracks"] = tracksAsAny(edit)
		}
		if v, ok := patch["soundtrack"]; ok {
			next["soundtrack"] = v
		} else if edit.Timeline.Soundtrack != nil {
			next["soundtrack"] = edit.Timeline.Soundtrack
		}
		if v, ok := patch["background"]; ok {
			next["background"] = v
		} else if edit.Timeline.Background != "" {
			next["background"] = edit.Timeline.Background
		}
		newEdit, err := editFromArgs(next)
		if err != nil {
			return nil, err
		}
		edit = newEdit
	}
	if raw, ok := patch["output"].(map[string]any); ok {
		if v := strArg(raw, "format", ""); v != "" {
			output.Format = v
		}
		if v := strArg(raw, "resolution", ""); v != "" {
			output.Resolution = v
		}
		if v := strArg(raw, "aspect", ""); v != "" {
			output.Aspect = v
		}
		if v := intArg(raw, "fps", 0); v > 0 {
			output.FPS = v
		}
		validateOutput(&output)
	}
	if err := validateEditOutput(edit, output); err != nil {
		return nil, err
	}
	applyTimelineTiming(edit)
	resolveRelativeClipStarts(edit)

	newEditJSON, _ := json.Marshal(edit)
	newOutputJSON, _ := json.Marshal(output)
	dur := editDurationSeconds(edit)

	_, err := ctx.AppDB().Exec(
		`UPDATE compositions SET name=?, edit_json=?, output_json=?, duration_seconds=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		name, string(newEditJSON), string(newOutputJSON), dur, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	ctx.EmitWithProject("composition.updated", projectID, map[string]any{
		"composition_id": id, "name": name, "duration_seconds": dur,
	})
	return map[string]any{"id": id, "duration_seconds": dur, "warnings": v1TypographyWarnings(edit)}, nil
}

func compositionPayloadFromV2Args(args map[string]any) (editJSON string, outputJSON string, duration float64, version string, ok bool, err error) {
	spec, isV2, err := v2SpecFromArgs(args)
	if !isV2 {
		return "", "", 0, "", false, nil
	}
	if !composerV2Enabled() {
		return "", "", 0, composerV2Version, true, errors.New("composer/v2 is disabled in this public release")
	}
	if err != nil {
		return "", "", 0, composerV2Version, true, err
	}
	b, _ := json.MarshalIndent(spec, "", "  ")
	output := v2OutputToOutput(spec.Output)
	outBytes, _ := json.Marshal(output)
	return string(b), string(outBytes), v2DurationSeconds(spec), composerV2Version, true, nil
}

func tracksAsAny(e *Edit) any {
	b, _ := json.Marshal(e.Timeline.Tracks)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func (a *App) toolCompositionGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	if id == 0 {
		return nil, errors.New("id required")
	}
	row, err := loadComposition(ctx, id)
	if err != nil {
		return nil, err
	}
	if editJSON, _ := row["edit_json"].(string); editJSON != "" {
		projectID, _ := row["project_id"].(string)
		row["edit_json"] = enrichEditJSONFromMediaStudio(ctx, editJSON, projectID)
	}
	row["latest_render"] = loadLatestRender(ctx, id)
	return row, nil
}

func (a *App) toolCompositionList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScopeFromArgs(ctx, args)
	limit := intArg(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	if boolArg(args, "summary", false) {
		return a.toolCompositionListSummary(ctx, pid, limit)
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, name, edit_json, output_json, duration_seconds, created_at, updated_at
		 FROM compositions WHERE project_id=? ORDER BY id DESC LIMIT ?`, pid, limit,
	)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		var (
			id                         int64
			name, editJSON, outputJSON string
			dur                        float64
			createdAt, updatedAt       string
		)
		if err := rows.Scan(&id, &name, &editJSON, &outputJSON, &dur, &createdAt, &updatedAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id":               id,
			"name":             name,
			"edit_json":        editJSON,
			"output_json":      outputJSON,
			"duration_seconds": dur,
			"created_at":       createdAt,
			"updated_at":       updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var history map[int64]*mediaHistoryRow
	for _, item := range out {
		editJSON, _ := item["edit_json"].(string)
		if editJSONNeedsAIEnrichment(editJSON) {
			history, _ = mediaHistoryByStorageID(ctx, pid)
			break
		}
	}
	for _, item := range out {
		id, _ := item["id"].(int64)
		if editJSON, _ := item["edit_json"].(string); editJSON != "" {
			item["edit_json"] = enrichEditJSONWithMediaHistory(editJSON, history)
		}
		item["latest_render"] = loadLatestRender(ctx, id)
	}
	return map[string]any{"compositions": out}, nil
}

func (a *App) toolCompositionListSummary(ctx *sdk.AppCtx, pid string, limit int) (any, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT
		     c.id,
		     c.name,
		     c.duration_seconds,
		     c.created_at,
		     c.updated_at,
		     COALESCE((
		       SELECT r.id FROM renders r
		       WHERE r.composition_id = c.id
		       ORDER BY r.id DESC LIMIT 1
		     ), 0) AS latest_render_id,
		     COALESCE((
		       SELECT r.status FROM renders r
		       WHERE r.composition_id = c.id
		       ORDER BY r.id DESC LIMIT 1
		     ), '') AS latest_render_status
		   FROM compositions c
		   WHERE c.project_id=?
		   ORDER BY c.id DESC LIMIT ?`, pid, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id, latestRenderID         int64
			name, createdAt, updatedAt string
			latestRenderStatus         string
			dur                        float64
		)
		if err := rows.Scan(&id, &name, &dur, &createdAt, &updatedAt, &latestRenderID, &latestRenderStatus); err != nil {
			continue
		}
		item := map[string]any{
			"id":               id,
			"name":             name,
			"duration_seconds": dur,
			"created_at":       createdAt,
			"updated_at":       updatedAt,
		}
		if latestRenderID > 0 {
			item["latest_render"] = map[string]any{
				"id":     latestRenderID,
				"status": latestRenderStatus,
			}
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"compositions": out, "summary": true}, nil
}

func (a *App) toolCompositionDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	if id == 0 {
		return nil, errors.New("id required")
	}
	var projectID string
	if err := ctx.AppDB().QueryRow(`SELECT project_id FROM compositions WHERE id=?`, id).Scan(&projectID); err != nil {
		return nil, fmt.Errorf("not found: %w", err)
	}
	_, err := ctx.AppDB().Exec(`DELETE FROM compositions WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	ctx.EmitWithProject("composition.deleted", projectID, map[string]any{"composition_id": id})
	return map[string]any{"id": id, "deleted": true}, nil
}

func (a *App) toolCompositionValidate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	var editJSON string
	if raw := strArg(args, "edit_json", ""); strings.TrimSpace(raw) != "" {
		editJSON = raw
	} else if raw, ok := args["spec"]; ok {
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		editJSON = string(b)
	} else if isV2Map(args) {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		editJSON = string(b)
	} else {
		return nil, errors.New("spec or edit_json required")
	}
	if isV2EditJSON(editJSON) && !composerV2Enabled() {
		return CompositionValidation{
			Valid:    false,
			Version:  composerV2Version,
			Renderer: "disabled",
			Errors:   []string{"composer/v2 is disabled in this public release"},
		}, nil
	}
	return validateCompositionJSON(editJSON), nil
}

func (a *App) toolCompositionExamples(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if !composerV2Enabled() {
		return map[string]any{
			"examples": []map[string]any{},
			"note":     "Experimental composer/v2 examples are hidden in this public release.",
		}, nil
	}
	return map[string]any{"examples": composerV2Examples()}, nil
}

func loadComposition(ctx *sdk.AppCtx, id int64) (map[string]any, error) {
	var (
		name, editJSON, outputJSON string
		dur                        float64
		createdAt, updatedAt       string
		projectID                  string
	)
	err := ctx.AppDB().QueryRow(
		`SELECT project_id, name, edit_json, output_json, duration_seconds, created_at, updated_at
		 FROM compositions WHERE id=?`, id,
	).Scan(&projectID, &name, &editJSON, &outputJSON, &dur, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("not found (id=%d): %w", id, err)
	}
	return map[string]any{
		"id":               id,
		"project_id":       projectID,
		"name":             name,
		"edit_json":        editJSON,
		"output_json":      outputJSON,
		"duration_seconds": dur,
		"created_at":       createdAt,
		"updated_at":       updatedAt,
	}, nil
}

func loadLatestRender(ctx *sdk.AppCtx, compID int64) map[string]any {
	var (
		id, storageID, durMS, attempts                        int64
		executor, status, phase, errMsg, qaJSON, progressJSON string
		costUSD                                               float64
		progressPct                                           float64
		createdAt, updatedAt                                  string
	)
	err := ctx.AppDB().QueryRow(
		`SELECT id, executor, status, COALESCE(phase,''), COALESCE(progress_pct,0), COALESCE(progress_json,'{}'),
		        storage_id, duration_ms, cost_usd, error, attempts, created_at, updated_at, qa_json
		 FROM renders WHERE composition_id=? ORDER BY id DESC LIMIT 1`, compID,
	).Scan(&id, &executor, &status, &phase, &progressPct, &progressJSON, &storageID, &durMS, &costUSD, &errMsg, &attempts, &createdAt, &updatedAt, &qaJSON)
	if err != nil {
		return nil
	}
	row := map[string]any{
		"id":           id,
		"executor":     executor,
		"status":       status,
		"phase":        phase,
		"progress_pct": progressPct,
		"progress":     decodeJSONMap(progressJSON),
		"storage_id":   storageID,
		"duration_ms":  durMS,
		"cost_usd":     costUSD,
		"error":        errMsg,
		"attempts":     attempts,
		"created_at":   createdAt,
		"updated_at":   updatedAt,
		"qa":           decodeRenderQA(qaJSON),
	}
	if storageID > 0 {
		row["storage_url"] = "/api/apps/storage/files/" + strconv.FormatInt(storageID, 10) + "/content"
	}
	if storageID == 0 {
		if u := localCacheURL(id); u != "" {
			row["local_cache_url"] = u
		}
	}
	return row
}

// --- render orchestration ----------------------------------------

func (a *App) toolCompositionRender(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	if id == 0 {
		return nil, errors.New("id required")
	}
	executorOverride := strArg(args, "executor", "")
	wait := boolArg(args, "wait", true)
	renderID := int64Arg(args, "_render_id", 0)
	resumePhase := ""

	row, err := loadComposition(ctx, id)
	if err != nil {
		return nil, err
	}
	rawEditJSON := row["edit_json"].(string)
	rawOutputJSON := row["output_json"].(string)
	pid := row["project_id"].(string)
	if renderID > 0 {
		var snapshotEdit, snapshotOutput, requestedExecutor string
		if err := ctx.AppDB().QueryRow(
			`SELECT edit_snapshot, output_snapshot, executor, COALESCE(phase,'') FROM renders WHERE id=? AND composition_id=?`,
			renderID, id,
		).Scan(&snapshotEdit, &snapshotOutput, &requestedExecutor, &resumePhase); err != nil {
			return nil, fmt.Errorf("load render row: %w", err)
		}
		if strings.TrimSpace(snapshotEdit) != "" {
			rawEditJSON = snapshotEdit
		}
		if strings.TrimSpace(snapshotOutput) != "" && strings.TrimSpace(snapshotOutput) != "{}" {
			rawOutputJSON = snapshotOutput
		}
		if executorOverride == "" && requestedExecutor != "auto" {
			executorOverride = requestedExecutor
		}
	} else {
		requestedExecutor := executorOverride
		if requestedExecutor == "" {
			requestedExecutor = "auto"
		}
		initialStatus, initialPhase := "rendering", "preparing"
		if !wait {
			initialStatus, initialPhase = "queued", "queued"
		}
		renderID, err = createRenderRow(ctx, id, pid, requestedExecutor, rawEditJSON, rawOutputJSON, initialStatus, initialPhase)
		if err != nil {
			return nil, err
		}
		if !wait {
			return map[string]any{
				"render_id":      renderID,
				"composition_id": id,
				"status":         "queued",
				"phase":          "queued",
			}, nil
		}
	}
	if resumePhase != "generating_assets" {
		setRenderProgress(ctx, renderID, id, pid, "rendering", "preparing", 2, map[string]any{
			"message": "Preparing composition",
		})
	}
	if isV2EditJSON(rawEditJSON) {
		if !composerV2Enabled() {
			err := errors.New("composer/v2 rendering is disabled in this public release")
			failRender(ctx, renderID, id, pid, err, "")
			return nil, err
		}
		if spec, specErr := parseV2CompositionJSON(rawEditJSON); specErr == nil {
			output := v2OutputToOutput(spec.Output)
			if output.Format == "mp4" && v2UseDirectRenderer(spec) {
				executorName := "native-v2"
				renderFn := renderV2Native
				if strings.EqualFold(strings.TrimSpace(spec.Output.Renderer), "browser") {
					executorName = "browser-v2"
					renderFn = renderV2Browser
				}
				_, _ = ctx.AppDB().Exec(`UPDATE renders SET executor=?, phase='rendering', progress_pct=50, updated_at=CURRENT_TIMESTAMP WHERE id=?`, executorName, renderID)
				setRenderProgress(ctx, renderID, id, pid, "rendering", "rendering", 50, map[string]any{"message": "Rendering composition", "executor": executorName})
				rctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				defer cancel()
				result, nativeWarnings, err := renderFn(rctx, ctx.WithProject(pid), spec, pid)
				if err != nil {
					failRender(ctx, renderID, id, pid, err, result.FFmpegCommand)
					return nil, err
				}
				if result.Cleanup != nil {
					defer result.Cleanup()
				}
				qa := analyzeRender(result.LocalPath, nil)
				qa.Warnings = append(qa.Warnings, nativeWarnings...)
				setRenderProgress(ctx, renderID, id, pid, "rendering", "uploading", 90, map[string]any{"message": "Uploading render output"})
				storageID := saveRenderOutput(ctx, result.LocalPath, output.Format, pid, id)
				if storageID == 0 {
					if cacheErr := writeLocalCacheFromPath(renderID, result.LocalPath, output.Format); cacheErr != nil {
						ctx.Logger().Warn("local cache write failed", "render_id", renderID, "err", cacheErr)
					}
				}
				ctx.AppDB().Exec(
					`UPDATE renders
				 SET status='complete', phase='complete', progress_pct=100, storage_id=?, duration_ms=?, cost_usd=?,
				     ffmpeg_command=?, qa_json=?, finished_at=CURRENT_TIMESTAMP, next_attempt_at=NULL, updated_at=CURRENT_TIMESTAMP
				 WHERE id=?`,
					storageID, result.DurationMS, result.CostUSD, result.FFmpegCommand, encodeRenderQA(qa), renderID,
				)
				ctx.EmitWithProject("composition.rendered", pid, map[string]any{
					"composition_id": id,
					"render_id":      renderID,
					"executor":       executorName,
					"storage_id":     storageID,
					"duration_ms":    result.DurationMS,
					"qa":             qa,
				})
				return map[string]any{
					"render_id":   renderID,
					"status":      "complete",
					"storage_id":  storageID,
					"executor":    executorName,
					"version":     composerV2Version,
					"warnings":    nativeWarnings,
					"duration_ms": result.DurationMS,
					"cost_usd":    result.CostUSD,
					"qa":          qa,
				}, nil
			}
		}
	}
	edit, output, renderVersion, renderWarnings, err := renderEditFromStoredJSON(rawEditJSON, rawOutputJSON)
	if err != nil {
		err = fmt.Errorf("composition.edit_json invalid: %w", err)
		failRender(ctx, renderID, id, pid, err, "")
		return nil, err
	}
	if err := validateEditOutput(edit, output); err != nil {
		failRender(ctx, renderID, id, pid, err, "")
		return nil, err
	}
	persistMaterializedEdit := renderVersion != composerV2Version
	mat, err := materializeAIAssets(ctx.WithProject(pid), edit, id, pid, persistMaterializedEdit)
	if err != nil {
		failRender(ctx, renderID, id, pid, err, "")
		return nil, err
	}
	if mat.Changed {
		materialized, _ := json.Marshal(edit)
		_, _ = ctx.AppDB().Exec(
			`UPDATE renders SET edit_snapshot=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			string(materialized), renderID,
		)
		rawEditJSON = string(materialized)
	}
	if len(mat.Pending) > 0 {
		deferRenderForAI(ctx, renderID, id, pid, mat.Pending)
		return map[string]any{
			"render_id":      renderID,
			"composition_id": id,
			"status":         "waiting_ai",
			"phase":          "generating_assets",
			"pending":        mat.Pending,
			"message":        "AI assets are generating automatically: " + strings.Join(mat.Pending, "; "),
		}, nil
	}
	if resumePhase == "generating_assets" {
		setRenderProgress(ctx, renderID, id, pid, "rendering", "preparing", 25, map[string]any{
			"message": "AI assets ready; preparing render",
		})
	}

	exec, err := chooseExecutor(ctx, executorOverride)
	if err != nil {
		failRender(ctx, renderID, id, pid, err, "")
		return nil, err
	}

	editSnapshot := []byte(rawEditJSON)
	if renderVersion != composerV2Version {
		editSnapshot, _ = json.Marshal(edit)
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE renders SET executor=?, edit_snapshot=?, phase='rendering', progress_pct=50, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		exec.Name(), string(editSnapshot), renderID,
	)
	setRenderProgress(ctx, renderID, id, pid, "rendering", "rendering", 50, map[string]any{
		"message":  "Rendering composition",
		"executor": exec.Name(),
	})

	rctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	result, err := exec.Render(rctx, ctx, edit, output, pid)
	if err != nil {
		failRender(ctx, renderID, id, pid, err, result.FFmpegCommand)
		return nil, err
	}
	if result.Cleanup != nil {
		defer result.Cleanup()
	}

	// Sync executors deliver bytes via LocalPath. Persist to storage
	// (when bound) or local cache (when not).
	var storageID int64
	qa := RenderQA{Warnings: timelineWarnings(edit)}
	if len(renderWarnings) > 0 {
		qa.Warnings = append(qa.Warnings, renderWarnings...)
	}
	setRenderProgress(ctx, renderID, id, pid, "rendering", "quality_checks", 82, map[string]any{"message": "Checking render output"})
	if result.Sync && strings.HasPrefix(result.LocalPath, "storage://files/") {
		if id, err := strconv.ParseInt(strings.TrimPrefix(result.LocalPath, "storage://files/"), 10, 64); err == nil && id > 0 {
			storageID = id
		}
	} else if result.Sync && result.LocalPath != "" {
		qa = analyzeRender(result.LocalPath, edit)
		setRenderProgress(ctx, renderID, id, pid, "rendering", "uploading", 90, map[string]any{"message": "Uploading render output"})
		storageID = saveRenderOutput(ctx, result.LocalPath, output.Format, pid, id)
		if storageID == 0 {
			if cacheErr := writeLocalCacheFromPath(renderID, result.LocalPath, output.Format); cacheErr != nil {
				ctx.Logger().Warn("local cache write failed", "render_id", renderID, "err", cacheErr)
			}
		}
	}

	ctx.AppDB().Exec(
		`UPDATE renders
		 SET status='complete', phase='complete', progress_pct=100, storage_id=?, duration_ms=?, cost_usd=?,
		     ffmpeg_command=?, qa_json=?, finished_at=CURRENT_TIMESTAMP, next_attempt_at=NULL, updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		storageID, result.DurationMS, result.CostUSD, result.FFmpegCommand, encodeRenderQA(qa), renderID,
	)

	ctx.EmitWithProject("composition.rendered", pid, map[string]any{
		"composition_id": id,
		"render_id":      renderID,
		"executor":       exec.Name(),
		"storage_id":     storageID,
		"duration_ms":    result.DurationMS,
		"qa":             qa,
	})

	return map[string]any{
		"render_id":   renderID,
		"status":      "complete",
		"storage_id":  storageID,
		"executor":    exec.Name(),
		"version":     renderVersion,
		"warnings":    renderWarnings,
		"duration_ms": result.DurationMS,
		"cost_usd":    result.CostUSD,
		"qa":          qa,
	}, nil
}

func renderEditFromStoredJSON(editJSON, outputJSON string) (*Edit, Output, string, []string, error) {
	if isV2EditJSON(editJSON) {
		spec, err := parseV2CompositionJSON(editJSON)
		if err != nil {
			return nil, Output{}, composerV2Version, nil, err
		}
		edit, output, warnings, err := v2ToV1FFmpeg(spec)
		return edit, output, composerV2Version, warnings, err
	}
	edit, err := parseEditJSON(editJSON)
	if err != nil {
		return nil, Output{}, "composer/v1", nil, err
	}
	var output Output
	_ = json.Unmarshal([]byte(outputJSON), &output)
	validateOutput(&output)
	return edit, output, "composer/v1", v1TypographyWarnings(edit), nil
}

// saveRenderOutput uploads the bytes to storage and returns the
// resulting storage id (or 0 when storage is unbound / fails).
// Reads the file into memory via base64 — fine for v0.1 video sizes;
// streaming upload is a follow-up if outputs grow past ~50 MB.
func saveRenderOutput(ctx *sdk.AppCtx, path, format, projectID string, compID int64) int64 {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return 0
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		ctx.Logger().Warn("read render output failed", "path", path, "err", err)
		return 0
	}
	name := fmt.Sprintf("composition-%d-%d.%s", compID, time.Now().Unix(), format)
	var got struct {
		ID int64 `json:"id"`
	}
	err = ctx.PlatformAPI().CallAppResult("storage", "files_upload", map[string]any{
		"name":           name,
		"content_base64": base64Encode(bytes),
		"folder":         "/.composer/",
		"content_type":   renderContentType(format),
		"tags":           []string{"composer", "render"},
		"_project_id":    projectID,
	}, &got)
	if err != nil {
		ctx.Logger().Warn("storage upload failed", "err", err)
		return 0
	}
	return got.ID
}

func renderContentType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "m4a":
		return "audio/mp4"
	case "aac":
		return "audio/aac"
	case "mp4":
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

// base64Encode is a tiny wrapper so we don't sprinkle stdlib imports.
func base64Encode(b []byte) string {
	return stdBase64.StdEncoding.EncodeToString(b)
}

// --- render_status -----------------------------------------------

func (a *App) toolRenderStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "render_id", 0)
	if id == 0 {
		return nil, errors.New("render_id required")
	}
	var (
		compID, storageID, durMS, attempts                    int64
		executor, status, phase, errMsg, qaJSON, progressJSON string
		costUSD                                               float64
		progressPct                                           float64
		createdAt, updatedAt                                  string
	)
	err := ctx.AppDB().QueryRow(
		`SELECT composition_id, executor, status, COALESCE(phase,''), COALESCE(progress_pct,0), COALESCE(progress_json,'{}'),
		        storage_id, duration_ms, cost_usd, error, attempts, created_at, updated_at, qa_json
		 FROM renders WHERE id=?`, id,
	).Scan(&compID, &executor, &status, &phase, &progressPct, &progressJSON, &storageID, &durMS, &costUSD, &errMsg, &attempts, &createdAt, &updatedAt, &qaJSON)
	if err != nil {
		return nil, fmt.Errorf("not found: %w", err)
	}
	return map[string]any{
		"render_id":      id,
		"composition_id": compID,
		"executor":       executor,
		"status":         status,
		"phase":          phase,
		"progress_pct":   progressPct,
		"progress":       decodeJSONMap(progressJSON),
		"storage_id":     storageID,
		"duration_ms":    durMS,
		"cost_usd":       costUSD,
		"error":          errMsg,
		"attempts":       attempts,
		"created_at":     createdAt,
		"updated_at":     updatedAt,
		"qa":             decodeRenderQA(qaJSON),
	}, nil
}

func (a *App) toolRenderCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "render_id", 0)
	if id == 0 {
		return nil, errors.New("render_id required")
	}
	return cancelQueuedRender(ctx, id, projectScope(ctx))
}

func decodeJSONMap(raw string) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// --- asset_inspect -----------------------------------------------

func (a *App) toolAssetInspect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	src := strArg(args, "src", "")
	if src == "" {
		return nil, errors.New("src required")
	}
	url, err := resolveAssetURL(ctx, src)
	if err != nil {
		return nil, fmt.Errorf("resolve: %w", err)
	}
	rctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(rctx, ffprobePath(),
		"-v", "error",
		"-print_format", "json",
		"-show_format", "-show_streams",
		url,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	var probe map[string]any
	_ = json.Unmarshal(out, &probe)
	return probe, nil
}

func (a *App) handleAssetResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	src := strings.TrimSpace(r.URL.Query().Get("src"))
	if src == "" {
		http.Error(w, "src required", http.StatusBadRequest)
		return
	}
	url, err := resolveAssetURL(globalCtx, src)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResp(w, map[string]any{
		"src":  src,
		"url":  url,
		"kind": assetKindHint(src),
	})
}

func (a *App) handleStorageAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	folder := strings.TrimSpace(r.URL.Query().Get("folder"))
	if folder == "" {
		folder = "/"
	}
	recursive := r.URL.Query().Get("recursive") != "false"
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	args := map[string]any{
		"folder":      folder,
		"recursive":   recursive,
		"limit":       limit,
		"_project_id": projectScopeFromArgs(globalCtx, map[string]any{"project_id": r.URL.Query().Get("project_id")}),
	}
	var got map[string]any
	if err := globalCtx.PlatformAPI().CallAppResult("storage", "files_list", args, &got); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	jsonResp(w, got)
}

func assetKindHint(src string) string {
	s := strings.ToLower(src)
	switch {
	case strings.Contains(s, ".png"), strings.Contains(s, ".jpg"), strings.Contains(s, ".jpeg"),
		strings.Contains(s, ".webp"), strings.Contains(s, ".gif"):
		return "image"
	case strings.Contains(s, ".mp3"), strings.Contains(s, ".wav"), strings.Contains(s, ".m4a"),
		strings.Contains(s, ".aac"), strings.Contains(s, ".flac"):
		return "audio"
	default:
		return "video"
	}
}

// --- HTTP handlers (panel) ---------------------------------------

func (a *App) handleListCompositions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	out, err := a.toolCompositionList(globalCtx, map[string]any{
		"limit":      limit,
		"project_id": r.URL.Query().Get("project_id"),
		"summary":    r.URL.Query().Get("summary") == "1" || r.URL.Query().Get("summary") == "true",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, out)
}

func (a *App) handleCompositionByID(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/composition/")
	idStr = strings.SplitN(idStr, "/", 2)[0]
	if r.Method == http.MethodPost && (idStr == "" || idStr == "new") {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body["project_id"] == nil {
			body["project_id"] = r.URL.Query().Get("project_id")
		}
		out, err := a.toolCompositionCreate(globalCtx, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResp(w, out)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolCompositionGet(globalCtx, map[string]any{"id": id})
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonResp(w, out)
	case http.MethodPut, http.MethodPatch:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := a.toolCompositionUpdate(globalCtx, map[string]any{"id": id, "patch": body})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResp(w, out)
	case http.MethodDelete:
		out, err := a.toolCompositionDelete(globalCtx, map[string]any{"id": id})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResp(w, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleRender(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out, err := a.toolCompositionRender(globalCtx, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, out)
}

// handleAIGenerate materializes one draft AI asset through Composer so the
// panel and render path share cache and ElevenLabs continuity semantics.
func (a *App) handleAIGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		AI        *AIAsset `json:"ai"`
		Track     *Track   `json:"track"`
		ClipUID   string   `json:"clip_uid"`
		ProjectID string   `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pid := strings.TrimSpace(body.ProjectID)
	if pid == "" {
		pid = strings.TrimSpace(r.URL.Query().Get("project_id"))
	}
	if pid == "" {
		pid = projectScope(globalCtx)
	}
	ctx := globalCtx.WithProject(pid)

	var ai *AIAsset
	var continuity ttsContinuityPlan
	if body.Track != nil {
		for i := range body.Track.Clips {
			normalizeGeneratedAsset(&body.Track.Clips[i])
			normalizeClipDurationMetadata(&body.Track.Clips[i])
		}
		for i := range body.Track.Clips {
			if body.Track.Clips[i].UID == body.ClipUID {
				ai = body.Track.Clips[i].AI
				continuity = planTTSContinuity(body.Track, i)
				break
			}
		}
		if ai == nil {
			http.Error(w, "clip_uid not found in track", http.StatusBadRequest)
			return
		}
	} else {
		ai = body.AI
		applyDefaultAIOptions(ai)
	}
	if ai == nil {
		http.Error(w, "ai or track+clip_uid required", http.StatusBadRequest)
		return
	}
	_, pending, err := materializeOneAIAsset(ctx, ai, "AI asset", pid, continuity.ProviderOptions, continuity.CacheOptions)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	jsonResp(w, map[string]any{
		"ai":      ai,
		"pending": pending,
	})
}

func (a *App) handleRenderStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/render-status/")
	idStr = strings.SplitN(idStr, "/", 2)[0]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	out, err := a.toolRenderStatus(globalCtx, map[string]any{"render_id": id})
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResp(w, out)
}

func (a *App) handleBindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	pid := projectScopeFromArgs(globalCtx, map[string]any{"project_id": r.URL.Query().Get("project_id")})
	out := map[string]any{
		"storage_bound":     appToolAvailable(globalCtx, "storage", "files_list", map[string]any{"limit": 1, "_project_id": pid}),
		"instances_bound":   appToolAvailable(globalCtx, "instances", "instance_get", map[string]any{"id": 0, "_project_id": pid}),
		"mediastudio_bound": appToolAvailable(globalCtx, "media-studio", "media_history", map[string]any{"limit": 1, "_project_id": pid}),
		"render_host_id":    renderHostID(globalCtx),
		"ffmpeg_path":       ffmpegPath(),
	}
	if bound := globalCtx.IntegrationFor("render_executor"); bound != nil {
		out["render_executor"] = bound.AppSlug
	}
	jsonResp(w, out)
}

func (a *App) handleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out, err := a.toolCompositionValidate(globalCtx, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResp(w, out)
}

func (a *App) handleExamples(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !composerV2Enabled() {
		jsonResp(w, map[string]any{
			"examples": []map[string]any{},
			"note":     "Experimental composer/v2 examples are hidden in this public release.",
		})
		return
	}
	jsonResp(w, map[string]any{"examples": composerV2Examples()})
}

func appToolAvailable(ctx *sdk.AppCtx, appName, tool string, args map[string]any) bool {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return false
	}
	var got map[string]any
	return ctx.PlatformAPI().CallAppResult(appName, tool, args, &got) == nil
}

func jsonResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
