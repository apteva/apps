package main

import (
	"database/sql"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"time"
)

var errNotFound = errors.New("subscription not found")

func beginWrite(db *sql.DB) (*sql.Tx, error) {
	tx, e := db.Begin()
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec("UPDATE subscriptions SET id=id WHERE 0"); e != nil {
		tx.Rollback()
		return nil, e
	}
	return tx, nil
}
func requiredSub(db queryDB, pid string, id int64, nested bool) (*Subscription, error) {
	s, e := dbSubscriptionGet(db, pid, id, nested)
	if e != nil {
		return nil, e
	}
	if s == nil {
		return nil, errNotFound
	}
	return s, nil
}
func cycleArgs(input map[string]any) (map[string]any, error) {
	args := copyArgs(input)
	if e := validateScalarFields(args); e != nil {
		return nil, e
	}
	if e := validateIDs(args, "subscription_id", "invoice_id", "order_id", "entitlement_grant_id"); e != nil {
		return nil, e
	}
	if e := dates(args, "period_start", "period_end", "due_at"); e != nil {
		return nil, e
	}
	if e := period(strArg(args, "period_start"), strArg(args, "period_end")); e != nil {
		return nil, e
	}
	if _, e := metadata(args["metadata"]); e != nil {
		return nil, e
	}
	payment := firstNonEmpty(strArg(args, "payment_status"), "pending")
	fulfillment := firstNonEmpty(strArg(args, "fulfillment_status"), "none")
	if !validPayment[payment] || !validFulfillment[fulfillment] {
		return nil, errors.New("invalid payment or fulfillment status")
	}
	args["payment_status"] = payment
	args["fulfillment_status"] = fulfillment
	for _, k := range []string{"tax_cents", "shipping_cents", "total_cents"} {
		if _, ok := args[k]; !ok {
			continue
		}
		n, e := integer(args, k, 0)
		if e != nil {
			return nil, e
		}
		if n < 0 {
			return nil, fmt.Errorf("%s must be nonnegative", k)
		}
		args[k] = n
	}
	return args, nil
}
func isComplete(payment, fulfillment string) bool {
	return payment == "paid" && (fulfillment == "none" || fulfillment == "fulfilled" || fulfillment == "delivered")
}

func validateCreation(args map[string]any) error {
	if err := validateScalarFields(args); err != nil {
		return err
	}
	if err := validateIDs(args, "customer_id"); err != nil {
		return err
	}
	if _, err := metadata(args["metadata"]); err != nil {
		return err
	}
	count, err := integer(args, "interval_count", 1)
	if err != nil {
		return err
	}
	if err = recurrence(firstNonEmpty(strArg(args, "interval"), "month"), count); err != nil {
		return err
	}
	if _, err = number(args, "quantity", 1); err != nil {
		return err
	}
	if err = dates(args, "trial_start", "trial_end", "current_period_start", "current_period_end", "next_renewal_at"); err != nil {
		return err
	}
	for _, pair := range [][2]string{{"trial_start", "trial_end"}, {"current_period_start", "current_period_end"}} {
		if strArg(args, pair[0]) != "" && strArg(args, pair[1]) != "" {
			if err = period(strArg(args, pair[0]), strArg(args, pair[1])); err != nil {
				return err
			}
		}
	}
	return nil
}

func itemUnitAmount(args map[string]any) int64 {
	if _, ok := args["unit_amount_cents"]; ok {
		return int64Arg(args, "unit_amount_cents")
	}
	return int64Arg(args, "unit_price_cents")
}
func validateItemInput(args map[string]any) error {
	if _, err := metadata(args["metadata"]); err != nil {
		return err
	}
	if err := validateIDs(args, "catalog_product_id", "catalog_price_id", "product_id", "price_id"); err != nil {
		return err
	}
	if _, ok := args["quantity"]; ok {
		if _, err := number(args, "quantity", 1); err != nil {
			return err
		}
	}
	for _, key := range []string{"unit_amount_cents", "unit_price_cents", "included_units", "unit_size"} {
		if _, ok := args[key]; !ok {
			continue
		}
		n, err := integer(args, key, 0)
		if err != nil {
			return err
		}
		if n < 0 || (key == "unit_size" && n == 0) {
			return fmt.Errorf("invalid %s", key)
		}
	}
	return nil
}
func strictItems(raw []any, currency string) ([]itemIn, error) {
	cur, err := currencyCode(currency)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > 1000 {
		return nil, errors.New("items must contain 1 to 1000 entries")
	}
	out := make([]itemIn, 0, len(raw))
	for _, v := range raw {
		args, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("items must be objects")
		}
		if err = validateItemInput(args); err != nil {
			return nil, err
		}
		items := normalizeItems([]any{args}, cur)
		if len(items) != 1 {
			return nil, errors.New("item title and positive quantity required")
		}
		it := items[0]
		if it.Currency != cur {
			return nil, errors.New("item currency must match subscription currency")
		}
		if err = validateItem(it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

func existingCycle(tx *sql.Tx, sub *Subscription, args map[string]any) (*Cycle, error) {
	start, end := strArg(args, "period_start"), strArg(args, "period_end")
	var id int64
	err := tx.QueryRow(`SELECT cycle_id FROM subscription_cycle_keys WHERE subscription_id=? AND period_start=?`, sub.ID, start).Scan(&id)
	if err == nil {
		c, err := dbCycleGet(tx, sub.ProjectID, id)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, errors.New("cycle missing")
		}
		actual, err := parseDate(c.PeriodEnd)
		if err != nil || actual.Format("2006-01-02T15:04:05Z07:00") != end {
			return nil, errors.New("existing cycle has a different period end")
		}
		return c, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	var n int
	err = tx.QueryRow(`SELECT COUNT(*) FROM subscription_cycles WHERE subscription_id=? AND julianday(period_start)<julianday(?) AND julianday(period_end)>julianday(?)`, sub.ID, end, start).Scan(&n)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, errors.New("renewal overlaps an existing period")
	}
	return nil, nil
}

func cancellationTopic(sub *Subscription) string {
	if sub.CancelAt != "" && sub.Status != "cancelled" && sub.Status != "ended" {
		return "subscription.cancellation_scheduled"
	}
	return "subscription." + sub.Status
}

func anchoredPeriodEnd(db *sql.DB, sub *Subscription, start time.Time) (time.Time, error) {
	var day int
	if err := db.QueryRow(`SELECT billing_anchor_day FROM subscriptions WHERE id=? AND project_id=?`, sub.ID, sub.ProjectID).Scan(&day); err != nil {
		return time.Time{}, err
	}
	if day == 0 {
		day = start.Day()
	}
	return nextPeriod(start, sub.Interval, sub.IntervalCount, day)
}

func processScheduledCancellations(ctx *sdk.AppCtx, pid string, now time.Time) error {
	rows, err := ctx.AppDB().Query(`SELECT id FROM subscriptions WHERE project_id=? AND status NOT IN ('cancelled','ended') AND julianday(cancel_at)<=julianday(?) ORDER BY id LIMIT 200`, pid, now.UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		sub, err := dbSubscriptionGet(ctx.AppDB(), pid, id, false)
		if err != nil {
			return err
		}
		if sub == nil {
			continue
		}
		updated, err := dbSubscriptionSetStatusMetadata(ctx.AppDB(), pid, id, "ended", mapFromAny(sub.Metadata), "subscription.period_ended", map[string]any{"cancel_at": sub.CancelAt}, now)
		if err != nil {
			return err
		}
		emitSubscriptionLifecycle(ctx, updated)
	}
	return nil
}

func hasField(args map[string]any, key string) bool { _, ok := args[key]; return ok }
