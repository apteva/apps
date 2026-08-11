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
	for _, want := range []string{`"slug":"community-course"`, `"catalog_price_id":71`, `"slug":"course"`, `"Welcome"`, `"Launch confidently"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("public product missing %s: %s", want, body)
		}
	}
	for _, forbidden := range []string{"This must never be public", member.AuthUserIDOrEmpty(), "auth-alice"} {
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

func TestStorefrontRouteRedirectsGenericSlugs(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/store/makecademy/checkout/all-courses?offer=92&project_id=p1", nil)
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
	for _, want := range []string{"community=makecademy", "product=all-courses", "intent=buy", "offer=92", "project_id=p1"} {
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
