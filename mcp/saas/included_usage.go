package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// SaaS v0.14.0 -- recurring included usage.
//
// A plan can include an allowance per meter, e.g. 60 minutes a month:
//
//	metadata.included_usage: [{source_app, meter, amount, unit, period}]
//
// Each allowance lives in its own Credits wallet for the account, in the
// allowance's unit (e.g. "minutes"), separate from the paid wallet (the
// plan's credits_unit). Every period SaaS grants the amount as a lot that
// expires at the period end, so unused units never carry over. The grant's
// source_ref names the account, meter, period and plan, so a retry never
// grants twice. When the plan changes mid-period, only the difference over
// what was already granted this period is added; credit already granted is
// never taken back.
//
// saas_usage_authorize spends the allowance first: it holds what the
// allowance can cover and prices only the rest from the paid wallet. Plans
// that are not prepaid (free or paid subscription plans) can use metered
// usage up to their allowance.
//
// Periods: "month" is the UTC calendar month. "billing_cycle" follows the
// subscription's current period (read from Subscriptions) and falls back to
// the calendar month for an account without a subscription.

const (
	includedPeriodMonth        = "month"
	includedPeriodBillingCycle = "billing_cycle"

	includedUsageStateKey   = "included_usage_state"
	includedLotPriority     = 1
	includedSweepLimit      = 100
	includedRecheckInterval = 10 * time.Minute
	includedLotsListLimit   = 200
)

// includedNow is the clock for allowance periods; tests replace it.
var includedNow = func() time.Time { return time.Now().UTC() }

type includedUsageItem struct {
	SourceApp string `json:"source_app"`
	Meter     string `json:"meter"`
	Amount    int64  `json:"amount"`
	Unit      string `json:"unit"`
	Period    string `json:"period"`
}

func (i includedUsageItem) key() string { return i.SourceApp + "/" + i.Meter }

// includedUsageFromMetadata parses metadata.included_usage. Invalid entries
// are skipped here; validateIncludedUsage rejects them on plan upsert.
func includedUsageFromMetadata(meta map[string]any) []includedUsageItem {
	raw, ok := meta["included_usage"].([]any)
	if !ok {
		return nil
	}
	var out []includedUsageItem
	for _, entry := range raw {
		m := mapFromAny(entry)
		item := includedUsageItem{
			SourceApp: firstNonEmpty(strings.TrimSpace(strArg(m, "source_app")), "llm"),
			Meter:     strings.TrimSpace(strArg(m, "meter")),
			Amount:    int64Arg(m, "amount"),
			Unit:      strings.ToLower(strings.TrimSpace(strArg(m, "unit"))),
			Period:    firstNonEmpty(strings.TrimSpace(strArg(m, "period")), includedPeriodMonth),
		}
		if item.Meter == "" || item.Amount <= 0 || item.Unit == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func includedUsageForPlan(plan *Plan) []includedUsageItem {
	if plan == nil {
		return nil
	}
	return includedUsageFromMetadata(mapFromAny(plan.Metadata))
}

// validateIncludedUsage runs on plan upsert.
func validateIncludedUsage(metadata map[string]any, billingMode string, subscriptionRequired bool) error {
	raw, present := metadata["included_usage"]
	if !present || raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return errors.New("metadata.included_usage must be a list of {meter, amount, unit, period}")
	}
	creditsUnit := strings.ToLower(strings.TrimSpace(strArg(metadata, "credits_unit")))
	seenKeys := map[string]bool{}
	seenUnits := map[string]bool{}
	for i, entry := range list {
		m := mapFromAny(entry)
		if m == nil {
			return fmt.Errorf("metadata.included_usage[%d] must be an object", i)
		}
		meter := strings.TrimSpace(strArg(m, "meter"))
		if meter == "" {
			return fmt.Errorf("metadata.included_usage[%d].meter required (e.g. call:minutes)", i)
		}
		if int64Arg(m, "amount") <= 0 {
			return fmt.Errorf("metadata.included_usage[%d].amount must be a positive whole number", i)
		}
		unit := strings.ToLower(strings.TrimSpace(strArg(m, "unit")))
		if unit == "" {
			return fmt.Errorf("metadata.included_usage[%d].unit required (e.g. minutes)", i)
		}
		if creditsUnit != "" && unit == creditsUnit {
			return fmt.Errorf("metadata.included_usage[%d].unit must differ from the plan's credits_unit %q", i, creditsUnit)
		}
		if seenUnits[unit] {
			return fmt.Errorf("metadata.included_usage: unit %q is used twice; each allowance needs its own unit (e.g. call_minutes, video_minutes)", unit)
		}
		seenUnits[unit] = true
		item := includedUsageItem{SourceApp: firstNonEmpty(strings.TrimSpace(strArg(m, "source_app")), "llm"), Meter: meter}
		if seenKeys[item.key()] {
			return fmt.Errorf("metadata.included_usage: meter %s is listed twice", item.key())
		}
		seenKeys[item.key()] = true
		switch period := firstNonEmpty(strings.TrimSpace(strArg(m, "period")), includedPeriodMonth); period {
		case includedPeriodMonth:
		case includedPeriodBillingCycle:
			if billingMode != "paid" && !subscriptionRequired {
				return fmt.Errorf("metadata.included_usage[%d].period billing_cycle needs a paid subscription plan; use month", i)
			}
		default:
			return fmt.Errorf("metadata.included_usage[%d].period must be month or billing_cycle", i)
		}
		if exp := strings.TrimSpace(strArg(m, "expires")); exp != "" && exp != "period_end" {
			return fmt.Errorf("metadata.included_usage[%d].expires must be period_end", i)
		}
	}
	return nil
}

// matchIncludedUsage finds the allowance for a meter: the exact meter first,
// then "<kind>:*" like plan meters.
func matchIncludedUsage(items []includedUsageItem, sourceApp, meter string) *includedUsageItem {
	var wildcard *includedUsageItem
	kindWildcard := ""
	if i := strings.Index(meter, ":"); i > 0 {
		kindWildcard = meter[:i] + ":*"
	}
	for idx := range items {
		it := &items[idx]
		if it.SourceApp != sourceApp {
			continue
		}
		if it.Meter == meter {
			return it
		}
		if kindWildcard != "" && it.Meter == kindWildcard {
			wildcard = it
		}
	}
	return wildcard
}

func calendarMonth(now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

type includedPeriod struct {
	Start time.Time
	End   time.Time
	// Source is "month" or "billing_cycle".
	Source string
}

// subscriptionPeriodReader caches one Subscriptions read per sweep.
type subscriptionPeriodReader struct {
	ctx    *sdk.AppCtx
	pid    string
	loaded bool
	start  time.Time
	end    time.Time
	err    error
}

func (r *subscriptionPeriodReader) period(subID int64) (time.Time, time.Time, error) {
	if r.loaded {
		return r.start, r.end, r.err
	}
	r.loaded = true
	var out map[string]any
	if err := r.ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_get", map[string]any{"_project_id": r.pid, "id": subID}, &out); err != nil {
		r.err = fmt.Errorf("subscription %d: %w", subID, err)
		return r.start, r.end, r.err
	}
	sub := unwrapMap(out, "subscription")
	start, errStart := time.Parse(time.RFC3339, strArg(sub, "current_period_start"))
	end, errEnd := time.Parse(time.RFC3339, strArg(sub, "current_period_end"))
	if errStart != nil || errEnd != nil || !end.After(start) {
		r.err = fmt.Errorf("subscription %d has no current period", subID)
		return r.start, r.end, r.err
	}
	r.start, r.end = start.UTC(), end.UTC()
	return r.start, r.end, nil
}

// currentIncludedPeriod returns the allowance period containing now. ok is
// false when a billing cycle has lapsed and is not renewed yet.
func currentIncludedPeriod(item includedUsageItem, acct *Account, subs *subscriptionPeriodReader, now time.Time) (includedPeriod, bool, error) {
	if item.Period == includedPeriodBillingCycle && int64PtrValue(acct.SubscriptionID) != 0 {
		start, end, err := subs.period(int64PtrValue(acct.SubscriptionID))
		if err != nil {
			return includedPeriod{}, false, err
		}
		if !now.Before(end) || now.Before(start) {
			return includedPeriod{}, false, nil
		}
		return includedPeriod{Start: start, End: end, Source: includedPeriodBillingCycle}, true, nil
	}
	start, end := calendarMonth(now)
	return includedPeriod{Start: start, End: end, Source: includedPeriodMonth}, true, nil
}

type includedLot struct {
	ID        int64           `json:"id"`
	Source    string          `json:"source"`
	SourceRef string          `json:"source_ref"`
	Original  int64           `json:"original"`
	Remaining int64           `json:"remaining"`
	Held      int64           `json:"held"`
	ExpiresAt string          `json:"expires_at"`
	Status    string          `json:"status"`
	Metadata  json.RawMessage `json:"metadata"`
}

func (l includedLot) meta() map[string]any { return mapFromAny(l.Metadata) }

func includedSourceRef(accountID string, item includedUsageItem, period includedPeriod, planKey string) string {
	return fmt.Sprintf("saas:included:acct:%s:meter:%s:period:%s:plan:%s", accountID, item.key(), period.Start.Format(time.RFC3339), planKey)
}

func (a *App) includedLots(ctx *sdk.AppCtx, pid, accountID, unit string) ([]includedLot, error) {
	input := walletRef(pid, accountID, unit)
	input["limit"] = includedLotsListLimit
	var out struct {
		Lots []includedLot `json:"lots"`
	}
	if err := ctx.PlatformAPI().CallAppResult("credits", "credits_lots_list", input, &out); err != nil {
		return nil, fmt.Errorf("credits lots: %w", err)
	}
	return out.Lots, nil
}

// periodLots returns the allowance lots granted for this meter and period.
func periodLots(lots []includedLot, item includedUsageItem, period includedPeriod) []includedLot {
	start := period.Start.Format(time.RFC3339)
	var out []includedLot
	for _, l := range lots {
		m := l.meta()
		if strArg(m, "included_key") == item.key() && strArg(m, "period_start") == start {
			out = append(out, l)
		}
	}
	return out
}

// ensureIncludedUsage grants the account's allowances for the current period.
// It is idempotent: each grant is keyed by source_ref, and only the amount
// not yet granted this period is added.
func (a *App) ensureIncludedUsage(ctx *sdk.AppCtx, pid, accountID string) (map[string]any, error) {
	db := ctx.AppDB()
	acct, err := dbAccountGet(db, pid, accountID)
	if err != nil || acct == nil {
		return nil, firstErr(err, errors.New("account not found"))
	}
	if acct.Status != StatusActive {
		return map[string]any{"skipped": "account_" + acct.Status}, nil
	}
	plan, err := dbPlanGet(db, pid, acct.PlanKey)
	if err != nil {
		return nil, err
	}
	items := includedUsageForPlan(plan)
	if len(items) == 0 {
		return map[string]any{"skipped": "no_included_usage"}, nil
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	now := includedNow()
	subs := &subscriptionPeriodReader{ctx: ctx, pid: pid}
	nextCheck := time.Time{}
	var granted []map[string]any
	var firstError error
	for _, item := range items {
		period, ok, err := currentIncludedPeriod(item, acct, subs, now)
		if err != nil || !ok {
			if err != nil && firstError == nil {
				firstError = err
			}
			// Lapsed or unreadable cycle: look again soon.
			if retry := now.Add(includedRecheckInterval); nextCheck.IsZero() || retry.Before(nextCheck) {
				nextCheck = retry
			}
			continue
		}
		if nextCheck.IsZero() || period.End.Before(nextCheck) {
			nextCheck = period.End
		}
		ensure := walletRef(pid, acct.ID, item.Unit)
		ensure["metadata"] = map[string]any{"source_app": "saas", "saas_account_id": acct.ID, "included_key": item.key(), "kind": "included_usage"}
		var wallet map[string]any
		if err := ctx.PlatformAPI().CallAppResult("credits", "credits_wallet_ensure", ensure, &wallet); err != nil {
			if firstError == nil {
				firstError = fmt.Errorf("credits wallet %s: %w", item.Unit, err)
			}
			continue
		}
		lots, err := a.includedLots(ctx, pid, acct.ID, item.Unit)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		var already int64
		for _, l := range periodLots(lots, item, period) {
			already += l.Original
		}
		diff := item.Amount - already
		if diff <= 0 {
			continue
		}
		grant := walletRef(pid, acct.ID, item.Unit)
		grant["amount"] = diff
		grant["source"] = "promo"
		grant["source_ref"] = includedSourceRef(acct.ID, item, period, acct.PlanKey)
		grant["priority"] = includedLotPriority
		grant["expires_at"] = period.End.Format(time.RFC3339)
		grant["metadata"] = map[string]any{
			"source_app": "saas", "saas_account_id": acct.ID, "plan_key": acct.PlanKey, "reason": "included_usage",
			"included_key": item.key(), "period": item.Period, "period_start": period.Start.Format(time.RFC3339), "period_end": period.End.Format(time.RFC3339),
		}
		var out struct {
			Deduped bool `json:"deduped"`
		}
		if err := ctx.PlatformAPI().CallAppResult("credits", "credits_grant", grant, &out); err != nil {
			if firstError == nil {
				firstError = fmt.Errorf("included usage grant %s: %w", item.key(), err)
			}
			continue
		}
		if out.Deduped {
			continue
		}
		entry := map[string]any{
			"meter": item.key(), "unit": item.Unit, "amount": diff, "plan_amount": item.Amount,
			"period_start": period.Start.Format(time.RFC3339), "period_end": period.End.Format(time.RFC3339),
		}
		granted = append(granted, entry)
		_ = recordEvent(db, pid, acct.ID, "included_usage.granted", "saas", entry)
	}
	if nextCheck.IsZero() {
		nextCheck = now.Add(includedRecheckInterval)
	}
	if firstError != nil {
		// Retry on the next sweep rather than at the period end.
		if retry := now.Add(includedRecheckInterval); retry.Before(nextCheck) {
			nextCheck = retry
		}
	}
	state := map[string]any{"plan_key": acct.PlanKey, "checked_at": now.Format(time.RFC3339), "next_check_at": nextCheck.Format(time.RFC3339)}
	if firstError != nil {
		state["error"] = firstError.Error()
	}
	if _, err := db.Exec(`UPDATE saas_accounts SET metadata_json=json_set(COALESCE(metadata_json,'{}'), '$.`+includedUsageStateKey+`', json(?)) WHERE project_id=? AND id=?`,
		jsonOrEmpty(state, "{}"), pid, acct.ID); err != nil {
		return nil, err
	}
	out := map[string]any{"granted": granted, "next_check_at": state["next_check_at"]}
	if firstError != nil {
		return out, firstError
	}
	return out, nil
}

// ensureIncludedUsageSoft runs ensureIncludedUsage where a failure must not
// fail the caller; the included-usage worker retries it.
func (a *App) ensureIncludedUsageSoft(ctx *sdk.AppCtx, pid, accountID string) {
	if pid == "" || accountID == "" || ctx.PlatformAPI() == nil {
		return
	}
	_, _ = a.ensureIncludedUsage(ctx, pid, accountID)
}

// runIncludedUsage is the included-usage worker: it grants allowances for
// active accounts whose period ended, whose plan changed, or that were never
// checked.
func (a *App) runIncludedUsage(ctx *sdk.AppCtx) error {
	accounts, err := dbAccountsDueIncludedUsage(ctx.AppDB(), projectID(ctx, nil), includedNow(), includedSweepLimit)
	if err != nil {
		return err
	}
	for _, acct := range accounts {
		_, _ = a.ensureIncludedUsage(ctx, acct.ProjectID, acct.ID)
	}
	return nil
}

func dbAccountsDueIncludedUsage(db *sql.DB, pid string, now time.Time, limit int) ([]*Account, error) {
	query := accountSelect() + ` WHERE id IN (
		SELECT a.id FROM saas_accounts a
		JOIN saas_plans p ON p.project_id=a.project_id AND p.key=a.plan_key
		WHERE a.status=?
		  AND json_type(p.metadata_json, '$.included_usage')='array'
		  AND json_array_length(p.metadata_json, '$.included_usage') > 0
		  AND (json_extract(a.metadata_json, '$.` + includedUsageStateKey + `.next_check_at') IS NULL
		       OR json_extract(a.metadata_json, '$.` + includedUsageStateKey + `.next_check_at') <= ?
		       OR COALESCE(json_extract(a.metadata_json, '$.` + includedUsageStateKey + `.plan_key'), '') <> a.plan_key)`
	args := []any{StatusActive, now.Format(time.RFC3339)}
	if pid != "" {
		query += ` AND a.project_id=?`
		args = append(args, pid)
	}
	query += `) ORDER BY updated_at, id LIMIT ?`
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

// includedUsageSummary reports each allowance for the current period: how
// much was included, how much is left and when it resets, plus the paid
// balance for prepaid plans. It returns nil when the plan includes nothing
// and is not prepaid.
func (a *App) includedUsageSummary(ctx *sdk.AppCtx, pid string, acct *Account) (map[string]any, error) {
	plan, err := dbPlanGet(ctx.AppDB(), pid, acct.PlanKey)
	if err != nil {
		return nil, err
	}
	items := includedUsageForPlan(plan)
	if len(items) == 0 && !isPrepaidPlan(plan) {
		return nil, nil
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	now := includedNow()
	nowS := now.Format(time.RFC3339)
	subs := &subscriptionPeriodReader{ctx: ctx, pid: pid}
	included := []map[string]any{}
	for _, item := range items {
		entry := map[string]any{
			"source_app": item.SourceApp, "meter": item.Meter, "unit": item.Unit, "period": item.Period,
			"plan_amount": item.Amount, "included": int64(0), "remaining": int64(0), "used": int64(0),
		}
		period, ok, err := currentIncludedPeriod(item, acct, subs, now)
		if err == nil && ok {
			entry["period_start"] = period.Start.Format(time.RFC3339)
			entry["resets_at"] = period.End.Format(time.RFC3339)
			lots, err := a.includedLots(ctx, pid, acct.ID, item.Unit)
			if err != nil {
				entry["error"] = err.Error()
			} else {
				var total, remaining int64
				for _, l := range periodLots(lots, item, period) {
					total += l.Original
					if l.Status == "active" && (l.ExpiresAt == "" || l.ExpiresAt > nowS) {
						remaining += l.Remaining - l.Held
					}
				}
				if remaining < 0 {
					remaining = 0
				}
				used := total - remaining
				if used < 0 {
					used = 0
				}
				entry["included"], entry["remaining"], entry["used"] = total, remaining, used
			}
		} else if err != nil {
			entry["error"] = err.Error()
		}
		included = append(included, entry)
	}
	out := map[string]any{"included": included}
	if isPrepaidPlan(plan) {
		unit := prepaidSettingsFor(plan).CreditsUnit
		paid := map[string]any{"unit": unit, "available": int64(0)}
		if wallet, err := a.prepaidWallet(ctx, pid, acct.ID, unit); err != nil {
			paid["error"] = err.Error()
		} else if w := unwrapMap(wallet, "wallet"); w != nil {
			paid["available"] = int64Arg(w, "available")
			paid["held"] = int64Arg(w, "held")
		}
		out["paid"] = paid
	}
	return out, nil
}

func (a *App) toolAccountIncludedUsage(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	acct, err := dbAccountGet(ctx.AppDB(), pid, strArg(args, "account_id"))
	if err != nil || acct == nil {
		return nil, firstErr(err, errors.New("account not found"))
	}
	summary, err := a.includedUsageSummary(ctx, pid, acct)
	if err != nil {
		return nil, err
	}
	if summary == nil {
		summary = map[string]any{"included": []map[string]any{}}
	}
	summary["account_id"] = acct.ID
	summary["plan_key"] = acct.PlanKey
	return summary, nil
}
