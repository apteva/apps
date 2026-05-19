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

// --- composition CRUD ---------------------------------------------

func (a *App) toolCompositionCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	edit, err := editFromArgs(args)
	if err != nil {
		return nil, err
	}
	output := outputFromArgs(args)
	editJSON, _ := json.Marshal(edit)
	outputJSON, _ := json.Marshal(output)
	pid := os.Getenv("APTEVA_PROJECT_ID")
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
	return map[string]any{"id": id, "duration_seconds": dur}, nil
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
		name, editJSON, outputJSON string
	)
	if err := ctx.AppDB().QueryRow(
		`SELECT name, edit_json, output_json FROM compositions WHERE id=?`, id,
	).Scan(&name, &editJSON, &outputJSON); err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	edit, _ := parseEditJSON(editJSON)
	var output Output
	_ = json.Unmarshal([]byte(outputJSON), &output)

	// Apply patch — only the fields the validator knows.
	if v := strArg(patch, "name", ""); v != "" {
		name = v
	}
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
	return map[string]any{"id": id, "duration_seconds": dur}, nil
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
	row["latest_render"] = loadLatestRender(ctx, id)
	return row, nil
}

func (a *App) toolCompositionList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	limit := intArg(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, name, edit_json, output_json, duration_seconds, created_at, updated_at
		 FROM compositions WHERE project_id=? ORDER BY id DESC LIMIT ?`, pid, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id                          int64
			name, editJSON, outputJSON  string
			dur                         float64
			createdAt, updatedAt        string
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
			"latest_render":    loadLatestRender(ctx, id),
		})
	}
	return map[string]any{"compositions": out}, nil
}

func (a *App) toolCompositionDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	if id == 0 {
		return nil, errors.New("id required")
	}
	_, err := ctx.AppDB().Exec(`DELETE FROM compositions WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "deleted": true}, nil
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
		id, storageID, durMS, attempts int64
		executor, status, errMsg       string
		costUSD                        float64
		createdAt, updatedAt           string
	)
	err := ctx.AppDB().QueryRow(
		`SELECT id, executor, status, storage_id, duration_ms, cost_usd, error, attempts, created_at, updated_at
		 FROM renders WHERE composition_id=? ORDER BY id DESC LIMIT 1`, compID,
	).Scan(&id, &executor, &status, &storageID, &durMS, &costUSD, &errMsg, &attempts, &createdAt, &updatedAt)
	if err != nil {
		return nil
	}
	row := map[string]any{
		"id":          id,
		"executor":    executor,
		"status":      status,
		"storage_id":  storageID,
		"duration_ms": durMS,
		"cost_usd":    costUSD,
		"error":       errMsg,
		"attempts":    attempts,
		"created_at":  createdAt,
		"updated_at":  updatedAt,
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

	row, err := loadComposition(ctx, id)
	if err != nil {
		return nil, err
	}
	edit, err := parseEditJSON(row["edit_json"].(string))
	if err != nil {
		return nil, fmt.Errorf("composition.edit_json invalid: %w", err)
	}
	var output Output
	_ = json.Unmarshal([]byte(row["output_json"].(string)), &output)
	validateOutput(&output)

	exec, err := chooseExecutor(ctx, executorOverride)
	if err != nil {
		return nil, err
	}

	pid := row["project_id"].(string)
	editSnapshot, _ := json.Marshal(edit)

	insertRes, err := ctx.AppDB().Exec(
		`INSERT INTO renders (composition_id, project_id, executor, status, edit_snapshot)
		 VALUES (?, ?, ?, 'rendering', ?)`,
		id, pid, exec.Name(), string(editSnapshot),
	)
	if err != nil {
		return nil, fmt.Errorf("insert render: %w", err)
	}
	renderID, _ := insertRes.LastInsertId()

	rctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	result, err := exec.Render(rctx, ctx, edit, output, pid)
	if err != nil {
		ctx.AppDB().Exec(
			`UPDATE renders SET status='failed', error=?, ffmpeg_command=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			err.Error(), result.FFmpegCommand, renderID,
		)
		ctx.EmitWithProject("composition.failed", pid, map[string]any{
			"composition_id": id, "render_id": renderID, "error": err.Error(),
		})
		return nil, err
	}

	// Sync executors deliver bytes via LocalPath. Persist to storage
	// (when bound) or local cache (when not).
	var storageID int64
	if result.Sync && result.LocalPath != "" && !strings.HasPrefix(result.LocalPath, "storage://") {
		storageID = saveRenderOutput(ctx, result.LocalPath, output.Format, pid, id)
		if storageID == 0 {
			if cacheErr := writeLocalCacheFromPath(renderID, result.LocalPath, output.Format); cacheErr != nil {
				ctx.Logger().Warn("local cache write failed", "render_id", renderID, "err", cacheErr)
			}
		}
	}

	ctx.AppDB().Exec(
		`UPDATE renders
		 SET status='complete', storage_id=?, duration_ms=?, cost_usd=?,
		     ffmpeg_command=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		storageID, result.DurationMS, result.CostUSD, result.FFmpegCommand, renderID,
	)

	ctx.EmitWithProject("composition.rendered", pid, map[string]any{
		"composition_id": id,
		"render_id":      renderID,
		"executor":       exec.Name(),
		"storage_id":     storageID,
		"duration_ms":    result.DurationMS,
	})

	return map[string]any{
		"render_id":   renderID,
		"status":      "complete",
		"storage_id":  storageID,
		"executor":    exec.Name(),
		"duration_ms": result.DurationMS,
		"cost_usd":    result.CostUSD,
	}, nil
}

// saveRenderOutput uploads the bytes to storage and returns the
// resulting storage id (or 0 when storage is unbound / fails).
// Reads the file into memory via base64 — fine for v0.1 video sizes;
// streaming upload is a follow-up if outputs grow past ~50 MB.
func saveRenderOutput(ctx *sdk.AppCtx, path, format, projectID string, compID int64) int64 {
	storage := ctx.IntegrationFor("storage")
	if storage == nil {
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
		"content_type":   "video/" + format,
		"tags":           []string{"composer", "render"},
	}, &got)
	if err != nil {
		ctx.Logger().Warn("storage upload failed", "err", err)
		return 0
	}
	return got.ID
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
		compID, storageID, durMS, attempts int64
		executor, status, errMsg           string
		costUSD                            float64
		createdAt, updatedAt               string
	)
	err := ctx.AppDB().QueryRow(
		`SELECT composition_id, executor, status, storage_id, duration_ms, cost_usd, error, attempts, created_at, updated_at
		 FROM renders WHERE id=?`, id,
	).Scan(&compID, &executor, &status, &storageID, &durMS, &costUSD, &errMsg, &attempts, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("not found: %w", err)
	}
	return map[string]any{
		"render_id":      id,
		"composition_id": compID,
		"executor":       executor,
		"status":         status,
		"storage_id":     storageID,
		"duration_ms":    durMS,
		"cost_usd":       costUSD,
		"error":          errMsg,
		"attempts":       attempts,
		"created_at":     createdAt,
		"updated_at":     updatedAt,
	}, nil
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
	out, err := a.toolCompositionList(globalCtx, map[string]any{"limit": 200})
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
	case http.MethodPost:
		// POST /composition/ creates a new composition from the body
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := a.toolCompositionCreate(globalCtx, body)
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
	out := map[string]any{
		"storage_bound":     globalCtx.IntegrationFor("storage") != nil,
		"instances_bound":   globalCtx.IntegrationFor("instances") != nil,
		"mediastudio_bound": globalCtx.IntegrationFor("media-studio") != nil,
		"render_host_id":    renderHostID(),
		"ffmpeg_path":       ffmpegPath(),
	}
	if bound := globalCtx.IntegrationFor("render_executor"); bound != nil {
		out["render_executor"] = bound.AppSlug
	}
	jsonResp(w, out)
}

func jsonResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
