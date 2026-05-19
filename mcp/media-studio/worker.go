package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Async-video polling worker.
//
// Venice's /video/queue returns a queue_id and we have to poll
// /video/retrieve until it returns binary mp4 (vs JSON
// {status:"PROCESSING"}). Runs every 15s — typical Venice video
// generation is 30s–3min so 15s strikes a balance between latency
// and wasted retrieve calls.
//
// On success: bytes go to storage, a generations row lands,
// media.generated event fires, the video_jobs row flips to complete.
// On failure (provider error, exhausted attempts, malformed response):
// row flips to failed, media.failed event fires.

const (
	videoPollInterval   = 15 * time.Second
	maxVideoPollAttempts = 80 // 80 × 15s = 20 minutes — beyond that we give up
)

func (a *App) videoPollWorker(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := app.AppDB().Query(
		`SELECT id, project_id, queue_id, provider, model, prompt,
		        source_image_ref, attempts
		 FROM video_jobs
		 WHERE status IN ('queued', 'polling')
		 ORDER BY id ASC`,
	)
	if err != nil {
		return err
	}
	type pending struct {
		ID             int64
		ProjectID      string
		QueueID        string
		Provider       string
		Model          string
		Prompt         string
		SourceImageRef string
		Attempts       int
	}
	var jobs []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.QueueID, &p.Provider,
			&p.Model, &p.Prompt, &p.SourceImageRef, &p.Attempts); err != nil {
			continue
		}
		jobs = append(jobs, p)
	}
	rows.Close()
	if len(jobs) == 0 {
		return nil
	}

	bound := app.IntegrationFor("video_provider")
	if bound == nil {
		// Operator unbound the provider mid-flight; nothing we can do.
		// Leave the jobs in their current state — re-binding restores
		// progress without losing the queue_ids.
		app.Logger().Warn("video poll: no video_provider bound; skipping", "in_flight", len(jobs))
		return nil
	}

	for _, p := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.pollOneVideoJob(app, bound, p.ID, p.ProjectID, p.QueueID, p.Provider, p.Model, p.Prompt, p.SourceImageRef, p.Attempts)
	}
	return nil
}

func (a *App) pollOneVideoJob(
	app *sdk.AppCtx,
	bound *sdk.BoundIntegration,
	jobID int64,
	projectID, queueID, provider, model, prompt, sourceRef string,
	attempts int,
) {
	attempts++

	// Bail out on chronic failures so the worker stops spinning forever.
	if attempts > maxVideoPollAttempts {
		errMsg := fmt.Sprintf("gave up after %d polls (%s)", maxVideoPollAttempts, time.Duration(maxVideoPollAttempts*15)*time.Second)
		videoJobUpdateStatus(app, jobID, "failed", errMsg)
		app.Emit("media.failed", map[string]any{
			"kind": KindVideo, "job_id": jobID, "queue_id": queueID, "error": errMsg,
		})
		return
	}

	// Provider-specific tool name (Venice's retrieve is "retrieve_video";
	// future providers may name theirs differently).
	retrieveTool := "retrieve_video"

	res, err := app.PlatformAPI().ExecuteIntegrationTool(
		bound.ConnectionID,
		retrieveTool,
		map[string]any{
			"queue_id": queueID,
			"model":    model,
		},
	)
	if err != nil {
		// Transient — bump attempts, leave status alone so we retry.
		app.AppDB().Exec(
			`UPDATE video_jobs SET attempts=?, last_poll_at=?, updated_at=?, status='polling' WHERE id=?`,
			attempts, time.Now(), time.Now(), jobID,
		)
		app.Logger().Warn("video retrieve transient error", "id", jobID, "err", err)
		return
	}
	if res == nil || !res.Success {
		// Provider returned a non-2xx — treat as terminal for now.
		errMsg := "provider non-2xx"
		if res != nil {
			errMsg = "provider returned status " + fmt.Sprint(res.Status) + ": " + truncate(string(res.Data), 300)
		}
		videoJobUpdateStatus(app, jobID, "failed", errMsg)
		app.Emit("media.failed", map[string]any{
			"kind": KindVideo, "job_id": jobID, "queue_id": queueID, "error": errMsg,
		})
		return
	}

	// Two response shapes possible:
	//   - {_binary:true, base64, mimeType}     → COMPLETED
	//   - {status:"PROCESSING", …}             → still cooking
	var envelope struct {
		Binary   bool   `json:"_binary"`
		Base64   string `json:"base64"`
		MimeType string `json:"mimeType"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(res.Data, &envelope); err != nil {
		// Couldn't even parse — log and retry.
		app.AppDB().Exec(
			`UPDATE video_jobs SET attempts=?, last_poll_at=?, updated_at=?, status='polling' WHERE id=?`,
			attempts, time.Now(), time.Now(), jobID,
		)
		app.Logger().Warn("video retrieve parse failed", "id", jobID, "err", err)
		return
	}

	if !envelope.Binary {
		// Still processing — bump attempts, set polling status, move on.
		app.AppDB().Exec(
			`UPDATE video_jobs SET attempts=?, last_poll_at=?, updated_at=?, status='polling' WHERE id=?`,
			attempts, time.Now(), time.Now(), jobID,
		)
		return
	}

	// Completed — bytes are in envelope.Base64.
	mime := envelope.MimeType
	if mime == "" {
		mime = "video/mp4"
	}
	a.finalizeVideoJob(app, jobID, projectID, queueID, provider, model, prompt, sourceRef, envelope.Base64, mime)
}

// finalizeVideoJob saves the bytes to storage (when bound), writes the
// generations row, marks the video_jobs row complete, and emits the
// media.generated event so the panel refreshes.
func (a *App) finalizeVideoJob(
	app *sdk.AppCtx,
	jobID int64,
	projectID, queueID, provider, model, prompt, sourceRef, base64Bytes, mime string,
) {
	ext := extFromMime(mime)
	if ext == "bin" {
		ext = "mp4"
	}
	media := generatedMedia{
		B64:      base64Bytes,
		MimeType: mime,
		Ext:      ext,
	}

	storage := app.IntegrationFor("storage")
	var storageIDs []int64
	if storage != nil {
		id, err := saveToStorage(app, media, "videos", provider, 0)
		if err != nil {
			app.Logger().Warn("video save-to-storage failed", "job_id", jobID, "err", err)
		} else if id != 0 {
			storageIDs = append(storageIDs, id)
		}
	}

	extras := map[string]any{"queue_id": queueID, "capability": "video.generate"}
	if sourceRef != "" {
		extras["source_image_ref"] = sourceRef
	}
	extraJSON, _ := json.Marshal(extras)

	sj, _ := json.Marshal(storageIDs)
	uj, _ := json.Marshal([]string{}) // no upstream URL for Venice's binary path
	res, err := app.AppDB().Exec(
		`INSERT INTO generations
			(project_id, kind, prompt, revised_prompt, provider, model,
			 size, duration_ms, storage_ids, upstream_urls, thumbnail_b64,
			 extra_json, count)
		 VALUES (?, ?, ?, '', ?, ?, '', 0, ?, ?, '', ?, 1)`,
		projectID, KindVideo, prompt, provider, model,
		string(sj), string(uj), string(extraJSON),
	)
	var generationID int64
	if err != nil {
		app.Logger().Warn("video generations insert failed", "job_id", jobID, "err", err)
	} else {
		generationID, _ = res.LastInsertId()
	}

	var storageID int64
	if len(storageIDs) > 0 {
		storageID = storageIDs[0]
	}
	videoJobMarkComplete(app, jobID, storageID, generationID)

	app.Emit("media.generated", map[string]any{
		"kind":     KindVideo,
		"job_id":   jobID,
		"queue_id": queueID,
		"model":    model,
		"prompt":   prompt,
	})
}
