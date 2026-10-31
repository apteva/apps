package main

import (
	"context"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"strings"
	"testing"
)

func TestAnalyticsConsentAndIdentityBoundary(t *testing.T) {
	in := map[string]any{"consent": "granted", "visitor_id": "visitor-123", "user_id": "victim", "aliases": []string{"victim"}, "email": "private@example.com", "utm_campaign": "qa", "touch_landing": "https://shop.test/p?email=secret#fragment", "test_submission": true}
	got, e := cleanCartAttribution(in)
	if e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"user_id", "aliases", "email"} {
		if _, ok := got[key]; ok {
			t.Fatal("untrusted field retained", key)
		}
	}
	if got["touch_landing"] != "https://shop.test/p" {
		t.Fatal(got)
	}
	revoked, e := cleanCartAttribution(map[string]any{"consent": "denied", "visitor_id": "visitor-123", "utm_campaign": "old"})
	if e != nil || len(revoked) != 1 {
		t.Fatal(revoked, e)
	}
}
func seedAnalyticsSale(t *testing.T, consent bool) (*sdk.AppCtx, *commercePlatformStub, *Store, *Sale) {
	t.Helper()
	platform := newCommercePlatformStub()
	platform.responses["analytics:analytics_identify"] = map[string]any{"ok": true}
	platform.responses["analytics:analytics_track"] = map[string]any{"id": 1}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("analytics-qa"), tk.WithPlatform(platform))
	store, _, _, cart := seedCommerceCart(t, ctx.AppDB(), "analytics-qa", "pixel-qa", 0)
	meta := map[string]any{"analytics": map[string]any{"enabled": true, "enabled_at": "2026-01-01T00:00:00Z"}}
	_, e := ctx.AppDB().Exec(`UPDATE commerce_stores SET metadata_json=? WHERE id=?`, jsonText(meta, "{}"), store.ID)
	if e != nil {
		t.Fatal(e)
	}
	store.Metadata = meta
	attr := map[string]any{"consent": "denied"}
	if consent {
		attr = map[string]any{"consent": "granted", "visitor_id": "qa-visitor-123", "utm_campaign": "qa-campaign", "test_submission": true, "qa_run": "pr-qa-new-tag"}
	}
	if _, e = (&App{}).toolCartAttributionSet(ctx, map[string]any{"cart_id": cart.ID, "attribution": attr}); e != nil {
		t.Fatal(e)
	}
	r, e := ctx.AppDB().Exec(`INSERT INTO commerce_sales(project_id,store_id,cart_id,invoice_id,payment_status,status,total_cents,currency,paid_at) VALUES(?,?,?,501,'paid','paid',4475,'USD','2026-10-31 12:00:00')`, "analytics-qa", store.ID, cart.ID)
	if e != nil {
		t.Fatal(e)
	}
	id, _ := r.LastInsertId()
	sale, e := dbSaleGet(ctx.AppDB(), "analytics-qa", id)
	if e != nil {
		t.Fatal(e)
	}
	return ctx, platform, store, sale
}
func TestAnalyticsPaidOutboxRetryIsStableAndIndependent(t *testing.T) {
	ctx, p, _, sale := seedAnalyticsSale(t, true)
	p.errors["analytics:analytics_track"] = errors.New("offline")
	if e := (&App{}).reconcileSaleAnalytics(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	first := findPlatformCall(p.calls, "analytics", "analytics_track").Input
	if first["delivery_id"] == nil || strArg(first, "user_id") == "" {
		t.Fatal(first)
	}
	var delivered *string
	var attempts int
	if e := ctx.AppDB().QueryRow(`SELECT attempts,delivered_at FROM commerce_analytics_deliveries WHERE sale_id=?`, sale.ID).Scan(&attempts, &delivered); e != nil || attempts != 1 || delivered != nil {
		t.Fatal(attempts, delivered, e)
	}
	// The payment remains real while optional Analytics is offline.
	saved, _ := dbSaleGet(ctx.AppDB(), ctx.CurrentProject(), sale.ID)
	if saved.PaymentStatus != "paid" {
		t.Fatal(saved)
	}
	delete(p.errors, "analytics:analytics_track")
	ctx.AppDB().Exec(`UPDATE commerce_analytics_deliveries SET next_attempt_at=datetime('now','-1 second')`)
	if e := (&App{}).reconcileSaleAnalytics(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	var last map[string]any
	for _, c := range p.calls {
		if c.Tool == "analytics_track" {
			last = c.Input
		}
	}
	if jsonText(first, "") != jsonText(last, "") {
		t.Fatal("retry changed immutable request")
	}
	count := countPlatformCalls(p.calls, "analytics", "analytics_track")
	(&App{}).reconcileSaleAnalytics(context.Background(), ctx)
	if countPlatformCalls(p.calls, "analytics", "analytics_track") != count {
		t.Fatal("delivered purchase resent")
	}
}
func TestAnalyticsUnpaidAndDeniedConsent(t *testing.T) {
	ctx, p, store, sale := seedAnalyticsSale(t, false)
	sale.PaymentStatus = "unpaid"
	payload, e := paidAnalyticsPayload(ctx, store, sale)
	if e != nil || payload != nil {
		t.Fatal("unpaid became purchase", payload, e)
	}
	if e = (&App{}).reconcileSaleAnalytics(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	if hasPlatformCall(p.calls, "analytics", "analytics_identify") {
		t.Fatal("identified denied-consent visitor")
	}
	tracked := findPlatformCall(p.calls, "analytics", "analytics_track").Input
	if tracked["user_id"] != nil || strings.Contains(jsonText(tracked, ""), "qa-visitor") {
		t.Fatal("optional identity leaked", tracked)
	}
}
