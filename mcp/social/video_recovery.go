package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Recovery is separate from ordinary transient retry: one destination, a fresh
// decision bound to its exact revision/source/attempt, and one consumed grant.
type recoveryTarget struct {
	PostID, TargetID, AccountID, ConnectionID                                      int64
	Revision, Attempts                                                             int
	ApprovalRequired                                                               int
	ApprovedRevision                                                               int
	Status, AccountStatus, Platform, Provider, Operation, PublicID                 string
	Body, Options, MediaIDs, MediaProject, Failure, LastError, Session, ExternalID string
}

func loadRecoveryTarget(ctx *sdk.AppCtx, post, target int64) (r recoveryTarget, err error) {
	r.PostID, r.TargetID = post, target
	err = ctx.AppDB().QueryRow(`SELECT t.social_account_id,a.connection_id,p.revision,t.attempts,p.approval_required,COALESCE(p.approved_revision,0),t.status,a.status,a.platform,COALESCE(a.provider_slug,'native'),COALESCE(t.publish_operation_id,''),COALESCE(t.platform_post_id,''),p.body,COALESCE(t.options,'{}'),COALESCE(p.media_storage_ids,'[]'),COALESCE(p.media_project_id,''),t.failure_code,COALESCE(t.last_error,''),COALESCE(t.youtube_upload_session,''),COALESCE(a.external_account_id,'') FROM post_targets t JOIN posts p ON p.id=t.post_id JOIN social_accounts a ON a.id=t.social_account_id WHERE p.id=? AND t.id=? AND p.project_id=? AND a.project_id=p.project_id`, post, target, projectScope(ctx)).Scan(&r.AccountID, &r.ConnectionID, &r.Revision, &r.Attempts, &r.ApprovalRequired, &r.ApprovedRevision, &r.Status, &r.AccountStatus, &r.Platform, &r.Provider, &r.Operation, &r.PublicID, &r.Body, &r.Options, &r.MediaIDs, &r.MediaProject, &r.Failure, &r.LastError, &r.Session, &r.ExternalID)
	return
}
func (r recoveryTarget) fingerprint(source string) string {
	raw, _ := json.Marshal(r)
	return imageSHA(append(raw, []byte("\n"+source)...))
}
func recoveryIDs(args map[string]any) (int64, int64, error) {
	p, t := int64(intArg(args, "post_id", 0)), int64(intArg(args, "target_id", 0))
	if p <= 0 || t <= 0 {
		return 0, 0, errors.New("post_id and target_id required")
	}
	return p, t, nil
}
func (a *App) toolPostDeliveryInspect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	post, target, err := recoveryIDs(args)
	if err != nil {
		return nil, err
	}
	unlock := lockSocialPost(ctx, post)
	defer unlock()
	r, err := loadRecoveryTarget(ctx, post, target)
	if err != nil {
		return mcpError("target not found in this project"), nil
	}
	evidence, _, err := a.deliveryRecoveryDecision(ctx, r, false)
	if err != nil {
		return nil, err
	}
	evidence["targets"] = a.loadTargets(ctx, post)
	evidence["delivery_diagnostics"] = loadDeliveryDiagnostics(ctx, target)
	snapshots, err := ctx.AppDB().Query(`SELECT correlation_id,diagnostics,created_at FROM video_delivery_attempts WHERE project_id=? AND target_id=? ORDER BY created_at DESC,rowid DESC LIMIT 50`, projectScope(ctx), target)
	if err != nil {
		return nil, err
	}
	durable := []map[string]any{}
	for snapshots.Next() {
		var key, raw, at string
		if err := snapshots.Scan(&key, &raw, &at); err != nil {
			snapshots.Close()
			return nil, err
		}
		durable = append(durable, map[string]any{"correlation_id": key, "created_at": at, "diagnostics": decodeDeliveryDiagnostics(raw, r.Platform, "")})
	}
	err = snapshots.Err()
	snapshots.Close()
	if err != nil {
		return nil, err
	}
	evidence["diagnostics_history"] = durable
	evidence["diagnostics_history_limit"] = 50
	// Deliberately no publish, retry, or lifecycle status update here.
	rows, err := ctx.AppDB().Query(`SELECT operation_id,status,reason,created_at FROM target_delivery_attempts WHERE target_id=? ORDER BY id`, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := []map[string]any{}
	for rows.Next() {
		var op, status, reason, at string
		if err = rows.Scan(&op, &status, &reason, &at); err != nil {
			return nil, err
		}
		history = append(history, map[string]any{"operation_id": safeRequestID(op), "status": status, "reason": redactDeliverySecrets(reason), "created_at": at})
	}
	evidence["attempt_history"] = history
	return evidence, rows.Err()
}

func (a *App) deliveryRecoveryDecision(ctx *sdk.AppCtx, r recoveryTarget, validateSource bool) (map[string]any, string, error) {
	e := map[string]any{"post_id": r.PostID, "target_id": r.TargetID, "decision": "blocked", "reason": "nonpublication has not been established", "revision": r.Revision, "source_attempt": r.Attempts}
	decision := "blocked"
	if r.PublicID != "" || r.Status == "published" || r.Status == "awaiting_audience_setup" {
		e["decision"] = "published"
		e["reason"] = "existing publication; never resubmit"
		return e, "", nil
	}
	if r.Provider != "native" || r.AccountStatus != "active" || r.ApprovalRequired != 0 && r.ApprovedRevision != r.Revision {
		e["reason"] = "account, provider, or revision approval is not eligible"
		return e, "", nil
	}
	if r.Failure == "duplicate_content" {
		e["reason"] = "duplicate content is not delivery recovery"
		return e, "", nil
	}
	if r.Operation != "" && r.Platform == "tiktok" {
		status, reason, ids, err := a.getTikTokPublishStatus(ctx, r.ConnectionID, r.Operation)
		e["provider_status"], e["fail_reason"], e["platform_post_ids"] = safeRequestID(status), safeRequestID(reason), ids
		if err != nil {
			e["reason"] = "existing operation could not be inspected"
			return e, "", nil
		}
		switch status {
		case "PUBLISH_COMPLETE":
			e["decision"] = "published"
			e["reason"] = "provider confirms completed publication"
			return e, "", nil
		case "FAILED":
			switch reason {
			case "internal", "video_pull_failed", "picture_size_check_failed":
				decision = "retry"
				e["reason"] = "provider confirms FAILED; source revalidation required (provider reason is not a root-cause diagnosis)"
			default:
				e["reason"] = "provider failure requires content, authorization, or configuration review"
				return e, "", nil
			}
		case "PROCESSING_UPLOAD", "PROCESSING_DOWNLOAD", "SEND_TO_USER_INBOX":
			e["decision"] = "pending"
			e["reason"] = "existing operation is processing; never resubmit"
			return e, "", nil
		default:
			e["reason"] = "provider returned an unknown operation state; never resubmit"
			return e, "", nil
		}
	} else if r.Platform == "youtube" && r.Session != "" {
		if !validYouTubeSession(r.Session) {
			e["reason"] = "stored YouTube session origin is invalid; do not send credentials or bytes"
			return e, "", nil
		}
		// An existing session may only be resumed. No replacement initialization.
		decision = "resume"
		e["reason"] = "retain and probe the existing YouTube session before sending bytes"
	} else if r.Operation == "" && r.Status == "failed" && r.Failure != "outcome_unknown" {
		d := loadDeliveryDiagnostics(ctx, r.TargetID)
		switch {
		case r.Failure == "source_delivery" && d.OperationState == "not_started":
			decision = "retry"
			e["reason"] = "source failed before provider initialization"
		case r.Platform == "facebook" && facebookFetchRejected(r.LastError):
			decision = "retry"
			e["reason"] = "Facebook explicitly rejected fetching the video (389/1363057)"
		}
	}
	if decision == "blocked" {
		return e, "", nil
	}
	if !validateSource {
		e["decision"] = "revalidation_required"
		return e, "", nil
	}
	conn, err := ctx.PlatformAPI().GetConnection(r.ConnectionID)
	def, ok := platforms[r.Platform]
	if err != nil || conn == nil || !ok || conn.ID != r.ConnectionID || conn.Status != "active" || conn.ProjectID != projectScope(ctx) || conn.AppSlug != def.IntegrationSlug {
		e["reason"] = "connection is not active for this project and integration"
		return e, "", nil
	}
	var ids []int64
	if json.Unmarshal([]byte(r.MediaIDs), &ids) != nil || len(ids) != 1 {
		e["reason"] = "recovery requires exactly one stored video"
		return e, "", nil
	}
	media, err := a.resolveMedia(ctx, ids, r.MediaProject)
	if err != nil || len(media) != 1 || !media[0].IsVideo() {
		e["reason"] = "stored video could not be resolved"
		return e, "", nil
	}
	var options map[string]any
	if json.Unmarshal([]byte(r.Options), &options) != nil {
		e["reason"] = "invalid stored target options"
		return e, "", nil
	}
	if err = validateAudienceTarget(ctx, projectScope(ctx), targetSpec{SocialAccountID: r.AccountID, Options: options}); err != nil {
		e["reason"] = "audience options require review"
		return e, "", nil
	}
	j := publishJob{targetID: r.TargetID, connID: r.ConnectionID, platform: r.Platform, body: r.Body, options: options, media: media, mediaProjectID: r.MediaProject}
	if r.Platform == "tiktok" {
		if _, err = tikTokPostInfo(ctx, j); err != nil {
			e["reason"] = "current TikTok settings reject the stored options"
			return e, "", nil
		}
	}
	p, err := a.prepareVideoSource(ctx, j, media[0], "RECOVERY_VALIDATION")
	if err != nil {
		e["reason"] = redactDeliverySecrets(err.Error())
		return e, "", nil
	}
	defer p.Close()
	e["source_sha256"], e["source_bytes"] = p.SHA256, p.Size
	e["storage_install_id"] = p.Diagnostics.StorageInstallID
	sourceProof := fmt.Sprintf("%s:%d:%s:%d", p.SHA256, p.Diagnostics.StorageInstallID, p.Diagnostics.MediaProjectID, p.Diagnostics.SourceFileID)
	if decision == "resume" {
		var id string
		var probeErr error
		audience, _ := parseAudience(options)
		if audience.Type == "members" {
			res, err := ctx.PlatformAPI().ExecuteIntegrationTool(r.ConnectionID, "upload_video_status", map[string]any{"upload_url": r.Session, "content_range": fmt.Sprintf("bytes */%d", p.Size)})
			probeErr = err
			if err == nil && res != nil && res.Success {
				id = uploadedVideoID(res)
				if id == "" {
					probeErr = errors.New("missing identity")
				}
			} else if err == nil && res != nil && res.Status == 308 {
				_, probeErr = youtubeResumeOffset(res, p.Size)
			} else if err == nil {
				probeErr = errors.New("unknown session")
			}
		} else {
			id, _, probeErr = probeYouTubeSession(r.Session, p.Size)
		}
		if probeErr != nil {
			e["reason"] = "existing YouTube session outcome is unknown; no replacement upload"
			return e, "", nil
		}
		if id != "" {
			e["decision"] = "published"
			if audience.Type == "members" {
				e["decision"] = "audience_setup"
			}
			e["platform_post_ids"] = []string{id}
			e["reason"] = "existing session confirms publication"
			return e, sourceProof, nil
		}
	}
	e["decision"] = decision
	return e, sourceProof, nil
}

func facebookFetchRejected(message string) bool {
	// Require the exact provider error/subcode, not a generic 400 or a message
	// fragment that could also appear in an ambiguous transport failure.
	start := strings.Index(message, "{")
	if start < 0 {
		return false
	}
	var body struct {
		Error struct {
			Code    int `json:"code"`
			Subcode int `json:"error_subcode"`
		} `json:"error"`
	}
	return json.Unmarshal([]byte(message[start:]), &body) == nil && body.Error.Code == 389 && body.Error.Subcode == 1363057
}
func (a *App) toolPostDeliveryRevalidate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	post, target, err := recoveryIDs(args)
	if err != nil {
		return nil, err
	}
	unlock := lockSocialPost(ctx, post)
	defer unlock()
	r, err := loadRecoveryTarget(ctx, post, target)
	if err != nil {
		return mcpError("target not found in this project"), nil
	}
	e, source, err := a.deliveryRecoveryDecision(ctx, r, true)
	if err != nil {
		return nil, err
	}
	id := randomDeliveryID()
	expires := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339Nano)
	evidence, _ := json.Marshal(e)
	_, err = ctx.AppDB().Exec(`INSERT INTO delivery_recovery_requests(id,project_id,post_id,target_id,revision,source_attempt,fingerprint,decision,evidence,actor,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, projectScope(ctx), post, target, r.Revision, r.Attempts, r.fingerprint(source), e["decision"], string(evidence), workflowActor(args), expires)
	if err != nil {
		return nil, err
	}
	e["recovery_id"], e["expires_at"] = id, expires
	return e, nil
}
func (a *App) toolPostDeliveryRecover(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	post, target, err := recoveryIDs(args)
	if err != nil {
		return nil, err
	}
	confirmed, _ := boolOption(args, "confirm")
	id := strOption(args, "recovery_id")
	if !confirmed || id == "" {
		return mcpError("confirm=true and recovery_id from post_delivery_revalidate required"), nil
	}
	unlock := lockSocialPost(ctx, post)
	defer unlock()
	var fingerprint, decision, status, expires, result string
	err = ctx.AppDB().QueryRow(`SELECT fingerprint,decision,status,expires_at,result FROM delivery_recovery_requests WHERE id=? AND project_id=? AND post_id=? AND target_id=?`, id, projectScope(ctx), post, target).Scan(&fingerprint, &decision, &status, &expires, &result)
	if err != nil {
		return mcpError("recovery request not found in this project/target"), nil
	}
	if status != "prepared" {
		var prior map[string]any
		_ = json.Unmarshal([]byte(result), &prior)
		return map[string]any{"recovery_id": id, "status": status, "idempotent": true, "result": prior}, nil
	}
	until, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || time.Now().After(until) {
		return mcpError("recovery decision expired; revalidate"), nil
	}
	if decision != "retry" && decision != "resume" && decision != "published" && decision != "pending" && decision != "audience_setup" {
		return mcpError("recovery decision does not authorize a retry or reconciliation"), nil
	}
	r, err := loadRecoveryTarget(ctx, post, target)
	if err != nil {
		return nil, err
	}
	e, source, err := a.deliveryRecoveryDecision(ctx, r, true)
	if err != nil {
		return nil, err
	}
	if e["decision"] != decision || r.fingerprint(source) != fingerprint {
		return mcpError("target/source/remote state changed; revalidate"), nil
	}
	if r.PublicID != "" || r.Status == "published" || r.Status == "awaiting_audience_setup" {
		return e, nil
	}
	if time.Now().After(until) {
		return mcpError("recovery decision expired during validation; revalidate"), nil
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	claimed, err := tx.Exec(`UPDATE delivery_recovery_requests SET status='consumed' WHERE id=? AND status='prepared' AND NOT EXISTS(SELECT 1 FROM delivery_recovery_requests other WHERE other.target_id=? AND other.revision=? AND other.status IN ('consumed','complete') AND other.decision IN ('retry','resume') AND delivery_recovery_requests.decision IN ('retry','resume'))`, id, target, r.Revision)
	if err != nil {
		return nil, err
	}
	n, _ := claimed.RowsAffected()
	if n != 1 {
		return mcpError("this revision already consumed its recovery allowance"), nil
	}
	if decision == "retry" || decision == "resume" {
		historyID := r.Operation
		if historyID == "" {
			historyID = fmt.Sprintf("attempt:%d:recovery:%s", r.Attempts, id)
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO target_delivery_attempts(target_id,operation_id,status,reason) VALUES(?,?,'failed',?)`, target, historyID, redactDeliverySecrets(r.LastError)); err != nil {
			return nil, err
		}
		operation := r.Operation
		if decision == "retry" {
			operation = ""
		}
		update, err := tx.Exec(`UPDATE post_targets SET status='pending',retryable=1,failure_code='',last_error=NULL,publish_operation_id=? WHERE id=? AND attempts=? AND status=? AND COALESCE(platform_post_id,'')=''`, operation, target, r.Attempts, r.Status)
		if err != nil {
			return nil, err
		}
		n, _ = update.RowsAffected()
		if n != 1 {
			return mcpError("target changed; no recovery executed"), nil
		}
	} else if decision == "pending" {
		if _, err = tx.Exec(`UPDATE post_targets SET status='publishing',retryable=0,identity_resolve_after=datetime('now','+5 minutes') WHERE id=?`, target); err != nil {
			return nil, err
		}
	} else if decision == "audience_setup" {
		ids, ok := e["platform_post_ids"].([]string)
		if !ok || len(ids) == 0 || ids[0] == "" {
			return mcpError("completed membership session has no identity; inspect it again"), nil
		}
		if _, err = tx.Exec(`UPDATE post_targets SET status='awaiting_audience_setup',platform_post_id=?,platform_url=?,audience_status='awaiting_setup',published_at=NULL,retryable=0,last_error=?,failure_code='' WHERE id=?`, ids[0], "https://www.youtube.com/watch?v="+ids[0], (&awaitingAudienceError{}).Error(), target); err != nil {
			return nil, err
		}
	} else if decision == "published" {
		publicID := ""
		if ids, ok := e["platform_post_ids"].([]string); ok && len(ids) > 0 {
			publicID = ids[0]
		}
		if _, err = tx.Exec(`UPDATE post_targets SET status='published',platform_post_id=?,published_at=COALESCE(published_at,CURRENT_TIMESTAMP),retryable=0,last_error=NULL,failure_code='' WHERE id=?`, nullable(publicID), target); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if decision == "retry" {
		d := loadDeliveryDiagnostics(ctx, target)
		d.OperationID = ""
		d.OperationState = "not_started"
		d.AutomaticPollAttempts = 0
		d.AutomaticPollingStopped = false
		d.NextPollAt = ""
		d.Reconciliation = "authorized_recovery"
		_ = d.save(ctx, target)
	}
	if decision == "retry" || decision == "resume" {
		a.publishSelectedPostTargets(ctx, post, target)
	} else {
		a.rollupPostStatus(ctx, post)
	}
	out := map[string]any{"recovery_id": id, "decision": decision, "target_id": target, "targets": a.loadTargets(ctx, post)}
	raw, _ := json.Marshal(out)
	_, err = ctx.AppDB().Exec(`UPDATE delivery_recovery_requests SET status='complete',result=? WHERE id=?`, string(raw), id)
	return out, err
}
