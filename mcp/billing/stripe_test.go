package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestStripeDirectCreatesWebhookAndCheckoutSession(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer sk_test_direct" {
			t.Fatalf("Authorization=%q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		switch r.URL.Path {
		case "/webhook_endpoints":
			if got := r.Form.Get("url"); got != "https://billing-test.example/webhooks/stripe" {
				t.Fatalf("webhook url=%q", got)
			}
			if got := r.Form["enabled_events[]"]; len(got) == 0 {
				t.Fatalf("enabled_events missing: %#v", r.Form)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"we_123","url":"https://billing-test.example/webhooks/stripe","secret":"whsec_generated"}`))
		case "/checkout/sessions":
			if got := r.Form.Get("mode"); got != "payment" {
				t.Fatalf("mode=%q", got)
			}
			if got := r.Form.Get("line_items[0][price_data][currency]"); got != "usd" {
				t.Fatalf("currency=%q", got)
			}
			if got := r.Form.Get("line_items[0][price_data][unit_amount]"); got != "1650" {
				t.Fatalf("unit_amount=%q", got)
			}
			if got := r.Form.Get("line_items[0][quantity]"); got != "1" {
				t.Fatalf("quantity=%q", got)
			}
			if got := r.Form.Get("metadata[apteva_project_id]"); got != "test-proj" {
				t.Fatalf("metadata project=%q", got)
			}
			if got := r.Form.Get("success_url"); !strings.HasPrefix(got, "https://billing-test.example/dashboard?app=billing") {
				t.Fatalf("success_url=%q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"cs_test_123","url":"https://checkout.stripe.com/c/pay/cs_test_123","expires_at":1893456000}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("STRIPE_API_BASE", srv.URL)

	ctx := newTestCtx(t, tk.WithConfig(map[string]string{
		"stripe_secret_key":  "sk_test_direct",
		"stripe_webhook_url": "https://billing-test.example/webhooks/stripe",
	}))
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
	if len(calls) != 2 || calls[0] != "/webhook_endpoints" || calls[1] != "/checkout/sessions" {
		t.Fatalf("calls=%v", calls)
	}
	settings, err := loadStripeSettings(ctx.AppDB())
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if settings.WebhookEndpointID != "we_123" || settings.WebhookSecret != "whsec_generated" {
		t.Fatalf("settings=%#v", settings)
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

func TestVerifyStripeWebhookSignature(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"id":"evt_123","type":"checkout.session.completed"}`)
	now := time.Unix(1893456000, 0)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.%s", now.Unix(), payload)))
	header := fmt.Sprintf("t=%d,v1=%s", now.Unix(), hex.EncodeToString(mac.Sum(nil)))

	if err := verifyStripeWebhookSignature(payload, header, secret, now); err != nil {
		t.Fatalf("verify valid signature: %v", err)
	}
	if err := verifyStripeWebhookSignature(payload, header, "wrong", now); err == nil {
		t.Fatal("expected wrong secret to fail")
	}
}
