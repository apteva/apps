package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Async-job polling worker — shared by the video + avatar kinds.
//
// Both queue at the provider and return a handle; we poll a
// provider-specific status tool until the result is ready, then save
// bytes to storage (or local cache) and emit media.generated.
//
// Per-kind retrieve mechanics:
//   - video (venice-ai): retrieve_video → returns binary mp4 when done
//     (executor wraps as {_binary, base64}) or {status:"PROCESSING"}.
//   - avatar (tavus): get_video → {status, download_url}. On
//     status="ready" we fetch download_url's bytes ourselves.
//   - avatar (heygen): get_video → {data:{status, video_url}}. On
//     status="completed" we fetch video_url's bytes ourselves.
//
// Runs every 15s. Gives up after maxVideoPollAttempts (~20 min).

const (
	videoPollInterval    = 15 * time.Second
	maxVideoPollAttempts = 80 // 80 × 15s = 20 minutes
)

type pendingJob struct {
	ID             int64
	Kind           string
	Role           string
	ProjectID      string
	QueueID        string
	Provider       string
	Model          string
	Prompt         string
	SourceImageRef string
	CacheKey       string
	RequestJSON    string
	Attempts       int
}

func (a *App) videoPollWorker(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().Query(
		`SELECT id, kind, role, project_id, queue_id, provider, model, prompt,
		        source_image_ref, cache_key, request_json, attempts
		 FROM video_jobs
		 WHERE status IN ('queued', 'polling')
		 ORDER BY id ASC`,
	)
	if err != nil {
		return err
	}
	var jobs []pendingJob
	for rows.Next() {
		var p pendingJob
		if err := rows.Scan(&p.ID, &p.Kind, &p.Role, &p.ProjectID, &p.QueueID,
			&p.Provider, &p.Model, &p.Prompt, &p.SourceImageRef, &p.CacheKey, &p.RequestJSON, &p.Attempts); err != nil {
			continue
		}
		jobs = append(jobs, p)
	}
	rows.Close()
	if len(jobs) == 0 {
		return nil
	}

	for _, p := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Resolve the provider per the job's own role — a video job
		// polls the video_provider, an avatar job the avatar_provider.
		bound := app.IntegrationFor(p.Role)
		if bound == nil {
			app.Logger().Warn("poll: no provider bound for role; skipping", "role", p.Role, "job_id", p.ID)
			continue
		}
		a.pollOneJob(app, bound, p)
	}
	return nil
}

func (a *App) pollOneJob(app *sdk.AppCtx, bound *sdk.BoundIntegration, p pendingJob) {
	attempts := p.Attempts + 1

	if attempts > maxVideoPollAttempts {
		errMsg := fmt.Sprintf("gave up after %d polls (~%s)", maxVideoPollAttempts, time.Duration(maxVideoPollAttempts)*videoPollInterval)
		videoJobUpdateStatus(app, p.ID, "failed", errMsg)
		app.EmitWithProject("media.failed", p.ProjectID, map[string]any{
			"kind": p.Kind, "job_id": p.ID, "queue_id": p.QueueID, "error": errMsg,
		})
		return
	}

	switch p.Kind {
	case KindAvatar:
		a.pollAvatarJob(app, bound, p, attempts)
	default: // KindVideo
		a.pollVideoJob(app, bound, p, attempts)
	}
}

// --- video (binary-envelope) polling ------------------------------

func (a *App) pollVideoJob(app *sdk.AppCtx, bound *sdk.BoundIntegration, p pendingJob, attempts int) {
	res, err := app.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "retrieve_video",
		map[string]any{"queue_id": p.QueueID, "model": p.Model})
	if err != nil {
		bumpPolling(app, p.ID, attempts)
		app.Logger().Warn("video retrieve transient error", "id", p.ID, "err", err)
		return
	}
	if res == nil || !res.Success {
		errMsg := "provider non-2xx"
		if res != nil {
			errMsg = "provider status " + fmt.Sprint(res.Status) + ": " + truncate(string(res.Data), 300)
		}
		failJob(app, p, errMsg)
		return
	}
	var envelope struct {
		Binary   bool   `json:"_binary"`
		Base64   string `json:"base64"`
		MimeType string `json:"mimeType"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(res.Data, &envelope); err != nil {
		bumpPolling(app, p.ID, attempts)
		app.Logger().Warn("video retrieve parse failed", "id", p.ID, "err", err)
		return
	}
	if !envelope.Binary {
		bumpPolling(app, p.ID, attempts)
		return
	}
	mime := envelope.MimeType
	if mime == "" {
		mime = "video/mp4"
	}
	a.finalizeJob(app, p, envelope.Base64, mime)
}

// --- avatar (status + download_url) polling ------------------------

func (a *App) pollAvatarJob(app *sdk.AppCtx, bound *sdk.BoundIntegration, p pendingJob, attempts int) {
	switch bound.AppSlug {
	case "tavus":
		a.pollTavusAvatarJob(app, bound, p, attempts)
	case "heygen":
		a.pollHeyGenAvatarJob(app, bound, p, attempts)
	default:
		failJob(app, p, "avatar provider "+bound.AppSlug+" not wired")
	}
}

// pollHeyGenAvatarJob polls /v3/videos/{video_id}. HeyGen wraps the
// result in {data:{status, video_url, failure_message}}. status flows
// pending → processing → completed (or failed). On completed we fetch
// data.video_url's bytes ourselves.
func (a *App) pollHeyGenAvatarJob(app *sdk.AppCtx, bound *sdk.BoundIntegration, p pendingJob, attempts int) {
	res, err := app.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "get_video",
		map[string]any{"video_id": p.QueueID})
	if err != nil {
		bumpPolling(app, p.ID, attempts)
		app.Logger().Warn("heygen get_video transient error", "id", p.ID, "err", err)
		return
	}
	if res == nil || !res.Success {
		errMsg := "heygen non-2xx"
		if res != nil {
			errMsg = "heygen status " + fmt.Sprint(res.Status) + ": " + truncate(string(res.Data), 300)
		}
		failJob(app, p, errMsg)
		return
	}
	var body struct {
		Data struct {
			Status         string `json:"status"`    // pending | processing | completed | failed
			VideoURL       string `json:"video_url"` // direct mp4 when completed
			FailureMessage string `json:"failure_message"`
			Error          any    `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &body); err != nil {
		bumpPolling(app, p.ID, attempts)
		app.Logger().Warn("heygen status parse failed", "id", p.ID, "err", err)
		return
	}
	switch body.Data.Status {
	case "completed":
		if body.Data.VideoURL == "" {
			failJob(app, p, "heygen completed but no video_url")
			return
		}
		bytes, err := fetchBytes(body.Data.VideoURL)
		if err != nil {
			bumpPolling(app, p.ID, attempts)
			app.Logger().Warn("heygen download fetch failed", "id", p.ID, "err", err)
			return
		}
		a.finalizeJob(app, p, base64.StdEncoding.EncodeToString(bytes), "video/mp4")
	case "failed":
		errMsg := "heygen status=failed"
		if body.Data.FailureMessage != "" {
			errMsg += ": " + body.Data.FailureMessage
		}
		failJob(app, p, errMsg)
	default: // pending | processing
		bumpPolling(app, p.ID, attempts)
	}
}

func (a *App) pollTavusAvatarJob(app *sdk.AppCtx, bound *sdk.BoundIntegration, p pendingJob, attempts int) {
	res, err := app.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "get_video",
		map[string]any{"video_id": p.QueueID})
	if err != nil {
		bumpPolling(app, p.ID, attempts)
		app.Logger().Warn("tavus get_video transient error", "id", p.ID, "err", err)
		return
	}
	if res == nil || !res.Success {
		errMsg := "tavus non-2xx"
		if res != nil {
			errMsg = "tavus status " + fmt.Sprint(res.Status) + ": " + truncate(string(res.Data), 300)
		}
		failJob(app, p, errMsg)
		return
	}
	var body struct {
		Status      string `json:"status"`       // queued | generating | ready | deleted | error
		DownloadURL string `json:"download_url"` // direct mp4 (mux) when ready
		HostedURL   string `json:"hosted_url"`
	}
	if err := json.Unmarshal(res.Data, &body); err != nil {
		bumpPolling(app, p.ID, attempts)
		app.Logger().Warn("tavus get_video parse failed", "id", p.ID, "err", err)
		return
	}
	switch body.Status {
	case "ready":
		if body.DownloadURL == "" {
			failJob(app, p, "tavus ready but no download_url")
			return
		}
		bytes, err := fetchBytes(body.DownloadURL)
		if err != nil {
			// Transient network error fetching the finished file — retry.
			bumpPolling(app, p.ID, attempts)
			app.Logger().Warn("tavus download fetch failed", "id", p.ID, "err", err)
			return
		}
		a.finalizeJob(app, p, base64.StdEncoding.EncodeToString(bytes), "video/mp4")
	case "error", "deleted":
		failJob(app, p, "tavus status="+body.Status)
	default: // queued | generating
		bumpPolling(app, p.ID, attempts)
	}
}

// --- shared helpers ----------------------------------------------

func bumpPolling(app *sdk.AppCtx, jobID int64, attempts int) {
	app.AppDB().Exec(
		`UPDATE video_jobs SET attempts=?, last_poll_at=?, updated_at=?, status='polling' WHERE id=?`,
		attempts, time.Now(), time.Now(), jobID,
	)
}

func failJob(app *sdk.AppCtx, p pendingJob, errMsg string) {
	videoJobUpdateStatus(app, p.ID, "failed", errMsg)
	app.EmitWithProject("media.failed", p.ProjectID, map[string]any{
		"kind": p.Kind, "job_id": p.ID, "queue_id": p.QueueID, "error": errMsg,
	})
}

// finalizeJob saves the bytes to storage (or local cache), writes the
// generations row tagged with the job's kind, marks the video_jobs row
// complete, and emits media.generated.
func (a *App) finalizeJob(app *sdk.AppCtx, p pendingJob, base64Bytes, mime string) {
	scopedApp := app
	if p.ProjectID != "" {
		scopedApp = app.WithProject(p.ProjectID)
	}
	ext := extFromMime(mime)
	if ext == "bin" {
		ext = "mp4"
	}
	media := generatedMedia{B64: base64Bytes, MimeType: mime, Ext: ext}

	storageDir := "videos"
	capability := "video.generate"
	if p.Kind == KindAvatar {
		storageDir = "avatars"
		capability = "avatar.generate"
	}
	storageFolder := defaultStorageFolder(storageDir)
	if p.RequestJSON != "" {
		var req map[string]any
		if json.Unmarshal([]byte(p.RequestJSON), &req) == nil {
			if folder, err := storageFolderArg(req, storageDir); err == nil {
				storageFolder = folder
			}
		}
	}

	storage := app.IntegrationFor("storage")
	var storageIDs []int64
	if storage != nil {
		id, err := saveToStorage(scopedApp, media, storageFolder, p.Provider, 0)
		if err != nil {
			app.Logger().Warn("save-to-storage failed", "job_id", p.ID, "err", err)
		} else if id != 0 {
			storageIDs = append(storageIDs, id)
		}
	}

	extras := map[string]any{"queue_id": p.QueueID, "capability": capability}
	extras["storage_folder"] = storageFolder
	if p.CacheKey != "" {
		extras["cache_key"] = p.CacheKey
	}
	if p.SourceImageRef != "" {
		extras["source_image_ref"] = p.SourceImageRef
	}
	extraJSON, _ := json.Marshal(extras)

	var costUSD float64
	app.AppDB().QueryRow(`SELECT cost_usd FROM video_jobs WHERE id=?`, p.ID).Scan(&costUSD)

	generationID := a.dbInsertGeneration(generationRecord{
		ProjectID:    p.ProjectID,
		Kind:         p.Kind,
		Prompt:       p.Prompt,
		Provider:     p.Provider,
		Model:        p.Model,
		StorageIDs:   storageIDs,
		UpstreamURLs: []string{},
		ExtraJSON:    string(extraJSON),
		Count:        1,
		CostUSD:      costUSD,
		CacheKey:     p.CacheKey,
	})
	if storage == nil && generationID > 0 {
		if err := writeLocalCache(generationID, base64Bytes, ext); err != nil {
			app.Logger().Warn("writeLocalCache failed", "gen_id", generationID, "err", err)
		}
	}

	var storageID int64
	if len(storageIDs) > 0 {
		storageID = storageIDs[0]
	}
	videoJobMarkComplete(app, p.ID, storageID, generationID)

	app.EmitWithProject("media.generated", p.ProjectID, map[string]any{
		"kind":           p.Kind,
		"job_id":         p.ID,
		"queue_id":       p.QueueID,
		"model":          p.Model,
		"prompt":         p.Prompt,
		"storage_folder": storageFolder,
	})
}
