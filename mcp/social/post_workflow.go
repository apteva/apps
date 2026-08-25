package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	postModeDraft    = "draft"
	postModeSchedule = "schedule"
	postModePublish  = "publish"
)

func explicitPostMode(args map[string]any) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(toString(args["mode"])))
	switch mode {
	case postModeDraft, postModeSchedule, postModePublish:
		return mode, nil
	case "":
		return "", errors.New("mode required: choose draft, schedule, or publish; no post was created")
	default:
		return "", fmt.Errorf("invalid mode %q: choose draft, schedule, or publish", mode)
	}
}

func workflowActor(args map[string]any) string {
	if actor := strings.TrimSpace(toString(args["_caller"])); actor != "" {
		return actor
	}
	if actor := strings.TrimSpace(toString(args["actor"])); actor != "" {
		return actor
	}
	return "system"
}

func parsePostTargets(args map[string]any, allowEmpty bool) ([]targetSpec, bool, error) {
	rawAccts, hasAccts := args["social_account_ids"].([]any)
	rawTargets, hasTargets := args["targets"].([]any)
	if hasAccts && hasTargets && len(rawAccts) > 0 && len(rawTargets) > 0 {
		return nil, false, errors.New("pass either social_account_ids[] OR targets[], not both")
	}
	present := hasAccts || hasTargets
	var targets []targetSpec
	switch {
	case len(rawTargets) > 0:
		for i, raw := range rawTargets {
			item, ok := raw.(map[string]any)
			if !ok {
				return nil, present, fmt.Errorf("targets[%d] must be an object {social_account_id, …}", i)
			}
			id := toInt64Loose(item["social_account_id"])
			if id <= 0 {
				return nil, present, fmt.Errorf("targets[%d].social_account_id required", i)
			}
			opts := make(map[string]any, len(item))
			for key, value := range item {
				if key != "social_account_id" {
					opts[key] = value
				}
			}
			targets = append(targets, targetSpec{SocialAccountID: id, Options: opts})
		}
	case len(rawAccts) > 0:
		for _, raw := range rawAccts {
			if id := toInt64Loose(raw); id > 0 {
				targets = append(targets, targetSpec{SocialAccountID: id})
			}
		}
	}
	if len(targets) == 0 && !allowEmpty {
		return nil, present, errors.New("social_account_ids or targets required (at least one)")
	}
	return targets, present, nil
}

func validatePostBodyForDelivery(body string, targets []targetSpec) error {
	if strings.TrimSpace(body) != "" {
		return nil
	}
	if len(targets) == 0 {
		return errors.New("body required")
	}
	for i, target := range targets {
		targetBody, _ := target.Options["body"].(string)
		if strings.TrimSpace(targetBody) == "" {
			return fmt.Errorf("body required: pass top-level body or targets[%d].body", i)
		}
	}
	return nil
}

func insertDraftTargets(tx *sql.Tx, postID int64, targets []targetSpec) error {
	for _, target := range targets {
		var optionsJSON sql.NullString
		if len(target.Options) > 0 {
			encoded, err := json.Marshal(target.Options)
			if err != nil {
				return err
			}
			optionsJSON = sql.NullString{String: string(encoded), Valid: true}
		}
		if _, err := tx.Exec(
			`INSERT INTO post_targets (post_id, social_account_id, status, options)
			 VALUES (?, ?, 'draft', ?)`,
			postID, target.SocialAccountID, optionsJSON,
		); err != nil {
			return fmt.Errorf("create draft target for account %d: %w", target.SocialAccountID, err)
		}
	}
	return nil
}

func postWorkflowSnapshotTx(tx *sql.Tx, postID int64) (string, error) {
	var body, mediaJSON, mediaProjectID, scheduleAt, status, approvalStatus, requestedMode string
	var revision, approvedRevision, approvalRequired int64
	if err := tx.QueryRow(
		`SELECT body, COALESCE(media_storage_ids,'[]'), COALESCE(media_project_id,''),
		        COALESCE(schedule_at,''), status, revision, approval_status,
		        approved_revision, approval_required, requested_mode
		   FROM posts WHERE id=?`, postID,
	).Scan(&body, &mediaJSON, &mediaProjectID, &scheduleAt, &status, &revision,
		&approvalStatus, &approvedRevision, &approvalRequired, &requestedMode); err != nil {
		return "", err
	}
	targetRows, err := tx.Query(
		`SELECT social_account_id, COALESCE(options,'')
		   FROM post_targets WHERE post_id=? ORDER BY id`, postID,
	)
	if err != nil {
		return "", err
	}
	targets := []map[string]any{}
	for targetRows.Next() {
		var accountID int64
		var optionsRaw string
		if targetRows.Scan(&accountID, &optionsRaw) != nil {
			continue
		}
		item := map[string]any{"social_account_id": accountID}
		if optionsRaw != "" {
			var options map[string]any
			if json.Unmarshal([]byte(optionsRaw), &options) == nil {
				for key, value := range options {
					item[key] = value
				}
			}
		}
		targets = append(targets, item)
	}
	targetRows.Close()
	var mediaIDs []int64
	_ = json.Unmarshal([]byte(mediaJSON), &mediaIDs)
	snapshot, err := json.Marshal(map[string]any{
		"body":              body,
		"media_storage_ids": mediaIDs,
		"media_project_id":  mediaProjectID,
		"schedule_at":       scheduleAt,
		"status":            status,
		"revision":          revision,
		"approval_status":   approvalStatus,
		"approved_revision": approvedRevision,
		"approval_required": approvalRequired != 0,
		"requested_mode":    requestedMode,
		"targets":           targets,
	})
	return string(snapshot), err
}

func recordPostRevisionTx(tx *sql.Tx, postID, revision int64, actor string) error {
	snapshot, err := postWorkflowSnapshotTx(tx, postID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO post_revisions (post_id, revision, actor, snapshot)
		 VALUES (?, ?, ?, ?)`, postID, revision, actor, snapshot,
	)
	return err
}

func recordPostReviewTx(tx *sql.Tx, postID, revision int64, action, actor, reason string) error {
	_, err := tx.Exec(
		`INSERT INTO post_reviews (post_id, revision, action, actor, reason)
		 VALUES (?, ?, ?, ?, ?)`, postID, revision, action, actor, reason,
	)
	return err
}

func (a *App) toolPostDraftCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	copyArgs := make(map[string]any, len(args)+1)
	for key, value := range args {
		copyArgs[key] = value
	}
	copyArgs["mode"] = postModeDraft
	return a.toolPostCreate(ctx, copyArgs)
}

func (a *App) toolPostDraftUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	postID := int64(intArg(args, "post_id", 0))
	expectedRevision := int64(intArg(args, "expected_revision", 0))
	if postID <= 0 || expectedRevision <= 0 {
		return mcpError("post_id and expected_revision are required"), nil
	}
	pid := projectScope(ctx, args)
	var currentStatus, body, mediaJSON, mediaProjectID string
	var revision int64
	if err := ctx.AppDB().QueryRow(
		`SELECT status, body, COALESCE(media_storage_ids,'[]'), COALESCE(media_project_id,''), revision
		   FROM posts WHERE id=? AND project_id=?`, postID, pid,
	).Scan(&currentStatus, &body, &mediaJSON, &mediaProjectID, &revision); err != nil {
		if err == sql.ErrNoRows {
			return mcpError("post not found"), nil
		}
		return nil, err
	}
	if currentStatus != "draft" && currentStatus != "rejected" && currentStatus != "approved" {
		return mcpError("only draft, rejected, or approved posts can be updated"), nil
	}
	if revision != expectedRevision {
		return mcpError(fmt.Sprintf("revision conflict: current revision is %d", revision)), nil
	}
	if next, ok := args["body"].(string); ok {
		body = next
	}
	if raw, present := args["media_storage_ids"]; present {
		values, ok := raw.([]any)
		if !ok {
			return mcpError("media_storage_ids must be an array"), nil
		}
		ids := []int64{}
		for _, value := range values {
			if id := toInt64Loose(value); id > 0 {
				ids = append(ids, id)
			}
		}
		encoded, _ := json.Marshal(ids)
		mediaJSON = string(encoded)
		mediaProjectID = strings.TrimSpace(stringArgAny(args, "media_project_id", "storage_project_id", "_project_id", "project_id"))
	}
	targets, targetsPresent, err := parsePostTargets(args, true)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if targetsPresent && len(targets) > 0 {
		if _, err := validatePostTargets(ctx, pid, targets); err != nil {
			return mcpError(err.Error()), nil
		}
		a.validateTargetOptions(ctx, pid, targets)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`UPDATE posts
		    SET body=?, media_storage_ids=?, media_project_id=?, revision=revision+1,
		        status='draft', approval_status='not_requested', approved_revision=0,
		        rejection_reason='', requested_mode='draft', updated_at=CURRENT_TIMESTAMP
		  WHERE id=? AND project_id=? AND revision=?`,
		body, mediaJSON, mediaProjectID, postID, pid, expectedRevision,
	)
	if err != nil {
		return nil, err
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return mcpError("revision conflict"), nil
	}
	if targetsPresent {
		if _, err := tx.Exec(`DELETE FROM post_targets WHERE post_id=?`, postID); err != nil {
			return nil, err
		}
		if err := insertDraftTargets(tx, postID, targets); err != nil {
			return nil, err
		}
	}
	newRevision := expectedRevision + 1
	if err := recordPostRevisionTx(tx, postID, newRevision, workflowActor(args)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("post.draft_updated", map[string]any{"post_id": postID, "revision": newRevision})
	providerSync := a.syncProviderWorkflow(ctx, postID, postModeDraft, "")
	post, err := a.loadPostByID(ctx, pid, postID)
	if post != nil && len(providerSync) > 0 {
		post["provider_sync"] = providerSync
	}
	return post, err
}

func (a *App) transitionDraftReview(ctx *sdk.AppCtx, args map[string]any, action string) (any, error) {
	postID := int64(intArg(args, "post_id", 0))
	expectedRevision := int64(intArg(args, "expected_revision", 0))
	if postID <= 0 || expectedRevision <= 0 {
		return mcpError("post_id and expected_revision are required"), nil
	}
	reason := strings.TrimSpace(toString(args["reason"]))
	if action == "reject" && reason == "" {
		return mcpError("reason required when rejecting a draft"), nil
	}
	pid := projectScope(ctx, args)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	var revision int64
	if err := tx.QueryRow(`SELECT status, revision FROM posts WHERE id=? AND project_id=?`, postID, pid).Scan(&status, &revision); err != nil {
		if err == sql.ErrNoRows {
			return mcpError("post not found"), nil
		}
		return nil, err
	}
	if revision != expectedRevision {
		return mcpError(fmt.Sprintf("revision conflict: current revision is %d", revision)), nil
	}
	actor := workflowActor(args)
	switch action {
	case "submit":
		if status != "draft" && status != "rejected" {
			return mcpError("only draft or rejected posts can be submitted"), nil
		}
		if _, err := tx.Exec(
			`UPDATE posts SET status='in_review', approval_status='pending', approval_required=1,
			        rejection_reason='', updated_at=CURRENT_TIMESTAMP WHERE id=?`, postID,
		); err != nil {
			return nil, err
		}
	case "approve":
		if status != "in_review" {
			return mcpError("only posts in review can be approved"), nil
		}
		if _, err := tx.Exec(
			`UPDATE posts SET status='approved', approval_status='approved',
			        approved_revision=revision, rejection_reason='', updated_at=CURRENT_TIMESTAMP
			  WHERE id=?`, postID,
		); err != nil {
			return nil, err
		}
	case "reject":
		if status != "in_review" {
			return mcpError("only posts in review can be rejected"), nil
		}
		if _, err := tx.Exec(
			`UPDATE posts SET status='rejected', approval_status='rejected',
			        approved_revision=0, rejection_reason=?, updated_at=CURRENT_TIMESTAMP
			  WHERE id=?`, reason, postID,
		); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown review action %q", action)
	}
	if err := recordPostReviewTx(tx, postID, revision, action, actor, reason); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	eventTopic := map[string]string{
		"submit":  "post.draft_submitted",
		"approve": "post.draft_approved",
		"reject":  "post.draft_rejected",
	}[action]
	ctx.Emit(eventTopic, map[string]any{"post_id": postID, "revision": revision, "actor": actor})
	return a.loadPostByID(ctx, pid, postID)
}

func (a *App) toolPostDraftSubmit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.transitionDraftReview(ctx, args, "submit")
}

func (a *App) toolPostDraftApprove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.transitionDraftReview(ctx, args, "approve")
}

func (a *App) toolPostDraftReject(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.transitionDraftReview(ctx, args, "reject")
}

func (a *App) toolPostDraftPublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	postID := int64(intArg(args, "post_id", 0))
	expectedRevision := int64(intArg(args, "expected_revision", 0))
	mode := strings.ToLower(strings.TrimSpace(toString(args["mode"])))
	if postID <= 0 || expectedRevision <= 0 {
		return mcpError("post_id and expected_revision are required"), nil
	}
	if mode != postModePublish && mode != postModeSchedule {
		return mcpError("mode required: choose publish or schedule"), nil
	}
	scheduleAt := strings.TrimSpace(toString(args["schedule_at"]))
	if mode == postModeSchedule {
		if scheduleAt == "" {
			return mcpError("schedule_at required when mode=schedule"), nil
		}
		canonical, err := normaliseScheduleAt(scheduleAt)
		if err != nil {
			return mcpError("invalid schedule_at: " + err.Error()), nil
		}
		scheduleAt = canonical
	} else if scheduleAt != "" {
		return mcpError("schedule_at is only valid when mode=schedule"), nil
	}
	pid := projectScope(ctx, args)
	var status, body string
	var revision, approvedRevision, approvalRequired, targetCount int64
	if err := ctx.AppDB().QueryRow(
		`SELECT status, body, revision, approved_revision, approval_required,
		        (SELECT COUNT(*) FROM post_targets WHERE post_id=posts.id)
		   FROM posts WHERE id=? AND project_id=?`, postID, pid,
	).Scan(&status, &body, &revision, &approvedRevision, &approvalRequired, &targetCount); err != nil {
		if err == sql.ErrNoRows {
			return mcpError("post not found"), nil
		}
		return nil, err
	}
	if revision != expectedRevision {
		return mcpError(fmt.Sprintf("revision conflict: current revision is %d", revision)), nil
	}
	if status != "draft" && status != "approved" {
		return mcpError("only draft or approved posts can be published with post_draft_publish"), nil
	}
	if approvalRequired != 0 && (status != "approved" || approvedRevision != revision) {
		return mcpError("this post requires approval for its current revision"), nil
	}
	if targetCount == 0 {
		return mcpError("at least one social target is required before publishing"), nil
	}
	if err := validateStoredDraftForDelivery(ctx, postID, body); err != nil {
		return mcpError(err.Error()), nil
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nextStatus := "publishing"
	if mode == postModeSchedule {
		nextStatus = "scheduled"
	}
	if _, err := tx.Exec(
		`UPDATE posts SET status=?, requested_mode=?, schedule_at=?, job_id=0,
		        updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND revision=?`,
		nextStatus, mode, nullable(scheduleAt), postID, pid, revision,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE post_targets SET status='pending', attempts=0, last_error=NULL,
		        failure_code='', retryable=1, upstream_status=0, existing_post_id=''
		  WHERE post_id=?`, postID,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if mode == postModePublish {
		a.publishPostTargets(ctx, postID)
	} else if ctx.IntegrationFor("jobs") == nil {
		ctx.Logger().Info("draft scheduled for worker fallback", "post", postID, "run_at", scheduleAt)
	} else if jobID, err := a.scheduleJob(ctx, postID, scheduleAt); err != nil {
		ctx.Logger().Warn("schedule approved draft via jobs failed; using worker fallback", "post", postID, "err", err)
	} else {
		_, _ = ctx.AppDB().Exec(`UPDATE posts SET job_id=? WHERE id=? AND project_id=?`, jobID, postID, pid)
	}
	ctx.Emit("post.publish_requested", map[string]any{"post_id": postID, "revision": revision, "mode": mode})
	return a.loadPostByID(ctx, pid, postID)
}

func validateStoredDraftForDelivery(ctx *sdk.AppCtx, postID int64, body string) error {
	rows, err := ctx.AppDB().Query(`SELECT COALESCE(options,'') FROM post_targets WHERE post_id=?`, postID)
	if err != nil {
		return err
	}
	defer rows.Close()
	targets := []targetSpec{}
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		options := map[string]any{}
		if raw != "" {
			_ = json.Unmarshal([]byte(raw), &options)
		}
		targets = append(targets, targetSpec{Options: options})
	}
	return validatePostBodyForDelivery(body, targets)
}
