package main

import (
	"database/sql"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// SaaS v0.13.0 -- saas.checkout.paid, a server-side conversion signal.
//
// Emitted once per checkout when its first invoice is paid. Free, zero-total
// and trial activations do not emit it: only a real paid invoice counts as a
// purchase. The saas_events row "checkout.paid" is the durable guard, so a
// replayed invoice.paid or a payment-recovery retry never emits twice.
// Consumers should still dedupe on idempotency_key.

const checkoutPaidTopic = "saas.checkout.paid"

func checkoutPaidRecorded(db *sql.DB, pid, checkoutID string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM saas_events WHERE project_id=? AND event_type='checkout.paid'
		AND json_extract(payload_json, '$.checkout_id')=?`, pid, checkoutID).Scan(&n)
	return n > 0, err
}

func (a *App) emitCheckoutPaid(ctx *sdk.AppCtx, pid, checkoutID string, invoice *billingInvoiceProjection) {
	if checkoutID == "" || invoice == nil || invoice.AmountPaidCents <= 0 {
		return
	}
	db := ctx.AppDB()
	if done, err := checkoutPaidRecorded(db, pid, checkoutID); err != nil || done {
		return
	}
	checkout, err := dbCheckoutGet(db, pid, checkoutID)
	if err != nil || checkout == nil {
		return
	}
	payload := map[string]any{
		"checkout_id":     checkout.ID,
		"account_id":      checkout.AccountID,
		"plan_key":        checkout.PlanKey,
		"invoice_id":      invoice.InvoiceID,
		"subscription_id": invoice.SubscriptionID,
		"amount_cents":    invoice.AmountPaidCents,
		"currency":        strings.ToUpper(invoice.Currency),
		"paid_at":         invoice.PaidAt,
		"idempotency_key": "saas:checkout_paid:" + checkout.ID,
	}
	if err := recordEvent(db, pid, checkout.AccountID, "checkout.paid", "billing", payload); err != nil {
		return
	}
	ctx.EmitWithProject(checkoutPaidTopic, pid, payload)
}

// manualPaymentMethods mirrors the methods Billing's payments_record accepts,
// plus "manual" as an alias for "other".
var manualPaymentMethods = []string{"wire", "cash", "check", "other", "stripe", "manual"}

// manualPaymentMethod checks a saas_checkout_mark_paid method before anything
// is recorded, so an invalid value fails with a clear error instead of a late
// Billing error.
func manualPaymentMethod(raw string) (string, error) {
	method := strings.ToLower(strings.TrimSpace(raw))
	switch method {
	case "":
		return "wire", nil
	case "manual":
		return "other", nil
	case "wire", "cash", "check", "other", "stripe":
		return method, nil
	}
	return "", fmt.Errorf("method %q is not supported; use wire, cash, check, other or stripe", raw)
}
