package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	AddonStatusActive    = "active"
	AddonStatusSuspended = "suspended"
	AddonStatusCancelled = "cancelled"
)

type TenantAddon struct {
	ID                  int64           `json:"id"`
	TenantID            string          `json:"tenant_id"`
	CustomerID          int64           `json:"customer_id"`
	AddonKey            string          `json:"addon_key"`
	Status              string          `json:"status"`
	FeatureKey          string          `json:"feature_key"`
	IncludedQuantity    int64           `json:"included_quantity"`
	ResetInterval       string          `json:"reset_interval"`
	CatalogProductID    *int64          `json:"catalog_product_id,omitempty"`
	CatalogPriceID      *int64          `json:"catalog_price_id,omitempty"`
	OverageProductID    *int64          `json:"overage_product_id,omitempty"`
	OveragePriceID      *int64          `json:"overage_price_id,omitempty"`
	SubscriptionID      *int64          `json:"subscription_id,omitempty"`
	SubscriptionItemID  *int64          `json:"subscription_item_id,omitempty"`
	UnitAmountCents     int64           `json:"unit_amount_cents"`
	UnitSize            int64           `json:"unit_size"`
	Currency            string          `json:"currency"`
	ExternalApp         string          `json:"external_app,omitempty"`
	ExternalSubjectType string          `json:"external_subject_type,omitempty"`
	ExternalSubjectID   string          `json:"external_subject_id,omitempty"`
	ExternalTokenID     string          `json:"external_token_id,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
	ActivatedAt         string          `json:"activated_at,omitempty"`
	SuspendedAt         string          `json:"suspended_at,omitempty"`
	CancelledAt         string          `json:"cancelled_at,omitempty"`
	CreatedAt           string          `json:"created_at"`
	UpdatedAt           string          `json:"updated_at"`
}

type MeteringPeriod struct {
	ID               int64           `json:"id"`
	AddonID          int64           `json:"addon_id"`
	TenantID         string          `json:"tenant_id"`
	CustomerID       int64           `json:"customer_id"`
	FeatureKey       string          `json:"feature_key"`
	PeriodStart      string          `json:"period_start"`
	PeriodEnd        string          `json:"period_end"`
	IncludedQuantity int64           `json:"included_quantity"`
	TotalQuantity    int64           `json:"total_quantity"`
	BillableQuantity int64           `json:"billable_quantity"`
	UnitAmountCents  int64           `json:"unit_amount_cents"`
	UnitSize         int64           `json:"unit_size"`
	Currency         string          `json:"currency"`
	InvoiceID        *int64          `json:"invoice_id,omitempty"`
	Status           string          `json:"status"`
	IdempotencyKey   string          `json:"idempotency_key"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
	BilledAt         string          `json:"billed_at,omitempty"`
}

func (a *App) toolAddonEnable(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	addon, credentials, err := a.enableAddon(ctx, args)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"addon": addon}
	if len(credentials) > 0 {
		out["credentials"] = credentials
	}
	return out, nil
}

func (a *App) toolAddonList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbAddonList(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"addons": out, "count": len(out)}, nil
}

func (a *App) toolAddonSuspend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	addon, err := requireAddon(ctx.AppDB(), int64Arg(args, "addon_id"))
	if err != nil {
		return nil, err
	}
	if err := suspendExternalAddon(ctx, addon); err != nil {
		return nil, err
	}
	addon, err = dbAddonSetStatus(ctx.AppDB(), addon.ID, AddonStatusSuspended)
	if err != nil {
		return nil, err
	}
	_ = recordEvent(ctx.AppDB(), addon.TenantID, "addon.suspended", actor(args), map[string]any{"addon_id": addon.ID, "addon_key": addon.AddonKey})
	return map[string]any{"addon": addon}, nil
}

func (a *App) toolAddonResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	addon, err := requireAddon(ctx.AppDB(), int64Arg(args, "addon_id"))
	if err != nil {
		return nil, err
	}
	if err := resumeExternalAddon(ctx, addon); err != nil {
		return nil, err
	}
	addon, err = dbAddonSetStatus(ctx.AppDB(), addon.ID, AddonStatusActive)
	if err != nil {
		return nil, err
	}
	_ = recordEvent(ctx.AppDB(), addon.TenantID, "addon.resumed", actor(args), map[string]any{"addon_id": addon.ID, "addon_key": addon.AddonKey})
	return map[string]any{"addon": addon}, nil
}

func (a *App) toolUsageRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tenant, err := requireTenant(ctx.AppDB(), strArg(args, "tenant_id"))
	if err != nil {
		return nil, err
	}
	feature := strArg(args, "feature_key")
	qty := int64Arg(args, "quantity")
	if qty <= 0 {
		qty = 1
	}
	idem := strArg(args, "idempotency_key")
	if idem == "" {
		idem = fmt.Sprintf("manual:%s:%s:%d", tenant.ID, feature, time.Now().UnixNano())
	}
	if err := recordUsage(ctx.AppDB(), tenant.ID, tenant.CustomerID, feature, qty, idem, args["metadata"]); err != nil {
		return nil, err
	}
	recordEntitlementUsage(ctx, tenant, feature, qty, idem, args["metadata"])
	return map[string]any{"recorded": true, "tenant_id": tenant.ID, "feature_key": feature, "quantity": qty}, nil
}

func (a *App) toolMeteredInvoiceCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	period, invoice, err := a.createMeteredInvoice(ctx, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"metering_period": period, "invoice": invoice}, nil
}

func (a *App) handleLLMUsageRecorded(ctx *sdk.AppCtx, event sdk.Event) error {
	data := event.Data
	if strings.TrimSpace(strFromAny(data["subject_type"])) != "hosting_tenant" {
		return nil
	}
	tenantID := strFromAny(data["subject_id"])
	if tenantID == "" {
		return nil
	}
	tenant, err := dbTenantGet(ctx.AppDB(), tenantID)
	if err != nil || tenant == nil {
		return err
	}
	feature := firstNonEmpty(strFromAny(data["feature_key"]), "llm.tokens")
	qty := int64FromAny(data["total_tokens"])
	if qty <= 0 {
		qty = int64FromAny(data["request_tokens"]) + int64FromAny(data["response_tokens"])
	}
	if qty <= 0 {
		return nil
	}
	idem := firstNonEmpty(strFromAny(data["usage_event_id"]), strFromAny(data["request_id"]), strFromAny(data["provider_request_id"]))
	if idem == "" {
		idem = fmt.Sprintf("llm:%s:%s:%s:%d", tenant.ID, feature, event.ProjectID, time.Now().UnixNano())
	} else {
		idem = "llm:" + idem
	}
	if err := recordUsage(ctx.AppDB(), tenant.ID, tenant.CustomerID, feature, qty, idem, data); err != nil {
		return err
	}
	recordEntitlementUsage(ctx, tenant, feature, qty, idem, data)
	_ = recordEvent(ctx.AppDB(), tenant.ID, "usage.recorded", "llm", map[string]any{"feature_key": feature, "quantity": qty, "idempotency_key": idem})
	return nil
}

func (a *App) enableTenantAddons(ctx *sdk.AppCtx, tenant *Tenant, rawAddons []any, base map[string]any) ([]*TenantAddon, error) {
	var out []*TenantAddon
	for _, raw := range rawAddons {
		addonArgs := mergeMaps(mapFromAny(raw), base)
		addonArgs["tenant_id"] = tenant.ID
		addon, _, err := a.enableAddon(ctx, addonArgs)
		if err != nil {
			return out, err
		}
		out = append(out, addon)
	}
	return out, nil
}

func (a *App) enableAddon(ctx *sdk.AppCtx, args map[string]any) (*TenantAddon, map[string]any, error) {
	tenant, err := requireTenant(ctx.AppDB(), strArg(args, "tenant_id"))
	if err != nil {
		return nil, nil, err
	}
	feature := strArg(args, "feature_key")
	if feature == "" {
		return nil, nil, errors.New("feature_key required")
	}
	key := firstNonEmpty(strArg(args, "addon_key"), feature)
	addon := &TenantAddon{
		TenantID:            tenant.ID,
		CustomerID:          tenant.CustomerID,
		AddonKey:            key,
		Status:              AddonStatusActive,
		FeatureKey:          feature,
		IncludedQuantity:    int64Arg(args, "included_quantity"),
		ResetInterval:       firstNonEmpty(strArg(args, "reset_interval"), "month"),
		CatalogProductID:    nullableInt64(int64Arg(args, "catalog_product_id")),
		CatalogPriceID:      nullableInt64(int64Arg(args, "catalog_price_id")),
		OverageProductID:    nullableInt64(int64Arg(args, "overage_product_id")),
		OveragePriceID:      nullableInt64(int64Arg(args, "overage_price_id")),
		SubscriptionID:      nullableInt64(firstPositive(int64Arg(args, "subscription_id"), derefInt64(tenant.SubscriptionID))),
		SubscriptionItemID:  nullableInt64(int64Arg(args, "subscription_item_id")),
		UnitAmountCents:     int64Arg(args, "unit_amount_cents"),
		UnitSize:            firstPositive(int64Arg(args, "unit_size"), 1),
		Currency:            strings.ToUpper(firstNonEmpty(strArg(args, "currency"), "USD")),
		ExternalApp:         strArg(args, "external_app"),
		ExternalSubjectType: firstNonEmpty(strArg(args, "external_subject_type"), "hosting_tenant"),
		ExternalSubjectID:   firstNonEmpty(strArg(args, "external_subject_id"), tenant.ID),
		Metadata:            json.RawMessage(jsonOrEmpty(args["metadata"], "{}")),
	}
	if strings.HasPrefix(feature, "llm.") && addon.ExternalApp == "" {
		addon.ExternalApp = "llm"
	}
	credentials, err := provisionExternalAddon(ctx, addon, args)
	if err != nil {
		return nil, nil, err
	}
	if tokenID := strFromAny(credentials["token_id"]); tokenID != "" {
		addon.ExternalTokenID = tokenID
	} else if id := int64FromAny(credentials["id"]); id > 0 {
		addon.ExternalTokenID = fmt.Sprint(id)
	}
	saved, err := dbAddonUpsert(ctx.AppDB(), addon)
	if err != nil {
		return nil, nil, err
	}
	grantEntitlement(ctx, saved)
	_ = recordEvent(ctx.AppDB(), tenant.ID, "addon.active", "hosting", map[string]any{"addon_id": saved.ID, "addon_key": saved.AddonKey, "feature_key": saved.FeatureKey})
	return saved, credentials, nil
}

func provisionExternalAddon(ctx *sdk.AppCtx, addon *TenantAddon, args map[string]any) (map[string]any, error) {
	if addon.ExternalApp != "llm" {
		return nil, nil
	}
	policy := map[string]any{
		"subject_type": addon.ExternalSubjectType,
		"subject_id":   addon.ExternalSubjectID,
	}
	if raw := mapArg(args, "llm_policy"); len(raw) > 0 {
		for k, v := range raw {
			policy[k] = v
		}
	}
	if limits := mapArg(args, "limits"); len(limits) > 0 {
		policy["limits"] = limits
	}
	var policyOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("llm", "llm_policy_set", withProject(ctx.CurrentProject(), policy), &policyOut); err != nil {
		return nil, fmt.Errorf("configure llm policy: %w", err)
	}
	tokenArgs := map[string]any{
		"subject_type": addon.ExternalSubjectType,
		"subject_id":   addon.ExternalSubjectID,
		"scopes":       []string{"chat", "models", "usage"},
		"expires_at":   strArg(args, "expires_at"),
	}
	var tokenOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("llm", "llm_tokens_create", withProject(ctx.CurrentProject(), tokenArgs), &tokenOut); err != nil {
		return nil, fmt.Errorf("create llm token: %w", err)
	}
	return tokenOut, nil
}

func suspendExternalAddon(ctx *sdk.AppCtx, addon *TenantAddon) error {
	if addon.ExternalApp != "llm" {
		return nil
	}
	var out map[string]any
	return ctx.PlatformAPI().CallAppResult("llm", "llm_subject_suspend", withProject(ctx.CurrentProject(), map[string]any{
		"subject_type": addon.ExternalSubjectType,
		"subject_id":   addon.ExternalSubjectID,
	}), &out)
}

func resumeExternalAddon(ctx *sdk.AppCtx, addon *TenantAddon) error {
	if addon.ExternalApp != "llm" {
		return nil
	}
	var out map[string]any
	return ctx.PlatformAPI().CallAppResult("llm", "llm_subject_resume", withProject(ctx.CurrentProject(), map[string]any{
		"subject_type": addon.ExternalSubjectType,
		"subject_id":   addon.ExternalSubjectID,
	}), &out)
}

func suspendTenantAddons(ctx *sdk.AppCtx, tenantID string) error {
	addons, err := dbAddonList(ctx.AppDB(), map[string]any{"tenant_id": tenantID, "status": AddonStatusActive})
	if err != nil {
		return err
	}
	for _, addon := range addons {
		_ = suspendExternalAddon(ctx, addon)
		_, _ = dbAddonSetStatus(ctx.AppDB(), addon.ID, AddonStatusSuspended)
	}
	return nil
}

func resumeTenantAddons(ctx *sdk.AppCtx, tenantID string) error {
	addons, err := dbAddonList(ctx.AppDB(), map[string]any{"tenant_id": tenantID, "status": AddonStatusSuspended})
	if err != nil {
		return err
	}
	for _, addon := range addons {
		_ = resumeExternalAddon(ctx, addon)
		_, _ = dbAddonSetStatus(ctx.AppDB(), addon.ID, AddonStatusActive)
	}
	return nil
}

func (a *App) createMeteredInvoice(ctx *sdk.AppCtx, args map[string]any) (*MeteringPeriod, map[string]any, error) {
	addon, err := requireAddon(ctx.AppDB(), int64Arg(args, "addon_id"))
	if err != nil {
		return nil, nil, err
	}
	customer, err := dbCustomerGet(ctx.AppDB(), addon.CustomerID)
	if err != nil || customer == nil {
		return nil, nil, firstErr(err, errors.New("hosting customer not found"))
	}
	if customer.BillingCustomerID == nil || *customer.BillingCustomerID == 0 {
		return nil, nil, errors.New("hosting customer has no billing_customer_id")
	}
	start, end, err := meteringWindow(args)
	if err != nil {
		return nil, nil, err
	}
	total, err := dbUsageSum(ctx.AppDB(), addon.TenantID, addon.FeatureKey, start, end)
	if err != nil {
		return nil, nil, err
	}
	included := firstPositive(int64Arg(args, "included_quantity"), addon.IncludedQuantity)
	billable := total - included
	if billable < 0 {
		billable = 0
	}
	unitSize := firstPositive(int64Arg(args, "unit_size"), addon.UnitSize, 1)
	unitAmount := firstPositive(int64Arg(args, "unit_amount_cents"), addon.UnitAmountCents)
	quantityUnits := float64(billable) / float64(unitSize)
	if boolArg(args, "round_up_units") && billable > 0 {
		quantityUnits = math.Ceil(quantityUnits)
	}
	period := &MeteringPeriod{
		AddonID:          addon.ID,
		TenantID:         addon.TenantID,
		CustomerID:       addon.CustomerID,
		FeatureKey:       addon.FeatureKey,
		PeriodStart:      start.Format(time.RFC3339),
		PeriodEnd:        end.Format(time.RFC3339),
		IncludedQuantity: included,
		TotalQuantity:    total,
		BillableQuantity: billable,
		UnitAmountCents:  unitAmount,
		UnitSize:         unitSize,
		Currency:         firstNonEmpty(strArg(args, "currency"), addon.Currency, "USD"),
		Status:           "draft",
		IdempotencyKey:   fmt.Sprintf("hosting-meter:%d:%s:%s:%s", addon.ID, addon.FeatureKey, start.Format("2006-01-02"), end.Format("2006-01-02")),
		Metadata:         json.RawMessage(jsonOrEmpty(args["metadata"], "{}")),
	}
	if billable == 0 && !boolArg(args, "invoice_zero_usage") {
		saved, err := dbMeteringPeriodUpsert(ctx.AppDB(), period)
		return saved, map[string]any{"skipped": true, "reason": "no billable overage"}, err
	}
	description := firstNonEmpty(strArg(args, "description"), fmt.Sprintf("%s overage %s to %s", addon.FeatureKey, start.Format("2006-01-02"), end.Format("2006-01-02")))
	line := map[string]any{
		"description":      description,
		"quantity":         quantityUnits,
		"unit_price_cents": unitAmount,
		"product_id":       derefInt64(addon.OverageProductID),
		"price_id":         derefInt64(addon.OveragePriceID),
		"metadata": map[string]any{
			"source_app":        "hosting",
			"tenant_id":         addon.TenantID,
			"addon_id":          addon.ID,
			"addon_key":         addon.AddonKey,
			"feature_key":       addon.FeatureKey,
			"meter_key":         addon.FeatureKey,
			"period_start":      period.PeriodStart,
			"period_end":        period.PeriodEnd,
			"included_quantity": included,
			"total_quantity":    total,
			"billable_quantity": billable,
			"unit_size":         unitSize,
		},
	}
	invoiceArgs := map[string]any{
		"customer_id": *customer.BillingCustomerID,
		"currency":    period.Currency,
		"provider":    firstNonEmpty(strArg(args, "provider"), "local"),
		"line_items":  []any{line},
		"metadata": map[string]any{
			"source_app":      "hosting",
			"metering_key":    period.IdempotencyKey,
			"tenant_id":       addon.TenantID,
			"addon_id":        addon.ID,
			"subscription_id": derefInt64(addon.SubscriptionID),
		},
	}
	invOut, err := callAppMap(ctx, "billing", "invoices_create", withProject(ctx.CurrentProject(), invoiceArgs))
	if err != nil {
		return nil, nil, fmt.Errorf("create billing invoice: %w", err)
	}
	invoiceID := invoiceIDFromResult(invOut)
	period.InvoiceID = nullableInt64(invoiceID)
	period.Status = "invoiced"
	saved, err := dbMeteringPeriodUpsert(ctx.AppDB(), period)
	if err != nil {
		return nil, nil, err
	}
	if boolArg(args, "finalize") && invoiceID > 0 {
		if finalOut, err := callAppMap(ctx, "billing", "invoices_finalize", withProject(ctx.CurrentProject(), map[string]any{"invoice_id": invoiceID})); err == nil {
			invOut = finalOut
		}
	}
	_ = recordEvent(ctx.AppDB(), addon.TenantID, "metered_invoice.created", "hosting", map[string]any{"addon_id": addon.ID, "invoice_id": invoiceID, "billable_quantity": billable})
	return saved, invOut, nil
}

func dbAddonUpsert(db *sql.DB, a *TenantAddon) (*TenantAddon, error) {
	res, err := db.Exec(`
		INSERT INTO hosting_tenant_addons
			(tenant_id, customer_id, addon_key, status, feature_key, included_quantity, reset_interval,
			 catalog_product_id, catalog_price_id, overage_product_id, overage_price_id,
			 subscription_id, subscription_item_id, unit_amount_cents, unit_size, currency,
			 external_app, external_subject_type, external_subject_id, external_token_id, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, addon_key) DO UPDATE SET
			status=excluded.status,
			feature_key=excluded.feature_key,
			included_quantity=excluded.included_quantity,
			reset_interval=excluded.reset_interval,
			catalog_product_id=excluded.catalog_product_id,
			catalog_price_id=excluded.catalog_price_id,
			overage_product_id=excluded.overage_product_id,
			overage_price_id=excluded.overage_price_id,
			subscription_id=COALESCE(excluded.subscription_id, hosting_tenant_addons.subscription_id),
			subscription_item_id=COALESCE(excluded.subscription_item_id, hosting_tenant_addons.subscription_item_id),
			unit_amount_cents=excluded.unit_amount_cents,
			unit_size=excluded.unit_size,
			currency=excluded.currency,
			external_app=excluded.external_app,
			external_subject_type=excluded.external_subject_type,
			external_subject_id=excluded.external_subject_id,
			external_token_id=COALESCE(NULLIF(excluded.external_token_id,''), hosting_tenant_addons.external_token_id),
			metadata_json=excluded.metadata_json,
			suspended_at=NULL,
			cancelled_at=NULL,
			updated_at=CURRENT_TIMESTAMP`,
		a.TenantID, a.CustomerID, a.AddonKey, a.Status, a.FeatureKey, a.IncludedQuantity, a.ResetInterval,
		a.CatalogProductID, a.CatalogPriceID, a.OverageProductID, a.OveragePriceID,
		a.SubscriptionID, a.SubscriptionItemID, a.UnitAmountCents, a.UnitSize, a.Currency,
		a.ExternalApp, a.ExternalSubjectType, a.ExternalSubjectID, a.ExternalTokenID, string(a.Metadata))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		return dbAddonGetByTenantKey(db, a.TenantID, a.AddonKey)
	}
	return dbAddonGet(db, id)
}

func requireAddon(db *sql.DB, id int64) (*TenantAddon, error) {
	if id <= 0 {
		return nil, errors.New("addon_id required")
	}
	addon, err := dbAddonGet(db, id)
	if err != nil {
		return nil, err
	}
	if addon == nil {
		return nil, errors.New("addon not found")
	}
	return addon, nil
}

func dbAddonGet(db *sql.DB, id int64) (*TenantAddon, error) {
	return scanAddon(db.QueryRow(addonSelect()+` WHERE id=?`, id))
}

func dbAddonGetByTenantKey(db *sql.DB, tenantID, key string) (*TenantAddon, error) {
	return scanAddon(db.QueryRow(addonSelect()+` WHERE tenant_id=? AND addon_key=?`, tenantID, key))
}

func dbAddonList(db *sql.DB, args map[string]any) ([]*TenantAddon, error) {
	where := []string{"1=1"}
	qargs := []any{}
	if id := strArg(args, "tenant_id"); id != "" {
		where = append(where, "tenant_id=?")
		qargs = append(qargs, id)
	}
	if id := int64Arg(args, "customer_id"); id > 0 {
		where = append(where, "customer_id=?")
		qargs = append(qargs, id)
	}
	if id := int64Arg(args, "subscription_id"); id > 0 {
		where = append(where, "subscription_id=?")
		qargs = append(qargs, id)
	}
	if s := strArg(args, "status"); s != "" {
		where = append(where, "status=?")
		qargs = append(qargs, s)
	}
	rows, err := db.Query(addonSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TenantAddon
	for rows.Next() {
		addon, err := scanAddon(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, addon)
	}
	return out, rows.Err()
}

func addonSelect() string {
	return `SELECT id, tenant_id, customer_id, addon_key, status, feature_key, included_quantity, reset_interval,
		catalog_product_id, catalog_price_id, overage_product_id, overage_price_id,
		subscription_id, subscription_item_id, unit_amount_cents, unit_size, currency,
		external_app, external_subject_type, external_subject_id, external_token_id, metadata_json,
		COALESCE(activated_at,''), COALESCE(suspended_at,''), COALESCE(cancelled_at,''), created_at, updated_at
		FROM hosting_tenant_addons`
}

func scanAddon(row rowScanner) (*TenantAddon, error) {
	var a TenantAddon
	var productID, priceID, overageProductID, overagePriceID, subID, itemID sql.NullInt64
	var meta string
	if err := row.Scan(&a.ID, &a.TenantID, &a.CustomerID, &a.AddonKey, &a.Status, &a.FeatureKey, &a.IncludedQuantity, &a.ResetInterval,
		&productID, &priceID, &overageProductID, &overagePriceID, &subID, &itemID, &a.UnitAmountCents, &a.UnitSize, &a.Currency,
		&a.ExternalApp, &a.ExternalSubjectType, &a.ExternalSubjectID, &a.ExternalTokenID, &meta,
		&a.ActivatedAt, &a.SuspendedAt, &a.CancelledAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a.CatalogProductID = nullableSQLInt(productID)
	a.CatalogPriceID = nullableSQLInt(priceID)
	a.OverageProductID = nullableSQLInt(overageProductID)
	a.OveragePriceID = nullableSQLInt(overagePriceID)
	a.SubscriptionID = nullableSQLInt(subID)
	a.SubscriptionItemID = nullableSQLInt(itemID)
	a.Metadata = json.RawMessage(firstNonEmpty(meta, "{}"))
	return &a, nil
}

func dbAddonSetStatus(db *sql.DB, id int64, status string) (*TenantAddon, error) {
	stamp := "updated_at=CURRENT_TIMESTAMP"
	switch status {
	case AddonStatusActive:
		stamp += ", suspended_at=NULL, cancelled_at=NULL"
	case AddonStatusSuspended:
		stamp += ", suspended_at=CURRENT_TIMESTAMP"
	case AddonStatusCancelled:
		stamp += ", cancelled_at=CURRENT_TIMESTAMP"
	}
	res, err := db.Exec(`UPDATE hosting_tenant_addons SET status=?, `+stamp+` WHERE id=?`, status, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("addon not found")
	}
	return dbAddonGet(db, id)
}

func dbUsageSum(db *sql.DB, tenantID, feature string, start, end time.Time) (int64, error) {
	var total int64
	err := db.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM hosting_usage_events WHERE tenant_id=? AND feature_key=? AND occurred_at >= ? AND occurred_at < ?`,
		tenantID, feature, start.Format(time.RFC3339), end.Format(time.RFC3339)).Scan(&total)
	return total, err
}

func dbMeteringPeriodUpsert(db *sql.DB, p *MeteringPeriod) (*MeteringPeriod, error) {
	res, err := db.Exec(`
		INSERT INTO hosting_metering_periods
			(addon_id, tenant_id, customer_id, feature_key, period_start, period_end,
			 included_quantity, total_quantity, billable_quantity, unit_amount_cents, unit_size,
			 currency, invoice_id, status, idempotency_key, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(addon_id, feature_key, period_start, period_end) DO UPDATE SET
			total_quantity=excluded.total_quantity,
			billable_quantity=excluded.billable_quantity,
			invoice_id=COALESCE(excluded.invoice_id, hosting_metering_periods.invoice_id),
			status=excluded.status,
			metadata_json=excluded.metadata_json,
			updated_at=CURRENT_TIMESTAMP,
			billed_at=CASE WHEN excluded.invoice_id IS NOT NULL THEN CURRENT_TIMESTAMP ELSE hosting_metering_periods.billed_at END`,
		p.AddonID, p.TenantID, p.CustomerID, p.FeatureKey, p.PeriodStart, p.PeriodEnd,
		p.IncludedQuantity, p.TotalQuantity, p.BillableQuantity, p.UnitAmountCents, p.UnitSize,
		p.Currency, p.InvoiceID, p.Status, p.IdempotencyKey, string(p.Metadata))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		return dbMeteringPeriodGetByKey(db, p.IdempotencyKey)
	}
	return dbMeteringPeriodGet(db, id)
}

func dbMeteringPeriodGet(db *sql.DB, id int64) (*MeteringPeriod, error) {
	return scanMeteringPeriod(db.QueryRow(meteringSelect()+` WHERE id=?`, id))
}

func dbMeteringPeriodGetByKey(db *sql.DB, key string) (*MeteringPeriod, error) {
	return scanMeteringPeriod(db.QueryRow(meteringSelect()+` WHERE idempotency_key=?`, key))
}

func meteringSelect() string {
	return `SELECT id, addon_id, tenant_id, customer_id, feature_key, period_start, period_end,
		included_quantity, total_quantity, billable_quantity, unit_amount_cents, unit_size,
		currency, invoice_id, status, idempotency_key, metadata_json, created_at, updated_at, COALESCE(billed_at,'')
		FROM hosting_metering_periods`
}

func scanMeteringPeriod(row rowScanner) (*MeteringPeriod, error) {
	var p MeteringPeriod
	var inv sql.NullInt64
	var meta string
	if err := row.Scan(&p.ID, &p.AddonID, &p.TenantID, &p.CustomerID, &p.FeatureKey, &p.PeriodStart, &p.PeriodEnd,
		&p.IncludedQuantity, &p.TotalQuantity, &p.BillableQuantity, &p.UnitAmountCents, &p.UnitSize,
		&p.Currency, &inv, &p.Status, &p.IdempotencyKey, &meta, &p.CreatedAt, &p.UpdatedAt, &p.BilledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p.InvoiceID = nullableSQLInt(inv)
	p.Metadata = json.RawMessage(firstNonEmpty(meta, "{}"))
	return &p, nil
}

func grantEntitlement(ctx *sdk.AppCtx, addon *TenantAddon) {
	if addon == nil || ctx.PlatformAPI() == nil {
		return
	}
	args := map[string]any{
		"subject_type":   "hosting_tenant",
		"subject_id":     addon.TenantID,
		"feature_key":    addon.FeatureKey,
		"source_type":    "hosting_addon",
		"source_id":      fmt.Sprint(addon.ID),
		"reset_interval": addon.ResetInterval,
		"metadata":       map[string]any{"addon_key": addon.AddonKey},
	}
	var out map[string]any
	_ = ctx.PlatformAPI().CallAppResult("entitlements", "entitlement_grants_create", withProject(ctx.CurrentProject(), args), &out)
	if addon.IncludedQuantity > 0 {
		limitArgs := map[string]any{
			"subject_type":   "hosting_tenant",
			"subject_id":     addon.TenantID,
			"feature_key":    addon.FeatureKey,
			"limit_type":     "quota",
			"limit_value":    addon.IncludedQuantity,
			"reset_interval": addon.ResetInterval,
			"metadata":       map[string]any{"addon_key": addon.AddonKey},
		}
		_ = ctx.PlatformAPI().CallAppResult("entitlements", "entitlement_limits_set", withProject(ctx.CurrentProject(), limitArgs), &out)
	}
}

func recordEntitlementUsage(ctx *sdk.AppCtx, tenant *Tenant, feature string, qty int64, idem string, meta any) {
	if ctx == nil || ctx.PlatformAPI() == nil || tenant == nil {
		return
	}
	var out map[string]any
	_ = ctx.PlatformAPI().CallAppResult("entitlements", "usage_record", withProject(ctx.CurrentProject(), map[string]any{
		"subject_type":    "hosting_tenant",
		"subject_id":      tenant.ID,
		"feature_key":     feature,
		"quantity":        qty,
		"idempotency_key": idem,
		"metadata":        meta,
	}), &out)
}

func meteringWindow(args map[string]any) (time.Time, time.Time, error) {
	startRaw := strArg(args, "period_start")
	endRaw := strArg(args, "period_end")
	if startRaw == "" || endRaw == "" {
		now := time.Now().UTC()
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		start := first.AddDate(0, -1, 0)
		return start, first, nil
	}
	start, err := parseTimeOrDate(startRaw)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("period_start: %w", err)
	}
	end, err := parseTimeOrDate(endRaw)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("period_end: %w", err)
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, errors.New("period_end must be after period_start")
	}
	return start, end, nil
}

func parseTimeOrDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("must be RFC3339 or YYYY-MM-DD")
}

func invoiceIDFromResult(out map[string]any) int64 {
	if out == nil {
		return 0
	}
	if id := int64Arg(out, "id"); id > 0 {
		return id
	}
	return int64Arg(mapFromAny(out["invoice"]), "id")
}

func nullableSQLInt(v sql.NullInt64) *int64 {
	if !v.Valid || v.Int64 == 0 {
		return nil
	}
	return &v.Int64
}

func strFromAny(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func int64FromAny(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case int32:
		return int64(x)
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}
