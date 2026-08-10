package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const purchaseSource = "community_purchase"

type CourseOffer struct {
	SpaceID          string  `json:"space_id"`
	CatalogProductID int64   `json:"catalog_product_id"`
	CatalogPriceID   int64   `json:"catalog_price_id"`
	ProductName      string  `json:"product_name"`
	PriceNickname    string  `json:"price_nickname,omitempty"`
	UnitAmountCents  int64   `json:"unit_amount_cents"`
	Currency         string  `json:"currency"`
	Active           bool    `json:"active"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	ArchivedAt       *string `json:"archived_at,omitempty"`
}

type CoursePurchase struct {
	ID                string  `json:"id"`
	CommunityID       string  `json:"community_id"`
	SpaceID           string  `json:"space_id"`
	MemberID          string  `json:"member_id"`
	CatalogProductID  int64   `json:"catalog_product_id"`
	CatalogPriceID    int64   `json:"catalog_price_id"`
	ProductName       string  `json:"product_name"`
	UnitAmountCents   int64   `json:"unit_amount_cents"`
	Currency          string  `json:"currency"`
	CustomerEmail     string  `json:"customer_email"`
	CustomerName      string  `json:"customer_name,omitempty"`
	AuthSubjectID     string  `json:"auth_subject_id,omitempty"`
	BillingCustomerID *int64  `json:"billing_customer_id,omitempty"`
	BillingInvoiceID  *int64  `json:"billing_invoice_id,omitempty"`
	BillingSessionID  *string `json:"billing_session_id,omitempty"`
	CheckoutURL       *string `json:"checkout_url,omitempty"`
	Status            string  `json:"status"`
	RefundedCents     int64   `json:"refunded_cents"`
	LastError         string  `json:"last_error,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	PaidAt            *string `json:"paid_at,omitempty"`
	FulfilledAt       *string `json:"fulfilled_at,omitempty"`
	CancelledAt       *string `json:"cancelled_at,omitempty"`
	RefundedAt        *string `json:"refunded_at,omitempty"`
}

type CoursePurchaseEvent struct {
	ID         int64  `json:"id"`
	PurchaseID string `json:"purchase_id"`
	EventKey   string `json:"event_key"`
	EventName  string `json:"event_name"`
	SourceApp  string `json:"source_app"`
	Payload    any    `json:"payload"`
	CreatedAt  string `json:"created_at"`
}

type catalogPrice struct {
	ID              int64  `json:"id"`
	ProductID       int64  `json:"product_id"`
	Nickname        string `json:"nickname"`
	UnitAmountCents int64  `json:"unit_amount_cents"`
	Currency        string `json:"currency"`
	Interval        string `json:"interval"`
	IntervalCount   int64  `json:"interval_count"`
	TrialDays       int64  `json:"trial_days"`
	BillingScheme   string `json:"billing_scheme"`
	Active          bool   `json:"active"`
	ArchivedAt      string `json:"archived_at"`
}

type catalogProduct struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	ArchivedAt string `json:"archived_at"`
}

type billingInvoice struct {
	ID              int64            `json:"id"`
	CustomerID      int64            `json:"customer_id"`
	Status          string           `json:"status"`
	TotalCents      int64            `json:"total_cents"`
	AmountPaidCents int64            `json:"amount_paid_cents"`
	Currency        string           `json:"currency"`
	Metadata        json.RawMessage  `json:"metadata"`
	Payments        []billingPayment `json:"payments,omitempty"`
}

type billingPayment struct {
	AmountCents int64 `json:"amount_cents"`
}

func courseSalesTools() []sdk.Tool {
	stringSchema := map[string]any{"type": "string"}
	integerSchema := map[string]any{"type": "integer"}
	return []sdk.Tool{
		{
			Name:        "course_offer_get",
			Description: "Get the active Catalog-backed sale offer for a course. Args: space_id.",
			InputSchema: schemaObject(map[string]any{"space_id": stringSchema}, []string{"space_id"}),
			Handler:     toolCourseOfferGet,
		},
		{
			Name:        "course_offer_upsert",
			Description: "Operator: bind a course to an active one-time Catalog price. The price is validated and snapshotted; the course enrollment mode becomes paid. Args: space_id, catalog_price_id.",
			InputSchema: schemaObject(map[string]any{"space_id": stringSchema, "catalog_price_id": integerSchema}, []string{"space_id", "catalog_price_id"}),
			Handler:     toolCourseOfferUpsert,
		},
		{
			Name:        "course_offer_archive",
			Description: "Operator: stop new sales for a course without changing existing purchases or enrollments. Args: space_id.",
			InputSchema: schemaObject(map[string]any{"space_id": stringSchema}, []string{"space_id"}),
			Handler:     toolCourseOfferArchive,
		},
		{
			Name:        "course_purchase_start",
			Description: "Start or resume the verified member's hosted Billing checkout for a paid course. Enrollment is granted only after invoice.paid. Args: space_id, member_id, customer_email, customer_name, success_url, cancel_url.",
			InputSchema: schemaObject(map[string]any{
				"space_id": stringSchema, "member_id": stringSchema, "customer_email": stringSchema,
				"customer_name": stringSchema, "success_url": stringSchema, "cancel_url": stringSchema,
			}, []string{"space_id", "member_id"}),
			Handler: toolCoursePurchaseStart,
		},
		{
			Name:        "course_purchase_status",
			Description: "Get and reconcile the verified member's latest purchase for a course. Args: space_id or purchase_id; member_id.",
			InputSchema: schemaObject(map[string]any{"space_id": stringSchema, "purchase_id": stringSchema, "member_id": stringSchema}, []string{"member_id"}),
			Handler:     toolCoursePurchaseStatus,
		},
		{
			Name:        "course_purchase_cancel",
			Description: "Cancel the verified member's unpaid course purchase and void its open invoice. Args: purchase_id or space_id; member_id.",
			InputSchema: schemaObject(map[string]any{"space_id": stringSchema, "purchase_id": stringSchema, "member_id": stringSchema}, []string{"member_id"}),
			Handler:     toolCoursePurchaseCancel,
		},
		{
			Name:        "course_purchases_list",
			Description: "Operator: list course purchases. Args: space_id, member_id, status, limit.",
			InputSchema: schemaObject(map[string]any{"space_id": stringSchema, "member_id": stringSchema, "status": stringSchema, "limit": integerSchema}, nil),
			Handler:     toolCoursePurchasesList,
		},
		{
			Name:        "course_purchase_get",
			Description: "Operator: get one course purchase with its reconciliation event history. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": stringSchema}, []string{"id"}),
			Handler:     toolCoursePurchaseGet,
		},
		{
			Name:        "course_purchase_reconcile",
			Description: "Operator: reconcile one course purchase against its Billing invoice. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": stringSchema}, []string{"id"}),
			Handler:     toolCoursePurchaseReconcile,
		},
		{
			Name:        "course_purchase_refund",
			Description: "Operator: request an idempotent Billing refund. Access is revoked only after Billing confirms a full refund. Args: id, amount_cents?, reason?.",
			InputSchema: schemaObject(map[string]any{"id": stringSchema, "amount_cents": integerSchema, "reason": stringSchema}, []string{"id"}),
			Handler:     toolCoursePurchaseRefund,
		},
	}
}

func toolCourseOfferGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if err := requireCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	offer, err := loadCourseOffer(ctx.AppDB(), spaceID, false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"offer": offer}, nil
}

func toolCourseOfferUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if _, err := ensureCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	priceID, ok := intArg(args, "catalog_price_id")
	if !ok || priceID <= 0 {
		return nil, errors.New("catalog_price_id must be a positive integer")
	}
	price, product, err := fetchSaleCatalog(ctx, priceID)
	if err != nil {
		return nil, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO course_offers
		   (space_id, catalog_product_id, catalog_price_id, product_name, price_nickname,
		    unit_amount_cents, currency, active, archived_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, NULL)
		 ON CONFLICT(space_id) DO UPDATE SET
		   catalog_product_id=excluded.catalog_product_id,
		   catalog_price_id=excluded.catalog_price_id,
		   product_name=excluded.product_name,
		   price_nickname=excluded.price_nickname,
		   unit_amount_cents=excluded.unit_amount_cents,
		   currency=excluded.currency,
		   active=1, archived_at=NULL, updated_at=CURRENT_TIMESTAMP`,
		spaceID, product.ID, price.ID, product.Name, price.Nickname,
		price.UnitAmountCents, strings.ToUpper(price.Currency),
	); err != nil {
		return nil, fmt.Errorf("save course offer: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO enrollment_rules (space_id, access_mode, requires_approval)
		 VALUES (?, 'paid', 0)
		 ON CONFLICT(space_id) DO UPDATE SET access_mode='paid', updated_at=CURRENT_TIMESTAMP`,
		spaceID,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	offer, err := loadCourseOffer(ctx.AppDB(), spaceID, true)
	if err == nil {
		emit(ctx, "course.offer_updated", map[string]any{
			"space_id": spaceID, "catalog_price_id": price.ID, "unit_amount_cents": price.UnitAmountCents,
			"currency": strings.ToUpper(price.Currency),
		})
	}
	return map[string]any{"offer": offer}, err
}

func toolCourseOfferArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	if _, err := ensureCourseSpace(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	result, err := ctx.AppDB().Exec(
		`UPDATE course_offers
		    SET active=0, archived_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		  WHERE space_id=? AND active=1`,
		spaceID,
	)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return nil, errors.New("active course offer not found")
	}
	offer, err := loadCourseOffer(ctx.AppDB(), spaceID, true)
	if err == nil {
		emit(ctx, "course.offer_archived", map[string]any{"space_id": spaceID})
	}
	return map[string]any{"offer": offer}, err
}

func toolCoursePurchaseStart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	space, err := ensureCourseSpace(ctx, ctx.AppDB(), spaceID)
	if err != nil {
		return nil, err
	}
	if err := verifyMember(ctx.AppDB(), space.CommunityID, memberID); err != nil {
		return nil, err
	}
	if active, err := hasActiveCourseEnrollment(ctx.AppDB(), spaceID, memberID); err != nil {
		return nil, err
	} else if active {
		return map[string]any{"already_enrolled": true, "enrolled": true, "purchase": nil}, nil
	}

	email := strings.TrimSpace(strArg(args, "customer_email", ""))
	authSubjectID := strings.TrimSpace(strArg(args, "_auth_subject_id", ""))
	if strArg(args, "_viewer_member_id", "") != "" {
		email = strings.TrimSpace(strArg(args, "_subject_email", ""))
		if email == "" {
			return nil, errors.New("verified Auth email is required to purchase a course")
		}
	}
	email, err = validatePurchaseEmail(email)
	if err != nil {
		return nil, err
	}

	purchase, err := loadActiveCoursePurchase(ctx.AppDB(), spaceID, memberID)
	if err != nil {
		return nil, err
	}
	if purchase == nil {
		offer, err := refreshCourseOffer(ctx, spaceID)
		if err != nil {
			return nil, err
		}
		if err := validateCourseSaleWindow(ctx.AppDB(), spaceID, memberID); err != nil {
			return nil, err
		}
		purchase, err = createCoursePurchase(ctx.AppDB(), space, memberID, email,
			strings.TrimSpace(strArg(args, "customer_name", "")), authSubjectID, offer)
		if err != nil {
			return nil, err
		}
	}
	if purchase.Status == "fulfilled" || purchase.Status == "paid" || purchase.Status == "refund_pending" || purchase.Status == "partially_refunded" {
		reconciled, err := reconcileCoursePurchase(ctx, purchase, "purchase.status_checked", "community", nil)
		return purchaseCheckoutResponse(reconciled), err
	}

	if err := ensureBillingCustomer(ctx, purchase); err != nil {
		recordPurchaseError(ctx.AppDB(), purchase.ID, err)
		return nil, err
	}
	if err := ensureBillingInvoice(ctx, purchase); err != nil {
		recordPurchaseError(ctx.AppDB(), purchase.ID, err)
		return nil, err
	}
	purchase, err = loadCoursePurchase(ctx.AppDB(), purchase.ID)
	if err != nil {
		return nil, err
	}
	reconciled, err := reconcileCoursePurchase(ctx, purchase, "purchase.start_reconcile", "community", nil)
	if err != nil {
		recordPurchaseError(ctx.AppDB(), purchase.ID, err)
		return nil, err
	}
	if reconciled.Status == "fulfilled" {
		return purchaseCheckoutResponse(reconciled), nil
	}

	var payment struct {
		URL             string `json:"url"`
		StripeSessionID string `json:"stripe_session_id"`
	}
	input := map[string]any{
		"_project_id":     scopeProject(ctx),
		"invoice_id":      *reconciled.BillingInvoiceID,
		"presentation":    "hosted",
		"idempotency_key": "community-purchase:" + reconciled.ID,
	}
	if value := strings.TrimSpace(strArg(args, "success_url", "")); value != "" {
		input["success_url"] = value
	}
	if value := strings.TrimSpace(strArg(args, "cancel_url", "")); value != "" {
		input["cancel_url"] = value
	}
	if err := callAppResult(ctx, "billing", "invoices_create_payment_session", input, &payment); err != nil {
		recordPurchaseStatusError(ctx.AppDB(), reconciled.ID, "payment_failed", err)
		return nil, fmt.Errorf("create Billing payment session: %w", err)
	}
	if strings.TrimSpace(payment.URL) == "" {
		err := errors.New("Billing returned no hosted checkout URL")
		recordPurchaseStatusError(ctx.AppDB(), reconciled.ID, "payment_failed", err)
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE course_purchases
		    SET billing_session_id=?, checkout_url=?, status='awaiting_payment',
		        last_error='', updated_at=CURRENT_TIMESTAMP
		  WHERE id=?`,
		nullIfEmpty(payment.StripeSessionID), payment.URL, reconciled.ID,
	); err != nil {
		return nil, err
	}
	purchase, err = loadCoursePurchase(ctx.AppDB(), reconciled.ID)
	if err == nil {
		emit(ctx, "course.purchase_started", purchaseEventPayload(purchase))
	}
	return purchaseCheckoutResponse(purchase), err
}

func toolCoursePurchaseStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	purchase, err := purchaseForArgs(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	if purchase == nil {
		return map[string]any{"purchase": nil}, nil
	}
	if err := ensureCommunityVisible(ctx, ctx.AppDB(), purchase.CommunityID); err != nil {
		return nil, err
	}
	if purchase.BillingInvoiceID != nil {
		purchase, err = reconcileCoursePurchase(ctx, purchase, "purchase.status_checked", "community", nil)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"purchase": purchase}, nil
}

func toolCoursePurchaseCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	purchase, err := purchaseForArgs(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	if purchase == nil {
		return nil, errors.New("course purchase not found")
	}
	if err := ensureCommunityVisible(ctx, ctx.AppDB(), purchase.CommunityID); err != nil {
		return nil, err
	}
	if purchase.BillingInvoiceID != nil {
		purchase, err = reconcileCoursePurchase(ctx, purchase, "purchase.cancel_reconcile", "community", nil)
		if err != nil {
			return nil, err
		}
	}
	switch purchase.Status {
	case "fulfilled", "paid", "refund_pending", "partially_refunded":
		return nil, errors.New("paid course purchases cannot be cancelled; request a refund")
	case "cancelled", "refunded":
		return map[string]any{"purchase": purchase}, nil
	}
	if purchase.BillingInvoiceID != nil {
		var result any
		if err := callAppResult(ctx, "billing", "invoices_void", map[string]any{
			"_project_id": scopeProject(ctx), "invoice_id": *purchase.BillingInvoiceID,
			"reason": "Community course checkout cancelled",
		}, &result); err != nil {
			return nil, fmt.Errorf("void Billing invoice: %w", err)
		}
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE course_purchases
		    SET status='cancelled', cancelled_at=CURRENT_TIMESTAMP,
		        checkout_url=NULL, last_error='', updated_at=CURRENT_TIMESTAMP
		  WHERE id=? AND status NOT IN ('paid','fulfilled','refund_pending','partially_refunded','refunded')`,
		purchase.ID,
	); err != nil {
		return nil, err
	}
	purchase, err = loadCoursePurchase(ctx.AppDB(), purchase.ID)
	if err == nil {
		emit(ctx, "course.purchase_cancelled", purchaseEventPayload(purchase))
	}
	return map[string]any{"purchase": purchase}, err
}

func toolCoursePurchasesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	purchases, err := listCoursePurchases(ctx.AppDB(), scopeProject(ctx), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"purchases": purchases, "count": len(purchases)}, nil
}

func toolCoursePurchaseGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	purchase, err := loadCoursePurchaseForProject(ctx.AppDB(), scopeProject(ctx), id)
	if err != nil {
		return nil, err
	}
	events, err := loadCoursePurchaseEvents(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"purchase": purchase, "events": events}, nil
}

func toolCoursePurchaseReconcile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	purchase, err := loadCoursePurchaseForProject(ctx.AppDB(), scopeProject(ctx), id)
	if err != nil {
		return nil, err
	}
	if purchase.BillingInvoiceID == nil {
		return nil, errors.New("purchase has no Billing invoice yet")
	}
	purchase, err = reconcileCoursePurchase(ctx, purchase, "purchase.operator_reconciled", "community", args)
	return map[string]any{"purchase": purchase}, err
}

func toolCoursePurchaseRefund(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	purchase, err := loadCoursePurchaseForProject(ctx.AppDB(), scopeProject(ctx), id)
	if err != nil {
		return nil, err
	}
	if purchase.BillingInvoiceID == nil || (purchase.Status != "fulfilled" && purchase.Status != "partially_refunded") {
		return nil, errors.New("only a fulfilled course purchase can be refunded")
	}
	input := map[string]any{
		"_project_id":     scopeProject(ctx),
		"invoice_id":      *purchase.BillingInvoiceID,
		"idempotency_key": "community-refund:" + purchase.ID + ":" + fmt.Sprint(purchase.RefundedCents),
	}
	if amount, ok := intArg(args, "amount_cents"); ok && amount > 0 {
		input["amount_cents"] = amount
		input["idempotency_key"] = fmt.Sprintf("%s:%d", input["idempotency_key"], amount)
	}
	if reason := strings.TrimSpace(strArg(args, "reason", "")); reason != "" {
		input["reason"] = reason
	}
	var out map[string]any
	if err := callAppResult(ctx, "billing", "invoices_refund", input, &out); err != nil {
		return nil, fmt.Errorf("request Billing refund: %w", err)
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE course_purchases SET status='refund_pending', last_error='', updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		purchase.ID,
	); err != nil {
		return nil, err
	}
	purchase, err = loadCoursePurchase(ctx.AppDB(), purchase.ID)
	if err == nil {
		emit(ctx, "course.refund_requested", purchaseEventPayload(purchase))
	}
	return map[string]any{"purchase": purchase, "billing": out}, err
}

func (a *App) handleCourseBillingEvent(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "billing" {
		return nil
	}
	invoiceID := numberFromAny(event.Data["id"])
	if invoiceID == 0 {
		invoiceID = numberFromAny(event.Data["invoice_id"])
	}
	if invoiceID == 0 {
		return nil
	}
	purchase, err := loadCoursePurchaseByInvoice(ctx.AppDB(), event.ProjectID, invoiceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	_, err = reconcileCoursePurchase(ctx, purchase, event.Name(), firstNonEmptyString(event.SourceApp, "billing"), event.Data)
	return err
}

func fetchSaleCatalog(ctx *sdk.AppCtx, priceID int64) (catalogPrice, catalogProduct, error) {
	var priceOut struct {
		Price catalogPrice `json:"price"`
	}
	if err := callAppResult(ctx, "catalog", "catalog_prices_get", map[string]any{
		"_project_id": scopeProject(ctx), "id": priceID,
	}, &priceOut); err != nil {
		return catalogPrice{}, catalogProduct{}, fmt.Errorf("get Catalog price: %w", err)
	}
	price := priceOut.Price
	if price.ID == 0 || price.ProductID == 0 {
		return catalogPrice{}, catalogProduct{}, errors.New("Catalog price response is incomplete")
	}
	if !price.Active || price.ArchivedAt != "" {
		return catalogPrice{}, catalogProduct{}, errors.New("Catalog price is inactive or archived")
	}
	if strings.TrimSpace(price.Interval) != "" {
		return catalogPrice{}, catalogProduct{}, errors.New("course sales currently require a one-time Catalog price")
	}
	if price.BillingScheme != "" && price.BillingScheme != "flat" {
		return catalogPrice{}, catalogProduct{}, errors.New("course sales require a flat Catalog price")
	}
	if price.UnitAmountCents <= 0 {
		return catalogPrice{}, catalogProduct{}, errors.New("paid course Catalog price must be greater than zero")
	}
	var productOut struct {
		Product catalogProduct `json:"product"`
	}
	if err := callAppResult(ctx, "catalog", "catalog_products_get", map[string]any{
		"_project_id": scopeProject(ctx), "id": price.ProductID,
	}, &productOut); err != nil {
		return catalogPrice{}, catalogProduct{}, fmt.Errorf("get Catalog product: %w", err)
	}
	product := productOut.Product
	if product.ID == 0 || product.ArchivedAt != "" {
		return catalogPrice{}, catalogProduct{}, errors.New("Catalog product is missing or archived")
	}
	if product.Type != "one_time" && product.Type != "service" {
		return catalogPrice{}, catalogProduct{}, errors.New("course sales currently require a one-time Catalog product")
	}
	return price, product, nil
}

func refreshCourseOffer(ctx *sdk.AppCtx, spaceID string) (*CourseOffer, error) {
	offer, err := loadCourseOffer(ctx.AppDB(), spaceID, false)
	if err != nil {
		return nil, err
	}
	if offer == nil {
		return nil, errors.New("this course is not currently for sale")
	}
	price, product, err := fetchSaleCatalog(ctx, offer.CatalogPriceID)
	if err != nil {
		return nil, err
	}
	if price.ProductID != offer.CatalogProductID {
		return nil, errors.New("Catalog price no longer belongs to the configured product")
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE course_offers
		    SET product_name=?, price_nickname=?, unit_amount_cents=?, currency=?,
		        updated_at=CURRENT_TIMESTAMP
		  WHERE space_id=?`,
		product.Name, price.Nickname, price.UnitAmountCents, strings.ToUpper(price.Currency), spaceID,
	); err != nil {
		return nil, err
	}
	return loadCourseOffer(ctx.AppDB(), spaceID, false)
}

func ensureBillingCustomer(ctx *sdk.AppCtx, purchase *CoursePurchase) error {
	if purchase.BillingCustomerID != nil {
		return nil
	}
	var out struct {
		Customer struct {
			ID int64 `json:"id"`
		} `json:"customer"`
	}
	defaults := map[string]any{
		"name":        purchase.CustomerName,
		"external_id": purchase.AuthSubjectID,
		"metadata": map[string]any{
			"source_app": "community", "community_member_id": purchase.MemberID,
			"auth_subject_id": purchase.AuthSubjectID,
		},
	}
	if err := callAppResult(ctx, "billing", "customers_upsert_by_email", map[string]any{
		"_project_id": scopeProject(ctx), "email": purchase.CustomerEmail, "defaults": defaults,
	}, &out); err != nil {
		return fmt.Errorf("upsert Billing customer: %w", err)
	}
	if out.Customer.ID == 0 {
		return errors.New("Billing returned no customer id")
	}
	_, err := ctx.AppDB().Exec(
		`UPDATE course_purchases SET billing_customer_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		out.Customer.ID, purchase.ID,
	)
	if err == nil {
		purchase.BillingCustomerID = &out.Customer.ID
	}
	return err
}

func ensureBillingInvoice(ctx *sdk.AppCtx, purchase *CoursePurchase) error {
	if purchase.BillingInvoiceID != nil {
		return nil
	}
	if purchase.BillingCustomerID == nil {
		return errors.New("Billing customer is missing")
	}
	if recovered, err := recoverBillingInvoice(ctx, purchase); err != nil {
		return err
	} else if recovered != nil {
		if err := savePurchaseInvoice(ctx.AppDB(), purchase.ID, recovered.ID); err != nil {
			return err
		}
		purchase.BillingInvoiceID = &recovered.ID
		return finalizeBillingInvoice(ctx, purchase, recovered)
	}

	metadata := map[string]any{
		"source_app": "community", "flow": "course_purchase",
		"community_purchase_id": purchase.ID, "community_id": purchase.CommunityID,
		"community_space_id": purchase.SpaceID, "community_member_id": purchase.MemberID,
		"auth_subject_id": purchase.AuthSubjectID, "catalog_product_id": purchase.CatalogProductID,
		"catalog_price_id": purchase.CatalogPriceID,
	}
	var created struct {
		Invoice billingInvoice `json:"invoice"`
	}
	if err := callAppResult(ctx, "billing", "invoices_create", map[string]any{
		"_project_id": scopeProject(ctx), "customer_id": *purchase.BillingCustomerID,
		"currency": purchase.Currency, "provider": "local",
		"line_items": []any{map[string]any{
			"price_id": purchase.CatalogPriceID, "quantity": 1,
			"metadata": map[string]any{"community_purchase_id": purchase.ID, "community_space_id": purchase.SpaceID},
		}},
		"metadata": metadata,
	}, &created); err != nil {
		return fmt.Errorf("create Billing invoice: %w", err)
	}
	if created.Invoice.ID == 0 {
		return errors.New("Billing returned no invoice id")
	}
	if err := savePurchaseInvoice(ctx.AppDB(), purchase.ID, created.Invoice.ID); err != nil {
		return err
	}
	purchase.BillingInvoiceID = &created.Invoice.ID
	return finalizeBillingInvoice(ctx, purchase, &created.Invoice)
}

func recoverBillingInvoice(ctx *sdk.AppCtx, purchase *CoursePurchase) (*billingInvoice, error) {
	var out struct {
		Invoices []billingInvoice `json:"invoices"`
	}
	if err := callAppResult(ctx, "billing", "invoices_search", map[string]any{
		"_project_id": scopeProject(ctx), "customer_id": *purchase.BillingCustomerID, "limit": 200,
	}, &out); err != nil {
		return nil, fmt.Errorf("search Billing invoices: %w", err)
	}
	for i := range out.Invoices {
		if metadataString(out.Invoices[i].Metadata, "community_purchase_id") == purchase.ID {
			return &out.Invoices[i], nil
		}
	}
	return nil, nil
}

func finalizeBillingInvoice(ctx *sdk.AppCtx, purchase *CoursePurchase, invoice *billingInvoice) error {
	if invoice.Status != "" && invoice.Status != "draft" {
		return nil
	}
	var out struct {
		Invoice billingInvoice `json:"invoice"`
	}
	if err := callAppResult(ctx, "billing", "invoices_finalize", map[string]any{
		"_project_id": scopeProject(ctx), "invoice_id": *purchase.BillingInvoiceID,
	}, &out); err != nil {
		return fmt.Errorf("finalize Billing invoice: %w", err)
	}
	return nil
}

func reconcileCoursePurchase(ctx *sdk.AppCtx, purchase *CoursePurchase, eventName, source string, payload any) (*CoursePurchase, error) {
	if purchase == nil || purchase.BillingInvoiceID == nil {
		return purchase, nil
	}
	var out struct {
		Invoice billingInvoice `json:"invoice"`
	}
	if err := callAppResult(ctx, "billing", "invoices_get", map[string]any{
		"_project_id": scopeProject(ctx), "id": *purchase.BillingInvoiceID,
	}, &out); err != nil {
		return nil, fmt.Errorf("get Billing invoice: %w", err)
	}
	invoice := out.Invoice
	if invoice.ID == 0 {
		return nil, errors.New("Billing returned no invoice")
	}
	if sourceApp := metadataString(invoice.Metadata, "source_app"); sourceApp != "" && sourceApp != "community" {
		return nil, errors.New("Billing invoice is not owned by Community")
	}
	if ref := metadataString(invoice.Metadata, "community_purchase_id"); ref != "" && ref != purchase.ID {
		return nil, errors.New("Billing invoice belongs to a different Community purchase")
	}

	switch {
	case eventName == "invoice.payment_failed":
		if _, err := ctx.AppDB().Exec(
			`UPDATE course_purchases SET status='payment_failed', last_error='payment failed',
			 updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('creating','awaiting_payment','payment_failed')`,
			purchase.ID,
		); err != nil {
			return nil, err
		}
	case invoice.AmountPaidCents >= invoice.TotalCents && invoice.TotalCents > 0 && invoice.Status == "paid":
		if err := fulfillCoursePurchase(ctx, purchase, invoice); err != nil {
			return nil, err
		}
	case (eventName == "invoice.refunded" || invoiceHasRefund(invoice) || purchase.Status == "refund_pending" ||
		purchase.Status == "partially_refunded" || purchase.Status == "fulfilled") &&
		invoice.AmountPaidCents < invoice.TotalCents:
		if err := reconcileCourseRefund(ctx, purchase, invoice); err != nil {
			return nil, err
		}
	case invoice.Status == "void":
		if _, err := ctx.AppDB().Exec(
			`UPDATE course_purchases
			    SET status='cancelled', cancelled_at=COALESCE(cancelled_at,CURRENT_TIMESTAMP),
			        checkout_url=NULL, updated_at=CURRENT_TIMESTAMP
			  WHERE id=? AND status NOT IN ('fulfilled','refunded')`,
			purchase.ID,
		); err != nil {
			return nil, err
		}
	}
	if eventName != "" {
		_ = recordCoursePurchaseEvent(ctx.AppDB(), purchase.ID, eventName, source, invoice, payload)
	}
	return loadCoursePurchase(ctx.AppDB(), purchase.ID)
}

func fulfillCoursePurchase(ctx *sdk.AppCtx, purchase *CoursePurchase, invoice billingInvoice) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE course_purchases
		    SET status='fulfilled', paid_at=COALESCE(paid_at,CURRENT_TIMESTAMP),
		        fulfilled_at=COALESCE(fulfilled_at,CURRENT_TIMESTAMP),
		        refunded_cents=0, refunded_at=NULL, checkout_url=NULL,
		        last_error='', updated_at=CURRENT_TIMESTAMP
		  WHERE id=?`,
		purchase.ID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO course_enrollments
		   (space_id, member_id, status, source, source_ref, access_revoked_at)
		 VALUES (?, ?, 'active', ?, ?, NULL)
		 ON CONFLICT(space_id, member_id) DO UPDATE SET
		   status='active', completed_at=NULL, source=excluded.source,
		   source_ref=excluded.source_ref, access_revoked_at=NULL`,
		purchase.SpaceID, purchase.MemberID, purchaseSource, purchase.ID,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	emit(ctx, "course.purchase_fulfilled", map[string]any{
		"community_id": purchase.CommunityID, "space_id": purchase.SpaceID,
		"member_id": purchase.MemberID, "purchase_id": purchase.ID, "invoice_id": invoice.ID,
	})
	emit(ctx, "course.enrolled", map[string]any{
		"community_id": purchase.CommunityID, "space_id": purchase.SpaceID,
		"member_id": purchase.MemberID, "status": "active", "source": purchaseSource,
	})
	return nil
}

func reconcileCourseRefund(ctx *sdk.AppCtx, purchase *CoursePurchase, invoice billingInvoice) error {
	refunded := invoice.TotalCents - invoice.AmountPaidCents
	if refunded < 0 {
		refunded = 0
	}
	if invoice.AmountPaidCents > 0 {
		_, err := ctx.AppDB().Exec(
			`UPDATE course_purchases
			    SET status='partially_refunded', refunded_cents=?, refunded_at=NULL,
			        last_error='', updated_at=CURRENT_TIMESTAMP
			  WHERE id=?`,
			refunded, purchase.ID,
		)
		if err == nil {
			emit(ctx, "course.purchase_partially_refunded", map[string]any{
				"community_id": purchase.CommunityID, "space_id": purchase.SpaceID,
				"member_id": purchase.MemberID, "purchase_id": purchase.ID, "refunded_cents": refunded,
			})
		}
		return err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE course_purchases
		    SET status='refunded', refunded_cents=?, refunded_at=COALESCE(refunded_at,CURRENT_TIMESTAMP),
		        checkout_url=NULL, last_error='', updated_at=CURRENT_TIMESTAMP
		  WHERE id=?`,
		refunded, purchase.ID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE course_enrollments
		    SET status='cancelled', access_revoked_at=CURRENT_TIMESTAMP
		  WHERE space_id=? AND member_id=? AND source=? AND source_ref=?`,
		purchase.SpaceID, purchase.MemberID, purchaseSource, purchase.ID,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	emit(ctx, "course.purchase_refunded", map[string]any{
		"community_id": purchase.CommunityID, "space_id": purchase.SpaceID,
		"member_id": purchase.MemberID, "purchase_id": purchase.ID, "refunded_cents": refunded,
	})
	emit(ctx, "course.access_revoked", map[string]any{
		"community_id": purchase.CommunityID, "space_id": purchase.SpaceID,
		"member_id": purchase.MemberID, "source": purchaseSource,
	})
	return nil
}

func loadCourseOffer(db *sql.DB, spaceID string, includeInactive bool) (*CourseOffer, error) {
	query := `SELECT space_id, catalog_product_id, catalog_price_id, product_name, price_nickname,
	                 unit_amount_cents, currency, active, created_at, updated_at, archived_at
	            FROM course_offers WHERE space_id=?`
	if !includeInactive {
		query += ` AND active=1 AND archived_at IS NULL`
	}
	var offer CourseOffer
	var active int
	var archived sql.NullString
	err := db.QueryRow(query, spaceID).Scan(
		&offer.SpaceID, &offer.CatalogProductID, &offer.CatalogPriceID, &offer.ProductName,
		&offer.PriceNickname, &offer.UnitAmountCents, &offer.Currency, &active,
		&offer.CreatedAt, &offer.UpdatedAt, &archived,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	offer.Active = active != 0
	if archived.Valid {
		offer.ArchivedAt = &archived.String
	}
	return &offer, nil
}

func createCoursePurchase(db *sql.DB, space Space, memberID, email, name, authSubjectID string, offer *CourseOffer) (*CoursePurchase, error) {
	id := newID("cp")
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var max sql.NullInt64
	if err := tx.QueryRow(
		`SELECT max_enrollments FROM enrollment_rules WHERE space_id=?`,
		space.ID,
	).Scan(&max); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if max.Valid && max.Int64 > 0 {
		var count int64
		if err := tx.QueryRow(
			`SELECT COUNT(DISTINCT member_id) FROM (
			   SELECT member_id FROM course_enrollments
			    WHERE space_id=? AND member_id<>? AND status IN ('pending','active','completed')
			   UNION ALL
			   SELECT member_id FROM course_purchases
			    WHERE space_id=? AND member_id<>?
			      AND status IN ('creating','awaiting_payment','paid','fulfilled','refund_pending','partially_refunded')
			 )`,
			space.ID, memberID, space.ID, memberID,
		).Scan(&count); err != nil {
			return nil, err
		}
		if count >= max.Int64 {
			return nil, errors.New("course enrollment limit reached")
		}
	}
	_, err = tx.Exec(
		`INSERT INTO course_purchases
		   (id, community_id, space_id, member_id, catalog_product_id, catalog_price_id,
		    product_name, unit_amount_cents, currency, customer_email, customer_name, auth_subject_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, space.CommunityID, space.ID, memberID, offer.CatalogProductID, offer.CatalogPriceID,
		offer.ProductName, offer.UnitAmountCents, offer.Currency, email, name, authSubjectID,
	)
	if err != nil {
		_ = tx.Rollback()
		if existing, loadErr := loadActiveCoursePurchase(db, space.ID, memberID); loadErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return loadCoursePurchase(db, id)
}

const coursePurchaseColumns = `p.id, p.community_id, p.space_id, p.member_id,
	p.catalog_product_id, p.catalog_price_id, p.product_name, p.unit_amount_cents, p.currency,
	p.customer_email, p.customer_name, p.auth_subject_id, p.billing_customer_id,
	p.billing_invoice_id, p.billing_session_id, p.checkout_url, p.status, p.refunded_cents,
	p.last_error, p.created_at, p.updated_at, p.paid_at, p.fulfilled_at, p.cancelled_at, p.refunded_at`

func scanCoursePurchase(scan func(...any) error) (*CoursePurchase, error) {
	var purchase CoursePurchase
	var customerID, invoiceID sql.NullInt64
	var sessionID, checkoutURL, paidAt, fulfilledAt, cancelledAt, refundedAt sql.NullString
	err := scan(
		&purchase.ID, &purchase.CommunityID, &purchase.SpaceID, &purchase.MemberID,
		&purchase.CatalogProductID, &purchase.CatalogPriceID, &purchase.ProductName,
		&purchase.UnitAmountCents, &purchase.Currency, &purchase.CustomerEmail,
		&purchase.CustomerName, &purchase.AuthSubjectID, &customerID, &invoiceID,
		&sessionID, &checkoutURL, &purchase.Status, &purchase.RefundedCents,
		&purchase.LastError, &purchase.CreatedAt, &purchase.UpdatedAt, &paidAt,
		&fulfilledAt, &cancelledAt, &refundedAt,
	)
	if err != nil {
		return nil, err
	}
	if customerID.Valid {
		purchase.BillingCustomerID = &customerID.Int64
	}
	if invoiceID.Valid {
		purchase.BillingInvoiceID = &invoiceID.Int64
	}
	if sessionID.Valid {
		purchase.BillingSessionID = &sessionID.String
	}
	if checkoutURL.Valid {
		purchase.CheckoutURL = &checkoutURL.String
	}
	if paidAt.Valid {
		purchase.PaidAt = &paidAt.String
	}
	if fulfilledAt.Valid {
		purchase.FulfilledAt = &fulfilledAt.String
	}
	if cancelledAt.Valid {
		purchase.CancelledAt = &cancelledAt.String
	}
	if refundedAt.Valid {
		purchase.RefundedAt = &refundedAt.String
	}
	return &purchase, nil
}

func loadCoursePurchase(db *sql.DB, id string) (*CoursePurchase, error) {
	purchase, err := scanCoursePurchase(db.QueryRow(
		`SELECT `+coursePurchaseColumns+` FROM course_purchases p WHERE p.id=?`, id,
	).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("course purchase not found")
	}
	return purchase, err
}

func loadCoursePurchaseForProject(db *sql.DB, projectID, id string) (*CoursePurchase, error) {
	purchase, err := scanCoursePurchase(db.QueryRow(
		`SELECT `+coursePurchaseColumns+`
		   FROM course_purchases p
		   JOIN communities c ON c.id=p.community_id
		  WHERE p.id=? AND c.project_id=?`,
		id, projectID,
	).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("course purchase not found")
	}
	return purchase, err
}

func loadCoursePurchaseByInvoice(db *sql.DB, projectID string, invoiceID int64) (*CoursePurchase, error) {
	query := `SELECT ` + coursePurchaseColumns + `
	            FROM course_purchases p
	            JOIN communities c ON c.id=p.community_id
	           WHERE p.billing_invoice_id=?`
	args := []any{invoiceID}
	if projectID != "" {
		query += ` AND c.project_id=?`
		args = append(args, projectID)
	}
	return scanCoursePurchase(db.QueryRow(query, args...).Scan)
}

func loadActiveCoursePurchase(db *sql.DB, spaceID, memberID string) (*CoursePurchase, error) {
	purchase, err := scanCoursePurchase(db.QueryRow(
		`SELECT `+coursePurchaseColumns+`
		   FROM course_purchases p
		  WHERE p.space_id=? AND p.member_id=?
		    AND p.status IN ('creating','awaiting_payment','payment_failed','paid','fulfilled','refund_pending','partially_refunded')
		  ORDER BY p.created_at DESC LIMIT 1`,
		spaceID, memberID,
	).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return purchase, err
}

func purchaseForArgs(db *sql.DB, args map[string]any) (*CoursePurchase, error) {
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	if id := strings.TrimSpace(strArg(args, "purchase_id", "")); id != "" {
		purchase, err := loadCoursePurchase(db, id)
		if err != nil {
			return nil, err
		}
		if purchase.MemberID != memberID {
			return nil, errors.New("course purchase does not belong to this member")
		}
		return purchase, nil
	}
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, errors.New("space_id or purchase_id is required")
	}
	purchase, err := scanCoursePurchase(db.QueryRow(
		`SELECT `+coursePurchaseColumns+`
		   FROM course_purchases p
		  WHERE p.space_id=? AND p.member_id=?
		  ORDER BY p.created_at DESC LIMIT 1`,
		spaceID, memberID,
	).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return purchase, err
}

func listCoursePurchases(db *sql.DB, projectID string, args map[string]any) ([]*CoursePurchase, error) {
	where := []string{"c.project_id=?"}
	values := []any{projectID}
	for _, item := range []struct {
		arg, column string
	}{
		{"space_id", "p.space_id"}, {"member_id", "p.member_id"}, {"status", "p.status"},
	} {
		if value := strings.TrimSpace(strArg(args, item.arg, "")); value != "" {
			where = append(where, item.column+"=?")
			values = append(values, value)
		}
	}
	values = append(values, boundedLimit(args, "limit", 100, 500))
	rows, err := db.Query(
		`SELECT `+coursePurchaseColumns+`
		   FROM course_purchases p
		   JOIN communities c ON c.id=p.community_id
		  WHERE `+strings.Join(where, " AND ")+`
		  ORDER BY p.created_at DESC LIMIT ?`,
		values...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CoursePurchase{}
	for rows.Next() {
		purchase, err := scanCoursePurchase(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, purchase)
	}
	return out, rows.Err()
}

func loadCoursePurchaseEvents(db *sql.DB, purchaseID string) ([]CoursePurchaseEvent, error) {
	rows, err := db.Query(
		`SELECT id, purchase_id, event_key, event_name, source_app, payload_json, created_at
		   FROM course_purchase_events WHERE purchase_id=? ORDER BY created_at DESC, id DESC`,
		purchaseID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CoursePurchaseEvent{}
	for rows.Next() {
		var event CoursePurchaseEvent
		var payload string
		if err := rows.Scan(&event.ID, &event.PurchaseID, &event.EventKey, &event.EventName, &event.SourceApp, &payload, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			event.Payload = map[string]any{}
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func recordCoursePurchaseEvent(db *sql.DB, purchaseID, eventName, source string, invoice billingInvoice, payload any) error {
	body, _ := json.Marshal(map[string]any{"invoice": invoice, "event": payload})
	key := fmt.Sprintf("%s:%d:%s:%d:%d", eventName, invoice.ID, invoice.Status, invoice.AmountPaidCents, invoice.TotalCents)
	_, err := db.Exec(
		`INSERT OR IGNORE INTO course_purchase_events
		   (purchase_id, event_key, event_name, source_app, payload_json)
		 VALUES (?, ?, ?, ?, ?)`,
		purchaseID, key, eventName, source, string(body),
	)
	return err
}

func validateCourseSaleWindow(db *sql.DB, spaceID, memberID string) error {
	rule, err := loadEnrollmentRule(db, spaceID)
	if err != nil {
		return err
	}
	if rule.AccessMode != "paid" {
		return errors.New("course enrollment mode must be paid before checkout")
	}
	now := time.Now().UTC()
	if rule.StartsAt != nil {
		start, err := time.Parse(time.RFC3339, *rule.StartsAt)
		if err != nil || now.Before(start) {
			return errors.New("course sales have not opened")
		}
	}
	if rule.EndsAt != nil {
		end, err := time.Parse(time.RFC3339, *rule.EndsAt)
		if err != nil || now.After(end) {
			return errors.New("course sales have closed")
		}
	}
	return nil
}

func hasActiveCourseEnrollment(db *sql.DB, spaceID, memberID string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM course_enrollments
		  WHERE space_id=? AND member_id=? AND status IN ('active','completed')
		    AND access_revoked_at IS NULL
		    AND (access_expires_at IS NULL OR datetime(access_expires_at)>=CURRENT_TIMESTAMP)`,
		spaceID, memberID,
	).Scan(&count)
	return count > 0, err
}

func savePurchaseInvoice(db *sql.DB, purchaseID string, invoiceID int64) error {
	_, err := db.Exec(
		`UPDATE course_purchases SET billing_invoice_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		invoiceID, purchaseID,
	)
	return err
}

func recordPurchaseError(db *sql.DB, id string, err error) {
	if err == nil {
		return
	}
	_, _ = db.Exec(
		`UPDATE course_purchases SET last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		truncateText(err.Error(), 1000), id,
	)
}

func recordPurchaseStatusError(db *sql.DB, id, status string, err error) {
	if err == nil {
		return
	}
	_, _ = db.Exec(
		`UPDATE course_purchases SET status=?, last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		status, truncateText(err.Error(), 1000), id,
	)
}

func purchaseCheckoutResponse(purchase *CoursePurchase) map[string]any {
	out := map[string]any{"purchase": purchase}
	if purchase != nil && purchase.CheckoutURL != nil {
		out["checkout_url"] = *purchase.CheckoutURL
	}
	if purchase != nil && purchase.Status == "fulfilled" {
		out["enrolled"] = true
	}
	return out
}

func purchaseEventPayload(purchase *CoursePurchase) map[string]any {
	if purchase == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"community_id": purchase.CommunityID, "space_id": purchase.SpaceID,
		"member_id": purchase.MemberID, "purchase_id": purchase.ID, "status": purchase.Status,
	}
	if purchase.BillingInvoiceID != nil {
		out["invoice_id"] = *purchase.BillingInvoiceID
	}
	return out
}

func callAppResult(ctx *sdk.AppCtx, app, tool string, input map[string]any, out any) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform API unavailable")
	}
	if _, ok := input["_project_id"]; !ok {
		input["_project_id"] = scopeProject(ctx)
	}
	return ctx.PlatformAPI().CallAppResult(app, tool, input, out)
}

func validatePurchaseEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || !strings.EqualFold(parsed.Address, value) || !strings.Contains(value, "@") {
		return "", errors.New("a valid verified email is required for Billing")
	}
	return value, nil
}

func metadataString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func invoiceHasRefund(invoice billingInvoice) bool {
	for _, payment := range invoice.Payments {
		if payment.AmountCents < 0 {
			return true
		}
	}
	return false
}

func numberFromAny(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		out, _ := typed.Int64()
		return out
	default:
		return 0
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func truncateText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
