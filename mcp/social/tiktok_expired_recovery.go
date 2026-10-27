package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// TikTok documents one hour from issuance. Use the later receipt time and a
// five-minute margin; expiry is only one prerequisite, never a retry decision.
const tikTokUploadRecoveryAge = 65 * time.Minute
const tikTokRecoveryObservationAge = 2 * time.Minute

type uploadStopEvidence struct {
	AcknowledgedBytes *int64           `json:"acknowledged_upload_bytes"`
	LastUpload        *deliveryOutcome `json:"last_upload"`
}

type uploadOperationReceipt struct {
	AccountBinding    string             `json:"account_binding"`
	ContentBinding    string             `json:"content_binding"`
	SourceSHA256      string             `json:"source_sha256"`
	CorrelationID     string             `json:"correlation_id"`
	SourceAttempt     int                `json:"source_attempt"`
	InitializedAt     string             `json:"initialized_at"`
	UploaderState     string             `json:"uploader_state"`
	UploaderStoppedAt string             `json:"uploader_stopped_at"`
	Provenance        string             `json:"provenance"`
	StopEvidence      uploadStopEvidence `json:"stop_evidence"`
}

// Persist active BEFORE sending bytes. Only the exiting uploader may persist
// stopped. A crash leaves active/unknown and cannot be cleared merely by age.
func guardedTikTokUpload(ctx *sdk.AppCtx, target int64, p *preparedVideo, d *deliveryDiagnostics, uploadURL string, chunkSize int64, chunks int) error {
	if target <= 0 {
		return uploadTikTokChunks(ctx, target, p, d, uploadURL, chunkSize, chunks)
	}
	if !d.bound || d.Initialization == nil || d.Initialization.Result != "operation_persisted" {
		return errors.New("upload receipt is not bound to a current attempt")
	}
	r, err := loadRecoveryTarget(ctx, postForTarget(ctx, target), target)
	if err != nil || r.DeliveryRevision != r.Revision {
		return errors.New("source attempt does not match the approved revision")
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO delivery_upload_operations(project_id,target_id,operation_id,correlation_id,source_attempt,initialized_at,uploader_state,provenance,account_binding,content_binding,source_sha256) SELECT ?,id,?,?,attempts,?,'active','social_upload_receipt',?,?,? FROM post_targets WHERE id=? AND attempts=? AND publish_operation_id=? AND status='publishing'`, projectScope(ctx), d.OperationID, d.CorrelationID, d.Initialization.At, uploadRecoveryAccountBinding(projectScope(ctx), r), uploadRecoveryContentBinding(r), p.SHA256, target, d.boundAttempt, d.OperationID)
	if err != nil {
		return errors.New("could not persist uploader guard; upload not started")
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("stale upload attempt; upload not started")
	}
	defer func() {
		// Retain the in-memory response too: a diagnostics write failure must
		// never hide a successful acknowledgement from the recovery decision.
		proof, _ := json.Marshal(uploadStopEvidence{AcknowledgedBytes: d.AcknowledgedBytes, LastUpload: d.LastUpload})
		_, _ = ctx.AppDB().Exec(`UPDATE delivery_upload_operations SET uploader_state='stopped',stop_evidence=?,uploader_stopped_at=? WHERE project_id=? AND target_id=? AND operation_id=? AND correlation_id=? AND uploader_state='active'`, string(proof), time.Now().UTC().Format(time.RFC3339Nano), projectScope(ctx), target, d.OperationID, d.CorrelationID)
	}()
	return uploadTikTokChunks(ctx, target, p, d, uploadURL, chunkSize, chunks)
}

func loadUploadReceipt(ctx *sdk.AppCtx, r recoveryTarget) (uploadOperationReceipt, error) {
	var receipt uploadOperationReceipt
	var stopRaw string
	err := ctx.AppDB().QueryRow(`SELECT correlation_id,source_attempt,initialized_at,uploader_state,COALESCE(uploader_stopped_at,''),provenance,stop_evidence,account_binding,content_binding,source_sha256 FROM delivery_upload_operations WHERE project_id=? AND target_id=? AND operation_id=?`, projectScope(ctx), r.TargetID, r.Operation).Scan(&receipt.CorrelationID, &receipt.SourceAttempt, &receipt.InitializedAt, &receipt.UploaderState, &receipt.UploaderStoppedAt, &receipt.Provenance, &stopRaw, &receipt.AccountBinding, &receipt.ContentBinding, &receipt.SourceSHA256)
	if err == nil {
		err = json.Unmarshal([]byte(stopRaw), &receipt.StopEvidence)
	}
	return receipt, err
}

// Explicit review can adopt an application-owned historical receipt, never a
// timestamp, zero counter or uploader assertion supplied in callback/tool args.
// The old code returns immediately on a persisted HTTP rejection. Require its
// full versioned, first-chunk 404 receipt plus initialization/URL binding. All
// other legacy evidence stays unknown and requires external investigation.
func reviewStoredUploadReceipt(ctx *sdk.AppCtx, r recoveryTarget) error {
	rows, err := ctx.AppDB().Query(`SELECT diagnostics FROM video_delivery_attempts WHERE project_id=? AND target_id=? AND json_valid(diagnostics) AND json_extract(diagnostics,'$.operation_id')=? AND COALESCE(json_extract(diagnostics,'$.purpose'),'')='' ORDER BY rowid LIMIT 200`, projectScope(ctx), r.TargetID, r.Operation)
	if err != nil {
		return err
	}
	var candidate *deliveryDiagnostics
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		var d deliveryDiagnostics
		if json.Unmarshal([]byte(raw), &d) != nil || d.AttemptPurpose != "" || d.OperationID != r.Operation || d.SourceSHA256 == "" || r.DeliveryRevision != r.Revision || d.ProgressVersion < 1 || d.SourceAttempt != nil && *d.SourceAttempt != r.Attempts || d.Method != "FILE_UPLOAD" || d.CorrelationID == "" || d.AcknowledgedBytes == nil || *d.AcknowledgedBytes != 0 || d.Initialization == nil || d.Initialization.Result != "operation_persisted" || d.Initialization.HTTPStatus < 200 || d.Initialization.HTTPStatus >= 300 || d.LastUpload == nil {
			continue
		}
		last := d.LastUpload
		initAt, ie := time.Parse(time.RFC3339Nano, d.Initialization.At)
		startAt, se := time.Parse(time.RFC3339Nano, last.StartedAt)
		endAt, ee := time.Parse(time.RFC3339Nano, last.At)
		if ie != nil || se != nil || ee != nil || startAt.Before(initAt) || endAt.Before(startAt) || endAt.After(time.Now()) || last.HTTPStatus != 404 || last.Result != "http_rejected" || last.Chunk != 1 || d.UploadURLSHA256 == "" || last.URLSHA256 != d.UploadURLSHA256 || d.UploadAcknowledgementResolution != nil || d.TransferredBytes != 0 {
			continue
		}
		copy := d
		candidate = &copy
		break
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if candidate == nil {
		return errors.New("legacy evidence review required: no complete application-owned initialization and stopped-uploader receipt; unknown bytes remain unknown")
	}
	proof, _ := json.Marshal(uploadStopEvidence{AcknowledgedBytes: candidate.AcknowledgedBytes, LastUpload: candidate.LastUpload})
	_, err = ctx.AppDB().Exec(`INSERT INTO delivery_upload_operations(project_id,target_id,operation_id,correlation_id,source_attempt,initialized_at,uploader_state,uploader_stopped_at,provenance,stop_evidence,account_binding,content_binding,source_sha256) SELECT ?,id,?,?,attempts,?,'stopped',?,'reviewed_stored_receipt',?,?,?,? FROM post_targets WHERE id=? AND attempts=? AND publish_operation_id=? AND COALESCE(platform_post_id,'')='' ON CONFLICT(project_id,target_id,operation_id) DO UPDATE SET account_binding=excluded.account_binding,content_binding=excluded.content_binding,source_sha256=excluded.source_sha256,provenance=excluded.provenance WHERE delivery_upload_operations.account_binding='' AND delivery_upload_operations.uploader_state='stopped' AND delivery_upload_operations.correlation_id=excluded.correlation_id AND delivery_upload_operations.source_attempt=excluded.source_attempt`, projectScope(ctx), r.Operation, candidate.CorrelationID, candidate.Initialization.At, candidate.LastUpload.At, string(proof), uploadRecoveryAccountBinding(projectScope(ctx), r), uploadRecoveryContentBinding(r), candidate.SourceSHA256, r.TargetID, r.Attempts, r.Operation)
	return err
}

func zeroTikTokObservation(d deliveryDiagnostics, now, floor time.Time) bool {
	o := d.ProviderObservation
	if o == nil || !o.QuerySucceeded || o.HTTPStatus < 200 || o.HTTPStatus >= 300 || o.Status != "PROCESSING_UPLOAD" || o.UploadedBytes == nil || *o.UploadedBytes != 0 || o.DownloadedBytes != nil && *o.DownloadedBytes != 0 {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, o.At)
	return err == nil && !at.Before(floor) && !at.After(now) && now.Sub(at) <= tikTokRecoveryObservationAge
}

// Called only for an explicit expiry review, while the caller holds the post
// lock. It performs a second fresh read and retains both observations for audit.
func (a *App) expiredTikTokRecovery(ctx *sdk.AppCtx, r recoveryTarget) (map[string]any, bool) {
	evidence := map[string]any{"eligible": false, "reason": "expired upload evidence is incomplete"}
	receipt, err := loadUploadReceipt(ctx, r)
	if errors.Is(err, sql.ErrNoRows) || err == nil && receipt.AccountBinding == "" {
		if err = reviewStoredUploadReceipt(ctx, r); err == nil {
			receipt, err = loadUploadReceipt(ctx, r)
		}
	}
	if err != nil {
		evidence["reason"] = redactDeliverySecrets(err.Error())
		return evidence, false
	}
	evidence["receipt"] = receipt
	if r.Session != "" || receipt.AccountBinding != uploadRecoveryAccountBinding(projectScope(ctx), r) || receipt.ContentBinding != uploadRecoveryContentBinding(r) || receipt.SourceSHA256 == "" {
		evidence["reason"] = "operation account, approved content, source checksum or resumable-sender evidence is missing or changed"
		return evidence, false
	}
	stop := receipt.StopEvidence
	if stop.AcknowledgedBytes == nil || *stop.AcknowledgedBytes != 0 || stop.LastUpload == nil || stop.LastUpload.Result != "http_rejected" || stop.LastUpload.HTTPStatus < 400 || stop.LastUpload.HTTPStatus >= 500 {
		evidence["reason"] = "uploader did not retain an explicit zero-acknowledgement rejection; ambiguous or accepted uploads cannot be replaced"
		return evidence, false
	}
	now := time.Now().UTC()
	initAt, ie := time.Parse(time.RFC3339Nano, receipt.InitializedAt)
	stoppedAt, se := time.Parse(time.RFC3339Nano, receipt.UploaderStoppedAt)
	if receipt.SourceAttempt != r.Attempts || receipt.UploaderState != "stopped" || ie != nil || se != nil || stoppedAt.Before(initAt) || stoppedAt.After(now) || now.Sub(initAt) < tikTokUploadRecoveryAge {
		evidence["reason"] = "operation is not expired with a proven stopped uploader for this attempt"
		return evidence, false
	}
	floor := initAt.Add(tikTokUploadRecoveryAge)
	if stoppedAt.After(floor) {
		floor = stoppedAt
	}
	d := loadDeliveryDiagnostics(ctx, r.TargetID)
	if !d.bound || d.boundOperation != r.Operation || d.boundAttempt != r.Attempts || d.Method != "FILE_UPLOAD" || d.ProgressVersion < 1 || d.AcknowledgedBytes == nil || *d.AcknowledgedBytes != 0 || d.TransferredBytes != 0 || d.ProviderUploadedHighWater != nil && *d.ProviderUploadedHighWater != 0 || d.ProviderDownloadedHighWater != nil && *d.ProviderDownloadedHighWater != 0 || d.UploadAcknowledgementResolution != nil || !zeroTikTokObservation(d, now, floor) {
		evidence["reason"] = "explicit zero progress is not established; missing, positive, stale or conflicting bytes block recovery"
		return evidence, false
	}
	first := *d.ProviderObservation
	status, _, ids, err := a.getTikTokPublishStatus(ctx, r.ConnectionID, r.Operation)
	d = loadDeliveryDiagnostics(ctx, r.TargetID)
	if err != nil || status != "PROCESSING_UPLOAD" || len(ids) != 0 || !d.bound || d.boundAttempt != r.Attempts || d.boundOperation != r.Operation || !zeroTikTokObservation(d, time.Now().UTC(), floor) || d.ProviderObservation.At == first.At || d.ProviderUploadedHighWater != nil && *d.ProviderUploadedHighWater != 0 || d.ProviderDownloadedHighWater != nil && *d.ProviderDownloadedHighWater != 0 {
		evidence["reason"] = "second fresh status check did not confirm zero bytes and no publication identity"
		return evidence, false
	}
	evidence["eligible"], evidence["reason"] = true, "expired FILE_UPLOAD with stopped uploader, no acknowledged or received bytes, and no publication identity"
	evidence["observations"] = []tikTokProgressObservation{first, *d.ProviderObservation}
	evidence["provider_status"] = "PROCESSING_UPLOAD"
	evidence["local_disposition"] = "locally_abandoned_on_authorization"
	return evidence, true
}

func currentTikTokAttempt(ctx *sdk.AppCtx, target int64, attempt int, operation string) bool {
	var n int
	err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE id=? AND attempts=? AND COALESCE(publish_operation_id,'')=? AND EXISTS(SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=?)`, target, attempt, operation, projectScope(ctx)).Scan(&n)
	return err == nil && n == 1
}

func persistTikTokUploadOperation(ctx *sdk.AppCtx, target int64, d *deliveryDiagnostics, operation string) error {
	if target <= 0 {
		return nil
	}
	if !d.bound {
		return errors.New("missing upload attempt binding")
	}
	res, err := ctx.AppDB().Exec(`UPDATE post_targets SET publish_operation_id=? WHERE id=? AND attempts=? AND COALESCE(publish_operation_id,'')=? AND status='publishing' AND EXISTS(SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=?)`, operation, target, d.boundAttempt, d.boundOperation, projectScope(ctx))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("stale initialization result; operation not attached")
	}
	return nil
}

func postForTarget(ctx *sdk.AppCtx, target int64) int64 {
	var post int64
	_ = ctx.AppDB().QueryRow(`SELECT post_id FROM post_targets WHERE id=?`, target).Scan(&post)
	return post
}
func uploadRecoveryAccountBinding(project string, r recoveryTarget) string {
	raw, _ := json.Marshal([]any{project, r.AccountID, r.ConnectionID, r.Platform, r.Provider, r.ExternalID})
	return imageSHA(raw)
}
func uploadRecoveryContentBinding(r recoveryTarget) string {
	raw, _ := json.Marshal([]any{r.PostID, r.TargetID, r.Revision, r.DeliveryRevision, r.Body, r.Options, r.MediaIDs, r.MediaProject})
	return imageSHA(raw)
}
