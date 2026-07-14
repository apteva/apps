package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type PlanChange struct {
	ID                   string          `json:"id"`
	ProjectID            string          `json:"project_id"`
	AccountID            string          `json:"account_id"`
	SubscriptionID       int64           `json:"subscription_id"`
	IdempotencyKey       string          `json:"idempotency_key"`
	RequestFingerprint   string          `json:"-"`
	FromPlanKey          string          `json:"from_plan_key"`
	TargetPlanKey        string          `json:"target_plan_key"`
	ChangeKind           string          `json:"change_kind"`
	EffectiveMode        string          `json:"effective_at"`
	ProrationPolicy      string          `json:"proration_policy"`
	DiscountPolicy       string          `json:"discount_policy"`
	SubscriptionChangeID *int64          `json:"subscription_change_id,omitempty"`
	BillingCustomerID    *int64          `json:"billing_customer_id,omitempty"`
	InvoiceID            *int64          `json:"invoice_id,omitempty"`
	Status               string          `json:"status"`
	Stage                string          `json:"stage"`
	Proration            json.RawMessage `json:"proration,omitempty"`
	PaymentLink          json.RawMessage `json:"payment_link,omitempty"`
	SuccessURL           string          `json:"-"`
	CancelURL            string          `json:"-"`
	LastError            string          `json:"last_error,omitempty"`
	AttemptCount         int64           `json:"attempt_count"`
	CompletedAt          string          `json:"completed_at,omitempty"`
	CreatedAt            string          `json:"created_at"`
	UpdatedAt            string          `json:"updated_at"`
}

func (a *App) toolAccountChangePlan(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	change, err := a.createOrContinuePlanChange(ctx, args)
	if err != nil {
		return nil, err
	}
	return a.planChangeResponse(ctx, change)
}

func (a *App) toolPlanChangeGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	change, err := dbPlanChangeGet(ctx.AppDB(), pid, strArg(args, "change_id"))
	if err != nil || change == nil {
		return nil, firstErr(err, errors.New("plan change not found"))
	}
	return a.planChangeResponse(ctx, change)
}

func (a *App) createOrContinuePlanChange(ctx *sdk.AppCtx, args map[string]any) (*PlanChange, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	acct, err := dbAccountGet(ctx.AppDB(), pid, strArg(args, "account_id"))
	if err != nil || acct == nil {
		return nil, firstErr(err, errors.New("account not found"))
	}
	if acct.Status != StatusActive {
		return nil, fmt.Errorf("plan changes require an active account, got %s", acct.Status)
	}
	if int64PtrValue(acct.SubscriptionID) == 0 {
		return nil, errors.New("plan changes require a subscription-backed account")
	}
	targetKey, err := normalizeKey(strArg(args, "target_plan_key"))
	if err != nil {
		return nil, err
	}
	mode := strings.ToLower(firstNonEmpty(strArg(args, "effective_at"), "immediate"))
	if mode != "immediate" && mode != "next_cycle" {
		return nil, errors.New("effective_at must be immediate or next_cycle")
	}
	defaultProration := "prorate"
	if mode == "next_cycle" {
		defaultProration = "none"
	}
	policy := strings.ToLower(firstNonEmpty(strArg(args, "proration_policy"), defaultProration))
	if policy != "none" && policy != "prorate" && policy != "charge_full" {
		return nil, errors.New("proration_policy must be none, prorate, or charge_full")
	}
	if mode == "next_cycle" && policy != "none" {
		return nil, errors.New("next_cycle changes require proration_policy=none")
	}
	discountPolicy := strings.ToLower(firstNonEmpty(strArg(args, "discount_policy"), "preserve"))
	if discountPolicy != "preserve" && discountPolicy != "drop" {
		return nil, errors.New("discount_policy must be preserve or drop")
	}
	idem := strings.TrimSpace(strArg(args, "idempotency_key"))
	if idem == "" {
		return nil, errors.New("idempotency_key required")
	}
	successURL, cancelURL := strings.TrimSpace(strArg(args, "success_url")), strings.TrimSpace(strArg(args, "cancel_url"))
	if existing, err := dbPlanChangeByIdempotency(ctx.AppDB(), pid, idem); err != nil {
		return nil, err
	} else if existing != nil {
		fingerprint := planChangeFingerprint(acct.ID, existing.FromPlanKey, targetKey, mode, policy, discountPolicy, successURL, cancelURL)
		if existing.RequestFingerprint != fingerprint {
			return nil, errors.New("idempotency_key was already used for a different plan change")
		}
		if existing.Status == "applied" {
			return existing, nil
		}
		targetPlan, err := dbPlanGet(ctx.AppDB(), pid, targetKey)
		if err != nil || targetPlan == nil {
			return nil, firstErr(err, errors.New("target plan not found"))
		}
		targetPrice, err := a.resolveCheckoutPrice(ctx, pid, targetPlan, map[string]any{})
		if err != nil {
			return nil, fmt.Errorf("resolve target plan price: %w", err)
		}
		if err := a.continuePlanChange(ctx, existing, targetPlan, targetPrice); err != nil {
			_ = dbPlanChangeFail(ctx.AppDB(), pid, existing.ID, err.Error())
			return nil, err
		}
		return dbPlanChangeGet(ctx.AppDB(), pid, existing.ID)
	}
	fromPlan, err := dbPlanGet(ctx.AppDB(), pid, acct.PlanKey)
	if err != nil || fromPlan == nil {
		return nil, firstErr(err, errors.New("current plan not found"))
	}
	if targetKey == acct.PlanKey {
		return nil, errors.New("account is already on the target plan")
	}
	targetPlan, err := dbPlanGet(ctx.AppDB(), pid, targetKey)
	if err != nil || targetPlan == nil {
		return nil, firstErr(err, errors.New("target plan not found"))
	}
	if !fromPlan.SubscriptionRequired || !targetPlan.SubscriptionRequired {
		return nil, errors.New("plan changes require subscription-backed source and target plans")
	}
	fromProduct, targetProduct := int64PtrValue(fromPlan.CatalogProductID), int64PtrValue(targetPlan.CatalogProductID)
	if fromProduct == 0 || targetProduct == 0 {
		return nil, errors.New("both plans must reference a Catalog product")
	}
	if fromProduct != targetProduct {
		return nil, errors.New("cross-product plan changes require an explicit migration workflow")
	}
	fromPrice, err := a.resolveCheckoutPrice(ctx, pid, fromPlan, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("resolve current plan price: %w", err)
	}
	targetPrice, err := a.resolveCheckoutPrice(ctx, pid, targetPlan, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("resolve target plan price: %w", err)
	}
	if mode == "immediate" && policy == "prorate" && targetPrice.UnitAmountCents < fromPrice.UnitAmountCents {
		return nil, errors.New("immediate prorated downgrades require credit-note support; use next_cycle or proration_policy=none")
	}
	kind := "lateral"
	if targetPrice.UnitAmountCents > fromPrice.UnitAmountCents {
		kind = "upgrade"
	} else if targetPrice.UnitAmountCents < fromPrice.UnitAmountCents {
		kind = "downgrade"
	}
	fingerprint := planChangeFingerprint(acct.ID, acct.PlanKey, targetKey, mode, policy, discountPolicy, successURL, cancelURL)
	change, created, err := dbPlanChangeClaim(ctx.AppDB(), pid, acct, idem, fingerprint, targetKey, kind, mode, policy, discountPolicy, successURL, cancelURL)
	if err != nil {
		return nil, err
	}
	if !created && change.Status == "applied" {
		return change, nil
	}
	if err := a.continuePlanChange(ctx, change, targetPlan, targetPrice); err != nil {
		_ = dbPlanChangeFail(ctx.AppDB(), pid, change.ID, err.Error())
		return nil, err
	}
	return dbPlanChangeGet(ctx.AppDB(), pid, change.ID)
}

func (a *App) continuePlanChange(ctx *sdk.AppCtx, change *PlanChange, targetPlan *Plan, targetPrice checkoutPrice) error {
	pid := change.ProjectID
	if int64PtrValue(change.SubscriptionChangeID) == 0 {
		var out map[string]any
		input := map[string]any{
			"_project_id": pid, "subscription_id": change.SubscriptionID,
			"items": []any{map[string]any{
				"catalog_product_id": targetPrice.ProductID, "catalog_price_id": targetPrice.PriceID, "title": targetPlan.Name,
				"quantity": 1, "unit_amount_cents": targetPrice.UnitAmountCents, "currency": targetPrice.Currency,
				"metadata": map[string]any{"saas_plan_key": targetPlan.Key},
			}},
			"effective_at": change.EffectiveMode, "proration_policy": change.ProrationPolicy, "discount_policy": change.DiscountPolicy,
			"interval": targetPrice.Interval, "interval_count": targetPrice.IntervalCount,
			"subscription_metadata_patch": subscriptionPlanMetadataPatch(targetPlan),
			"idempotency_key":             "saas-plan-change:" + change.ID, "source_app": "saas", "source_ref": change.ID,
			"defer_apply": change.EffectiveMode == "immediate",
		}
		if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscription_changes_create", input, &out); err != nil {
			return fmt.Errorf("create subscription change: %w", err)
		}
		subChange := unwrapMap(out, "change")
		subChangeID := int64Arg(subChange, "id")
		if subChangeID == 0 {
			return errors.New("subscription_changes_create returned no change id")
		}
		if err := dbPlanChangeSetSubscriptionChange(ctx.AppDB(), pid, change.ID, subChangeID, mapFromAny(subChange["proration"])); err != nil {
			return err
		}
		change.SubscriptionChangeID = &subChangeID
		change.Proration = json.RawMessage(jsonOrEmpty(subChange["proration"], "{}"))
	}
	if change.EffectiveMode == "next_cycle" {
		return dbPlanChangeSetStatus(ctx.AppDB(), pid, change.ID, "scheduled", "subscription_change_scheduled", "")
	}
	proration := mapFromAny(change.Proration)
	if credit := int64Arg(proration, "credit_cents"); credit > 0 {
		return fmt.Errorf("subscription change requires an unsupported credit of %d %s cents", credit, strArg(proration, "currency"))
	}
	if int64Arg(proration, "charge_cents") > 0 {
		if int64PtrValue(change.InvoiceID) > 0 {
			handled, err := a.handlePlanChangeInvoicePaid(ctx, pid, int64PtrValue(change.InvoiceID))
			if err != nil {
				return err
			}
			if handled {
				latest, err := dbPlanChangeGet(ctx.AppDB(), pid, change.ID)
				if err != nil {
					return err
				}
				if latest != nil && latest.Status == "applied" {
					return nil
				}
			}
		}
		return a.ensurePlanChangePayment(ctx, change)
	}
	if err := a.applySubscriptionPlanChange(ctx, change); err != nil {
		return err
	}
	return a.finalizePlanChange(ctx, pid, change.ID)
}

func subscriptionPlanMetadataPatch(plan *Plan) map[string]any {
	patch := map[string]any{"saas_plan_key": plan.Key}
	if plan.CatalogProductID != nil {
		patch["catalog_product_id"] = *plan.CatalogProductID
	} else {
		patch["catalog_product_id"] = nil
	}
	if plan.CatalogPriceID != nil {
		patch["catalog_price_id"] = *plan.CatalogPriceID
	} else {
		patch["catalog_price_id"] = nil
	}
	return patch
}

func (a *App) ensurePlanChangePayment(ctx *sdk.AppCtx, change *PlanChange) error {
	pid := change.ProjectID
	acct, err := dbAccountGet(ctx.AppDB(), pid, change.AccountID)
	if err != nil || acct == nil {
		return firstErr(err, errors.New("account not found"))
	}
	customer, err := dbCustomerGet(ctx.AppDB(), pid, acct.CustomerID)
	if err != nil || customer == nil {
		return firstErr(err, errors.New("customer not found"))
	}
	billingCustomerID, _, err := a.ensureBillingCustomer(ctx, pid, customer, map[string]any{})
	if err != nil {
		return err
	}
	operationKey := "plan_change:" + change.ID
	op, err := dbEnsurePlanChangeCommerceOperation(ctx.AppDB(), change, operationKey, billingCustomerID)
	if err != nil {
		return err
	}
	invoiceID := int64PtrValue(change.InvoiceID)
	if invoiceID == 0 {
		invoiceID = int64PtrValue(op.InvoiceID)
	}
	if invoiceID == 0 {
		invoiceID, err = a.findOrCreatePlanChangeInvoice(ctx, change, billingCustomerID, operationKey)
		if err != nil {
			return err
		}
		if err := dbPlanChangeSetInvoice(ctx.AppDB(), pid, change.ID, billingCustomerID, invoiceID); err != nil {
			return err
		}
		if err := dbCommerceOperationSetInvoice(ctx.AppDB(), pid, op.ID, invoiceID); err != nil {
			return err
		}
	}
	link := mapFromAny(change.PaymentLink)
	if len(link) == 0 {
		link, err = a.createPaymentLink(ctx, pid, invoiceID, map[string]any{"success_url": change.SuccessURL, "cancel_url": change.CancelURL})
		if err != nil {
			return err
		}
		if err := dbPlanChangeSetPaymentLink(ctx.AppDB(), pid, change.ID, link); err != nil {
			return err
		}
	}
	if err := dbCommerceOperationCompleteBilling(ctx.AppDB(), pid, op.ID, link); err != nil {
		return err
	}
	return dbPlanChangeSetStatus(ctx.AppDB(), pid, change.ID, "awaiting_payment", "payment_link_created", "")
}

func (a *App) findOrCreatePlanChangeInvoice(ctx *sdk.AppCtx, change *PlanChange, billingCustomerID int64, operationKey string) (int64, error) {
	var search map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_search", map[string]any{
		"_project_id": change.ProjectID, "customer_id": billingCustomerID, "limit": 200,
	}, &search); err == nil {
		for _, raw := range sliceFromAny(search["invoices"]) {
			invoice := mapFromAny(raw)
			if strArg(mapFromAny(invoice["metadata"]), "operation_key") == operationKey {
				if id := int64Arg(invoice, "id"); id != 0 {
					return id, nil
				}
			}
		}
	}
	proration := mapFromAny(change.Proration)
	lines := sliceFromAny(proration["line_items"])
	if len(lines) == 0 {
		return 0, errors.New("plan change proration returned no Billing lines")
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_create_from_prepared_lines", map[string]any{
		"_project_id": change.ProjectID, "customer_id": billingCustomerID, "currency": strArg(proration, "currency"),
		"line_items": lines, "finalize": true,
		"metadata": map[string]any{"source_app": "saas", "operation_key": operationKey, "saas_plan_change_id": change.ID, "saas_account_id": change.AccountID, "subscription_id": change.SubscriptionID},
	}, &out); err != nil {
		return 0, fmt.Errorf("create plan-change invoice: %w", err)
	}
	id := int64Arg(unwrapMap(out, "invoice"), "id")
	if id == 0 {
		return 0, errors.New("Billing returned no plan-change invoice id")
	}
	return id, nil
}

func (a *App) applySubscriptionPlanChange(ctx *sdk.AppCtx, change *PlanChange) error {
	if int64PtrValue(change.SubscriptionChangeID) == 0 {
		return errors.New("plan change has no subscription change")
	}
	if err := dbPlanChangeSetStatus(ctx.AppDB(), change.ProjectID, change.ID, "applying", "applying_subscription_change", ""); err != nil {
		return err
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscription_changes_apply", map[string]any{
		"_project_id": change.ProjectID, "change_id": int64PtrValue(change.SubscriptionChangeID),
	}, &out); err != nil {
		return fmt.Errorf("apply subscription change: %w", err)
	}
	if status := strArg(unwrapMap(out, "change"), "status"); status != "applied" {
		return fmt.Errorf("subscription change returned status %q", status)
	}
	return nil
}

func (a *App) handleSubscriptionChangeApplied(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "subscriptions" {
		return nil
	}
	pid := firstNonEmpty(event.ProjectID, projectID(ctx, nil))
	changeID := int64FromAny(event.Data["change_id"])
	if pid == "" || changeID == 0 {
		return nil
	}
	change, err := dbPlanChangeBySubscriptionChange(ctx.AppDB(), pid, changeID)
	if err != nil || change == nil {
		return err
	}
	return a.finalizePlanChange(ctx, pid, change.ID)
}

func (a *App) handlePlanChangeInvoicePaid(ctx *sdk.AppCtx, pid string, invoiceID int64) (bool, error) {
	change, err := dbPlanChangeByInvoice(ctx.AppDB(), pid, invoiceID)
	if err != nil || change == nil {
		return false, err
	}
	if change.Status == "applied" {
		op, opErr := dbCommerceOperationByInvoice(ctx.AppDB(), pid, invoiceID)
		if opErr != nil {
			return true, opErr
		}
		if op != nil && op.Status != "paid" {
			return true, dbCommerceOperationCompletePlanChangePayment(ctx.AppDB(), pid, op.ID)
		}
		return true, nil
	}
	op, err := dbCommerceOperationByInvoice(ctx.AppDB(), pid, invoiceID)
	if err != nil || op == nil {
		return true, firstErr(err, errors.New("plan-change commerce operation not found"))
	}
	projection, err := a.fetchBillingInvoiceProjection(ctx, pid, op)
	if err != nil {
		return true, err
	}
	if projection.Status != "paid" {
		return true, nil
	}
	claimed, err := dbPlanChangePaymentClaim(ctx.AppDB(), pid, change.ID)
	if err != nil || !claimed {
		return true, err
	}
	fail := func(cause error) (bool, error) {
		if persistErr := dbPlanChangeFail(ctx.AppDB(), pid, change.ID, cause.Error()); persistErr != nil {
			return true, fmt.Errorf("%w; persist plan-change failure: %v", cause, persistErr)
		}
		return true, cause
	}
	if err := persistBillingInvoiceProjection(ctx.AppDB(), pid, op, projection); err != nil {
		return fail(err)
	}
	if err := a.applySubscriptionPlanChange(ctx, change); err != nil {
		return fail(err)
	}
	if err := a.finalizePlanChange(ctx, pid, change.ID); err != nil {
		return fail(err)
	}
	if err := dbCommerceOperationCompletePlanChangePayment(ctx.AppDB(), pid, op.ID); err != nil {
		return true, err
	}
	return true, nil
}

func (a *App) recoverPlanChanges(ctx *sdk.AppCtx) error {
	pid := projectID(ctx, nil)
	if pid == "" {
		return nil
	}
	rows, err := ctx.AppDB().Query(`SELECT id FROM saas_plan_changes
		WHERE project_id=? AND status IN ('pending','scheduled','awaiting_payment','applying','failed')
		AND (status<>'failed' OR next_attempt_at IS NULL OR next_attempt_at<=CURRENT_TIMESTAMP)
		ORDER BY updated_at,id LIMIT 100`, pid)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var first error
	for _, id := range ids {
		if err := a.recoverPlanChange(ctx, pid, id); err != nil {
			_ = dbPlanChangeFail(ctx.AppDB(), pid, id, err.Error())
			if first == nil {
				first = err
			}
		}
	}
	return first
}

func (a *App) recoverPlanChange(ctx *sdk.AppCtx, pid, id string) error {
	change, err := dbPlanChangeGet(ctx.AppDB(), pid, id)
	if err != nil || change == nil {
		return err
	}
	if change.Status == "applied" {
		return nil
	}
	if int64PtrValue(change.InvoiceID) > 0 {
		handled, err := a.handlePlanChangeInvoicePaid(ctx, pid, int64PtrValue(change.InvoiceID))
		if err != nil || handled && change.Status == "awaiting_payment" {
			return err
		}
	}
	if int64PtrValue(change.SubscriptionChangeID) > 0 {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscription_changes_get", map[string]any{
			"_project_id": pid, "change_id": int64PtrValue(change.SubscriptionChangeID),
		}, &out); err != nil {
			return fmt.Errorf("read subscription change: %w", err)
		}
		if strArg(unwrapMap(out, "change"), "status") == "applied" {
			return a.finalizePlanChange(ctx, pid, change.ID)
		}
		if change.Status == "scheduled" || change.Status == "awaiting_payment" {
			return nil
		}
	}
	targetPlan, err := dbPlanGet(ctx.AppDB(), pid, change.TargetPlanKey)
	if err != nil || targetPlan == nil {
		return firstErr(err, errors.New("target plan not found"))
	}
	targetPrice, err := a.resolveCheckoutPrice(ctx, pid, targetPlan, map[string]any{})
	if err != nil {
		return err
	}
	return a.continuePlanChange(ctx, change, targetPlan, targetPrice)
}

func (a *App) finalizePlanChange(ctx *sdk.AppCtx, pid, changeID string) error {
	change, err := dbPlanChangeGet(ctx.AppDB(), pid, changeID)
	if err != nil || change == nil {
		return firstErr(err, errors.New("plan change not found"))
	}
	if change.Status == "applied" {
		return nil
	}
	acct, err := dbAccountGet(ctx.AppDB(), pid, change.AccountID)
	if err != nil || acct == nil {
		return firstErr(err, errors.New("account not found"))
	}
	if acct.PlanKey == change.TargetPlanKey {
		return dbPlanChangeComplete(ctx.AppDB(), pid, change.ID)
	}
	fromPlan, err := dbPlanGet(ctx.AppDB(), pid, change.FromPlanKey)
	if err != nil || fromPlan == nil {
		return firstErr(err, errors.New("source plan not found"))
	}
	targetPlan, err := dbPlanGet(ctx.AppDB(), pid, change.TargetPlanKey)
	if err != nil || targetPlan == nil {
		return firstErr(err, errors.New("target plan not found"))
	}
	if err := a.applyPlanAccess(ctx, pid, acct, targetPlan); err != nil {
		return err
	}
	event := "plan_changed"
	if change.ChangeKind == "upgrade" {
		event = "plan_upgraded"
	} else if change.ChangeKind == "downgrade" {
		event = "plan_downgraded"
	}
	if _, err := a.runFulfillmentForPlan(ctx, pid, acct, targetPlan.Key, event, change.ID); err != nil {
		return err
	}
	if err := a.removeObsoletePlanAccess(ctx, pid, acct, fromPlan, targetPlan); err != nil {
		return err
	}
	if err := dbAccountCompletePlanChange(ctx.AppDB(), pid, acct.ID, targetPlan.Key, change); err != nil {
		return err
	}
	ctx.Emit("saas.account.plan_changed", map[string]any{
		"plan_change_id": change.ID, "account_id": acct.ID, "customer_id": acct.CustomerID,
		"from_plan_key": change.FromPlanKey, "target_plan_key": change.TargetPlanKey, "change_kind": change.ChangeKind,
	})
	updated, _ := dbAccountGet(ctx.AppDB(), pid, acct.ID)
	if updated != nil {
		_, _ = a.toolUsageSync(ctx, map[string]any{"_project_id": pid, "account_id": updated.ID})
	}
	return nil
}

func (a *App) removeObsoletePlanAccess(ctx *sdk.AppCtx, pid string, acct *Account, fromPlan, targetPlan *Plan) error {
	if err := a.removeObsoletePlanGrants(ctx, pid, acct, targetPlan); err != nil {
		return err
	}
	if fromPlan == nil {
		return nil
	}
	targetLimits := map[string]bool{}
	for _, limit := range targetPlan.Limits {
		targetLimits[limit.FeatureKey] = true
	}
	for _, limit := range fromPlan.Limits {
		if targetLimits[limit.FeatureKey] {
			continue
		}
		var ignored map[string]any
		if err := ctx.PlatformAPI().CallAppResult("entitlements", "entitlement_limits_set", map[string]any{
			"_project_id": pid, "subject_type": "saas_account", "subject_id": acct.ID, "feature_key": limit.FeatureKey,
			"limit_type": "quota", "limit_value": 0, "reset_interval": "none", "metadata": map[string]any{"cleared_by_plan_change": true, "plan_key": targetPlan.Key},
		}, &ignored); err != nil {
			return fmt.Errorf("clear obsolete limit %s: %w", limit.FeatureKey, err)
		}
	}
	return nil
}

func (a *App) removeObsoletePlanGrants(ctx *sdk.AppCtx, pid string, acct *Account, targetPlan *Plan) error {
	targetFeatures := map[string]bool{}
	for _, feature := range targetPlan.Features {
		targetFeatures[feature.FeatureKey] = true
	}
	var grantsOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("entitlements", "entitlement_grants_list", map[string]any{
		"_project_id": pid, "subject_type": "saas_account", "subject_id": acct.ID, "status": "active", "limit": 1000,
	}, &grantsOut); err != nil {
		return fmt.Errorf("list account grants: %w", err)
	}
	for _, raw := range sliceFromAny(grantsOut["grants"]) {
		grant := mapFromAny(raw)
		if strArg(grant, "source_type") != "saas" || strArg(grant, "source_id") != acct.ID || targetFeatures[strArg(grant, "feature_key")] {
			continue
		}
		var ignored map[string]any
		if err := ctx.PlatformAPI().CallAppResult("entitlements", "entitlement_grants_revoke", map[string]any{
			"_project_id": pid, "id": int64Arg(grant, "id"), "reason": "SaaS plan changed to " + targetPlan.Key,
		}, &ignored); err != nil {
			return fmt.Errorf("revoke obsolete grant %s: %w", strArg(grant, "feature_key"), err)
		}
	}
	return nil
}

func (a *App) toolAccountReconcile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	accountID := strings.TrimSpace(strArg(args, "account_id"))
	if accountID == "" {
		return nil, errors.New("account_id required")
	}
	acct, err := dbAccountGet(ctx.AppDB(), pid, accountID)
	if err != nil || acct == nil {
		return nil, firstErr(err, errors.New("account not found"))
	}
	plan, err := dbPlanGet(ctx.AppDB(), pid, acct.PlanKey)
	if err != nil || plan == nil {
		return nil, firstErr(err, errors.New("account plan not found"))
	}

	steps := map[string]any{}
	if subID := int64PtrValue(acct.SubscriptionID); subID != 0 {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_update_metadata", map[string]any{
			"_project_id": pid, "id": subID, "metadata_patch": subscriptionPlanMetadataPatch(plan),
			"actor": "saas", "note": "Reconcile SaaS account plan projection",
		}, &out); err != nil {
			return nil, fmt.Errorf("reconcile subscription metadata: %w", err)
		}
		steps["subscription_metadata"] = true
	}
	if err := a.applyPlanAccess(ctx, pid, acct, plan); err != nil {
		return nil, err
	}
	steps["entitlement_grants"] = true
	steps["entitlement_limits"] = true

	var previousPlan *Plan
	previousKey := strArg(mapFromAny(acct.Metadata), "previous_plan_key")
	if previousKey != "" && previousKey != plan.Key {
		previousPlan, err = dbPlanGet(ctx.AppDB(), pid, previousKey)
		if err != nil {
			return nil, err
		}
	}
	if err := a.removeObsoletePlanAccess(ctx, pid, acct, previousPlan, plan); err != nil {
		return nil, err
	}
	steps["obsolete_access"] = true

	updated, err := dbAccountGet(ctx.AppDB(), pid, acct.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"account": updated, "plan": plan, "reconciled": true, "steps": steps}, nil
}

func (a *App) planChangeResponse(ctx *sdk.AppCtx, change *PlanChange) (map[string]any, error) {
	if change == nil {
		return nil, errors.New("plan change not found")
	}
	out := map[string]any{"plan_change": change, "status": change.Status, "requires_payment": change.Status == "awaiting_payment"}
	if link := mapFromAny(change.PaymentLink); len(link) > 0 {
		out["payment_link"] = link
		out["url"] = firstNonEmpty(strArg(link, "url"), strArg(link, "external_url"))
	}
	if acct, err := dbAccountGet(ctx.AppDB(), change.ProjectID, change.AccountID); err != nil {
		return nil, err
	} else if acct != nil {
		out["account"] = acct
	}
	return out, nil
}

func planChangeFingerprint(accountID, fromPlan, targetPlan, mode, proration, discount, successURL, cancelURL string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{accountID, fromPlan, targetPlan, mode, proration, discount, successURL, cancelURL}, "|")))
	return hex.EncodeToString(sum[:])
}

func dbPlanChangeClaim(db *sql.DB, pid string, acct *Account, idem, fingerprint, target, kind, mode, proration, discount, successURL, cancelURL string) (*PlanChange, bool, error) {
	existing, err := dbPlanChangeByIdempotency(db, pid, idem)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if existing.RequestFingerprint != fingerprint {
			return nil, false, errors.New("idempotency_key was already used for a different plan change")
		}
		return existing, false, nil
	}
	var open string
	if err := db.QueryRow(`SELECT id FROM saas_plan_changes WHERE project_id=? AND account_id=? AND status IN ('pending','scheduled','awaiting_payment','applying','failed') LIMIT 1`, pid, acct.ID).Scan(&open); err == nil {
		return nil, false, fmt.Errorf("account already has open plan change %s", open)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	id := newID("plc")
	_, err = db.Exec(`INSERT INTO saas_plan_changes
		(id,project_id,account_id,subscription_id,idempotency_key,request_fingerprint,from_plan_key,target_plan_key,change_kind,effective_mode,proration_policy,discount_policy,success_url,cancel_url)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, pid, acct.ID, int64PtrValue(acct.SubscriptionID), idem, fingerprint, acct.PlanKey, target, kind, mode, proration, discount, successURL, cancelURL)
	if err != nil {
		return nil, false, err
	}
	change, err := dbPlanChangeGet(db, pid, id)
	return change, true, err
}

func dbPlanChangeSetSubscriptionChange(db *sql.DB, pid, id string, subscriptionChangeID int64, proration map[string]any) error {
	_, err := db.Exec(`UPDATE saas_plan_changes SET subscription_change_id=?,proration_json=?,stage='subscription_change_created',status='pending',last_error='',updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		subscriptionChangeID, jsonOrEmpty(proration, "{}"), pid, id)
	return err
}

func dbPlanChangeSetInvoice(db *sql.DB, pid, id string, billingCustomerID, invoiceID int64) error {
	_, err := db.Exec(`UPDATE saas_plan_changes SET billing_customer_id=?,invoice_id=?,stage='invoice_created',updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, billingCustomerID, invoiceID, pid, id)
	return err
}

func dbPlanChangeSetPaymentLink(db *sql.DB, pid, id string, link map[string]any) error {
	_, err := db.Exec(`UPDATE saas_plan_changes SET payment_link_json=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, jsonOrEmpty(link, "{}"), pid, id)
	return err
}

func dbPlanChangeSetStatus(db *sql.DB, pid, id, status, stage, lastError string) error {
	_, err := db.Exec(`UPDATE saas_plan_changes SET status=?,stage=?,last_error=?,attempt_count=attempt_count+1,lease_until=NULL,next_attempt_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, status, stage, lastError, pid, id)
	return err
}

func dbPlanChangeFail(db *sql.DB, pid, id, message string) error {
	_, err := db.Exec(`UPDATE saas_plan_changes SET status='failed',stage='failed',last_error=?,attempt_count=attempt_count+1,lease_until=NULL,next_attempt_at=datetime('now','+1 minute'),updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, message, pid, id)
	return err
}

func dbPlanChangePaymentClaim(db *sql.DB, pid, id string) (bool, error) {
	result, err := db.Exec(`UPDATE saas_plan_changes SET status='applying',stage='payment_confirmed',attempt_count=attempt_count+1,lease_until=datetime('now','+15 minutes'),updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=? AND (status IN ('awaiting_payment','failed') OR (status='applying' AND lease_until<=CURRENT_TIMESTAMP))`, pid, id)
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	return rows == 1, nil
}

func dbPlanChangeComplete(db *sql.DB, pid, id string) error {
	_, err := db.Exec(`UPDATE saas_plan_changes SET status='applied',stage='completed',last_error='',lease_until=NULL,next_attempt_at=NULL,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, id)
	return err
}

func dbAccountCompletePlanChange(db *sql.DB, pid, accountID, targetPlan string, change *PlanChange) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var metadataText string
	if err := tx.QueryRow(`SELECT metadata_json FROM saas_accounts WHERE project_id=? AND id=?`, pid, accountID).Scan(&metadataText); err != nil {
		return err
	}
	metadata := mapFromAny(json.RawMessage(metadataText))
	metadata["last_plan_change_id"] = change.ID
	metadata["previous_plan_key"] = change.FromPlanKey
	metadata["plan_changed_at"] = time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`UPDATE saas_accounts SET plan_key=?,metadata_json=?,last_error='',updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, targetPlan, jsonOrEmpty(metadata, "{}"), pid, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM saas_usage_snapshots WHERE project_id=? AND account_id=?`, pid, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM saas_usage_source_state WHERE project_id=? AND account_id=?`, pid, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM saas_quota_states WHERE project_id=? AND account_id=?`, pid, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE saas_plan_changes SET status='applied',stage='completed',last_error='',lease_until=NULL,next_attempt_at=NULL,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, change.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func dbEnsurePlanChangeCommerceOperation(db *sql.DB, change *PlanChange, operationKey string, billingCustomerID int64) (*CommerceOperation, error) {
	_, err := db.Exec(`INSERT INTO saas_commerce_operations (project_id,operation_key,account_id,subscription_id,cycle_id,billing_customer_id,status,stage,prepared_json)
		VALUES (?,?,?,?,0,?,'pending','plan_change',?) ON CONFLICT(project_id,operation_key) DO UPDATE SET billing_customer_id=excluded.billing_customer_id,updated_at=CURRENT_TIMESTAMP`,
		change.ProjectID, operationKey, change.AccountID, change.SubscriptionID, billingCustomerID, stringOrJSON(change.Proration, "{}"))
	if err != nil {
		return nil, err
	}
	return dbCommerceOperationByKey(db, change.ProjectID, operationKey)
}

func dbPlanChangeGet(db *sql.DB, pid, id string) (*PlanChange, error) {
	if id == "" {
		return nil, nil
	}
	return scanPlanChange(db.QueryRow(planChangeSelect()+` WHERE project_id=? AND id=?`, pid, id))
}

func dbPlanChangeByIdempotency(db *sql.DB, pid, key string) (*PlanChange, error) {
	return scanPlanChange(db.QueryRow(planChangeSelect()+` WHERE project_id=? AND idempotency_key=?`, pid, key))
}

func dbPlanChangeBySubscriptionChange(db *sql.DB, pid string, id int64) (*PlanChange, error) {
	return scanPlanChange(db.QueryRow(planChangeSelect()+` WHERE project_id=? AND subscription_change_id=?`, pid, id))
}

func dbPlanChangeByInvoice(db *sql.DB, pid string, id int64) (*PlanChange, error) {
	return scanPlanChange(db.QueryRow(planChangeSelect()+` WHERE project_id=? AND invoice_id=?`, pid, id))
}

func planChangeSelect() string {
	return `SELECT id,project_id,account_id,subscription_id,idempotency_key,request_fingerprint,from_plan_key,target_plan_key,change_kind,effective_mode,proration_policy,discount_policy,subscription_change_id,billing_customer_id,invoice_id,status,stage,proration_json,payment_link_json,success_url,cancel_url,last_error,attempt_count,completed_at,created_at,updated_at FROM saas_plan_changes`
}

func scanPlanChange(row rowScanner) (*PlanChange, error) {
	var change PlanChange
	var subChange, billingCustomer, invoice sql.NullInt64
	var completed sql.NullString
	var proration, paymentLink string
	err := row.Scan(&change.ID, &change.ProjectID, &change.AccountID, &change.SubscriptionID, &change.IdempotencyKey, &change.RequestFingerprint,
		&change.FromPlanKey, &change.TargetPlanKey, &change.ChangeKind, &change.EffectiveMode, &change.ProrationPolicy, &change.DiscountPolicy,
		&subChange, &billingCustomer, &invoice, &change.Status, &change.Stage, &proration, &paymentLink, &change.SuccessURL, &change.CancelURL,
		&change.LastError, &change.AttemptCount, &completed, &change.CreatedAt, &change.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	change.SubscriptionChangeID = ptrIfValid(subChange)
	change.BillingCustomerID = ptrIfValid(billingCustomer)
	change.InvoiceID = ptrIfValid(invoice)
	change.Proration = json.RawMessage(proration)
	change.PaymentLink = json.RawMessage(paymentLink)
	if completed.Valid {
		change.CompletedAt = completed.String
	}
	return &change, nil
}
