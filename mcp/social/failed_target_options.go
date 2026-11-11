package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// The parent revision belongs to the publication, including successful siblings.
// A separate options revision guards corrections to one failed destination.
type failedOptionsTarget struct {
	recoveryTarget
	OptionsRevision                                                 int
	PostStatus, ProviderPostID, PagePostID, ExistingID, Diagnostics string
	SubmittedAt, AcceptedAt                                         string
}

func loadFailedOptionsTarget(db recoveryQueryer, project string, post, target int64) (r failedOptionsTarget, err error) {
	r.recoveryTarget, err = loadRecoveryTargetFrom(db, project, post, target)
	if err == nil {
		err = db.QueryRow(`SELECT t.options_revision,p.status,COALESCE(t.provider_post_id,''),COALESCE(t.facebook_page_post_id,''),COALESCE(t.existing_post_id,''),t.delivery_diagnostics,COALESCE(t.submitted_at,''),COALESCE(t.provider_accepted_at,'') FROM post_targets t JOIN posts p ON p.id=t.post_id WHERE t.id=? AND p.id=? AND p.project_id=?`, target, post, project).Scan(&r.OptionsRevision, &r.PostStatus, &r.ProviderPostID, &r.PagePostID, &r.ExistingID, &r.Diagnostics, &r.SubmittedAt, &r.AcceptedAt)
	}
	return
}

func (a *App) toolPostTargetOptionsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	post, target, err := recoveryIDs(args)
	if err != nil {
		return nil, err
	}
	revision, optionsRevision := intArg(args, "expected_revision", 0), intArg(args, "expected_options_revision", 0)
	patch, ok := args["options"].(map[string]any)
	reason := strings.TrimSpace(toString(args["reason"]))
	if revision < 1 || optionsRevision < 1 || !ok || len(patch) == 0 || reason == "" {
		return mcpError("expected_revision, expected_options_revision, nonempty options patch, and reason required"), nil
	}
	unlock := lockSocialPost(ctx, post)
	defer unlock()
	pid := projectScope(ctx)
	r, err := loadFailedOptionsTarget(ctx.AppDB(), pid, post, target)
	if err != nil {
		return mcpError("target not found in this post/project"), nil
	}
	if r.Revision != revision || r.OptionsRevision != optionsRevision {
		return mcpError("revision conflict; inspect the current target"), nil
	}
	if err := validateDeliveryAccount(args, r.AccountID); err != nil {
		return mcpError(err.Error()), nil
	}
	if r.Status != "failed" || (r.PostStatus != "partial" && r.PostStatus != "failed") || r.AccountStatus != "active" {
		return mcpError("only a failed target on an active account in a failed or partially published post can be corrected"), nil
	}
	if r.ApprovalRequired != 0 {
		return mcpError("reviewed publications require fresh approval; failed-target corrections cannot bypass review"), nil
	}
	if r.PublicID != "" || r.ProviderPostID != "" || r.PagePostID != "" || r.ExistingID != "" || r.Failure == "duplicate_content" || (r.Failure == "outcome_unknown" && r.Operation == "") || r.Session != "" {
		return mcpError("resolve the existing publication or upload before changing options; no retry authorized"), nil
	}
	// This is a readback, never a new publish. Complete and pending operations
	// reconcile locally and return without altering their submitted options.
	hadOperation := r.Operation != ""
	terminalReason := ""
	if hadOperation {
		if r.Platform != "tiktok" || r.Provider != "native" {
			return mcpError("resolve the existing provider operation before changing options"), nil
		}
		remote, reason, ids, err := a.getTikTokPublishStatus(ctx, r.ConnectionID, r.Operation)
		if err != nil {
			return nil, fmt.Errorf("reconcile failed target: %w", err)
		}
		current, err := loadFailedOptionsTarget(ctx.AppDB(), pid, post, target)
		if err != nil {
			return nil, err
		}
		// Status readback records its own bound diagnostic observation.
		r.Diagnostics = current.Diagnostics
		if current != r {
			return mcpError("delivery changed during reconciliation; inspect the current target"), nil
		}
		switch remote {
		case "FAILED":
			// Keep the old operation until validation and audit commit together.
			// An invalid correction must not strand the failed operation.
			terminalReason = reason
		case "PUBLISH_COMPLETE":
			identity := ""
			if len(ids) > 0 {
				identity = ids[0]
			}
			_, err = ctx.AppDB().Exec(`UPDATE post_targets SET status='published',platform_post_id=?,delivery_recorded_at=CURRENT_TIMESTAMP,last_error=NULL,failure_code='',retryable=0 WHERE id=? AND attempts=? AND publish_operation_id=? AND options_revision=?`, nullable(identity), target, r.Attempts, r.Operation, optionsRevision)
			if err == nil {
				a.rollupPostStatus(ctx, post)
			}
			return map[string]any{"status": "published", "reconciled": true, "target_id": target, "platform_post_id": identity, "retried": 0}, err
		case "PROCESSING_UPLOAD", "PROCESSING_DOWNLOAD", "SEND_TO_USER_INBOX":
			_, err = ctx.AppDB().Exec(`UPDATE post_targets SET status='publishing',identity_resolve_after=datetime('now','+5 minutes') WHERE id=? AND attempts=? AND publish_operation_id=? AND options_revision=?`, target, r.Attempts, r.Operation, optionsRevision)
			if err == nil {
				a.rollupPostStatus(ctx, post)
			}
			return map[string]any{"status": "publishing", "target_id": target, "retried": 0, "message": "existing operation is processing; options were not changed"}, err
		default:
			return mcpError("existing operation has an unknown outcome; options were not changed"), nil
		}
	}
	if r.Revision != revision || r.OptionsRevision != optionsRevision {
		return mcpError("revision conflict after reconciliation; inspect the current target"), nil
	}
	// Do not turn source recovery or an undocumented legacy timeout into a
	// configuration retry. Only proven preflight failures or a reconciled FAILED
	// operation can be corrected here.
	d := loadDeliveryDiagnostics(ctx, target)
	if !hadOperation && (d.OperationState == "unknown" || d.OperationState == "pending" || d.OperationState == "published" || r.AcceptedAt != "" || (r.SubmittedAt != "" && d.OperationState != "not_started")) {
		return mcpError("a submitted operation has not been proven unsuccessful; reconcile it before changing options"), nil
	}
	if !hadOperation && d.OperationID != "" && d.OperationState != "not_started" && d.OperationState != "confirmed_failure" {
		return mcpError("delivery diagnostics contain an unresolved operation; reconcile it before changing options"), nil
	}
	if !hadOperation && r.Failure != "validation_error" && r.Failure != "options_corrected" && d.OperationState != "not_started" && !strings.Contains(r.LastError, "choose a valid TikTok privacy_level from the creator's current options") {
		return mcpError("nonpublication is unverified; inspect and reconcile delivery before correcting options"), nil
	}
	var unresolved int
	err = ctx.AppDB().QueryRow(unresolvedOptionsUploadsSQL, pid, target, r.Operation).Scan(&unresolved)
	if err != nil {
		return nil, err
	}
	if unresolved != 0 {
		return mcpError("an upload receipt remains unresolved; reconcile it before changing options"), nil
	}
	options := map[string]any{}
	optionsRaw := strings.TrimSpace(r.Options)
	if optionsRaw != "" && optionsRaw != "null" && json.Unmarshal([]byte(optionsRaw), &options) != nil {
		return mcpError("stored options are invalid"), nil
	}
	def, exists := platforms[r.Platform]
	if !exists {
		return mcpError("unsupported platform"), nil
	}
	fields := map[string]optionField{"body": {Name: "body", Type: "textarea"}, "thumbnail_project_id": {Name: "thumbnail_project_id", Type: "text"}}
	for _, field := range def.OptionFields {
		fields[field.Name] = field
	}
	for key, value := range patch {
		field, allowed := fields[key]
		if !allowed {
			return mcpError(fmt.Sprintf("option %q is not an editable publication option for %s; account, source, audience and destination cannot change", key, r.Platform)), nil
		}
		if value == nil {
			if key == "privacy_level" || key == "visibility" {
				return mcpError("privacy must be explicitly selected, not removed"), nil
			}
			delete(options, key)
			continue
		}
		if err := validateCorrectedOption(field, value); err != nil {
			return mcpError(err.Error()), nil
		}
		options[key] = value
	}
	for key, value := range options {
		if field, known := fields[key]; known {
			if err := validateCorrectedOption(field, value); err != nil {
				return mcpError(err.Error()), nil
			}
		}
	}
	if err := validateAudienceTarget(ctx, pid, targetSpec{SocialAccountID: r.AccountID, Options: options}); err != nil {
		return mcpError(err.Error()), nil
	}
	if err := validatePostBodyForDelivery(r.Body, []targetSpec{{SocialAccountID: r.AccountID, Options: options}}); err != nil {
		return mcpError(err.Error()), nil
	}
	if r.Platform == "tiktok" && r.Provider == "native" {
		if _, err := tikTokPostInfo(ctx, publishJob{connID: r.ConnectionID, options: options}); err != nil {
			return mcpError(err.Error()), nil
		}
	} else if r.Platform == "tiktok" {
		return mcpError("this provider cannot verify the creator's current TikTok privacy choices; use a direct TikTok connection"), nil
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	previous := map[string]any{}
	if optionsRaw != "" && optionsRaw != "null" {
		_ = json.Unmarshal([]byte(optionsRaw), &previous)
	}
	canonicalPrevious, _ := json.Marshal(previous)
	if bytes.Equal(canonicalPrevious, encoded) {
		return mcpError("options are unchanged; no correction recorded or retry authorized"), nil
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := loadFailedOptionsTarget(tx, pid, post, target)
	if err != nil {
		return nil, err
	}
	if current != r {
		return mcpError("delivery changed during validation; inspect the current target"), nil
	}
	if err = tx.QueryRow(unresolvedOptionsUploadsSQL, pid, target, r.Operation).Scan(&unresolved); err != nil {
		return nil, err
	}
	if unresolved != 0 {
		return mcpError("upload receipts changed during validation; inspect the current delivery"), nil
	}
	if hadOperation {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO target_delivery_attempts(target_id,operation_id,status,reason) VALUES(?,?,'failed',?)`, target, r.Operation, terminalReason); err != nil {
			return nil, err
		}
	}
	res, err := tx.Exec(`UPDATE post_targets SET options=?,options_revision=options_revision+1,retry_target_only=1,retryable=1,failure_code='options_corrected',publish_operation_id='',identity_resolve_after=NULL WHERE id=? AND status='failed' AND options_revision=?`, string(encoded), target, optionsRevision)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return mcpError("options revision conflict"), nil
	}
	previousFailure, _ := json.Marshal(map[string]any{"failure_code": r.Failure, "last_error": redactDeliverySecrets(r.LastError), "operation_id": safeRequestID(r.Operation), "provider_failure_reason": redactDeliverySecrets(terminalReason)})
	_, err = tx.Exec(`INSERT INTO target_options_corrections(target_id,post_revision,options_revision,source_attempt,previous_options,corrected_options,previous_failure,actor,reason) VALUES(?,?,?,?,?,?,?,?,?)`, target, revision, optionsRevision+1, r.Attempts, string(canonicalPrevious), string(encoded), string(previousFailure), workflowActor(args), reason)
	if err != nil {
		return nil, err
	}
	// Old recovery grants refer to the old options and must be prepared afresh.
	_, err = tx.Exec(`UPDATE delivery_recovery_requests SET status='invalidated' WHERE target_id=? AND status='prepared'`, target)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("target.options_corrected", map[string]any{"post_id": post, "target_id": target, "options_revision": optionsRevision + 1})
	return map[string]any{"status": "corrected", "post_id": post, "target_id": target, "revision": revision, "options_revision": optionsRevision + 1, "options": options, "published": false, "retry": map[string]any{"tool": "post_retry", "post_id": post, "target_id": target, "expected_options_revision": optionsRevision + 1}}, nil
}

const unresolvedOptionsUploadsSQL = `SELECT COUNT(*) FROM delivery_upload_operations o WHERE o.project_id=? AND o.target_id=? AND (o.uploader_state!='stopped' OR (o.abandoned_at IS NULL AND o.operation_id!=? AND NOT EXISTS (SELECT 1 FROM target_delivery_attempts a WHERE a.target_id=o.target_id AND a.operation_id=o.operation_id AND a.status='failed')))`

func validateCorrectedOption(field optionField, value any) error {
	valid := true
	switch field.Type {
	case "boolean":
		_, valid = value.(bool)
	case "number", "media":
		switch n := value.(type) {
		case float64:
			valid = n >= 0 && (field.Type != "media" || n > 0 && n == float64(int64(n)))
		case int:
			valid = n >= 0 && (field.Type != "media" || n > 0)
		case int64:
			valid = n >= 0 && (field.Type != "media" || n > 0)
		default:
			valid = false
		}
	case "tags":
		switch tags := value.(type) {
		case []string:
		case []any:
			for _, tag := range tags {
				if _, ok := tag.(string); !ok {
					valid = false
				}
			}
		default:
			valid = false
		}
	default:
		text, ok := value.(string)
		valid = ok
		if valid && len(field.Options) > 0 {
			valid = false
			for _, option := range field.Options {
				if text == option {
					valid = true
				}
			}
		}
	}
	if !valid {
		return fmt.Errorf("invalid %s for %s", field.Type, field.Name)
	}
	return nil
}

func loadTargetOptionsCorrections(ctx *sdk.AppCtx, target int64) ([]map[string]any, error) {
	rows, err := ctx.AppDB().Query(`SELECT post_revision,options_revision,source_attempt,previous_options,corrected_options,previous_failure,actor,reason,created_at FROM target_options_corrections WHERE target_id=? ORDER BY options_revision DESC LIMIT 50`, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var revision, optionsRevision, attempt int
		var previous, corrected, failure, actor, reason, at string
		if err := rows.Scan(&revision, &optionsRevision, &attempt, &previous, &corrected, &failure, &actor, &reason, &at); err != nil {
			return nil, err
		}
		var before, after, previousFailure map[string]any
		if err := json.Unmarshal([]byte(previous), &before); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(corrected), &after); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(failure), &previousFailure); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"post_revision": revision, "options_revision": optionsRevision, "source_attempt": attempt, "previous_options": before, "corrected_options": after, "previous_failure": previousFailure, "actor": actor, "reason": reason, "created_at": at})
	}
	return out, rows.Err()
}
