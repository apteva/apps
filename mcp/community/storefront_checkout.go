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
	BumpCents         int64           `json:"-"`
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
	PriceID         int64           `json:"price_id"`
	ProductID       int64           `json:"product_id"`
	UnitAmountCents int64           `json:"unit_amount_cents"`
	Currency        string          `json:"currency"`
	Quantity        float64         `json:"quantity"`
	Metadata        json.RawMessage `json:"metadata"`
}

type preparedStorefrontCheckout struct {
	OrderBumps         []storefrontOrderBump `json:"order_bumps"`
	SelectionsLocked   bool                  `json:"selections_locked"`
	OfferKind          string                `json:"offer_kind"`
	CheckoutID         int64                 `json:"checkout_session_id"`
	RecoveryToken      string                `json:"recovery_token,omitempty"`
	SessionToken       string                `json:"session_token,omitempty"`
	InvoiceID          int64                 `json:"invoice_id,omitempty"`
	InvoiceNumber      string                `json:"invoice_number,omitempty"`
	Presentation       string                `json:"presentation"`
	Mode               string                `json:"mode,omitempty"`
	AmountCents        int64                 `json:"amount_cents"`
	Currency           string                `json:"currency"`
	PaymentMethodTypes []string              `json:"payment_method_types,omitempty"`
	PaymentIntentID    string                `json:"payment_intent_id,omitempty"`
	ClientSecret       string                `json:"client_secret,omitempty"`
	PublishableKey     string                `json:"publishable_key"`
	CheckoutURL        string                `json:"checkout_url,omitempty"`
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
		CatalogPriceID int64             `json:"catalog_price_id"`
		Email          string            `json:"email"`
		CustomerName   string            `json:"customer_name"`
		RecoveryToken  string            `json:"recovery_token"`
		ReturnURL      string            `json:"return_url"`
		SuccessURL     string            `json:"success_url"`
		CancelURL      string            `json:"cancel_url"`
		PromotionCode  string            `json:"promotion_code"`
		Attribution    map[string]string `json:"attribution"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid checkout request")
		return
	}
	email := strings.TrimSpace(input.Email)
	if email != "" {
		var validateErr error
		email, validateErr = validatePurchaseEmail(email)
		if validateErr != nil {
			writeErr(w, http.StatusBadRequest, validateErr.Error())
			return
		}
	}
	target, err := resolveStorefrontOffer(globalCtx.AppDB(), community.ID, input.CatalogPriceID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	urls := map[string]string{}
	for name, raw := range map[string]string{"return_url": input.ReturnURL, "success_url": input.SuccessURL, "cancel_url": input.CancelURL} {
		value, validateErr := validateStorefrontReturnURL(r, raw, &community)
		if validateErr != nil {
			writeErr(w, http.StatusBadRequest, validateErr.Error())
			return
		}
		if value != "" {
			urls[name] = value
		}
	}
	prepared, err := prepareCheckoutWithCheckoutApp(requestAppCtx(r), community.ID, target, email, strings.TrimSpace(input.CustomerName), strings.TrimSpace(input.RecoveryToken), urls, storefrontAcquisitionInput{PromotionCode: input.PromotionCode, Attribution: input.Attribution})
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "secure checkout could not be prepared")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, prepared)
}

func validateStorefrontReturnURL(r *http.Request, raw string, communities ...*Community) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("checkout return URL must be an absolute HTTP URL")
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return parsed.String(), nil
	}
	for _, community := range communities {
		if community == nil || strings.TrimSpace(community.PortalHost) == "" {
			continue
		}
		portal, portalErr := url.Parse(community.PortalHost)
		if portalErr == nil && strings.EqualFold(parsed.Scheme, portal.Scheme) && strings.EqualFold(parsed.Host, portal.Host) {
			return parsed.String(), nil
		}
	}
	return "", errors.New("checkout return URL must use this portal origin")
}

func prepareCheckoutWithCheckoutApp(ctx *sdk.AppCtx, communityID string, target storefrontOfferTarget, email, name, recoveryToken string, urls map[string]string, acquisition ...storefrontAcquisitionInput) (*preparedStorefrontCheckout, error) {
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
		if err := validatePreparedCheckout(communityID, target, session, cart); err != nil {
			return nil, err
		}
		session.BumpCents = storefrontBumpTotal(cart, target.Price)
		saved, err := checkoutContext(ctx.AppDB(), session.ID)
		if err != nil {
			return nil, err
		}
		if err = validateStorefrontContext(saved, communityID, target, session); err != nil {
			return nil, err
		}
		if len(acquisition) > 0 && saved != nil && saved.Code != strings.TrimSpace(acquisition[0].PromotionCode) {
			return nil, errors.New("checkout promotion changed")
		}
	} else {
		var created struct {
			Cart guestCheckoutCart `json:"cart"`
		}
		if err := callAppResult(ctx, "checkout", "cart_create", map[string]any{
			"_project_id": pid,
			"metadata":    map[string]any{"source_app": "community", "community_id": communityID, "context_ref": communityID, "catalog_price_id": target.Price, "offer_kind": target.Kind},
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
		patch := map[string]any{"metadata": map[string]any{
			"source_app": "community", "community_id": communityID, "catalog_price_id": target.Price, "offer_kind": target.Kind,
		}}
		if email != "" {
			patch["email"] = email
		}
		if name != "" {
			patch["customer_name"] = name
		}
		var updated struct {
			Session guestCheckoutSession `json:"session"`
		}
		if err := callAppResult(ctx, "checkout", "checkout_update", map[string]any{
			"_project_id": pid, "session_id": session.ID, "patch": patch,
		}, &updated); err != nil {
			return nil, fmt.Errorf("label Checkout session: %w", err)
		}
		session = updated.Session
		input := storefrontAcquisitionInput{}
		if len(acquisition) > 0 {
			input = acquisition[0]
		}
		if err := prepareStorefrontContext(ctx, communityID, target, session, input); err != nil {
			return nil, err
		}
	}
	var prepared struct {
		Session guestCheckoutSession `json:"session"`
		Payment struct {
			Presentation       string   `json:"presentation"`
			Mode               string   `json:"mode"`
			AmountCents        int64    `json:"amount_cents"`
			Currency           string   `json:"currency"`
			PaymentMethodTypes []string `json:"payment_method_types"`
			PublishableKey     string   `json:"publishable_key"`
		} `json:"payment"`
	}
	if err := callAppResult(ctx, "checkout", "checkout_prepare", map[string]any{
		"_project_id": pid, "session_id": session.ID,
	}, &prepared); err != nil {
		return nil, fmt.Errorf("prepare Checkout Elements: %w", err)
	}
	bumps, err := getStorefrontBumps(ctx, communityID, session.ID)
	if err != nil {
		return nil, err
	}
	return &preparedStorefrontCheckout{
		OrderBumps: bumps, SelectionsLocked: session.Status != "started",
		OfferKind: target.Kind, CheckoutID: prepared.Session.ID, RecoveryToken: recoveryToken,
		SessionToken: cart.SessionToken, Presentation: prepared.Payment.Presentation,
		Mode: prepared.Payment.Mode, AmountCents: prepared.Payment.AmountCents,
		Currency: prepared.Payment.Currency, PaymentMethodTypes: prepared.Payment.PaymentMethodTypes,
		PublishableKey: prepared.Payment.PublishableKey,
	}, nil
}

func validatePreparedCheckout(communityID string, target storefrontOfferTarget, session guestCheckoutSession, cart guestCheckoutCart) error {
	if session.Status != "started" && session.Status != "awaiting_payment" {
		return errors.New("prepared checkout is no longer payable")
	}
	if metadataString(session.Metadata, "community_id") != communityID || metadataString(session.Metadata, "catalog_price_id") != fmt.Sprint(target.Price) || metadataString(session.Metadata, "offer_kind") != target.Kind {
		return errors.New("prepared checkout does not match this offer")
	}
	baseCount := 0
	for _, it := range cart.Items {
		if it.PriceID == target.Price && it.ProductID == target.Product && it.Quantity == 1 && it.UnitAmountCents == target.Amount && strings.EqualFold(it.Currency, target.Currency) {
			baseCount++
			continue
		}
		if metadataString(it.Metadata, "order_bump_id") == "" || it.Quantity != 1 || !strings.EqualFold(it.Currency, target.Currency) {
			return errors.New("checkout contains an invalid add-on")
		}
	}
	if baseCount != 1 {
		return errors.New("prepared checkout totals no longer match this offer")
	}

	return nil
}

func toolStorefrontCheckoutClaim(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := validateContentArgs(args); err != nil {
		return nil, err
	}
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
	if err := validatePreparedCheckout(communityID, target, restored.Session, restored.Cart); err != nil {
		return nil, err
	}
	if err := validateStorefrontCartBumps(ctx, communityID, target, restored.Session, restored.Cart); err != nil {
		return nil, err
	}
	restored.Session.BumpCents = storefrontBumpTotal(restored.Cart, target.Price)
	if restored.Session.Email != "" && !strings.EqualFold(restored.Session.Email, email) {
		return nil, errors.New("prepared checkout belongs to another email address")
	}
	memberID := strArg(args, "_viewer_member_id", strArg(args, "member_id", ""))
	if target.Kind == "one_time" {
		if err := verifyMember(ctx.AppDB(), communityID, memberID); err != nil {
			return nil, err
		}
		if active, err := hasActiveCourseEnrollment(ctx.AppDB(), target.SpaceID, memberID); err != nil {
			return nil, err
		} else if active {
			return map[string]any{"enrolled": true, "access_active": true}, nil
		}
	}
	pid := scopeProject(ctx)
	if restored.Session.Status == "started" {
		var updated struct {
			Session guestCheckoutSession `json:"session"`
		}
		if err := callAppResult(ctx, "checkout", "checkout_update", map[string]any{
			"_project_id": pid, "session_id": restored.Session.ID, "patch": map[string]any{"email": email, "metadata": checkoutBuyerMetadata(restored.Session.Metadata, memberID, strArg(args, "_auth_subject_id", ""))},
		}, &updated); err != nil {
			return nil, fmt.Errorf("bind verified buyer to Checkout session: %w", err)
		}
		restored.Session = updated.Session
		restored.Session.BumpCents = storefrontBumpTotal(restored.Cart, target.Price)
	}
	if prepareOnly, _ := args["prepare_only"].(bool); prepareOnly {
		return map[string]any{"claimed": true}, nil
	}
	if err := reserveStorefrontPromotion(ctx, communityID, target, restored.Session, email); err != nil {
		return nil, err
	}
	for _, it := range restored.Cart.Items {
		if it.PriceID == target.Price || metadataString(it.Metadata, "fulfillment_app") != "community" {
			continue
		}
		spaceID := metadataString(it.Metadata, "fulfillment_ref")
		if err := validateCommunityBumpCourse(ctx, communityID, it.ProductID, spaceID); err != nil {
			return nil, err
		}
		if err = validateCourseSaleWindow(ctx.AppDB(), spaceID, memberID); err != nil {
			return nil, err
		}
		if active, err := hasActiveCourseEnrollment(ctx.AppDB(), spaceID, memberID); err != nil {
			return nil, err
		} else if active {
			return nil, errors.New("you already own an add-on course; remove it before paying")
		}
	}
	paymentArgs := map[string]any{
		"_project_id": pid, "session_id": restored.Session.ID, "provider": "stripe", "presentation": "deferred",
		"idempotency_key": fmt.Sprintf("community-storefront:%s:%d", communityID, restored.Session.ID),
	}
	paymentArgs["expected_total_cents"] = restored.Session.TotalCents
	expectedPrices := []int64{}
	for _, it := range restored.Cart.Items {
		expectedPrices = append(expectedPrices, it.PriceID)
	}
	paymentArgs["expected_price_ids"] = expectedPrices
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
		return nil, fmt.Errorf("create verified Checkout payment: %w", err)
	}
	if paid.InvoiceID == 0 || paid.Session.InvoiceID == nil {
		return nil, errors.New("Checkout returned no Billing invoice")
	}
	if err := registerStorefrontItems(ctx, communityID, memberID, target, paid.Session, restored.Cart); err != nil {
		return nil, err
	}
	result := map[string]any{
		"offer_kind": target.Kind, "presentation": "deferred", "invoice_id": paid.InvoiceID,
		"invoice_number": paid.InvoiceNumber, "payment_intent_id": strArg(paid.Payment, "payment_intent_id", ""),
		"client_secret": strArg(paid.Payment, "client_secret", ""), "publishable_key": strArg(paid.Payment, "publishable_key", ""),
	}
	if target.Kind == "one_time" {
		purchase, claimErr := claimCourseCheckout(ctx, target, memberID, email, strArg(args, "_auth_subject_id", ""), paid.Session)
		result["purchase"] = purchase
		result["enrolled"] = purchase == nil && claimErr == nil
		return result, claimErr
	}
	subscription, claimErr := claimMembershipCheckout(ctx, target, memberID, email, strArg(args, "_auth_subject_id", ""), paid.Session)
	result["subscription"] = subscription
	return result, claimErr
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
		if saved, e := checkoutContext(ctx.AppDB(), session.ID); e != nil {
			return nil, e
		} else if saved != nil {
			offer.UnitAmountCents = saved.Subtotal - saved.Discount
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
	if invoice.ID != *session.InvoiceID || invoice.TotalCents != purchase.UnitAmountCents || !strings.EqualFold(invoice.Currency, target.Currency) {
		return nil, errors.New("Billing invoice does not match this course offer")
	}
	_, err = ctx.AppDB().Exec(`UPDATE course_purchases SET billing_customer_id=?,billing_invoice_id=?,billing_session_id=?,checkout_url=NULL,status='awaiting_payment',last_error='',updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		invoice.CustomerID, *session.InvoiceID, nullIfEmpty(session.ProviderSessionID), purchase.ID)
	if err != nil {
		return nil, err
	}
	var metadata map[string]any
	if err = json.Unmarshal(invoice.Metadata, &metadata); err != nil && len(invoice.Metadata) > 0 {
		return nil, err
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["source_app"] = "community"
	metadata["community_purchase_id"] = purchase.ID
	metadata["community_id"] = space.CommunityID
	metadata["checkout_session_id"] = session.ID
	if saved, e := checkoutContext(ctx.AppDB(), session.ID); e != nil {
		return nil, e
	} else if saved != nil {
		metadata["attribution"] = saved.Attribution
		metadata["promotion_code"] = saved.Code
		metadata["catalog_subtotal_cents"] = saved.Subtotal
		metadata["discount_cents"] = saved.Discount
		metadata["discount_reservation_id"] = saved.Reservation
	}
	var invoiceUpdated any
	if err = callAppResult(ctx, "billing", "invoices_update", map[string]any{"id": invoice.ID, "patch": map[string]any{"metadata": metadata}}, &invoiceUpdated); err != nil {
		return nil, err
	}
	if err = bindStorefrontPurchase(ctx, session.ID, purchase.ID); err != nil {
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
	if invoice.ID != *session.InvoiceID || invoice.TotalCents != target.Amount || !strings.EqualFold(invoice.Currency, target.Currency) {
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
	projected, err := projectStorefrontInvoice(ctx, out.Invoice)
	return &projected, err
}
