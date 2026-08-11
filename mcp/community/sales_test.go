package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type salesPlatformStub struct {
	tk.BasePlatformClient
	mu               sync.Mutex
	price            catalogPrice
	product          catalogProduct
	invoice          billingInvoice
	invoiceCreates   int
	sessionCreates   int
	checkoutSession  guestCheckoutSession
	checkoutCart     guestCheckoutCart
	checkoutMeta     json.RawMessage
	checkoutPrepares int
	checkoutPayments int
}

func newSalesPlatformStub() *salesPlatformStub {
	return &salesPlatformStub{
		price: catalogPrice{
			ID: 71, ProductID: 61, Nickname: "Launch price", UnitAmountCents: 9900,
			Currency: "EUR", BillingScheme: "flat", Active: true,
		},
		product: catalogProduct{ID: 61, Name: "Community Course", Slug: "community-course", Description: "Build a stronger community.", Type: "one_time"},
	}
}

func (s *salesPlatformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var value any
	switch app + ":" + tool {
	case "catalog:catalog_prices_get":
		value = map[string]any{"price": s.price}
	case "catalog:catalog_products_get":
		value = map[string]any{"product": s.product}
	case "billing:customers_upsert_by_email":
		value = map[string]any{"customer": map[string]any{"id": int64(501)}, "was_created": true}
	case "billing:invoices_search":
		invoices := []billingInvoice{}
		if s.invoice.ID != 0 {
			invoices = append(invoices, s.invoice)
		}
		value = map[string]any{"invoices": invoices, "count": len(invoices)}
	case "billing:invoices_create":
		s.invoiceCreates++
		metadata, _ := json.Marshal(input["metadata"])
		s.invoice = billingInvoice{
			ID: 701, CustomerID: 501, Status: "draft", TotalCents: s.price.UnitAmountCents,
			Currency: s.price.Currency, Metadata: metadata,
		}
		value = map[string]any{"invoice": s.invoice}
	case "billing:invoices_finalize":
		s.invoice.Status = "open"
		value = map[string]any{"invoice": s.invoice}
	case "billing:invoices_create_payment_session":
		s.sessionCreates++
		if input["presentation"] == "elements" {
			value = map[string]any{"presentation": "elements", "client_secret": "cs_test_secret", "publishable_key": "pk_test_public", "stripe_session_id": "cs_test_course"}
		} else {
			value = map[string]any{"presentation": "hosted", "url": "https://payments.example.test/session", "stripe_session_id": "cs_test_course"}
		}
	case "checkout:cart_create":
		s.checkoutCart = guestCheckoutCart{ID: 801, SessionToken: "cart-recovery"}
		s.checkoutMeta, _ = json.Marshal(input["metadata"])
		value = map[string]any{"cart": s.checkoutCart}
	case "checkout:cart_add_item":
		s.checkoutCart.Items = []guestCheckoutItem{{PriceID: s.price.ID, ProductID: s.product.ID, UnitAmountCents: s.price.UnitAmountCents, Currency: s.price.Currency, Quantity: 1}}
		value = map[string]any{"cart": s.checkoutCart}
	case "checkout:checkout_start":
		s.checkoutSession = guestCheckoutSession{ID: 802, CartID: s.checkoutCart.ID, Status: "started", TotalCents: s.price.UnitAmountCents, Currency: s.price.Currency, Metadata: s.checkoutMeta, RecoveryToken: "checkout-recovery"}
		value = map[string]any{"session": s.checkoutSession}
	case "checkout:checkout_update":
		patch, _ := input["patch"].(map[string]any)
		if email, ok := patch["email"].(string); ok {
			s.checkoutSession.Email = email
		}
		if name, ok := patch["customer_name"].(string); ok {
			s.checkoutSession.CustomerName = name
		}
		if metadata, ok := patch["metadata"]; ok {
			s.checkoutSession.Metadata, _ = json.Marshal(metadata)
		}
		value = map[string]any{"session": s.checkoutSession}
	case "checkout:checkout_prepare":
		s.checkoutPrepares++
		value = map[string]any{"session": s.checkoutSession, "payment": map[string]any{
			"provider": "stripe", "presentation": "deferred", "mode": "payment",
			"amount_cents": s.price.UnitAmountCents, "currency": strings.ToLower(s.price.Currency),
			"payment_method_types": []string{"card"}, "publishable_key": "pk_test_public",
		}}
	case "checkout:checkout_bootstrap":
		value = map[string]any{"session": s.checkoutSession, "cart": s.checkoutCart}
	case "checkout:checkout_pay":
		s.checkoutPayments++
		s.invoice = billingInvoice{ID: 701, CustomerID: 501, Status: "open", TotalCents: s.price.UnitAmountCents, Currency: s.price.Currency}
		invoiceID := s.invoice.ID
		s.checkoutSession.Status = "awaiting_payment"
		s.checkoutSession.InvoiceID = &invoiceID
		s.checkoutSession.ProviderSessionID = "pi_test_course"
		value = map[string]any{"session": s.checkoutSession, "invoice_id": invoiceID, "invoice_number": "INV-701", "payment": map[string]any{
			"provider": "stripe", "presentation": "deferred", "payment_intent_id": "pi_test_course",
			"client_secret": "pi_test_course_secret", "publishable_key": "pk_test_public",
		}}
	case "billing:invoices_get":
		if s.invoice.ID == 0 {
			return errors.New("invoice not found")
		}
		value = map[string]any{"invoice": s.invoice}
	case "billing:invoices_void":
		s.invoice.Status = "void"
		value = map[string]any{"invoice": s.invoice}
	case "billing:invoices_refund":
		value = map[string]any{"refund": map[string]any{"id": int64(801), "status": "submitted"}}
	default:
		return errors.New("unexpected cross-app call: " + app + ":" + tool)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func newSalesTestCtx(t *testing.T, platform *salesPlatformStub) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithPlatform(platform),
	)
	globalCtx = ctx
	return ctx
}

func salesFixture(t *testing.T, platform *salesPlatformStub) (*sdk.AppCtx, Community, Member, Space) {
	t.Helper()
	ctx := newSalesTestCtx(t, platform)
	community := mustCreateCommunity(t, ctx, "main", "Main")
	member := mustCreateLinkedMember(t, ctx, community.ID, "alice", "auth-alice")
	course := mustCreateSpace(t, ctx, community.ID, "course", "course")
	if _, err := toolCourseOfferUpsert(ctx, map[string]any{
		"space_id": course.ID, "catalog_price_id": int64(71),
	}); err != nil {
		t.Fatalf("course offer: %v", err)
	}
	return ctx, community, member, course
}

func userPurchaseContext(subjectID, email string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{
		SubjectType: "user", SubjectID: subjectID, SubjectEmail: email,
	})
}

func TestCoursePurchaseUsesVerifiedEmailAndIsRetrySafe(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, _, member, course := salesFixture(t, platform)
	call := delegatedTool(t, "course_purchase_start")
	args := map[string]any{
		"space_id": course.ID, "member_id": "spoofed",
		"customer_email": "attacker@example.test",
		"success_url":    "https://community.example.test/?payment=success",
		"cancel_url":     "https://community.example.test/?payment=cancelled",
	}
	out, err := call(userPurchaseContext("auth-alice", "alice@example.test"), ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	first := out.(map[string]any)
	purchase := first["purchase"].(*CoursePurchase)
	if purchase.MemberID != member.ID {
		t.Fatalf("member_id=%q, want verified member %q", purchase.MemberID, member.ID)
	}
	if purchase.CustomerEmail != "alice@example.test" {
		t.Fatalf("customer_email=%q, want verified Auth email", purchase.CustomerEmail)
	}
	if first["checkout_url"] != "https://payments.example.test/session" {
		t.Fatalf("checkout URL missing: %+v", first)
	}
	if _, err := call(userPurchaseContext("auth-alice", "alice@example.test"), ctx, args); err != nil {
		t.Fatalf("retry purchase: %v", err)
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	if platform.invoiceCreates != 1 {
		t.Fatalf("invoice creates=%d, want 1", platform.invoiceCreates)
	}
}

func TestCoursePurchasePaidEventFulfillsIdempotently(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, _, member, course := salesFixture(t, platform)
	out, err := delegatedTool(t, "course_purchase_start")(
		userPurchaseContext("auth-alice", "alice@example.test"),
		ctx,
		map[string]any{"space_id": course.ID, "success_url": "https://community.example.test/success", "cancel_url": "https://community.example.test/cancel"},
	)
	if err != nil {
		t.Fatal(err)
	}
	purchase := out.(map[string]any)["purchase"].(*CoursePurchase)
	platform.mu.Lock()
	platform.invoice.Status = "paid"
	platform.invoice.AmountPaidCents = platform.invoice.TotalCents
	platform.mu.Unlock()

	event := sdk.Event{
		Event: "invoice.paid", SourceApp: "billing", ProjectID: "test-proj",
		Data: map[string]any{"id": int64(701), "status": "paid"},
	}
	app := &App{}
	if err := app.handleCourseBillingEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := app.handleCourseBillingEvent(ctx, event); err != nil {
		t.Fatalf("duplicate event: %v", err)
	}
	enrollment, err := loadCourseEnrollment(ctx.AppDB(), course.ID, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Status != "active" || enrollment.Source != purchaseSource ||
		enrollment.SourceRef == nil || *enrollment.SourceRef != purchase.ID {
		t.Fatalf("unexpected enrollment: %+v", enrollment)
	}
	current, err := loadCoursePurchase(ctx.AppDB(), purchase.ID)
	if err != nil || current.Status != "fulfilled" {
		t.Fatalf("purchase=%+v err=%v", current, err)
	}
	var eventCount int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM course_purchase_events WHERE purchase_id=? AND event_name='invoice.paid'`,
		purchase.ID,
	).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("paid event rows=%d, want 1", eventCount)
	}
}

func TestFullRefundRevokesPurchaseAccessButPreservesProgress(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, _, member, course := salesFixture(t, platform)
	section, err := toolSectionsCreate(ctx, map[string]any{"space_id": course.ID, "title": "Start"})
	if err != nil {
		t.Fatal(err)
	}
	lesson, err := toolLessonsCreate(ctx, map[string]any{"section_id": section.(Section).ID, "title": "One"})
	if err != nil {
		t.Fatal(err)
	}
	purchaseOut, err := delegatedTool(t, "course_purchase_start")(
		userPurchaseContext("auth-alice", "alice@example.test"),
		ctx,
		map[string]any{"space_id": course.ID, "success_url": "https://community.example.test/success", "cancel_url": "https://community.example.test/cancel"},
	)
	if err != nil {
		t.Fatal(err)
	}
	purchase := purchaseOut.(map[string]any)["purchase"].(*CoursePurchase)
	platform.mu.Lock()
	platform.invoice.Status = "paid"
	platform.invoice.AmountPaidCents = platform.invoice.TotalCents
	platform.mu.Unlock()
	app := &App{}
	if err := app.handleCourseBillingEvent(ctx, sdk.Event{
		Event: "invoice.paid", SourceApp: "billing", ProjectID: "test-proj", Data: map[string]any{"id": int64(701)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO lesson_progress (lesson_id, member_id, status, completed_at) VALUES (?, ?, 'complete', CURRENT_TIMESTAMP)`,
		lesson.(Lesson).ID, member.ID,
	); err != nil {
		t.Fatal(err)
	}

	platform.mu.Lock()
	platform.invoice.Status = "open"
	platform.invoice.AmountPaidCents = 0
	platform.mu.Unlock()
	if err := app.handleCourseBillingEvent(ctx, sdk.Event{
		Event: "invoice.refunded", SourceApp: "billing", ProjectID: "test-proj", Data: map[string]any{"id": int64(701)},
	}); err != nil {
		t.Fatal(err)
	}
	current, _ := loadCoursePurchase(ctx.AppDB(), purchase.ID)
	enrollment, _ := loadCourseEnrollment(ctx.AppDB(), course.ID, member.ID)
	if current.Status != "refunded" || enrollment.Status != "cancelled" || enrollment.AccessRevokedAt == nil {
		t.Fatalf("purchase=%+v enrollment=%+v", current, enrollment)
	}
	var progress int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM lesson_progress WHERE lesson_id=? AND member_id=? AND status='complete'`,
		lesson.(Lesson).ID, member.ID,
	).Scan(&progress); err != nil {
		t.Fatal(err)
	}
	if progress != 1 {
		t.Fatal("full refund deleted course progress")
	}
}

func TestPartialRefundRetainsCourseAccess(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, _, member, course := salesFixture(t, platform)
	purchaseOut, err := delegatedTool(t, "course_purchase_start")(
		userPurchaseContext("auth-alice", "alice@example.test"),
		ctx,
		map[string]any{"space_id": course.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	purchase := purchaseOut.(map[string]any)["purchase"].(*CoursePurchase)
	platform.mu.Lock()
	platform.invoice.Status = "paid"
	platform.invoice.AmountPaidCents = platform.invoice.TotalCents
	platform.mu.Unlock()
	app := &App{}
	if err := app.handleCourseBillingEvent(ctx, sdk.Event{
		Event: "invoice.paid", SourceApp: "billing", ProjectID: "test-proj", Data: map[string]any{"id": int64(701)},
	}); err != nil {
		t.Fatal(err)
	}

	platform.mu.Lock()
	platform.invoice.Status = "open"
	platform.invoice.AmountPaidCents = 4900
	platform.invoice.Payments = []billingPayment{{AmountCents: -5000}}
	platform.mu.Unlock()
	if err := app.handleCourseBillingEvent(ctx, sdk.Event{
		Event: "invoice.refunded", SourceApp: "billing", ProjectID: "test-proj", Data: map[string]any{"id": int64(701)},
	}); err != nil {
		t.Fatal(err)
	}
	current, _ := loadCoursePurchase(ctx.AppDB(), purchase.ID)
	enrollment, _ := loadCourseEnrollment(ctx.AppDB(), course.ID, member.ID)
	if current.Status != "partially_refunded" || current.RefundedCents != 5000 {
		t.Fatalf("unexpected purchase after partial refund: %+v", current)
	}
	if enrollment.Status != "active" || enrollment.AccessRevokedAt != nil {
		t.Fatalf("partial refund revoked course access: %+v", enrollment)
	}
}

func TestFullRefundPreservesManualEnrollmentOverride(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, _, member, course := salesFixture(t, platform)
	_, err := delegatedTool(t, "course_purchase_start")(
		userPurchaseContext("auth-alice", "alice@example.test"),
		ctx,
		map[string]any{"space_id": course.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	platform.mu.Lock()
	platform.invoice.Status = "paid"
	platform.invoice.AmountPaidCents = platform.invoice.TotalCents
	platform.mu.Unlock()
	app := &App{}
	if err := app.handleCourseBillingEvent(ctx, sdk.Event{
		Event: "invoice.paid", SourceApp: "billing", ProjectID: "test-proj", Data: map[string]any{"id": int64(701)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolCourseEnrollmentUpdate(ctx, map[string]any{
		"space_id": course.ID, "member_id": member.ID, "status": "active",
	}); err != nil {
		t.Fatal(err)
	}

	platform.mu.Lock()
	platform.invoice.Status = "open"
	platform.invoice.AmountPaidCents = 0
	platform.mu.Unlock()
	if err := app.handleCourseBillingEvent(ctx, sdk.Event{
		Event: "invoice.refunded", SourceApp: "billing", ProjectID: "test-proj", Data: map[string]any{"id": int64(701)},
	}); err != nil {
		t.Fatal(err)
	}
	enrollment, err := loadCourseEnrollment(ctx.AppDB(), course.ID, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Status != "active" || enrollment.Source != "manual" || enrollment.AccessRevokedAt != nil {
		t.Fatalf("full refund overrode operator-managed access: %+v", enrollment)
	}
}

func TestCoursePurchaseRequiresServerVerifiedEmail(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, _, _, course := salesFixture(t, platform)
	_, err := delegatedTool(t, "course_purchase_start")(
		userPurchaseContext("auth-alice", ""),
		ctx,
		map[string]any{"space_id": course.ID, "customer_email": "spoofed@example.test"},
	)
	if err == nil || !strings.Contains(err.Error(), "verified Auth email") {
		t.Fatalf("error=%v, want verified email failure", err)
	}
}

func TestCoursePurchaseStartRecognizesExistingEnrollment(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, _, member, course := salesFixture(t, platform)
	if _, err := toolCourseEnroll(ctx, map[string]any{
		"space_id": course.ID, "member_id": member.ID,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := delegatedTool(t, "course_purchase_start")(
		userPurchaseContext("auth-alice", "alice@example.test"),
		ctx,
		map[string]any{"space_id": course.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["enrolled"] != true || result["purchase"] != nil {
		t.Fatalf("unexpected existing enrollment response: %+v", result)
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	if platform.invoiceCreates != 0 || platform.sessionCreates != 0 {
		t.Fatalf("existing enrollment started Billing: invoices=%d sessions=%d", platform.invoiceCreates, platform.sessionCreates)
	}
}

func TestCourseOfferRejectsRecurringPrice(t *testing.T) {
	platform := newSalesPlatformStub()
	platform.price.Interval = "month"
	ctx := newSalesTestCtx(t, platform)
	community := mustCreateCommunity(t, ctx, "main", "Main")
	course := mustCreateSpace(t, ctx, community.ID, "course", "course")
	if _, err := toolCourseOfferUpsert(ctx, map[string]any{
		"space_id": course.ID, "catalog_price_id": int64(71),
	}); err == nil {
		t.Fatal("recurring Catalog price should be rejected in one-time course sales")
	}
}

func TestCourseOfferArchiveStopsNewSales(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, _, _, course := salesFixture(t, platform)
	if _, err := toolCourseOfferArchive(ctx, map[string]any{"space_id": course.ID}); err != nil {
		t.Fatal(err)
	}
	offer, err := loadCourseOffer(ctx.AppDB(), course.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if offer != nil {
		t.Fatalf("archived offer remains active: %+v", offer)
	}
	_, err = delegatedTool(t, "course_purchase_start")(
		userPurchaseContext("auth-alice", "alice@example.test"),
		ctx,
		map[string]any{"space_id": course.ID},
	)
	if err == nil || !strings.Contains(err.Error(), "not currently for sale") {
		t.Fatalf("purchase error=%v, want archived sale rejection", err)
	}
}

func TestMetadataStringMissingValueIsEmpty(t *testing.T) {
	if got := metadataString(json.RawMessage(`{}`), "missing"); got != "" {
		t.Fatalf("missing metadata=%q, want empty", got)
	}
	if got := metadataString(json.RawMessage(`{"value":null}`), "value"); got != "" {
		t.Fatalf("null metadata=%q, want empty", got)
	}
}
