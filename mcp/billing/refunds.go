package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type RefundRequest struct {
	ReopenInvoice     bool   `json:"reopen_invoice"`
	ID                int64  `json:"id"`
	InvoiceID         int64  `json:"invoice_id"`
	PaymentID         int64  `json:"payment_id"`
	Provider          string `json:"provider"`
	ProviderPaymentID string `json:"provider_payment_id"`
	ProviderRefundID  string `json:"provider_refund_id,omitempty"`
	IdempotencyKey    string `json:"idempotency_key"`
	AmountCents       int64  `json:"amount_cents"`
	Currency          string `json:"currency"`
	Reason            string `json:"reason"`
	Status            string `json:"status"`
	Error             string `json:"error,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	CompletedAt       string `json:"completed_at,omitempty"`
}

func (a *App) toolInvoicesRefund(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	iid := int64Arg(args, "invoice_id")
	key := strings.TrimSpace(strArg(args, "idempotency_key"))
	if iid <= 0 || key == "" || len(key) > 200 {
		return nil, errors.New("invoice_id and idempotency_key (max 200 characters) required")
	}
	unlock := operationLock(fmt.Sprintf("refund-invoice:%s:%d", pid, iid))
	defer unlock()
	amount := int64Arg(args, "amount_cents")
	reason := firstString(strArg(args, "reason"), "requested_by_customer")
	if requestReason := strArg(args, "reason"); requestReason == "" {
		if old, e := dbRefundByKey(ctx.AppDB(), pid, key); e == nil && old != nil {
			reason = old.Reason
		}
	}
	if reason != "duplicate" && reason != "fraudulent" && reason != "requested_by_customer" {
		return nil, errors.New("invalid refund reason")
	}
	request, err := dbRefundByKey(ctx.AppDB(), pid, key)
	if err != nil {
		return nil, err
	}
	if request != nil {
		if request.InvoiceID != iid || (amount != 0 && amount != request.AmountCents) || reason != request.Reason || (args["reopen_invoice"] != nil && boolFromArg(args, "reopen_invoice") != request.ReopenInvoice) {
			return nil, errors.New("refund key reused with different parameters")
		}
		if request.Status == "succeeded" || request.Status == "failed" {
			return map[string]any{"refund": request}, nil
		}
	} else {
		inv, err := dbInvoiceGetByID(ctx.AppDB(), pid, iid)
		if err != nil {
			return nil, err
		}
		if inv == nil {
			return nil, errors.New("invoice not found")
		}
		if amount == 0 {
			amount = inv.AmountPaidCents
		}
		if amount <= 0 || amount > inv.AmountPaidCents {
			return nil, errors.New("refund exceeds paid balance")
		}
		payment, err := dbRefundableStripePayment(ctx.AppDB(), pid, iid, amount)
		if err != nil {
			return nil, err
		}
		if payment == nil {
			return nil, errors.New("no original Stripe payment has sufficient unreserved refundable balance")
		}
		request, err = dbRefundCreate(ctx.AppDB(), pid, inv, payment, amount, reason, key, boolFromArg(args, "reopen_invoice"))
		if err != nil {
			return nil, err
		}
	}
	bound, err := requireProcessor(ctx)
	if err != nil {
		return nil, err
	}
	if err = requirePaymentConnection(ctx, bound, request); err != nil {
		return nil, err
	}
	var result map[string]any
	err = executeStripe(ctx, bound, "create_refund", map[string]any{"payment_intent": request.ProviderPaymentID, "amount": request.AmountCents, "reason": request.Reason, "idempotency_key": key, "metadata": map[string]any{"apteva_project_id": pid, "apteva_invoice_id": fmt.Sprint(iid), "apteva_refund_request_id": fmt.Sprint(request.ID), "apteva_idempotency_key": key}}, &result)
	if err != nil {
		ctx.AppDB().Exec("UPDATE billing_refund_requests SET error=? WHERE id=?", err.Error(), request.ID)
		return nil, err
	}
	id := strArg(result, "id")
	if id == "" {
		return nil, errors.New("provider returned no refund id")
	}
	status := "submitted"
	switch strArg(result, "status") {
	case "succeeded":
		status = "submitted"
	case "failed", "canceled":
		status = "failed"
	}
	if err = dbRefundSubmitted(ctx.AppDB(), pid, request.ID, id, status); err != nil {
		return nil, err
	}
	result["payment_intent"] = request.ProviderPaymentID
	result["amount"] = request.AmountCents
	result["currency"] = strings.ToLower(request.Currency)
	raw, _ := json.Marshal(result)
	if err = a.handleRefundObject(ctx, raw); err != nil {
		return nil, err
	}
	request, err = dbRefundByID(ctx.AppDB(), pid, request.ID)
	return map[string]any{"refund": request}, err
}

func dbRefundableStripePayment(db *sql.DB, pid string, invoiceID, amount int64) (*Payment, error) {
	row := db.QueryRow(
		`SELECT id, project_id, invoice_id, customer_id, amount_cents, currency,
		        method, external_id, received_at, notes, created_at
		   FROM payments
		  WHERE project_id=? AND invoice_id=? AND method='stripe'
		    AND amount_cents - COALESCE((SELECT -SUM(r.amount_cents) FROM payments r WHERE r.source_payment_id=payments.id),0)
 - COALESCE((SELECT SUM(rr.amount_cents) FROM billing_refund_requests rr WHERE rr.payment_id=payments.id AND rr.status IN ('pending','submitted')),0)>=? AND external_id LIKE 'pi_%'
		  ORDER BY amount_cents ASC, received_at DESC, id DESC LIMIT 1`,
		pid, invoiceID, amount)
	var payment Payment
	var nullableInvoice sql.NullInt64
	var externalID, notes sql.NullString
	if err := row.Scan(
		&payment.ID, &payment.ProjectID, &nullableInvoice, &payment.CustomerID,
		&payment.AmountCents, &payment.Currency, &payment.Method, &externalID,
		&payment.ReceivedAt, &notes, &payment.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if nullableInvoice.Valid {
		value := nullableInvoice.Int64
		payment.InvoiceID = &value
	}
	payment.ExternalID = externalID.String
	payment.Notes = notes.String
	return &payment, nil
}

func dbRefundCreate(db *sql.DB, pid string, invoice *Invoice, payment *Payment, amount int64, reason, key string, reopen ...bool) (*RefundRequest, error) {
	reopenInvoice := len(reopen) > 0 && reopen[0]
	result, err := db.Exec(
		`INSERT INTO billing_refund_requests
		   (project_id, invoice_id, payment_id, provider, provider_payment_id,
		    idempotency_key, amount_cents, currency, reason, reopen_invoice)
		 SELECT ?, ?, ?, 'stripe', ?, ?, ?, ?, ?, ?
 WHERE (SELECT amount_cents-COALESCE((SELECT -SUM(amount_cents) FROM payments r WHERE r.source_payment_id=p.id),0)-COALESCE((SELECT SUM(amount_cents) FROM billing_refund_requests rr WHERE rr.payment_id=p.id AND status IN ('pending','submitted')),0) FROM payments p WHERE p.id=?)>=?
 AND (SELECT amount_paid_cents-COALESCE((SELECT SUM(amount_cents) FROM billing_refund_requests rr WHERE rr.invoice_id=i.id AND status IN ('pending','submitted')),0) FROM invoices i WHERE i.id=?)>=?
		 ON CONFLICT(project_id, idempotency_key) DO NOTHING`,
		pid, invoice.ID, payment.ID, payment.ExternalID, key, amount, invoice.Currency, reason, boolInt(reopenInvoice), payment.ID, amount, invoice.ID, amount)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 1 {
		id, _ := result.LastInsertId()
		return dbRefundByID(db, pid, id)
	}
	existing, err := dbRefundByKey(db, pid, key)
	if err == nil && existing == nil {
		return nil, errors.New("refundable balance was reserved concurrently")
	}
	return existing, err
}

func dbRefundSubmitted(db *sql.DB, pid string, id int64, providerRefundID, status string) error {
	completed := ""
	if status == "succeeded" {
		completed = ", completed_at=CURRENT_TIMESTAMP"
	}
	_, err := db.Exec(
		`UPDATE billing_refund_requests
		    SET provider_refund_id=?, status=?, error='', updated_at=CURRENT_TIMESTAMP`+completed+`
		  WHERE project_id=? AND id=? AND status<>'succeeded'`,
		providerRefundID, status, pid, id)
	return err
}

func dbRefundFail(db *sql.DB, pid string, id int64, message string) error {
	_, err := db.Exec(
		`UPDATE billing_refund_requests SET status='failed', error=?, updated_at=CURRENT_TIMESTAMP
		 WHERE project_id=? AND id=?`, message, pid, id)
	return err
}

func dbRefundByID(db *sql.DB, pid string, id int64) (*RefundRequest, error) {
	return scanRefund(db.QueryRow(refundSelect()+` WHERE project_id=? AND id=?`, pid, id))
}

func dbRefundByKey(db *sql.DB, pid, key string) (*RefundRequest, error) {
	refund, err := scanRefund(db.QueryRow(refundSelect()+` WHERE project_id=? AND idempotency_key=?`, pid, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return refund, err
}

func refundSelect() string {
	return `SELECT id, invoice_id, payment_id, provider, provider_payment_id,
		provider_refund_id, idempotency_key, amount_cents, currency, reason,
		status, error, created_at, updated_at, completed_at, reopen_invoice
		FROM billing_refund_requests`
}

func scanRefund(scanner rowScanner) (*RefundRequest, error) {
	var refund RefundRequest
	var completed sql.NullString
	if err := scanner.Scan(
		&refund.ID, &refund.InvoiceID, &refund.PaymentID, &refund.Provider,
		&refund.ProviderPaymentID, &refund.ProviderRefundID, &refund.IdempotencyKey,
		&refund.AmountCents, &refund.Currency, &refund.Reason, &refund.Status,
		&refund.Error, &refund.CreatedAt, &refund.UpdatedAt, &completed, &refund.ReopenInvoice,
	); err != nil {
		return nil, err
	}
	refund.CompletedAt = completed.String
	return &refund, nil
}

func firstRefundErr(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}
