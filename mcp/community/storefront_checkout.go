package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type guestCheckoutSession struct {
	ID                int64           `json:"id"`
	CartID            int64           `json:"cart_id"`
	ProviderSessionID string          `json:"provider_session_id,omitempty"`
	Email             string          `json:"email,omitempty"`
	CustomerName      string          `json:"customer_name,omitempty"`
	Status            string          `json:"status"`
	InvoiceID         *int64          `json:"invoice_id,omitempty"`
	TotalCents        int64           `json:"total_cents"`
	Currency          string          `json:"currency"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	RecoveryToken     string          `json:"recovery_token,omitempty"`
}

type guestCheckoutCart struct {
	ID           int64               `json:"id"`
	SessionToken string              `json:"session_token,omitempty"`
	Items        []guestCheckoutItem `json:"items,omitempty"`
}

type guestCheckoutItem struct {
	PriceID         int64   `json:"price_id"`
	ProductID       int64   `json:"product_id"`
	UnitAmountCents int64   `json:"unit_amount_cents"`
	Currency        string  `json:"currency"`
	Quantity        float64 `json:"quantity"`
}

type preparedStorefrontCheckout struct {
	OfferKind      string `json:"offer_kind"`
	CheckoutID     int64  `json:"checkout_session_id"`
	RecoveryToken  string `json:"recovery_token,omitempty"`
	SessionToken   string `json:"session_token,omitempty"`
	InvoiceID      int64  `json:"invoice_id"`
	InvoiceNumber  string `json:"invoice_number,omitempty"`
	Presentation   string `json:"presentation"`
	ClientSecret   string `json:"client_secret"`
	PublishableKey string `json:"publishable_key"`
	CheckoutURL    string `json:"checkout_url,omitempty"`
}

type storefrontOfferTarget struct {
	Kind     string
	SpaceID  string
	PlanID   string
	Product  int64
	Price    int64
	Amount   int64
	Currency string
}

func resolveStorefrontOffer(db *sql.DB, communityID string, priceID int64) (storefrontOfferTarget, error) {
	var target storefrontOfferTarget
	err := db.QueryRow(`SELECT co.catalog_product_id,co.catalog_price_id,co.unit_amount_cents,co.currency,s.id
		FROM course_offers co JOIN spaces s ON s.id=co.space_id
		WHERE s.community_id=? AND s.kind='course' AND s.archived_at IS NULL
		AND co.catalog_price_id=? AND co.active=1 AND co.archived_at IS NULL`, communityID, priceID).
		Scan(&target.Product, &target.Price, &target.Amount, &target.Currency, &target.SpaceID)
	if err == nil {
		target.Kind = "one_time"
		target.Currency = strings.ToUpper(target.Currency)
		return target, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return target, err
	}
	var trialDays int64
	err = db.QueryRow(`SELECT catalog_product_id,catalog_price_id,unit_amount_cents,currency,id,trial_days
		FROM membership_plans WHERE community_id=? AND catalog_price_id=? AND active=1 AND archived_at IS NULL`,
		communityID, priceID).Scan(&target.Product, &target.Price, &target.Amount, &target.Currency, &target.PlanID, &trialDays)
	if errors.Is(err, sql.ErrNoRows) {
		return target, errors.New("this product offer is not available")
	}
	if err != nil {
		return target, err
	}
	if trialDays > 0 {
		return target, errors.New("trial memberships start after account verification and do not require payment details")
	}
	target.Kind = "recurring"
	target.Currency = strings.ToUpper(target.Currency)
	return target, nil
}

func (a *App) httpPortalCheckoutPrepare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	community, err := publicPortalCommunity(r)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	var input struct {
		CatalogPriceID int64  `json:"catalog_price_id"`
		Email          string `json:"email"`
		CustomerName   string `json:"customer_name"`
		RecoveryToken  string `json:"recovery_token"`
		ReturnURL      string `json:"return_url"`
		SuccessURL     string `json:"success_url"`
		CancelURL      string `json:"cancel_url"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid checkout request")
		return
	}
	email, err := validatePurchaseEmail(input.Email)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	target, err := resolveStorefrontOffer(globalCtx.AppDB(), community.ID, input.CatalogPriceID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	urls := map[string]string{}
	for name, raw := range map[string]string{"return_url": input.ReturnURL, "success_url": input.SuccessURL, "cancel_url": input.CancelURL} {
		value, validateErr := validateStorefrontReturnURL(r, raw)
		if validateErr != nil {
			writeErr(w, http.StatusBadRequest, validateErr.Error())
			return
		}
		if value != "" {
			urls[name] = value
		}
	}
	prepared, err := prepareCheckoutWithCheckoutApp(globalCtx, community.ID, target, email, strings.TrimSpace(input.CustomerName), strings.TrimSpace(input.RecoveryToken), urls)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "secure checkout could not be prepared")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, prepared)
}

func validateStorefrontReturnURL(r *http.Request, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("checkout return URL must be an absolute HTTP URL")
	}
	if !strings.EqualFold(parsed.Host, r.Host) {
		return "", errors.New("checkout return URL must use this portal origin")
	}
	return parsed.String(), nil
}

func prepareCheckoutWithCheckoutApp(ctx *sdk.AppCtx, communityID string, target storefrontOfferTarget, email, name, recoveryToken string, urls map[string]string) (*preparedStorefrontCheckout, error) {
	pid := scopeProject(ctx)
	var session guestCheckoutSession
	var cart guestCheckoutCart
	if recoveryToken != "" {
		var restored struct {
			Session guestCheckoutSession `json:"session"`
			Cart    guestCheckoutCart    `json:"cart"`
		}
		if err := callAppResult(ctx, "checkout", "checkout_bootstrap", map[string]any{"_project_id": pid, "recovery_token": recoveryToken}, &restored); err != nil {
			return nil, fmt.Errorf("restore Checkout session: %w", err)
		}
		session, cart = restored.Session, restored.Cart
		if err := validatePreparedCheckout(communityID, target, email, session, cart); err != nil {
			return nil, err
		}
	} else {
		var created struct {
			Cart guestCheckoutCart `json:"cart"`
		}
		if err := callAppResult(ctx, "checkout", "cart_create", map[string]any{
			"_project_id": pid,
			"metadata":    map[string]any{"source_app": "community", "community_id": communityID, "catalog_price_id": target.Price, "offer_kind": target.Kind},
		}, &created); err != nil {
			return nil, fmt.Errorf("create Checkout cart: %w", err)
		}
		cart = created.Cart
		var added struct {
			Cart guestCheckoutCart `json:"cart"`
		}
		if err := callAppResult(ctx, "checkout", "cart_add_item", map[string]any{"_project_id": pid, "cart_id": cart.ID, "price_id": target.Price, "quantity": 1}, &added); err != nil {
			return nil, fmt.Errorf("add Checkout item: %w", err)
		}
		cart = added.Cart
		var started struct {
			Session guestCheckoutSession `json:"session"`
		}
		if err := callAppResult(ctx, "checkout", "checkout_start", map[string]any{"_project_id": pid, "cart_id": cart.ID}, &started); err != nil {
			return nil, fmt.Errorf("start Checkout session: %w", err)
		}
		session = started.Session
		recoveryToken = session.RecoveryToken
		var updated struct {
			Session guestCheckoutSession `json:"session"`
		}
		if err := callAppResult(ctx, "checkout", "checkout_update", map[string]any{
			"_project_id": pid, "session_id": session.ID,
			"patch": map[string]any{"email": email, "customer_name": name, "metadata": map[string]any{
				"source_app": "community", "community_id": communityID, "catalog_price_id": target.Price, "offer_kind": target.Kind,
			}},
		}, &updated); err != nil {
			return nil, fmt.Errorf("update Checkout buyer: %w", err)
		}
		session = updated.Session
	}
	paymentArgs := map[string]any{
		"_project_id": pid, "session_id": session.ID, "provider": "stripe", "presentation": "elements",
		"idempotency_key": fmt.Sprintf("community-storefront:%s:%d", communityID, session.ID),
	}
	for name, value := range urls {
		paymentArgs[name] = value
	}
	if target.Kind == "recurring" {
		paymentArgs["save_payment_method"] = true
		paymentArgs["set_default_payment_method"] = true
	}
	var paid struct {
		Session       guestCheckoutSession `json:"session"`
		InvoiceID     int64                `json:"invoice_id"`
		InvoiceNumber string               `json:"invoice_number"`
		Payment       map[string]any       `json:"payment"`
	}
	if err := callAppResult(ctx, "checkout", "checkout_pay", paymentArgs, &paid); err != nil {
		return nil, fmt.Errorf("prepare Checkout payment: %w", err)
	}
	return &preparedStorefrontCheckout{
		OfferKind: target.Kind, CheckoutID: paid.Session.ID, RecoveryToken: recoveryToken,
		SessionToken: cart.SessionToken, InvoiceID: paid.InvoiceID, InvoiceNumber: paid.InvoiceNumber,
		Presentation: "elements", ClientSecret: strings.TrimSpace(strArg(paid.Payment, "client_secret", "")),
		PublishableKey: strings.TrimSpace(strArg(paid.Payment, "publishable_key", "")), CheckoutURL: strings.TrimSpace(strArg(paid.Payment, "url", "")),
	}, nil
}

func validatePreparedCheckout(communityID string, target storefrontOfferTarget, email string, session guestCheckoutSession, cart guestCheckoutCart) error {
	if session.Status != "started" && session.Status != "awaiting_payment" {
		return errors.New("prepared checkout is no longer payable")
	}
	if !strings.EqualFold(strings.TrimSpace(session.Email), strings.TrimSpace(email)) {
		return errors.New("prepared checkout belongs to another email address")
	}
	if metadataString(session.Metadata, "community_id") != communityID || metadataString(session.Metadata, "catalog_price_id") != fmt.Sprint(target.Price) || metadataString(session.Metadata, "offer_kind") != target.Kind {
		return errors.New("prepared checkout does not match this offer")
	}
	if len(cart.Items) != 1 || cart.Items[0].PriceID != target.Price || cart.Items[0].ProductID != target.Product || cart.Items[0].Quantity != 1 || cart.Items[0].UnitAmountCents != target.Amount || !strings.EqualFold(cart.Items[0].Currency, target.Currency) {
		return errors.New("prepared checkout totals no longer match this offer")
	}
	return nil
}

func toolStorefrontCheckoutClaim(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	priceID, ok := intArg(args, "catalog_price_id")
	if !ok || priceID <= 0 {
		return nil, errors.New("catalog_price_id must be a positive integer")
	}
	recoveryToken, err := mustStr(args, "recovery_token")
	if err != nil {
		return nil, err
	}
	target, err := resolveStorefrontOffer(ctx.AppDB(), communityID, priceID)
	if err != nil {
		return nil, err
	}
	var restored struct {
		Session guestCheckoutSession `json:"session"`
		Cart    guestCheckoutCart    `json:"cart"`
	}
	if err := callAppResult(ctx, "checkout", "checkout_bootstrap", map[string]any{"_project_id": scopeProject(ctx), "recovery_token": recoveryToken}, &restored); err != nil {
		return nil, fmt.Errorf("restore Checkout session: %w", err)
	}
	email := strings.TrimSpace(strArg(args, "_subject_email", ""))
	if email == "" {
		return nil, errors.New("verified Auth email is required to claim checkout")
	}
	if err := validatePreparedCheckout(communityID, target, email, restored.Session, restored.Cart); err != nil {
		return nil, err
	}
	if restored.Session.InvoiceID == nil || *restored.Session.InvoiceID == 0 {
		return nil, errors.New("prepared checkout has no Billing invoice")
	}
	memberID := strArg(args, "_viewer_member_id", strArg(args, "member_id", ""))
	if target.Kind == "one_time" {
		purchase, claimErr := claimCourseCheckout(ctx, target, memberID, email, strArg(args, "_auth_subject_id", ""), restored.Session)
		return map[string]any{"offer_kind": target.Kind, "purchase": purchase, "enrolled": purchase == nil && claimErr == nil}, claimErr
	}
	subscription, claimErr := claimMembershipCheckout(ctx, target, memberID, email, strArg(args, "_auth_subject_id", ""), restored.Session)
	return map[string]any{"offer_kind": target.Kind, "subscription": subscription}, claimErr
}

func claimCourseCheckout(ctx *sdk.AppCtx, target storefrontOfferTarget, memberID, email, authSubjectID string, session guestCheckoutSession) (*CoursePurchase, error) {
	space, err := ensureCourseSpace(ctx, ctx.AppDB(), target.SpaceID)
	if err != nil {
		return nil, err
	}
	if err := verifyMember(ctx.AppDB(), space.CommunityID, memberID); err != nil {
		return nil, err
	}
	if active, err := hasActiveCourseEnrollment(ctx.AppDB(), space.ID, memberID); err != nil || active {
		if err != nil {
			return nil, err
		}
		return nil, nil
	}
	purchase, err := loadActiveCoursePurchase(ctx.AppDB(), space.ID, memberID)
	if err != nil {
		return nil, err
	}
	if purchase == nil {
		offer, err := refreshCourseOffer(ctx, space.ID)
		if err != nil {
			return nil, err
		}
		if err := validateCourseSaleWindow(ctx.AppDB(), space.ID, memberID); err != nil {
			return nil, err
		}
		purchase, err = createCoursePurchase(ctx.AppDB(), space, memberID, email, session.CustomerName, authSubjectID, offer)
		if err != nil {
			return nil, err
		}
	}
	if purchase.BillingInvoiceID != nil && *purchase.BillingInvoiceID != *session.InvoiceID {
		return nil, errors.New("member already has another active checkout for this course")
	}
	invoice, err := storefrontBillingInvoice(ctx, *session.InvoiceID)
	if err != nil {
		return nil, err
	}
	if invoice.TotalCents != target.Amount || !strings.EqualFold(invoice.Currency, target.Currency) {
		return nil, errors.New("Billing invoice does not match this course offer")
	}
	_, err = ctx.AppDB().Exec(`UPDATE course_purchases SET billing_customer_id=?,billing_invoice_id=?,billing_session_id=?,checkout_url=NULL,status='awaiting_payment',last_error='',updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		invoice.CustomerID, *session.InvoiceID, nullIfEmpty(session.ProviderSessionID), purchase.ID)
	if err != nil {
		return nil, err
	}
	purchase, err = loadCoursePurchase(ctx.AppDB(), purchase.ID)
	if err == nil {
		emit(ctx, "course.purchase_started", purchaseEventPayload(purchase))
	}
	return purchase, err
}

func claimMembershipCheckout(ctx *sdk.AppCtx, target storefrontOfferTarget, memberID, email, authSubjectID string, session guestCheckoutSession) (*MemberSubscription, error) {
	plan, err := loadMembershipPlan(ctx.AppDB(), target.PlanID, false)
	if err != nil {
		return nil, err
	}
	if err := verifyMember(ctx.AppDB(), plan.CommunityID, memberID); err != nil {
		return nil, err
	}
	if existing, _ := loadLiveMemberSubscription(ctx.AppDB(), plan.CommunityID, memberID); existing != nil {
		var invoice sql.NullInt64
		_ = ctx.AppDB().QueryRow(`SELECT billing_invoice_id FROM membership_checkouts WHERE member_subscription_id=? ORDER BY created_at DESC LIMIT 1`, existing.ID).Scan(&invoice)
		if invoice.Valid && invoice.Int64 == *session.InvoiceID {
			return existing, nil
		}
		return nil, errors.New("member already has an active membership or checkout")
	}
	invoice, err := storefrontBillingInvoice(ctx, *session.InvoiceID)
	if err != nil {
		return nil, err
	}
	if invoice.TotalCents != target.Amount || !strings.EqualFold(invoice.Currency, target.Currency) {
		return nil, errors.New("Billing invoice does not match this membership offer")
	}
	msID := newID("msub")
	if _, err = ctx.AppDB().Exec(`INSERT INTO member_subscriptions(id,community_id,member_id,plan_id,billing_customer_id,status) VALUES(?,?,?,?,?,'creating')`, msID, plan.CommunityID, memberID, plan.ID, invoice.CustomerID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	periodEnd := membershipPeriodEnd(now, plan.Interval, plan.IntervalCount)
	var subOut struct {
		Subscription subscriptionSnapshot `json:"subscription"`
	}
	if err = callAppResult(ctx, "subscriptions", "subscriptions_create", map[string]any{
		"customer_id": invoice.CustomerID, "customer_email": email, "kind": "service", "status": "past_due", "billing_provider": "local",
		"currency": plan.Currency, "interval": plan.Interval, "interval_count": plan.IntervalCount, "source": "community", "source_ref": msID,
		"current_period_start": now.Format(time.RFC3339), "current_period_end": periodEnd.Format(time.RFC3339), "next_renewal_at": periodEnd.Format(time.RFC3339),
		"items":    []any{map[string]any{"product_id": plan.CatalogProductID, "price_id": plan.CatalogPriceID, "title": plan.ProductName, "quantity": 1, "unit_amount_cents": plan.UnitAmountCents, "currency": plan.Currency, "billing_scheme": "flat"}},
		"metadata": map[string]any{"source_app": "community", "community_id": plan.CommunityID, "community_member_id": memberID, "membership_plan_id": plan.ID, "auth_subject_id": authSubjectID, "collection_method": plan.CollectionMethod, "unpaid_grace_days": plan.GraceDays},
	}, &subOut); err != nil || subOut.Subscription.ID == 0 {
		return nil, failMembership(ctx.AppDB(), msID, firstMembershipErr(err, errors.New("Subscriptions returned no subscription id")))
	}
	var cycleOut struct {
		Cycle subscriptionCycle `json:"cycle"`
	}
	if err = callAppResult(ctx, "subscriptions", "subscription_cycles_create", map[string]any{
		"subscription_id": subOut.Subscription.ID, "period_start": now.Format(time.RFC3339), "period_end": periodEnd.Format(time.RFC3339),
		"due_at": now.Format(time.RFC3339), "payment_status": "pending", "metadata": map[string]any{"source": "community_initial", "member_subscription_id": msID},
	}, &cycleOut); err != nil {
		return nil, failMembership(ctx.AppDB(), msID, err)
	}
	if err = callAppResult(ctx, "subscriptions", "subscription_cycles_update", map[string]any{"id": cycleOut.Cycle.ID, "invoice_id": *session.InvoiceID, "payment_status": "pending"}, &map[string]any{}); err != nil {
		return nil, failMembership(ctx.AppDB(), msID, err)
	}
	_, err = ctx.AppDB().Exec(`UPDATE member_subscriptions SET subscription_id=?,status='past_due',current_period_start=?,current_period_end=?,next_renewal_at=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, subOut.Subscription.ID, now.Format(time.RFC3339), periodEnd.Format(time.RFC3339), periodEnd.Format(time.RFC3339), msID)
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO membership_checkouts(id,member_subscription_id,cycle_id,billing_invoice_id,billing_session_id,status,idempotency_key) VALUES(?,?,?,?,?,'awaiting_payment',?)`,
		newID("mcheckout"), msID, cycleOut.Cycle.ID, *session.InvoiceID, nullIfEmpty(session.ProviderSessionID), fmt.Sprintf("community-storefront:%s:%d", plan.CommunityID, session.ID))
	if err != nil {
		return nil, err
	}
	return loadMemberSubscription(ctx.AppDB(), msID)
}

func storefrontBillingInvoice(ctx *sdk.AppCtx, invoiceID int64) (*billingInvoice, error) {
	var out struct {
		Invoice billingInvoice `json:"invoice"`
	}
	if err := callAppResult(ctx, "billing", "invoices_get", map[string]any{"_project_id": scopeProject(ctx), "id": invoiceID}, &out); err != nil {
		return nil, err
	}
	if out.Invoice.ID == 0 || (out.Invoice.Status != "open" && out.Invoice.Status != "paid") {
		return nil, errors.New("Billing invoice is not payable")
	}
	return &out.Invoice, nil
}
