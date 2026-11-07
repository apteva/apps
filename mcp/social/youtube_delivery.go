package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var youtubeVideoUploadClient = &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

func youtubeSessionKey(session string) string { return "youtube-session:" + imageSHA([]byte(session)) }
func videoUploadFingerprint(p *preparedVideo, conn int64, input map[string]any) string {
	d := sha256.New()
	_, _ = io.Copy(d, io.NewSectionReader(p.File, 0, p.Size))
	meta, _ := json.Marshal(input)
	fmt.Fprintf(d, "\n%d\n%s", conn, meta)
	return hex.EncodeToString(d.Sum(nil))
}
func (a *App) uploadPreparedYouTube(ctx *sdk.AppCtx, def platformDef, j publishJob, p *preparedVideo, input map[string]any) (string, error) {
	d := p.Diagnostics
	d.Method = "FILE_UPLOAD"
	var session, fingerprint, retryAt string
	if j.targetID > 0 {
		if err := ctx.AppDB().QueryRow(`SELECT COALESCE(youtube_upload_session,''),COALESCE(youtube_upload_fingerprint,''),COALESCE(youtube_upload_retry_after,'') FROM post_targets WHERE id=?`, j.targetID).Scan(&session, &fingerprint, &retryAt); err != nil {
			return "", err
		}
	}
	if until, err := time.Parse(time.RFC3339Nano, retryAt); err == nil && time.Now().Before(until) {
		return "", uploadRetry("YouTube asked to retry after " + retryAt)
	}
	actual := videoUploadFingerprint(p, j.connID, input)
	if session != "" && (fingerprint != actual || !validYouTubeSession(session)) {
		return "", errors.New("stored upload does not match this video; verify the remote outcome before retrying")
	}
	if session == "" {
		d.OperationState = "unknown"
		if err := validatePreparedDestination(ctx, j); err != nil {
			return "", err
		}
		if err := d.event(ctx, j.targetID, deliveryEvent{Stage: "initialization", Result: "request_started"}); err != nil {
			return "", errors.New("could not persist upload initialization guard")
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, def.PostTool, input)
		if err != nil {
			return "", fmt.Errorf("upload initialization outcome unknown; verify the remote outcome: %s", redactDeliverySecrets(err.Error()))
		}
		if res == nil || !res.Success {
			captureProviderDiagnostics(&d, res)
			if res != nil && res.Status >= 400 && res.Status < 500 {
				d.OperationState = "not_started"
			}
			d.event(ctx, j.targetID, deliveryEvent{Stage: "initialization", HTTPStatus: executeStatus(res), Result: "provider_rejected"})
			return "", upstreamError(res)
		}
		session = integrationHeader(res, "Location")
		if !validYouTubeSession(session) {
			return "", errors.New("upload_video_init: no Location header or invalid Google session; verify the remote outcome before retrying")
		}
		d.OperationID = youtubeSessionKey(session)
		d.OperationState = "pending"
		if j.targetID > 0 {
			_, err = ctx.AppDB().Exec(`UPDATE post_targets SET youtube_upload_session=?,youtube_upload_fingerprint=?,publish_operation_id=? WHERE id=?`, session, actual, d.OperationID, j.targetID)
			if err != nil {
				return "", &publishedWarningError{pending: true, operationID: d.OperationID, warning: "Upload session was created but persistence failed; verify the remote outcome."}
			}
		}
		d.event(ctx, j.targetID, deliveryEvent{Stage: "initialization", Result: "session_persisted"})
	} else {
		d.OperationID = youtubeSessionKey(session)
		d.OperationState = "pending"
	}
	// Probe a retained session before sending another byte. Never create a
	// replacement merely because the status request failed or expired.
	offset := int64(0)
	if fingerprint != "" {
		id, next, err := probeYouTubeSession(session, p.Size)
		d.event(ctx, j.targetID, deliveryEvent{Stage: "polling", Result: "session_probed"})
		if err != nil {
			d.Reconciliation = "unknown"
			_ = d.save(ctx, j.targetID)
			return "", uploadRetry("Existing YouTube session could not be reconciled; retry checks the same session")
		}
		if id != "" {
			d.OperationState = "published"
			d.ProviderStatus = "complete"
			_ = d.save(ctx, j.targetID)
			return id, nil
		}
		offset = next
	}
	const chunkSize int64 = 4 << 20
	for offset < p.Size {
		n := min(chunkSize, p.Size-offset)
		req, err := http.NewRequest(http.MethodPut, session, io.NewSectionReader(p.File, offset, n))
		if err != nil {
			return "", err
		}
		req.ContentLength = n
		req.Header.Set("Content-Type", p.MIME)
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+n-1, p.Size))
		d.event(ctx, j.targetID, deliveryEvent{Stage: "upload", Bytes: offset, Result: "chunk_started"})
		resp, err := youtubeVideoUploadClient.Do(req)
		if err != nil {
			return "", uploadRetry("Upload interrupted; existing session retained for probing")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		d.TransferredBytes = offset + n
		d.event(ctx, j.targetID, deliveryEvent{Stage: "upload", HTTPStatus: resp.StatusCode, Bytes: offset + n, RequestID: responseRequestID(resp.Header)})
		if readErr != nil {
			return "", uploadRetry("Upload response interrupted; existing session retained for probing")
		}
		if resp.StatusCode == 308 {
			next, err := youtubeResumeOffset(&sdk.ExecuteResult{Headers: map[string]string{"Range": resp.Header.Get("Range")}}, p.Size)
			if err != nil || next <= offset || next > offset+n {
				return "", uploadRetry("Upload progress uncertain; probe the retained session before retrying")
			}
			offset = next
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", a.youtubeSessionFailure(ctx, j.targetID, &sdk.ExecuteResult{Status: resp.StatusCode, Headers: map[string]string{"Retry-After": resp.Header.Get("Retry-After")}})
		}
		var resource struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(body, &resource)
		if resource.ID == "" {
			return "", uploadRetry("Upload completion has no video ID; probe retained session")
		}
		d.OperationState = "published"
		d.ProviderStatus = "complete"
		d.event(ctx, j.targetID, deliveryEvent{Stage: "processing", Result: "published"})
		return resource.ID, nil
	}
	return "", uploadRetry("All bytes sent; probe retained upload session for completion")
}
func probeYouTubeSession(session string, total int64) (string, int64, error) {
	if !validYouTubeSession(session) {
		return "", 0, errors.New("invalid stored upload session origin")
	}
	req, err := http.NewRequest(http.MethodPut, session, http.NoBody)
	if err != nil {
		return "", 0, errors.New("invalid stored upload session")
	}
	req.ContentLength = 0
	req.Header.Set("Content-Length", "0")
	req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", total))
	resp, err := youtubeVideoUploadClient.Do(req)
	if err != nil {
		return "", 0, errors.New("upload session status unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 308 {
		next, err := youtubeResumeOffset(&sdk.ExecuteResult{Headers: map[string]string{"Range": resp.Header.Get("Range")}}, total)
		return "", next, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var resource struct {
			ID string `json:"id"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&resource)
		if err == nil && resource.ID != "" {
			return resource.ID, total, nil
		}
	}
	return "", 0, errors.New("existing upload session outcome is unknown")
}
