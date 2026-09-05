package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

type stripeRefund struct {
	ID            string `json:"id"`
	PaymentIntent string `json:"payment_intent"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	Created       int64  `json:"created"`
}

func (a *App) storeRefundIdentity(ctx *sdk.AppCtx, raw json.RawMessage) error {
	var r stripeRefund
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if r.ID == "" || r.PaymentIntent == "" || r.Amount <= 0 || r.Amount > maxMoney {
		return errors.New("invalid provider refund")
	}
	status := r.Status
	if status == "" {
		status = "succeeded"
	}
	var pi string
	var amount int64
	err := ctx.AppDB().QueryRow("SELECT provider_payment_id,amount_cents FROM billing_provider_refunds WHERE id=?", r.ID).Scan(&pi, &amount)
	if err == nil && (pi != r.PaymentIntent || amount != r.Amount) {
		return errors.New("refund identity changed")
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO billing_provider_refunds(id,provider_payment_id,amount_cents,status,received_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=CASE WHEN billing_provider_refunds.status='succeeded' THEN 'succeeded' ELSE excluded.status END`, r.ID, r.PaymentIntent, r.Amount, status, providerReceivedAt(r.Created))
	return err
}
func (a *App) handleRefundObject(ctx *sdk.AppCtx, raw json.RawMessage) error {
	if err := a.storeRefundIdentity(ctx, raw); err != nil {
		return err
	}
	var r stripeRefund
	json.Unmarshal(raw, &r)
	if err := a.reconcileRefundTotal(ctx, r.PaymentIntent, r.Currency, "", 0); err != nil {
		return err
	}
	status := r.Status
	if status == "" {
		status = "succeeded"
	}
	if status == "canceled" {
		status = "failed"
	}
	if status != "succeeded" && status != "failed" {
		status = "submitted"
	}
	_, err := ctx.AppDB().Exec(`UPDATE billing_refund_requests SET status=CASE WHEN status='succeeded' THEN status ELSE ? END,completed_at=CASE WHEN ?='succeeded' THEN COALESCE(completed_at,CURRENT_TIMESTAMP) ELSE completed_at END,updated_at=CURRENT_TIMESTAMP WHERE provider_refund_id=?`, status, status, r.ID)
	if err != nil {
		return err
	}
	if status == "succeeded" {
		if _, err = ctx.AppDB().Exec(`UPDATE invoices SET collection_hold=0 WHERE id IN(SELECT invoice_id FROM billing_refund_requests WHERE provider_refund_id=? AND reopen_invoice=1 AND status='succeeded') AND NOT EXISTS(SELECT 1 FROM billing_refund_requests rr WHERE rr.invoice_id=invoices.id AND rr.status IN ('pending','submitted')) AND COALESCE((SELECT -SUM(amount_cents) FROM payments WHERE invoice_id=invoices.id AND amount_cents<0),0) = COALESCE((SELECT SUM(amount_cents) FROM billing_refund_requests WHERE invoice_id=invoices.id AND reopen_invoice=1 AND status='succeeded'),0)`, r.ID); err != nil {
			return err
		}
	}
	if status == "succeeded" || status == "failed" {
		return finishProviderOperation(ctx.AppDB(), r.ID)
	}
	return nil
}
func (a *App) reconcileRefundTotal(ctx *sdk.AppCtx, pi, currency, chargeID string, observed int64) error {
	unlock := operationLock("refund-reconcile:" + pi)
	defer unlock()
	db := ctx.AppDB()
	var sourceID, iid, original int64
	var pid, cur string
	if err := db.QueryRow(`SELECT id,invoice_id,project_id,currency,amount_cents FROM payments WHERE method='stripe' AND external_id=? AND amount_cents>0`, pi).Scan(&sourceID, &iid, &pid, &cur, &original); err != nil {
		return fmt.Errorf("refund awaiting original payment: %w", err)
	}
	if currency != "" && !strings.EqualFold(cur, currency) {
		return errors.New("refund currency mismatch")
	}
	if observed < 0 || observed > original {
		return errors.New("refund total exceeds original payment")
	}
	// Associate pre-upgrade charge refund rows, preserving already recorded money.
	if chargeID != "" {
		if _, err := db.Exec(`UPDATE payments SET source_payment_id=? WHERE invoice_id=? AND method='stripe' AND amount_cents<0 AND source_payment_id IS NULL AND (external_id=? OR substr(external_id,1,?)=?)`, sourceID, iid, chargeID+":refund", len(chargeID+":refund:"), chargeID+":refund:"); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`INSERT INTO billing_refund_reconciliation(provider_payment_id,project_id,invoice_id,observed_cents) VALUES(?,?,?,?) ON CONFLICT(provider_payment_id) DO UPDATE SET observed_cents=max(observed_cents,excluded.observed_cents)`, pi, pid, iid, observed); err != nil {
		return err
	}
	var target, recorded int64
	if err := db.QueryRow(`SELECT max(observed_cents,COALESCE((SELECT SUM(amount_cents) FROM billing_provider_refunds WHERE provider_payment_id=? AND status='succeeded'),0)) FROM billing_refund_reconciliation WHERE provider_payment_id=?`, pi, pi).Scan(&target); err != nil {
		return err
	}
	if target > original {
		return errors.New("provider refund identities exceed original payment")
	}
	if err := db.QueryRow(`SELECT COALESCE(-SUM(amount_cents),0) FROM payments WHERE source_payment_id=? AND amount_cents<0`, sourceID).Scan(&recorded); err != nil {
		return err
	}
	if target > recorded {
		_, _, err := dbPaymentRecordSource(db, pid, iid, recorded-target, "stripe", fmt.Sprintf("%s:refund-total:%d", pi, target), providerReceivedAt(0), "Stripe refund reconciliation", "system:stripe-webhook", sourceID)
		if err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE billing_refund_reconciliation SET recorded_cents=max(recorded_cents,?) WHERE provider_payment_id=?`, target, pi); err != nil {
		return err
	}
	return nil
}
