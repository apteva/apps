package main

import (
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Reconcile a selected target before creating another native operation. The
// caller holds the post lock; failed, published and in-flight siblings stay put.
func (a *App) reconcileSelectedRetry(ctx *sdk.AppCtx, postID, targetID int64) (any, bool, error) {
	var status, platform, provider, operation, postIdentity, failure, lastError string
	var connection int64
	var attempt int
	err := ctx.AppDB().QueryRow(`SELECT t.status,a.platform,COALESCE(a.provider_slug,'native'),a.connection_id,COALESCE(t.publish_operation_id,''),COALESCE(t.platform_post_id,''),t.failure_code,COALESCE(t.last_error,''),t.attempts FROM post_targets t JOIN posts p ON p.id=t.post_id JOIN social_accounts a ON a.id=t.social_account_id WHERE t.id=? AND p.id=? AND p.project_id=?`, targetID, postID, projectScope(ctx)).Scan(&status, &platform, &provider, &connection, &operation, &postIdentity, &failure, &lastError, &attempt)
	if err != nil {
		return mcpError("target not found in this post/project"), true, nil
	}
	if status == "published" || postIdentity != "" {
		return map[string]any{"retried": 0, "target_id": targetID, "status": status, "message": "destination already has a post identity; no new publication"}, true, nil
	}
	if failure == "outcome_unknown" || failure == "duplicate_content" {
		return mcpError("resolve the existing delivery before retrying"), true, nil
	}
	if operation != "" {
		if platform == "youtube" && provider == "native" && failure == "upload_incomplete" {
			var session string
			if err := ctx.AppDB().QueryRow(`SELECT COALESCE(youtube_upload_session,'') FROM post_targets WHERE id=?`, targetID).Scan(&session); err != nil {
				return nil, true, err
			}
			if validYouTubeSession(session) {
				return nil, false, nil
			} // Same session only; publisher probes before sending bytes.
		}
		if platform != "tiktok" || provider != "native" {
			return mcpError("resolve the existing provider operation before retrying"), true, nil
		}
		remote, reason, ids, err := a.getTikTokPublishStatus(ctx, connection, operation)
		if err != nil {
			return nil, true, fmt.Errorf("reconcile selected TikTok operation: %w", err)
		}
		if !currentTikTokAttempt(ctx, targetID, attempt, operation) {
			return mcpError("TikTok operation changed; inspect the current attempt"), true, nil
		}
		switch remote {
		case "PUBLISH_COMPLETE":
			identity := ""
			if len(ids) > 0 {
				identity = ids[0]
			}
			_, err = ctx.AppDB().Exec(`UPDATE post_targets SET status='published',platform_post_id=?,delivery_recorded_at=CURRENT_TIMESTAMP,last_error=NULL,failure_code='',upstream_status=0,retryable=0 WHERE id=? AND attempts=? AND publish_operation_id=?`, nullable(identity), targetID, attempt, operation)
			if err == nil {
				a.rollupPostStatus(ctx, postID)
			}
			return map[string]any{"retried": 0, "target_id": targetID, "status": "published", "reconciled": true, "platform_post_id": identity}, true, err
		case "FAILED":
			// A remote terminal failure authorizes replacing this operation, but
			// retain its ID and reason as attempt history before consuming the slot.
			tx, err := ctx.AppDB().Begin()
			if err != nil {
				return nil, true, err
			}
			defer tx.Rollback()
			if _, err = tx.Exec(`INSERT OR IGNORE INTO target_delivery_attempts(target_id,operation_id,status,reason) VALUES(?,?,'failed',?)`, targetID, operation, reason); err != nil {
				return nil, true, err
			}
			if _, err = tx.Exec(`UPDATE post_targets SET status='failed',publish_operation_id='',retryable=CASE WHEN ?='picture_size_check_failed' THEN 1 ELSE retryable END WHERE id=? AND attempts=? AND publish_operation_id=?`, reason, targetID, attempt, operation); err != nil {
				return nil, true, err
			}
			if err = tx.Commit(); err != nil {
				return nil, true, err
			}

		default:
			_, err = ctx.AppDB().Exec(`UPDATE post_targets SET status='publishing',identity_resolve_after=datetime('now','+5 minutes') WHERE id=? AND attempts=? AND publish_operation_id=?`, targetID, attempt, operation)
			if err == nil {
				a.rollupPostStatus(ctx, postID)
			}
			return map[string]any{"retried": 0, "target_id": targetID, "status": "publishing", "message": "existing TikTok operation is not terminal; no new publication"}, true, err
		}
	} else if platform == "tiktok" && provider == "native" && status == "failed" && strings.TrimSpace(lastError) == "TikTok publish failed: picture_size_check_failed" {
		// Older versions did not persist the operation ID, but this exact error
		// comes only from a remote FAILED result, never a transport timeout.
		_, err = ctx.AppDB().Exec(`UPDATE post_targets SET retryable=1 WHERE id=?`, targetID)
		if err != nil {
			return nil, true, err
		}
	}
	return nil, false, nil
}
