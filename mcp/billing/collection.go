package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// CollectionAttempt is Billing's durable record of one automatic invoice
// collection request. Product apps provide a stable idempotency key; Billing
// owns the provider call and webhook reconciliation.
type CollectionAttempt struct {
	ID                      int64           `json:"id"`
	ProjectID               string          `json:"project_id,omitempty"`
	InvoiceID               int64           `json:"invoice_id"`
	PaymentMethodID         int64           `json:"payment_method_id,omitempty"`
	Provider                string          `json:"provider"`
	ProviderPaymentIntentID string          `json:"provider_payment_intent_id,omitempty"`
	IdempotencyKey          string          `json:"idempotency_key"`
	AmountCents             int64           `json:"amount_cents"`
	Currency                string          `json:"currency"`
	Status                  string          `json:"status"`
	ErrorCode               string          `json:"error_code,omitempty"`
	ErrorMessage            string          `json:"error_message,omitempty"`
	NextAction              json.RawMessage `json:"next_action,omitempty"`
	CreatedAt               string          `json:"created_at,omitempty"`
	UpdatedAt               string          `json:"updated_at,omitempty"`
	CompletedAt             string          `json:"completed_at,omitempty"`
}

type stripePaymentIntent struct {
	ID            string          `json:"id"`
	Status        string          `json:"status"`
	Amount        int64           `json:"amount"`
	Currency      string          `json:"currency"`
	PaymentMethod string          `json:"payment_method"`
	NextAction    json.RawMessage `json:"next_action"`
	LastError     struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"last_payment_error"`
}

func (a *App) toolInvoicesCollect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	invoiceID := int64Arg(args, "invoice_id")
	if invoiceID == 0 {
		return nil, errors.New("invoice_id required")
	}
	idempotencyKey := strings.TrimSpace(strArg(args, "idempotency_key"))
	if idempotencyKey == "" {
		return nil, errors.New("idempotency_key required")
	}
	if len(idempotencyKey) > 255 {
		return nil, errors.New("idempotency_key must be at most 255 characters")
	}
	if existing, err := dbCollectionAttemptByKey(ctx.AppDB(), pid, idempotencyKey); err != nil {
		return nil, err
	} else if existing != nil {
		if existing.InvoiceID != invoiceID {
			return nil, errors.New("idempotency_key was already used for a different invoice")
		}
		return map[string]any{"collection_attempt": existing, "replayed": true}, nil
	}

	inv, cust, err := loadInvoiceForRender(ctx.AppDB(), pid, invoiceID)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, fmt.Errorf("invoice %d not found", invoiceID)
	}
	if inv.Status != "open" && inv.Status != "uncollectible" {
		return nil, fmt.Errorf("cannot collect %s invoice", inv.Status)
	}
	amountDue := inv.TotalCents - inv.AmountPaidCents
	if amountDue <= 0 {
		return nil, errors.New("invoice has no outstanding balance")
	}

	var pm *PaymentMethod
	if paymentMethodID := int64Arg(args, "payment_method_id"); paymentMethodID != 0 {
		pm, err = dbPaymentMethodGet(ctx.AppDB(), pid, paymentMethodID)
	} else {
		pm, err = dbDefaultPaymentMethod(ctx.AppDB(), pid, inv.CustomerID)
	}
	if err != nil {
		return nil, err
	}
	if pm == nil {
		return nil, errors.New("customer has no reusable payment method")
	}
	if pm.CustomerID != inv.CustomerID {
		return nil, errors.New("payment method belongs to a different customer")
	}
	if pm.Status != "active" || !pm.Reusable {
		return nil, errors.New("payment method is not active and reusable")
	}
	if pm.Provider != "stripe" || pm.ProviderPaymentMethodID == "" {
		return nil, errors.New("automatic collection currently requires a Stripe payment method")
	}

	attempt, created, err := dbCollectionAttemptClaim(
		ctx.AppDB(), pid, invoiceID, pm.ID, idempotencyKey, amountDue, inv.Currency,
	)
	if err != nil {
		return nil, err
	}
	if !created || attempt.Status != "pending" {
		return map[string]any{"collection_attempt": attempt, "replayed": true}, nil
	}

	bound, err := requireProcessor(ctx)
	if err != nil {
		_ = dbCollectionAttemptFail(ctx.AppDB(), attempt.ID, "processor_unavailable", err.Error(), nil)
		return nil, err
	}
	if err := ensureStripeWebhook(ctx, bound); err != nil {
		_ = dbCollectionAttemptFail(ctx.AppDB(), attempt.ID, "webhook_unavailable", err.Error(), nil)
		return nil, fmt.Errorf("stripe webhook setup: %w", err)
	}
	stripeCustomerID := pm.ProviderCustomerID
	if stripeCustomerID == "" {
		if cust == nil {
			return nil, errors.New("invoice customer not found")
		}
		stripeCustomerID, err = ensureStripeCustomer(ctx, pid, cust, bound)
		if err != nil {
			_ = dbCollectionAttemptFail(ctx.AppDB(), attempt.ID, "customer_setup_failed", err.Error(), nil)
			return nil, err
		}
	}

	input := map[string]any{
		"amount":          amountDue,
		"currency":        strings.ToLower(inv.Currency),
		"customer":        stripeCustomerID,
		"payment_method":  pm.ProviderPaymentMethodID,
		"confirm":         true,
		"off_session":     true,
		"description":     firstString(inv.Number, fmt.Sprintf("Invoice #%d", inv.ID)),
		"idempotency_key": idempotencyKey,
		"metadata": map[string]any{
			"apteva_project_id":         pid,
			"apteva_invoice_id":         fmt.Sprintf("%d", inv.ID),
			"apteva_customer_id":        fmt.Sprintf("%d", inv.CustomerID),
			"apteva_collection_attempt": fmt.Sprintf("%d", attempt.ID),
			"apteva_idempotency_key":    idempotencyKey,
		},
	}
	var intent stripePaymentIntent
	if err := executeStripe(ctx, bound, "create_payment_intent", input, &intent); err != nil {
		_ = dbCollectionAttemptFail(ctx.AppDB(), attempt.ID, "processor_error", err.Error(), nil)
		failed, _ := dbCollectionAttemptGet(ctx.AppDB(), pid, attempt.ID)
		emitCollectionFailure(ctx, "invoice.payment_failed", inv, failed)
		return map[string]any{"collection_attempt": failed}, nil
	}
	if intent.ID == "" {
		err := errors.New("Stripe returned no payment intent id")
		_ = dbCollectionAttemptFail(ctx.AppDB(), attempt.ID, "invalid_processor_response", err.Error(), nil)
		return nil, err
	}
	if intent.Amount != 0 && intent.Amount != amountDue {
		return nil, fmt.Errorf("Stripe payment intent amount mismatch: got %d want %d", intent.Amount, amountDue)
	}
	if intent.Currency != "" && !strings.EqualFold(intent.Currency, inv.Currency) {
		return nil, fmt.Errorf("Stripe payment intent currency mismatch: got %s want %s", intent.Currency, inv.Currency)
	}

	attempt, err = a.applyCollectionIntent(ctx, pid, inv, attempt.ID, &intent)
	if err != nil {
		return nil, err
	}
	return map[string]any{"collection_attempt": attempt}, nil
}

func (a *App) applyCollectionIntent(ctx *sdk.AppCtx, pid string, inv *Invoice, attemptID int64, intent *stripePaymentIntent) (*CollectionAttempt, error) {
	if intent == nil {
		return nil, errors.New("payment intent required")
	}
	switch intent.Status {
	case "succeeded":
		if _, _, err := dbPaymentRecord(
			ctx.AppDB(), pid, inv.ID, inv.TotalCents-inv.AmountPaidCents,
			"stripe", intent.ID, nowRFC3339(),
			"Automatic Stripe collection", "system:stripe-collection",
		); err != nil {
			return nil, err
		}
		if err := dbCollectionAttemptSucceed(ctx.AppDB(), attemptID, intent.ID); err != nil {
			return nil, err
		}
		paid, err := dbInvoiceGetByID(ctx.AppDB(), pid, inv.ID)
		if err != nil {
			return nil, err
		}
		emitInvoice(ctx, "invoice.paid", paid)
	case "requires_action":
		if err := dbCollectionAttemptUpdate(
			ctx.AppDB(), attemptID, intent.ID, "failed",
			firstString(intent.LastError.Code, "authentication_required"),
			firstString(intent.LastError.Message, "customer authentication is required"),
			intent.NextAction, true,
		); err != nil {
			return nil, err
		}
		current, _ := dbCollectionAttemptGet(ctx.AppDB(), pid, attemptID)
		emitCollectionFailure(ctx, "invoice.payment_action_required", inv, current)
	case "requires_payment_method", "canceled":
		if err := dbCollectionAttemptUpdate(
			ctx.AppDB(), attemptID, intent.ID, "failed",
			firstString(intent.LastError.Code, intent.Status),
			firstString(intent.LastError.Message, "automatic payment failed"),
			intent.NextAction, true,
		); err != nil {
			return nil, err
		}
		current, _ := dbCollectionAttemptGet(ctx.AppDB(), pid, attemptID)
		emitCollectionFailure(ctx, "invoice.payment_failed", inv, current)
	default:
		if err := dbCollectionAttemptUpdate(
			ctx.AppDB(), attemptID, intent.ID, firstString(intent.Status, "processing"), "", "", intent.NextAction, false,
		); err != nil {
			return nil, err
		}
	}
	return dbCollectionAttemptGet(ctx.AppDB(), pid, attemptID)
}

func emitCollectionFailure(ctx *sdk.AppCtx, topic string, inv *Invoice, attempt *CollectionAttempt) {
	if ctx == nil || inv == nil || attempt == nil {
		return
	}
	ctx.EmitWithProject(topic, inv.ProjectID, map[string]any{
		"id":                 inv.ID,
		"customer_id":        inv.CustomerID,
		"number":             inv.Number,
		"status":             inv.Status,
		"total_cents":        inv.TotalCents,
		"currency":           inv.Currency,
		"collection_attempt": attempt,
	})
}

func dbCollectionAttemptClaim(db *sql.DB, pid string, invoiceID, paymentMethodID int64, key string, amount int64, currency string) (*CollectionAttempt, bool, error) {
	res, err := db.Exec(
		`INSERT OR IGNORE INTO billing_collection_attempts
		   (project_id, invoice_id, payment_method_id, provider, idempotency_key,
		    amount_cents, currency, status, next_action, created_at, updated_at)
		 VALUES (?, ?, ?, 'stripe', ?, ?, ?, 'pending', '{}', ?, ?)`,
		pid, invoiceID, paymentMethodID, key, amount, currency, nowRFC3339(), nowRFC3339(),
	)
	if err != nil {
		return nil, false, err
	}
	n, _ := res.RowsAffected()
	attempt, err := dbCollectionAttemptByKey(db, pid, key)
	if err != nil {
		return nil, false, err
	}
	if attempt == nil {
		return nil, false, errors.New("failed to claim collection attempt")
	}
	if attempt.InvoiceID != invoiceID || attempt.AmountCents != amount || !strings.EqualFold(attempt.Currency, currency) {
		return nil, false, errors.New("idempotency_key was already used for a different collection request")
	}
	return attempt, n == 1, nil
}

func dbCollectionAttemptByKey(db *sql.DB, pid, key string) (*CollectionAttempt, error) {
	return scanCollectionAttempt(db.QueryRow(
		`SELECT id, project_id, invoice_id, payment_method_id, provider,
		        provider_payment_intent_id, idempotency_key, amount_cents,
		        currency, status, error_code, error_message, next_action,
		        created_at, updated_at, completed_at
		 FROM billing_collection_attempts
		 WHERE project_id = ? AND idempotency_key = ?`, pid, key,
	))
}

func dbCollectionAttemptGet(db *sql.DB, pid string, id int64) (*CollectionAttempt, error) {
	return scanCollectionAttempt(db.QueryRow(
		`SELECT id, project_id, invoice_id, payment_method_id, provider,
		        provider_payment_intent_id, idempotency_key, amount_cents,
		        currency, status, error_code, error_message, next_action,
		        created_at, updated_at, completed_at
		 FROM billing_collection_attempts
		 WHERE project_id = ? AND id = ?`, pid, id,
	))
}

func dbCollectionAttemptByIntent(db *sql.DB, provider, intentID string) (*CollectionAttempt, error) {
	return scanCollectionAttempt(db.QueryRow(
		`SELECT id, project_id, invoice_id, payment_method_id, provider,
		        provider_payment_intent_id, idempotency_key, amount_cents,
		        currency, status, error_code, error_message, next_action,
		        created_at, updated_at, completed_at
		 FROM billing_collection_attempts
		 WHERE provider = ? AND provider_payment_intent_id = ?`, provider, intentID,
	))
}

func scanCollectionAttempt(row rowScanner) (*CollectionAttempt, error) {
	var out CollectionAttempt
	var pmID sql.NullInt64
	var providerIntent, errorCode, errorMessage, completed sql.NullString
	var nextAction string
	if err := row.Scan(
		&out.ID, &out.ProjectID, &out.InvoiceID, &pmID, &out.Provider,
		&providerIntent, &out.IdempotencyKey, &out.AmountCents, &out.Currency,
		&out.Status, &errorCode, &errorMessage, &nextAction,
		&out.CreatedAt, &out.UpdatedAt, &completed,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	out.PaymentMethodID = pmID.Int64
	out.ProviderPaymentIntentID = providerIntent.String
	out.ErrorCode = errorCode.String
	out.ErrorMessage = errorMessage.String
	out.NextAction = json.RawMessage(firstString(nextAction, "{}"))
	out.CompletedAt = completed.String
	return &out, nil
}

func dbCollectionAttemptUpdate(db *sql.DB, id int64, intentID, status, errorCode, errorMessage string, nextAction json.RawMessage, completed bool) error {
	_, err := db.Exec(
		`UPDATE billing_collection_attempts
		 SET provider_payment_intent_id = COALESCE(NULLIF(?, ''), provider_payment_intent_id),
		     status = ?, error_code = NULLIF(?, ''), error_message = NULLIF(?, ''),
		     next_action = ?, updated_at = ?,
		     completed_at = CASE WHEN ? = 1 THEN COALESCE(completed_at, ?) ELSE completed_at END
		 WHERE id = ?`,
		intentID, status, errorCode, errorMessage, jsonOrEmpty(nextAction, "{}"),
		nowRFC3339(), boolInt(completed), nowRFC3339(), id,
	)
	return err
}

func dbCollectionAttemptSucceed(db *sql.DB, id int64, intentID string) error {
	return dbCollectionAttemptUpdate(db, id, intentID, "succeeded", "", "", nil, true)
}

func dbCollectionAttemptFail(db *sql.DB, id int64, code, message string, nextAction json.RawMessage) error {
	return dbCollectionAttemptUpdate(db, id, "", "failed", code, message, nextAction, true)
}
