package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDiscountedCoursePaymentEnrollmentAndAttribution(t *testing.T) {
	platform := newSalesPlatformStub()
	platform.price.UnitAmountCents = 1900
	platform.price.Currency = "USD"
	ctx, community, member, course := salesFixture(t, platform)
	target, err := resolveStorefrontOffer(ctx.AppDB(), community.ID, 71)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareCheckoutWithCheckoutApp(ctx, community.ID, target, "", "", "", nil, storefrontAcquisitionInput{PromotionCode: "ESP32WELCOME", Attribution: map[string]string{"utm_campaign": "mkc-esp32-guide-eur5-paused-20261027", "campaign_id": "120249388397420178", "captured_at": time.Now().UTC().Format(time.RFC3339), "email": "never-export@example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.AmountCents != 900 || platform.checkoutPayments != 0 {
		t.Fatalf("anonymous checkout = %+v", prepared)
	}
	out, err := delegatedTool(t, "storefront_checkout_claim")(userPurchaseContext("auth-alice", "Alice@example.test"), ctx, map[string]any{"community_id": community.ID, "catalog_price_id": int64(71), "member_id": member.ID, "recovery_token": prepared.RecoveryToken})
	if err != nil {
		t.Fatal(err)
	}
	purchase := out.(map[string]any)["purchase"].(*CoursePurchase)
	if purchase.UnitAmountCents != 900 || purchase.Status != "awaiting_payment" || platform.invoice.TotalCents != 900 {
		t.Fatalf("purchase=%+v invoice=%+v", purchase, platform.invoice)
	}
	if active, _ := hasActiveCourseEnrollment(ctx.AppDB(), course.ID, member.ID); active {
		t.Fatal("unpaid invoice granted access")
	}
	platform.invoice.Status = "paid"
	platform.invoice.AmountPaidCents = 900
	for i := 0; i < 2; i++ {
		platform.discountAccountingFailure = i == 0
		if _, err = reconcileCoursePurchase(ctx, purchase, "invoice.paid", "billing", nil); err != nil {
			t.Fatal(err)
		}
	}
	if active, _ := hasActiveCourseEnrollment(ctx.AppDB(), course.ID, member.ID); !active {
		t.Fatal("confirmed payment did not grant access")
	}
	today := time.Now().UTC().Format("2006-01-02")
	report, err := toolStorefrontAcquisitionReport(ctx, map[string]any{"community_id": community.ID, "since": today, "until": today})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	var decoded struct {
		Cohorts []struct {
			Count       int               `json:"unique_new_paying_customers"`
			Attribution map[string]string `json:"attribution"`
		} `json:"cohorts"`
	}
	json.Unmarshal(raw, &decoded)
	if len(decoded.Cohorts) != 1 || decoded.Cohorts[0].Count != 1 || decoded.Cohorts[0].Attribution["campaign_id"] != "120249388397420178" || decoded.Cohorts[0].Attribution["email"] != "" {
		t.Fatalf("deduped report=%s", raw)
	}
	// A duplicate checkout by an already enrolled member must not create another payment.
	_, err = delegatedTool(t, "storefront_checkout_claim")(userPurchaseContext("auth-alice", "alice@example.test"), ctx, map[string]any{"community_id": community.ID, "catalog_price_id": int64(71), "member_id": member.ID, "recovery_token": prepared.RecoveryToken})
	if err != nil {
		t.Fatal(err)
	}
	if platform.checkoutPayments != 1 {
		t.Fatal("duplicate claim created another payment")
	}
	// A full refund revokes access and is reported separately from acquired customers.
	platform.invoice.AmountPaidCents = 0
	purchase, _ = loadCoursePurchase(ctx.AppDB(), purchase.ID)
	if _, err = reconcileCoursePurchase(ctx, purchase, "invoice.refunded", "billing", nil); err != nil {
		t.Fatal(err)
	}
	if active, _ := hasActiveCourseEnrollment(ctx.AppDB(), course.ID, member.ID); active {
		t.Fatal("full refund retained course access")
	}
	report, err = toolStorefrontAcquisitionReport(ctx, map[string]any{"community_id": community.ID, "since": today, "until": today})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(report)
	json.Unmarshal(raw, &decoded)
	if decoded.Cohorts[0].Count != 1 {
		t.Fatal("refund erased historical acquisition")
	}
}
func TestStorefrontRejectsTamperedNetAmount(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, community, member, _ := salesFixture(t, platform)
	target, _ := resolveStorefrontOffer(ctx.AppDB(), community.ID, 71)
	prepared, err := prepareCheckoutWithCheckoutApp(ctx, community.ID, target, "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	platform.checkoutSession.TotalCents = 1
	_, err = delegatedTool(t, "storefront_checkout_claim")(userPurchaseContext("auth-alice", "alice@example.test"), ctx, map[string]any{"community_id": community.ID, "catalog_price_id": int64(71), "member_id": member.ID, "recovery_token": prepared.RecoveryToken})
	if err == nil || platform.checkoutPayments != 0 {
		t.Fatal("tampered amount reached payment")
	}
}
func TestStorefrontDiscardsStaleAttribution(t *testing.T) {
	raw := sanitizeStorefrontAttribution(map[string]string{"utm_campaign": "stale", "captured_at": time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)})
	if string(raw) != "{}" {
		t.Fatalf("stale attribution retained: %s", raw)
	}
}

func TestAcquisitionReportExcludesRepeatCustomersAcrossLifetime(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, community, member, course := salesFixture(t, platform)
	offer, _ := loadCourseOffer(ctx.AppDB(), course.ID, false)
	first, err := createCoursePurchase(ctx.AppDB(), course, member.ID, "ALICE@example.test", "Alice", "auth-alice", offer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ctx.AppDB().Exec(`UPDATE course_purchases SET status='refunded',paid_at='2026-10-20T12:00:00Z',refunded_cents=unit_amount_cents WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := createCoursePurchase(ctx.AppDB(), course, member.ID, "alice@example.test", "Alice", "auth-alice", offer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ctx.AppDB().Exec(`UPDATE course_purchases SET status='fulfilled',paid_at='2026-10-27T12:00:00Z' WHERE id=?`, second.ID); err != nil {
		t.Fatal(err)
	}
	report, err := toolStorefrontAcquisitionReport(ctx, map[string]any{"community_id": community.ID, "since": "2026-10-27", "until": "2026-10-27", "timezone": "Europe/Madrid"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	var result struct {
		Cohorts []any `json:"cohorts"`
	}
	json.Unmarshal(raw, &result)
	if len(result.Cohorts) != 0 {
		t.Fatalf("repeat customer was counted as new: %s", raw)
	}
}
func TestAcquisitionReportUsesAdAccountTimezone(t *testing.T) {
	platform := newSalesPlatformStub()
	ctx, community, member, course := salesFixture(t, platform)
	offer, _ := loadCourseOffer(ctx.AppDB(), course.ID, false)
	purchase, err := createCoursePurchase(ctx.AppDB(), course, member.ID, "alice@example.test", "Alice", "auth-alice", offer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ctx.AppDB().Exec(`UPDATE course_purchases SET status='fulfilled',paid_at='2026-10-26T23:30:00Z' WHERE id=?`, purchase.ID); err != nil {
		t.Fatal(err)
	}
	report, err := toolStorefrontAcquisitionReport(ctx, map[string]any{"community_id": community.ID, "since": "2026-10-27", "until": "2026-10-27", "timezone": "Europe/Madrid"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	var result struct {
		Cohorts []struct {
			Count int `json:"unique_new_paying_customers"`
		} `json:"cohorts"`
	}
	json.Unmarshal(raw, &result)
	if len(result.Cohorts) != 1 || result.Cohorts[0].Count != 1 {
		t.Fatalf("Madrid reporting day mismatch: %s", raw)
	}
}
