package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// prepaidStub simulates Credits (one wallet, holds), Catalog metered prices
// and llm tokens on top of the shared platformStub.
type prepaidStub struct {
	*platformStub
	available   int64
	denyReason  string
	nextHold    int64
	holds       map[int64]int64
	committed   map[int64]int64
	released    map[int64]bool
	nextTokenID int64
	revoked     []int64
}

var prepaidPrices = map[int64]map[string]any{
	11: {"id": 11, "unit_amount_cents": 75, "unit_size": 10_000_000, "currency": "USD", "billing_scheme": "metered", "meter_key": "llm:input", "active": true},
	12: {"id": 12, "unit_amount_cents": 300, "unit_size": 10_000_000, "currency": "USD", "billing_scheme": "metered", "meter_key": "llm:output", "active": true},
	13: {"id": 13, "unit_amount_cents": 1, "unit_size": 100, "currency": "USD", "billing_scheme": "metered", "meter_key": "llm:request", "active": true},
	14: {"id": 14, "unit_amount_cents": 75, "unit_size": 10_000_000, "currency": "EUR", "billing_scheme": "metered", "meter_key": "llm:input", "active": true},
	15: {"id": 15, "unit_amount_cents": 2900, "currency": "USD", "billing_scheme": "flat", "active": true},
}

func newPrepaidStub() *prepaidStub {
	return &prepaidStub{
		platformStub: &platformStub{entitled: true},
		available:    5_000_000, nextHold: 9000, nextTokenID: 76,
		holds: map[int64]int64{}, committed: map[int64]int64{}, released: map[int64]bool{},
	}
}

func (p *prepaidStub) CallAppResult(appName, tool string, input map[string]any, out any) error {
	var body any
	switch appName + ":" + tool {
	case "catalog:catalog_prices_get":
		body = map[string]any{"price": prepaidPrices[int64FromAny(input["id"])]}
	case "credits:credits_wallet_ensure":
		body = map[string]any{"wallet": map[string]any{"id": 1, "unit": input["unit"]}, "created": true}
	case "credits:credits_grant":
		body = map[string]any{"lot": map[string]any{"id": 1}}
	case "credits:credits_wallet_get":
		body = map[string]any{"wallet": map[string]any{"id": 1, "available": p.available, "unit": input["unit"]}}
	case "credits:credits_reserve":
		if p.denyReason != "" {
			body = map[string]any{"denied": true, "reason": p.denyReason}
			break
		}
		p.nextHold++
		p.holds[p.nextHold] = int64FromAny(input["amount"])
		body = map[string]any{"hold_id": p.nextHold, "available_after": p.available - int64FromAny(input["amount"])}
	case "credits:credits_commit":
		id := int64FromAny(input["hold_id"])
		amount, given := input["amount"]
		p.committed[id] = p.holds[id]
		if given {
			p.committed[id] = int64FromAny(amount)
		}
		body = map[string]any{"committed": p.committed[id]}
	case "credits:credits_release":
		p.released[int64FromAny(input["hold_id"])] = true
		body = map[string]any{"released": true}
	case "llm:llm_tokens_create":
		p.nextTokenID++
		body = map[string]any{"id": p.nextTokenID, "token": "rl_live_secret", "display": "rl_live_…cret", "metadata": input["metadata"], "base_url": "/api/apps/llm/v1"}
	case "llm:llm_tokens_revoke":
		p.revoked = append(p.revoked, int64FromAny(input["token_id"]))
		body = map[string]any{"revoked": true}
	default:
		return p.platformStub.CallAppResult(appName, tool, input, out)
	}
	p.platformStub.mu.Lock()
	p.platformStub.calls = append(p.platformStub.calls, callAppCall{App: appName, Tool: tool, Input: input})
	p.platformStub.mu.Unlock()
	raw, _ := json.Marshal(body)
	return json.Unmarshal(raw, out)
}

func (p *prepaidStub) lastCall(app, tool string) map[string]any {
	p.platformStub.mu.Lock()
	defer p.platformStub.mu.Unlock()
	for i := len(p.platformStub.calls) - 1; i >= 0; i-- {
		if c := p.platformStub.calls[i]; c.App == app && c.Tool == tool {
			return c.Input
		}
	}
	return nil
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", v)
	}
	return m
}

func setupPrepaidAccount(t *testing.T) (*App, *sdk.AppCtx, *prepaidStub, *Account) {
	t.Helper()
	pricing = &pricingCache{}
	pf := newPrepaidStub()
	ctx, db := newTestCtx(t, pf)
	t.Cleanup(func() { db.Close() })
	app := &App{}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "api", "name": "API", "billing_mode": "prepaid"}); err == nil || !strings.Contains(err.Error(), "credits_unit") {
		t.Fatalf("prepaid plan without credits_unit: err=%v", err)
	}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "api", "name": "API", "billing_mode": "prepaid", "subscription_required": true, "metadata": map[string]any{"credits_unit": "usd"}}); err == nil {
		t.Fatal("prepaid plan with subscription_required should fail")
	}
	if _, err := app.toolPlanUpsert(ctx, map[string]any{"key": "api", "name": "API", "billing_mode": "prepaid", "metadata": map[string]any{
		"credits_unit": "usd", "signup_credit": 5_000_000, "signup_credit_expires_days": 30, "low_balance_threshold": 1_000_000,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanFeatureAdd(ctx, map[string]any{"plan_key": "api", "feature_key": "api:access"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPlanMeterSet(ctx, map[string]any{"plan_key": "api", "source_app": "llm", "meter": "model:text-fast", "input_price_id": 14}); err == nil || !strings.Contains(err.Error(), "credits_unit") {
		t.Fatalf("EUR price on usd plan: err=%v", err)
	}
	if _, err := app.toolPlanMeterSet(ctx, map[string]any{"plan_key": "api", "source_app": "llm", "meter": "model:text-fast", "input_price_id": 15}); err == nil || !strings.Contains(err.Error(), "metered") {
		t.Fatalf("flat price as meter: err=%v", err)
	}
	meterOut, err := app.toolPlanMeterSet(ctx, map[string]any{"plan_key": "api", "source_app": "llm", "meter": "model:text-fast", "input_price_id": 11, "output_price_id": 12})
	if err != nil {
		t.Fatal(err)
	}
	if meta := string(asMap(t, meterOut)["meter"].(*PlanMeter).Metadata); meta != "{}" {
		t.Fatalf("meter metadata = %s, want {}", meta)
	}
	created, err := app.toolAccountCreate(ctx, map[string]any{"owner_email": "dev@example.com", "slug": "runlane-dev", "plan_key": "api"})
	if err != nil {
		t.Fatal(err)
	}
	acct := asMap(t, created)["account"].(*Account)
	if acct.Status != StatusActive {
		t.Fatalf("prepaid account status=%s, want active", acct.Status)
	}
	return app, ctx, pf, acct
}

func TestPriceUsage_RoundsUpOncePerRequest(t *testing.T) {
	in := meterPriceInfo{UnitAmountCents: 75, UnitSize: 10_000_000}
	out := meterPriceInfo{UnitAmountCents: 300, UnitSize: 10_000_000}
	cached := meterPriceInfo{UnitAmountCents: 30, UnitSize: 10_000_000}
	m := &resolvedMeter{Input: &in, Output: &out, CachedInput: &cached}
	// 1000 input = 75 micro, 500 output = 150 micro.
	if got := priceUsage(m, usageQuantities{Input: 1000, Output: 500}); got != 225 {
		t.Fatalf("got %d, want 225", got)
	}
	// 1 input token = 0.075 micro → rounds up to 1.
	if got := priceUsage(m, usageQuantities{Input: 1}); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
	// 1000 input of which 400 cached: 600*0.075 + 400*0.03 = 45 + 12.
	if got := priceUsage(m, usageQuantities{Input: 1000, CachedInput: 400}); got != 57 {
		t.Fatalf("got %d, want 57", got)
	}
	// Without a cached price, cached tokens use the input price.
	m.CachedInput = nil
	if got := priceUsage(m, usageQuantities{Input: 1000, CachedInput: 400}); got != 75 {
		t.Fatalf("got %d, want 75", got)
	}
}

func TestPrepaid_ProvisioningCreatesWalletAndSignupCredit(t *testing.T) {
	_, _, pf, acct := setupPrepaidAccount(t)
	ensure := pf.lastCall("credits", "credits_wallet_ensure")
	if ensure == nil || ensure["subject_type"] != "saas_account" || ensure["subject_id"] != acct.ID || ensure["unit"] != "usd" || int64FromAny(ensure["low_threshold"]) != 1_000_000 {
		t.Fatalf("wallet ensure input = %#v", ensure)
	}
	grant := pf.lastCall("credits", "credits_grant")
	if grant == nil || int64FromAny(grant["amount"]) != 5_000_000 || grant["source"] != "promo" || grant["source_ref"] != "saas:signup:"+acct.ID || int64FromAny(grant["expires_in_days"]) != 30 {
		t.Fatalf("signup grant input = %#v", grant)
	}
	if pf.lastCall("subscriptions", "subscriptions_create") != nil {
		t.Fatal("prepaid account must not create a subscription")
	}
}

func TestPrepaid_AccessCheckRequiresCredit(t *testing.T) {
	app, ctx, pf, acct := setupPrepaidAccount(t)
	out, err := app.toolAccessCheck(ctx, map[string]any{"account_id": acct.ID, "feature_key": "api:access"})
	if err != nil {
		t.Fatal(err)
	}
	if res := asMap(t, out); res["allowed"] != true || int64FromAny(res["credit_available"]) != 5_000_000 {
		t.Fatalf("funded access = %#v", res)
	}
	pf.available = 0
	out, err = app.toolAccessCheck(ctx, map[string]any{"account_id": acct.ID, "feature_key": "api:access"})
	if err != nil {
		t.Fatal(err)
	}
	if res := asMap(t, out); res["allowed"] != false || res["reason"] != "no_credit" {
		t.Fatalf("empty wallet access = %#v", res)
	}
}

func TestPrepaid_AuthorizeAndSettle(t *testing.T) {
	app, ctx, pf, acct := setupPrepaidAccount(t)
	meta := map[string]any{"account_id": acct.ID, "project": "production"}
	out, err := app.toolUsageAuthorize(ctx, map[string]any{
		"request_id": "req_1", "token_metadata": meta, "model": "text-fast",
		"estimated_input_tokens": 1000, "max_output_tokens": 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := asMap(t, out)
	if res["allow"] != true || res["admission_ref"] != "9001" || int64FromAny(res["amount"]) != 225 {
		t.Fatalf("authorize = %#v", res)
	}
	reserve := pf.lastCall("credits", "credits_reserve")
	if reserve["reference"] != "llm/request/req_1" || reserve["scope"] != "project:production" || reserve["subject_id"] != acct.ID || int64FromAny(reserve["amount"]) != 225 {
		t.Fatalf("reserve input = %#v", reserve)
	}

	// llm sends token_metadata as raw JSON; settle prices the actual counts.
	rawMeta, _ := json.Marshal(meta)
	out, err = app.toolUsageSettle(ctx, map[string]any{
		"admission_ref": "9001", "status": "completed", "token_metadata": json.RawMessage(rawMeta),
		"requested_model": "text-fast", "model": "gpt-provider-x", "input_tokens": 800, "output_tokens": 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res := asMap(t, out); int64FromAny(res["amount"]) != 90 || pf.committed[9001] != 90 {
		t.Fatalf("settle = %#v committed=%d", res, pf.committed[9001])
	}

	// A failed request releases its hold.
	if _, err := app.toolUsageAuthorize(ctx, map[string]any{"request_id": "req_2", "token_metadata": meta, "model": "text-fast", "estimated_input_tokens": 10, "max_output_tokens": 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolUsageSettle(ctx, map[string]any{"admission_ref": "9002", "status": "failed", "token_metadata": meta}); err != nil {
		t.Fatal(err)
	}
	if !pf.released[9002] {
		t.Fatal("failed request did not release its hold")
	}

	// No admission ref (free or bypassed request) is a no-op.
	if out, err := app.toolUsageSettle(ctx, map[string]any{"admission_ref": "", "status": "completed"}); err != nil || asMap(t, out)["settled"] != false {
		t.Fatalf("empty ref settle = %#v, %v", out, err)
	}
}

func TestPrepaid_AuthorizeRefusals(t *testing.T) {
	app, ctx, pf, acct := setupPrepaidAccount(t)
	meta := map[string]any{"account_id": acct.ID}
	check := func(args map[string]any, status int, errType, reason string) {
		t.Helper()
		args["token_metadata"] = meta
		if _, ok := args["request_id"]; !ok {
			args["request_id"] = "req_x"
		}
		out, err := app.toolUsageAuthorize(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		res := asMap(t, out)
		if res["allow"] != false || int64FromAny(res["http_status"]) != int64(status) || res["error_type"] != errType || res["reason"] != reason {
			t.Fatalf("refusal = %#v, want %d %s %s", res, status, errType, reason)
		}
	}
	check(map[string]any{"model": "unknown-model"}, 400, "invalid_request_error", "no_price")
	pf.denyReason = "insufficient_balance"
	check(map[string]any{"model": "text-fast"}, 429, "insufficient_quota", "no_credit")
	pf.denyReason = "cap_reached"
	check(map[string]any{"model": "text-fast"}, 429, "insufficient_quota", "cap_reached")
	pf.denyReason = ""

	// model:* acts as the plan's default price.
	if _, err := app.toolPlanMeterSet(ctx, map[string]any{"plan_key": "api", "source_app": "llm", "meter": "model:*", "per_request_price_id": 13}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolUsageAuthorize(ctx, map[string]any{"request_id": "req_d", "token_metadata": meta, "model": "unknown-model"})
	if err != nil || asMap(t, out)["allow"] != true || int64FromAny(asMap(t, out)["amount"]) != 100 {
		t.Fatalf("default meter authorize = %#v, %v", out, err)
	}

	if _, err := app.toolAccountSuspend(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	check(map[string]any{"model": "text-fast"}, 403, "permission_error", "account_suspended")

	out, err = app.toolUsageAuthorize(ctx, map[string]any{"request_id": "req_y", "token_metadata": map[string]any{}, "model": "text-fast"})
	if err != nil || asMap(t, out)["reason"] != "no_account" {
		t.Fatalf("unlinked token = %#v, %v", out, err)
	}
}

func TestPrepaid_AccountKeys(t *testing.T) {
	app, ctx, pf, acct := setupPrepaidAccount(t)
	out, err := app.toolAccountKeysCreate(ctx, map[string]any{"account_id": acct.ID, "label": "Production server", "project": "production", "prefix": "rl_live_"})
	if err != nil {
		t.Fatal(err)
	}
	res := asMap(t, out)
	key := res["key"].(*AccountKey)
	if res["token"] != "rl_live_secret" || key.TokenID != 77 || key.Space != "production" || key.Status != "active" {
		t.Fatalf("create key = %#v key=%#v", res, key)
	}
	create := pf.lastCall("llm", "llm_tokens_create")
	admission := mapFromAny(create["admission"])
	metadata := mapFromAny(create["metadata"])
	if admission["app"] != "saas" || admission["authorize_tool"] != "saas_usage_authorize" || admission["settle_tool"] != "saas_usage_settle" ||
		metadata["account_id"] != acct.ID || metadata["project"] != "production" || create["subject_id"] != acct.ID || create["prefix"] != "rl_live_" {
		t.Fatalf("llm_tokens_create input = %#v", create)
	}
	if _, err := app.toolAccountKeysCreate(ctx, map[string]any{"account_id": acct.ID, "label": "Staging"}); err != nil {
		t.Fatal(err)
	}
	listed, err := app.toolAccountKeysList(ctx, map[string]any{"account_id": acct.ID})
	if err != nil || asMap(t, listed)["count"] != 2 {
		t.Fatalf("list = %#v, %v", listed, err)
	}
	if _, err := app.toolAccountKeysRevoke(ctx, map[string]any{"account_id": acct.ID, "key_id": key.ID}); err != nil {
		t.Fatal(err)
	}
	if len(pf.revoked) != 1 || pf.revoked[0] != 77 {
		t.Fatalf("revoked = %v", pf.revoked)
	}
	if _, err := app.toolAccountCancel(ctx, map[string]any{"account_id": acct.ID}); err != nil {
		t.Fatal(err)
	}
	if len(pf.revoked) != 2 || pf.revoked[1] != 78 {
		t.Fatalf("cancel should revoke remaining keys, revoked = %v", pf.revoked)
	}
	listed, _ = app.toolAccountKeysList(ctx, map[string]any{"account_id": acct.ID})
	if asMap(t, listed)["count"] != 0 {
		t.Fatalf("active keys after cancel = %#v", listed)
	}
	if _, err := app.toolAccountKeysCreate(ctx, map[string]any{"account_id": acct.ID}); err == nil {
		t.Fatal("cancelled account must not get new keys")
	}
}

func TestPrepaid_AccountReadsIncludeWallet(t *testing.T) {
	app, ctx, _, acct := setupPrepaidAccount(t)
	out, err := app.toolAccountGet(ctx, map[string]any{"account_id": acct.ID})
	if err != nil {
		t.Fatal(err)
	}
	if wallet := unwrapMap(mapFromAny(asMap(t, out)["account"].(*Account).Wallet), "wallet"); int64FromAny(wallet["available"]) != 5_000_000 {
		t.Fatalf("account wallet = %#v", wallet)
	}
	listed, err := app.toolAccountList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	accounts := asMap(t, listed)["accounts"].([]*Account)
	if len(accounts) != 1 || accounts[0].Wallet == nil {
		t.Fatalf("listed accounts missing wallet: %#v", accounts)
	}
}

func TestPrepaid_WalletEmails(t *testing.T) {
	app, ctx, pf, acct := setupPrepaidAccount(t)
	if err := app.handleCreditsTopupPaid(ctx, sdk.Event{Event: "credits.topup.paid", SourceApp: "credits", ProjectID: "proj-test", Data: map[string]any{
		"topup_id": 42, "subject_type": "saas_account", "subject_id": acct.ID, "unit": "usd", "amount": 20_000_000, "price_cents": 2000, "currency": "usd", "available": 25_000_000,
	}}); err != nil {
		t.Fatal(err)
	}
	sent := pf.lastCall("messaging", "send_message")
	if sent == nil || sent["idempotency_key"] != "saas:"+acct.ID+":topup:42" || !strings.Contains(strFromAny(sent["body"]), "20.00 USD") || !strings.Contains(strFromAny(sent["body"]), "25.00 USD") {
		t.Fatalf("top-up receipt = %#v", sent)
	}
	if err := app.handleCreditsBalanceLow(ctx, sdk.Event{Event: "credits.balance.low", SourceApp: "credits", ProjectID: "proj-test", Data: map[string]any{
		"subject_type": "saas_account", "subject_id": acct.ID, "unit": "usd", "available": 750_000,
	}}); err != nil {
		t.Fatal(err)
	}
	sent = pf.lastCall("messaging", "send_message")
	if sent == nil || !strings.HasPrefix(strFromAny(sent["idempotency_key"]), "saas:"+acct.ID+":credits_low:") || !strings.Contains(strFromAny(sent["body"]), "0.75 USD") {
		t.Fatalf("low balance email = %#v", sent)
	}
	// Wallets owned by something other than a SaaS account are ignored.
	before := len(pf.platformStub.calls)
	_ = app.handleCreditsBalanceLow(ctx, sdk.Event{Event: "credits.balance.low", SourceApp: "credits", ProjectID: "proj-test", Data: map[string]any{"subject_type": "customer", "subject_id": "c1"}})
	if len(pf.platformStub.calls) != before {
		t.Fatal("non-account wallet event should not send")
	}
}
