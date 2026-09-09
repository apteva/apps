package main

import (
	"context"
	stdBase64 "encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	if pid := projectScope(ctx); pid != "" {
		return pid
	}
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
		return map[string]any{"id": id, "revision": int64(1), "version": version, "duration_seconds": dur}, nil
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
	return map[string]any{"id": id, "revision": int64(1), "version": "composer/v1", "duration_seconds": dur, "warnings": v1TypographyWarnings(edit)}, nil
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
		revision                              int64
	)
	if err := ctx.AppDB().QueryRow(
		`SELECT project_id, name, edit_json, output_json, revision FROM compositions WHERE id=? AND project_id=?`, id, projectScope(ctx),
	).Scan(&projectID, &name, &editJSON, &outputJSON, &revision); err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	if expected := int64Arg(patch, "expected_revision", 0); expected > 0 && expected != revision {
		return nil, errors.New("composition changed since loading; reload before saving")
	}
	currentIsV2 := isV2EditJSON(editJSON)
	if currentIsV2 && !composerV2Enabled() {
		return nil, errors.New("composer/v2 is disabled by COMPOSER_V2_ENABLED")
	}
	if v := strArg(patch, "name", ""); v != "" {
		name = v
	}
	if nextEditJSON, nextOutputJSON, dur, version, ok, err := compositionPayloadFromV2Args(patch); ok {
		if err != nil {
			return nil, err
		}
		updated, err := ctx.AppDB().Exec(
			`UPDATE compositions SET name=?, edit_json=?, output_json=?, duration_seconds=?, updated_at=CURRENT_TIMESTAMP, revision=revision+1 WHERE id=? AND project_id=? AND revision=?`,
			name, nextEditJSON, nextOutputJSON, dur, id, projectID, revision,
		)
		if err != nil {
			return nil, fmt.Errorf("update: %w", err)
		}
		if n, _ := updated.RowsAffected(); n != 1 {
			return nil, errors.New("composition changed while saving; reload before saving")
		}
		ctx.EmitWithProject("composition.updated", projectID, map[string]any{
			"composition_id": id, "name": name, "duration_seconds": dur,
		})
		return map[string]any{"id": id, "revision": revision + 1, "version": version, "duration_seconds": dur}, nil
	}
	if currentIsV2 {
		updated, err := ctx.AppDB().Exec(
			`UPDATE compositions SET name=?, updated_at=CURRENT_TIMESTAMP, revision=revision+1 WHERE id=? AND project_id=? AND revision=?`,
			name, id, projectID, revision,
		)
		if err != nil {
			return nil, fmt.Errorf("update: %w", err)
		}
		var spec V2Composition
		_ = json.Unmarshal([]byte(editJSON), &spec)
		dur := v2DurationSeconds(&spec)
		if n, _ := updated.RowsAffected(); n != 1 {
			return nil, errors.New("composition changed while saving; reload before saving")
		}
		ctx.EmitWithProject("composition.updated", projectID, map[string]any{
			"composition_id": id, "name": name, "duration_seconds": dur,
		})
		return map[string]any{"id": id, "revision": revision + 1, "version": composerV2Version, "duration_seconds": dur}, nil
	}
	edit, _ := parseEditJSON(editJSON)
	var output Output
	_ = json.Unmarshal([]byte(outputJSON), &output)

	// Apply patch — only the fields the validator knows.
	if _, ok := patch["tracks"]; ok || patch["soundtrack"] != nil || patch["background"] != nil || patch["markers"] != nil {
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
		if v, ok := patch["markers"]; ok {
			next["markers"] = v
		} else if len(edit.Timeline.Markers) > 0 {
			next["markers"] = edit.Timeline.Markers
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

	updated, err := ctx.AppDB().Exec(
		`UPDATE compositions SET name=?, edit_json=?, output_json=?, duration_seconds=?, updated_at=CURRENT_TIMESTAMP, revision=revision+1 WHERE id=? AND project_id=? AND revision=?`,
		name, string(newEditJSON), string(newOutputJSON), dur, id, projectID, revision,
	)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return nil, errors.New("composition changed while saving; reload before saving")
	}
	ctx.EmitWithProject("composition.updated", projectID, map[string]any{
		"composition_id": id, "name": name, "duration_seconds": dur,
	})
	return map[string]any{"id": id, "revision": revision + 1, "duration_seconds": dur, "warnings": v1TypographyWarnings(edit)}, nil
}

func compositionPayloadFromV2Args(args map[string]any) (editJSON string, outputJSON string, duration float64, version string, ok bool, err error) {
	spec, isV2, err := v2SpecFromArgs(args)
	if !isV2 {
		return "", "", 0, "", false, nil
	}
	if !composerV2Enabled() {
		return "", "", 0, composerV2Version, true, errors.New("composer/v2 is disabled by COMPOSER_V2_ENABLED")
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
		       WHERE r.composition_id = c.id AND r.output_id IS NULL
		       ORDER BY r.id DESC LIMIT 1
		     ), 0) AS latest_render_id,
		     COALESCE((
		       SELECT r.status FROM renders r
		       WHERE r.composition_id = c.id AND r.output_id IS NULL
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
	if err := ctx.AppDB().QueryRow(`SELECT project_id FROM compositions WHERE id=? AND project_id=?`, id, projectScope(ctx)).Scan(&projectID); err != nil {
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
			Errors:   []string{"composer/v2 is disabled by COMPOSER_V2_ENABLED"},
		}, nil
	}
	return validateCompositionJSON(editJSON), nil
}

func (a *App) toolCompositionExamples(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if !composerV2Enabled() {
		return map[string]any{
			"examples": []map[string]any{},
			"note":     "Composer V2 examples are hidden because COMPOSER_V2_ENABLED is disabled.",
		}, nil
	}
	return map[string]any{"examples": composerV2Examples()}, nil
}

func loadComposition(ctx *sdk.AppCtx, id int64) (map[string]any, error) {
	var (
		revision                   int64
		name, editJSON, outputJSON string
		dur                        float64
		createdAt, updatedAt       string
		projectID                  string
	)
	err := ctx.AppDB().QueryRow(
		`SELECT project_id, name, edit_json, output_json, duration_seconds, created_at, updated_at, revision
		 FROM compositions WHERE id=? AND project_id=?`, id, projectScope(ctx),
	).Scan(&projectID, &name, &editJSON, &outputJSON, &dur, &createdAt, &updatedAt, &revision)
	if err != nil {
		return nil, fmt.Errorf("not found (id=%d): %w", id, err)
	}
	return map[string]any{
		"revision":         revision,
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
		 FROM renders WHERE composition_id=? AND output_id IS NULL ORDER BY id DESC LIMIT 1`, compID,
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
		row["storage_url"] = "/api/apps/storage/files/" + strconv.FormatInt(storageID, 10) + "/content?project_id=" + url.QueryEscape(projectScope(ctx))
	}
	if storageID == 0 {
		if u := localCacheURL(id, projectScope(ctx)); u != "" {
			row["local_cache_url"] = u
		}
	}
	return row
}

// --- render orchestration ----------------------------------------

func (a *App) toolCompositionRender(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.renderComposition(composerLifetimeContext(), ctx, args, 0)
}

func (a *App) renderComposition(parent context.Context, ctx *sdk.AppCtx, args map[string]any, renderID int64) (any, error) {
	id := int64Arg(args, "id", 0)
	if id == 0 {
		return nil, errors.New("id required")
	}
	executorOverride := strArg(args, "executor", "")
	wait := boolArg(args, "wait", true)

	resumePhase := ""

	row, err := loadComposition(ctx, id)
	if err != nil {
		return nil, err
	}
	if expected := int64Arg(args, "expected_revision", 0); renderID == 0 && expected > 0 && expected != row["revision"].(int64) {
		return nil, errors.New("composition changed before render; save again to render the latest draft")
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
	rctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	unregister := registerRenderCancel(renderID, cancel)
	defer func() { cancel(); unregister() }()
	var currentStatus string
	_ = ctx.AppDB().QueryRow(`SELECT status FROM renders WHERE id=?`, renderID).Scan(&currentStatus)
	if currentStatus == "cancelled" {
		cancel()
	}
	if err := rctx.Err(); err != nil {
		failRender(ctx, renderID, id, pid, err, "")
		return nil, err
	}
	if resumePhase != "generating_assets" {
		setRenderProgress(ctx, renderID, id, pid, "rendering", "preparing", 2, map[string]any{
			"message": "Preparing composition",
		})
	}
	if isV2EditJSON(rawEditJSON) {
		if !composerV2Enabled() {
			err := errors.New("composer/v2 rendering is disabled by COMPOSER_V2_ENABLED")
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

				rctx = withJobRenderProgress(rctx, ctx, renderID, id, pid, executorName)
				result, nativeWarnings, err := renderFn(rctx, ctx.WithProject(pid), spec, pid)
				if err != nil {
					failRender(ctx, renderID, id, pid, err, result.FFmpegCommand)
					return nil, err
				}
				if result.Cleanup != nil {
					defer func() {
						if result.Cleanup != nil {
							result.Cleanup()
						}
					}()
				}
				qa := analyzeRenderContext(rctx, result.LocalPath, nil)
				qa.Warnings = append(qa.Warnings, nativeWarnings...)
				setRenderProgress(ctx, renderID, id, pid, "rendering", "uploading", 90, map[string]any{"message": "Uploading render output"})
				storageID := saveRenderOutputContext(rctx, ctx, result.LocalPath, output.Format, pid, id)
				if storageID == 0 {
					if cacheErr := writeLocalCacheFromPath(renderID, result.LocalPath, output.Format); cacheErr != nil {
						result.Cleanup = nil // Retain the encoded file for recovery.
						err := fmt.Errorf("output persistence failed; encoded file retained at %s: %w", result.LocalPath, cacheErr)
						failRender(ctx, renderID, id, pid, err, result.FFmpegCommand)
						return nil, err
					}
				}
				if err := rctx.Err(); err != nil {
					failRender(ctx, renderID, id, pid, err, result.FFmpegCommand)
					return nil, err
				}
				completion, completeErr := ctx.AppDB().Exec(
					`UPDATE renders
				 SET status='complete', phase='complete', progress_pct=100, storage_id=?, duration_ms=?, cost_usd=?,
				     ffmpeg_command=?, qa_json=?, finished_at=CURRENT_TIMESTAMP, next_attempt_at=NULL, updated_at=CURRENT_TIMESTAMP
				 WHERE id=? AND status='rendering'`,
					storageID, result.DurationMS, result.CostUSD, result.FFmpegCommand, encodeRenderQA(qa), renderID,
				)
				if completeErr != nil {
					return nil, completeErr
				}
				if n, _ := completion.RowsAffected(); n != 1 {
					return nil, context.Canceled
				}
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

	rctx = withJobRenderProgress(rctx, ctx, renderID, id, pid, exec.Name())
	result, err := exec.Render(rctx, ctx, edit, output, pid)
	if err != nil {
		failRender(ctx, renderID, id, pid, err, result.FFmpegCommand)
		return nil, err
	}
	if result.Cleanup != nil {
		defer func() {
			if result.Cleanup != nil {
				result.Cleanup()
			}
		}()
	}

	// Sync executors deliver bytes via LocalPath. Persist to storage
	// (when bound) or local cache (when not).
	var storageID int64
	qa := RenderQA{Warnings: timelineWarnings(edit)}
	if len(renderWarnings) > 0 {
		qa.Warnings = appendUniqueStrings(qa.Warnings, renderWarnings...)
	}
	setRenderProgress(ctx, renderID, id, pid, "rendering", "quality_checks", 82, map[string]any{"message": "Checking render output"})
	if result.Sync && strings.HasPrefix(result.LocalPath, "storage://files/") {
		if id, err := strconv.ParseInt(strings.TrimPrefix(result.LocalPath, "storage://files/"), 10, 64); err == nil && id > 0 {
			storageID = id
		}
	} else if result.Sync && result.LocalPath != "" {
		qa = analyzeRenderContext(rctx, result.LocalPath, edit)
		setRenderProgress(ctx, renderID, id, pid, "rendering", "uploading", 90, map[string]any{"message": "Uploading render output"})
		storageID = saveRenderOutputContext(rctx, ctx, result.LocalPath, output.Format, pid, id)
		if storageID == 0 {
			if cacheErr := writeLocalCacheFromPath(renderID, result.LocalPath, output.Format); cacheErr != nil {
				result.Cleanup = nil // Retain the encoded file for recovery.
				err := fmt.Errorf("output persistence failed; encoded file retained at %s: %w", result.LocalPath, cacheErr)
				failRender(ctx, renderID, id, pid, err, result.FFmpegCommand)
				return nil, err
			}
		}
	}

	if err := rctx.Err(); err != nil {
		failRender(ctx, renderID, id, pid, err, result.FFmpegCommand)
		return nil, err
	}
	completion, completeErr := ctx.AppDB().Exec(
		`UPDATE renders
		 SET status='complete', phase='complete', progress_pct=100, storage_id=?, duration_ms=?, cost_usd=?,
		     ffmpeg_command=?, qa_json=?, finished_at=CURRENT_TIMESTAMP, next_attempt_at=NULL, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='rendering'`,
		storageID, result.DurationMS, result.CostUSD, result.FFmpegCommand, encodeRenderQA(qa), renderID,
	)

	if completeErr != nil {
		return nil, completeErr
	}
	if n, _ := completion.RowsAffected(); n != 1 {
		return nil, context.Canceled
	}
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

func withJobRenderProgress(rctx context.Context, appCtx *sdk.AppCtx, renderID, compositionID int64, projectID, executorName string) context.Context {
	lastProgressPct := 49.0
	return withRenderProgress(rctx, func(progress RenderProgress) {
		pct := 50 + clampFloat(progress.Fraction, 0, 1)*30
		if pct < lastProgressPct+0.5 && progress.Fraction < 1 {
			return
		}
		lastProgressPct = pct
		detail := map[string]any{
			"message":          "Rendering composition",
			"executor":         executorName,
			"out_time_seconds": progress.OutTimeSeconds,
			"frame":            progress.Frame,
			"speed":            progress.Speed,
		}
		setRenderProgress(appCtx, renderID, compositionID, projectID, "rendering", "rendering", pct, detail)
	})
}

func appendUniqueStrings(dst []string, values ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(values))
	for _, value := range dst {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		dst = append(dst, value)
	}
	return dst
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

// saveRenderOutput keeps upload memory bounded to a single 1 MiB part.
func saveRenderOutput(ctx *sdk.AppCtx, path, format, projectID string, compID int64) int64 {
	return saveRenderOutputContext(context.Background(), ctx, path, format, projectID, compID)
}
func saveRenderOutputContext(c context.Context, ctx *sdk.AppCtx, path, format, projectID string, compID int64) int64 {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() <= 0 {
		return 0
	}
	args := map[string]any{"name": fmt.Sprintf("composition-%d-%d.%s", compID, time.Now().UnixNano(), format), "folder": "/.composer/", "content_type": renderContentType(format), "tags": []string{"composer", "render"}, "_project_id": projectID}
	call := func(tool string, args map[string]any, out any) error {
		if err := c.Err(); err != nil {
			return err
		}
		return ctx.PlatformAPI().CallAppResult("storage", tool, args, out)
	}
	if st.Size() <= 1<<20 {
		data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			return 0
		}
		args["content_base64"] = base64Encode(data)
		var got struct {
			ID int64 `json:"id"`
		}
		if call("files_upload", args, &got) != nil {
			return 0
		}
		return got.ID
	}
	args["size_bytes"] = st.Size()
	var init struct {
		UploadID string `json:"upload_id"`
		PartSize int    `json:"part_size"`
		File     struct {
			ID int64 `json:"id"`
		} `json:"file"`
	}
	if call("storage_upload_init", args, &init) != nil {
		return 0
	}
	if init.File.ID > 0 {
		return init.File.ID
	}
	if init.UploadID == "" {
		return 0
	}
	completed := false
	defer func() {
		if !completed {
			var out any
			_ = ctx.PlatformAPI().CallAppResult("storage", "storage_abort_upload", map[string]any{"id": init.UploadID, "reason": "Composer upload interrupted", "_project_id": projectID}, &out)
		}
	}()
	partSize := init.PartSize
	if partSize <= 0 || partSize > 1<<20 {
		partSize = 1 << 20
	}
	buf := make([]byte, partSize)
	for part := 1; ; part++ {
		n, err := io.ReadFull(f, buf)
		if err == io.EOF {
			break
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return 0
		}
		var out any
		if call("storage_upload_part", map[string]any{"upload_id": init.UploadID, "part_number": part, "content_base64": base64Encode(buf[:n]), "_project_id": projectID}, &out) != nil {
			return 0
		}
	}
	var result struct {
		File struct {
			ID int64 `json:"id"`
		} `json:"file"`
	}
	if call("storage_upload_complete", map[string]any{"upload_id": init.UploadID, "_project_id": projectID}, &result) != nil {
		return 0
	}
	completed = result.File.ID > 0
	return result.File.ID
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
		 FROM renders WHERE id=? AND project_id=?`, id, projectScope(ctx),
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
	url, err := resolveAssetLocal(ctx, src)
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
	url, err := resolveAssetURL(requestAppCtx(r), src)
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
		"_project_id": projectScopeFromArgs(requestAppCtx(r), map[string]any{"project_id": r.URL.Query().Get("project_id")}),
	}
	var got map[string]any
	if err := requestAppCtx(r).PlatformAPI().CallAppResult("storage", "files_list", args, &got); err != nil {
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
	out, err := a.toolCompositionList(requestAppCtx(r), map[string]any{
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
		out, err := a.toolCompositionCreate(requestAppCtx(r), body)
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
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/composition/"), "/"), "/")
	if len(parts) >= 2 && parts[1] == "outputs" {
		a.handleOutputs(w, r, id, parts[2:])
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolCompositionGet(requestAppCtx(r), map[string]any{"id": id})
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
		out, err := a.toolCompositionUpdate(requestAppCtx(r), map[string]any{"id": id, "patch": body})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResp(w, out)
	case http.MethodDelete:
		out, err := a.toolCompositionDelete(requestAppCtx(r), map[string]any{"id": id})
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
	out, err := a.toolCompositionRender(requestAppCtx(r), body)
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
	ctx := requestAppCtx(r)
	pid = projectScope(ctx)

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
	out, err := a.toolRenderStatus(requestAppCtx(r), map[string]any{"render_id": id})
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
	pid := projectScopeFromArgs(requestAppCtx(r), map[string]any{"project_id": r.URL.Query().Get("project_id")})
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
			"note":     "Composer V2 examples are hidden because COMPOSER_V2_ENABLED is disabled.",
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
