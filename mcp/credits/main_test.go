package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

const pid = "proj_test"

// ─── Fixtures ────────────────────────────────────────────────────────

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type billingCall struct {
	Tool string
	Args map[string]any
}

// fakeBilling stands in for the billing app over CallAppResult.
type fakeBilling struct {
	testkit.BasePlatformClient
	mu        sync.Mutex
	calls     []billingCall
	nextID    int64
	failTools map[string]error
}

func (f *fakeBilling) CallAppResult(app, tool string, input map[string]any, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app != "billing" {
		return fmt.Errorf("unexpected app %s", app)
	}
	f.calls = append(f.calls, billingCall{Tool: tool, Args: input})
	if err := f.failTools[tool]; err != nil {
		return err
	}
	var resp any
	switch tool {
	case "invoices_create_from_prepared_lines":
		f.nextID++
		resp = map[string]any{"invoice": map[string]any{"id": 800 + f.nextID, "status": "open"}}
	case "invoices_create_payment_session":
		resp = map[string]any{"url": "https://pay.example/s/1", "presentation": input["presentation"]}
	case "invoices_collect":
		resp = map[string]any{"collection_attempt": map[string]any{"status": "processing"}}
	default:
		return fmt.Errorf("unexpected billing tool %s", tool)
	}
	raw, _ := json.Marshal(resp)
	return json.Unmarshal(raw, out)
}

func (f *fakeBilling) toolCalls(name string) []billingCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []billingCall
	for _, c := range f.calls {
		if c.Tool == name {
			out = append(out, c)
		}
	}
	return out
}

type harness struct {
	t       *testing.T
	ctx     *sdk.AppCtx
	app     *App
	rec     *testkit.EmitRecorder
	billing *fakeBilling
	clock   *clock
	tools   map[string]sdk.ToolHandler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)}
	prevNow, prevAsync := nowFunc, runAsync
	nowFunc = c.now
	runAsync = func(f func()) { f() }
	t.Cleanup(func() { nowFunc, runAsync = prevNow, prevAsync })

	rec := testkit.NewEmitRecorder()
	fb := &fakeBilling{failTools: map[string]error{}}
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID(pid), testkit.WithEmitter(rec), testkit.WithPlatform(fb))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: ctx, app: app, rec: rec, billing: fb, clock: c, tools: map[string]sdk.ToolHandler{}}
	for _, tool := range app.MCPTools() {
		h.tools[tool.Name] = tool.Handler
	}
	t.Cleanup(h.assertReconciled)
	return h
}

// call runs a tool and round-trips the result through JSON, as a caller sees it.
func (h *harness) call(name string, args map[string]any) map[string]any {
	h.t.Helper()
	out, err := h.try(name, args)
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	return out
}

func (h *harness) try(name string, args map[string]any) (map[string]any, error) {
	h.t.Helper()
	handler, ok := h.tools[name]
	if !ok {
		h.t.Fatalf("no tool %s", name)
	}
	cp := map[string]any{}
	for k, v := range args {
		cp[k] = v
	}
	res, err := handler(h.ctx, cp)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(res)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, nil
}

func (h *harness) assertReconciled() {
	h.t.Helper()
	out := h.call("credits_reconcile", nil)
	if out["ok"] != true {
		h.t.Fatalf("reconcile failed: %v", out["issues"])
	}
}

func (h *harness) wallet(subject, unit string) map[string]any {
	h.t.Helper()
	out := h.call("credits_wallet_get", map[string]any{"subject_type": "saas_account", "subject_id": subject, "unit": unit})
	w, _ := out["wallet"].(map[string]any)
	return w
}

func (h *harness) balance(subject string) (int64, int64) {
	h.t.Helper()
	w := h.wallet(subject, "usd")
	if w == nil {
		return 0, 0
	}
	return num(w["available"]), num(w["held"])
}

func (h *harness) events(topic string) []map[string]any {
	var out []map[string]any
	for _, ev := range h.rec.EventsByTopic(topic) {
		raw, _ := json.Marshal(ev.Data)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		out = append(out, m)
	}
	return out
}

func (h *harness) entriesCount(walletID int64) int64 {
	var n int64
	if err := h.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM credit_entries WHERE wallet_id=?`, walletID).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func num(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	}
	return 0
}

func acct(subject string, extra map[string]any) map[string]any {
	out := map[string]any{"subject_type": "saas_account", "subject_id": subject, "unit": "usd"}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

const usd = int64(1_000_000)

// ─── Manifest ────────────────────────────────────────────────────────

func TestEmbeddedManifest_MatchesFile(t *testing.T) {
	file, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fromFile, err := sdk.ParseManifest(file)
	if err != nil {
		t.Fatalf("apteva.yaml: %v", err)
	}
	embedded := (&App{}).Manifest()
	if embedded.Name != fromFile.Name || embedded.Version != fromFile.Version || fromFile.Version != appVersion {
		t.Fatalf("name/version mismatch: embedded %s@%s, file %s@%s, const %s", embedded.Name, embedded.Version, fromFile.Name, fromFile.Version, appVersion)
	}
	declared := map[string]bool{}
	for _, tl := range fromFile.Provides.MCPTools {
		declared[tl.Name] = true
	}
	for _, tl := range (&App{}).MCPTools() {
		if !declared[tl.Name] {
			t.Errorf("tool %s missing from apteva.yaml", tl.Name)
		}
		delete(declared, tl.Name)
		if tl.InputSchema["type"] != "object" {
			t.Errorf("tool %s has no object schema", tl.Name)
		}
	}
	for name := range declared {
		t.Errorf("apteva.yaml declares %s but the app does not register it", name)
	}
}

// ─── Grants & spend order ────────────────────────────────────────────

func TestGrant_IdempotentBySourceRef(t *testing.T) {
	h := newHarness(t)
	args := acct("acct_1", map[string]any{"source": "promo", "source_ref": "saas:signup:acct_1", "amount": float64(5 * usd), "expires_in_days": float64(90)})
	first := h.call("credits_grant", args)
	second := h.call("credits_grant", args)
	if first["deduped"] != false || second["deduped"] != true {
		t.Fatalf("dedupe flags: %v %v", first["deduped"], second["deduped"])
	}
	if avail, _ := h.balance("acct_1"); avail != 5*usd {
		t.Fatalf("available %d, want %d", avail, 5*usd)
	}
	if got := len(h.events("credits.granted")); got != 1 {
		t.Fatalf("credits.granted emitted %d times", got)
	}
	lot := first["lot"].(map[string]any)
	if num(lot["priority"]) != promoPriority || lot["paid"] != false {
		t.Fatalf("promo lot: %v", lot)
	}
}

func TestGrant_RejectsPurchaseAndFractions(t *testing.T) {
	h := newHarness(t)
	if _, err := h.try("credits_grant", acct("a", map[string]any{"source": "purchase", "amount": float64(1)})); err == nil || !strings.Contains(err.Error(), "topup") {
		t.Fatalf("purchase grant should be refused, got %v", err)
	}
	if _, err := h.try("credits_grant", acct("a", map[string]any{"source": "promo", "amount": 1.5})); err == nil {
		t.Fatal("fractional amount accepted")
	}
	if _, err := h.try("credits_grant", acct("a", map[string]any{"source": "promo", "amount": float64(-5)})); err == nil {
		t.Fatal("negative grant accepted")
	}
}

func TestSpendOrder_PriorityThenExpiryThenAge(t *testing.T) {
	h := newHarness(t)
	grant := func(ref string, amount int64, extra map[string]any) int64 {
		args := acct("a", map[string]any{"source": "reward", "source_ref": ref, "amount": float64(amount)})
		for k, v := range extra {
			args[k] = v
		}
		return num(h.call("credits_grant", args)["lot"].(map[string]any)["id"])
	}
	old := grant("old-noexp", 100, nil)
	soon := grant("soon", 100, map[string]any{"expires_in_days": float64(2)})
	later := grant("later", 100, map[string]any{"expires_in_days": float64(30)})
	first := grant("first", 100, map[string]any{"priority": float64(1)})

	out := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(350), "reference": "test/op/1"}))
	holdID := num(out["hold_id"])
	rows, err := h.ctx.AppDB().Query(`SELECT lot_id, amount FROM credit_hold_lots WHERE hold_id=? ORDER BY seq`, holdID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var lot, amt int64
		_ = rows.Scan(&lot, &amt)
		got = append(got, fmt.Sprintf("%d:%d", lot, amt))
	}
	want := []string{fmt.Sprintf("%d:100", first), fmt.Sprintf("%d:100", soon), fmt.Sprintf("%d:100", later), fmt.Sprintf("%d:50", old)}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("draw order %v, want %v", got, want)
	}
}

// ─── Holds ───────────────────────────────────────────────────────────

func TestReserveCommitLess_ReleasesRest(t *testing.T) {
	h := newHarness(t)
	h.call("credits_grant", acct("a", map[string]any{"source": "reward", "amount": float64(10 * usd)}))
	r := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(2 * usd), "reference_app": "llm", "reference_type": "request", "reference_id": "req_1"}))
	if r["denied"] != false || num(r["available_after"]) != 8*usd {
		t.Fatalf("reserve: %v", r)
	}
	if avail, held := h.balance("a"); avail != 8*usd || held != 2*usd {
		t.Fatalf("after reserve: %d/%d", avail, held)
	}
	c := h.call("credits_commit", map[string]any{"hold_id": r["hold_id"], "amount": float64(usd / 2)})
	if num(c["committed"]) != usd/2 || num(c["released"]) != 3*usd/2 {
		t.Fatalf("commit: %v", c)
	}
	if avail, held := h.balance("a"); avail != 9*usd+usd/2 || held != 0 {
		t.Fatalf("after commit: %d/%d", avail, held)
	}
	again := h.call("credits_commit", map[string]any{"hold_id": r["hold_id"], "amount": float64(usd / 2)})
	if again["replayed"] != true {
		t.Fatalf("second commit not a replay: %v", again)
	}
	if _, err := h.try("credits_release", map[string]any{"hold_id": r["hold_id"]}); err == nil {
		t.Fatal("release after commit should fail")
	}
}

func TestReserve_SameReferenceSameHold(t *testing.T) {
	h := newHarness(t)
	h.call("credits_grant", acct("a", map[string]any{"source": "reward", "amount": float64(usd)}))
	args := acct("a", map[string]any{"amount": float64(100), "reference": "llm/request/r1"})
	a := h.call("credits_reserve", args)
	b := h.call("credits_reserve", args)
	if a["hold_id"] != b["hold_id"] || b["replayed"] != true {
		t.Fatalf("not idempotent: %v vs %v", a, b)
	}
	if _, held := h.balance("a"); held != 100 {
		t.Fatalf("held %d after duplicate reserve", held)
	}
}

func TestReserve_DeniedWritesNothing(t *testing.T) {
	h := newHarness(t)
	g := h.call("credits_grant", acct("a", map[string]any{"source": "reward", "amount": float64(100)}))
	wid := num(g["wallet"].(map[string]any)["id"])
	before := h.entriesCount(wid)
	out := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(101), "reference": "x/y/1"}))
	if out["denied"] != true || out["reason"] != "insufficient_balance" {
		t.Fatalf("expected insufficient_balance: %v", out)
	}
	if h.entriesCount(wid) != before {
		t.Fatal("denied reserve wrote entries")
	}
	missing := h.call("credits_reserve", acct("nobody", map[string]any{"amount": float64(1), "reference": "x/y/2"}))
	if missing["denied"] != true || missing["reason"] != "insufficient_balance" {
		t.Fatalf("missing wallet: %v", missing)
	}
	if h.wallet("nobody", "usd") != nil {
		t.Fatal("reserve created a wallet")
	}
}

func TestCommitOverrun_TakesBalanceThenShortfall(t *testing.T) {
	h := newHarness(t)
	h.call("credits_grant", acct("a", map[string]any{"source": "reward", "amount": float64(1000)}))
	r := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(300), "reference": "llm/request/r1"}))
	// Actual cost 500: 300 from the hold, 200 from available.
	c := h.call("credits_commit", map[string]any{"hold_id": r["hold_id"], "amount": float64(500)})
	if num(c["committed"]) != 500 || num(c["shortfall"]) != 0 {
		t.Fatalf("overrun within balance: %v", c)
	}
	if avail, _ := h.balance("a"); avail != 500 {
		t.Fatalf("available %d, want 500", avail)
	}
	// Actual cost 900 on a 100 hold: 100 from the hold, the 400 still available, 400 short.
	r2 := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(100), "reference": "llm/request/r2"}))
	c2 := h.call("credits_commit", map[string]any{"hold_id": r2["hold_id"], "amount": float64(900)})
	if num(c2["committed"]) != 500 || num(c2["shortfall"]) != 400 {
		t.Fatalf("overrun beyond balance: %v", c2)
	}
	if avail, held := h.balance("a"); avail != 0 || held != 0 {
		t.Fatalf("balance went to %d/%d", avail, held)
	}
	sf := h.call("credits_shortfalls_list", nil)
	if num(sf["count"]) != 1 {
		t.Fatalf("shortfalls: %v", sf)
	}
	if len(h.events("credits.shortfall")) != 1 || len(h.events("credits.balance.empty")) != 1 {
		t.Fatal("expected shortfall and balance.empty events")
	}
}

func TestSpend_ShortcutIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.call("credits_grant", acct("a", map[string]any{"source": "reward", "amount": float64(100)}))
	args := acct("a", map[string]any{"amount": float64(30), "reference": "sms/message/m1"})
	first := h.call("credits_spend", args)
	second := h.call("credits_spend", args)
	if first["denied"] != false || num(first["committed"]) != 30 || second["replayed"] != true {
		t.Fatalf("spend: %v / %v", first, second)
	}
	if avail, _ := h.balance("a"); avail != 70 {
		t.Fatalf("available %d, want 70", avail)
	}
	if _, err := h.try("credits_spend", acct("a", map[string]any{"amount": float64(1)})); err == nil {
		t.Fatal("spend without a reference should fail")
	}
}

func TestFrozenWalletDeniesReserve(t *testing.T) {
	h := newHarness(t)
	h.call("credits_grant", acct("a", map[string]any{"source": "reward", "amount": float64(100)}))
	h.call("credits_wallet_freeze", acct("a", nil))
	out := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(1), "reference": "x/y/1"}))
	if out["denied"] != true || out["reason"] != "frozen" {
		t.Fatalf("frozen: %v", out)
	}
	h.call("credits_wallet_unfreeze", acct("a", nil))
	if out := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(1), "reference": "x/y/1"})); out["denied"] != false {
		t.Fatalf("after unfreeze: %v", out)
	}
}

// ─── Caps ────────────────────────────────────────────────────────────

func TestCaps_ScopedMonthlyCap(t *testing.T) {
	h := newHarness(t)
	h.call("credits_grant", acct("a", map[string]any{"source": "reward", "amount": float64(50 * usd)}))
	h.call("credits_caps_set", acct("a", map[string]any{"scope": "project:dev", "window": "month", "limit_amount": float64(15 * usd)}))

	ok := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(10 * usd), "reference": "llm/request/1", "scope": "project:dev"}))
	if ok["denied"] != false {
		t.Fatalf("first reserve: %v", ok)
	}
	denied := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(6 * usd), "reference": "llm/request/2", "scope": "project:dev"}))
	if denied["denied"] != true || denied["reason"] != "cap_reached" {
		t.Fatalf("cap not enforced: %v", denied)
	}
	h.call("credits_reserve", acct("a", map[string]any{"amount": float64(6 * usd), "reference": "llm/request/3", "scope": "project:dev"}))
	if n := len(h.events("credits.cap.reached")); n != 1 {
		t.Fatalf("cap.reached emitted %d times, want once per period", n)
	}
	other := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(20 * usd), "reference": "llm/request/4", "scope": "project:prod"}))
	if other["denied"] != false {
		t.Fatalf("other scope should not be capped: %v", other)
	}

	// The open dev hold was created in November; committing it in December
	// still counts against November, and December starts empty.
	h.clock.t = time.Date(2026, 12, 1, 0, 0, 30, 0, time.UTC)
	h.call("credits_commit", map[string]any{"hold_id": ok["hold_id"], "amount": float64(10 * usd)})
	caps := h.call("credits_caps_get", acct("a", nil))["caps"].([]any)
	dec := caps[0].(map[string]any)
	if dec["period"] != "2026-12" || num(dec["spent"]) != 0 || num(dec["held"]) != 0 {
		t.Fatalf("december usage: %v", dec)
	}
	var novSpent, novHeld int64
	_ = h.ctx.AppDB().QueryRow(`SELECT spent, held FROM credit_cap_usage WHERE period='2026-11'`).Scan(&novSpent, &novHeld)
	if novSpent != 10*usd || novHeld != 0 {
		t.Fatalf("november usage spent %d held %d", novSpent, novHeld)
	}
	if out := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(15 * usd), "reference": "llm/request/5", "scope": "project:dev"})); out["denied"] != false {
		t.Fatalf("december reserve: %v", out)
	}
}

// ─── Expiry ──────────────────────────────────────────────────────────

func TestExpiry_HoldReleasedByWorker(t *testing.T) {
	h := newHarness(t)
	h.call("credits_grant", acct("a", map[string]any{"source": "reward", "amount": float64(100)}))
	r := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(40), "reference": "x/y/1", "ttl_seconds": float64(60)}))
	h.clock.advance(2 * time.Minute)
	if err := h.app.runExpiry(nil, h.ctx); err != nil {
		t.Fatal(err)
	}
	if avail, held := h.balance("a"); avail != 100 || held != 0 {
		t.Fatalf("after expiry %d/%d", avail, held)
	}
	if len(h.events("credits.hold.expired")) != 1 {
		t.Fatal("no hold.expired event")
	}
	if _, err := h.try("credits_commit", map[string]any{"hold_id": r["hold_id"], "amount": float64(40)}); err == nil {
		t.Fatal("commit on an expired hold should fail")
	}
}

func TestExpiry_LotExpiresAroundHeldPart(t *testing.T) {
	h := newHarness(t)
	h.call("credits_grant", acct("a", map[string]any{"source": "promo", "amount": float64(100), "expires_in_days": float64(1)}))
	h.call("credits_grant", acct("a", map[string]any{"source": "reward", "amount": float64(50)}))
	r := h.call("credits_reserve", acct("a", map[string]any{"amount": float64(30), "reference": "video/job/1", "ttl_seconds": float64(3 * 24 * 3600)}))

	h.clock.advance(25 * time.Hour)
	if err := h.app.runExpiry(nil, h.ctx); err != nil {
		t.Fatal(err)
	}
	// 70 unheld promo expired; the 30 held stays held.
	if avail, held := h.balance("a"); avail != 50 || held != 30 {
		t.Fatalf("after lot expiry %d/%d", avail, held)
	}
	// Job used 10: the 20 left over belongs to an expired lot, so it expires.
	c := h.call("credits_commit", map[string]any{"hold_id": r["hold_id"], "amount": float64(10)})
	if num(c["released"]) != 0 || num(c["expired"]) != 20 {
		t.Fatalf("commit on expired lot: %v", c)
	}
	if avail, held := h.balance("a"); avail != 50 || held != 0 {
		t.Fatalf("final %d/%d", avail, held)
	}
	lots := h.call("credits_lots_list", acct("a", map[string]any{"status": "expired"}))
	if num(lots["count"]) != 1 {
		t.Fatalf("expired lots: %v", lots)
	}
}

// ─── Admin ───────────────────────────────────────────────────────────

func TestAdjust_ReasonRequiredAndNeverNegative(t *testing.T) {
	h := newHarness(t)
	if _, err := h.try("credits_adjust", acct("a", map[string]any{"amount": float64(10)})); err == nil {
		t.Fatal("adjust without reason accepted")
	}
	h.call("credits_adjust", acct("a", map[string]any{"amount": float64(100), "reason": "goodwill", "idempotency_key": "t-1"}))
	replay := h.call("credits_adjust", acct("a", map[string]any{"amount": float64(100), "reason": "goodwill", "idempotency_key": "t-1"}))
	if replay["replayed"] != true {
		t.Fatalf("adjust replay: %v", replay)
	}
	if _, err := h.try("credits_adjust", acct("a", map[string]any{"amount": float64(-101), "reason": "fix"})); err == nil {
		t.Fatal("negative adjust beyond balance accepted")
	}
	h.call("credits_adjust", acct("a", map[string]any{"amount": float64(-40), "reason": "fix"}))
	if avail, _ := h.balance("a"); avail != 60 {
		t.Fatalf("available %d, want 60", avail)
	}
}

func TestTransfer_SameCustomerKeepsPaidFlag(t *testing.T) {
	h := newHarness(t)
	h.call("credits_wallet_ensure", acct("org", map[string]any{"billing_customer_id": float64(7)}))
	h.call("credits_topup_create", acct("org", map[string]any{"price_cents": float64(5000), "idempotency_key": "k1", "presentation": "none"}))
	h.paid(801, 0)
	before := h.call("credits_liability_report", nil)

	out := h.call("credits_transfer", map[string]any{
		"from_subject_type": "saas_account", "from_subject_id": "org", "to_subject_type": "project", "to_subject_id": "p1",
		"unit": "usd", "amount": float64(20 * usd), "idempotency_key": "tr-1", "reason": "fund project",
	})
	lot := out["lots"].([]any)[0].(map[string]any)
	if lot["source"] != "transfer" || lot["paid"] != true || lot["refundable"] != true {
		t.Fatalf("transferred lot: %v", lot)
	}
	after := h.call("credits_liability_report", nil)
	unspent := func(r map[string]any) int64 { return num(r["units"].([]any)[0].(map[string]any)["unspent"]) }
	if unspent(before) != 50*usd || unspent(after) != 50*usd {
		t.Fatalf("transfer changed liability: %v -> %v", before["units"], after["units"])
	}
	h.call("credits_wallet_ensure", acct("stranger", map[string]any{"billing_customer_id": float64(99)}))
	_, err := h.try("credits_transfer", map[string]any{"from_subject_type": "saas_account", "from_subject_id": "org", "to_subject_type": "saas_account", "to_subject_id": "stranger", "unit": "usd", "amount": float64(1)})
	if err == nil || !strings.Contains(err.Error(), "different billing customers") {
		t.Fatalf("cross-customer transfer: %v", err)
	}
}

// ─── Billing ─────────────────────────────────────────────────────────

func (h *harness) paid(invoiceID int64, topupID int64) {
	h.t.Helper()
	data := map[string]any{"id": float64(invoiceID), "status": "paid", "total_cents": float64(6000), "event_id": fmt.Sprintf("payment:%d:paid", invoiceID)}
	if topupID != 0 {
		data["metadata"] = map[string]any{"source": "credits", "credits_topup_id": float64(topupID)}
	}
	if err := h.app.handleInvoicePaid(h.ctx, sdk.Event{Topic: "invoice.paid", ProjectID: pid, Data: data}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) refunded(invoiceID, totalCents, paymentCents int64, eventID string) {
	h.t.Helper()
	data := map[string]any{"id": float64(invoiceID), "status": "paid", "total_cents": float64(totalCents), "payment_amount_cents": float64(paymentCents), "event_id": eventID}
	if err := h.app.handleInvoiceRefunded(h.ctx, sdk.Event{Topic: "invoice.refunded", ProjectID: pid, Data: data}); err != nil {
		h.t.Fatal(err)
	}
}

func TestTopup_CreditOnlyOncePaid(t *testing.T) {
	h := newHarness(t)
	out := h.call("credits_topup_create", acct("a", map[string]any{"price_cents": float64(5000), "customer_id": float64(7), "idempotency_key": "console-1", "success_url": "https://x/ok"}))
	topup := out["topup"].(map[string]any)
	if topup["status"] != "pending" || num(topup["amount"]) != 50*usd || num(out["invoice_id"]) != 801 {
		t.Fatalf("topup: %v", out)
	}
	if out["payment_session"].(map[string]any)["url"] == "" {
		t.Fatal("no payment session")
	}
	inv := h.billing.toolCalls("invoices_create_from_prepared_lines")
	if len(inv) != 1 || inv[0].Args["finalize"] != true || inv[0].Args["currency"] != "usd" {
		t.Fatalf("invoice call: %+v", inv)
	}
	line := inv[0].Args["line_items"].([]any)[0].(map[string]any)
	if line["unit_price_cents"] != int64(5000) {
		t.Fatalf("line item: %v", line)
	}
	if avail, _ := h.balance("a"); avail != 0 {
		t.Fatalf("credited before payment: %d", avail)
	}
	// A retry with the same key reuses the invoice.
	h.call("credits_topup_create", acct("a", map[string]any{"price_cents": float64(5000), "idempotency_key": "console-1"}))
	if n := len(h.billing.toolCalls("invoices_create_from_prepared_lines")); n != 1 {
		t.Fatalf("retry created %d invoices", n)
	}

	h.paid(801, 0)
	h.paid(801, 0) // redelivery
	if avail, _ := h.balance("a"); avail != 50*usd {
		t.Fatalf("after paid: %d", avail)
	}
	if n := len(h.events("credits.topup.paid")); n != 1 {
		t.Fatalf("topup.paid emitted %d times", n)
	}
	lots := h.call("credits_lots_list", acct("a", nil))["lots"].([]any)
	lot := lots[0].(map[string]any)
	if lot["source"] != "purchase" || lot["paid"] != true || lot["source_ref"] != "billing:invoice:801" {
		t.Fatalf("purchase lot: %v", lot)
	}
	// Unrelated invoices are ignored.
	h.paid(999, 0)
}

func TestTopup_PaidMatchedByMetadataWhenInvoiceIDUnsaved(t *testing.T) {
	h := newHarness(t)
	out := h.call("credits_topup_create", acct("a", map[string]any{"price_cents": float64(1000), "customer_id": float64(7), "idempotency_key": "k", "presentation": "none"}))
	tid := num(out["topup"].(map[string]any)["id"])
	if _, err := h.ctx.AppDB().Exec(`UPDATE credit_topups SET invoice_id=0 WHERE id=?`, tid); err != nil {
		t.Fatal(err)
	}
	h.paid(801, tid)
	if avail, _ := h.balance("a"); avail != 10*usd {
		t.Fatalf("not credited via metadata: %d", avail)
	}
}

func TestRefund_PartialThenSpentPartIsShortfall(t *testing.T) {
	h := newHarness(t)
	h.call("credits_topup_create", acct("a", map[string]any{"price_cents": float64(5000), "customer_id": float64(7), "idempotency_key": "k", "presentation": "none"}))
	h.paid(801, 0)
	// Spend $30 of the $50.
	h.call("credits_spend", acct("a", map[string]any{"amount": float64(30 * usd), "reference": "llm/request/1"}))

	// Invoice total $60 incl. VAT; refund $12 gross = 1/5 = $10 of credit.
	h.refunded(801, 6000, -1200, "payment:5")
	h.refunded(801, 6000, -1200, "payment:5") // redelivery
	if avail, _ := h.balance("a"); avail != 10*usd {
		t.Fatalf("after partial refund: %d, want %d", avail, 10*usd)
	}
	// Refund the remaining $48 gross = $40 of credit, but only $10 is unspent.
	h.refunded(801, 6000, -4800, "payment:6")
	if avail, _ := h.balance("a"); avail != 0 {
		t.Fatalf("after full refund: %d", avail)
	}
	sf := h.call("credits_shortfalls_list", nil)["shortfalls"].([]any)
	if len(sf) != 1 || num(sf[0].(map[string]any)["amount"]) != 30*usd || sf[0].(map[string]any)["kind"] != "refund_spent" {
		t.Fatalf("shortfalls: %v", sf)
	}
	liab := h.call("credits_liability_report", nil)["units"].([]any)[0].(map[string]any)
	if num(liab["unspent"]) != 0 {
		t.Fatalf("liability after refund: %v", liab)
	}
}

func TestLiability_AsOfDate(t *testing.T) {
	h := newHarness(t)
	h.call("credits_grant", acct("a", map[string]any{"source": "promo", "amount": float64(5 * usd)}))
	h.call("credits_topup_create", acct("a", map[string]any{"price_cents": float64(5000), "customer_id": float64(7), "idempotency_key": "k", "presentation": "none"}))
	h.paid(801, 0)
	h.clock.advance(48 * time.Hour) // 2026-11-20
	h.call("credits_spend", acct("a", map[string]any{"amount": float64(25 * usd), "reference": "llm/request/1"}))

	at := func(day string) int64 {
		units := h.call("credits_liability_report", map[string]any{"as_of": day})["units"].([]any)
		if len(units) == 0 {
			return 0
		}
		return num(units[0].(map[string]any)["unspent"])
	}
	// Promo is spent first, so the $25 spend takes $5 promo + $20 purchase.
	if got := at("2026-11-17"); got != 0 {
		t.Fatalf("before purchase: %d", got)
	}
	if got := at("2026-11-19"); got != 50*usd {
		t.Fatalf("after purchase: %d", got)
	}
	if got := at("2026-11-20"); got != 30*usd {
		t.Fatalf("after spend: %d", got)
	}
}

func TestLowBalance_EventsAndAutoReload(t *testing.T) {
	h := newHarness(t)
	h.call("credits_wallet_ensure", acct("a", map[string]any{"billing_customer_id": float64(7)}))
	h.call("credits_wallet_update", acct("a", map[string]any{"low_threshold": float64(5 * usd), "auto_reload": map[string]any{"enabled": true, "price_cents": float64(2000)}}))
	h.call("credits_grant", acct("a", map[string]any{"source": "reward", "amount": float64(10 * usd)}))
	if len(h.events("credits.balance.recovered")) != 1 {
		t.Fatal("expected recovered on first credit")
	}
	h.call("credits_spend", acct("a", map[string]any{"amount": float64(6 * usd), "reference": "x/y/1"}))
	if len(h.events("credits.balance.low")) != 1 {
		t.Fatal("expected balance.low")
	}
	collect := h.billing.toolCalls("invoices_collect")
	if len(collect) != 1 {
		t.Fatalf("auto-reload collect calls: %d", len(collect))
	}
	// Still low, and one auto-reload is in flight: no second attempt.
	h.call("credits_spend", acct("a", map[string]any{"amount": float64(4 * usd), "reference": "x/y/2"}))
	if len(h.events("credits.balance.empty")) != 1 {
		t.Fatal("expected balance.empty")
	}
	if n := len(h.billing.toolCalls("invoices_collect")); n != 1 {
		t.Fatalf("second auto-reload started while one is in flight: %d", n)
	}
	h.paid(801, 0)
	if avail, _ := h.balance("a"); avail != 20*usd {
		t.Fatalf("after auto-reload paid: %d", avail)
	}
	if len(h.events("credits.balance.recovered")) != 2 {
		t.Fatal("expected recovered after reload")
	}
}

// ─── HTTP ────────────────────────────────────────────────────────────

func TestHTTP_RoutesMirrorTools(t *testing.T) {
	h := newHarness(t)
	mux := http.NewServeMux()
	for _, r := range h.app.HTTPRoutes() {
		pattern := r.Pattern
		if r.Method != "" {
			pattern = r.Method + " " + pattern
		}
		mux.HandleFunc(pattern, r.Handler)
	}
	do := func(method, path string, body any) (int, map[string]any) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		rw := httptest.NewRecorder()
		mux.ServeHTTP(rw, req)
		var out map[string]any
		_ = json.Unmarshal(rw.Body.Bytes(), &out)
		return rw.Code, out
	}
	code, g := do("POST", "/grant", acct("a", map[string]any{"source": "promo", "amount": 1000}))
	if code != 200 {
		t.Fatalf("grant %d %v", code, g)
	}
	wid := num(g["wallet"].(map[string]any)["id"])
	_, r := do("POST", "/reserve", acct("a", map[string]any{"amount": 400, "reference": "llm/request/h1"}))
	code, c := do("POST", fmt.Sprintf("/holds/%d/commit", num(r["hold_id"])), map[string]any{"amount": 100})
	if code != 200 || num(c["committed"]) != 100 {
		t.Fatalf("commit %d %v", code, c)
	}
	code, e := do("GET", fmt.Sprintf("/wallets/%d/entries?limit=2", wid), nil)
	if code != 200 || num(e["count"]) != 2 || e["next_before_id"] == nil {
		t.Fatalf("entries %d %v", code, e)
	}
	if code, _ := do("GET", "/wallets/99999", nil); code != 404 {
		t.Fatalf("unknown wallet: %d", code)
	}
	code, wg := do("GET", "/wallets/lookup?subject_type=saas_account&subject_id=a&unit=usd", nil)
	if code != 200 || num(wg["wallet"].(map[string]any)["available"]) != 900 {
		t.Fatalf("lookup %d %v", code, wg)
	}
}

func TestReserve_ConcurrentNeverOverdraws(t *testing.T) {
	h := newHarness(t)
	h.call("credits_grant", acct("a", map[string]any{"source": "reward", "amount": float64(1000)}))
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := h.app.toolReserve(h.ctx, acct("a", map[string]any{"amount": float64(30), "reference": fmt.Sprintf("llm/request/c%d", i)}))
			if err != nil {
				t.Error(err)
				return
			}
			if !out.(*ReserveOut).Denied {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if granted != 33 {
		t.Fatalf("granted %d holds of 30 from 1000, want 33", granted)
	}
	if avail, held := h.balance("a"); avail != 10 || held != 990 {
		t.Fatalf("balance %d/%d", avail, held)
	}
}
