package main

// Stripe integration (v0.8.0+).
//
// Stripe enters billing as the `payment_processor` integration role
// declared in apteva.yaml. When bound, the agent can call
// invoices_send_payment_link(invoice_id) to generate a Stripe Checkout
// Session and share its URL with the customer. The webhook handler
// at /webhooks/stripe receives the payment confirmation event and
// records a method='stripe' payment + transitions the invoice to
// 'paid' (idempotent via the (method, external_id) unique index on
// the payments table — already present since v0.1.0).
//
// When the integration isn't bound, the whole module degrades:
// invoices_send_payment_link returns a clean "bind the integration"
// error, the webhook endpoint returns 503, and everything else in
// billing keeps working as before (manual payment recording).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// ─── Helpers ────────────────────────────────────────────────────────

// requireProcessor returns the bound payment_processor integration
// or an error suitable for surfacing to agents/UI.
func requireProcessor(ctx *sdk.AppCtx) (*sdk.BoundIntegration, error) {
	bound := ctx.IntegrationFor("payment_processor")
	if bound == nil {
		return nil, errors.New(
			"no payment_processor bound — bind a Stripe connection in the billing app's settings",
		)
	}
	return bound, nil
}

// executeStripe runs a Stripe integration tool by its catalog name
// (create_checkout_session, create_customer, etc.) and decodes the
// upstream JSON response into `out`.
func executeStripe(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, tool string, input map[string]any, out any) error {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, input)
	if err != nil {
		return fmt.Errorf("stripe %s: %w", tool, err)
	}
	if res == nil || !res.Success {
		status := 0
		if res != nil {
			status = res.Status
		}
		return fmt.Errorf("stripe %s failed (HTTP %d): %s", tool, status, string(safeData(res)))
	}
	if out != nil {
		if err := json.Unmarshal(res.Data, out); err != nil {
			return fmt.Errorf("stripe %s: decode response: %w", tool, err)
		}
	}
	return nil
}

func safeData(r *sdk.ExecuteResult) []byte {
	if r == nil {
		return nil
	}
	return r.Data
}

// ─── Send payment link ──────────────────────────────────────────────

// toolInvoicesSendPaymentLink implements the v0.8.0 "agent shares a
// Stripe URL the customer pays at" flow. Creates a Stripe Checkout
// Session whose line items mirror our invoice, returns the hosted
// payment URL. The customer's eventual payment fires a
// checkout.session.completed webhook → handleStripeWebhook records
// the payment + transitions our invoice to 'paid' idempotently.
//
// Stripe Checkout Sessions have a 24h max lifetime. Calling this
// tool again on the same invoice creates a fresh session; the
// previous URL keeps working until its own expiry.
func (a *App) toolInvoicesSendPaymentLink(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invoice_id")
	if id == 0 {
		return nil, errors.New("invoice_id required")
	}
	bound, err := requireProcessor(ctx)
	if err != nil {
		return nil, err
	}

	inv, cust, err := loadInvoiceForRender(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, fmt.Errorf("invoice %d not found", id)
	}
	if inv.Status != "open" && inv.Status != "uncollectible" {
		return nil, fmt.Errorf("cannot send payment link for %s invoice — only 'open' or 'uncollectible' qualify", inv.Status)
	}
	if cust == nil || strings.TrimSpace(cust.Email) == "" {
		return nil, errors.New("invoice's customer has no email on file — set one via customers_update before sending a payment link")
	}
	if len(inv.LineItems) == 0 {
		return nil, errors.New("invoice has no line items")
	}

	// Build Checkout line items. price_data=inline so we don't need
	// a Stripe Product/Price pre-registered. unit_amount is integer
	// cents — same shape we already use internally.
	lineItems := make([]map[string]any, 0, len(inv.LineItems))
	for _, li := range inv.LineItems {
		lineItems = append(lineItems, map[string]any{
			"price_data": map[string]any{
				"currency": strings.ToLower(inv.Currency),
				"unit_amount": li.UnitPriceCents,
				"product_data": map[string]any{
					"name": li.Description,
				},
			},
			"quantity": li.Quantity,
		})
	}

	successURL := strArg(args, "success_url")
	if successURL == "" {
		successURL = configString(ctx, "stripe_success_url", "")
	}
	if successURL == "" {
		// Sensible default — the dashboard's billing panel deep link
		// for this invoice. {CHECKOUT_SESSION_ID} is replaced by
		// Stripe at redirect time.
		successURL = fmt.Sprintf(
			"/dashboard?app=billing&invoice_id=%d&stripe_session={CHECKOUT_SESSION_ID}",
			inv.ID,
		)
	}
	cancelURL := strArg(args, "cancel_url")
	if cancelURL == "" {
		cancelURL = configString(ctx, "stripe_cancel_url", "")
	}
	if cancelURL == "" {
		cancelURL = fmt.Sprintf("/dashboard?app=billing&invoice_id=%d", inv.ID)
	}

	// metadata.invoice_id is THE join key — the webhook handler reads
	// it back to find our invoice. project_id mirrors the integration
	// global-call convention so cross-project routing works.
	input := map[string]any{
		"mode":                  "payment",
		"line_items":            lineItems,
		"customer_email":        cust.Email,
		"success_url":           successURL,
		"cancel_url":            cancelURL,
		"client_reference_id":   fmt.Sprintf("inv_%d", inv.ID),
		"metadata": map[string]any{
			"apteva_invoice_id":   fmt.Sprintf("%d", inv.ID),
			"apteva_customer_id":  fmt.Sprintf("%d", inv.CustomerID),
			"apteva_project_id":   pid,
			"apteva_invoice_number": inv.Number,
		},
	}

	var sess struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := executeStripe(ctx, bound, "create_checkout_session", input, &sess); err != nil {
		return nil, err
	}
	if sess.URL == "" {
		return nil, errors.New("Stripe returned no payment URL")
	}

	// Stash the Stripe session id + url on the invoice so the panel
	// can show "payment link active" without re-creating sessions.
	// external_id / external_url are already in v0.1.0's schema
	// (originally reserved for the v0.1.1 stripe-provider path).
	now := nowRFC3339()
	if _, err := ctx.AppDB().Exec(
		`UPDATE invoices
		 SET external_id = ?, external_url = ?, last_synced_at = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`,
		sess.ID, sess.URL, now, inv.ID, pid); err != nil {
		ctx.Logger().Warn("send_payment_link: persist external_id failed", "err", err)
	}

	// Audit log entry — the agent's "I shared a link" leaves a trail.
	if tx, txErr := ctx.AppDB().Begin(); txErr == nil {
		_ = writeAuditTx(tx, inv.ID, callerActor(args), "payment_link_sent", map[string]any{
			"stripe_session_id": sess.ID,
			"stripe_url":        sess.URL,
		})
		_ = tx.Commit()
	}

	return map[string]any{
		"url":               sess.URL,
		"stripe_session_id": sess.ID,
		"expires_at":        sess.ExpiresAt,
	}, nil
}

// ─── Webhook handler ────────────────────────────────────────────────

// handleStripeWebhook is the public POST /webhooks/stripe endpoint.
// Stripe POSTs here after a payment event. We forward the raw body
// + the Stripe-Signature header to the integration's process_webhook
// tool, which verifies the signature using the webhookSecret in the
// connection credentials. On success, dispatches on event type.
//
// Idempotency is handled by dbPaymentRecord — the (method,
// external_id) unique index on payments rejects duplicates cleanly,
// and dbPaymentRecord pre-checks for the existing row so re-deliveries
// return 200 OK without side effects.
func (a *App) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	bound := ctx.IntegrationFor("payment_processor")
	if bound == nil {
		httpErr(w, http.StatusServiceUnavailable, "no payment_processor integration bound")
		return
	}
	signature := r.Header.Get("Stripe-Signature")
	if signature == "" {
		httpErr(w, http.StatusBadRequest, "missing Stripe-Signature header")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	// Verify + parse via the integration's process_webhook tool.
	// process_webhook returns the canonical Stripe Event payload
	// on success; signature failure returns success=false.
	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := executeStripe(ctx, bound, "process_webhook", map[string]any{
		"payload":   string(body),
		"signature": signature,
	}, &event); err != nil {
		httpErr(w, http.StatusBadRequest, "webhook verification failed: "+err.Error())
		return
	}

	if err := a.dispatchStripeEvent(ctx, event.ID, event.Type, event.Data.Object); err != nil {
		ctx.Logger().Warn("stripe webhook dispatch failed",
			"event_id", event.ID, "type", event.Type, "err", err.Error())
		// Return 200 anyway — Stripe retries non-200 responses, and a
		// retry doesn't help if our DB call failed for a non-transient
		// reason. We log the issue for operators to investigate.
	}

	httpJSON(w, map[string]any{"received": true, "event_id": event.ID})
}

// dispatchStripeEvent routes a verified Stripe event to the right
// handler. Unhandled event types log a notice and return nil — we
// only care about a small set of money-movement events.
func (a *App) dispatchStripeEvent(ctx *sdk.AppCtx, eventID, eventType string, obj json.RawMessage) error {
	switch eventType {
	case "checkout.session.completed":
		return a.handleCheckoutCompleted(ctx, obj)
	case "payment_intent.succeeded":
		// Reserved for non-Checkout flows (direct PaymentIntent
		// creation). Not used by v0.8.0's send-payment-link path
		// but the catalog declares the event, so handle gracefully.
		return nil
	case "charge.refunded":
		return a.handleChargeRefunded(ctx, obj)
	case "invoice.paid":
		// Stripe-mirrored invoice flow (future v0.9.0). Not used
		// by v0.8.0 — our invoices live in our DB, not Stripe.
		return nil
	default:
		ctx.Logger().Info("stripe event not handled by billing", "type", eventType, "event_id", eventID)
		return nil
	}
}

// handleCheckoutCompleted is the payment-confirmation path. The
// Stripe checkout.session.completed event fires when the customer
// finishes paying. We look up our invoice by the metadata key we
// set when creating the session, then record a method='stripe'
// payment with external_id=payment_intent_id. dbPaymentRecord is
// idempotent on (method, external_id), so webhook re-delivery is a
// no-op.
func (a *App) handleCheckoutCompleted(ctx *sdk.AppCtx, obj json.RawMessage) error {
	var sess struct {
		ID                  string            `json:"id"`
		AmountTotal         int64             `json:"amount_total"`
		Currency            string            `json:"currency"`
		PaymentIntent       string            `json:"payment_intent"`
		PaymentStatus       string            `json:"payment_status"`
		ClientReferenceID   string            `json:"client_reference_id"`
		Customer            string            `json:"customer"`
		CustomerEmail       string            `json:"customer_email"`
		Metadata            map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(obj, &sess); err != nil {
		return fmt.Errorf("decode session: %w", err)
	}
	if sess.PaymentStatus != "paid" {
		// async_payment_succeeded would be the followup for async
		// methods (SEPA, etc.). Skip until that lands.
		ctx.Logger().Info("checkout.session.completed but payment_status not 'paid'",
			"session", sess.ID, "status", sess.PaymentStatus)
		return nil
	}

	// Find our invoice. Prefer metadata.apteva_invoice_id; fall back
	// to client_reference_id (we set it to "inv_<id>").
	invoiceID := int64(0)
	if v := sess.Metadata["apteva_invoice_id"]; v != "" {
		if n := atoi64(v); n > 0 {
			invoiceID = n
		}
	}
	if invoiceID == 0 && strings.HasPrefix(sess.ClientReferenceID, "inv_") {
		if n := atoi64(strings.TrimPrefix(sess.ClientReferenceID, "inv_")); n > 0 {
			invoiceID = n
		}
	}
	if invoiceID == 0 {
		return fmt.Errorf("session %s has no apteva invoice metadata", sess.ID)
	}
	pid := sess.Metadata["apteva_project_id"]
	if pid == "" {
		return fmt.Errorf("session %s has no apteva_project_id metadata", sess.ID)
	}

	externalID := sess.PaymentIntent
	if externalID == "" {
		externalID = sess.ID // fall back to checkout session id
	}

	now := time.Now().UTC().Format(time.RFC3339)
	pay, inv, err := dbPaymentRecord(ctx.AppDB(), pid, invoiceID, sess.AmountTotal,
		"stripe", externalID, now,
		fmt.Sprintf("Stripe Checkout Session %s", sess.ID),
		"system:stripe-webhook")
	if err != nil {
		return fmt.Errorf("record payment: %w", err)
	}
	emitInvoice(ctx, "invoice.paid", inv)
	ctx.Logger().Info("stripe payment recorded",
		"invoice_id", invoiceID, "payment_id", pay.ID, "amount", sess.AmountTotal, "session", sess.ID)
	return nil
}

// handleChargeRefunded records a negative-amount payment row for a
// refund. Stripe's charge.refunded event fires with the refund
// amount in `amount_refunded` (cumulative). We record the DELTA
// from the previous refund, but for v0.8.0 simplicity we just record
// the full amount_refunded once with idempotency on the charge id.
func (a *App) handleChargeRefunded(ctx *sdk.AppCtx, obj json.RawMessage) error {
	var charge struct {
		ID             string            `json:"id"`
		PaymentIntent  string            `json:"payment_intent"`
		Amount         int64             `json:"amount"`
		AmountRefunded int64             `json:"amount_refunded"`
		Currency       string            `json:"currency"`
		Metadata       map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(obj, &charge); err != nil {
		return fmt.Errorf("decode charge: %w", err)
	}
	if charge.AmountRefunded <= 0 {
		return nil
	}
	// Find invoice via payment_intent — we recorded the PI as the
	// payment's external_id, so look it up.
	if charge.PaymentIntent == "" {
		return fmt.Errorf("charge %s has no payment_intent", charge.ID)
	}
	var invoiceID int64
	var pid string
	if err := ctx.AppDB().QueryRow(
		`SELECT invoice_id, project_id FROM payments
		 WHERE method = 'stripe' AND external_id = ?
		 LIMIT 1`,
		charge.PaymentIntent).Scan(&invoiceID, &pid); err != nil {
		return fmt.Errorf("no payment found for payment_intent %s: %w", charge.PaymentIntent, err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// Negative amount = refund record. external_id is the charge id
	// + ":refund" to dedupe re-deliveries of the same refund event.
	refundExtID := charge.ID + ":refund"
	_, inv, err := dbPaymentRecord(ctx.AppDB(), pid, invoiceID, -charge.AmountRefunded,
		"stripe", refundExtID, now,
		fmt.Sprintf("Stripe refund on %s", charge.ID),
		"system:stripe-webhook")
	if err != nil {
		return fmt.Errorf("record refund: %w", err)
	}
	emitInvoice(ctx, "invoice.refunded", inv)
	return nil
}
