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
			if got := r.Form.Get("line_items[0][price_data][unit_amount]"); got != "2500" {
				t.Fatalf("unit_amount=%q", got)
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
	inv := mustDraft(t, ctx, cust.ID, []any{line("Hosting plan", 1, 2500, 0)})
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
