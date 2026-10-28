package main

import (
	"encoding/json"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

type membershipBumpPlatform struct {
	*membershipPlatformStub
	receipt           storefrontReceipt
	subscriptionItems []any
}

func (p *membershipBumpPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	if app+":"+tool == "subscriptions:subscriptions_create" {
		p.subscriptionItems = args["items"].([]any)
	}
	var value any
	switch app + ":" + tool {
	case "checkout:checkout_receipt":
		value = p.receipt
	case "billing:invoices_get":
		value = map[string]any{"invoice": p.invoice}
	default:
		return p.membershipPlatformStub.CallAppResult(app, tool, args, out)
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, out)
}

func TestMembershipBumpOnlyChargesInitialInvoice(t *testing.T) {
	p := &membershipBumpPlatform{membershipPlatformStub: &membershipPlatformStub{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(p))
	c := mustCreateCommunity(t, ctx, "membership-bumps", "Membership")
	m := mustCreateLinkedMember(t, ctx, c.ID, "alice", "auth-alice")
	course := mustCreateSpace(t, ctx, c.ID, "bonus", "course")
	result, err := toolMembershipPlansUpsert(ctx, map[string]any{"community_id": c.ID, "name": "Membership", "catalog_price_id": int64(91), "scope_type": "selected_courses"})
	if err != nil {
		t.Fatal(err)
	}
	plan := result.(map[string]any)["plan"].(*MembershipPlan)
	if _, err = ctx.AppDB().Exec(`INSERT INTO course_offers(space_id,catalog_product_id,catalog_price_id,product_name,unit_amount_cents,currency) VALUES(?,62,72,'Bonus',1900,'EUR')`, course.ID); err != nil {
		t.Fatal(err)
	}
	if included, err := membershipPlanIncludesCourse(ctx.AppDB(), plan.ID, course.ID); err != nil || included {
		t.Fatalf("unexpected inclusion: %v %v", included, err)
	}
	ctx.AppDB().Exec(`UPDATE membership_plans SET scope_type='all_courses' WHERE id=?`, plan.ID)
	if included, err := membershipPlanIncludesCourse(ctx.AppDB(), plan.ID, course.ID); err != nil || !included {
		t.Fatalf("all-course offer not excluded: %v %v", included, err)
	}
	ctx.AppDB().Exec(`UPDATE membership_plans SET scope_type='selected_courses' WHERE id=?`, plan.ID)
	id := int64(901)
	session := guestCheckoutSession{ID: 802, InvoiceID: &id, TotalCents: 11800, Currency: "EUR"}
	target, err := resolveStorefrontOffer(ctx.AppDB(), c.ID, 91)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"order_bump_id": 1, "fulfillment_app": "community", "fulfillment_ref": course.ID})
	cart := guestCheckoutCart{Items: []guestCheckoutItem{{PriceID: 91, ProductID: 81, UnitAmountCents: 9900, Currency: "EUR", Quantity: 1}, {PriceID: 72, ProductID: 62, UnitAmountCents: 1900, Currency: "EUR", Quantity: 1, Metadata: meta}}}
	p.receipt = storefrontReceipt{Items: []storefrontReceiptItem{{ID: 1, PriceID: 91, ProductID: 81, NetCents: 9900, Status: "pending"}, {ID: 2, PriceID: 72, ProductID: 62, NetCents: 1900, Status: "pending", Metadata: meta}}}
	p.invoice = billingInvoice{ID: id, CustomerID: 601, TotalCents: 11800, Currency: "EUR", Status: "open"}
	if err = registerStorefrontItems(ctx, c.ID, m.ID, target, session, cart); err != nil {
		t.Fatal(err)
	}
	if _, err = claimMembershipCheckout(ctx, target, m.ID, "alice@example.test", "auth-alice", session); err != nil {
		t.Fatal(err)
	}
	if len(p.subscriptionItems) != 1 {
		t.Fatalf("add-on entered renewal: %+v", p.subscriptionItems)
	}
	item := p.subscriptionItems[0].(map[string]any)
	if numberFromAny(item["price_id"]) != 91 || numberFromAny(item["unit_amount_cents"]) != 9900 {
		t.Fatalf("renewal must contain only membership price: %+v", item)
	}
}

type communityBumpPlatform struct {
	*salesPlatformStub
	bumps   []storefrontOrderBump
	receipt storefrontReceipt
}

func (p *communityBumpPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	var value any
	switch app + ":" + tool {
	case "checkout:checkout_pay":
		if err := p.salesPlatformStub.CallAppResult(app, tool, args, out); err != nil {
			return err
		}
		var total int64
		for _, it := range p.checkoutCart.Items {
			total += it.UnitAmountCents
		}
		p.invoice.TotalCents = total
		p.checkoutSession.TotalCents = total
		raw, _ := json.Marshal(out)
		var payment map[string]any
		json.Unmarshal(raw, &payment)
		payment["session"] = p.checkoutSession
		raw, _ = json.Marshal(payment)
		return json.Unmarshal(raw, out)
	case "checkout:checkout_bumps_get":
		value = map[string]any{"bumps": p.bumps}
	case "checkout:checkout_receipt":
		value = p.receipt
	default:
		return p.salesPlatformStub.CallAppResult(app, tool, args, out)
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, out)
}

func TestCheckoutIdentityClaimDoesNotCreateInvoice(t *testing.T) {
	p := newSalesPlatformStub()
	ctx, c, member, _ := salesFixture(t, p)
	target, err := resolveStorefrontOffer(ctx.AppDB(), c.ID, 71)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareCheckoutWithCheckoutApp(ctx, c.ID, target, "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := delegatedTool(t, "storefront_checkout_claim")(userPurchaseContext("auth-alice", "alice@example.test"), ctx, map[string]any{"community_id": c.ID, "catalog_price_id": int64(71), "member_id": member.ID, "recovery_token": prepared.RecoveryToken, "prepare_only": true})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["claimed"] != true || p.checkoutPayments != 0 || p.invoice.ID != 0 {
		t.Fatalf("sign-in created payment: result=%+v payments=%d invoice=%+v", out, p.checkoutPayments, p.invoice)
	}
	if p.checkoutSession.Email != "alice@example.test" {
		t.Fatal("verified identity was not bound")
	}
}

func TestMultiItemPaymentFulfillsAndRefundsCoursesIndependently(t *testing.T) {
	p := &communityBumpPlatform{salesPlatformStub: newSalesPlatformStub()}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(p))
	community := mustCreateCommunity(t, ctx, "bump-academy", "Academy")
	member := mustCreateLinkedMember(t, ctx, community.ID, "alice", "auth-alice")
	main := mustCreateSpace(t, ctx, community.ID, "main-course", "course")
	if _, err := toolCourseOfferUpsert(ctx, map[string]any{"space_id": main.ID, "catalog_price_id": int64(71)}); err != nil {
		t.Fatal(err)
	}
	bump := mustCreateSpace(t, ctx, community.ID, "bonus-course", "course")
	_, err := ctx.AppDB().Exec(`INSERT INTO course_offers(space_id,catalog_product_id,catalog_price_id,product_name,unit_amount_cents,currency) VALUES(?,62,72,'Bonus course',1900,'EUR')`, bump.ID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolveStorefrontOffer(ctx.AppDB(), community.ID, 71)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"order_bump_id": 1, "fulfillment_app": "community", "fulfillment_ref": bump.ID})
	invoiceID := int64(701)
	session := guestCheckoutSession{ID: 802, InvoiceID: &invoiceID, TotalCents: 11800, Currency: "EUR"}
	cart := guestCheckoutCart{Items: []guestCheckoutItem{{PriceID: 71, ProductID: 61, Quantity: 1, UnitAmountCents: 9900, Currency: "EUR"}, {PriceID: 72, ProductID: 62, Quantity: 1, UnitAmountCents: 1900, Currency: "EUR", Metadata: meta}}}
	p.receipt = storefrontReceipt{Items: []storefrontReceiptItem{{ID: 1, PriceID: 71, ProductID: 61, NetCents: 9900, Status: "pending"}, {ID: 2, PriceID: 72, ProductID: 62, NetCents: 1900, Status: "pending", Metadata: meta}}}
	p.invoice = billingInvoice{ID: invoiceID, CustomerID: 501, TotalCents: 11800, Currency: "EUR", Status: "open"}
	if err = registerStorefrontItems(ctx, community.ID, member.ID, target, session, cart); err != nil {
		t.Fatal(err)
	}
	purchase, err := claimCourseCheckout(ctx, target, member.ID, "alice@example.test", "auth-alice", session)
	if err != nil {
		t.Fatal(err)
	}
	if err = reconcileStorefrontItems(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if active, _ := hasActiveCourseEnrollment(ctx.AppDB(), bump.ID, member.ID); active {
		t.Fatal("unpaid bump granted access")
	}
	p.invoice.Status = "paid"
	p.invoice.AmountPaidCents = 11800
	for i := range p.receipt.Items {
		p.receipt.Items[i].Status = "paid"
	}
	if _, err = reconcileCoursePurchase(ctx, purchase, "invoice.paid", "billing", nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = reconcileStorefrontItems(ctx, session.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, course := range []Space{main, bump} {
		if active, _ := hasActiveCourseEnrollment(ctx.AppDB(), course.ID, member.ID); !active {
			t.Fatalf("paid course missing: %s", course.ID)
		}
	}
	p.receipt.Items[1].Status = "refunded"
	p.receipt.Items[1].RefundedCents = 1900
	p.invoice.AmountPaidCents = 9900
	p.invoice.Payments = []billingPayment{{AmountCents: -1900}}
	if _, err = reconcileCoursePurchase(ctx, purchase, "invoice.refunded", "billing", nil); err != nil {
		t.Fatal(err)
	}
	if err = reconcileStorefrontItems(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if active, _ := hasActiveCourseEnrollment(ctx.AppDB(), main.ID, member.ID); !active {
		t.Fatal("bump refund revoked main course")
	}
	if active, _ := hasActiveCourseEnrollment(ctx.AppDB(), bump.ID, member.ID); active {
		t.Fatal("refunded bump still active")
	}
	// Other grants must survive a refund/replay of this line.
	ctx.AppDB().Exec(`UPDATE course_enrollments SET status='active',access_revoked_at=NULL,source='manual',source_ref='operator' WHERE space_id=? AND member_id=?`, bump.ID, member.ID)
	if err = reconcileStorefrontItems(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if active, _ := hasActiveCourseEnrollment(ctx.AppDB(), bump.ID, member.ID); !active {
		t.Fatal("refund removed independent manual access")
	}
	wrong := member
	wrong.ID = "other-member"
	if err = registerStorefrontItems(ctx, community.ID, wrong.ID, target, session, cart); err == nil {
		t.Fatal("checkout rebound to another member")
	}
}

func TestBumpCartRejectsUnattributedAndWrongCommunityItems(t *testing.T) {
	p := &communityBumpPlatform{salesPlatformStub: newSalesPlatformStub()}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(p))
	target := storefrontOfferTarget{Kind: "one_time", Price: 71, Product: 61, Amount: 9900, Currency: "EUR"}
	metadata, _ := json.Marshal(map[string]any{"community_id": "academy", "catalog_price_id": 71, "offer_kind": "one_time"})
	session := guestCheckoutSession{ID: 1, Status: "started", Metadata: metadata}
	cart := guestCheckoutCart{Items: []guestCheckoutItem{{PriceID: 71, ProductID: 61, UnitAmountCents: 9900, Currency: "EUR", Quantity: 1}, {PriceID: 72, ProductID: 62, UnitAmountCents: 1900, Currency: "EUR", Quantity: 1}}}
	if err := validatePreparedCheckout("academy", target, session, cart); err == nil {
		t.Fatal("arbitrary extra item accepted")
	}
	cart.Items[1].Metadata = json.RawMessage(`{"order_bump_id":1}`)
	if err := validateStorefrontCartBumps(ctx, "academy", target, session, cart); err == nil {
		t.Fatal("unconfigured bump accepted")
	}
	if err := validatePreparedCheckout("another-academy", target, session, cart); err == nil {
		t.Fatal("cross-community checkout accepted")
	}
}

func TestVerifiedMultiItemClaimPreservesPromotionSnapshotAndOneInvoice(t *testing.T) {
	p := &communityBumpPlatform{salesPlatformStub: newSalesPlatformStub()}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(p))
	c := mustCreateCommunity(t, ctx, "main", "Academy")
	m := mustCreateLinkedMember(t, ctx, c.ID, "alice", "auth-alice")
	base := mustCreateSpace(t, ctx, c.ID, "main-course", "course")
	if _, err := toolCourseOfferUpsert(ctx, map[string]any{"space_id": base.ID, "catalog_price_id": int64(71)}); err != nil {
		t.Fatal(err)
	}
	bonus := mustCreateSpace(t, ctx, c.ID, "bonus", "course")
	if _, err := ctx.AppDB().Exec(`INSERT INTO course_offers(space_id,catalog_product_id,catalog_price_id,product_name,unit_amount_cents,currency) VALUES(?,62,72,'Bonus',1900,'EUR')`, bonus.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO enrollment_rules(space_id,access_mode) VALUES(?,'paid') ON CONFLICT(space_id) DO UPDATE SET access_mode='paid'`, bonus.ID); err != nil {
		t.Fatal(err)
	}
	target, err := resolveStorefrontOffer(ctx.AppDB(), c.ID, 71)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareCheckoutWithCheckoutApp(ctx, c.ID, target, "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"order_bump_id": 1, "fulfillment_app": "community", "fulfillment_ref": bonus.ID})
	p.checkoutCart.Items = append(p.checkoutCart.Items, guestCheckoutItem{PriceID: 73, ProductID: 62, UnitAmountCents: 1900, Currency: "EUR", Quantity: 1, Metadata: meta})
	p.checkoutSession.TotalCents = 11800
	p.bumps = []storefrontOrderBump{{ID: 1, PriceID: 73, ProductID: 62, AmountCents: 1900, Currency: "EUR", Available: true, Selected: true, FulfillmentApp: "community", FulfillmentRef: bonus.ID}}
	p.receipt = storefrontReceipt{Items: []storefrontReceiptItem{{ID: 1, PriceID: 71, ProductID: 61, NetCents: 9900, Status: "pending"}, {ID: 2, PriceID: 73, ProductID: 62, NetCents: 1900, Status: "pending", Metadata: meta}}}
	args := map[string]any{"community_id": c.ID, "catalog_price_id": int64(71), "member_id": m.ID, "recovery_token": prepared.RecoveryToken, "prepare_only": true}
	call := delegatedTool(t, "storefront_checkout_claim")
	if _, err = call(userPurchaseContext("auth-alice", "alice@example.test"), ctx, args); err != nil {
		t.Fatal(err)
	}
	if p.checkoutPayments != 0 {
		t.Fatal("identity binding created an invoice")
	}
	args["prepare_only"] = false
	if _, err = call(userPurchaseContext("auth-alice", "alice@example.test"), ctx, args); err != nil {
		t.Fatal(err)
	}
	if p.checkoutPayments != 1 || p.invoice.TotalCents != 11800 {
		t.Fatalf("invalid shared payment: count=%d invoice=%+v", p.checkoutPayments, p.invoice)
	}
	if metadataString(p.checkoutSession.Metadata, "auth_subject_id") != "auth-alice" {
		t.Fatal("lost verified buyer")
	}
}
