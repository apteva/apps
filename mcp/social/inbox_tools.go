// inbox_tools — MCP handlers for the inbox_* surface. Reads go
// straight at the local DB via inbox.go; replies / moderation
// dispatch into per-platform code (not yet wired — every platform
// returns status='unsupported' with a clear reason until handlers
// land).
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── inbox_list ────────────────────────────────────────────────────

func (a *App) toolInboxList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
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
	pid := os.Getenv("APTEVA_PROJECT_ID")
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
	pid := os.Getenv("APTEVA_PROJECT_ID")
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

// ─── inbox_reply (stub — per-platform dispatch lands later) ────────

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
	return dispatchInboxAction(ctx, args, "reply", func(item *inboxItem) bool {
		return platformSupportsInbox(item.Platform, item.Kind, "write")
	})
}

func (a *App) toolInboxPrivateReply(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return dispatchInboxAction(ctx, args, "private_reply", func(item *inboxItem) bool {
		return item.Kind == inboxKindComment &&
			platformSupportsInbox(item.Platform, inboxKindComment, "private_reply")
	})
}

func (a *App) toolInboxHide(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return dispatchInboxAction(ctx, args, "hide", func(item *inboxItem) bool {
		return platformSupportsInbox(item.Platform, item.Kind, "hide")
	})
}

func (a *App) toolInboxUnhide(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return dispatchInboxAction(ctx, args, "unhide", func(item *inboxItem) bool {
		return platformSupportsInbox(item.Platform, item.Kind, "hide")
	})
}

func (a *App) toolInboxLike(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return dispatchInboxAction(ctx, args, "like", func(item *inboxItem) bool {
		return platformSupportsInbox(item.Platform, item.Kind, "like")
	})
}

func (a *App) toolInboxDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return dispatchInboxAction(ctx, args, "delete", func(item *inboxItem) bool {
		return platformSupportsInbox(item.Platform, item.Kind, "delete")
	})
}

// dispatchInboxAction is the shared body for every platform-touching
// inbox tool. It loads the item, gates on the capability matrix, then
// (until per-platform handlers land) returns status='unsupported'
// with a "not yet implemented" reason. Replacing the trailing switch
// with real platform code is how each platform's inbox lights up.
func dispatchInboxAction(ctx *sdk.AppCtx, args map[string]any, action string, capCheck func(*inboxItem) bool) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
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

	out := inboxOutcome{
		InboxItemID:     item.ID,
		SocialAccountID: item.SocialAccountID,
		Platform:        item.Platform,
	}

	if !capCheck(item) {
		out.Status = "unsupported"
		out.Reason = fmt.Sprintf("%s does not support %s on %ss", item.Platform, action, item.Kind)
		return out, nil
	}

	// Capability matrix says the platform CAN handle it, but no
	// per-platform implementation is wired yet. Honest stub: callers
	// can branch on Status=="unsupported" + Reason==pendingImpl.
	out.Status = "unsupported"
	out.Reason = "platform handler not yet wired — Instagram + Twitter are the first to land"
	return out, nil
}

// ─── inbox_sync (stub — poll worker lands later) ───────────────────

func (a *App) toolInboxSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
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

	results := make([]inboxOutcome, 0, len(ids))
	for _, id := range ids {
		var platform string
		if err := ctx.AppDB().QueryRow(
			`SELECT platform FROM social_accounts WHERE id=? AND project_id=?`,
			id, pid,
		).Scan(&platform); err != nil {
			results = append(results, inboxOutcome{
				SocialAccountID: id,
				Status:          "failed",
				Error:           err.Error(),
			})
			continue
		}
		results = append(results, inboxOutcome{
			SocialAccountID: id,
			Platform:        platform,
			Status:          "unsupported",
			Reason:          "poll worker not yet wired",
		})
	}
	return map[string]any{
		"results": results,
		"count":   len(results),
	}, nil
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

// Silence "imported and not used" for stdlib pkgs only referenced in
// some branches we'll grow into. errors + strings stay so wiring new
// per-platform handlers doesn't churn the import block on first edit.
var (
	_ = errors.New
	_ = strings.TrimSpace
)
