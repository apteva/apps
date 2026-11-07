package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const facebookThumbnailMaxBytes = 10 << 20

// Facebook Page video IDs are numeric. Feed post IDs are compound and
// photo deliveries that saved a Page post ID can be excluded up front.
// The worker still checks the native video and its owning Page.
func facebookThumbnailCandidate(id, pagePostID string) bool {
	return id != "" && pagePostID == "" && strings.Trim(id, "0123456789") == ""
}

func (a *App) editFacebookVideoThumbnail(ctx *sdk.AppCtx, out targetEditOutcome, connID int64, pageCreds, pagePostID string, eff, requested map[string]any, mediaProjectID string) targetEditOutcome {
	if !facebookThumbnailCandidate(out.PlatformPostID, pagePostID) {
		out.Status, out.Reason = "unsupported", "Facebook cover edits require a saved Page video ID"
		return out
	}
	if extractPageToken(pageCreds) == "" {
		out.Status, out.Error = "failed", "Facebook Page access token missing — reconnect the account"
		return out
	}
	state, fileID, sourceProject := videoThumbnailRequest(ctx, eff, mediaProjectID)
	if fileID <= 0 {
		out.Status, out.Error = "failed", "Choose a Storage image for the Facebook video cover"
		return out
	}
	if _, err := a.resolveThumbnailOption(ctx, eff, mediaProjectID); err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if _, bodyRequested := requested["body"]; bodyRequested {
		out = a.editFacebookPost(ctx, out, connID, pageCreds, eff)
		if out.Status != "ok" {
			return out
		}
	}
	options, err := json.Marshal(eff)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	encoded := string(options)
	if err := queueVideoThumbnailEdit(ctx, out, state, fileID, sourceProject, &encoded); err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	out.Status, out.ThumbnailStatus = "ok", state
	return out
}

func (a *App) applyFacebookVideoThumbnail(ctx *sdk.AppCtx, job videoThumbnailJob) error {
	token := extractPageToken(job.pageCredentials)
	if job.channel == "" || token == "" {
		return job.finish(ctx, "failed", "", "Facebook Page credentials are missing")
	}

	result, err := ctx.PlatformAPI().ExecuteIntegrationTool(job.connectionID, "facebook_get_video", map[string]any{
		"videoId": job.videoID, "access_token": token, "fields": "id,from{id},status",
		"_apteva": map[string]any{"timeout_ms": 8000},
	})
	if err != nil {
		return job.retry(ctx, err.Error())
	}
	if result == nil || !result.Success {
		if result != nil && result.Status >= 400 && result.Status < 500 && result.Status != http.StatusTooManyRequests {
			return job.finish(ctx, "failed", "", upstreamError(result).Error())
		}
		return job.retry(ctx, upstreamError(result).Error())
	}
	var video struct {
		ID   string `json:"id"`
		From struct {
			ID string `json:"id"`
		} `json:"from"`
		Status struct {
			VideoStatus string `json:"video_status"`
		} `json:"status"`
	}
	if err := json.Unmarshal(result.Data, &video); err != nil || video.ID != job.videoID || video.From.ID != job.channel {
		return job.finish(ctx, "failed", "", "Facebook video or Page did not match the saved delivery")
	}
	switch video.Status.VideoStatus {
	case "ready":
	case "":
		return job.finish(ctx, "failed", "", "Facebook did not return a video processing status for this object")
	case "error", "failed":
		return job.finish(ctx, "failed", "", "Facebook video processing failed")
	default:
		return job.retry(ctx, "Waiting for Facebook video processing to finish")
	}

	thumb, err := a.resolveThumbnailOption(ctx, map[string]any{"thumbnail_storage_id": job.fileID}, job.sourceProject)
	if err != nil {
		return job.retry(ctx, err.Error())
	}
	if thumb == nil {
		return job.finish(ctx, "failed", "", "Saved thumbnail image is unavailable")
	}
	body, err := fetchThumbnailSource(thumb.URL, facebookThumbnailMaxBytes)
	if err != nil {
		return job.retry(ctx, err.Error())
	}
	mime := http.DetectContentType(body)
	var filename string
	switch mime {
	case "image/jpeg":
		filename = "thumbnail.jpg"
	case "image/png":
		filename = "thumbnail.png"
	default:
		return job.finish(ctx, "failed", "", fmt.Sprintf("Facebook thumbnail must be a JPEG or PNG image, got %s", mime))
	}
	current, err := job.stillBound(ctx)
	if err != nil || !current {
		return err
	}
	result, err = ctx.PlatformAPI().ExecuteIntegrationTool(job.connectionID, "facebook_set_video_thumbnail", map[string]any{
		"videoId": job.videoID, "access_token": token, "source": thumbnailBinaryEnvelope(body, mime),
		"source_filename": filename, "is_preferred": true, "_apteva": map[string]any{"timeout_ms": 15000},
	})
	if err != nil {
		return job.finish(ctx, "failed", "", "Facebook thumbnail write outcome is unknown: "+err.Error())
	}
	if result == nil || !result.Success {
		if result != nil && result.Status == http.StatusTooManyRequests {
			return job.retry(ctx, upstreamError(result).Error())
		}
		return job.finish(ctx, "failed", "", upstreamError(result).Error())
	}
	var response struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(result.Data, &response); err != nil || !response.Success {
		return job.finish(ctx, "failed", "", "Facebook did not confirm that it applied the thumbnail")
	}
	return job.finish(ctx, "applied", "", "")
}
