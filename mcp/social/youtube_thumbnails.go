package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Queue the image in the same write as the saved video ID. A thumbnail job
// never participates in the video upload or publication retry lifecycle.
const queueYouTubeThumbnailSQL = `youtube_thumbnail_state=?, youtube_thumbnail_video_id=?,
	youtube_thumbnail_file_id=?, youtube_thumbnail_project_id=?, youtube_thumbnail_due_at=0,
	youtube_thumbnail_attempts=0, youtube_thumbnail_receipt='', youtube_thumbnail_error=''`

const (
	youtubeThumbnailLeaseSeconds      = 120
	youtubeThumbnailProcessingPolls   = 120
	youtubeThumbnailVerificationPolls = 15
)

func youtubeThumbnailRequest(ctx *sdk.AppCtx, options map[string]any, mediaProject string) (string, int64, string) {
	id := thumbnailStorageID(options)
	if id <= 0 {
		return "", 0, ""
	}
	project := nonEmpty(strOption(options, "thumbnail_project_id"), strings.TrimSpace(mediaProject), ctx.CurrentProject())
	return "pending", id, project
}

type youtubeThumbnailJob struct {
	targetID, connectionID, fileID                           int64
	state, receipt, videoID, project, sourceProject, channel string
	attempts                                                 int
	leaseUntil                                               int64
}

func (a *App) runYouTubeThumbnails(runCtx context.Context, ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().QueryContext(runCtx, `SELECT id,post_id FROM post_targets
		WHERE youtube_thumbnail_state IN ('pending','verifying') AND youtube_thumbnail_due_at<=unixepoch()
		ORDER BY youtube_thumbnail_due_at,id LIMIT 12`)
	if err != nil {
		return err
	}
	var targets [][2]int64
	for rows.Next() {
		var ids [2]int64
		if err := rows.Scan(&ids[0], &ids[1]); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, ids)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ids := range targets {
		if err := runCtx.Err(); err != nil {
			return err
		}
		unlock := lockSocialPost(ctx, ids[1])
		err := a.advanceYouTubeThumbnail(ctx, ids[0])
		unlock()
		if err != nil {
			ctx.Logger().Warn("YouTube thumbnail job could not be advanced", "target_id", ids[0], "err", redactDeliverySecrets(err.Error()))
		}
	}
	return nil
}

func (a *App) advanceYouTubeThumbnail(ctx *sdk.AppCtx, targetID int64) error {
	job := youtubeThumbnailJob{targetID: targetID}
	var bound bool
	err := ctx.AppDB().QueryRow(`SELECT t.youtube_thumbnail_state,t.youtube_thumbnail_receipt,t.youtube_thumbnail_video_id,
		t.youtube_thumbnail_file_id,t.youtube_thumbnail_project_id,t.youtube_thumbnail_attempts,
		p.project_id,sa.connection_id,COALESCE(sa.external_account_id,''),
		COALESCE(t.platform_post_id=t.youtube_thumbnail_video_id AND t.youtube_thumbnail_video_id<>'' AND
			t.status IN ('published','awaiting_audience_setup') AND sa.status='active' AND sa.platform='youtube'
			AND COALESCE(sa.provider_slug,'native')='native' AND sa.project_id=p.project_id,0)
		FROM post_targets t JOIN posts p ON p.id=t.post_id JOIN social_accounts sa ON sa.id=t.social_account_id
		WHERE t.id=? AND t.youtube_thumbnail_state IN ('pending','verifying') AND t.youtube_thumbnail_due_at<=unixepoch()`, targetID).
		Scan(&job.state, &job.receipt, &job.videoID, &job.fileID, &job.sourceProject, &job.attempts, &job.project, &job.connectionID, &job.channel, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	err = ctx.AppDB().QueryRow(`UPDATE post_targets SET youtube_thumbnail_due_at=unixepoch()+?,youtube_thumbnail_attempts=youtube_thumbnail_attempts+1
		WHERE id=? AND youtube_thumbnail_state=? AND youtube_thumbnail_video_id=? AND youtube_thumbnail_file_id=?
		AND youtube_thumbnail_project_id=? AND youtube_thumbnail_attempts=? AND youtube_thumbnail_due_at<=unixepoch()
		RETURNING youtube_thumbnail_due_at`,
		youtubeThumbnailLeaseSeconds, job.targetID, job.state, job.videoID, job.fileID, job.sourceProject, job.attempts).Scan(&job.leaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	job.attempts++
	ctx = ctx.WithProject(job.project)
	if !bound {
		return job.finish(ctx, "failed", job.receipt, "YouTube account or saved video no longer matches the delivery")
	}

	video, err := readYouTubeThumbnailVideo(ctx, job)
	if err != nil {
		return job.retry(ctx, err.Error())
	}
	if video.ID != job.videoID || (job.channel != "" && video.Snippet.ChannelID != job.channel) {
		return job.finish(ctx, "failed", job.receipt, "YouTube video or channel did not match the saved delivery")
	}
	if job.state == "verifying" {
		var receipt map[string]string
		if err := json.Unmarshal([]byte(job.receipt), &receipt); err != nil || len(receipt) == 0 {
			return job.finish(ctx, "failed", job.receipt, "YouTube accepted the image but did not supply a thumbnail receipt to verify")
		}
		if youtubeThumbnailMatches(video, receipt) {
			return job.finish(ctx, "verified", job.receipt, "")
		}
		return job.retry(ctx, "YouTube has not returned the applied custom thumbnail yet")
	}

	switch video.Processing.Status {
	case "succeeded":
		// Proceed only once YouTube reports successful processing.
	case "failed", "terminated":
		return job.finish(ctx, "failed", "", "YouTube video processing "+video.Processing.Status)
	default:
		return job.retry(ctx, "Waiting for YouTube video processing to succeed")
	}
	thumb, err := a.resolveThumbnailOption(ctx, map[string]any{"thumbnail_storage_id": job.fileID}, job.sourceProject)
	if err != nil {
		return job.retry(ctx, err.Error())
	}
	if thumb == nil {
		return job.finish(ctx, "failed", "", "Saved thumbnail image is unavailable")
	}
	current, err := job.stillBound(ctx)
	if err != nil || !current {
		return err
	}
	receipt, err := a.setBinaryThumbnail(ctx, platforms["youtube"], job.connectionID, job.videoID, *thumb)
	if err != nil {
		var providerErr *historyReadError
		if errors.As(err, &providerErr) && providerErr.status >= 400 && providerErr.status < 500 && providerErr.status != 429 {
			return job.finish(ctx, "failed", "", err.Error())
		}
		return job.retry(ctx, err.Error())
	}
	if len(receipt) == 0 {
		return job.finish(ctx, "failed", "", "YouTube accepted the image but did not supply a thumbnail receipt to verify")
	}
	encoded, _ := json.Marshal(receipt)
	return job.finish(ctx, "verifying", string(encoded), "")
}

func (j youtubeThumbnailJob) stillBound(ctx *sdk.AppCtx) (bool, error) {
	var ok bool
	err := ctx.AppDB().QueryRow(`SELECT EXISTS(SELECT 1 FROM post_targets t
		JOIN social_accounts sa ON sa.id=t.social_account_id JOIN posts p ON p.id=t.post_id
		WHERE t.id=? AND t.youtube_thumbnail_state='pending' AND t.youtube_thumbnail_due_at=?
		AND t.youtube_thumbnail_attempts=? AND t.youtube_thumbnail_video_id=? AND t.platform_post_id=?
		AND t.youtube_thumbnail_file_id=? AND t.youtube_thumbnail_project_id=?
		AND t.status IN ('published','awaiting_audience_setup')
		AND sa.platform='youtube' AND sa.status='active' AND COALESCE(sa.provider_slug,'native')='native'
		AND sa.connection_id=? AND COALESCE(sa.external_account_id,'')=? AND p.project_id=? AND sa.project_id=p.project_id)`,
		j.targetID, j.leaseUntil, j.attempts, j.videoID, j.videoID, j.fileID, j.sourceProject, j.connectionID, j.channel, j.project).Scan(&ok)
	return ok, err
}

func (j youtubeThumbnailJob) retry(ctx *sdk.AppCtx, reason string) error {
	limit := youtubeThumbnailProcessingPolls
	if j.state == "verifying" {
		limit = youtubeThumbnailVerificationPolls
	}
	if j.attempts >= limit {
		return j.finish(ctx, "failed", j.receipt, reason)
	}
	return j.finish(ctx, j.state, j.receipt, reason)
}

func (j youtubeThumbnailJob) finish(ctx *sdk.AppCtx, state, receipt, reason string) error {
	attempts := j.attempts
	if state != j.state {
		attempts = 0
	}
	_, err := ctx.AppDB().Exec(`UPDATE post_targets
		SET youtube_thumbnail_state=?,youtube_thumbnail_receipt=?,youtube_thumbnail_error=?,
			youtube_thumbnail_attempts=?,youtube_thumbnail_due_at=unixepoch()+?
		WHERE id=? AND youtube_thumbnail_state=? AND youtube_thumbnail_video_id=? AND youtube_thumbnail_file_id=?
		AND youtube_thumbnail_project_id=? AND youtube_thumbnail_attempts=? AND youtube_thumbnail_due_at=?`,
		state, receipt, redactDeliverySecrets(reason), attempts, int64(time.Minute/time.Second),
		j.targetID, j.state, j.videoID, j.fileID, j.sourceProject, j.attempts, j.leaseUntil)
	return err
}

type youtubeThumbnailVideo struct {
	ID         string `json:"id"`
	Processing struct {
		Status string `json:"processingStatus"`
	} `json:"processingDetails"`
	Content struct {
		Custom *bool `json:"hasCustomThumbnail"`
	} `json:"contentDetails"`
	Snippet struct {
		ChannelID  string         `json:"channelId"`
		Thumbnails map[string]any `json:"thumbnails"`
	} `json:"snippet"`
}

func readYouTubeThumbnailVideo(ctx *sdk.AppCtx, job youtubeThumbnailJob) (youtubeThumbnailVideo, error) {
	var video youtubeThumbnailVideo
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(job.connectionID, "get_video", map[string]any{
		"id": job.videoID, "part": "processingDetails,snippet,contentDetails", "_apteva": map[string]any{"timeout_ms": 8000},
	})
	if err != nil {
		return video, err
	}
	if res == nil || !res.Success {
		return video, upstreamError(res)
	}
	var payload struct {
		Items []youtubeThumbnailVideo `json:"items"`
	}
	if err := json.Unmarshal(res.Data, &payload); err != nil || len(payload.Items) != 1 {
		return video, errors.New("YouTube did not return the saved video for thumbnail verification")
	}
	return payload.Items[0], nil
}

func youtubeThumbnailURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return nil
	}
	if !strings.HasSuffix(u.Hostname(), ".ytimg.com") && u.Hostname() != "img.youtube.com" {
		return nil
	}
	return u
}

func youtubeThumbnailURLs(thumbnails map[string]any) map[string]string {
	result := map[string]string{}
	for name, item := range thumbnails {
		rendition, _ := item.(map[string]any)
		raw := historyString(rendition, "url")
		if youtubeThumbnailURL(raw) != nil {
			result[name] = raw
		}
	}
	return result
}

// The set response and the same video's owner readback must agree. A custom
// flag alone could describe an older thumbnail.
func youtubeThumbnailMatches(video youtubeThumbnailVideo, expected map[string]string) bool {
	actual := youtubeThumbnailURLs(video.Snippet.Thumbnails)
	if video.Content.Custom != nil && !*video.Content.Custom {
		return false
	}
	if short := expected["shortsAspectRatio"]; short != "" {
		return sameYouTubeThumbnail(video.ID, short, actual["shortsAspectRatio"])
	}
	if video.Content.Custom == nil && actual["shortsAspectRatio"] == "" {
		return false
	}
	for name, want := range expected {
		if want != "" && actual[name] == want {
			return true
		}
	}
	for _, name := range []string{"maxres", "standard", "high", "medium", "default"} {
		if sameYouTubeThumbnail(video.ID, expected[name], actual[name]) {
			return true
		}
	}
	return false
}

// YouTube can return the same rendition with different CDN signing or
// transformation parameters. Compare the served images when only those
// parameters differ. The stable path by itself can also serve older art.
func sameYouTubeThumbnail(videoID, expected, actual string) bool {
	if expected == "" || actual == "" {
		return false
	}
	if expected == actual {
		return true
	}
	want, got := youtubeThumbnailURL(expected), youtubeThumbnailURL(actual)
	if want == nil || got == nil || want.Host != got.Host || want.EscapedPath() != got.EscapedPath() {
		return false
	}
	path := got.EscapedPath()
	if !strings.HasPrefix(path, "/vi/"+url.PathEscape(videoID)+"/") &&
		!strings.HasPrefix(path, "/vi_webp/"+url.PathEscape(videoID)+"/") {
		return false
	}
	wantQuery, err := url.ParseQuery(want.RawQuery)
	if err != nil {
		return false
	}
	gotQuery, err := url.ParseQuery(got.RawQuery)
	if err != nil {
		return false
	}
	for _, param := range []string{"sqp", "rs"} {
		wantQuery.Del(param)
		gotQuery.Del(param)
	}
	if wantQuery.Encode() != gotQuery.Encode() {
		return false
	}
	wantImage, ok := youtubeThumbnailImageHash(expected)
	if !ok {
		return false
	}
	gotImage, ok := youtubeThumbnailImageHash(actual)
	return ok && wantImage == gotImage
}

var youtubeThumbnailCDNClient = &http.Client{
	Timeout: 5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func youtubeThumbnailImageHash(raw string) ([sha256.Size]byte, bool) {
	const maxRenditionBytes = 4 << 20
	var empty [sha256.Size]byte
	res, err := youtubeThumbnailCDNClient.Get(raw)
	if err != nil {
		return empty, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.ContentLength > maxRenditionBytes {
		return empty, false
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxRenditionBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxRenditionBytes {
		return empty, false
	}
	switch http.DetectContentType(body) {
	case "image/jpeg", "image/png", "image/webp":
		return sha256.Sum256(body), true
	default:
		return empty, false
	}
}
