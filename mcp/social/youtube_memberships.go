package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type awaitingAudienceError struct{ Warning string }

func (e *awaitingAudienceError) Error() string {
	return "Uploaded as unlisted. Complete members-only audience and tier settings on the platform, then confirm in Social." + e.Warning
}

type youtubeUploadRetryError struct{ Message string }

func (e *youtubeUploadRetryError) Error() string { return e.Message }
func uploadRetry(message string) error           { return &youtubeUploadRetryError{Message: message} }
func integrationHeader(res *sdk.ExecuteResult, name string) string {
	if res != nil {
		for key, value := range res.Headers {
			if strings.EqualFold(key, name) {
				return value
			}
		}
	}
	return ""
}
func youtubeResumeOffset(res *sdk.ExecuteResult, total int64) (int64, error) {
	raw := integrationHeader(res, "Range")
	if raw == "" {
		return 0, nil
	}
	if !strings.HasPrefix(raw, "bytes=0-") {
		return 0, errors.New("invalid YouTube upload Range header")
	}
	last, err := strconv.ParseInt(strings.TrimPrefix(raw, "bytes=0-"), 10, 64)
	if err != nil || last < 0 || last >= total {
		return 0, errors.New("invalid YouTube upload Range offset")
	}
	return last + 1, nil
}
func validYouTubeSession(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "www.googleapis.com" && u.User == nil && u.Fragment == "" && strings.HasPrefix(u.Path, "/upload/youtube/v3/")
}
func (a *App) publishYouTubeMembership(ctx *sdk.AppCtx, def platformDef, j publishJob, r audienceRequest) (string, string, error) {
	if len(j.media) != 1 || !j.media[0].IsVideo() {
		return "", "", errors.New("YouTube membership publishing requires one video file")
	}
	var accountID int64
	if err := ctx.AppDB().QueryRow(`SELECT t.social_account_id FROM post_targets t JOIN posts p ON p.id=t.post_id WHERE t.id=? AND p.project_id=?`, j.targetID, projectScope(ctx)).Scan(&accountID); err != nil {
		return "", "", err
	}
	account, err := loadAudienceAccount(ctx, projectScope(ctx), accountID)
	if err != nil || account.Status != "active" || account.ConnectionID != j.connID {
		return "", "", errors.New("membership account is not active on this connection")
	}
	// Never obtain OAuth credentials in the sidecar. The integration runner
	// authenticates all session PUTs using this account's existing connection.
	conn, err := ctx.PlatformAPI().GetConnection(j.connID)
	if err != nil {
		return "", "", err
	}
	if conn == nil || conn.ID != j.connID || conn.Status != "active" || conn.ProjectID != projectScope(ctx) || conn.AppSlug != def.IntegrationSlug {
		return "", "", errors.New("YouTube connection is not active in this project")
	}
	var id, session, fingerprint, retryAt string
	err = ctx.AppDB().QueryRow(`SELECT COALESCE(platform_post_id,''),COALESCE(youtube_upload_session,''),COALESCE(youtube_upload_fingerprint,''),COALESCE(youtube_upload_retry_after,'') FROM post_targets WHERE id=?`, j.targetID).Scan(&id, &session, &fingerprint, &retryAt)
	if err != nil {
		return "", "", err
	}
	if id != "" {
		return id, "https://www.youtube.com/watch?v=" + url.QueryEscape(id), &awaitingAudienceError{}
	}
	if retryAt != "" {
		if until, e := time.Parse(time.RFC3339Nano, retryAt); e == nil && time.Now().Before(until) {
			return "", "", uploadRetry("YouTube asked to retry after " + retryAt)
		}
	}
	if err := validateAudienceTarget(ctx, projectScope(ctx), targetSpec{SocialAccountID: accountID, Options: j.options}); err != nil {
		// Selecting tiers explicitly requests validation when delivery runs,
		// including a scheduled delivery after the discovery cache expires.
		if len(r.LevelIDs) == 0 {
			return "", "", err
		}
		if _, refreshErr := a.toolMembershipLevels(ctx, map[string]any{"social_account_id": accountID, "refresh": true}); refreshErr != nil {
			return "", "", refreshErr
		}
		if err := validateAudienceTarget(ctx, projectScope(ctx), targetSpec{SocialAccountID: accountID, Options: j.options}); err != nil {
			return "", "", err
		}
	}
	identity, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, "get_my_channel", map[string]any{"part": "id"})
	if err != nil {
		return "", "", err
	}
	if identity == nil || !identity.Success {
		return "", "", upstreamError(identity)
	}
	var channel struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if json.Unmarshal(identity.Data, &channel) != nil || len(channel.Items) != 1 || channel.Items[0].ID == "" || (account.ExternalID != "" && account.ExternalID != channel.Items[0].ID) {
		return "", "", errors.New("membership connection does not match the selected channel")
	}
	thumbnail, err := a.resolveThumbnailOption(ctx, j.options, j.mediaProjectID)
	if err != nil {
		return "", "", err
	}
	// Share scoped source preparation and bounded replayable spooling with
	// ordinary uploads, while retaining this path's existing resumable protocol.
	prepared, err := a.prepareVideoSource(ctx, j, j.media[0], "FILE_UPLOAD")
	if err != nil {
		return "", "", err
	}
	defer prepared.Close()
	file, total := prepared.File, prepared.Size
	digest := sha256.New()
	if _, err = io.Copy(digest, io.NewSectionReader(file, 0, total)); err != nil {
		return "", "", err
	}
	title := strOption(j.options, "title")
	if title == "" {
		title = firstChars(strings.TrimSpace(j.body), 80)
	}
	if title == "" {
		title = "Untitled"
	}
	snippet := map[string]any{"title": title, "description": j.body}
	category := strOption(j.options, "category")
	if category == "" {
		category = "22"
	}
	snippet["categoryId"] = category
	if tags := anySliceOption(j.options, "tags"); len(tags) > 0 {
		snippet["tags"] = tags
	}
	mime := j.media[0].Mime
	if mime == "" {
		mime = "video/mp4"
	}
	input := map[string]any{"snippet": snippet, "status": map[string]any{"privacyStatus": "unlisted", "selfDeclaredMadeForKids": false}, "upload_content_type": mime, "upload_content_length": total}
	metadata, _ := json.Marshal(input)
	fmt.Fprintf(digest, "\n%d\n%s", j.connID, metadata)
	actualFingerprint := hex.EncodeToString(digest.Sum(nil))
	if session != "" && (fingerprint != actualFingerprint || !validYouTubeSession(session)) {
		return "", "", errors.New("stored upload does not match this video or connection; verify the remote outcome before creating another post")
	}
	d := prepared.Diagnostics
	offset := int64(0)
	if session == "" {
		d.OperationState = "unknown"
		if err := validatePreparedDestination(ctx, j); err != nil {
			return "", "", err
		}
		if err := d.event(ctx, j.targetID, deliveryEvent{Stage: "initialization", Result: "request_started"}); err != nil {
			return "", "", errors.New("could not persist upload initialization guard")
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, def.PostTool, input)
		if err != nil {
			return "", "", err
		}
		if res == nil || !res.Success {
			captureProviderDiagnostics(&d, res)
			if res != nil && res.Status >= 400 && res.Status < 500 {
				d.OperationState = "not_started"
			}
			d.event(ctx, j.targetID, deliveryEvent{Stage: "initialization", HTTPStatus: executeStatus(res), Result: "provider_rejected"})
			return "", "", upstreamError(res)
		}
		session = integrationHeader(res, "Location")
		if !validYouTubeSession(session) {
			return "", "", errors.New("upload_video_init: missing or invalid Google Location header; verify the remote outcome before retrying")
		}
		if _, err := ctx.AppDB().Exec(`UPDATE post_targets SET youtube_upload_session=?,youtube_upload_fingerprint=?,publish_operation_id=? WHERE id=?`, session, actualFingerprint, youtubeSessionKey(session), j.targetID); err != nil {
			return "", "", errors.New("session persistence failed; verify the remote outcome before retrying")
		}
		d.OperationID = youtubeSessionKey(session)
		d.OperationState = "pending"
		d.event(ctx, j.targetID, deliveryEvent{Stage: "initialization", Result: "session_persisted"})
	} else {
		d.OperationID = youtubeSessionKey(session)
		d.OperationState = "pending"
		d.event(ctx, j.targetID, deliveryEvent{Stage: "polling", Result: "session_probe"})
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, "upload_video_status", map[string]any{"upload_url": session, "content_range": fmt.Sprintf("bytes */%d", total)})
		if err != nil {
			return "", "", uploadRetry("Check the existing upload session after updating the YouTube catalog/server: " + redactDeliverySecrets(err.Error()))
		}
		if res != nil && res.Success {
			id = uploadedVideoID(res)
		} else if res != nil && res.Status == 308 {
			offset, err = youtubeResumeOffset(res, total)
			if err != nil {
				return "", "", err
			}
		} else {
			return "", "", a.youtubeSessionFailure(ctx, j.targetID, res)
		}
		if id == "" && res != nil && res.Success {
			return "", "", uploadRetry("Upload completion response did not contain a video ID; check this same session again")
		}
	}
	const chunkSize int64 = 4 << 20 // Multiple of YouTube's required 256 KiB granularity.
	for id == "" && offset < total {
		size := min(chunkSize, total-offset)
		chunk := make([]byte, int(size))
		if _, err := file.ReadAt(chunk, offset); err != nil {
			return "", "", err
		}
		d.event(ctx, j.targetID, deliveryEvent{Stage: "upload", Bytes: offset, Result: "chunk_started"})
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, "upload_video_chunk", map[string]any{
			"upload_url": session, "content_range": fmt.Sprintf("bytes %d-%d/%d", offset, offset+size-1, total),
			"video": map[string]any{"_binary": true, "base64": base64.StdEncoding.EncodeToString(chunk), "mimeType": mime},
		})
		if err != nil {
			return "", "", uploadRetry("Upload interrupted; retry will check the same session: " + redactDeliverySecrets(err.Error()))
		}
		d.TransferredBytes = offset + size
		d.event(ctx, j.targetID, deliveryEvent{Stage: "upload", HTTPStatus: executeStatus(res), Bytes: offset + size})
		if res != nil && res.Success {
			id = uploadedVideoID(res)
			if id == "" {
				return "", "", uploadRetry("Upload completed without a video ID; retry will check the same session")
			}
			break
		}
		if res != nil && res.Status == 308 {
			if integrationHeader(res, "Range") == "" {
				return "", "", uploadRetry("Upload progress is unavailable. The server must forward YouTube's Range header; retry will check the same session")
			}
			next, err := youtubeResumeOffset(res, total)
			if err != nil {
				return "", "", err
			}
			if next <= offset || next > offset+size {
				return "", "", uploadRetry("YouTube upload progress was inconsistent; check the same session again")
			}
			offset = next
			continue
		}
		return "", "", a.youtubeSessionFailure(ctx, j.targetID, res)
	}
	if id == "" {
		return "", "", uploadRetry("All video bytes were received but completion is uncertain; check the same session again")
	}
	d.OperationState = "published"
	d.ProviderStatus = "complete"
	d.event(ctx, j.targetID, deliveryEvent{Stage: "processing", Result: "awaiting_audience_setup"})
	watch := "https://www.youtube.com/watch?v=" + url.QueryEscape(id)
	// Save remote identity before any thumbnail call or final status write.
	if _, err := ctx.AppDB().Exec(`UPDATE post_targets SET platform_post_id=?,platform_url=?,audience_status='awaiting_setup',youtube_upload_retry_after=NULL WHERE id=?`, id, watch, j.targetID); err != nil {
		return "", "", err
	}
	warning := ""
	if thumbnail != nil {
		if err := a.setBinaryThumbnail(ctx, def, j.connID, id, *thumbnail); err != nil {
			warning = " Custom thumbnail failed; edit the thumbnail without uploading another video."
		}
	}
	return id, watch, &awaitingAudienceError{Warning: warning}
}
func uploadedVideoID(res *sdk.ExecuteResult) string {
	var resource struct {
		ID string `json:"id"`
	}
	if res == nil || json.Unmarshal(res.Data, &resource) != nil {
		return ""
	}
	return resource.ID
}
func (a *App) youtubeSessionFailure(ctx *sdk.AppCtx, targetID int64, res *sdk.ExecuteResult) error {
	if res != nil && (res.Status == 404 || res.Status == 410) {
		return errors.New("upload session expired or was lost; verify the remote video before creating a replacement")
	}
	if raw := integrationHeader(res, "Retry-After"); raw != "" {
		until, err := http.ParseTime(raw)
		if seconds, e := strconv.ParseInt(raw, 10, 32); e == nil && seconds >= 0 {
			until = time.Now().Add(time.Duration(seconds) * time.Second)
			err = nil
		}
		if err == nil {
			if _, err := ctx.AppDB().Exec(`UPDATE post_targets SET youtube_upload_retry_after=? WHERE id=?`, until.UTC().Format(time.RFC3339Nano), targetID); err != nil {
				return err
			}
		}
	}
	if res == nil || res.Status == 0 || res.Status == 429 || res.Status >= 500 {
		return uploadRetry("YouTube upload interrupted; retry will check the same session")
	}
	return upstreamError(res)
}
