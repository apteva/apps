package main

import (
	"database/sql"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// SaaS v0.12.1 -- cancelling an account cancels its subscription.
//
// An explicit saas_account_cancel asks Subscriptions to cancel the linked
// subscription. The request is recorded in the account metadata under
// subscriptionCancelKey so a failed call can be retried by reconcile or the
// subscription-cancel-sync worker. The account cancel itself is never rolled
// back. A cancel reported by Subscriptions is only marked as done here; SaaS
// never calls back into Subscriptions for it.

const (
	subscriptionCancelKey    = "subscription_cancel"
	subscriptionCancelReason = "account_cancelled"

	subscriptionCancelRequested = "requested"
	subscriptionCancelDone      = "done"
	subscriptionCancelFailed    = "failed"

	subscriptionCancelSweepLimit = 50
)

type subscriptionCancelOpts struct {
	// AtPeriodEnd lets the paid period run out before Subscriptions stops
	// the subscription. SaaS still cancels the account at once.
	AtPeriodEnd bool
	Reason      string
	Actor       string
	// Force calls Subscriptions even when an earlier request already
	// succeeded. subscriptions_cancel is idempotent, so this is safe.
	Force bool
}

func subscriptionCancelState(acct *Account) map[string]any {
	if acct == nil {
		return nil
	}
	return mapFromAny(mapFromAny(acct.Metadata)[subscriptionCancelKey])
}

func planCancelAtPeriodEnd(plan *Plan) bool {
	if plan == nil {
		return false
	}
	return boolArg(mapFromAny(plan.Metadata), "cancel_at_period_end")
}

func (a *App) accountCancelAtPeriodEnd(ctx *sdk.AppCtx, pid string, acct *Account) bool {
	plan, err := dbPlanGet(ctx.AppDB(), pid, acct.PlanKey)
	if err != nil {
		return false
	}
	return planCancelAtPeriodEnd(plan)
}

func setSubscriptionCancelState(db *sql.DB, pid, accountID string, state map[string]any) error {
	acct, err := dbAccountGet(db, pid, accountID)
	if err != nil || acct == nil {
		return firstErr(err, sql.ErrNoRows)
	}
	meta := mapFromAny(acct.Metadata)
	meta[subscriptionCancelKey] = state
	return dbAccountSetMetadata(db, pid, accountID, meta)
}

// cancelAccountSubscription asks Subscriptions to cancel the account's
// subscription and records the outcome. It never returns an error: a failure
// is stored on the account and retried later. It returns the stored state, or
// nil when the account has no subscription.
func (a *App) cancelAccountSubscription(ctx *sdk.AppCtx, pid string, acct *Account, opts subscriptionCancelOpts) map[string]any {
	subID := int64PtrValue(acct.SubscriptionID)
	if subID == 0 {
		return nil
	}
	db := ctx.AppDB()
	// Read the latest metadata; acct may be stale.
	if fresh, err := dbAccountGet(db, pid, acct.ID); err == nil && fresh != nil {
		acct = fresh
	}
	prev := subscriptionCancelState(acct)
	if !opts.Force && strArg(prev, "status") == subscriptionCancelDone {
		return prev
	}
	reason := firstNonEmpty(strings.TrimSpace(opts.Reason), subscriptionCancelReason)
	actorName := firstNonEmpty(opts.Actor, "saas")
	now := time.Now().UTC().Format(time.RFC3339)
	state := map[string]any{
		"status":          subscriptionCancelRequested,
		"subscription_id": subID,
		"at_period_end":   opts.AtPeriodEnd,
		"reason":          reason,
		"requested_at":    firstNonEmpty(strArg(prev, "requested_at"), now),
		"attempted_at":    now,
		"attempts":        int64Arg(prev, "attempts") + 1,
	}
	_ = setSubscriptionCancelState(db, pid, acct.ID, state)

	var out map[string]any
	err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_cancel", map[string]any{
		"_project_id":   pid,
		"id":            subID,
		"at_period_end": opts.AtPeriodEnd,
		"reason":        reason,
		"actor":         actorName,
	}, &out)
	if err != nil {
		msg := err.Error()
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		state["status"] = subscriptionCancelFailed
		state["error"] = msg
		_ = setSubscriptionCancelState(db, pid, acct.ID, state)
		_ = recordEvent(db, pid, acct.ID, "subscription.cancel_failed", actorName, map[string]any{
			"subscription_id": subID, "at_period_end": opts.AtPeriodEnd, "error": msg, "attempts": state["attempts"],
		})
		return state
	}
	state["status"] = subscriptionCancelDone
	state["completed_at"] = time.Now().UTC().Format(time.RFC3339)
	sub := mapFromAny(out["subscription"])
	if len(sub) == 0 {
		sub = out
	}
	if s := strArg(sub, "status"); s != "" {
		state["subscription_status"] = s
	}
	if c := strArg(sub, "cancel_at"); c != "" {
		state["cancel_at"] = c
	}
	_ = setSubscriptionCancelState(db, pid, acct.ID, state)
	_ = recordEvent(db, pid, acct.ID, "subscription.cancel_requested", actorName, map[string]any{
		"subscription_id": subID, "at_period_end": opts.AtPeriodEnd, "reason": reason,
		"subscription_status": state["subscription_status"],
	})
	return state
}

// markSubscriptionCancelled records that Subscriptions itself cancelled or
// ended the subscription, so no request is ever sent back for it.
func markSubscriptionCancelled(db *sql.DB, pid string, acct *Account, event string) {
	if acct == nil || int64PtrValue(acct.SubscriptionID) == 0 {
		return
	}
	if fresh, err := dbAccountGet(db, pid, acct.ID); err == nil && fresh != nil {
		acct = fresh
	}
	if strArg(subscriptionCancelState(acct), "status") == subscriptionCancelDone {
		return
	}
	_ = setSubscriptionCancelState(db, pid, acct.ID, map[string]any{
		"status":          subscriptionCancelDone,
		"subscription_id": int64PtrValue(acct.SubscriptionID),
		"source":          "subscriptions",
		"event":           event,
		"completed_at":    time.Now().UTC().Format(time.RFC3339),
	})
}

// retryPendingSubscriptionCancels cancels the subscriptions of cancelled
// accounts whose request failed or was never sent (accounts cancelled before
// v0.12.1).
func (a *App) retryPendingSubscriptionCancels(ctx *sdk.AppCtx) error {
	accounts, err := dbAccountsPendingSubscriptionCancel(ctx.AppDB(), projectID(ctx, nil), subscriptionCancelSweepLimit)
	if err != nil {
		return err
	}
	for _, acct := range accounts {
		a.cancelAccountSubscription(ctx, acct.ProjectID, acct, subscriptionCancelOpts{
			AtPeriodEnd: a.accountCancelAtPeriodEnd(ctx, acct.ProjectID, acct),
			Actor:       "saas",
		})
	}
	return nil
}

func dbAccountsPendingSubscriptionCancel(db *sql.DB, pid string, limit int) ([]*Account, error) {
	if limit <= 0 || limit > 500 {
		limit = subscriptionCancelSweepLimit
	}
	query := accountSelect() + ` WHERE status=? AND subscription_id IS NOT NULL AND subscription_id > 0
		AND COALESCE(json_extract(metadata_json, '$.` + subscriptionCancelKey + `.status'), '') <> ?`
	args := []any{StatusCancelled, subscriptionCancelDone}
	if pid != "" {
		query += ` AND project_id=?`
		args = append(args, pid)
	}
	query += ` ORDER BY updated_at, id LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		acct, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, acct)
	}
	return out, rows.Err()
}
