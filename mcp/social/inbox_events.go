// inbox_events — puts comments, DMs and replies on the app bus. Every
// poller saves items through recordInboxItem, so one place decides what
// is new, what is backfill and what the account owner wrote outside
// Social. Sends emit from inbox_reply (inbox_sends.go).
package main

import (
	"database/sql"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	inboxEventReceived      = "inbox.item_received"
	inboxEventHumanReply    = "inbox.human_reply_detected"
	inboxEventReplySent     = "inbox.reply_sent"
	inboxEventReplyFailed   = "inbox.reply_failed"
	inboxEventReplyUnsure   = "inbox.reply_uncertain"
	inboxEventSynced        = "inbox.synced"
	inboxEventCoverage      = "inbox.coverage_changed"
	inboxEventsSinceCursor  = "events_since"
	inboxBackfillGrace      = time.Hour
	inboxEventTimestampForm = time.RFC3339
)

// inboxSyncCounts is what one account sync recorded. Backfilled items
// (older than when Social started watching the account) are counted but
// not emitted one by one, so connecting a busy account is one summary
// event instead of thousands of agent triggers.
type inboxSyncCounts struct {
	Received     int `json:"received"`
	Backfilled   int `json:"backfilled"`
	HumanReplies int `json:"human_replies"`
	Reconciled   int `json:"reconciled_sends"`
}

func (c inboxSyncCounts) any() bool {
	return c.Received+c.Backfilled+c.HumanReplies+c.Reconciled > 0
}

var inboxEventCounts = struct {
	sync.Mutex
	m map[postLockKey]*inboxSyncCounts
}{m: map[postLockKey]*inboxSyncCounts{}}

func inboxCountsKey(db *sql.DB, accountID int64) postLockKey {
	return postLockKey{db, accountID, "inbox-events"}
}

func resetInboxCounts(db *sql.DB, accountID int64) {
	inboxEventCounts.Lock()
	inboxEventCounts.m[inboxCountsKey(db, accountID)] = &inboxSyncCounts{}
	inboxEventCounts.Unlock()
}

func takeInboxCounts(db *sql.DB, accountID int64) inboxSyncCounts {
	inboxEventCounts.Lock()
	defer inboxEventCounts.Unlock()
	key := inboxCountsKey(db, accountID)
	counts := inboxEventCounts.m[key]
	delete(inboxEventCounts.m, key)
	if counts == nil {
		return inboxSyncCounts{}
	}
	return *counts
}

func countInbox(db *sql.DB, accountID int64, add func(*inboxSyncCounts)) {
	inboxEventCounts.Lock()
	defer inboxEventCounts.Unlock()
	if counts := inboxEventCounts.m[inboxCountsKey(db, accountID)]; counts != nil {
		add(counts)
	}
}

// inboxEventsSince is when Social started watching the account's inbox.
// It is set once, the first time anything asks; items that happened more
// than inboxBackfillGrace before it are backfill.
func inboxEventsSince(db *sql.DB, accountID int64) time.Time {
	now := time.Now().UTC().Format(inboxEventTimestampForm)
	_, _ = db.Exec(`INSERT INTO inbox_cursors(social_account_id,kind,cursor,last_sync_at) VALUES (?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(social_account_id,kind) DO NOTHING`, accountID, inboxEventsSinceCursor, now)
	var raw string
	_ = db.QueryRow(`SELECT COALESCE(cursor,'') FROM inbox_cursors WHERE social_account_id=? AND kind=?`, accountID, inboxEventsSinceCursor).Scan(&raw)
	since, err := time.Parse(inboxEventTimestampForm, raw)
	if err != nil {
		return time.Now().UTC()
	}
	return since
}

// recordInboxItem saves an item seen by a poller and emits the matching
// bus event the first time it is seen. Re-polls update the row and emit
// nothing. Reply handlers record their own sends with upsertInboxItem:
// those are announced as inbox.reply_sent by inbox_reply.
func recordInboxItem(ctx *sdk.AppCtx, in inboxUpsertInput) (int64, bool, error) {
	id, inserted, err := upsertInboxItem(ctx.AppDB(), in)
	if err != nil || !inserted {
		return id, inserted, err
	}
	db := ctx.AppDB()
	occurred := in.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	backfill := occurred.Before(inboxEventsSince(db, in.SocialAccountID).Add(-inboxBackfillGrace))
	if in.Outbound {
		if intent := reconcileInboxSend(ctx, in, id); intent != nil {
			countInbox(db, in.SocialAccountID, func(c *inboxSyncCounts) { c.Reconciled++ })
			return id, inserted, nil
		}
		if backfill {
			countInbox(db, in.SocialAccountID, func(c *inboxSyncCounts) { c.Backfilled++ })
			return id, inserted, nil
		}
		if in.ParentExternalID == "" && in.Kind != inboxKindDM {
			// The owner's own top-level comment, not an answer to anyone.
			return id, inserted, nil
		}
		// The account owner answered outside Social (YouTube Studio, the
		// Facebook app...). The parent counts as handled.
		_ = markInboxRepliedByExternalID(db, in.SocialAccountID, in.Kind, in.ParentExternalID)
		countInbox(db, in.SocialAccountID, func(c *inboxSyncCounts) { c.HumanReplies++ })
		ctx.EmitWithProject(inboxEventHumanReply, in.ProjectID, inboxItemEventPayload(id, in, occurred))
		return id, inserted, nil
	}
	if backfill {
		countInbox(db, in.SocialAccountID, func(c *inboxSyncCounts) { c.Backfilled++ })
		return id, inserted, nil
	}
	countInbox(db, in.SocialAccountID, func(c *inboxSyncCounts) { c.Received++ })
	ctx.EmitWithProject(inboxEventReceived, in.ProjectID, inboxItemEventPayload(id, in, occurred))
	return id, inserted, nil
}

// inboxItemEventPayload carries identities, not content: consumers read
// the body with inbox_get, so message text does not spread across the bus.
func inboxItemEventPayload(id int64, in inboxUpsertInput, occurred time.Time) map[string]any {
	out := map[string]any{
		"inbox_item_id":     id,
		"social_account_id": in.SocialAccountID,
		"platform":          in.Platform,
		"kind":              in.Kind,
		"external_id":       in.ExternalID,
		"occurred_at":       occurred.UTC().Format(time.RFC3339),
	}
	for key, value := range map[string]string{
		"parent_external_id": in.ParentExternalID,
		"thread_id":          in.ExternalPostID,
		"author_external_id": in.AuthorExternalID,
		"author_name":        in.AuthorName,
		"author_handle":      in.AuthorHandle,
		"permalink":          in.Permalink,
	} {
		if value != "" {
			out[key] = value
		}
	}
	if in.PostID > 0 {
		out["post_id"] = in.PostID
	}
	return out
}

// emitInboxSynced summarizes one account sync when it recorded anything.
// Quiet polls emit nothing.
func emitInboxSynced(ctx *sdk.AppCtx, pid string, accountID int64, platform, status string, counts inboxSyncCounts) {
	if !counts.any() {
		return
	}
	ctx.EmitWithProject(inboxEventSynced, pid, map[string]any{
		"social_account_id": accountID,
		"platform":          platform,
		"status":            status,
		"received":          counts.Received,
		"backfilled":        counts.Backfilled,
		"human_replies":     counts.HumanReplies,
		"reconciled_sends":  counts.Reconciled,
	})
}
