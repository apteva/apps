package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

// Video is async on every provider we'd realistically wire (Venice's
// queue/retrieve, Runway's task-poll, Replicate's prediction loop).
// The dispatcher calls buildVideoArgs → ExecuteIntegrationTool on the
// provider's queue endpoint, gets back a job handle, and short-circuits
// the rest of the sync pipeline — see handleVideoQueueResponse below.

func buildVideoArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	switch providerSlug {
	case "venice-ai":
		return buildVeniceVideoQueueArgs(args)
	}
	return nil, fmt.Errorf("unsupported video provider slug: %q", providerSlug)
}

// buildVeniceVideoQueueArgs assembles Venice's POST /video/queue body.
// Required: model, prompt, duration. Source-image refs arrive as resolved
// args from the dispatcher; image-to-video uses image_url, while
// reference-to-video uses Venice's reference_image_urls array.
func buildVeniceVideoQueueArgs(args map[string]any) (map[string]any, error) {
	model := strArg(args, "model", "")
	if model == "" {
		return nil, errors.New("model required (call list_models?type=video for the live set)")
	}
	if videoModelRequiresSource(model) && len(resolvedSourceImages(args)) == 0 {
		return nil, errors.New("selected video model requires at least one source image")
	}
	prompt := strArg(args, "prompt", "")
	if count := utf8.RuneCountInString(prompt); count > veniceVideoPromptCharLimit {
		return nil, fmt.Errorf("%d characters exceeds Venice's %d-character video prompt limit", count, veniceVideoPromptCharLimit)
	}
	duration := strArg(args, "duration", "")
	if duration == "" {
		// Default to a short clip if the agent didn't specify.
		if d := intArg(args, "duration", 0); d > 0 {
			duration = fmt.Sprintf("%ds", d)
		} else {
			duration = "5s"
		}
	}
	out := map[string]any{
		"model":    model,
		"prompt":   prompt,
		"duration": duration,
	}
	if v := strArg(args, "aspect", ""); v != "" {
		out["aspect_ratio"] = v
	}
	if isReferenceToVideoModel(model) {
		if refs := resolvedSourceImages(args); len(refs) > 0 {
			urls := make([]string, 0, len(refs))
			for _, ref := range refs {
				urls = append(urls, ensureDataURL(ref))
			}
			out["reference_image_urls"] = urls
		}
	} else if v := strArg(args, "source_image", ""); v != "" {
		// source_image at this point is either a URL or a base64 string
		// (dispatcher resolved storage:N before us). Venice's image_url
		// accepts both forms — base64 must be a data: URL.
		out["image_url"] = ensureDataURL(v)
	}
	// Pass-through extras via the options bag.
	if opts, ok := args["options"].(map[string]any); ok {
		passThrough := []string{
			"negative_prompt", "resolution", "upscale_factor", "audio",
			"end_image_url", "audio_url", "video_url",
			"reference_image_urls", "reference_video_urls", "reference_audio_urls",
			"consents",
		}
		for _, k := range passThrough {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
	}
	return out, nil
}

func videoModelRequiresSource(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "image-to-video") || strings.Contains(model, "reference-to-video")
}

func isReferenceToVideoModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "reference-to-video")
}

// ensureDataURL leaves URLs untouched but wraps raw base64 in a
// data:image URL so Venice's image_url accepts it.
func ensureDataURL(s string) string {
	if len(s) >= 5 && (s[:5] == "http:" || s[:5] == "https") {
		return s
	}
	if len(s) >= 5 && s[:5] == "data:" {
		return s
	}
	// Mime-sniffing the raw bytes is overkill — default to png; Venice
	// re-decodes anyway.
	return "data:image/png;base64," + s
}

// normalizeVideoResponse parses the queue response into a generatedMedia
// list that carries the queue handle, not bytes. The dispatcher
// short-circuits the storage-save path when it sees this.
func normalizeVideoResponse(slug, capability string, raw json.RawMessage) ([]generatedMedia, string, string, error) {
	switch slug {
	case "venice-ai":
		var body struct {
			Model       string `json:"model"`
			QueueID     string `json:"queue_id"`
			DownloadURL string `json:"download_url"` // VPS models only
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, "", "", err
		}
		if body.QueueID == "" {
			return nil, "", "", fmt.Errorf("venice queue response missing queue_id: %s", truncate(string(raw), 200))
		}
		// Carry the queue_id through the UpstreamURL field (re-used as
		// a free-form handle); the dispatcher reads it from there.
		return []generatedMedia{{
			UpstreamURL: body.QueueID,
			MimeType:    "video/mp4",
			Ext:         "mp4",
		}}, "", body.Model, nil
	}
	return nil, "", "", fmt.Errorf("unsupported video provider slug: %q", slug)
}

// handleAsyncQueueResponse is invoked by the dispatcher AFTER an
// async provider's queue/create call succeeds (video or avatar). It
// inserts a video_jobs row tagged with the kind + role, emits a
// queued event, and shapes the MCP response. The worker (worker.go)
// takes it from here, routing the poll by kind.
func (a *App) handleAsyncQueueResponse(ctx *sdk.AppCtx, kind, role, providerSlug string, connectionID int64, args map[string]any, queueID, model string) any {
	if globalCtx == nil {
		return mcpError("app not mounted")
	}
	pid := projectScopeFromArgs(ctx, args)
	prompt := strArg(args, "prompt", "")
	sourceRef := persistedMediaRef(strArg(args, "_source_image_ref", ""))
	cacheKey := strArg(args, "cache_key", "")
	storageFolder := strArg(args, "_storage_folder", "")
	estimatedSeconds := estimatedDurationSeconds(kind, args)
	requestJSON := strArg(args, "_request_json", "")
	if requestJSON == "" {
		b, _ := json.Marshal(args)
		requestJSON = string(b)
	}
	draftID := int64Arg(args, "_draft_generation_id", 0)

	costUSD := 0.0
	if kind == KindVideo {
		costUSD = veniceVideoQuote(ctx, providerSlug, args)
	} else if kind == KindAvatar {
		costUSD = heygenAvatarQuote(providerSlug, args)
	}

	result, err := globalCtx.AppDB().Exec(
		`INSERT INTO video_jobs
			(project_id, kind, role, connection_id, queue_id, provider, model, prompt,
			 source_image_ref, request_json, status, cost_usd, cache_key, estimated_duration_seconds, generation_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?)`,
		pid, kind, role, connectionID, queueID, providerSlug, model, prompt, sourceRef, requestJSON, costUSD, cacheKey, estimatedSeconds, draftID,
	)
	if err != nil {
		ctx.Logger().Warn("video_jobs insert failed", "err", err)
		updateGenerationStatus(ctx, pid, draftID, "failed")
		return mcpError(kind + " queued at provider but local tracking row failed: " + err.Error())
	}
	jobID, _ := result.LastInsertId()
	if draftID > 0 {
		updateGenerationStatus(ctx, pid, draftID, "queued")
	}

	ctx.Emit(kind+".queued", map[string]any{
		"job_id":   jobID,
		"queue_id": queueID,
		"model":    model,
		"prompt":   prompt,
	})

	costLine := ""
	if costUSD > 0 {
		costLine = fmt.Sprintf("\nEstimated cost: $%.4f", costUSD)
	}
	noun := "Video"
	if kind == KindAvatar {
		noun = "Avatar video"
	}
	summary := fmt.Sprintf(
		"%s queued via %s (model=%s). Job #%d, handle=%s.\nPrompt: %q%s\n\n"+
			"The worker polls for completion every 15s. media.generated fires when it lands; "+
			"media_history surfaces the finished row.",
		noun, providerSlug, model, jobID, queueID, prompt, costLine,
	)
	meta := map[string]any{
		"kind":                       kind,
		"status":                     "queued",
		"job_id":                     jobID,
		"queue_id":                   queueID,
		"model":                      model,
		"provider":                   providerSlug,
		"generation_id":              draftID,
		"cost_usd":                   costUSD,
		"cache_key":                  cacheKey,
		"storage_folder":             storageFolder,
		"estimated_duration_seconds": estimatedSeconds,
		"chat_component":             generationChatComponent(draftID, jobID),
	}
	if refs, ok := args["_source_image_refs"].([]string); ok && len(refs) > 0 {
		safeRefs := persistedMediaRefs(refs)
		meta["source_image_refs"] = safeRefs
		meta["source_image_ref"] = safeRefs[0]
	} else if sourceRef != "" {
		meta["source_image_ref"] = sourceRef
		meta["source_image_refs"] = []string{sourceRef}
	}
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": summary},
		},
		"_meta": meta,
	}
}

// heygenAvatarQuote estimates API-key billing for HeyGen Avatar IV/V
// renders. HeyGen bills this path by output duration; before completion
// we only have the script, so callers may pass options.estimated_duration_seconds
// from the UI and we otherwise estimate speech at about 150 wpm.
func heygenAvatarQuote(providerSlug string, args map[string]any) float64 {
	if providerSlug != "heygen" {
		return 0
	}
	opts, _ := args["options"].(map[string]any)
	seconds := floatArg(opts, "estimated_duration_seconds", 0)
	if seconds <= 0 {
		seconds = estimateSpeechSecondsWithArgs(strArg(args, "prompt", ""), args)
	}
	if seconds <= 0 {
		return 0
	}
	avatarType := strArg(opts, "avatar_type", "photo_avatar")
	resolution := strings.ToLower(strArg(opts, "resolution", "1080p"))
	rate := 0.05
	if avatarType == "studio_avatar" || avatarType == "digital_twin" {
		rate = 0.0667
	}
	if resolution == "4k" {
		if avatarType == "photo_avatar" {
			rate = 0.0667
		} else {
			rate = 0.0833
		}
	}
	return seconds * rate
}

func floatArg(m map[string]any, key string, def float64) float64 {
	if m == nil {
		return def
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return def
}

// veniceVideoQuote calls POST /video/quote with the same shape we'd
// pass to /video/queue. Returns the USD quote or 0 on any failure.
// Best-effort — we never block the queue path on quote success.
func veniceVideoQuote(ctx *sdk.AppCtx, providerSlug string, args map[string]any) float64 {
	if providerSlug != "venice-ai" {
		return 0
	}
	bound := ctx.IntegrationFor("video_provider")
	if bound == nil {
		return 0
	}
	normalizeVeniceVideoArgsForModel(ctx, args, "video.generate")
	// Quote accepts a subset of queue's args — model + duration required;
	// aspect_ratio, resolution, upscale_factor, audio optional.
	quoteArgs := map[string]any{
		"model":    strArg(args, "model", ""),
		"duration": videoDurationArg(args),
	}
	if v := strArg(args, "aspect", ""); v != "" {
		quoteArgs["aspect_ratio"] = v
	}
	if opts, ok := args["options"].(map[string]any); ok {
		for _, k := range []string{"resolution", "upscale_factor", "audio"} {
			if v, exists := opts[k]; exists {
				quoteArgs[k] = v
			}
		}
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "quote_video", quoteArgs)
	if err != nil || res == nil || !res.Success {
		return 0
	}
	return estimateCostFromRaw(res.Data)
}

func videoDurationArg(args map[string]any) string {
	if duration := strArg(args, "duration", ""); duration != "" {
		return duration
	}
	if d := intArg(args, "duration", 0); d > 0 {
		return fmt.Sprintf("%ds", d)
	}
	return "5s"
}

func normalizeVeniceVideoArgsForModel(ctx *sdk.AppCtx, args map[string]any, capability string) {
	model := strings.TrimSpace(strArg(args, "model", ""))
	if model == "" {
		return
	}
	models, err := loadModelsForCapability(ctx, KindVideo, capability)
	if err != nil || len(models) == 0 {
		return
	}
	for _, entry := range models {
		if entry.ID != model {
			continue
		}
		if requested := durationArgSeconds(args); requested > 0 {
			if snapped := snapDurationToSupported(requested, entry.Durations); snapped > 0 {
				args["duration"] = fmt.Sprintf("%ds", snapped)
			}
		}
		// An empty published aspect list means Venice rejects the
		// aspect_ratio field entirely.
		if len(entry.AspectRatios) == 0 {
			delete(args, "aspect")
		}
		return
	}
}

func snapDurationToSupported(requested float64, supported []string) int {
	if requested <= 0 || len(supported) == 0 {
		return 0
	}
	bestAtOrAbove := 0
	largest := 0
	for _, raw := range supported {
		seconds := int(durationStringSeconds(raw))
		if seconds <= 0 {
			continue
		}
		if seconds > largest {
			largest = seconds
		}
		if float64(seconds)+0.001 >= requested && (bestAtOrAbove == 0 || seconds < bestAtOrAbove) {
			bestAtOrAbove = seconds
		}
	}
	if bestAtOrAbove > 0 {
		return bestAtOrAbove
	}
	return largest
}

func durationStringSeconds(raw string) float64 {
	return durationArgSeconds(map[string]any{"duration": raw})
}

// videoJobUpdateStatus updates a job row's status/error fields.
// Idempotent — no-op when the new status matches the current one.
func videoJobUpdateStatus(ctx *sdk.AppCtx, jobID int64, status, errMsg string) {
	_, err := ctx.AppDB().Exec(
		`UPDATE video_jobs
		 SET status=?, error=?, last_poll_at=?, updated_at=?
		 WHERE id=?`,
		status, errMsg, time.Now(), time.Now(), jobID,
	)
	if err != nil {
		ctx.Logger().Warn("video_jobs status update failed", "id", jobID, "err", err)
	}
}

// HTTP /video-jobs — panel polls this to render active processing jobs.
// Historical failures remain available through ?status=failed.
func (a *App) handleListVideoJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	statusFilter := r.URL.Query().Get("status")
	q := `SELECT id, queue_id, provider, model, prompt, status, error,
	             result_storage_id, generation_id, attempts, cost_usd,
	             estimated_duration_seconds, actual_duration_seconds, created_at, updated_at
	      FROM video_jobs
	      WHERE project_id = ?`
	args := []any{pid}
	if statusFilter != "" {
		q += ` AND status = ?`
		args = append(args, statusFilter)
	} else {
		q += ` AND status IN ('queued','polling','finalizing')`
	}
	q += ` ORDER BY id DESC LIMIT 100`

	rows, err := globalCtx.AppDB().Query(q, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var (
			id, storageID, generationID, attempts            int64
			queueID, provider, model, prompt, status, errMsg string
			createdAt, updatedAt                             string
			costUSD, estimatedDuration, actualDuration       float64
		)
		if err := rows.Scan(&id, &queueID, &provider, &model, &prompt,
			&status, &errMsg, &storageID, &generationID, &attempts,
			&costUSD, &estimatedDuration, &actualDuration, &createdAt, &updatedAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id":                         id,
			"queue_id":                   queueID,
			"provider":                   provider,
			"model":                      model,
			"prompt":                     prompt,
			"status":                     status,
			"error":                      errMsg,
			"result_storage_id":          storageID,
			"generation_id":              generationID,
			"attempts":                   attempts,
			"cost_usd":                   costUSD,
			"estimated_duration_seconds": estimatedDuration,
			"actual_duration_seconds":    actualDuration,
			"created_at":                 createdAt,
			"updated_at":                 updatedAt,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jobs": out})
}

// HTTP /video-jobs/<id> lets a chat attachment follow an asynchronous
// generation until it can promote itself to the completed generation.
func (a *App) handleGetVideoJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	id, err := pathID(r.URL.Path, "/video-jobs/")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var (
		storageID, generationID, attempts                      int64
		queueID, kind, provider, model, prompt, status, errMsg string
		createdAt, updatedAt                                   string
		costUSD, estimatedDuration, actualDuration             float64
	)
	err = globalCtx.AppDB().QueryRow(
		`SELECT queue_id, kind, provider, model, prompt, status, error,
		        result_storage_id, generation_id, attempts, cost_usd,
		        estimated_duration_seconds, actual_duration_seconds, created_at, updated_at
		 FROM video_jobs WHERE id = ? AND project_id = ?`,
		id, pid,
	).Scan(&queueID, &kind, &provider, &model, &prompt, &status, &errMsg,
		&storageID, &generationID, &attempts, &costUSD, &estimatedDuration,
		&actualDuration, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"job": map[string]any{
		"id": id, "queue_id": queueID, "kind": kind, "provider": provider,
		"model": model, "prompt": prompt, "status": status, "error": errMsg,
		"result_storage_id": storageID, "generation_id": generationID,
		"attempts": attempts, "cost_usd": costUSD,
		"estimated_duration_seconds": estimatedDuration,
		"actual_duration_seconds":    actualDuration,
		"created_at":                 createdAt, "updated_at": updatedAt,
	}})
}

// videoJobMarkComplete records the storage handoff + generations row id.
func videoJobMarkComplete(ctx *sdk.AppCtx, jobID, storageID, generationID int64, actualDuration float64) {
	_, err := ctx.AppDB().Exec(
		`UPDATE video_jobs
		 SET status='complete', result_storage_id=?, generation_id=?,
		     actual_duration_seconds=?, last_poll_at=?, updated_at=?
		 WHERE id=?`,
		storageID, generationID, actualDuration, time.Now(), time.Now(), jobID,
	)
	if err != nil {
		ctx.Logger().Warn("video_jobs complete update failed", "id", jobID, "err", err)
	}
}

const veniceVideoPromptCharLimit = 2500
