package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	providerImportModeOff         = "off"
	providerImportModeManaged     = "managed_only"
	providerImportModeFullHistory = "full_history"
)

func normalizeProviderImportMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		mode = providerImportModeManaged
	}
	switch mode {
	case providerImportModeOff, providerImportModeManaged, providerImportModeFullHistory:
		return mode, nil
	default:
		return "", fmt.Errorf("provider import mode must be off, managed_only, or full_history")
	}
}

// providerLifecycleCapabilities keeps the Social workflow provider-neutral.
// A future provider can opt into any subset without changing the draft tools.
type providerLifecycleCapabilities struct {
	NativeDrafts       bool `json:"native_drafts"`
	NativeScheduling   bool `json:"native_scheduling"`
	ImportAndReconcile bool `json:"import_and_reconcile"`
	MediaDrafts        bool `json:"media_drafts"`
}

type providerWorkflowRequest struct {
	Intent     string
	ScheduleAt string
	ProviderID string
	PublishJob publishJob
}

type providerWorkflowResult struct {
	ProviderPostID string
	Raw            json.RawMessage
}

type providerLifecycleAdapter interface {
	Capabilities() providerLifecycleCapabilities
	UpsertWorkflowPost(*sdk.AppCtx, providerWorkflowRequest) (providerWorkflowResult, error)
}

func (a *App) providerLifecycle(slug string) providerLifecycleAdapter {
	switch slug {
	case zernioProviderSlug:
		return zernioLifecycleAdapter{app: a}
	default:
		return nil
	}
}

type zernioLifecycleAdapter struct{ app *App }

func (z zernioLifecycleAdapter) Capabilities() providerLifecycleCapabilities {
	return providerLifecycleCapabilities{NativeDrafts: true, NativeScheduling: true, ImportAndReconcile: true, MediaDrafts: true}
}

func (z zernioLifecycleAdapter) UpsertWorkflowPost(ctx *sdk.AppCtx, request providerWorkflowRequest) (providerWorkflowResult, error) {
	j := request.PublishJob
	if j.providerAccountID == "" {
		return providerWorkflowResult{}, errors.New("zernio-backed account missing provider_account_id")
	}
	input, err := z.app.zernioWorkflowInput(ctx, j, request.Intent, request.ScheduleAt)
	if err != nil {
		return providerWorkflowResult{}, err
	}
	tool := "create_post"
	if request.ProviderID != "" {
		tool = "update_post"
		input["postId"] = request.ProviderID
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, tool, input)
	if err != nil {
		return providerWorkflowResult{}, err
	}
	if res == nil || !res.Success {
		return providerWorkflowResult{}, upstreamError(res)
	}
	providerID, _, _ := extractZernioPostIdentity(res.Data)
	if providerID == "" {
		providerID = request.ProviderID
	}
	if providerID == "" {
		return providerWorkflowResult{}, fmt.Errorf("zernio %s returned no provider post id", tool)
	}
	return providerWorkflowResult{ProviderPostID: providerID, Raw: sanitizeRawJSON(res.Data)}, nil
}

func (a *App) zernioWorkflowInput(ctx *sdk.AppCtx, j publishJob, intent, scheduleAt string) (map[string]any, error) {
	input := map[string]any{
		"content": j.body,
		"platforms": []any{map[string]any{
			"platform": normalizeZernioPlatform(j.platform), "accountId": j.providerAccountID,
		}},
	}
	switch intent {
	case postModeDraft:
		input["isDraft"] = true
		input["publishNow"] = false
	case postModeSchedule:
		input["isDraft"] = false
		input["publishNow"] = false
		input["scheduledFor"] = scheduleAt
	case postModePublish:
		input["isDraft"] = false
		input["publishNow"] = true
	default:
		return nil, fmt.Errorf("unsupported provider workflow intent %q", intent)
	}
	if title := strings.TrimSpace(toString(j.options["title"])); title != "" {
		input["title"] = title
	}
	if visibility := strings.TrimSpace(toString(j.options["visibility"])); visibility != "" {
		input["visibility"] = visibility
	}
	if tags, ok := j.options["tags"]; ok {
		input["tags"] = tags
	}
	if len(j.media) > 0 {
		items := make([]map[string]any, 0, len(j.media))
		for _, media := range j.media {
			item := map[string]any{"url": media.URL, "mime": media.Mime}
			if media.IsVideo() {
				item["type"] = "video"
			} else if media.IsImage() {
				item["type"] = "image"
			}
			items = append(items, item)
		}
		input["mediaItems"] = items
	}
	if platformData := zernioPlatformSpecificData(j.options); len(platformData) > 0 {
		input["platforms"].([]any)[0].(map[string]any)["platformSpecificData"] = platformData
	}
	return input, nil
}

// syncProviderWorkflow mirrors only posts explicitly configured with
// provider_sync_mode=mirror. Local is the default and never calls a provider.
// Mirror failures are recorded per target and do not destroy the local draft.
func (a *App) syncProviderWorkflow(ctx *sdk.AppCtx, postID int64, intent, scheduleAt string) []map[string]any {
	var syncMode, body, mediaProjectID string
	var mediaJSON string
	if err := ctx.AppDB().QueryRow(
		`SELECT provider_sync_mode, body, COALESCE(media_storage_ids,'[]'), COALESCE(media_project_id,'')
		   FROM posts WHERE id=?`, postID,
	).Scan(&syncMode, &body, &mediaJSON, &mediaProjectID); err != nil || syncMode != "mirror" {
		return nil
	}
	var mediaIDs []int64
	_ = json.Unmarshal([]byte(mediaJSON), &mediaIDs)
	media, mediaErr := a.resolveMedia(ctx, mediaIDs, mediaProjectID)
	rows, err := ctx.AppDB().Query(
		`SELECT t.id, a.platform, a.connection_id, COALESCE(a.external_account_id,''),
		        COALESCE(a.page_credentials,''), COALESCE(t.options,''),
		        COALESCE(a.provider_slug,'native'), COALESCE(a.provider_account_id,''),
		        COALESCE(t.provider_post_id,'')
		   FROM post_targets t JOIN social_accounts a ON a.id=t.social_account_id
		  WHERE t.post_id=? AND COALESCE(a.provider_slug,'native')!='native'`, postID,
	)
	if err != nil {
		return []map[string]any{{"status": "failed", "error": err.Error()}}
	}
	type syncEntry struct {
		job        publishJob
		providerID string
	}
	entries := []syncEntry{}
	for rows.Next() {
		var j publishJob
		var optionsRaw, providerID string
		if rows.Scan(&j.targetID, &j.platform, &j.connID, &j.extID, &j.pageCreds, &optionsRaw,
			&j.providerSlug, &j.providerAccountID, &providerID) != nil {
			continue
		}
		j.body, j.media, j.mediaProjectID = body, media, mediaProjectID
		_ = json.Unmarshal([]byte(optionsRaw), &j.options)
		if override := strings.TrimSpace(toString(j.options["body"])); override != "" {
			j.body = override
		}
		entries = append(entries, syncEntry{job: j, providerID: providerID})
	}
	rows.Close()
	results := []map[string]any{}
	for _, entry := range entries {
		j, providerID := entry.job, entry.providerID
		adapter := a.providerLifecycle(j.providerSlug)
		if adapter == nil || !adapter.Capabilities().NativeDrafts {
			continue
		}
		if mediaErr != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET provider_sync_status='failed', last_error=? WHERE id=?`, mediaErr.Error(), j.targetID)
			results = append(results, map[string]any{"target_id": j.targetID, "status": "failed", "error": mediaErr.Error()})
			continue
		}
		result, callErr := adapter.UpsertWorkflowPost(ctx, providerWorkflowRequest{Intent: intent, ScheduleAt: scheduleAt, ProviderID: providerID, PublishJob: j})
		if callErr != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET provider_sync_status='failed', last_error=? WHERE id=?`, callErr.Error(), j.targetID)
			results = append(results, map[string]any{"target_id": j.targetID, "status": "failed", "error": callErr.Error()})
			continue
		}
		_, _ = ctx.AppDB().Exec(
			`UPDATE post_targets SET provider_post_id=?, provider_data=?, provider_sync_status=?,
			        provider_updated_at=CURRENT_TIMESTAMP, last_error=NULL WHERE id=?`,
			result.ProviderPostID, string(result.Raw), intent, j.targetID,
		)
		results = append(results, map[string]any{"target_id": j.targetID, "status": intent, "provider_post_id": result.ProviderPostID})
	}
	return results
}

func zernioWorkflowStatus(item, platformItem map[string]any) (status, requestedMode, scheduleAt string) {
	raw := strings.ToLower(strings.TrimSpace(firstString(platformItem, "status", "state", "postStatus")))
	if raw == "" {
		raw = strings.ToLower(strings.TrimSpace(firstString(item, "status", "state", "postStatus")))
	}
	isDraft, _ := platformItem["isDraft"].(bool)
	if !isDraft {
		isDraft, _ = item["isDraft"].(bool)
	}
	scheduleAt = firstString(platformItem, "scheduledFor", "scheduled_for", "scheduleAt", "schedule_at")
	if scheduleAt == "" {
		scheduleAt = firstString(item, "scheduledFor", "scheduled_for", "scheduleAt", "schedule_at")
	}
	publishedEvidence := firstString(platformItem,
		"publishedAt", "published_at", "platformPostId", "platform_post_id", "platformPostUrl", "platform_post_url")
	if publishedEvidence == "" {
		publishedEvidence = firstString(item, "publishedAt", "published_at", "platformPostId", "platform_post_id")
	}
	switch {
	case isDraft || strings.Contains(raw, "draft"):
		return "draft", postModeDraft, ""
	case strings.Contains(raw, "fail") || strings.Contains(raw, "error"):
		return "failed", postModePublish, scheduleAt
	case raw == "published" || raw == "completed" || (raw == "" && publishedEvidence != ""):
		return "published", postModePublish, scheduleAt
	case raw == "publishing" || raw == "processing":
		return "publishing", postModePublish, scheduleAt
	case scheduleAt != "" || strings.Contains(raw, "schedul") || strings.Contains(raw, "queue"):
		return "scheduled", postModeSchedule, scheduleAt
	default:
		return "publishing", postModePublish, scheduleAt
	}
}

func (a *App) reconcileImportedProviderPost(
	ctx *sdk.AppCtx, pid string, accountID, profileID int64,
	body, providerPostID, platformPostID, platformURL, status, requestedMode, scheduleAt string,
	createdAt, publishedAt string, mediaURLs []string, raw map[string]any,
) (bool, error) {
	if providerPostID == "" {
		return false, errors.New("provider post id required")
	}
	var suppressed int
	tombstoneErr := ctx.AppDB().QueryRow(
		`SELECT 1 FROM provider_import_tombstones
		  WHERE social_account_id=? AND provider_slug=? AND provider_post_id=?`,
		accountID, zernioProviderSlug, providerPostID,
	).Scan(&suppressed)
	if tombstoneErr == nil {
		return false, nil
	}
	if tombstoneErr != sql.ErrNoRows {
		return false, tombstoneErr
	}
	rawJSON, _ := json.Marshal(raw)
	mediaJSON, _ := json.Marshal(mediaURLs)
	var targetID, postID int64
	err := ctx.AppDB().QueryRow(
		`SELECT id, post_id FROM post_targets WHERE social_account_id=? AND provider_post_id=?`,
		accountID, providerPostID,
	).Scan(&targetID, &postID)
	if err == nil {
		unlock := lockSocialPost(ctx, postID)
		defer unlock()
		var source, localStatus string
		var priorRevision int64
		if err := ctx.AppDB().QueryRow(`SELECT source,status,revision FROM posts WHERE id=? AND project_id=?`, postID, pid).Scan(&source, &localStatus, &priorRevision); err != nil {
			return false, err
		}
		tx, err := ctx.AppDB().Begin()
		if err != nil {
			return false, err
		}
		defer tx.Rollback()
		targetStatus := status
		reviewing := localStatus == "draft" || localStatus == "approved" || localStatus == "in_review" || localStatus == "rejected"
		if source != "provider" && reviewing {
			targetStatus = "draft"
		}
		if source == "provider" && !reviewing {
			_, err = tx.Exec(`UPDATE posts SET created_at=COALESCE(NULLIF(?,''),created_at),published_at=CASE WHEN ?='published' THEN NULLIF(?,'') ELSE NULL END,body=?,requested_mode=?,schedule_at=?,external_media_urls=?,revision=revision+CASE WHEN body<>? THEN 1 ELSE 0 END,approved_revision=CASE WHEN body<>? THEN 0 ELSE approved_revision END,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, createdAt, status, publishedAt, body, requestedMode, nullable(scheduleAt), string(mediaJSON), body, body, postID, pid)
			if err != nil {
				return false, err
			}
		}
		_, err = tx.Exec(`UPDATE post_targets SET status=?,platform_post_id=?,platform_url=?,provider_data=?,provider_sync_status=?,provider_updated_at=CURRENT_TIMESTAMP,published_at=CASE WHEN ?='published' THEN ? ELSE NULL END WHERE id=?`, targetStatus, nullable(platformPostID), nullable(platformURL), string(rawJSON), status, status, nullable(publishedAt), targetID)
		if err != nil {
			return false, err
		}

		var currentRevision int64
		if err = tx.QueryRow(`SELECT revision FROM posts WHERE id=?`, postID).Scan(&currentRevision); err != nil {
			return false, err
		}
		if currentRevision != priorRevision {
			if err = recordPostRevisionTx(tx, postID, currentRevision, "provider:zernio"); err != nil {
				return false, err
			}
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		if !reviewing {
			a.rollupPostStatus(ctx, postID)
		}
		return false, nil
	}
	if err != sql.ErrNoRows {
		return false, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	storedPublishedAt := any(nil)
	if status == "published" {
		storedPublishedAt = nullable(publishedAt)
	}
	postResult, err := tx.Exec(
		`INSERT INTO posts
		 (project_id, body, media_storage_ids, external_media_urls, schedule_at, status, profile_id,
		  created_at, imported_at, published_at, revision, approval_status, requested_mode, provider_sync_mode, source, updated_at)
		 VALUES (?, ?, '[]', ?, ?, ?, ?, COALESCE(NULLIF(?,''),CURRENT_TIMESTAMP), CURRENT_TIMESTAMP,
		         ?, 1, 'not_requested', ?, 'mirror', 'provider', CURRENT_TIMESTAMP)`,
		pid, body, string(mediaJSON), nullable(scheduleAt), status, profileID, createdAt, storedPublishedAt, requestedMode,
	)
	if err != nil {
		return false, err
	}
	postID, _ = postResult.LastInsertId()
	_, err = tx.Exec(
		`INSERT INTO post_targets
		 (post_id, social_account_id, status, platform_post_id, platform_url, provider_post_id,
		  provider_data, provider_sync_status, provider_updated_at, published_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP,
		         CASE WHEN ?='published' THEN ? ELSE NULL END)`,
		postID, accountID, status, nullable(platformPostID), nullable(platformURL), providerPostID,
		string(rawJSON), status, status, nullable(publishedAt),
	)
	if err != nil {
		return false, err
	}
	if err := recordPostRevisionTx(tx, postID, 1, "provider:zernio"); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (a *App) toolAccountProviderSyncUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	accountID := int64(intArg(args, "social_account_id", intArg(args, "id", 0)))
	if accountID <= 0 {
		return mcpError("social_account_id required"), nil
	}
	mode, err := normalizeProviderImportMode(toString(args["mode"]))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	pid := projectScope(ctx, args)
	result, err := ctx.AppDB().Exec(
		`UPDATE social_accounts SET provider_import_mode=?
		  WHERE id=? AND project_id=? AND provider_slug!=?`,
		mode, accountID, pid, "native",
	)
	if err != nil {
		return nil, err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return mcpError("provider-backed account not found"), nil
	}
	return map[string]any{
		"social_account_id":    accountID,
		"provider_import_mode": mode,
		"publishing_unchanged": true,
	}, nil
}

// runProviderReconciler keeps provider-native posts already managed by Social
// current by default. A provider account must be explicitly switched to
// full_history before the worker may discover and import unrelated posts.
// Native accounts and accounts with synchronization turned off remain untouched.
func (a *App) runProviderReconciler(runCtx context.Context, ctx *sdk.AppCtx) error {
	pid := projectScope(ctx)
	if pid == "" {
		return nil
	}
	type account struct {
		id, connectionID, profileID   int64
		providerAccountID, importMode string
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, connection_id, COALESCE(provider_account_id,''), COALESCE(profile_id,0),
		        COALESCE(provider_import_mode,?)
		   FROM social_accounts
		  WHERE project_id=? AND status='active' AND provider_slug=?
		  ORDER BY id`, providerImportModeManaged, pid, zernioProviderSlug,
	)
	if err != nil {
		return err
	}
	accounts := []account{}
	for rows.Next() {
		var item account
		if rows.Scan(&item.id, &item.connectionID, &item.providerAccountID, &item.profileID, &item.importMode) == nil {
			accounts = append(accounts, item)
		}
	}
	rows.Close()
	for _, item := range accounts {
		if err := runCtx.Err(); err != nil {
			return err
		}
		mode, err := normalizeProviderImportMode(item.importMode)
		if err != nil {
			ctx.Logger().Warn("provider reconciliation skipped invalid mode", "account", item.id, "mode", item.importMode)
			continue
		}
		if mode == providerImportModeOff {
			continue
		}
		var result importResult
		if mode == providerImportModeFullHistory {
			result = a.importZernioPosts(ctx, pid, importResult{}, item.id, item.connectionID, item.providerAccountID, item.profileID, 200)
		} else {
			result = a.reconcileManagedZernioPosts(ctx, pid, item.id, item.connectionID, item.providerAccountID, item.profileID)
		}
		if result.Status == "failed" {
			ctx.Logger().Warn("provider reconciliation failed", "account", item.id, "error", result.Error)
		}
	}
	return nil
}
