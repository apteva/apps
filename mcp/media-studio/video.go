package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

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
// Required: model, prompt, duration. Source-image (for image-to-video)
// arrives as args["source_image"]; we pass it through as image_url —
// Venice accepts both HTTPS URLs and data: URLs, and the dispatcher
// has already resolved storage:N handles before the build phase.
func buildVeniceVideoQueueArgs(args map[string]any) (map[string]any, error) {
	model := strArg(args, "model", "")
	if model == "" {
		return nil, errors.New("model required (call list_models?type=video for the live set)")
	}
	prompt := strArg(args, "prompt", "")
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
	if v := strArg(args, "source_image", ""); v != "" {
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
		}
		for _, k := range passThrough {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
	}
	return out, nil
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

// handleVideoQueueResponse is invoked by the dispatcher AFTER the
// provider's queue call succeeds. It inserts a video_jobs row,
// emits a queued event, and shapes the MCP response. The worker
// (worker.go) takes it from here.
func (a *App) handleVideoQueueResponse(ctx *sdk.AppCtx, providerSlug string, args map[string]any, queueID, model string) any {
	if globalCtx == nil {
		return mcpError("app not mounted")
	}
	pid := strArg(args, "project_id", "")
	if pid == "" {
		pid = os.Getenv("APTEVA_PROJECT_ID")
	}
	prompt := strArg(args, "prompt", "")
	sourceRef := strArg(args, "_source_image_ref", "")
	requestJSON, _ := json.Marshal(args)

	// Best-effort cost lookup via /video/quote. If the quote call fails
	// we still want the queue/poll path to work, so cost stays 0 and
	// the panel just renders the row without a price tag.
	costUSD := veniceVideoQuote(ctx, providerSlug, args)

	result, err := globalCtx.AppDB().Exec(
		`INSERT INTO video_jobs
			(project_id, queue_id, provider, model, prompt,
			 source_image_ref, request_json, status, cost_usd)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', ?)`,
		pid, queueID, providerSlug, model, prompt, sourceRef, string(requestJSON), costUSD,
	)
	if err != nil {
		ctx.Logger().Warn("video_jobs insert failed", "err", err)
		return mcpError("video queued at provider but local tracking row failed: " + err.Error())
	}
	jobID, _ := result.LastInsertId()

	ctx.Emit("video.queued", map[string]any{
		"job_id":   jobID,
		"queue_id": queueID,
		"model":    model,
		"prompt":   prompt,
	})

	costLine := ""
	if costUSD > 0 {
		costLine = fmt.Sprintf("\nEstimated cost: $%.4f", costUSD)
	}
	summary := fmt.Sprintf(
		"Video queued via %s (model=%s). Job #%d, queue_id=%s.\nPrompt: %q%s\n\n"+
			"The worker will poll for completion every 15s. media.generated will fire when the video lands; "+
			"media_history will surface the finished row.",
		providerSlug, model, jobID, queueID, prompt, costLine,
	)
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": summary},
		},
		"_meta": map[string]any{
			"kind":     KindVideo,
			"status":   "queued",
			"job_id":   jobID,
			"queue_id": queueID,
			"model":    model,
			"provider": providerSlug,
			"cost_usd": costUSD,
		},
	}
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
	// Quote accepts a subset of queue's args — model + duration required;
	// aspect_ratio, resolution, upscale_factor, audio optional.
	quoteArgs := map[string]any{
		"model":    strArg(args, "model", ""),
		"duration": strArg(args, "duration", "5s"),
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
	var body struct {
		Quote float64 `json:"quote"`
	}
	if err := json.Unmarshal(res.Data, &body); err != nil {
		return 0
	}
	return body.Quote
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

// HTTP /video-jobs — panel polls this to render an "X processing" badge
// and to surface failures the user might otherwise miss.
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
	             result_storage_id, generation_id, attempts, cost_usd, created_at, updated_at
	      FROM video_jobs
	      WHERE project_id = ?`
	args := []any{pid}
	if statusFilter != "" {
		q += ` AND status = ?`
		args = append(args, statusFilter)
	} else {
		// Default: in-flight + recently-failed (last 24h).
		q += ` AND (status IN ('queued','polling','failed') OR updated_at > datetime('now','-1 day'))`
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
			costUSD                                          float64
		)
		if err := rows.Scan(&id, &queueID, &provider, &model, &prompt,
			&status, &errMsg, &storageID, &generationID, &attempts,
			&costUSD, &createdAt, &updatedAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id":                id,
			"queue_id":          queueID,
			"provider":          provider,
			"model":             model,
			"prompt":            prompt,
			"status":            status,
			"error":             errMsg,
			"result_storage_id": storageID,
			"generation_id":     generationID,
			"attempts":          attempts,
			"cost_usd":          costUSD,
			"created_at":        createdAt,
			"updated_at":        updatedAt,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jobs": out})
}

// videoJobMarkComplete records the storage handoff + generations row id.
func videoJobMarkComplete(ctx *sdk.AppCtx, jobID, storageID, generationID int64) {
	_, err := ctx.AppDB().Exec(
		`UPDATE video_jobs
		 SET status='complete', result_storage_id=?, generation_id=?,
		     last_poll_at=?, updated_at=?
		 WHERE id=?`,
		storageID, generationID, time.Now(), time.Now(), jobID,
	)
	if err != nil {
		ctx.Logger().Warn("video_jobs complete update failed", "id", jobID, "err", err)
	}
}
