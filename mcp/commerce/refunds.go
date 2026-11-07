package main

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Refund intents freeze the resolved amount before contacting Billing. A retry
// of a full refund must never become a new refund of the remaining balance.
type SaleRefundRequest struct {
	ID             int64          `json:"id"`
	SaleID         int64          `json:"sale_id"`
	InvoiceID      int64          `json:"invoice_id"`
	AmountCents    int64          `json:"amount_cents"`
	Currency       string         `json:"currency"`
	Reason         string         `json:"reason"`
	IdempotencyKey string         `json:"idempotency_key"`
	BillingKey     string         `json:"-"`
	Status         string         `json:"status"`
	Refund         map[string]any `json:"refund,omitempty"`
	ExecutionError string         `json:"execution_error,omitempty"`
}

type SaleRefundResult struct {
	Request             *SaleRefundRequest `json:"request"`
	Sale                *Sale              `json:"sale"`
	Invoice             map[string]any     `json:"invoice,omitempty"`
	Reconciled          bool               `json:"reconciled"`
	ReconciliationError string             `json:"reconciliation_error,omitempty"`
}

func refundPositiveInteger(args map[string]any, key string) (int64, error) {
	n, ok := numberValue(args[key])
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 9007199254740991 || math.Trunc(n) != n {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return int64(n), nil
}

func (a *App) toolSaleRefund(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	saleID, err := refundPositiveInteger(args, "sale_id")
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(strArg(args, "idempotency_key"))
	if key == "" || len(key) > 200 {
		return nil, errors.New("idempotency_key required (max 200 characters)")
	}
	reason := firstNonEmpty(strings.TrimSpace(strArg(args, "reason")), "requested_by_customer")
	if reason != "requested_by_customer" && reason != "duplicate" && reason != "fraudulent" {
		return nil, errors.New("invalid refund reason")
	}
	var amount int64
	_, explicitAmount := args["amount_cents"]
	if explicitAmount {
		amount, err = refundPositiveInteger(args, "amount_cents")
		if err != nil {
			return nil, err
		}
	}
	sale, err := dbSaleGet(ctx.AppDB(), pid, saleID)
	if err != nil || sale == nil {
		return nil, firstErr(err, errors.New("sale not found"))
	}
	if sale.InvoiceID == nil || *sale.InvoiceID <= 0 {
		return nil, errors.New("sale has no Billing invoice")
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	ctx = ctx.WithProject(pid)
	request, err := dbSaleRefundByKey(ctx.AppDB(), pid, key)
	if err != nil {
		return nil, err
	}
	if request == nil {
		if sale.PaymentStatus != "paid" && sale.PaymentStatus != "partially_refunded" {
			return nil, errors.New("sale must have a refundable payment")
		}
		invoice, err := saleRefundInvoice(ctx, pid, sale)
		if err != nil {
			return nil, err
		}
		// Import previously completed refunds before computing the remaining amount.
		if err := reconcileSaleBillingInvoice(ctx, pid, sale, invoice); err != nil {
			return nil, err
		}
		sale, err = dbSaleGet(ctx.AppDB(), pid, saleID)
		if err != nil {
			return nil, err
		}
		remaining := sale.TotalCents - sale.RefundedCents
		if paid := intArg(invoice, "amount_paid_cents"); paid < remaining {
			remaining = paid
		}
		if !explicitAmount {
			amount = remaining
		}
		if amount <= 0 || amount > remaining {
			return nil, errors.New("refund exceeds remaining paid sale balance")
		}
		// Billing currently executes Stripe refunds only. Do not mistake recording a
		// manual payment adjustment for returning money to the customer.
		stripePaid := false
		for _, raw := range anySlice(invoice["payments"]) {
			payment := anyMap(raw)
			if strArg(payment, "method") == "stripe" && intArg(payment, "amount_cents") > 0 {
				stripePaid = true
			}
		}
		if !stripePaid {
			return nil, errors.New("refund execution requires an original Stripe payment")
		}
		billingKey := fmt.Sprintf("commerce:refund:%x", sha256.Sum256([]byte(pid+"\x00"+key)))
		_, err = ctx.AppDB().Exec(`INSERT INTO commerce_sale_refund_requests
   (project_id,sale_id,invoice_id,amount_cents,currency,reason,idempotency_key,billing_key)
   VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(project_id,idempotency_key) DO NOTHING`,
			pid, sale.ID, *sale.InvoiceID, amount, sale.Currency, reason, key, billingKey)
		if err != nil {
			return nil, err
		}
		request, err = dbSaleRefundByKey(ctx.AppDB(), pid, key)
		if err != nil {
			return nil, err
		}
	}
	if request.SaleID != sale.ID || request.InvoiceID != *sale.InvoiceID || request.Currency != sale.Currency ||
		(explicitAmount && amount != request.AmountCents) || reason != request.Reason {
		return nil, errors.New("refund idempotency key reused with different parameters")
	}
	if request.Status != "succeeded" && request.Status != "failed" {
		var response map[string]any
		callErr := ctx.PlatformAPI().CallAppResult("billing", "invoices_refund", map[string]any{
			"_project_id": pid, "invoice_id": request.InvoiceID, "amount_cents": request.AmountCents,
			"reason": request.Reason, "idempotency_key": request.BillingKey,
		}, &response)
		refund := mapArg(response, "refund")
		if callErr == nil {
			status := strArg(refund, "status")
			if intArg(refund, "id") <= 0 || intArg(refund, "invoice_id") != request.InvoiceID ||
				intArg(refund, "amount_cents") != request.AmountCents || !strings.EqualFold(strArg(refund, "currency"), request.Currency) ||
				strArg(refund, "idempotency_key") != request.BillingKey ||
				(status != "pending" && status != "submitted" && status != "succeeded" && status != "failed") {
				callErr = errors.New("Billing returned an invalid refund result")
			}
		}
		if callErr != nil {
			// An error can occur after the processor has accepted a refund. Preserve
			// the intent and return an unknown outcome; retry only with this same key.
			request.Status = "unknown"
			request.ExecutionError = callErr.Error()
		} else {
			request.Refund = refund
			request.Status = strArg(refund, "status")
			request.ExecutionError = ""
		}
		// Never downgrade a concurrent terminal success to an ambiguous retry.
		_, err = ctx.AppDB().Exec(`UPDATE commerce_sale_refund_requests SET status=?, refund_json=?, execution_error=?, updated_at=CURRENT_TIMESTAMP
   WHERE project_id=? AND id=? AND status NOT IN ('succeeded','failed')`, request.Status, jsonText(request.Refund, "{}"), request.ExecutionError, pid, request.ID)
		if err == nil {
			var saved *SaleRefundRequest
			saved, err = dbSaleRefundByKey(ctx.AppDB(), pid, key)
			if err == nil && saved != nil {
				request = saved
			}
		}
		if err != nil {
			// Execution already happened: a persistence failure must remain visible
			// without discarding the known processor result.
			return &SaleRefundResult{Request: request, Sale: sale, ReconciliationError: "save refund outcome: " + err.Error()}, nil
		}
	}
	result := &SaleRefundResult{Request: request, Sale: sale}
	invoice, err := saleRefundInvoice(ctx, pid, sale)
	if err == nil {
		result.Invoice = invoice
		err = reconcileSaleBillingInvoice(ctx, pid, sale, invoice)
	}
	if err == nil {
		result.Sale, err = dbSaleGet(ctx.AppDB(), pid, sale.ID)
	}
	result.Reconciled = err == nil
	if err != nil {
		result.ReconciliationError = err.Error()
	}
	return result, nil
}

func saleRefundInvoice(ctx *sdk.AppCtx, pid string, sale *Sale) (map[string]any, error) {
	var response map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_get", map[string]any{"_project_id": pid, "id": *sale.InvoiceID}, &response); err != nil {
		return nil, fmt.Errorf("load Billing invoice: %w", err)
	}
	invoice := mapArg(response, "invoice")
	if intArg(invoice, "id") != *sale.InvoiceID || strArg(invoice, "project_id") != pid {
		return nil, errors.New("Billing invoice does not belong to the sale project")
	}
	if !strings.EqualFold(strArg(invoice, "currency"), sale.Currency) {
		return nil, errors.New("Billing invoice currency does not match sale currency")
	}
	return invoice, nil
}

func dbSaleRefundByKey(db *sql.DB, pid, key string) (*SaleRefundRequest, error) {
	var r SaleRefundRequest
	var raw string
	err := db.QueryRow(`SELECT id,sale_id,invoice_id,amount_cents,currency,reason,idempotency_key,billing_key,status,refund_json,execution_error
  FROM commerce_sale_refund_requests WHERE project_id=? AND idempotency_key=?`, pid, key).Scan(
		&r.ID, &r.SaleID, &r.InvoiceID, &r.AmountCents, &r.Currency, &r.Reason, &r.IdempotencyKey, &r.BillingKey, &r.Status, &raw, &r.ExecutionError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Refund = jsonMap(raw)
	return &r, nil
}

func (a *App) toolSaleRefundsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	saleID, err := refundPositiveInteger(args, "sale_id")
	if err != nil {
		return nil, err
	}
	sale, err := dbSaleGet(ctx.AppDB(), pid, saleID)
	if err != nil || sale == nil {
		return nil, firstErr(err, errors.New("sale not found"))
	}
	rows, err := ctx.AppDB().Query(`SELECT idempotency_key FROM commerce_sale_refund_requests WHERE project_id=? AND sale_id=? ORDER BY id DESC`, pid, saleID)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return nil, rowErr
	}
	requests := []*SaleRefundRequest{}
	for _, key := range keys {
		request, err := dbSaleRefundByKey(ctx.AppDB(), pid, key)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return map[string]any{"requests": requests, "count": len(requests)}, nil
}
