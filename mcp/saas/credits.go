package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type SaaSCreditCheckout struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"project_id"`
	AccountID          string `json:"account_id"`
	IdempotencyKey     string `json:"idempotency_key"`
	CatalogPriceID     int64  `json:"catalog_price_id"`
	FeatureKey         string `json:"feature_key"`
	CreditUnits        int64  `json:"credit_units"`
	Quantity           int64  `json:"quantity"`
	AmountCents        int64  `json:"amount_cents"`
	CheckoutSessionID  *int64 `json:"checkout_session_id,omitempty"`
	BillingInvoiceID   *int64 `json:"billing_invoice_id,omitempty"`
	Status             string `json:"status"`
	GrantTransactionID *int64 `json:"grant_transaction_id,omitempty"`
	LastError          string `json:"last_error,omitempty"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
	CompletedAt        string `json:"completed_at,omitempty"`
}

func (a *App) recoverCreditCheckouts(ctx *sdk.AppCtx) error {
	pid := projectID(ctx, nil)
	if pid == "" || ctx.PlatformAPI() == nil {
		return nil
	}
	rows, err := ctx.AppDB().Query(`SELECT billing_invoice_id FROM saas_credit_checkouts WHERE project_id=? AND billing_invoice_id IS NOT NULL AND status IN ('awaiting_payment','processing','failed') ORDER BY updated_at LIMIT 50`, pid)
	if err != nil {
		return err
	}
	defer rows.Close()
	var invoices []int64
	for rows.Next() {
		var invoiceID sql.NullInt64
		if rows.Scan(&invoiceID) == nil && invoiceID.Valid {
			invoices = append(invoices, invoiceID.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, invoiceID := range invoices {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_get", map[string]any{"_project_id": pid, "id": invoiceID}, &out); err != nil {
			continue
		}
		invoice := unwrapMap(out, "invoice")
		if strArg(invoice, "status") == "paid" {
			if err := a.handleCreditInvoicePaid(ctx, pid, invoiceID); err != nil {
				ctx.Logger().Warn("credit checkout recovery failed", "invoice_id", invoiceID, "error", err.Error())
			}
		}
	}
	return nil
}

func (a *App) creditMCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "saas_credit_pack_checkout_create", Description: "Create a one-time Catalog credit-pack checkout for a SaaS account; credits are granted only after Billing invoice.paid.", InputSchema: schemaObject(map[string]any{
			"account_id": strSchema(), "catalog_price_id": intSchema(), "quantity": intSchema(), "idempotency_key": strSchema(),
			"provider": strSchema(), "presentation": strSchema(), "success_url": strSchema(), "cancel_url": strSchema(),
		}, []string{"account_id", "catalog_price_id", "idempotency_key"}), Handler: a.toolCreditPackCheckoutCreate},
		{Name: "saas_credit_balance", Description: "Return prepaid credit balance for a SaaS account and feature.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "feature_key": strSchema()}, []string{"account_id", "feature_key"}), Handler: a.toolSaaSCreditBalance},
		{Name: "saas_credit_transactions", Description: "List prepaid credit ledger transactions for a SaaS account.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "feature_key": strSchema(), "limit": intSchema(), "offset": intSchema()}, []string{"account_id"}), Handler: a.toolSaaSCreditTransactions},
	}
}

func (a *App) toolCreditPackCheckoutCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	accountID := strArg(args, "account_id")
	key := strArg(args, "idempotency_key")
	if accountID == "" || key == "" {
		return nil, errors.New("account_id and idempotency_key required")
	}
	if existing, err := dbSaaSCreditCheckoutByKey(ctx.AppDB(), pid, key); err != nil {
		return nil, err
	} else if existing != nil {
		return a.creditCheckoutResponse(ctx, existing)
	}
	account, err := dbAccountGet(ctx.AppDB(), pid, accountID)
	if err != nil || account == nil {
		return nil, firstErr(err, errors.New("account not found"))
	}
	if err := dbHydrateAccounts(ctx.AppDB(), pid, []*Account{account}); err != nil {
		return nil, err
	}
	priceID := int64Arg(args, "catalog_price_id")
	if priceID == 0 {
		return nil, errors.New("catalog_price_id required")
	}
	quantity := int64Arg(args, "quantity")
	if quantity == 0 {
		quantity = 1
	}
	if quantity <= 0 {
		return nil, errors.New("quantity must be greater than zero")
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	var priceResp struct {
		Price struct {
			ID              int64           `json:"id"`
			ProductID       int64           `json:"product_id"`
			UnitAmountCents int64           `json:"unit_amount_cents"`
			Currency        string          `json:"currency"`
			Interval        string          `json:"interval"`
			BillingScheme   string          `json:"billing_scheme"`
			MeterKey        string          `json:"meter_key"`
			UnitSize        int64           `json:"unit_size"`
			Active          bool            `json:"active"`
			ArchivedAt      string          `json:"archived_at"`
			Metadata        json.RawMessage `json:"metadata"`
		} `json:"price"`
	}
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_prices_get", map[string]any{"_project_id": pid, "id": priceID}, &priceResp); err != nil {
		return nil, fmt.Errorf("catalog price lookup: %w", err)
	}
	price := priceResp.Price
	if !price.Active || price.ArchivedAt != "" || price.Interval != "" {
		return nil, errors.New("credit pack must use an active one-time Catalog price")
	}
	if strings.ToLower(price.BillingScheme) != "metered" || strings.TrimSpace(price.MeterKey) == "" || price.UnitSize <= 0 {
		return nil, errors.New("credit pack price must be metered with meter_key and positive unit_size")
	}
	var productResp struct {
		Product struct {
			Type string `json:"type"`
		} `json:"product"`
	}
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_products_get", map[string]any{"_project_id": pid, "id": price.ProductID}, &productResp); err != nil {
		return nil, fmt.Errorf("catalog product lookup: %w", err)
	}
	if productResp.Product.Type != "one_time" {
		return nil, errors.New("credit pack product must be type one_time")
	}
	if quantity > math.MaxInt64/price.UnitSize {
		return nil, errors.New("credit quantity overflows")
	}
	creditUnits := price.UnitSize * quantity
	if quantity > 0 && price.UnitAmountCents > math.MaxInt64/quantity {
		return nil, errors.New("checkout amount overflows")
	}
	amountCents := price.UnitAmountCents * quantity
	if amountCents <= 0 {
		return nil, errors.New("credit pack price must have a positive amount")
	}
	id := newID("crd")
	if _, err := ctx.AppDB().Exec(`INSERT INTO saas_credit_checkouts
		(id,project_id,account_id,idempotency_key,catalog_price_id,feature_key,credit_units,quantity,amount_cents,status)
		VALUES (?,?,?,?,?,?,?,?,?,'pending')`, id, pid, accountID, key, priceID, price.MeterKey, creditUnits, quantity, amountCents); err != nil {
		return nil, err
	}
	meta := map[string]any{"source_app": "saas", "saas_credit_checkout_id": id, "account_id": accountID, "feature_key": price.MeterKey, "credit_units": creditUnits, "catalog_price_id": priceID}
	var cartOut map[string]any
	cartArgs := map[string]any{"_project_id": pid, "metadata": meta}
	if account.Customer != nil && account.Customer.BillingCustomerID != nil {
		cartArgs["customer_id"] = *account.Customer.BillingCustomerID
	}
	if err := ctx.PlatformAPI().CallAppResult("checkout", "cart_create", cartArgs, &cartOut); err != nil {
		return a.failCreditCheckout(ctx, id, fmt.Errorf("create credit cart: %w", err))
	}
	cart := unwrapMap(cartOut, "cart")
	cartID := int64Arg(cart, "id")
	if cartID == 0 {
		return a.failCreditCheckout(ctx, id, errors.New("checkout returned no cart id"))
	}
	var ignored map[string]any
	if err := ctx.PlatformAPI().CallAppResult("checkout", "cart_add_item", map[string]any{"_project_id": pid, "cart_id": cartID, "price_id": priceID, "quantity": quantity}, &ignored); err != nil {
		return a.failCreditCheckout(ctx, id, fmt.Errorf("add credit item: %w", err))
	}
	var startOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_start", map[string]any{"_project_id": pid, "cart_id": cartID}, &startOut); err != nil {
		return a.failCreditCheckout(ctx, id, fmt.Errorf("start credit checkout: %w", err))
	}
	session := unwrapMap(startOut, "session")
	sessionID := int64Arg(session, "id")
	if sessionID == 0 {
		return a.failCreditCheckout(ctx, id, errors.New("checkout returned no session id"))
	}
	if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_update", map[string]any{"_project_id": pid, "session_id": sessionID, "patch": map[string]any{"email": account.OwnerEmail, "customer_name": account.Customer.Name, "metadata": meta}}, &ignored); err != nil {
		return a.failCreditCheckout(ctx, id, fmt.Errorf("update credit checkout: %w", err))
	}
	provider := firstNonEmpty(strArg(args, "provider"), "stripe")
	presentation := firstNonEmpty(strArg(args, "presentation"), "hosted")
	payArgs := map[string]any{"_project_id": pid, "session_id": sessionID, "provider": provider, "presentation": presentation, "idempotency_key": key}
	for _, k := range []string{"success_url", "cancel_url"} {
		if v := strArg(args, k); v != "" {
			payArgs[k] = v
		}
	}
	var payOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_pay", payArgs, &payOut); err != nil {
		return a.failCreditCheckout(ctx, id, fmt.Errorf("prepare credit payment: %w", err))
	}
	checkoutSessionID := sessionID
	invoiceID := int64Arg(payOut, "invoice_id")
	if invoiceID == 0 {
		invoiceID = int64Arg(unwrapMap(payOut, "invoice"), "id")
	}
	status := "awaiting_payment"
	if strArg(payOut, "status") == "paid" {
		status = "paid"
	}
	if _, err := ctx.AppDB().Exec(`UPDATE saas_credit_checkouts SET checkout_session_id=?,billing_invoice_id=?,status=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, checkoutSessionID, nullableInt64(invoiceID), status, id); err != nil {
		return nil, err
	}
	row, err := dbSaaSCreditCheckoutGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	response, err := a.creditCheckoutResponse(ctx, row)
	if err != nil {
		return nil, err
	}
	response["checkout"] = payOut
	return response, nil
}

func (a *App) failCreditCheckout(ctx *sdk.AppCtx, id string, cause error) (any, error) {
	_, _ = ctx.AppDB().Exec(`UPDATE saas_credit_checkouts SET status='failed',last_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, cause.Error(), id)
	return nil, cause
}

func (a *App) creditCheckoutResponse(ctx *sdk.AppCtx, row *SaaSCreditCheckout) (map[string]any, error) {
	out := map[string]any{"credit_checkout": row}
	if row.CheckoutSessionID != nil && ctx.PlatformAPI() != nil {
		var checkout map[string]any
		if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_get", map[string]any{"_project_id": row.ProjectID, "session_id": *row.CheckoutSessionID}, &checkout); err == nil {
			out["payment"] = checkout
		}
	}
	return out, nil
}

func (a *App) toolSaaSCreditBalance(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	accountID := strArg(args, "account_id")
	feature := strArg(args, "feature_key")
	if accountID == "" || feature == "" {
		return nil, errors.New("account_id and feature_key required")
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("entitlements", "credits_balance", map[string]any{"_project_id": pid, "subject_type": "saas_account", "subject_id": accountID, "feature_key": feature}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) toolSaaSCreditTransactions(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	accountID := strArg(args, "account_id")
	if accountID == "" {
		return nil, errors.New("account_id required")
	}
	input := map[string]any{"_project_id": pid, "subject_type": "saas_account", "subject_id": accountID}
	for _, k := range []string{"feature_key", "limit", "offset"} {
		if v, ok := args[k]; ok {
			input[k] = v
		}
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("entitlements", "credits_transactions", input, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// enrichAccountReport keeps the SaaS facade useful for dashboards without
// conflating its local live gauges with Entitlements' durable counters or
// prepaid ledger. Cross-app reads are best-effort so an optional reporting
// dependency cannot make account reads fail.
func (a *App) enrichAccountReport(ctx *sdk.AppCtx, account *Account) error {
	if account == nil {
		return nil
	}
	usage, err := dbUsageTotals(ctx.AppDB(), account.ProjectID, map[string]any{"account_id": account.ID})
	if err == nil {
		account.Usage = usage
	}
	if ctx.PlatformAPI() == nil {
		return nil
	}
	var grants map[string]any
	if err := ctx.PlatformAPI().CallAppResult("entitlements", "entitlement_grants_list", map[string]any{"_project_id": account.ProjectID, "subject_type": "saas_account", "subject_id": account.ID, "limit": 200}, &grants); err == nil {
		entitlementReport := map[string]any{"grants": grants["grants"]}
		if plan, planErr := dbPlanGet(ctx.AppDB(), account.ProjectID, account.PlanKey); planErr == nil && plan != nil {
			counters := make([]any, 0, len(plan.Features))
			for _, feature := range plan.Features {
				var usage map[string]any
				if callErr := ctx.PlatformAPI().CallAppResult("entitlements", "usage_get", map[string]any{"_project_id": account.ProjectID, "subject_type": "saas_account", "subject_id": account.ID, "feature_key": feature.FeatureKey}, &usage); callErr == nil {
					counters = append(counters, usage)
				}
			}
			entitlementReport["usage"] = counters
		}
		account.Entitlements = entitlementReport
	}
	rows, err := ctx.AppDB().Query(`SELECT DISTINCT feature_key FROM saas_credit_checkouts WHERE project_id=? AND account_id=? ORDER BY feature_key`, account.ProjectID, account.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	balances := make([]any, 0)
	for rows.Next() {
		var feature string
		if rows.Scan(&feature) != nil {
			continue
		}
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("entitlements", "credits_balance", map[string]any{"_project_id": account.ProjectID, "subject_type": "saas_account", "subject_id": account.ID, "feature_key": feature}, &out); err == nil {
			balances = append(balances, out)
		}
	}
	account.Credits = map[string]any{"balances": balances}
	return nil
}

func (a *App) handleCreditInvoicePaid(ctx *sdk.AppCtx, pid string, invoiceID int64) error {
	row, err := dbSaaSCreditCheckoutByInvoice(ctx.AppDB(), pid, invoiceID)
	if err != nil || row == nil {
		return err
	}
	if row.Status == "granted" {
		return nil
	}
	res, err := ctx.AppDB().Exec(`UPDATE saas_credit_checkouts SET status='processing',last_error='',updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=? AND status IN ('pending','awaiting_payment','failed')`, pid, row.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	var out map[string]any
	key := fmt.Sprintf("billing:invoice:%d:credit_grant", invoiceID)
	if err := ctx.PlatformAPI().CallAppResult("entitlements", "credits_grant", map[string]any{"_project_id": pid, "subject_type": "saas_account", "subject_id": row.AccountID, "feature_key": row.FeatureKey, "amount": row.CreditUnits, "kind": "grant", "source_type": "billing_invoice", "source_id": fmt.Sprint(invoiceID), "idempotency_key": key, "metadata": map[string]any{"saas_credit_checkout_id": row.ID}}, &out); err != nil {
		_, _ = ctx.AppDB().Exec(`UPDATE saas_credit_checkouts SET status='failed',last_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, err.Error(), row.ID)
		return err
	}
	txID := int64Arg(unwrapMap(out, "transaction"), "id")
	_, err = ctx.AppDB().Exec(`UPDATE saas_credit_checkouts SET status='granted',grant_transaction_id=?,last_error='',completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, nullableInt64(txID), pid, row.ID)
	return err
}

func (a *App) handleCreditInvoiceRefunded(ctx *sdk.AppCtx, pid string, invoiceID int64, data map[string]any) error {
	row, err := dbSaaSCreditCheckoutByInvoice(ctx.AppDB(), pid, invoiceID)
	if err != nil || row == nil {
		return err
	}
	paymentID := int64Arg(data, "payment_id")
	if paymentID == 0 {
		paymentID = int64Arg(data, "id")
	}
	refundCents := int64Arg(data, "payment_amount_cents")
	if refundCents < 0 {
		refundCents = -refundCents
	}
	if refundCents == 0 {
		refundCents = row.AmountCents
	}
	if paymentID == 0 {
		paymentID = refundCents
	}
	key := fmt.Sprintf("billing:invoice:%d:refund:%d:credit_compensation", invoiceID, paymentID)
	units := row.CreditUnits
	if row.AmountCents > 0 && refundCents < row.AmountCents {
		units = (row.CreditUnits*refundCents + row.AmountCents/2) / row.AmountCents
		if units == 0 {
			units = 1
		}
	}
	if units > row.CreditUnits {
		units = row.CreditUnits
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO saas_credit_refunds(project_id,credit_checkout_id,invoice_id,payment_id,refund_cents,credit_units,idempotency_key,status) VALUES(?,?,?,?,?,?,?,'processing') ON CONFLICT(project_id,idempotency_key) DO NOTHING`, pid, row.ID, invoiceID, paymentID, refundCents, units, key)
	if err != nil {
		return err
	}
	var refundID int64
	_ = ctx.AppDB().QueryRow(`SELECT id FROM saas_credit_refunds WHERE project_id=? AND idempotency_key=?`, pid, key).Scan(&refundID)
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("entitlements", "credits_grant", map[string]any{"_project_id": pid, "subject_type": "saas_account", "subject_id": row.AccountID, "feature_key": row.FeatureKey, "amount": -units, "kind": "refund", "source_type": "billing_refund", "source_id": fmt.Sprint(paymentID), "idempotency_key": key, "metadata": map[string]any{"invoice_id": invoiceID, "saas_credit_checkout_id": row.ID, "refund_cents": refundCents}}, &out); err != nil {
		_, _ = ctx.AppDB().Exec(`UPDATE saas_credit_refunds SET status='failed',last_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, err.Error(), refundID)
		return err
	}
	txID := int64Arg(unwrapMap(out, "transaction"), "id")
	_, err = ctx.AppDB().Exec(`UPDATE saas_credit_refunds SET status='refunded',transaction_id=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, nullableInt64(txID), refundID)
	if err == nil {
		_, _ = ctx.AppDB().Exec(`UPDATE saas_credit_checkouts SET status='refunded',updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, row.ID)
	}
	return err
}

func dbSaaSCreditCheckoutGet(db *sql.DB, pid, id string) (*SaaSCreditCheckout, error) {
	return scanSaaSCreditCheckout(db.QueryRow(saasCreditCheckoutSelect()+` WHERE project_id=? AND id=?`, pid, id))
}
func dbSaaSCreditCheckoutByKey(db *sql.DB, pid, key string) (*SaaSCreditCheckout, error) {
	v, e := scanSaaSCreditCheckout(db.QueryRow(saasCreditCheckoutSelect()+` WHERE project_id=? AND idempotency_key=?`, pid, key))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return v, e
}
func dbSaaSCreditCheckoutByInvoice(db *sql.DB, pid string, invoice int64) (*SaaSCreditCheckout, error) {
	v, e := scanSaaSCreditCheckout(db.QueryRow(saasCreditCheckoutSelect()+` WHERE project_id=? AND billing_invoice_id=?`, pid, invoice))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return v, e
}
func saasCreditCheckoutSelect() string {
	return `SELECT id,project_id,account_id,idempotency_key,catalog_price_id,feature_key,credit_units,quantity,amount_cents,checkout_session_id,billing_invoice_id,status,grant_transaction_id,last_error,created_at,updated_at,completed_at FROM saas_credit_checkouts`
}
func scanSaaSCreditCheckout(row rowScanner) (*SaaSCreditCheckout, error) {
	var v SaaSCreditCheckout
	var session, invoice, grant sql.NullInt64
	var completed sql.NullString
	err := row.Scan(&v.ID, &v.ProjectID, &v.AccountID, &v.IdempotencyKey, &v.CatalogPriceID, &v.FeatureKey, &v.CreditUnits, &v.Quantity, &v.AmountCents, &session, &invoice, &v.Status, &grant, &v.LastError, &v.CreatedAt, &v.UpdatedAt, &completed)
	if session.Valid {
		v.CheckoutSessionID = &session.Int64
	}
	if invoice.Valid {
		v.BillingInvoiceID = &invoice.Int64
	}
	if grant.Valid {
		v.GrantTransactionID = &grant.Int64
	}
	if completed.Valid {
		v.CompletedAt = completed.String
	}
	return &v, err
}
