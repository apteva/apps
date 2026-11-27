package main

import (
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// SaaS v0.14.1: included usage is safe to bill with.

func setupVoiceAccount(t *testing.T, clock *testClock) (*App, *sdk.AppCtx, *includedStub, *Account) {
	t.Helper()
	pricing = &pricingCache{}
	pf := newIncludedStub(clock.now)
	ctx, db := newTestCtx(t, pf)
	t.Cleanup(func() { db.Close() })
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "voice", "name": "Voice", "billing_mode": "prepaid", "metadata": map[string]any{
		"credits_unit": "usd", "included_usage": minutesUsage(60),
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanMeterSet(ctx, map[string]any{"plan_key": "voice", "source_app": "telephony", "meter": "call:minutes", "unit_price_id": 21}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "v@example.com", "slug": "voice-1", "plan_key": "voice"})
	if err != nil {
		t.Fatal(err)
	}
	return app, ctx, pf, asMap(t, created)["account"].(*Account)
}

func settleMinutesReq(t *testing.T, app *App, ctx *sdk.AppCtx, acct *Account, req, ref string, minutes int64) map[string]any {
	t.Helper()
	out, err := app.toolUsageSettle(ctx, map[string]any{
		"request_id": req, "admission_ref": ref, "status": "completed", "source_app": "telephony", "meter": "call:minutes", "quantity": minutes,
		"token_metadata": map[string]any{"account_id": acct.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return asMap(t, out)
}

// 1. A call that runs past its estimate is billed: the allowance covers what
// it has left, the rest is charged at the unit price, nothing is a shortfall.
func TestV0141_OverageBeyondEstimateIsBilled(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupVoiceAccount(t, clock)
	auth := authorizeMinutes(t, app, ctx, acct, "call-1", 5)
	if auth["allow"] != true || auth["included_held"] != int64(5) {
		t.Fatalf("authorize=%v", auth)
	}
	// 60 free + 5,000,000 micro-units at 20,000 per minute = 250 minutes.
	if auth["max_quantity"] != int64(60+250) {
		t.Fatalf("max_quantity=%v", auth["max_quantity"])
	}
	settle := settleMinutesReq(t, app, ctx, acct, "call-1", strFromAny(auth["admission_ref"]), 90)
	if settle["included_used"] != int64(60) || settle["amount"] != int64(30*20_000) {
		t.Fatalf("settle=%v (want 60 included, 30 paid minutes)", settle)
	}
	if pf.available("minutes") != 0 || pf.usdAvail != 5_000_000-600_000 || pf.shortfalls["minutes"] != 0 || pf.shortfalls["usd"] != 0 {
		t.Fatalf("minutes=%d usd=%d shortfalls=%v", pf.available("minutes"), pf.usdAvail, pf.shortfalls)
	}
	// A retried settle charges nothing more.
	settleMinutesReq(t, app, ctx, acct, "call-1", strFromAny(auth["admission_ref"]), 90)
	if pf.usdAvail != 5_000_000-600_000 {
		t.Fatalf("retry charged again: usd=%d", pf.usdAvail)
	}
}

// 1b. On a free plan there is no price past the allowance: max_quantity says
// where to stop and the excess is reported, never drawn as a shortfall.
func TestV0141_FreePlanOverageIsReportedNotShortfall(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupFreeMinutesAccount(t, clock)
	auth := authorizeMinutes(t, app, ctx, acct, "call-1", 5)
	if auth["max_quantity"] != int64(60) {
		t.Fatalf("max_quantity=%v", auth["max_quantity"])
	}
	settle := settleMinutesReq(t, app, ctx, acct, "call-1", strFromAny(auth["admission_ref"]), 90)
	if settle["included_used"] != int64(60) || settle["unbilled_quantity"] != int64(30) {
		t.Fatalf("settle=%v", settle)
	}
	if pf.shortfalls["minutes"] != 0 || pf.available("minutes") != 0 {
		t.Fatalf("shortfalls=%v available=%d", pf.shortfalls, pf.available("minutes"))
	}
}

// 1c. Without estimated_quantity a quantity meter still gets a hold, so the
// usage is tracked and billed.
func TestV0141_NoEstimateIsStillTracked(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupFreeMinutesAccount(t, clock)
	out, err := app.toolUsageAuthorize(ctx, map[string]any{
		"request_id": "call-1", "source_app": "telephony", "meter": "call:minutes",
		"token_metadata": map[string]any{"account_id": acct.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := asMap(t, out)
	if auth["allow"] != true || strFromAny(auth["admission_ref"]) == "" || auth["included_held"] != int64(1) {
		t.Fatalf("authorize=%v", auth)
	}
	settleMinutesReq(t, app, ctx, acct, "call-1", strFromAny(auth["admission_ref"]), 7)
	if pf.available("minutes") != 53 {
		t.Fatalf("available=%d, want 53", pf.available("minutes"))
	}
}

// 2. Quantity holds outlive Credits' 15-minute default, and a hold that
// expired anyway is still billed at settle.
func TestV0141_LongSessionHoldsAndExpiredHoldsAreBilled(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupVoiceAccount(t, clock)
	auth := authorizeMinutes(t, app, ctx, acct, "call-1", 70)
	incHold, _, moneyHold, ok := decodeAdmissionRef(strFromAny(auth["admission_ref"]))
	if !ok || incHold == 0 || moneyHold == 0 {
		t.Fatalf("admission_ref=%v", auth["admission_ref"])
	}
	if pf.holds[incHold].ttl != quantityHoldSeconds || pf.holds[moneyHold].ttl != quantityHoldSeconds {
		t.Fatalf("ttl included=%d paid=%d, want %d", pf.holds[incHold].ttl, pf.holds[moneyHold].ttl, quantityHoldSeconds)
	}
	pf.expireHold(incHold)
	pf.expireHold(moneyHold)
	settle := settleMinutesReq(t, app, ctx, acct, "call-1", strFromAny(auth["admission_ref"]), 90)
	if settle["included_used"] != int64(60) || settle["amount"] != int64(30*20_000) || settle["hold_expired"] != true {
		t.Fatalf("settle=%v", settle)
	}
	if pf.available("minutes") != 0 || pf.usdAvail != 5_000_000-600_000 {
		t.Fatalf("minutes=%d usd=%d", pf.available("minutes"), pf.usdAvail)
	}
	// Retry: same holds by reference, commits replay.
	settleMinutesReq(t, app, ctx, acct, "call-1", strFromAny(auth["admission_ref"]), 90)
	if pf.usdAvail != 5_000_000-600_000 {
		t.Fatalf("retry charged again: usd=%d", pf.usdAvail)
	}
	// A failed request whose hold expired releases cleanly.
	auth = authorizeMinutes(t, app, ctx, acct, "call-2", 5)
	_, _, moneyHold, _ = decodeAdmissionRef(strFromAny(auth["admission_ref"]))
	pf.expireHold(moneyHold)
	if _, err := app.toolUsageSettle(ctx, map[string]any{"admission_ref": auth["admission_ref"], "status": "failed"}); err != nil {
		t.Fatalf("release of an expired hold: %v", err)
	}
}

// 3. Moving from a monthly free allowance to a billing-cycle plan counts the
// free minutes still left toward the new amount instead of stacking both.
func TestV0141_MonthToBillingCyclePlanChangeDoesNotStack(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupFreeMinutesAccount(t, clock)
	auth := authorizeMinutes(t, app, ctx, acct, "call-1", 20)
	settleMinutesReq(t, app, ctx, acct, "call-1", strFromAny(auth["admission_ref"]), 20)

	pro := []any{map[string]any{"source_app": "telephony", "meter": "call:minutes", "amount": 600, "unit": "minutes", "period": "billing_cycle"}}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "pro", "name": "Pro", "subscription_required": true, "metadata": map[string]any{"included_usage": pro}}); err != nil {
		t.Fatal(err)
	}
	pf.subPeriod = [2]string{"2026-11-20T00:00:00Z", "2026-12-20T00:00:00Z"}
	if _, err := ctx.AppDB().Exec(`UPDATE saas_accounts SET plan_key='pro', subscription_id=77 WHERE id=?`, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ensureIncludedUsage(ctx, "proj-test", acct.ID); err != nil {
		t.Fatal(err)
	}
	if got := pf.available("minutes"); got != 600 {
		t.Fatalf("available=%d, want 600 (40 free left + 560 pro), not 640", got)
	}
	// Spending the carried minutes and checking again grants nothing more.
	auth = authorizeMinutes(t, app, ctx, acct, "call-2", 40)
	settleMinutesReq(t, app, ctx, acct, "call-2", strFromAny(auth["admission_ref"]), 40)
	if _, err := app.ensureIncludedUsage(ctx, "proj-test", acct.ID); err != nil {
		t.Fatal(err)
	}
	if got := pf.available("minutes"); got != 560 {
		t.Fatalf("available=%d after spending carried minutes, want 560", got)
	}
	e := includedEntry(t, app, ctx, acct)
	if e["included"] != int64(600) || e["remaining"] != int64(560) || e["carried_over"] != int64(40) {
		t.Fatalf("summary=%v", e)
	}
}

// 3b. Raising a plan's amount mid-period grants the extra once.
func TestV0141_RaisingThePlanAmountGrantsTheExtra(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, _ := setupFreeMinutesAccount(t, clock)
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "free", "name": "Free", "metadata": map[string]any{"included_usage": minutesUsage(90)}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := app.runIncludedUsage(ctx); err != nil {
			t.Fatal(err)
		}
	}
	lots := pf.lots["minutes"]
	if len(lots) != 2 || lots[1].Original != 30 || !strings.HasSuffix(lots[1].SourceRef, ":upto:90") || pf.available("minutes") != 90 {
		t.Fatalf("lots=%d available=%d", len(lots), pf.available("minutes"))
	}
	// Saving the plan unchanged does not recheck anyone.
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "free", "name": "Free renamed", "metadata": map[string]any{"included_usage": minutesUsage(90)}}); err != nil {
		t.Fatal(err)
	}
	due, err := dbAccountsDueIncludedUsage(ctx.AppDB(), "proj-test", clock.t, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("due=%d err=%v", len(due), err)
	}
}

// 4. At the period boundary the first request grants the new period itself;
// it does not wait for the worker.
func TestV0141_NoGapAtPeriodStart(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupFreeMinutesAccount(t, clock)
	clock.t = time.Date(2026, 12, 1, 0, 0, 30, 0, time.UTC)
	auth := authorizeMinutes(t, app, ctx, acct, "call-1", 5)
	if auth["allow"] != true || auth["included_held"] != int64(5) {
		t.Fatalf("authorize at period start=%v", auth)
	}
	if n := len(pf.lots["minutes"]); n != 2 {
		t.Fatalf("lots=%d, want November + December", n)
	}
	// The access check also sees the new period without the worker.
	clock.t = time.Date(2027, 1, 1, 0, 0, 10, 0, time.UTC)
	e := includedEntry(t, app, ctx, acct)
	if e["remaining"] != int64(60) || e["period_start"] != "2027-01-01T00:00:00Z" {
		t.Fatalf("January summary=%v", e)
	}
}

// 5. Accounts that keep missing (a lapsed billing cycle) back off and go to
// the back of the queue, and one pass works through every due account.
func TestV0141_SchedulerBacksOffAndDrainsAllDueAccounts(t *testing.T) {
	if d := includedBackoff(1); d != 10*time.Minute {
		t.Fatalf("backoff(1)=%v", d)
	}
	if d := includedBackoff(3); d != 40*time.Minute {
		t.Fatalf("backoff(3)=%v", d)
	}
	if d := includedBackoff(20); d != includedMaxBackoff {
		t.Fatalf("backoff(20)=%v", d)
	}

	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupFreeMinutesAccount(t, clock)
	pro := []any{map[string]any{"source_app": "telephony", "meter": "call:minutes", "amount": 600, "unit": "minutes", "period": "billing_cycle"}}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "pro", "name": "Pro", "subscription_required": true, "metadata": map[string]any{"included_usage": pro}}); err != nil {
		t.Fatal(err)
	}
	pf.subPeriod = [2]string{"2026-10-15T00:00:00Z", "2026-11-15T00:00:00Z"} // lapsed
	if _, err := ctx.AppDB().Exec(`UPDATE saas_accounts SET plan_key='pro', subscription_id=77 WHERE id=?`, acct.ID); err != nil {
		t.Fatal(err)
	}
	var next []time.Time
	for i := 0; i < 3; i++ {
		if _, err := app.ensureIncludedUsage(ctx, "proj-test", acct.ID); err != nil {
			t.Fatal(err)
		}
		fresh, _ := dbAccountGet(ctx.AppDB(), "proj-test", acct.ID)
		next = append(next, includedNextCheck(fresh))
	}
	if next[0].Sub(clock.t) != 10*time.Minute || next[2].Sub(clock.t) != 40*time.Minute {
		t.Fatalf("lapsed rechecks at %v", next)
	}

	// 150 accounts due at once are all granted in one worker pass.
	for i := 0; i < 150; i++ {
		if _, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "u@example.com", "slug": "bulk-" + strings.Repeat("x", 1) + string(rune('a'+i%26)) + itoa(i), "plan_key": "free"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ctx.AppDB().Exec(`UPDATE saas_accounts SET metadata_json=json_remove(metadata_json, '$.included_usage_state') WHERE plan_key='free'`); err != nil {
		t.Fatal(err)
	}
	clock.t = clock.t.Add(time.Hour)
	if err := app.runIncludedUsage(ctx); err != nil {
		t.Fatal(err)
	}
	due, err := dbAccountsDueIncludedUsage(ctx.AppDB(), "proj-test", clock.t, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range due {
		if a.PlanKey == "free" {
			t.Fatalf("free account %s still due after one pass", a.ID)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
