package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicStorefrontProjectsOnlyBoundCatalogProducts(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, community, member, course := salesFixture(t, platform)
	platform.product.Metadata = json.RawMessage(`{"storefront":{"headline":"Build with confidence","eyebrow":"Masterclass","included":[{"title":"Practical projects","description":"Build as you learn."}],"testimonial":{"quote":"Exactly what I needed.","name":"Alex"}}}`)
	if _, err := toolCoursesUpdateDetails(ctx, map[string]any{
		"space_id": course.ID, "summary": "A public summary", "description": "Private lesson bodies stay hidden",
		"outcomes": []any{"Launch confidently"},
	}); err != nil {
		t.Fatal(err)
	}
	sectionOut, err := toolSectionsCreate(ctx, map[string]any{"space_id": course.ID, "title": "Foundations"})
	if err != nil {
		t.Fatal(err)
	}
	section := sectionOut.(Section)
	lessonOut, err := toolLessonsCreate(ctx, map[string]any{"section_id": section.ID, "title": "Welcome", "body": "This must never be public"})
	if err != nil {
		t.Fatal(err)
	}
	lesson := lessonOut.(Lesson)
	if _, err := toolLessonsPublish(ctx, map[string]any{"id": lesson.ID, "published": true}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/portal/products/community-course?community=main", nil)
	rec := httptest.NewRecorder()
	(&App{}).httpPortalProduct(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"slug":"community-course"`, `"catalog_price_id":71`, `"slug":"course"`, `"name":"Course"`, `"headline":"Build with confidence"`, `"title":"Practical projects"`, `"quote":"Exactly what I needed."`} {
		if !strings.Contains(body, want) {
			t.Fatalf("public product missing %s: %s", want, body)
		}
	}
	for _, forbidden := range []string{"This must never be public", "A public summary", "Launch confidently", "Welcome", member.AuthUserIDOrEmpty(), "auth-alice"} {
		if forbidden != "" && strings.Contains(body, forbidden) {
			t.Fatalf("public product leaked %q: %s", forbidden, body)
		}
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "max-age=60") {
		t.Fatalf("unexpected cache policy %q", got)
	}
	_ = community
}

func TestPublicStorefrontGroupsRecurringPricesByCatalogProduct(t *testing.T) {
	ctx, _, community, _, _, _, _ := membershipFixture(t)
	if _, err := toolMembershipPlansUpsert(ctx, map[string]any{
		"community_id": community.ID, "name": "Annual", "catalog_price_id": int64(92), "scope_type": "all_courses",
	}); err != nil {
		t.Fatal(err)
	}
	products, err := buildPublicProducts(ctx, community.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || products[0].Slug != "all-courses" || len(products[0].Offers) != 2 {
		t.Fatalf("products=%+v, want one product with monthly and annual offers", products)
	}
	intervals := map[string]bool{}
	for _, offer := range products[0].Offers {
		intervals[offer.Interval] = true
	}
	if !intervals["month"] || !intervals["year"] {
		t.Fatalf("offers=%+v, want monthly and annual", products[0].Offers)
	}
}

func TestStorefrontCheckoutMapsPriceServerSide(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, community, member, _ := salesFixture(t, platform)
	out, err := delegatedTool(t, "storefront_checkout_start")(
		userPurchaseContext("auth-alice", "alice@example.test"), ctx,
		map[string]any{
			"community_id": community.ID, "catalog_price_id": int64(71), "member_id": "spoofed",
			"success_url": "https://community.test/success", "cancel_url": "https://community.test/cancel",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	purchase := result["purchase"].(*CoursePurchase)
	if purchase.MemberID != member.ID || result["offer_kind"] != "one_time" || result["checkout_url"] == "" {
		t.Fatalf("unexpected checkout result: %+v", result)
	}
}

func TestPreparedCheckoutMustMatchCommunityOffer(t *testing.T) {
	target := storefrontOfferTarget{Kind: "one_time", Product: 61, Price: 71, Amount: 9900, Currency: "EUR"}
	metadata, _ := json.Marshal(map[string]any{
		"community_id": "comm_1", "catalog_price_id": 71, "offer_kind": "one_time",
	})
	session := guestCheckoutSession{ID: 10, Status: "started", Metadata: metadata}
	cart := guestCheckoutCart{ID: 11, Items: []guestCheckoutItem{{
		PriceID: 71, ProductID: 61, UnitAmountCents: 9900, Currency: "EUR", Quantity: 1,
	}}}
	if err := validatePreparedCheckout("comm_1", target, session, cart); err != nil {
		t.Fatalf("valid prepared checkout rejected: %v", err)
	}
	cart.Items[0].UnitAmountCents++
	if err := validatePreparedCheckout("comm_1", target, session, cart); err == nil {
		t.Fatal("checkout must not be claimable after its canonical total changes")
	}
}

func TestStorefrontReturnURLStaysOnPortalOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://community.test/portal/checkout/prepare", nil)
	if got, err := validateStorefrontReturnURL(req, "http://community.test/course?payment=success"); err != nil || got == "" {
		t.Fatalf("same-origin URL rejected: got=%q err=%v", got, err)
	}
	if _, err := validateStorefrontReturnURL(req, "https://attacker.test/steal"); err == nil {
		t.Fatal("cross-origin checkout return URL must be rejected")
	}
}

func TestDeferredStorefrontPreparationWaitsForVerifiedBuyer(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, community, member, _ := salesFixture(t, platform)
	target, err := resolveStorefrontOffer(ctx.AppDB(), community.ID, 71)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareCheckoutWithCheckoutApp(ctx, community.ID, target, "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Presentation != "deferred" || prepared.AmountCents != 9900 || prepared.Currency != "eur" || prepared.PublishableKey != "pk_test_public" || prepared.ClientSecret != "" || prepared.InvoiceID != 0 {
		t.Fatalf("unexpected anonymous preparation: %+v", prepared)
	}
	if platform.invoice.ID != 0 || platform.checkoutPayments != 0 || platform.checkoutSession.Email != "" {
		t.Fatalf("anonymous preparation created buyer billing state: invoice=%+v session=%+v", platform.invoice, platform.checkoutSession)
	}
	out, err := delegatedTool(t, "storefront_checkout_claim")(
		userPurchaseContext("auth-alice", "alice@example.test"), ctx,
		map[string]any{"community_id": community.ID, "catalog_price_id": int64(71), "member_id": member.ID, "recovery_token": prepared.RecoveryToken},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["client_secret"] != "pi_test_course_secret" || result["payment_intent_id"] != "pi_test_course" || platform.checkoutPayments != 1 || platform.checkoutSession.Email != "alice@example.test" {
		t.Fatalf("verified claim did not create the deferred payment correctly: result=%+v session=%+v", result, platform.checkoutSession)
	}
}

func TestStorefrontCheckoutSupportsInlineStripeElementsForCourse(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, community, _, _ := salesFixture(t, platform)
	out, err := delegatedTool(t, "storefront_checkout_start")(
		userPurchaseContext("auth-alice", "alice@example.test"), ctx,
		map[string]any{
			"community_id": community.ID, "catalog_price_id": int64(71), "member_id": "spoofed",
			"presentation": "elements", "return_url": "https://community.test/return",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["presentation"] != "elements" || result["client_secret"] != "cs_test_secret" || result["publishable_key"] != "pk_test_public" || result["checkout_url"] != "" {
		t.Fatalf("unexpected Elements checkout result: %+v", result)
	}
}

func TestStorefrontCheckoutSupportsInlineStripeElementsForMembership(t *testing.T) {
	ctx, _, community, _, _, _, _ := membershipFixture(t)
	out, err := delegatedTool(t, "storefront_checkout_start")(
		userPurchaseContext("auth-alice", "alice@example.test"), ctx,
		map[string]any{
			"community_id": community.ID, "catalog_price_id": int64(91), "member_id": "spoofed",
			"presentation": "elements", "return_url": "https://community.test/return",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["offer_kind"] != "recurring" || result["presentation"] != "elements" || result["client_secret"] != "cs_membership_secret" || result["publishable_key"] != "pk_test_public" {
		t.Fatalf("unexpected membership Elements result: %+v", result)
	}
}

func TestStorefrontRouteRedirectsGenericSlugs(t *testing.T) {
	_, _ = newTestCtx(t)
	req := httptest.NewRequest(http.MethodGet, "/store/makecademy/checkout/all-courses?offer=92", nil)
	req.Header.Set("X-Apteva-App-Install-ID", "233")
	rec := httptest.NewRecorder()
	(&App{}).httpStorefrontRoute(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "/api/apps/community/_install/233/ui/portal/dist/index.html?") {
		t.Fatalf("redirect lost app gateway prefix: %s", location)
	}
	for _, want := range []string{"community=makecademy", "product=all-courses", "intent=buy", "offer=92", "project_id=test-proj"} {
		if !strings.Contains(location, want) {
			t.Fatalf("redirect missing %s: %s", want, location)
		}
	}
}

func (m Member) AuthUserIDOrEmpty() string {
	if m.AuthUserID == nil {
		return ""
	}
	return *m.AuthUserID
}

func TestPublicProductJSONNeverContainsCheckoutTargets(t *testing.T) {
	encoded, err := json.Marshal(PublicOffer{CatalogPriceID: 1, Kind: "one_time", CourseSlugs: []string{"course"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"space_id", "plan_id", "member_id"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("public offer leaked internal checkout target %q: %s", forbidden, encoded)
		}
	}
}
