package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type stripeToolCall struct {
	ConnectionID int64
	Tool         string
	Input        map[string]any
}

type stripePlatformStub struct {
	tk.BasePlatformClient
	ensureCalls []sdk.IntegrationWebhookEnsureRequest
	verifyCalls []sdk.IntegrationWebhookVerifyRequest
	toolCalls   []stripeToolCall
	verifyEvent json.RawMessage
	intent      json.RawMessage
}

func (s *stripePlatformStub) GetConnectionPublicConfig(id int64) (*sdk.ConnectionPublicConfig, error) {
	return &sdk.ConnectionPublicConfig{
		ConnectionID: id,
		Slug:         "stripe",
		Fields:       map[string]string{"publishableKey": "pk_test_public"},
	}, nil
}

func (s *stripePlatformStub) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{
		AppName: "billing", InstallID: 1, ProjectID: "test-proj",
		Bindings: map[string]any{"payment_processor": float64(57)},
	}, nil
}

func (s *stripePlatformStub) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "stripe", Status: "active"}, nil
}

func (s *stripePlatformStub) PlatformInfo() (*sdk.PlatformInfo, error) {
	return &sdk.PlatformInfo{PublicURL: "https://billing-test.example"}, nil
}

func (s *stripePlatformStub) EnsureIntegrationWebhook(req sdk.IntegrationWebhookEnsureRequest) (*sdk.IntegrationWebhookStatus, error) {
	s.ensureCalls = append(s.ensureCalls, req)
	return &sdk.IntegrationWebhookStatus{
		ConnectionID: req.ConnectionID, Role: req.Role, Provider: "stripe", Status: "ready",
	}, nil
}

func (s *stripePlatformStub) VerifyIntegrationWebhook(req sdk.IntegrationWebhookVerifyRequest) (*sdk.IntegrationWebhookVerifyResult, error) {
	s.verifyCalls = append(s.verifyCalls, req)
	return &sdk.IntegrationWebhookVerifyResult{Provider: "stripe", Event: s.verifyEvent}, nil
}

func (s *stripePlatformStub) ExecuteIntegrationTool(connectionID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	s.toolCalls = append(s.toolCalls, stripeToolCall{ConnectionID: connectionID, Tool: tool, Input: input})
	switch tool {
	case "create_customer":
		return &sdk.ExecuteResult{
			Success: true, Status: http.StatusOK,
			Data: json.RawMessage(`{"id":"cus_test_123"}`),
		}, nil
	case "create_checkout_session":
		if input["ui_mode"] == "custom" {
			return &sdk.ExecuteResult{
				Success: true, Status: http.StatusOK,
				Data: json.RawMessage(`{"id":"cs_test_123","client_secret":"cs_test_123_secret_abc","expires_at":1893456000}`),
			}, nil
		}
		return &sdk.ExecuteResult{
			Success: true, Status: http.StatusOK,
			Data: json.RawMessage(`{"id":"cs_test_123","url":"https://checkout.stripe.com/c/pay/cs_test_123","expires_at":1893456000}`),
		}, nil
	case "create_payment_intent":
		data := s.intent
		if len(data) == 0 {
			data = json.RawMessage(`{"id":"pi_collect_123","status":"succeeded","amount":1200,"currency":"usd","payment_method":"pm_test_123"}`)
		}
		return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: data}, nil
	case "get_payment_intent":
		return &sdk.ExecuteResult{
			Success: true, Status: http.StatusOK,
			Data: json.RawMessage(`{"id":"pi_checkout_123","status":"succeeded","amount":1100,"currency":"usd","payment_method":"pm_test_123"}`),
		}, nil
	case "get_payment_method":
		return &sdk.ExecuteResult{
			Success: true, Status: http.StatusOK,
			Data: json.RawMessage(`{"id":"pm_test_123","type":"card","customer":"cus_test_123","card":{"brand":"visa","last4":"4242","exp_month":12,"exp_year":2030,"country":"US"}}`),
		}, nil
	default:
		return nil, fmt.Errorf("unexpected Stripe tool %q", tool)
	}
}

func TestStripeIntegrationEnsuresWebhookBeforeCheckoutSession(t *testing.T) {
	platform := &stripePlatformStub{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	cust := mustCustomer(t, ctx, "buyer@example.com", "Buyer")
	inv := mustDraft(t, ctx, cust.ID, []any{line("Fractional taxed usage", 1.5, 1000, 1000)})
	inv = mustFinalize(t, ctx, inv.ID)

	out, err := app.toolInvoicesSendPaymentLink(ctx, map[string]any{"invoice_id": inv.ID})
	if err != nil {
		t.Fatalf("send payment link: %v", err)
	}
	got := out.(map[string]any)
	if got["stripe_session_id"] != "cs_test_123" {
		t.Fatalf("stripe_session_id=%v", got["stripe_session_id"])
	}
	if len(platform.ensureCalls) != 1 {
		t.Fatalf("webhook ensure calls=%d, want 1", len(platform.ensureCalls))
	}
	ensure := platform.ensureCalls[0]
	if ensure.ConnectionID != 57 || ensure.Role != "payment_processor" ||
		ensure.CallbackPath != "/webhooks/stripe" {
		t.Fatalf("ensure request=%#v", ensure)
	}
	if len(platform.toolCalls) != 1 || platform.toolCalls[0].Tool != "create_checkout_session" {
		t.Fatalf("Stripe tool calls=%#v", platform.toolCalls)
	}
	input := platform.toolCalls[0].Input
	if input["mode"] != "payment" {
		t.Fatalf("mode=%v", input["mode"])
	}
	if !strings.HasPrefix(input["success_url"].(string), "https://billing-test.example/dashboard?app=billing") {
		t.Fatalf("success_url=%q", input["success_url"])
	}
	meta := input["metadata"].(map[string]any)
	if meta["apteva_project_id"] != "test-proj" {
		t.Fatalf("metadata=%#v", meta)
	}
	var expectedAmount int64
	if err := ctx.AppDB().QueryRow(
		`SELECT amount_cents FROM billing_checkout_sessions WHERE provider_session_id = 'cs_test_123'`).
		Scan(&expectedAmount); err != nil {
		t.Fatal(err)
	}
	if expectedAmount != 1650 {
		t.Fatalf("persisted checkout amount=%d, want 1650", expectedAmount)
	}
}

func TestCreateElementsPaymentSessionReturnsOnlyPublicBrowserConfig(t *testing.T) {
	platform := &stripePlatformStub{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	cust := mustCustomer(t, ctx, "elements@example.com", "Elements Buyer")
	inv := mustFinalize(t, ctx, mustDraft(t, ctx, cust.ID, []any{line("Order", 1, 2400, 0)}).ID)

	out, err := app.toolInvoicesCreatePaymentSession(ctx, map[string]any{
		"invoice_id":           inv.ID,
		"presentation":         "elements",
		"idempotency_key":      "checkout-42",
		"return_url":           "https://store.example/checkout/return",
		"payment_method_types": []any{"card", "sepa_debit"},
	})
	if err != nil {
		t.Fatalf("create elements payment session: %v", err)
	}
	got := out.(map[string]any)
	if got["publishable_key"] != "pk_test_public" {
		t.Fatalf("publishable_key=%v", got["publishable_key"])
	}
	if got["client_secret"] != "cs_test_123_secret_abc" {
		t.Fatalf("client_secret=%v", got["client_secret"])
	}
	if got["url"] != "" {
		t.Fatalf("elements url=%v, want empty", got["url"])
	}
	if len(platform.toolCalls) != 1 {
		t.Fatalf("tool calls=%#v", platform.toolCalls)
	}
	input := platform.toolCalls[0].Input
	if input["ui_mode"] != "custom" || input["return_url"] != "https://store.example/checkout/return" {
		t.Fatalf("custom checkout input=%#v", input)
	}
	if _, exists := input["success_url"]; exists {
		t.Fatalf("elements input unexpectedly has success_url: %#v", input)
	}
	if _, exists := input["cancel_url"]; exists {
		t.Fatalf("elements input unexpectedly has cancel_url: %#v", input)
	}
	if input["payment_method_types[0]"] != "card" || input["payment_method_types[1]"] != "sepa_debit" {
		t.Fatalf("Stripe payment method array is not provider-encoded: %#v", input)
	}
	if _, exists := input["payment_method_types"]; exists {
		t.Fatalf("Stripe input contains an invalid unindexed payment method array: %#v", input)
	}
	var presentation, key string
	if err := ctx.AppDB().QueryRow(
		`SELECT presentation, idempotency_key
		 FROM billing_checkout_sessions WHERE provider_session_id='cs_test_123'`,
	).Scan(&presentation, &key); err != nil {
		t.Fatal(err)
	}
	if presentation != "elements" || key != "checkout-42" {
		t.Fatalf("persisted presentation=%q key=%q", presentation, key)
	}
}

func TestCheckoutCompletedRequiresPersistedMatchingSession(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	cust := mustCustomer(t, ctx, "checkout@example.com", "Checkout Buyer")
	inv := mustFinalize(t, ctx, mustDraft(t, ctx, cust.ID, []any{line("Taxed", 1, 1000, 1000)}).ID)
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO billing_checkout_sessions
		 (project_id, invoice_id, provider, provider_session_id, amount_cents, currency, status)
		 VALUES ('test-proj', ?, 'stripe', 'cs_known', 1100, 'USD', 'pending')`, inv.ID); err != nil {
		t.Fatal(err)
	}
	bad := []byte(`{"id":"cs_unknown","mode":"payment","payment_status":"paid","amount_total":1100,"currency":"usd","payment_intent":"pi_bad","metadata":{"apteva_invoice_id":"` + fmt.Sprint(inv.ID) + `","apteva_project_id":"test-proj"}}`)
	if err := app.handleCheckoutCompleted(ctx, bad); err == nil {
		t.Fatal("expected unknown session to be rejected")
	}
	mismatch := []byte(`{"id":"cs_known","mode":"payment","payment_status":"paid","amount_total":1000,"currency":"usd","payment_intent":"pi_mismatch"}`)
	if err := app.handleCheckoutCompleted(ctx, mismatch); err == nil {
		t.Fatal("expected amount mismatch to be rejected")
	}
	good := []byte(`{"id":"cs_known","mode":"payment","payment_status":"paid","amount_total":1100,"currency":"usd","payment_intent":"pi_good"}`)
	if err := app.handleCheckoutCompleted(ctx, good); err != nil {
		t.Fatal(err)
	}
	got, err := dbInvoiceGetByID(ctx.AppDB(), "test-proj", inv.ID)
	if err != nil || got.Status != "paid" || got.AmountPaidCents != 1100 {
		t.Fatalf("invoice after checkout = %+v, err=%v", got, err)
	}
}

func TestPaymentLinkCanSaveDefaultPaymentMethod(t *testing.T) {
	platform := &stripePlatformStub{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	cust := mustCustomer(t, ctx, "save@example.com", "Save Buyer")
	inv := mustFinalize(t, ctx, mustDraft(t, ctx, cust.ID, []any{line("Recurring", 1, 1000, 1000)}).ID)

	if _, err := app.toolInvoicesSendPaymentLink(ctx, map[string]any{
		"invoice_id":                 inv.ID,
		"save_payment_method":        true,
		"set_default_payment_method": true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(platform.toolCalls) != 2 ||
		platform.toolCalls[0].Tool != "create_customer" ||
		platform.toolCalls[1].Tool != "create_checkout_session" {
		t.Fatalf("tool calls=%#v", platform.toolCalls)
	}
	checkoutInput := platform.toolCalls[1].Input
	if checkoutInput["customer"] != "cus_test_123" {
		t.Fatalf("customer=%v", checkoutInput["customer"])
	}
	if _, ok := checkoutInput["customer_email"]; ok {
		t.Fatalf("customer_email must be omitted when saving a payment method")
	}
	intentData := checkoutInput["payment_intent_data"].(map[string]any)
	if intentData["setup_future_usage"] != "off_session" {
		t.Fatalf("payment_intent_data=%#v", intentData)
	}

	completed := []byte(`{"id":"cs_test_123","mode":"payment","payment_status":"paid","amount_total":1100,"currency":"usd","payment_intent":"pi_checkout_123","customer":"cus_test_123"}`)
	if err := app.handleCheckoutCompleted(ctx, completed); err != nil {
		t.Fatal(err)
	}
	pm, err := dbDefaultPaymentMethod(ctx.AppDB(), "test-proj", cust.ID)
	if err != nil || pm == nil {
		t.Fatalf("default payment method=%+v err=%v", pm, err)
	}
	if pm.ProviderPaymentMethodID != "pm_test_123" || pm.DisplayLast4 != "4242" || !pm.IsDefault {
		t.Fatalf("saved payment method=%+v", pm)
	}
}

func TestInvoicesCollectIsDurablyIdempotent(t *testing.T) {
	platform := &stripePlatformStub{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	cust := mustCustomer(t, ctx, "renew@example.com", "Renew Buyer")
	inv := mustFinalize(t, ctx, mustDraft(t, ctx, cust.ID, []any{line("Renewal", 1, 1200, 0)}).ID)
	if _, err := dbPaymentMethodUpsert(ctx.AppDB(), &PaymentMethod{
		ProjectID:               "test-proj",
		CustomerID:              cust.ID,
		Provider:                "stripe",
		ProviderCustomerID:      "cus_collect",
		ProviderPaymentMethodID: "pm_collect",
		Type:                    "card",
		Status:                  "active",
		IsDefault:               true,
		Reusable:                true,
	}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"invoice_id":      inv.ID,
		"idempotency_key": "subscription:19:cycle:2:collect",
	}
	first, err := app.toolInvoicesCollect(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.toolInvoicesCollect(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if second.(map[string]any)["replayed"] != true {
		t.Fatalf("second response=%#v", second)
	}
	var createCalls int
	for _, call := range platform.toolCalls {
		if call.Tool == "create_payment_intent" {
			createCalls++
			if call.Input["off_session"] != true || call.Input["confirm"] != true {
				t.Fatalf("payment intent input=%#v", call.Input)
			}
		}
	}
	if createCalls != 1 {
		t.Fatalf("create_payment_intent calls=%d, want 1; first=%#v", createCalls, first)
	}
	got, err := dbInvoiceGetByID(ctx.AppDB(), "test-proj", inv.ID)
	if err != nil || got.Status != "paid" || got.AmountPaidCents != 1200 {
		t.Fatalf("invoice=%+v err=%v", got, err)
	}
	attempt, err := dbCollectionAttemptByKey(ctx.AppDB(), "test-proj", args["idempotency_key"].(string))
	if err != nil || attempt.Status != "succeeded" || attempt.ProviderPaymentIntentID != "pi_collect_123" {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
}

func TestChargeRefundedRecordsEachPartialRefundOnce(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	cust := mustCustomer(t, ctx, "refunds@example.com", "Refund Buyer")
	inv := mustFinalize(t, ctx, mustDraft(t, ctx, cust.ID, []any{line("X", 1, 1000, 0)}).ID)
	if _, _, err := dbPaymentRecord(ctx.AppDB(), "test-proj", inv.ID, 1000,
		"stripe", "pi_refunds", nowRFC3339(), "", "test"); err != nil {
		t.Fatal(err)
	}
	first := []byte(`{"id":"ch_1","payment_intent":"pi_refunds","amount_refunded":300,"currency":"usd","refunds":{"data":[{"id":"re_1","amount":300,"status":"succeeded"}]}}`)
	if err := app.handleChargeRefunded(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := []byte(`{"id":"ch_1","payment_intent":"pi_refunds","amount_refunded":500,"currency":"usd","refunds":{"data":[{"id":"re_1","amount":300,"status":"succeeded"},{"id":"re_2","amount":200,"status":"succeeded"}]}}`)
	if err := app.handleChargeRefunded(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := app.handleChargeRefunded(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, _ := dbInvoiceGetByID(ctx.AppDB(), "test-proj", inv.ID)
	if got.AmountPaidCents != 500 || got.Status != "open" {
		t.Fatalf("invoice after partial refunds = %+v", got)
	}
	var refunds int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM payments WHERE invoice_id = ? AND amount_cents < 0`, inv.ID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if refunds != 2 {
		t.Fatalf("refund rows=%d, want 2", refunds)
	}
}

func TestStripeWebhookRejectsOversizedBody(t *testing.T) {
	_ = newTestCtx(t)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	req.Header.Set("Stripe-Signature", "t=1,v1=bad")
	rec := httptest.NewRecorder()
	(&App{}).handleStripeWebhook(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413", rec.Code)
	}
}

func TestPlatformVerifiedStripeWebhookMarksInvoicePaid(t *testing.T) {
	platform := &stripePlatformStub{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	cust := mustCustomer(t, ctx, "webhook@example.com", "Webhook Buyer")
	inv := mustFinalize(t, ctx, mustDraft(t, ctx, cust.ID, []any{line("Service", 1, 1200, 0)}).ID)
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO billing_checkout_sessions
		 (project_id, invoice_id, provider, provider_session_id, amount_cents, currency, status)
		 VALUES ('test-proj', ?, 'stripe', 'cs_verified', 1200, 'USD', 'pending')`, inv.ID); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"id":"evt_verified","type":"checkout.session.completed","data":{"object":{"id":"cs_verified","mode":"payment","payment_status":"paid","amount_total":1200,"currency":"usd","payment_intent":"pi_verified"}}}`)
	platform.verifyEvent = payload
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(string(payload)))
	req.Header.Set("Stripe-Signature", "t=platform-verifies,v1=opaque-to-billing")
	rec := httptest.NewRecorder()
	app.handleStripeWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook status=%d: %s", rec.Code, rec.Body.String())
	}
	if len(platform.verifyCalls) != 1 ||
		platform.verifyCalls[0].Payload != string(payload) ||
		platform.verifyCalls[0].Signature != "t=platform-verifies,v1=opaque-to-billing" {
		t.Fatalf("verify calls=%#v", platform.verifyCalls)
	}
	got, err := dbInvoiceGetByID(ctx.AppDB(), "test-proj", inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "paid" || got.AmountPaidCents != 1200 {
		t.Fatalf("invoice after verified webhook=%+v", got)
	}
}
