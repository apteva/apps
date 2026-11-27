package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Credits model for included usage ──────────────────────────────────

type stubLot struct {
	ID        int64          `json:"id"`
	Source    string         `json:"source"`
	SourceRef string         `json:"source_ref"`
	Original  int64          `json:"original"`
	Remaining int64          `json:"remaining"`
	Held      int64          `json:"held"`
	Priority  int64          `json:"priority"`
	ExpiresAt string         `json:"expires_at,omitempty"`
	Status    string         `json:"status"`
	Metadata  map[string]any `json:"metadata"`
}

type stubHold struct {
	unit      string
	amount    int64
	allocs    map[int64]int64 // lot id -> units held
	status    string
	committed int64
}

// includedStub models Credits lots per wallet unit: grants dedupe by
// source_ref, lots expire at expires_at, reserves hold from active lots
// and commits consume them. The paid (usd) wallet is a plain balance.
type includedStub struct {
	*platformStub
	now         func() time.Time
	lots        map[string][]*stubLot // unit -> lots
	nextLot     int64
	holds       map[int64]*stubHold
	nextHold    int64
	usdAvail    int64
	subPeriod   [2]string
	grantErrors int
}

func newIncludedStub(now func() time.Time) *includedStub {
	return &includedStub{
		platformStub: &platformStub{entitled: true},
		now:          now, lots: map[string][]*stubLot{}, holds: map[int64]*stubHold{},
		nextLot: 100, nextHold: 500, usdAvail: 5_000_000,
	}
}

func (p *includedStub) active(l *stubLot) bool {
	if l.Status != "active" {
		return false
	}
	if l.ExpiresAt != "" {
		exp, _ := time.Parse(time.RFC3339, l.ExpiresAt)
		if !p.now().Before(exp) {
			return false
		}
	}
	return true
}

func (p *includedStub) available(unit string) int64 {
	if unit == "usd" {
		return p.usdAvail
	}
	var n int64
	for _, l := range p.lots[unit] {
		if p.active(l) {
			n += l.Remaining - l.Held
		}
	}
	return n
}

func (p *includedStub) listLots(unit string) []*stubLot {
	out := []*stubLot{}
	for _, l := range p.lots[unit] {
		c := *l
		if c.Status == "active" && !p.active(l) {
			c.Status = "expired"
		}
		out = append(out, &c)
	}
	return out
}

func (p *includedStub) CallAppResult(appName, tool string, input map[string]any, out any) error {
	var body any
	unit := strFromAny(input["unit"])
	switch appName + ":" + tool {
	case "catalog:catalog_prices_get":
		body = map[string]any{"price": includedPrices[int64FromAny(input["id"])]}
	case "credits:credits_wallet_ensure":
		if _, ok := p.lots[unit]; !ok {
			p.lots[unit] = nil
		}
		body = map[string]any{"wallet": map[string]any{"id": 1, "unit": unit}}
	case "credits:credits_wallet_get":
		if _, ok := p.lots[unit]; !ok && unit != "usd" {
			body = map[string]any{"wallet": nil}
			break
		}
		body = map[string]any{"wallet": map[string]any{"id": 1, "unit": unit, "available": p.available(unit), "status": "active"}}
	case "credits:credits_grant":
		if p.grantErrors > 0 {
			p.grantErrors--
			return fmt.Errorf("injected grant failure")
		}
		ref := strFromAny(input["source_ref"])
		for _, l := range p.lots[unit] {
			if ref != "" && l.SourceRef == ref {
				body = map[string]any{"lot": l, "deduped": true}
				break
			}
		}
		if body != nil {
			break
		}
		p.nextLot++
		l := &stubLot{ID: p.nextLot, Source: strFromAny(input["source"]), SourceRef: ref, Original: int64FromAny(input["amount"]),
			Remaining: int64FromAny(input["amount"]), Priority: int64FromAny(input["priority"]), ExpiresAt: strFromAny(input["expires_at"]),
			Status: "active", Metadata: mapFromAny(input["metadata"])}
		p.lots[unit] = append(p.lots[unit], l)
		body = map[string]any{"lot": l, "deduped": false}
	case "credits:credits_lots_list":
		body = map[string]any{"lots": p.listLots(unit)}
	case "credits:credits_reserve":
		amount := int64FromAny(input["amount"])
		if amount > p.available(unit) {
			body = map[string]any{"denied": true, "reason": "insufficient_balance"}
			break
		}
		p.nextHold++
		h := &stubHold{unit: unit, amount: amount, allocs: map[int64]int64{}, status: "held"}
		if unit != "usd" {
			left := amount
			for _, l := range p.lots[unit] {
				if left == 0 || !p.active(l) {
					continue
				}
				take := l.Remaining - l.Held
				if take > left {
					take = left
				}
				l.Held += take
				h.allocs[l.ID] += take
				left -= take
			}
		} else {
			p.usdAvail -= amount
		}
		p.holds[p.nextHold] = h
		body = map[string]any{"hold_id": p.nextHold, "available_after": p.available(unit)}
	case "credits:credits_commit":
		h := p.holds[int64FromAny(input["hold_id"])]
		amount := h.amount
		if v, ok := input["amount"]; ok {
			amount = int64FromAny(v)
		}
		if h.status == "committed" {
			body = map[string]any{"committed": h.committed, "replayed": true}
			break
		}
		p.settleHold(h, amount)
		h.status, h.committed = "committed", amount
		body = map[string]any{"committed": amount}
	case "credits:credits_release":
		h := p.holds[int64FromAny(input["hold_id"])]
		if h.status == "held" {
			p.settleHold(h, 0)
			h.status = "released"
		}
		body = map[string]any{"released": true}
	case "subscriptions:subscriptions_get":
		body = map[string]any{"subscription": map[string]any{"id": input["id"], "current_period_start": p.subPeriod[0], "current_period_end": p.subPeriod[1]}}
	default:
		return p.platformStub.CallAppResult(appName, tool, input, out)
	}
	p.platformStub.mu.Lock()
	p.platformStub.calls = append(p.platformStub.calls, callAppCall{App: appName, Tool: tool, Input: input})
	p.platformStub.mu.Unlock()
	raw, _ := json.Marshal(body)
	return json.Unmarshal(raw, out)
}

// settleHold unholds the hold's lots and consumes amount from them; an
// overrun is taken from other active lots.
func (p *includedStub) settleHold(h *stubHold, amount int64) {
	if h.unit == "usd" {
		p.usdAvail += h.amount - amount
		return
	}
	left := amount
	for _, l := range p.lots[h.unit] {
		held := h.allocs[l.ID]
		if held == 0 {
			continue
		}
		l.Held -= held
		use := held
		if use > left {
			use = left
		}
		l.Remaining -= use
		left -= use
	}
	for _, l := range p.lots[h.unit] {
		if left == 0 {
			break
		}
		if !p.active(l) {
			continue
		}
		use := l.Remaining - l.Held
		if use > left {
			use = left
		}
		l.Remaining -= use
		left -= use
	}
}

var includedPrices = map[int64]map[string]any{
	// 2 cents per minute.
	21: {"id": 21, "unit_amount_cents": 2, "unit_size": 1, "currency": "USD", "billing_scheme": "metered", "active": true},
}

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

func useClock(t *testing.T, start time.Time) *testClock {
	t.Helper()
	c := &testClock{t: start}
	prev := includedNow
	includedNow = c.now
	t.Cleanup(func() { includedNow = prev })
	return c
}

func minutesUsage(amount int64) []any {
	return []any{map[string]any{"source_app": "telephony", "meter": "call:minutes", "amount": amount, "unit": "minutes", "period": "month"}}
}

func setupFreeMinutesAccount(t *testing.T, clock *testClock) (*App, *sdk.AppCtx, *includedStub, *Account) {
	t.Helper()
	pricing = &pricingCache{}
	pf := newIncludedStub(clock.now)
	ctx, db := newTestCtx(t, pf)
	t.Cleanup(func() { db.Close() })
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "free", "name": "Free", "metadata": map[string]any{"included_usage": minutesUsage(60)}}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "free@example.com", "slug": "free-1", "plan_key": "free"})
	if err != nil {
		t.Fatal(err)
	}
	return app, ctx, pf, asMap(t, created)["account"].(*Account)
}

func includedEntry(t *testing.T, app *App, ctx *sdk.AppCtx, acct *Account) map[string]any {
	t.Helper()
	out, err := app.toolAccountIncludedUsage(ctx, map[string]any{"account_id": acct.ID})
	if err != nil {
		t.Fatal(err)
	}
	list := asMap(t, out)["included"].([]map[string]any)
	if len(list) != 1 {
		t.Fatalf("included entries=%d", len(list))
	}
	return list[0]
}

func authorizeMinutes(t *testing.T, app *App, ctx *sdk.AppCtx, acct *Account, req string, minutes int64) map[string]any {
	t.Helper()
	out, err := app.toolUsageAuthorize(ctx, map[string]any{
		"request_id": req, "source_app": "telephony", "meter": "call:minutes", "estimated_quantity": minutes,
		"token_metadata": map[string]any{"account_id": acct.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return asMap(t, out)
}

func settleMinutes(t *testing.T, app *App, ctx *sdk.AppCtx, acct *Account, ref string, minutes int64) map[string]any {
	t.Helper()
	out, err := app.toolUsageSettle(ctx, map[string]any{
		"admission_ref": ref, "status": "completed", "source_app": "telephony", "meter": "call:minutes", "quantity": minutes,
		"token_metadata": map[string]any{"account_id": acct.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return asMap(t, out)
}

// ─── Done-when scenarios ───────────────────────────────────────────────

func TestIncludedUsage_NewFreeAccountHas60Minutes(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupFreeMinutesAccount(t, clock)
	lots := pf.lots["minutes"]
	if len(lots) != 1 {
		t.Fatalf("lots=%d, want 1", len(lots))
	}
	lot := lots[0]
	if lot.Original != 60 || lot.ExpiresAt != "2026-12-01T00:00:00Z" || lot.Priority != includedLotPriority {
		t.Fatalf("lot=%+v", lot)
	}
	wantRef := "saas:included:acct:" + acct.ID + ":meter:telephony/call:minutes:period:2026-11-01T00:00:00Z:plan:free"
	if lot.SourceRef != wantRef {
		t.Fatalf("source_ref=%s, want %s", lot.SourceRef, wantRef)
	}
	e := includedEntry(t, app, ctx, acct)
	if e["included"] != int64(60) || e["remaining"] != int64(60) || e["used"] != int64(0) || e["resets_at"] != "2026-12-01T00:00:00Z" {
		t.Fatalf("summary=%v", e)
	}
}

func TestIncludedUsage_RetryNeverDoubles(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupFreeMinutesAccount(t, clock)
	for i := 0; i < 3; i++ {
		if _, err := app.ensureIncludedUsage(ctx, "proj-test", acct.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Force the worker to look again too.
	if _, err := ctx.AppDB().Exec(`UPDATE saas_accounts SET metadata_json=json_remove(metadata_json, '$.included_usage_state')`); err != nil {
		t.Fatal(err)
	}
	if err := app.runIncludedUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(pf.lots["minutes"]); n != 1 {
		t.Fatalf("lots=%d after retries, want 1", n)
	}
	if e := includedEntry(t, app, ctx, acct); e["included"] != int64(60) {
		t.Fatalf("included=%v", e["included"])
	}
}

func TestIncludedUsage_UseSpendsIncludedFirstAndRefusesWhenUsedUp(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupFreeMinutesAccount(t, clock)
	auth := authorizeMinutes(t, app, ctx, acct, "call-1", 20)
	if auth["allow"] != true || !strings.HasPrefix(strFromAny(auth["admission_ref"]), "inc:") || auth["included_remaining"] != int64(40) {
		t.Fatalf("authorize=%v", auth)
	}
	settle := settleMinutes(t, app, ctx, acct, strFromAny(auth["admission_ref"]), 18)
	if settle["included_used"] != int64(18) {
		t.Fatalf("settle=%v", settle)
	}
	if e := includedEntry(t, app, ctx, acct); e["remaining"] != int64(42) || e["used"] != int64(18) {
		t.Fatalf("after 18 minutes: %v", e)
	}
	// A free plan has no paid price: more than what is left is refused, and
	// nothing stays held.
	deny := authorizeMinutes(t, app, ctx, acct, "call-2", 50)
	if deny["allow"] != false || deny["reason"] != "included_exhausted" {
		t.Fatalf("over-allowance authorize=%v", deny)
	}
	if got := pf.available("minutes"); got != 42 {
		t.Fatalf("available after refusal=%d, want 42 (hold released)", got)
	}
	// A failed call releases the allowance.
	auth = authorizeMinutes(t, app, ctx, acct, "call-3", 10)
	if _, err := app.toolUsageSettle(ctx, map[string]any{"admission_ref": auth["admission_ref"], "status": "failed", "token_metadata": map[string]any{"account_id": acct.ID}}); err != nil {
		t.Fatal(err)
	}
	if got := pf.available("minutes"); got != 42 {
		t.Fatalf("available after failed call=%d, want 42", got)
	}
}

func TestIncludedUsage_ExpiresAtPeriodEndAndFreshAllowanceAppears(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupFreeMinutesAccount(t, clock)
	auth := authorizeMinutes(t, app, ctx, acct, "call-1", 30)
	settleMinutes(t, app, ctx, acct, strFromAny(auth["admission_ref"]), 30)

	clock.t = time.Date(2026, 12, 1, 0, 5, 0, 0, time.UTC)
	if got := pf.available("minutes"); got != 0 {
		t.Fatalf("November minutes still spendable after period end: %d", got)
	}
	if err := app.runIncludedUsage(ctx); err != nil {
		t.Fatal(err)
	}
	e := includedEntry(t, app, ctx, acct)
	if e["included"] != int64(60) || e["remaining"] != int64(60) || e["resets_at"] != "2027-01-01T00:00:00Z" || e["period_start"] != "2026-12-01T00:00:00Z" {
		t.Fatalf("December summary=%v", e)
	}
	if n := len(pf.lots["minutes"]); n != 2 {
		t.Fatalf("lots=%d, want 2 (November expired + December)", n)
	}
	// The worker does not look again before the period ends.
	if err := app.runIncludedUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(pf.lots["minutes"]); n != 2 {
		t.Fatalf("lots=%d after a second sweep, want 2", n)
	}
}

func TestIncludedUsage_FailedGrantIsRetriedByWorker(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	pricing = &pricingCache{}
	pf := newIncludedStub(clock.now)
	pf.grantErrors = 1
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "free", "name": "Free", "metadata": map[string]any{"included_usage": minutesUsage(60)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "free@example.com", "slug": "free-1", "plan_key": "free"}); err != nil {
		t.Fatalf("a failed grant must not fail account creation: %v", err)
	}
	if len(pf.lots["minutes"]) != 0 {
		t.Fatal("grant should have failed once")
	}
	clock.t = clock.t.Add(includedRecheckInterval + time.Minute)
	if err := app.runIncludedUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pf.lots["minutes"]) != 1 {
		t.Fatalf("worker did not retry the grant: lots=%d", len(pf.lots["minutes"]))
	}
}

// ─── Plan changes ──────────────────────────────────────────────────────

func TestIncludedUsage_PlanChangeGrantsOnlyTheDifference(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	app, ctx, pf, acct := setupFreeMinutesAccount(t, clock)
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "free-plus", "name": "Free Plus", "metadata": map[string]any{"included_usage": minutesUsage(100)}}); err != nil {
		t.Fatal(err)
	}
	// Spend some first: used credit is never taken back.
	auth := authorizeMinutes(t, app, ctx, acct, "call-1", 50)
	settleMinutes(t, app, ctx, acct, strFromAny(auth["admission_ref"]), 50)

	if _, err := ctx.AppDB().Exec(`UPDATE saas_accounts SET plan_key='free-plus' WHERE id=?`, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ensureIncludedUsage(ctx, "proj-test", acct.ID); err != nil {
		t.Fatal(err)
	}
	lots := pf.lots["minutes"]
	if len(lots) != 2 || lots[1].Original != 40 || !strings.HasSuffix(lots[1].SourceRef, ":plan:free-plus") {
		t.Fatalf("upgrade lots=%+v", lots)
	}
	e := includedEntry(t, app, ctx, acct)
	if e["included"] != int64(100) || e["remaining"] != int64(50) || e["used"] != int64(50) {
		t.Fatalf("after upgrade: %v", e)
	}
	// Downgrade: nothing granted, nothing removed.
	if _, err := ctx.AppDB().Exec(`UPDATE saas_accounts SET plan_key='free' WHERE id=?`, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ensureIncludedUsage(ctx, "proj-test", acct.ID); err != nil {
		t.Fatal(err)
	}
	if len(pf.lots["minutes"]) != 2 || pf.available("minutes") != 50 {
		t.Fatalf("downgrade changed lots: n=%d available=%d", len(pf.lots["minutes"]), pf.available("minutes"))
	}
}

// ─── Prepaid plan: included first, then paid ───────────────────────────

func TestIncludedUsage_PrepaidOverflowGoesToPaidWallet(t *testing.T) {
	clock := useClock(t, time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	pricing = &pricingCache{}
	pf := newIncludedStub(clock.now)
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
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
	acct := asMap(t, created)["account"].(*Account)

	auth := authorizeMinutes(t, app, ctx, acct, "call-1", 70)
	// 60 minutes from the allowance, 10 minutes at 2 cents = 20 cents = 200000 micro-units.
	if auth["allow"] != true || auth["included_held"] != int64(60) || auth["amount"] != int64(200_000) {
		t.Fatalf("authorize=%v", auth)
	}
	incHold, incTaken, moneyHold, ok := decodeAdmissionRef(strFromAny(auth["admission_ref"]))
	if !ok || incHold == 0 || incTaken != 60 || moneyHold == 0 {
		t.Fatalf("admission_ref=%v", auth["admission_ref"])
	}
	settle := settleMinutes(t, app, ctx, acct, strFromAny(auth["admission_ref"]), 65)
	if settle["included_used"] != int64(60) || settle["amount"] != int64(100_000) {
		t.Fatalf("settle=%v (want 60 included, 5 paid minutes = 100000)", settle)
	}
	if pf.available("minutes") != 0 || pf.usdAvail != 5_000_000-100_000 {
		t.Fatalf("minutes=%d usd=%d", pf.available("minutes"), pf.usdAvail)
	}
	// Short call fully inside the allowance: the paid hold commits nothing.
	clock.t = time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	if err := app.runIncludedUsage(ctx); err != nil {
		t.Fatal(err)
	}
	auth = authorizeMinutes(t, app, ctx, acct, "call-2", 5)
	if auth["amount"] != int64(0) || strings.Count(strFromAny(auth["admission_ref"]), ":") != 3 {
		t.Fatalf("in-allowance authorize=%v", auth)
	}
	if _, _, moneyHold, _ := decodeAdmissionRef(strFromAny(auth["admission_ref"])); moneyHold != 0 {
		t.Fatalf("no paid hold expected, got %d", moneyHold)
	}
	out, err := app.toolAccessCheck(ctx, map[string]any{"account_id": acct.ID, "feature_key": "voice:access"})
	if err != nil {
		t.Fatal(err)
	}
	if inc, ok := asMap(t, out)["included_usage"]; !ok || inc == nil {
		t.Fatalf("access check lacks included_usage: %v", out)
	}
}

func TestIncludedUsage_LegacyAdmissionRefStillSettles(t *testing.T) {
	if inc, taken, money, ok := decodeAdmissionRef("9001"); !ok || inc != 0 || taken != 0 || money != 9001 {
		t.Fatalf("legacy ref decode: %d %d %d %v", inc, taken, money, ok)
	}
	for _, bad := range []string{"", "abc", "inc:0:5:0", "inc:1:2", "inc:1:x:0", "-4"} {
		if _, _, _, ok := decodeAdmissionRef(bad); ok {
			t.Errorf("%q should not decode", bad)
		}
	}
	if encodeAdmissionRef(0, 0, 42) != "42" || encodeAdmissionRef(7, 60, 0) != "inc:7:60:0" {
		t.Fatal("encode")
	}
}

// ─── Billing cycle and validation ──────────────────────────────────────

func TestIncludedUsage_BillingCyclePeriodFromSubscription(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	pf := newIncludedStub(func() time.Time { return now })
	ctx, db := newTestCtx(t, pf)
	defer db.Close()
	pf.subPeriod = [2]string{"2026-11-15T00:00:00Z", "2026-12-15T00:00:00Z"}
	sub := int64(77)
	acct := &Account{ID: "acct_x", SubscriptionID: &sub}
	item := includedUsageItem{SourceApp: "telephony", Meter: "call:minutes", Amount: 60, Unit: "minutes", Period: includedPeriodBillingCycle}
	period, ok, err := currentIncludedPeriod(item, acct, &subscriptionPeriodReader{ctx: ctx, pid: "proj-test"}, now)
	if err != nil || !ok || period.Start.Format(time.RFC3339) != "2026-11-15T00:00:00Z" || period.End.Format(time.RFC3339) != "2026-12-15T00:00:00Z" {
		t.Fatalf("period=%+v ok=%v err=%v", period, ok, err)
	}
	// A lapsed cycle grants nothing until the subscription renews.
	pf.subPeriod = [2]string{"2026-10-15T00:00:00Z", "2026-11-15T00:00:00Z"}
	if _, ok, _ := currentIncludedPeriod(item, acct, &subscriptionPeriodReader{ctx: ctx, pid: "proj-test"}, now); ok {
		t.Fatal("lapsed cycle should not be current")
	}
	// No subscription: calendar month.
	period, ok, _ = currentIncludedPeriod(item, &Account{ID: "acct_y"}, &subscriptionPeriodReader{ctx: ctx, pid: "proj-test"}, now)
	if !ok || period.Source != includedPeriodMonth || period.Start.Format(time.RFC3339) != "2026-11-01T00:00:00Z" {
		t.Fatalf("fallback period=%+v", period)
	}
}

func TestIncludedUsage_Validation(t *testing.T) {
	bad := []struct {
		meta map[string]any
		mode string
		want string
	}{
		{map[string]any{"included_usage": "60 minutes"}, "free", "must be a list"},
		{map[string]any{"included_usage": []any{map[string]any{"amount": 60, "unit": "minutes"}}}, "free", "meter required"},
		{map[string]any{"included_usage": []any{map[string]any{"meter": "call:minutes", "amount": 0, "unit": "minutes"}}}, "free", "amount"},
		{map[string]any{"included_usage": []any{map[string]any{"meter": "call:minutes", "amount": 60}}}, "free", "unit required"},
		{map[string]any{"credits_unit": "usd", "included_usage": []any{map[string]any{"meter": "m", "amount": 1, "unit": "usd"}}}, "prepaid", "must differ"},
		{map[string]any{"included_usage": []any{map[string]any{"meter": "a", "amount": 1, "unit": "minutes"}, map[string]any{"meter": "b", "amount": 1, "unit": "minutes"}}}, "free", "used twice"},
		{map[string]any{"included_usage": []any{map[string]any{"meter": "a", "amount": 1, "unit": "x"}, map[string]any{"meter": "a", "amount": 1, "unit": "y"}}}, "free", "listed twice"},
		{map[string]any{"included_usage": []any{map[string]any{"meter": "a", "amount": 1, "unit": "x", "period": "billing_cycle"}}}, "free", "needs a paid subscription"},
		{map[string]any{"included_usage": []any{map[string]any{"meter": "a", "amount": 1, "unit": "x", "period": "week"}}}, "free", "month or billing_cycle"},
		{map[string]any{"included_usage": []any{map[string]any{"meter": "a", "amount": 1, "unit": "x", "expires": "never"}}}, "free", "period_end"},
	}
	for _, c := range bad {
		err := validateIncludedUsage(c.meta, c.mode, false)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("meta %v: err=%v, want %q", c.meta, err, c.want)
		}
	}
	ok := map[string]any{"included_usage": []any{map[string]any{"meter": "a", "amount": 1, "unit": "x", "period": "billing_cycle", "expires": "period_end"}}}
	if err := validateIncludedUsage(ok, "paid", true); err != nil {
		t.Fatal(err)
	}
	items := []includedUsageItem{{SourceApp: "telephony", Meter: "call:*", Unit: "m", Amount: 1}}
	if matchIncludedUsage(items, "telephony", "call:minutes") == nil || matchIncludedUsage(items, "llm", "call:minutes") != nil {
		t.Fatal("wildcard match")
	}
}
