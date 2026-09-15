package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func europeanFixture(t *testing.T, provider string) (*sdk.AppCtx, *App, *paymentPlatform, map[string]any) {
	pf := &paymentPlatform{bankingPlatform: bankingPlatform{conn: sdk.PlatformConnection{ID: 44, AppSlug: provider, Status: "active"}}}
	ctx := newCtxWithPlatform(t, pf)
	app := &App{}
	args := map[string]any{"connection_id": float64(44), "request_key": "intent-1", "amount": float64(1234), "reference": "Dinner", "mode": "payment", "recipient_name": "Friend"}
	args["currency"] = "EUR"
	args["country"] = "ES"
	args["recipient_address"] = "DE89370400440532013000"
	args["debtor_iban"] = "ES9121000418450200051332"
	args["bank_name"] = "Test Bank"
	args["redirect_url"] = "https://example.com/bank-return"
	args["legal_name"] = "Payer Name"
	args["email"] = "payer@example.com"
	args["customer_id"] = "customer-1"
	args["customer_ip"] = "203.0.113.7"
	pf.responses = map[string]any{}
	return ctx, app, pf, args
}
func TestEuropeanPaymentsPayloadsAndStatus(t *testing.T) {
	for _, provider := range []string{"enable-banking", "truelayer-payments", "saltedge-payments"} {
		t.Run(provider, func(t *testing.T) {
			ctx, app, pf, args := europeanFixture(t, provider)
			create := map[string]any{"id": "pay-1", "payment_id": "pay-1", "status": "authorization_required", "url": "https://auth.enablebanking.com/pay"}
			if provider == "truelayer-payments" {
				create = map[string]any{"id": "pay-1", "status": "authorization_required", "hosted_page": map[string]any{"uri": "https://payment.truelayer.com/pay"}}
			}
			if provider == "saltedge-payments" {
				create = map[string]any{"data": map[string]any{"id": "pay-1", "status": "created", "payment_url": "https://www.saltedge.com/connect/pay"}}
			}
			pf.responses["create_payment"] = create
			p := preparePayment(t, ctx, app, args)
			if pf.calls["create_payment"] != 0 {
				t.Fatal("prepare created payment")
			}
			v, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true})
			if err != nil {
				t.Fatal(err)
			}
			submitted := v.(bankPayment)
			if submitted.ProviderID != "pay-1" || submitted.State != "authorization_required" {
				t.Fatal(submitted)
			}
			if _, err := app.toolBankingPaymentAuthorize(ctx, map[string]any{"id": p.ID}); err != nil {
				t.Fatal(err)
			}
			in := pf.inputs["create_payment"]
			switch provider {
			case "enable-banking":
				tx := flattenItems(childMap(in, "payment_request")["credit_transfer_transaction"])[0]
				if firstString(tx, "instructed_amount.amount") != "12.34" || firstString(tx, "beneficiary.creditor_account.identification") != args["recipient_address"] || in["state"] != p.CallbackState {
					t.Fatal(in)
				}
			case "truelayer-payments":
				if in["amount_in_minor"] != int64(1234) || in["idempotency_key"] != p.ID || firstString(in, "payment_method.beneficiary.account_identifier.iban") != args["recipient_address"] {
					t.Fatal(in)
				}
			case "saltedge-payments":
				if firstString(in, "payment_attributes.amount") != "12.34" || firstString(in, "payment_attributes.customer_ip_address") != "203.0.113.7" || in["customer_id"] != "customer-1" {
					t.Fatal(in)
				}
			}
			status := map[string]any{"id": "pay-1", "payment_id": "pay-1", "status": "settled"}
			if provider == "saltedge-payments" {
				status = map[string]any{"data": status}
			}
			pf.responses["get_payment"] = status
			v, err = app.toolBankingPaymentGet(ctx, map[string]any{"id": p.ID, "refresh": true})
			if err != nil || v.(bankPayment).ProviderStatus != "settled" {
				t.Fatal(v, err)
			}
			if provider == "truelayer-payments" && pf.inputs["get_payment"]["id"] != "pay-1" {
				t.Fatal("wrong TrueLayer status parameter")
			}
			if _, err = app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true}); err != nil {
				t.Fatal(err)
			}
			if pf.calls["create_payment"] != 1 {
				t.Fatal("replayed payment")
			}
			var n int
			ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&n)
			if n != 0 {
				t.Fatal("payment booked ledger entry")
			}
		})
	}
}
func TestEnableDeferredCallbackAndOneExecution(t *testing.T) {
	ctx, app, pf, args := europeanFixture(t, "enable-banking")
	args["deferred"] = true
	pf.responses["create_payment"] = map[string]any{"payment_id": "pay-1", "status": "RCVD", "url": "https://auth.enablebanking.com/pay"}
	p := preparePayment(t, ctx, app, args)
	app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true})
	if _, err := app.toolBankingPaymentCallback(ctx, map[string]any{"id": p.ID, "return_url": "https://example.com/bank-return?state=wrong"}); err == nil {
		t.Fatal("accepted wrong state")
	}
	app.toolBankingPaymentContinue(ctx, map[string]any{"id": p.ID, "confirmed": true})
	if pf.calls["submit_payment"] != 0 {
		t.Fatal("executed before authorization")
	}
	if _, err := app.toolBankingPaymentCallback(ctx, map[string]any{"id": p.ID, "return_url": "https://evil.example/bank-return?state=" + p.CallbackState}); err == nil {
		t.Fatal("accepted wrong redirect")
	}
	_, err := app.toolBankingPaymentCallback(ctx, map[string]any{"id": p.ID, "return_url": "https://example.com/bank-return?state=" + url.QueryEscape(p.CallbackState)})
	if err != nil {
		t.Fatal(err)
	}
	pf.failures = map[string]bool{"submit_payment": true}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app.toolBankingPaymentContinue(ctx, map[string]any{"id": p.ID, "confirmed": true})
		}()
	}
	wg.Wait()
	pf.responses["get_payment"] = map[string]any{"payment_id": "pay-1", "status": "ACTC"}
	if _, err = app.toolBankingPaymentGet(ctx, map[string]any{"id": p.ID, "refresh": true}); err != nil {
		t.Fatal(err)
	}
	app.toolBankingPaymentContinue(ctx, map[string]any{"id": p.ID, "confirmed": true})
	if pf.calls["submit_payment"] != 1 {
		t.Fatal("deferred submission replayed")
	}
	final, err := readBankPayment(ctx, p.ID)
	if err != nil || !final.Continued {
		t.Fatal(final, err)
	}
}
func TestPaymentCapabilitiesAndValidation(t *testing.T) {
	ctx, app, pf, args := europeanFixture(t, "enable-banking")
	args["deferred"] = true
	pf.responses["list_banks"] = map[string]any{"aspsps": []any{map[string]any{"name": "Test Bank", "country": "ES", "payments": []any{map[string]any{"payment_type": "SEPA"}}}}}
	if _, err := app.toolBankingPaymentPrepare(ctx, args); err == nil || !strings.Contains(err.Error(), "deferred") {
		t.Fatal(err)
	}
	args["recipient_address"] = "DE00370400440532013000"
	if _, err := app.toolBankingPaymentPrepare(ctx, args); err == nil {
		t.Fatal("invalid IBAN")
	}
	ctx, app, pf, args = paymentFixture(t, "teller")
	pf.responses = map[string]any{"get_account": map[string]any{"id": "bank-1"}}
	p := preparePayment(t, ctx, app, args)
	if _, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true}); err == nil {
		t.Fatal("unsupported account accepted")
	}
	if pf.calls["create_payment"] != 0 {
		t.Fatal("unsupported account wrote payment")
	}
}
func TestPlaidACHContinuation(t *testing.T) {
	ctx, app, pf, args := paymentFixture(t, "plaid")
	args["mode"] = "ach"
	args["direction"] = "credit"
	args["legal_name"] = "Owner"
	args["ach_class"] = "ppd"
	pf.decision = "user_action_required"
	p := preparePayment(t, ctx, app, args)
	v, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true})
	if err != nil || v.(bankPayment).State != "authorization_required" {
		t.Fatal(v, err)
	}
	if _, err = app.toolBankingPaymentAuthorize(ctx, map[string]any{"id": p.ID}); err != nil {
		t.Fatal(err)
	}
	if firstString(pf.inputs["create_link_token"], "transfer.authorization_id") != "authorization-1" {
		t.Fatal("missing transfer Link config")
	}
	pf.decision = "approved"
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app.toolBankingPaymentContinue(ctx, map[string]any{"id": p.ID, "confirmed": true})
		}()
	}
	wg.Wait()
	if pf.calls["create_transfer"] != 1 {
		t.Fatal("transfer creation count", pf.calls)
	}
	if pf.inputs["create_transfer_authorization"]["idempotency_key"] != p.ID {
		t.Fatal("changed authorization key")
	}
}
func TestPaymentStaleReadCannotOverwriteClaim(t *testing.T) {
	ctx, app, _, args := paymentFixture(t, "teller")
	p := preparePayment(t, ctx, app, args)
	claimed, ok, err := claimPayment(ctx, p, "submitting")
	if err != nil || !ok {
		t.Fatal(err)
	}
	p.State = "draft"
	saved, err := saveBankPayment(ctx, p)
	if err != nil || saved.State != claimed.State {
		t.Fatal("stale overwrite", saved, err)
	}
}
func TestEuropeanReconciliationRejectsMismatch(t *testing.T) {
	p := bankPayment{ID: "intent-1", Request: bankPaymentRequest{Provider: "truelayer-payments", Amount: 1234, Currency: "EUR", RecipientAddress: "DE89370400440532013000", RecipientName: "Friend", Reference: "Dinner"}}
	raw := map[string]any{"amount_in_minor": 1234, "currency": "EUR", "metadata": map[string]any{"finance_payment_id": p.ID}, "payment_method": map[string]any{"beneficiary": map[string]any{"account_identifier": map[string]any{"iban": p.Request.RecipientAddress}, "reference": "Dinner", "account_holder_name": "Friend"}}}
	if !europeanResponseMatches(p, raw) {
		t.Fatal("matching rejected")
	}
	raw["amount_in_minor"] = 999
	if europeanResponseMatches(p, raw) {
		t.Fatal("mismatched amount accepted")
	}
}

func TestPlaidCancellationRechecksEligibility(t *testing.T) {
	ctx, app, pf, args := paymentFixture(t, "plaid")
	args["mode"] = "ach"
	args["direction"] = "credit"
	args["legal_name"] = "Owner"
	args["ach_class"] = "ppd"
	p := preparePayment(t, ctx, app, args)
	if _, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolBankingPaymentCancel(ctx, map[string]any{"id": p.ID}); err == nil {
		t.Fatal("unconfirmed cancellation")
	}
	pf.responses = map[string]any{"get_transfer": map[string]any{"transfer": map[string]any{"id": "transfer-1", "cancellable": false, "status": "pending"}}}
	app.toolBankingPaymentCancel(ctx, map[string]any{"id": p.ID, "confirmed": true})
	if pf.calls["cancel_transfer"] != 0 {
		t.Fatal("ineligible cancellation")
	}
	pf.responses["get_transfer"] = map[string]any{"transfer": map[string]any{"id": "transfer-1", "cancellable": true, "status": "pending"}}
	if _, err := app.toolBankingPaymentCancel(ctx, map[string]any{"id": p.ID, "confirmed": true}); err != nil {
		t.Fatal(err)
	}
	if pf.calls["cancel_transfer"] != 1 {
		t.Fatal(pf.calls)
	}
}
func TestPaymentEventCursorDoesNotAdvanceOnFailure(t *testing.T) {
	ctx, app, pf, args := paymentFixture(t, "plaid")
	args["mode"] = "ach"
	args["direction"] = "credit"
	args["legal_name"] = "Owner"
	args["ach_class"] = "ppd"
	p := preparePayment(t, ctx, app, args)
	if _, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true}); err != nil {
		t.Fatal(err)
	}
	pf.responses = map[string]any{"sync_transfer_events": map[string]any{"transfer_events": []any{map[string]any{"event_id": 1, "transfer_id": "transfer-1", "event_type": "returned"}, map[string]any{"event_id": 2, "event_type": "sweep.pending"}}}, "get_transfer": map[string]any{"transfer": map[string]any{"id": "transfer-1", "status": "returned"}}}
	pf.failures = map[string]bool{"get_transfer": true}
	if _, err := app.syncPlaidPaymentEvents(ctx, 44); err == nil {
		t.Fatal("expected status failure")
	}
	var cursor int
	ctx.AppDB().QueryRow(`SELECT after_id FROM bank_payment_event_cursors`).Scan(&cursor)
	if cursor != 0 {
		t.Fatal("lost unprocessed event")
	}
	pf.failures = nil
	if _, err := app.syncPlaidPaymentEvents(ctx, 44); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().QueryRow(`SELECT after_id FROM bank_payment_event_cursors`).Scan(&cursor)
	if cursor != 2 {
		t.Fatal(cursor)
	}
	current, err := readBankPayment(ctx, p.ID)
	if err != nil || current.ProviderStatus != "returned" {
		t.Fatal(current, err)
	}
	pf.responses["sync_transfer_events"] = map[string]any{"transfer_events": []any{}}
	if _, err := app.syncPlaidPaymentEvents(ctx, 44); err != nil {
		t.Fatal(err)
	}
	if pf.inputs["sync_transfer_events"]["after_id"] != int64(2) {
		t.Fatal("cursor not reused")
	}
}
func TestEnableExecutionCrashClaimCannotReplay(t *testing.T) {
	ctx, app, pf, args := europeanFixture(t, "enable-banking")
	args["deferred"] = true
	p := preparePayment(t, ctx, app, args)
	p.State = "ready_to_execute"
	p.ProviderID = "pay-1"
	p.CallbackVerified = true
	p, _ = saveBankPayment(ctx, p)
	_, claimed, err := claimPayment(ctx, p, "continuing")
	if err != nil || !claimed {
		t.Fatal(err)
	}
	pf.responses["get_payment"] = map[string]any{"payment_id": "pay-1", "status": "ACTC"}
	if _, err = app.toolBankingPaymentGet(ctx, map[string]any{"id": p.ID, "refresh": true}); err != nil {
		t.Fatal(err)
	}
	app.toolBankingPaymentContinue(ctx, map[string]any{"id": p.ID, "confirmed": true})
	if pf.calls["submit_payment"] != 0 {
		t.Fatal("replayed crash claim")
	}
}

func TestPaymentWorkerOnlyReadsProviderStatus(t *testing.T) {
	ctx, app, pf, args := paymentFixture(t, "teller")
	p := preparePayment(t, ctx, app, args)
	if _, err := app.toolBankingPaymentSubmit(ctx, map[string]any{"id": p.ID, "confirmed": true}); err != nil {
		t.Fatal(err)
	}
	if err := app.paymentStatusWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if pf.calls["create_payment"] != 1 || pf.calls["get_payment"] != 1 {
		t.Fatal(pf.calls)
	}
	var checked string
	if err := ctx.AppDB().QueryRow(`SELECT last_checked_at FROM bank_payments WHERE id=?`, p.ID).Scan(&checked); err != nil || checked == "" {
		t.Fatal(checked, err)
	}
}

func TestPaymentManifestPublishesToolsAndWorker(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	if err := sdk.ValidateManifest(&manifest); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range manifest.Provides.MCPTools {
		names[tool.Name] = true
	}
	for _, tool := range app.bankingPaymentTools() {
		if !names[tool.Name] {
			t.Fatal("missing manifest tool", tool.Name)
		}
	}
	if len(manifest.Provides.Workers) != 1 || manifest.Provides.Workers[0].Name != "payment-status" {
		t.Fatal("missing status worker")
	}
}
