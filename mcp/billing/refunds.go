package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type RefundRequest struct {
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
	invoiceID := int64Arg(args, "invoice_id")
	key := strings.TrimSpace(strArg(args, "idempotency_key"))
	if invoiceID == 0 || key == "" {
		return nil, errors.New("invoice_id and idempotency_key required")
	}
	if len(key) > 200 {
		return nil, errors.New("idempotency_key must be at most 200 characters")
	}
	if existing, err := dbRefundByKey(ctx.AppDB(), pid, key); err != nil {
		return nil, err
	} else if existing != nil {
		return map[string]any{"refund": existing}, nil
	}
	invoice, err := dbInvoiceGetByID(ctx.AppDB(), pid, invoiceID)
	if err != nil || invoice == nil {
		return nil, firstRefundErr(err, errors.New("invoice not found"))
	}
	if invoice.AmountPaidCents <= 0 {
		return nil, errors.New("invoice has no refundable paid balance")
	}
	amount := int64Arg(args, "amount_cents")
	if amount == 0 {
		amount = invoice.AmountPaidCents
	}
	if amount <= 0 || amount > invoice.AmountPaidCents {
		return nil, errors.New("amount_cents must be positive and cannot exceed the refundable paid balance")
	}
	reason := strings.ToLower(strings.TrimSpace(strArg(args, "reason")))
	if reason == "" {
		reason = "requested_by_customer"
	}
	if reason != "duplicate" && reason != "fraudulent" && reason != "requested_by_customer" {
		return nil, errors.New("reason must be duplicate, fraudulent, or requested_by_customer")
	}
	payment, err := dbRefundableStripePayment(ctx.AppDB(), pid, invoiceID, amount)
	if err != nil || payment == nil {
		return nil, firstRefundErr(err, errors.New("invoice has no Stripe payment large enough for this refund"))
	}
	request, err := dbRefundCreate(ctx.AppDB(), pid, invoice, payment, amount, reason, key)
	if err != nil {
		return nil, err
	}
	bound, err := requireProcessor(ctx)
	if err != nil {
		_ = dbRefundFail(ctx.AppDB(), pid, request.ID, err.Error())
		return nil, err
	}
	var providerResponse struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	err = executeStripe(ctx, bound, "create_refund", map[string]any{
		"payment_intent": payment.ExternalID,
		"amount":         amount,
		"reason":         reason,
		"metadata": map[string]any{
			"apteva_project_id": pid, "apteva_invoice_id": fmt.Sprintf("%d", invoiceID),
			"apteva_refund_request_id": fmt.Sprintf("%d", request.ID), "apteva_idempotency_key": key,
		},
	}, &providerResponse)
	if err != nil {
		_ = dbRefundFail(ctx.AppDB(), pid, request.ID, err.Error())
		return nil, err
	}
	status := strings.ToLower(providerResponse.Status)
	if status == "" || status == "pending" {
		status = "submitted"
	} else if status == "succeeded" {
		status = "succeeded"
	} else {
		status = "submitted"
	}
	if err := dbRefundSubmitted(ctx.AppDB(), pid, request.ID, providerResponse.ID, status); err != nil {
		return nil, err
	}
	request, err = dbRefundByID(ctx.AppDB(), pid, request.ID)
	if err == nil {
		ctx.Emit("invoice.refund_requested", map[string]any{
			"invoice_id": invoiceID, "refund_request_id": request.ID,
			"amount_cents": amount, "currency": invoice.Currency,
		})
	}
	return map[string]any{"refund": request}, err
}

func dbRefundableStripePayment(db *sql.DB, pid string, invoiceID, amount int64) (*Payment, error) {
	row := db.QueryRow(
		`SELECT id, project_id, invoice_id, customer_id, amount_cents, currency,
		        method, external_id, received_at, notes, created_at
		   FROM payments
		  WHERE project_id=? AND invoice_id=? AND method='stripe'
		    AND amount_cents>=? AND external_id LIKE 'pi_%'
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

func dbRefundCreate(db *sql.DB, pid string, invoice *Invoice, payment *Payment, amount int64, reason, key string) (*RefundRequest, error) {
	result, err := db.Exec(
		`INSERT INTO billing_refund_requests
		   (project_id, invoice_id, payment_id, provider, provider_payment_id,
		    idempotency_key, amount_cents, currency, reason)
		 VALUES (?, ?, ?, 'stripe', ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, idempotency_key) DO NOTHING`,
		pid, invoice.ID, payment.ID, payment.ExternalID, key, amount, invoice.Currency, reason)
	if err != nil {
		return nil, err
	}
	if id, _ := result.LastInsertId(); id != 0 {
		return dbRefundByID(db, pid, id)
	}
	return dbRefundByKey(db, pid, key)
}

func dbRefundSubmitted(db *sql.DB, pid string, id int64, providerRefundID, status string) error {
	completed := ""
	if status == "succeeded" {
		completed = ", completed_at=CURRENT_TIMESTAMP"
	}
	_, err := db.Exec(
		`UPDATE billing_refund_requests
		    SET provider_refund_id=?, status=?, error='', updated_at=CURRENT_TIMESTAMP`+completed+`
		  WHERE project_id=? AND id=?`,
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
		status, error, created_at, updated_at, completed_at
		FROM billing_refund_requests`
}

func scanRefund(scanner rowScanner) (*RefundRequest, error) {
	var refund RefundRequest
	var completed sql.NullString
	if err := scanner.Scan(
		&refund.ID, &refund.InvoiceID, &refund.PaymentID, &refund.Provider,
		&refund.ProviderPaymentID, &refund.ProviderRefundID, &refund.IdempotencyKey,
		&refund.AmountCents, &refund.Currency, &refund.Reason, &refund.Status,
		&refund.Error, &refund.CreatedAt, &refund.UpdatedAt, &completed,
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
