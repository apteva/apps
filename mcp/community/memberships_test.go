package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type membershipPlatformStub struct {
	tk.BasePlatformClient
	mu                  sync.Mutex
	subscription        subscriptionSnapshot
	cycle               subscriptionCycle
	invoice             billingInvoice
	subscriptionCreates int
	invoiceCreates      int
	linkCreates         int
}

func (s *membershipPlatformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var value any
	switch app + ":" + tool {
	case "catalog:catalog_prices_get":
		value = map[string]any{"price": catalogPrice{
			ID: 91, ProductID: 81, Nickname: "Monthly", UnitAmountCents: 9900,
			Currency: "EUR", Interval: "month", IntervalCount: 1,
			BillingScheme: "flat", Active: true,
		}}
	case "catalog:catalog_products_get":
		value = map[string]any{"product": catalogProduct{ID: 81, Name: "All courses", Type: "service"}}
	case "billing:customers_upsert_by_email":
		value = map[string]any{"customer": map[string]any{"id": int64(601)}, "was_created": true}
	case "subscriptions:subscriptions_create":
		s.subscriptionCreates++
		s.subscription = subscriptionSnapshot{
			ID: 301, Status: "past_due",
			CurrentPeriodStart: input["current_period_start"].(string),
			CurrentPeriodEnd:   input["current_period_end"].(string),
			NextRenewalAt:      input["next_renewal_at"].(string),
		}
		value = map[string]any{"subscription": s.subscription}
	case "subscriptions:subscription_cycles_create":
		s.cycle = subscriptionCycle{ID: 401, PeriodStart: input["period_start"].(string), PeriodEnd: input["period_end"].(string), PaymentStatus: "pending"}
		value = map[string]any{"cycle": s.cycle, "subscription": s.subscription}
	case "subscriptions:subscriptions_invoice_prepare":
		value = map[string]any{"subscription": s.subscription, "period_start": s.cycle.PeriodStart, "period_end": s.cycle.PeriodEnd,
			"line_items": []any{map[string]any{"price_id": int64(91), "quantity": 1}}}
	case "billing:invoices_create_from_prepared_lines":
		s.invoiceCreates++
		meta, _ := json.Marshal(input["metadata"])
		s.invoice = billingInvoice{ID: int64(900 + s.invoiceCreates), CustomerID: 601, Status: "open", TotalCents: 9900, Currency: "EUR", Metadata: meta}
		value = map[string]any{"invoice": s.invoice}
	case "billing:invoices_search":
		invoices := []billingInvoice{}
		if s.invoice.ID != 0 {
			invoices = append(invoices, s.invoice)
		}
		value = map[string]any{"invoices": invoices, "count": len(invoices)}
	case "subscriptions:subscription_cycles_update":
		if status, ok := input["payment_status"].(string); ok {
			s.cycle.PaymentStatus = status
		}
		value = map[string]any{"cycle": s.cycle}
	case "billing:invoices_send_payment_link":
		s.linkCreates++
		value = map[string]any{"url": "https://payments.example.test/membership", "stripe_session_id": "cs_membership"}
	case "billing:invoices_collect":
		return errors.New("customer has no reusable payment method")
	case "subscriptions:subscriptions_update_status":
		s.subscription.Status, _ = input["status"].(string)
		value = map[string]any{"subscription": s.subscription}
	case "subscriptions:subscriptions_cancel":
		if atEnd, _ := input["at_period_end"].(bool); atEnd {
			s.subscription.CancelAt = s.subscription.CurrentPeriodEnd
		} else {
			s.subscription.Status = "cancelled"
		}
		value = map[string]any{"subscription": s.subscription}
	case "subscriptions:subscriptions_resume":
		s.subscription.CancelAt = ""
		value = map[string]any{"subscription": s.subscription, "resumed": true}
	case "subscriptions:subscriptions_get":
		value = map[string]any{"subscription": s.subscription}
	default:
		return errors.New("unexpected cross-app call: " + app + ":" + tool)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func membershipFixture(t *testing.T) (*sdk.AppCtx, *membershipPlatformStub, Community, Member, Space, Space, *MembershipPlan) {
	t.Helper()
	platform := &membershipPlatformStub{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform))
	globalCtx = ctx
	community := mustCreateCommunity(t, ctx, "main", "Main")
	member := mustCreateLinkedMember(t, ctx, community.ID, "alice", "auth-alice")
	first := mustCreateSpace(t, ctx, community.ID, "first", "course")
	second := mustCreateSpace(t, ctx, community.ID, "second", "course")
	out, err := toolMembershipPlansUpsert(ctx, map[string]any{
		"community_id": community.ID, "name": "All Access", "catalog_price_id": int64(91),
		"scope_type": "all_courses", "grace_days": int64(7),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, platform, community, member, first, second, out.(map[string]any)["plan"].(*MembershipPlan)
}

func startMembership(t *testing.T, ctx *sdk.AppCtx, member Member, plan *MembershipPlan) *MemberSubscription {
	t.Helper()
	tool := delegatedTool(t, "membership_checkout_start")
	out, err := tool(
		sdk.WithCaller(context.Background(), &sdk.Caller{SubjectType: "user", SubjectID: "auth-alice", SubjectEmail: "alice@example.test"}),
		ctx,
		map[string]any{"plan_id": plan.ID, "member_id": "spoofed", "success_url": "https://community.test/success", "cancel_url": "https://community.test/cancel"},
	)
	if err != nil {
		t.Fatal(err)
	}
	ms := out.(map[string]any)["subscription"].(*MemberSubscription)
	if ms.MemberID != member.ID {
		t.Fatalf("membership member=%q, want verified member %q", ms.MemberID, member.ID)
	}
	return ms
}

func TestMembershipCheckoutIsIdempotentAndUnpaidHasNoAccess(t *testing.T) {
	ctx, platform, _, member, course, _, plan := membershipFixture(t)
	first := startMembership(t, ctx, member, plan)
	if membershipResponse(first)["access_active"] != false {
		t.Fatal("unpaid first checkout must report access_active=false")
	}
	second := startMembership(t, ctx, member, plan)
	if first.ID != second.ID {
		t.Fatalf("retry created another membership: %s != %s", first.ID, second.ID)
	}
	platform.mu.Lock()
	if platform.subscriptionCreates != 1 || platform.invoiceCreates != 1 || platform.linkCreates != 1 {
		t.Fatalf("external creates: subscriptions=%d invoices=%d links=%d", platform.subscriptionCreates, platform.invoiceCreates, platform.linkCreates)
	}
	platform.mu.Unlock()
	allowed, _, _, err := resolveCourseAccess(ctx.AppDB(), course.ID, member.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("an unpaid first checkout must not grant course access")
	}
}

func TestMembershipSubscriptionsListReleasesSingleDatabaseConnection(t *testing.T) {
	ctx, _, community, member, _, _, plan := membershipFixture(t)
	startMembership(t, ctx, member, plan)
	ctx.AppDB().SetMaxOpenConns(1)

	result := make(chan struct {
		out any
		err error
	}, 1)
	go func() {
		out, err := toolMembershipSubscriptionsList(ctx, map[string]any{
			"community_id": community.ID,
			"limit":        int64(100),
		})
		result <- struct {
			out any
			err error
		}{out: out, err: err}
	}()

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		subscriptions := got.out.(map[string]any)["subscriptions"].([]*MemberSubscription)
		if len(subscriptions) != 1 || subscriptions[0].Plan == nil {
			t.Fatalf("subscriptions=%#v, want one row with its plan", subscriptions)
		}
	case <-time.After(time.Second):
		t.Fatal("membership subscription list deadlocked with the SDK single-connection database pool")
	}
}

func TestPaidMembershipGrantsAllCoursesAndCancellationDoesNotErasePurchase(t *testing.T) {
	ctx, _, _, member, firstCourse, secondCourse, plan := membershipFixture(t)
	ms := startMembership(t, ctx, member, plan)
	event := sdk.Event{Event: "invoice.paid", SourceApp: "billing", ProjectID: "test-proj", Data: map[string]any{"id": int64(901)}}
	if err := (&App{}).handleMembershipBillingEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	for _, course := range []Space{firstCourse, secondCourse} {
		allowed, source, ref, err := resolveCourseAccess(ctx.AppDB(), course.ID, member.ID, true)
		if err != nil || !allowed || source != membershipEnrollmentSource || ref != ms.ID {
			t.Fatalf("access course=%s allowed=%v source=%q ref=%q err=%v", course.ID, allowed, source, ref, err)
		}
	}
	// A permanent purchase replaces only the materialized membership source.
	if _, err := ctx.AppDB().Exec(`UPDATE course_enrollments SET source=?,source_ref='purchase-1' WHERE space_id=? AND member_id=?`,
		purchaseSource, firstCourse.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := toolMembershipCancel(ctx, map[string]any{"id": ms.ID, "member_id": member.ID, "at_period_end": false}); err != nil {
		t.Fatal(err)
	}
	allowed, source, _, err := resolveCourseAccess(ctx.AppDB(), firstCourse.ID, member.ID, false)
	if err != nil || !allowed || source != purchaseSource {
		t.Fatalf("purchase access was erased: allowed=%v source=%q err=%v", allowed, source, err)
	}
	allowed, _, _, err = resolveCourseAccess(ctx.AppDB(), secondCourse.ID, member.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("membership-only course stayed accessible after immediate cancellation")
	}
}

func TestScheduledCancellationCanResume(t *testing.T) {
	ctx, _, _, member, _, _, plan := membershipFixture(t)
	ms := startMembership(t, ctx, member, plan)
	if _, err := toolMembershipCancel(ctx, map[string]any{"id": ms.ID, "member_id": member.ID, "at_period_end": true}); err != nil {
		t.Fatal(err)
	}
	scheduled, err := loadMemberSubscription(ctx.AppDB(), ms.ID)
	if err != nil || scheduled.CancelAt == nil {
		t.Fatalf("scheduled cancellation missing: %+v err=%v", scheduled, err)
	}
	if _, err = toolMembershipResume(ctx, map[string]any{"id": ms.ID, "member_id": member.ID}); err != nil {
		t.Fatal(err)
	}
	resumed, err := loadMemberSubscription(ctx.AppDB(), ms.ID)
	if err != nil || resumed.CancelAt != nil {
		t.Fatalf("resume did not clear cancellation: %+v err=%v", resumed, err)
	}
}

func TestRenewalCycleIsDurableAndDuplicateSafe(t *testing.T) {
	ctx, platform, _, member, _, _, plan := membershipFixture(t)
	ms := startMembership(t, ctx, member, plan)
	if err := (&App{}).handleMembershipBillingEvent(ctx, sdk.Event{
		Event: "invoice.paid", SourceApp: "billing", ProjectID: "test-proj", Data: map[string]any{"id": int64(901)},
	}); err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"subscription_id": int64(301), "cycle_id": int64(402),
		"period_start": "2027-01-01T00:00:00Z", "period_end": "2027-02-01T00:00:00Z",
	}
	event := sdk.Event{Event: "subscription.cycle_due", SourceApp: "subscriptions", ProjectID: "test-proj", Data: data}
	if err := (&App{}).handleMembershipSubscriptionEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).handleMembershipSubscriptionEvent(ctx, event); err != nil {
		t.Fatalf("duplicate cycle event: %v", err)
	}
	var invoiceID int64
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT billing_invoice_id,status FROM membership_cycle_operations
		WHERE member_subscription_id=? AND cycle_id=402`, ms.ID).Scan(&invoiceID, &status); err != nil {
		t.Fatal(err)
	}
	if invoiceID != 902 || status != "action_required" {
		t.Fatalf("renewal operation invoice=%d status=%q", invoiceID, status)
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	if platform.invoiceCreates != 2 || platform.linkCreates != 2 {
		t.Fatalf("duplicate renewal side effects: invoices=%d links=%d", platform.invoiceCreates, platform.linkCreates)
	}
}
