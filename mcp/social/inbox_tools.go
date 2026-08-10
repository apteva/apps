// inbox_tools — MCP handlers for the inbox_* surface. Reads go
// straight at the local DB via inbox.go; replies / moderation dispatch
// into per-platform code and return a consistent envelope.
package main

import (
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── inbox_list ────────────────────────────────────────────────────

func (a *App) toolInboxList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx, args)
	filter := inboxListFilter{ProjectID: pid}

	if v, ok := args["social_account_ids"]; ok {
		for _, raw := range toAnySlice(v) {
			if id := toInt64Loose(raw); id > 0 {
				filter.SocialAccountIDs = append(filter.SocialAccountIDs, id)
			}
		}
	}
	if v, ok := args["kinds"]; ok {
		for _, raw := range toAnySlice(v) {
			if s, _ := raw.(string); s != "" {
				if !validInboxKinds[s] {
					return mcpError(fmt.Sprintf("invalid kind %q — valid: comment, dm, mention, review", s)), nil
				}
				filter.Kinds = append(filter.Kinds, s)
			}
		}
	}
	if v, ok := args["status"]; ok {
		for _, raw := range toAnySlice(v) {
			if s, _ := raw.(string); s != "" {
				if !validInboxStatuses[s] {
					return mcpError(fmt.Sprintf("invalid status %q — valid: unread, read, replied, hidden, archived", s)), nil
				}
				filter.Statuses = append(filter.Statuses, s)
			}
		}
	}
	if s, _ := args["since"].(string); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return mcpError(fmt.Sprintf("invalid `since` (need RFC3339): %v", err)), nil
		}
		filter.Since = t
	}
	filter.Limit = intArg(args, "limit", 50)

	items, err := listInboxItems(ctx.AppDB(), filter)
	if err != nil {
		return nil, fmt.Errorf("list inbox items: %w", err)
	}
	return map[string]any{"items": items, "count": len(items)}, nil
}

// ─── inbox_get ─────────────────────────────────────────────────────

func (a *App) toolInboxGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx, args)
	id := int64(intArg(args, "id", 0))
	if id <= 0 {
		return mcpError("id required"), nil
	}
	withThread, _ := args["with_thread"].(bool)

	item, err := getInboxItem(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("get inbox item: %w", err)
	}
	if item == nil {
		return mcpError("inbox item not found"), nil
	}
	out := map[string]any{"item": item}
	if withThread {
		thread, terr := getInboxThread(ctx.AppDB(), pid, item)
		if terr != nil {
			return nil, fmt.Errorf("get inbox thread: %w", terr)
		}
		out["thread"] = thread
	}
	return out, nil
}

// ─── inbox_mark_read / inbox_mark_unread / inbox_archive ───────────

func (a *App) toolInboxMarkRead(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return setInboxStatusTool(ctx, args, inboxStatusRead)
}

func (a *App) toolInboxMarkUnread(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return setInboxStatusTool(ctx, args, inboxStatusUnread)
}

func (a *App) toolInboxArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return setInboxStatusTool(ctx, args, inboxStatusArchived)
}

// setInboxStatusTool accepts either {id} or {ids: [...]} so callers
// can mark a whole thread read in one call.
func setInboxStatusTool(ctx *sdk.AppCtx, args map[string]any, status string) (any, error) {
	pid := projectScope(ctx, args)
	ids := collectIDs(args)
	if len(ids) == 0 {
		return mcpError("id or ids required"), nil
	}
	updated := []int64{}
	for _, id := range ids {
		if err := setInboxStatus(ctx.AppDB(), pid, id, status); err != nil {
			// Don't fail the whole batch on one missing row; surface
			// it in `missing` instead.
			continue
		}
		updated = append(updated, id)
	}
	return map[string]any{
		"status":  status,
		"updated": updated,
		"missing": diffIDs(ids, updated),
	}, nil
}

const (
	inboxReplyModeAuto    = "auto"
	inboxReplyModePublic  = "public"
	inboxReplyModePrivate = "private"
)

// ─── inbox_reply ───────────────────────────────────────────────────

// inboxOutcome is the unified per-target envelope every inbox_* tool
// that touches a platform returns. Mirrors the post_metrics shape so
// the dashboard can render any inbox response with the same widget.
type inboxOutcome struct {
	InboxItemID     int64  `json:"inbox_item_id,omitempty"`
	SocialAccountID int64  `json:"social_account_id,omitempty"`
	Platform        string `json:"platform,omitempty"`
	Status          string `json:"status"` // ok | unsupported | skipped | failed
	Reason          string `json:"reason,omitempty"`
	Error           string `json:"error,omitempty"`
	ExternalID      string `json:"external_id,omitempty"` // platform-side id of the created reply
	Permalink       string `json:"permalink,omitempty"`
}

func (a *App) toolInboxReply(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx, args)
	id := int64(intArg(args, "id", 0))
	if id <= 0 {
		return mcpError("id required"), nil
	}
	body, _ := args["body"].(string)
	if body == "" {
		return mcpError("body required"), nil
	}
	item, err := getInboxItem(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("get inbox item: %w", err)
	}
	if item == nil {
		return mcpError("inbox item not found"), nil
	}
	mode := strings.ToLower(strings.TrimSpace(toString(args["mode"])))
	if mode == "" {
		mode = inboxReplyModeAuto
	}
	switch mode {
	case inboxReplyModeAuto, inboxReplyModePublic, inboxReplyModePrivate:
	default:
		return mcpError("mode must be auto, public, or private"), nil
	}
	if isProviderBackedAccount(ctx, pid, item.SocialAccountID, zernioProviderSlug) {
		if mode == inboxReplyModePrivate {
			return inboxOutcome{
				InboxItemID:     item.ID,
				SocialAccountID: item.SocialAccountID,
				Platform:        item.Platform,
				Status:          "unsupported",
				Reason:          "provider-backed accounts do not expose private comment replies yet",
			}, nil
		}
		return a.zernioInboxReply(ctx, item, body), nil
	}
	if mode == inboxReplyModePrivate {
		if item.Kind != inboxKindComment || !platformSupportsInbox(item.Platform, item.Kind, "private_reply") {
			return inboxOutcome{
				InboxItemID:     item.ID,
				SocialAccountID: item.SocialAccountID,
				Platform:        item.Platform,
				Status:          "unsupported",
				Reason:          fmt.Sprintf("%s does not support private replies to %s items", item.Platform, item.Kind),
			}, nil
		}
		switch item.Platform {
		case "instagram":
			return instagramInboxPrivateReply(ctx, item, body), nil
		case "facebook":
			return facebookInboxReply(ctx, item, body, mode), nil
		}
		return inboxOutcome{
			InboxItemID:     item.ID,
			SocialAccountID: item.SocialAccountID,
			Platform:        item.Platform,
			Status:          "unsupported",
			Reason:          fmt.Sprintf("%s private reply handler not yet wired", item.Platform),
		}, nil
	}
	if !platformSupportsInbox(item.Platform, item.Kind, "write") {
		return inboxOutcome{
			InboxItemID:     item.ID,
			SocialAccountID: item.SocialAccountID,
			Platform:        item.Platform,
			Status:          "unsupported",
			Reason:          fmt.Sprintf("%s does not support replies to %s items", item.Platform, item.Kind),
		}, nil
	}
	switch item.Platform {
	case "facebook":
		return facebookInboxReply(ctx, item, body, mode), nil
	case "instagram":
		return instagramInboxReply(ctx, item, body), nil
	case "twitter":
		return twitterInboxReply(ctx, item, body), nil
	}
	return inboxOutcome{
		InboxItemID:     item.ID,
		SocialAccountID: item.SocialAccountID,
		Platform:        item.Platform,
		Status:          "unsupported",
		Reason:          fmt.Sprintf("%s reply handler not yet wired", item.Platform),
	}, nil
}

func (a *App) toolInboxPrivateReply(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	args["mode"] = inboxReplyModePrivate
	return a.toolInboxReply(ctx, args)
}

func (a *App) toolInboxHide(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return moderateComment(ctx, args, "hide", true)
}

func (a *App) toolInboxUnhide(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return moderateComment(ctx, args, "hide", false)
}

func (a *App) toolInboxLike(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	// Like isn't wired on any platform yet — IG Graph API doesn't
	// expose the verb at all; FB pages support /{id}/likes POST but
	// that integration tool isn't in the catalog. Honest stub.
	return commentModerationStub(ctx, args, "like")
}

func (a *App) toolInboxDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx, args)
	id := int64(intArg(args, "id", 0))
	if id <= 0 {
		return mcpError("id required"), nil
	}
	item, err := getInboxItem(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("get inbox item: %w", err)
	}
	if item == nil {
		return mcpError("inbox item not found"), nil
	}
	if isProviderBackedAccount(ctx, pid, item.SocialAccountID, zernioProviderSlug) {
		return a.zernioCommentModeration(ctx, item, "delete", false), nil
	}
	if !platformSupportsInbox(item.Platform, item.Kind, "delete") {
		return inboxOutcome{
			InboxItemID:     item.ID,
			SocialAccountID: item.SocialAccountID,
			Platform:        item.Platform,
			Status:          "unsupported",
			Reason:          fmt.Sprintf("%s does not support delete on %s items", item.Platform, item.Kind),
		}, nil
	}
	if item.Platform == "instagram" {
		return instagramInboxDelete(ctx, item), nil
	}
	if item.Platform == "facebook" {
		return facebookInboxDelete(ctx, item), nil
	}
	return inboxOutcome{
		InboxItemID:     item.ID,
		SocialAccountID: item.SocialAccountID,
		Platform:        item.Platform,
		Status:          "unsupported",
		Reason:          fmt.Sprintf("%s delete handler not yet wired", item.Platform),
	}, nil
}

// moderateComment is the shared body for inbox_hide / inbox_unhide.
// `hide=true` hides, `hide=false` unhides — the upstream verb is the
// same endpoint.
func moderateComment(ctx *sdk.AppCtx, args map[string]any, action string, hide bool) (any, error) {
	pid := projectScope(ctx, args)
	id := int64(intArg(args, "id", 0))
	if id <= 0 {
		return mcpError("id required"), nil
	}
	item, err := getInboxItem(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("get inbox item: %w", err)
	}
	if item == nil {
		return mcpError("inbox item not found"), nil
	}
	if isProviderBackedAccount(ctx, pid, item.SocialAccountID, zernioProviderSlug) {
		return (&App{}).zernioCommentModeration(ctx, item, action, hide), nil
	}
	if !platformSupportsInbox(item.Platform, item.Kind, "hide") {
		return inboxOutcome{
			InboxItemID:     item.ID,
			SocialAccountID: item.SocialAccountID,
			Platform:        item.Platform,
			Status:          "unsupported",
			Reason:          fmt.Sprintf("%s does not support %s on %s items", item.Platform, action, item.Kind),
		}, nil
	}
	if item.Platform == "instagram" {
		return instagramInboxHide(ctx, item, hide), nil
	}
	if item.Platform == "facebook" {
		return facebookInboxHide(ctx, item, hide), nil
	}
	return inboxOutcome{
		InboxItemID:     item.ID,
		SocialAccountID: item.SocialAccountID,
		Platform:        item.Platform,
		Status:          "unsupported",
		Reason:          fmt.Sprintf("%s %s handler not yet wired", item.Platform, action),
	}, nil
}

// commentModerationStub mirrors moderateComment's plumbing but always
// returns unsupported with a clear reason. Lets inbox_like share the
// load-an-item-and-respond shape without a real implementation.
func commentModerationStub(ctx *sdk.AppCtx, args map[string]any, action string) (any, error) {
	pid := projectScope(ctx, args)
	id := int64(intArg(args, "id", 0))
	if id <= 0 {
		return mcpError("id required"), nil
	}
	item, err := getInboxItem(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, fmt.Errorf("get inbox item: %w", err)
	}
	if item == nil {
		return mcpError("inbox item not found"), nil
	}
	if isProviderBackedAccount(ctx, pid, item.SocialAccountID, zernioProviderSlug) {
		return (&App{}).zernioCommentModeration(ctx, item, action, true), nil
	}
	if !platformSupportsInbox(item.Platform, item.Kind, action) {
		return inboxOutcome{
			InboxItemID:     item.ID,
			SocialAccountID: item.SocialAccountID,
			Platform:        item.Platform,
			Status:          "unsupported",
			Reason:          fmt.Sprintf("%s does not support %s on %s items", item.Platform, action, item.Kind),
		}, nil
	}
	return inboxOutcome{
		InboxItemID:     item.ID,
		SocialAccountID: item.SocialAccountID,
		Platform:        item.Platform,
		Status:          "unsupported",
		Reason:          fmt.Sprintf("%s %s handler not yet wired", item.Platform, action),
	}, nil
}

// ─── inbox_sync (stub — poll worker lands later) ───────────────────

func (a *App) toolInboxSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx, args)
	// Resolve which accounts to (eventually) sync — if caller passed
	// none we'd cover all active accounts. For now we still return the
	// list so callers can see what WILL be synced once the worker
	// lands.
	var ids []int64
	if v, ok := args["social_account_ids"]; ok {
		for _, raw := range toAnySlice(v) {
			if id := toInt64Loose(raw); id > 0 {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		rows, err := ctx.AppDB().Query(
			`SELECT id FROM social_accounts WHERE project_id=? AND status='active' ORDER BY id`,
			pid,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
	}

	type syncResult struct {
		SocialAccountID int64  `json:"social_account_id"`
		Platform        string `json:"platform,omitempty"`
		Status          string `json:"status"`
		Reason          string `json:"reason,omitempty"`
		Error           string `json:"error,omitempty"`
		Report          any    `json:"report,omitempty"`
	}
	results := make([]syncResult, 0, len(ids))
	for _, id := range ids {
		var platform, providerSlug string
		var providerAccountID string
		var connID int64
		if err := ctx.AppDB().QueryRow(
			`SELECT platform, COALESCE(provider_slug,'native'), COALESCE(provider_account_id,''), connection_id
			   FROM social_accounts WHERE id=? AND project_id=?`,
			id, pid,
		).Scan(&platform, &providerSlug, &providerAccountID, &connID); err != nil {
			results = append(results, syncResult{
				SocialAccountID: id,
				Status:          "failed",
				Error:           err.Error(),
			})
			continue
		}
		if providerSlug == zernioProviderSlug {
			report, err := a.syncZernioInbox(ctx, pid, id, connID, providerAccountID, platform)
			if err != nil {
				results = append(results, syncResult{
					SocialAccountID: id,
					Platform:        platform,
					Status:          "failed",
					Error:           err.Error(),
				})
				continue
			}
			results = append(results, syncResult{
				SocialAccountID: id,
				Platform:        platform,
				Status:          "ok",
				Report:          report,
			})
			continue
		}
		switch platform {
		case "facebook":
			report, err := syncFacebookAccount(ctx, pid, id)
			if err != nil {
				results = append(results, syncResult{
					SocialAccountID: id,
					Platform:        platform,
					Status:          "failed",
					Error:           err.Error(),
				})
				continue
			}
			results = append(results, syncResult{
				SocialAccountID: id,
				Platform:        platform,
				Status:          "ok",
				Report:          report,
			})
		case "instagram":
			report, err := syncInstagramAccount(ctx, pid, id)
			if err != nil {
				results = append(results, syncResult{
					SocialAccountID: id,
					Platform:        platform,
					Status:          "failed",
					Error:           err.Error(),
				})
				continue
			}
			results = append(results, syncResult{
				SocialAccountID: id,
				Platform:        platform,
				Status:          "ok",
				Report:          report,
			})
		case "twitter":
			report, err := syncTwitterAccount(ctx, pid, id)
			if err != nil {
				results = append(results, syncResult{
					SocialAccountID: id,
					Platform:        platform,
					Status:          "failed",
					Error:           err.Error(),
				})
				continue
			}
			status := "ok"
			if len(report.Warnings) > 0 {
				status = "partial"
			}
			results = append(results, syncResult{
				SocialAccountID: id,
				Platform:        platform,
				Status:          status,
				Report:          report,
			})
		default:
			results = append(results, syncResult{
				SocialAccountID: id,
				Platform:        platform,
				Status:          "unsupported",
				Reason:          fmt.Sprintf("%s poll handler not yet wired", platform),
			})
		}
	}
	return map[string]any{
		"results": results,
		"count":   len(results),
	}, nil
}

func isProviderBackedAccount(ctx *sdk.AppCtx, pid string, accountID int64, provider string) bool {
	var slug string
	_ = ctx.AppDB().QueryRow(
		`SELECT COALESCE(provider_slug,'native') FROM social_accounts WHERE id=? AND project_id=?`,
		accountID, pid,
	).Scan(&slug)
	return slug == provider
}

// ─── helpers ───────────────────────────────────────────────────────

// collectIDs accepts either args["id"] (single) or args["ids"]
// (array of any-numeric-shape) so single-row and batch callers can
// share one tool.
func collectIDs(args map[string]any) []int64 {
	var out []int64
	if id := int64(intArg(args, "id", 0)); id > 0 {
		out = append(out, id)
	}
	if v, ok := args["ids"]; ok {
		for _, raw := range toAnySlice(v) {
			if id := toInt64Loose(raw); id > 0 {
				out = append(out, id)
			}
		}
	}
	return out
}

func toAnySlice(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []int64:
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out
	case []int:
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out
	case []string:
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out
	}
	return nil
}

func diffIDs(have, kept []int64) []int64 {
	keep := make(map[int64]bool, len(kept))
	for _, id := range kept {
		keep[id] = true
	}
	var miss []int64
	for _, id := range have {
		if !keep[id] {
			miss = append(miss, id)
		}
	}
	return miss
}
