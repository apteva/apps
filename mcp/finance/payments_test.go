package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type paymentPlatform struct {
	bankingPlatform
	mu        sync.Mutex
	calls     map[string]int
	inputs    map[string]map[string]any
	fail      bool
	mfa       bool
	decision  string
	responses map[string]any
	failures  map[string]bool
}

func (p *paymentPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls == nil {
		p.calls = map[string]int{}
		p.inputs = map[string]map[string]any{}
	}
	p.calls[tool]++
	p.inputs[tool] = input
	if p.fail && tool == "create_payment" {
		return nil, errors.New("timeout")
	}
	if p.failures[tool] {
		return nil, errors.New("timeout")
	}
	if body, ok := p.responses[tool]; ok {
		b, _ := json.Marshal(body)
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: b}, nil
	}
	var body any
	switch tool {
	case "get_account":
		body = map[string]any{"id": "bank-1", "links": map[string]any{"payments": "https://api.teller.io/accounts/bank-1/payments"}}
	case "get_payment_capabilities":
		body = map[string]any{"schemes": []any{map[string]any{"name": "zelle"}}}
	case "get_payment_template":
		body = map[string]any{"data": map[string]any{"identifier": "SEPA"}}
	case "get_payment_recipient":
		body = map[string]any{"recipient_id": "recipient-1", "name": "Friend", "iban": "DE89370400440532013000"}
	case "submit_payment", "cancel_transfer":
		body = map[string]any{"status": "ACSP"}
	case "list_banks":
		body = map[string]any{"aspsps": []any{map[string]any{"name": "Test Bank", "country": "ES", "payments": []any{map[string]any{"payment_type": "SEPA", "deferred_submission_supported": true}}}}}
	case "create_payment":
		if p.mfa {
			body = map[string]any{"connect_token": "mfa-token"}
		} else {
			body = map[string]any{"id": "payment-1", "payment_id": "payment-1", "status": "PAYMENT_STATUS_INPUT_NEEDED"}
		}
	case "create_transfer_authorization":
		decision := p.decision
		if decision == "" {
			decision = "approved"
		}
		body = map[string]any{"authorization": map[string]any{"id": "authorization-1", "decision": decision}}
	case "create_transfer":
		body = map[string]any{"transfer": map[string]any{"id": "transfer-1", "status": "pending"}}
	case "get_payment":
		body = map[string]any{"id": "payment-1", "payment_id": "payment-1", "amount": "12.34", "memo": "Dinner", "payee": map[string]any{"scheme": "zelle", "address": "friend@example.com"}}
	case "get_transfer":
		body = map[string]any{"transfer": map[string]any{"id": "transfer-1", "status": "settled"}}
	case "create_link_token":
		body = map[string]any{"hosted_link_url": "https://secure.plaid.com/link/test", "link_token": "link-test"}
	case "list_payments":
		body = []any{map[string]any{"id": "payment-1", "amount": "12.34", "memo": "Dinner", "date": "2026-09-15", "payee": map[string]any{"scheme": "zelle", "address": "friend@example.com"}}}
	default:
		return nil, errors.New("unexpected tool: " + tool)
	}
	b, _ := json.Marshal(body)
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: b}, nil
}
func paymentFixture(t *testing.T, provider string) (*sdk.AppCtx, *App, *paymentPlatform, map[string]any) {
	t.Helper()
	pf := &paymentPlatform{bankingPlatform: bankingPlatform{conn: sdk.PlatformConnection{ID: 44, AppSlug: provider, Status: "active"}}}
	ctx := newCtxWithPlatform(t, pf)
	app := &App{}
	acc, err := linkBankingAccount(ctx, provider, pf.conn, bankingAccount{ExternalID: "bank-1", Name: "Bank", Currency: "USD"}, 0, true, map[string]any{"access_token": "test-item-token"})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"connection_id": float64(44), "request_key": "intent-1", "amount": float64(1234), "currency": "USD", "reference": "Dinner", "mode": "payment", "account_id": float64(acc.Account.ID), "recipient_name": "Friend", "recipient_address": "friend@example.com"}
	return ctx, app, pf, args
}
func preparePayment(t *testing.T, ctx *sdk.AppCtx, app *App, args map[string]any) bankPayment {
	t.Helper()
	v, err := app.toolBankingPaymentPrepare(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return v.(bankPayment)
}

func TestPayment_ReviewImmutabilityAndExactlyOneSubmission(t *testing.T) {
	ctx, app, pf, args := paymentFixture(t, "teller")
	p := preparePayment(t, ctx, app, args)
	if len(pf.calls) != 0 {
		t.Fatal("prepare issued provider request")
	}
	again := preparePayment(t, ctx, app, args)
	if again.ID != p.ID {
		t.Fatal("draft not idempotent")
	}
	args["amount"] = float64(1200)
	if _, err := app.toolBankingPaymentPrepare(ctx, args); err == nil {
		t.Fatal("changed intent reused key")
	}
	if _, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID}); err == nil {
		t.Fatal("unconfirmed submission accepted")
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true})
		}()
	}
	wg.Wait()
	pf.mu.Lock()
	calls := pf.calls["create_payment"]
	input := pf.inputs["create_payment"]
	pf.mu.Unlock()
	if calls != 1 {
		t.Fatalf("provider writes=%d", calls)
	}
	if input["amount"] != "12.34" || input["idempotency_key"] != p.ID || asMap(input["payee"])["address"] != "friend@example.com" {
		t.Fatalf("wrong payload: %+v", input)
	}
	if _, ok := input["payee_id"]; ok {
		t.Fatal("obsolete payee_id sent")
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("submitted payment booked cash before bank sync")
	}
}
func TestPayment_TimeoutCannotResubmitAcrossAppRestart(t *testing.T) {
	ctx, app, pf, args := paymentFixture(t, "teller")
	pf.fail = true
	p := preparePayment(t, ctx, app, args)
	v, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true})
	if err != nil || v.(bankPayment).State != "unknown" {
		t.Fatal(v, err)
	}
	pf.fail = false
	other := &App{}
	if _, err = other.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true}); err != nil {
		t.Fatal(err)
	}
	if pf.calls["create_payment"] != 1 {
		t.Fatal("uncertain request replayed")
	}
	if _, err = other.toolBankingPaymentCancel(ctx, map[string]any{"id": p.ID}); err == nil {
		t.Fatal("cancelled an uncertain payment")
	}
}
func TestPayment_ProjectSourceExpiryAndAmountValidation(t *testing.T) {
	ctx, app, pf, args := paymentFixture(t, "teller")
	p := preparePayment(t, ctx, app, args)
	foreign := ctx.WithProject("foreign")
	if _, err := app.toolBankingPaymentSubmit(foreign, map[string]any{"id": p.ID, "confirmed": true}); err == nil {
		t.Fatal("foreign payment accepted")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE accounts SET archived=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true}); err == nil {
		t.Fatal("archived source accepted")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE accounts SET archived=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE bank_payments SET created_at='2000-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true}); err == nil {
		t.Fatal("expired draft accepted")
	}
	if pf.calls["create_payment"] != 0 {
		t.Fatal("invalid submission contacted bank")
	}
	for _, n := range []any{0, -1, 1.5, math.NaN(), math.Inf(1), float64(9007199254740992)} {
		if _, err := exactPositiveInteger(n); err == nil {
			t.Fatalf("accepted amount %v", n)
		}
	}
	if n, err := exactPositiveInteger(float64(1000000)); err != nil || n != 1000000 {
		t.Fatalf("large exact integer rejected: %v %v", n, err)
	}
}
func TestPayment_PlaidAuthorizationAndACHDecision(t *testing.T) {
	ctx, app, pf, args := paymentFixture(t, "plaid")
	args["currency"] = "EUR"
	args["recipient_id"] = "recipient-1"
	args["user_id"] = "user-1"
	args["country"] = "ES"
	p := preparePayment(t, ctx, app, args)
	v, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true})
	if err != nil || v.(bankPayment).State != "authorization_required" {
		t.Fatal(v, err)
	}
	if _, err = app.toolBankingPaymentAuthorize(ctx, map[string]any{"id": p.ID}); err != nil {
		t.Fatal(err)
	}
	input := pf.inputs["create_link_token"]
	if input["user_id"] != "user-1" || asMap(input["payment_initiation"])["payment_id"] != "payment-1" {
		t.Fatalf("wrong Link payload: %+v", input)
	}
	args["request_key"] = "ach-1"
	args["mode"] = "ach"
	args["currency"] = "USD"
	args["direction"] = "credit"
	args["legal_name"] = "Account Owner"
	args["ach_class"] = "ppd"
	p = preparePayment(t, ctx, app, args)
	pf.decision = "declined"
	v, err = app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true})
	if err != nil || v.(bankPayment).State != "declined" {
		t.Fatal(v, err)
	}
	if pf.calls["create_transfer"] != 0 {
		t.Fatal("declined ACH authorization still transferred")
	}
	args["request_key"] = "ach-2"
	p = preparePayment(t, ctx, app, args)
	pf.decision = "approved"
	v, err = app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true})
	if err != nil || v.(bankPayment).ProviderID != "transfer-1" {
		t.Fatal(v, err)
	}
	if pf.inputs["create_transfer_authorization"]["idempotency_key"] != p.ID {
		t.Fatal("ACH authorization missing stable key")
	}
	if pf.inputs["create_transfer"]["authorization_id"] != "authorization-1" {
		t.Fatal("ACH authorization not carried through")
	}
}
func TestPayment_TellerMFAAndVerifiedReconciliation(t *testing.T) {
	ctx, app, pf, args := paymentFixture(t, "teller")
	pf.mfa = true
	p := preparePayment(t, ctx, app, args)
	v, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true})
	if err != nil || v.(bankPayment).State != "authorization_required" {
		t.Fatal(v, err)
	}
	handoff, err := app.toolBankingPaymentAuthorize(ctx, map[string]any{"id": p.ID})
	if err != nil || handoff.(map[string]any)["connect_token"] != "mfa-token" {
		t.Fatal(handoff, err)
	}
	v, err = app.toolBankingPaymentGet(ctx, map[string]any{"id": p.ID, "refresh": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.(bankPayment).Response["candidates"].([]map[string]any)) != 1 {
		t.Fatal("matching provider payment not listed")
	}
	v, err = app.toolBankingPaymentGet(ctx, map[string]any{"id": p.ID, "refresh": true, "provider_id": "payment-1"})
	if err != nil || v.(bankPayment).ProviderID != "payment-1" {
		t.Fatal(v, err)
	}
	if pf.calls["create_payment"] != 1 {
		t.Fatal("MFA resubmitted payment")
	}
	wrong := v.(bankPayment).Request
	wrong.Amount = 555
	if paymentResponseMatches(wrong, map[string]any{"amount": "12.34", "memo": "Dinner", "payee": map[string]any{"scheme": "zelle", "address": "friend@example.com"}}) {
		t.Fatal("mismatched payment matched")
	}
}
func TestPayment_UnsupportedConnectorsAdvertiseNoWrites(t *testing.T) {
	for _, provider := range []string{"nordigen", "truelayer", "saltedge"} {
		pf := &paymentPlatform{bankingPlatform: bankingPlatform{conn: sdk.PlatformConnection{ID: 44, AppSlug: provider, Status: "active"}}}
		ctx := newCtxWithPlatform(t, pf)
		_, err := (&App{}).toolBankingPaymentPrepare(ctx, map[string]any{"connection_id": float64(44), "request_key": "x", "amount": float64(100)})
		if err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatal(provider, err)
		}
	}
}

func TestPayment_HTTPWorkflow(t *testing.T) {
	_, app, pf, args := paymentFixture(t, "teller")
	payload, _ := json.Marshal(args)
	recorder := httptest.NewRecorder()
	app.handleBankingPayments(recorder, httptest.NewRequest("POST", "/banking/payments/prepare", bytes.NewReader(payload)))
	if recorder.Code != 200 {
		t.Fatalf("prepare HTTP %d: %s", recorder.Code, recorder.Body.String())
	}
	var p bankPayment
	if err := json.Unmarshal(recorder.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(map[string]any{"id": p.ID, "confirmed": true})
	recorder = httptest.NewRecorder()
	app.handleBankingPayments(recorder, httptest.NewRequest("POST", "/banking/payments/submit", bytes.NewReader(payload)))
	if recorder.Code != 200 || pf.calls["create_payment"] != 1 {
		t.Fatalf("submit HTTP %d: %s", recorder.Code, recorder.Body.String())
	}
}
