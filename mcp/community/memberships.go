package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const membershipEnrollmentSource = "community_membership"

type MembershipPlan struct {
	ID               string   `json:"id"`
	CommunityID      string   `json:"community_id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	CatalogProductID int64    `json:"catalog_product_id"`
	CatalogPriceID   int64    `json:"catalog_price_id"`
	ProductName      string   `json:"product_name"`
	PriceNickname    string   `json:"price_nickname,omitempty"`
	UnitAmountCents  int64    `json:"unit_amount_cents"`
	Currency         string   `json:"currency"`
	Interval         string   `json:"interval"`
	IntervalCount    int64    `json:"interval_count"`
	ScopeType        string   `json:"scope_type"`
	CollectionMethod string   `json:"collection_method"`
	TrialDays        int64    `json:"trial_days"`
	GraceDays        int64    `json:"grace_days"`
	Active           bool     `json:"active"`
	CourseIDs        []string `json:"course_ids"`
	Tags             []string `json:"tags"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
	ArchivedAt       *string  `json:"archived_at,omitempty"`
}

type MemberSubscription struct {
	ID                 string          `json:"id"`
	CommunityID        string          `json:"community_id"`
	MemberID           string          `json:"member_id"`
	PlanID             string          `json:"plan_id"`
	BillingCustomerID  *int64          `json:"billing_customer_id,omitempty"`
	SubscriptionID     *int64          `json:"subscription_id,omitempty"`
	Status             string          `json:"status"`
	CurrentPeriodStart *string         `json:"current_period_start,omitempty"`
	CurrentPeriodEnd   *string         `json:"current_period_end,omitempty"`
	NextRenewalAt      *string         `json:"next_renewal_at,omitempty"`
	CancelAt           *string         `json:"cancel_at,omitempty"`
	CheckoutURL        *string         `json:"checkout_url,omitempty"`
	PaymentSuccessURL  string          `json:"-"`
	PaymentCancelURL   string          `json:"-"`
	AccessStartedAt    *string         `json:"access_started_at,omitempty"`
	PastDueAt          *string         `json:"past_due_at,omitempty"`
	LastError          string          `json:"last_error,omitempty"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	EndedAt            *string         `json:"ended_at,omitempty"`
	Plan               *MembershipPlan `json:"plan,omitempty"`
}

type subscriptionSnapshot struct {
	ID                 int64           `json:"id"`
	Status             string          `json:"status"`
	CurrentPeriodStart string          `json:"current_period_start"`
	CurrentPeriodEnd   string          `json:"current_period_end"`
	NextRenewalAt      string          `json:"next_renewal_at"`
	CancelAt           string          `json:"cancel_at"`
	Metadata           json.RawMessage `json:"metadata"`
}

type subscriptionCycle struct {
	ID            int64  `json:"id"`
	PeriodStart   string `json:"period_start"`
	PeriodEnd     string `json:"period_end"`
	PaymentStatus string `json:"payment_status"`
}

func membershipTools() []sdk.Tool {
	s := map[string]any{"type": "string"}
	i := map[string]any{"type": "integer"}
	b := map[string]any{"type": "boolean"}
	a := map[string]any{"type": "array", "items": s}
	return []sdk.Tool{
		{Name: "membership_plans_list", Description: "List recurring course membership plans.", InputSchema: schemaObject(map[string]any{"community_id": s, "include_archived": b}, []string{"community_id"}), Handler: toolMembershipPlansList},
		{Name: "membership_plans_get", Description: "Get a recurring course membership plan.", InputSchema: schemaObject(map[string]any{"id": s}, []string{"id"}), Handler: toolMembershipPlansGet},
		{Name: "membership_plans_upsert", Description: "Operator: create or update a Catalog-backed recurring course membership plan.", InputSchema: schemaObject(map[string]any{
			"id": s, "community_id": s, "name": s, "description": s, "catalog_price_id": i,
			"scope_type": s, "collection_method": s, "trial_days": i, "grace_days": i,
		}, []string{"community_id", "name", "catalog_price_id"}), Handler: toolMembershipPlansUpsert},
		{Name: "membership_plans_archive", Description: "Operator: archive a membership plan without affecting existing subscriptions.", InputSchema: schemaObject(map[string]any{"id": s}, []string{"id"}), Handler: toolMembershipPlansArchive},
		{Name: "membership_plan_courses_set", Description: "Operator: replace the selected-course scope of a membership plan.", InputSchema: schemaObject(map[string]any{"id": s, "space_ids": a}, []string{"id", "space_ids"}), Handler: toolMembershipPlanCoursesSet},
		{Name: "membership_plan_tags_set", Description: "Operator: replace the course-tag scope of a membership plan.", InputSchema: schemaObject(map[string]any{"id": s, "tags": a}, []string{"id", "tags"}), Handler: toolMembershipPlanTagsSet},
		{Name: "membership_checkout_start", Description: "Start or resume the verified member's recurring membership checkout.", InputSchema: schemaObject(map[string]any{
			"plan_id": s, "member_id": s, "customer_email": s, "customer_name": s, "success_url": s, "cancel_url": s,
		}, []string{"plan_id", "member_id"}), Handler: toolMembershipCheckoutStart},
		{Name: "membership_status", Description: "Return the verified member's current membership.", InputSchema: schemaObject(map[string]any{"community_id": s, "member_id": s}, []string{"community_id", "member_id"}), Handler: toolMembershipStatus},
		{Name: "membership_cancel", Description: "Cancel a verified member's membership immediately or at period end.", InputSchema: schemaObject(map[string]any{"id": s, "member_id": s, "at_period_end": b, "reason": s}, []string{"id", "member_id"}), Handler: toolMembershipCancel},
		{Name: "membership_resume", Description: "Resume a verified member's scheduled period-end cancellation.", InputSchema: schemaObject(map[string]any{"id": s, "member_id": s}, []string{"id", "member_id"}), Handler: toolMembershipResume},
		{Name: "membership_subscriptions_list", Description: "Operator: list Community membership subscriptions.", InputSchema: schemaObject(map[string]any{"community_id": s, "member_id": s, "status": s, "limit": i}, []string{"community_id"}), Handler: toolMembershipSubscriptionsList},
		{Name: "membership_subscription_get", Description: "Operator: get a Community membership subscription.", InputSchema: schemaObject(map[string]any{"id": s}, []string{"id"}), Handler: toolMembershipSubscriptionGet},
		{Name: "membership_subscription_reconcile", Description: "Operator: reconcile a membership with Subscriptions.", InputSchema: schemaObject(map[string]any{"id": s}, []string{"id"}), Handler: toolMembershipSubscriptionReconcile},
		{Name: "course_access_explain", Description: "Explain whether a member can access a course and which grant source applies.", InputSchema: schemaObject(map[string]any{"space_id": s, "member_id": s}, []string{"space_id", "member_id"}), Handler: toolCourseAccessExplain},
	}
}

func toolMembershipPlansList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	plans, err := listMembershipPlans(ctx.AppDB(), communityID, membershipBoolArg(args, "include_archived", false))
	return map[string]any{"plans": plans, "count": len(plans)}, err
}

func toolMembershipPlansGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	plan, err := loadMembershipPlan(ctx.AppDB(), id, true)
	return map[string]any{"plan": plan}, err
}

func toolMembershipPlansUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	if _, err := loadCommunity(ctx.AppDB(), communityID); err != nil {
		return nil, err
	}
	name, err := mustStr(args, "name")
	if err != nil {
		return nil, err
	}
	priceID, ok := intArg(args, "catalog_price_id")
	if !ok || priceID <= 0 {
		return nil, errors.New("catalog_price_id must be a positive integer")
	}
	price, product, err := fetchMembershipCatalog(ctx, priceID)
	if err != nil {
		return nil, err
	}
	scope := strings.TrimSpace(strArg(args, "scope_type", "all_courses"))
	if scope != "all_courses" && scope != "selected_courses" && scope != "course_tags" {
		return nil, errors.New("scope_type must be all_courses, selected_courses, or course_tags")
	}
	collection := strings.TrimSpace(strArg(args, "collection_method", "automatic"))
	if collection != "automatic" && collection != "send_invoice" {
		return nil, errors.New("collection_method must be automatic or send_invoice")
	}
	trialDays, _ := intArg(args, "trial_days")
	if trialDays < 0 {
		return nil, errors.New("trial_days must be zero or greater")
	}
	if _, present := args["trial_days"]; !present {
		trialDays = price.TrialDays
	}
	graceDays, present := intArg(args, "grace_days")
	if !present {
		graceDays = 7
	}
	if graceDays < 0 {
		return nil, errors.New("grace_days must be zero or greater")
	}
	id := strings.TrimSpace(strArg(args, "id", ""))
	if id == "" {
		id = newID("mplan")
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO membership_plans
		(id,community_id,name,description,catalog_product_id,catalog_price_id,product_name,price_nickname,
		 unit_amount_cents,currency,interval,interval_count,scope_type,collection_method,trial_days,grace_days,active,archived_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,NULL)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,
		catalog_product_id=excluded.catalog_product_id,catalog_price_id=excluded.catalog_price_id,
		product_name=excluded.product_name,price_nickname=excluded.price_nickname,
		unit_amount_cents=excluded.unit_amount_cents,currency=excluded.currency,interval=excluded.interval,
		interval_count=excluded.interval_count,scope_type=excluded.scope_type,
		collection_method=excluded.collection_method,trial_days=excluded.trial_days,
		grace_days=excluded.grace_days,active=1,archived_at=NULL,updated_at=CURRENT_TIMESTAMP
		WHERE membership_plans.community_id=excluded.community_id`,
		id, communityID, name, strings.TrimSpace(strArg(args, "description", "")),
		product.ID, price.ID, product.Name, price.Nickname, price.UnitAmountCents,
		strings.ToUpper(price.Currency), price.Interval, maxInt64(price.IntervalCount, 1),
		scope, collection, trialDays, graceDays)
	if err != nil {
		return nil, err
	}
	plan, err := loadMembershipPlan(ctx.AppDB(), id, true)
	if err == nil {
		emit(ctx, "membership.plan_updated", map[string]any{"community_id": communityID, "plan_id": id})
	}
	return map[string]any{"plan": plan}, err
}

func toolMembershipPlansArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	res, err := ctx.AppDB().Exec(`UPDATE membership_plans SET active=0,archived_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND active=1`, id)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, errors.New("active membership plan not found")
	}
	emit(ctx, "membership.plan_archived", map[string]any{"plan_id": id})
	return toolMembershipPlansGet(ctx, args)
}

func toolMembershipPlanCoursesSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	plan, err := loadMembershipPlan(ctx.AppDB(), id, true)
	if err != nil {
		return nil, err
	}
	values, ok := stringArrayArg(args, "space_ids")
	if !ok {
		return nil, errors.New("space_ids must be an array")
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM membership_plan_courses WHERE plan_id=?`, id); err != nil {
		return nil, err
	}
	for _, spaceID := range values {
		var count int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM spaces WHERE id=? AND community_id=? AND kind='course' AND archived_at IS NULL`, spaceID, plan.CommunityID).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, fmt.Errorf("course %s not found in plan community", spaceID)
		}
		if _, err = tx.Exec(`INSERT INTO membership_plan_courses(plan_id,space_id) VALUES(?,?)`, id, spaceID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return toolMembershipPlansGet(ctx, map[string]any{"id": id})
}

func toolMembershipPlanTagsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	if _, err = loadMembershipPlan(ctx.AppDB(), id, true); err != nil {
		return nil, err
	}
	values, ok := stringArrayArg(args, "tags")
	if !ok {
		return nil, errors.New("tags must be an array")
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM membership_plan_tags WHERE plan_id=?`, id); err != nil {
		return nil, err
	}
	for _, tag := range values {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO membership_plan_tags(plan_id,tag) VALUES(?,?)`, id, tag); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return toolMembershipPlansGet(ctx, map[string]any{"id": id})
}

func toolMembershipCheckoutStart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	planID, err := mustStr(args, "plan_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	plan, err := loadMembershipPlan(ctx.AppDB(), planID, false)
	if err != nil {
		return nil, err
	}
	if err = verifyMember(ctx.AppDB(), plan.CommunityID, memberID); err != nil {
		return nil, err
	}
	if existing, _ := loadLiveMemberSubscription(ctx.AppDB(), plan.CommunityID, memberID); existing != nil {
		if existing.CheckoutURL == nil && existing.AccessStartedAt == nil &&
			existing.Status == "past_due" && existing.SubscriptionID != nil && existing.BillingCustomerID != nil &&
			existing.CurrentPeriodStart != nil && existing.CurrentPeriodEnd != nil {
			var cycleID int64
			if queryErr := ctx.AppDB().QueryRow(`SELECT cycle_id FROM membership_checkouts
				WHERE member_subscription_id=? ORDER BY created_at DESC LIMIT 1`, existing.ID).Scan(&cycleID); queryErr == nil {
				return createMembershipInvoice(ctx, existing.ID, *existing.SubscriptionID, cycleID, *existing.BillingCustomerID,
					*existing.CurrentPeriodStart, *existing.CurrentPeriodEnd, existing.Plan, args, true)
			}
		}
		return membershipResponse(existing), nil
	}
	email := strings.TrimSpace(strArg(args, "customer_email", ""))
	authSubjectID := strings.TrimSpace(strArg(args, "_auth_subject_id", ""))
	if strArg(args, "_viewer_member_id", "") != "" {
		email = strings.TrimSpace(strArg(args, "_subject_email", ""))
	}
	email, err = validatePurchaseEmail(email)
	if err != nil {
		return nil, err
	}
	ms := &MemberSubscription{ID: newID("msub"), CommunityID: plan.CommunityID, MemberID: memberID, PlanID: plan.ID, Status: "creating", Plan: plan}
	if _, err = ctx.AppDB().Exec(`INSERT INTO member_subscriptions
		(id,community_id,member_id,plan_id,status,payment_success_url,payment_cancel_url)
		VALUES(?,?,?,?,?,?,?)`, ms.ID, ms.CommunityID, ms.MemberID, ms.PlanID, ms.Status,
		strings.TrimSpace(strArg(args, "success_url", "")), strings.TrimSpace(strArg(args, "cancel_url", ""))); err != nil {
		return nil, err
	}
	var customer struct {
		Customer struct {
			ID int64 `json:"id"`
		} `json:"customer"`
	}
	err = callAppResult(ctx, "billing", "customers_upsert_by_email", map[string]any{
		"email": email, "defaults": map[string]any{"name": strings.TrimSpace(strArg(args, "customer_name", "")), "external_id": authSubjectID,
			"metadata": map[string]any{"source_app": "community", "community_member_id": memberID}},
	}, &customer)
	if err != nil || customer.Customer.ID == 0 {
		return nil, failMembership(ctx.AppDB(), ms.ID, firstMembershipErr(err, errors.New("Billing returned no customer id")))
	}
	now := time.Now().UTC()
	periodStart := now
	if plan.TrialDays > 0 {
		periodStart = now.AddDate(0, 0, int(plan.TrialDays))
	}
	periodEnd := membershipPeriodEnd(periodStart, plan.Interval, plan.IntervalCount)
	status := "past_due"
	subArgs := map[string]any{
		"customer_id": customer.Customer.ID, "customer_email": email,
		"customer_name": strings.TrimSpace(strArg(args, "customer_name", "")), "kind": "service",
		"status": status, "billing_provider": "stripe", "currency": plan.Currency,
		"interval": plan.Interval, "interval_count": plan.IntervalCount, "source": "community",
		"source_ref": ms.ID, "current_period_start": periodStart.Format(time.RFC3339),
		"current_period_end": periodEnd.Format(time.RFC3339), "next_renewal_at": periodEnd.Format(time.RFC3339),
		"items": []any{map[string]any{"product_id": plan.CatalogProductID, "price_id": plan.CatalogPriceID,
			"title": plan.ProductName, "quantity": 1, "unit_amount_cents": plan.UnitAmountCents,
			"currency": plan.Currency, "billing_scheme": "flat"}},
		"metadata": map[string]any{"source_app": "community", "community_id": plan.CommunityID,
			"community_member_id": memberID, "membership_plan_id": plan.ID,
			"collection_method": plan.CollectionMethod, "unpaid_grace_days": plan.GraceDays},
	}
	if plan.TrialDays > 0 {
		status = "trialing"
		subArgs["status"] = status
		subArgs["trial_start"] = now.Format(time.RFC3339)
		subArgs["trial_end"] = periodStart.Format(time.RFC3339)
		subArgs["trial_end_behavior"] = "collect"
	}
	var subOut struct {
		Subscription subscriptionSnapshot `json:"subscription"`
	}
	if err = callAppResult(ctx, "subscriptions", "subscriptions_create", subArgs, &subOut); err != nil || subOut.Subscription.ID == 0 {
		return nil, failMembership(ctx.AppDB(), ms.ID, firstMembershipErr(err, errors.New("Subscriptions returned no subscription id")))
	}
	_, err = ctx.AppDB().Exec(`UPDATE member_subscriptions SET billing_customer_id=?,subscription_id=?,status=?,
		current_period_start=?,current_period_end=?,next_renewal_at=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		customer.Customer.ID, subOut.Subscription.ID, status, periodStart.Format(time.RFC3339), periodEnd.Format(time.RFC3339), periodEnd.Format(time.RFC3339), ms.ID)
	if err != nil {
		return nil, err
	}
	if status == "trialing" {
		current, _ := loadMemberSubscription(ctx.AppDB(), ms.ID)
		emit(ctx, "membership.trial_started", membershipEventPayload(current))
		return membershipResponse(current), nil
	}
	var cycleOut struct {
		Cycle subscriptionCycle `json:"cycle"`
	}
	if err = callAppResult(ctx, "subscriptions", "subscription_cycles_create", map[string]any{
		"subscription_id": subOut.Subscription.ID, "period_start": periodStart.Format(time.RFC3339),
		"period_end": periodEnd.Format(time.RFC3339), "due_at": now.Format(time.RFC3339), "payment_status": "pending",
		"metadata": map[string]any{"source": "community_initial", "member_subscription_id": ms.ID},
	}, &cycleOut); err != nil {
		return nil, failMembership(ctx.AppDB(), ms.ID, err)
	}
	if _, err = ctx.AppDB().Exec(`INSERT INTO membership_checkouts
		(id,member_subscription_id,cycle_id,status,idempotency_key) VALUES(?,?,?,'creating',?)`,
		newID("mcheckout"), ms.ID, cycleOut.Cycle.ID,
		fmt.Sprintf("community-membership:%d:%d", subOut.Subscription.ID, cycleOut.Cycle.ID)); err != nil {
		return nil, failMembership(ctx.AppDB(), ms.ID, err)
	}
	result, err := createMembershipInvoice(ctx, ms.ID, subOut.Subscription.ID, cycleOut.Cycle.ID, customer.Customer.ID,
		periodStart.Format(time.RFC3339), periodEnd.Format(time.RFC3339), plan, args, true)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func toolMembershipStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	ms, err := loadLiveMemberSubscription(ctx.AppDB(), communityID, memberID)
	if err != nil {
		return nil, err
	}
	return membershipResponse(ms), nil
}

func toolMembershipCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	ms, err := loadMemberSubscription(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if viewer := strArg(args, "_viewer_member_id", ""); viewer != "" && viewer != ms.MemberID {
		return nil, errors.New("membership belongs to another member")
	}
	if ms.SubscriptionID == nil {
		return nil, errors.New("membership has no Subscriptions record")
	}
	atEnd := membershipBoolArg(args, "at_period_end", true)
	var out struct {
		Subscription subscriptionSnapshot `json:"subscription"`
	}
	if err = callAppResult(ctx, "subscriptions", "subscriptions_cancel", map[string]any{
		"id": *ms.SubscriptionID, "at_period_end": atEnd, "reason": strings.TrimSpace(strArg(args, "reason", "member requested")), "actor": "community_member",
	}, &out); err != nil {
		return nil, err
	}
	if atEnd {
		cancelAt := out.Subscription.CancelAt
		if cancelAt == "" && ms.CurrentPeriodEnd != nil {
			cancelAt = *ms.CurrentPeriodEnd
		}
		_, err = ctx.AppDB().Exec(`UPDATE member_subscriptions SET cancel_at=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, nullIfEmpty(cancelAt), id)
	} else {
		_, err = ctx.AppDB().Exec(`UPDATE member_subscriptions SET status='cancelled',cancel_at=CURRENT_TIMESTAMP,ended_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
		if err == nil {
			err = revokeMembershipEnrollments(ctx.AppDB(), id)
		}
	}
	if err != nil {
		return nil, err
	}
	current, err := loadMemberSubscription(ctx.AppDB(), id)
	return membershipResponse(current), err
}

func toolMembershipResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	ms, err := loadMemberSubscription(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if viewer := strArg(args, "_viewer_member_id", ""); viewer != "" && viewer != ms.MemberID {
		return nil, errors.New("membership belongs to another member")
	}
	if ms.SubscriptionID == nil {
		return nil, errors.New("membership has no Subscriptions record")
	}
	var out any
	if err = callAppResult(ctx, "subscriptions", "subscriptions_resume", map[string]any{"id": *ms.SubscriptionID, "actor": "community_member", "reason": "member resumed"}, &out); err != nil {
		return nil, err
	}
	if _, err = ctx.AppDB().Exec(`UPDATE member_subscriptions SET cancel_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id); err != nil {
		return nil, err
	}
	current, err := loadMemberSubscription(ctx.AppDB(), id)
	return membershipResponse(current), err
}

func toolMembershipSubscriptionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + memberSubscriptionCols + ` FROM member_subscriptions ms WHERE ms.community_id=?`
	values := []any{communityID}
	if memberID := strings.TrimSpace(strArg(args, "member_id", "")); memberID != "" {
		query += ` AND ms.member_id=?`
		values = append(values, memberID)
	}
	if status := strings.TrimSpace(strArg(args, "status", "")); status != "" {
		query += ` AND ms.status=?`
		values = append(values, status)
	}
	limit, ok := intArg(args, "limit")
	if !ok || limit < 1 || limit > 200 {
		limit = 100
	}
	query += ` ORDER BY ms.updated_at DESC LIMIT ?`
	values = append(values, limit)
	rows, err := ctx.AppDB().Query(query, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MemberSubscription{}
	for rows.Next() {
		ms, e := scanMemberSubscription(rows.Scan)
		if e != nil {
			return nil, e
		}
		ms.Plan, _ = loadMembershipPlan(ctx.AppDB(), ms.PlanID, true)
		out = append(out, ms)
	}
	return map[string]any{"subscriptions": out, "count": len(out)}, rows.Err()
}

func toolMembershipSubscriptionGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	ms, err := loadMemberSubscription(ctx.AppDB(), id)
	return map[string]any{"subscription": ms}, err
}

func toolMembershipSubscriptionReconcile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	ms, err := reconcileMemberSubscription(ctx, id)
	return map[string]any{"subscription": ms}, err
}

func toolCourseAccessExplain(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	allowed, source, ref, err := resolveCourseAccess(ctx.AppDB(), spaceID, memberID, false)
	return map[string]any{"allowed": allowed, "source": source, "source_ref": ref, "space_id": spaceID, "member_id": memberID}, err
}

func fetchMembershipCatalog(ctx *sdk.AppCtx, priceID int64) (catalogPrice, catalogProduct, error) {
	var priceOut struct {
		Price catalogPrice `json:"price"`
	}
	if err := callAppResult(ctx, "catalog", "catalog_prices_get", map[string]any{"id": priceID}, &priceOut); err != nil {
		return catalogPrice{}, catalogProduct{}, fmt.Errorf("get Catalog price: %w", err)
	}
	price := priceOut.Price
	if price.ID == 0 || !price.Active || price.ArchivedAt != "" {
		return catalogPrice{}, catalogProduct{}, errors.New("Catalog price is missing, inactive, or archived")
	}
	if price.Interval == "" {
		return catalogPrice{}, catalogProduct{}, errors.New("membership plans require a recurring Catalog price")
	}
	if price.BillingScheme != "" && price.BillingScheme != "flat" {
		return catalogPrice{}, catalogProduct{}, errors.New("membership plans currently require a flat Catalog price")
	}
	if price.UnitAmountCents <= 0 {
		return catalogPrice{}, catalogProduct{}, errors.New("membership price must be greater than zero")
	}
	var productOut struct {
		Product catalogProduct `json:"product"`
	}
	if err := callAppResult(ctx, "catalog", "catalog_products_get", map[string]any{"id": price.ProductID}, &productOut); err != nil {
		return catalogPrice{}, catalogProduct{}, fmt.Errorf("get Catalog product: %w", err)
	}
	if productOut.Product.ID == 0 || productOut.Product.ArchivedAt != "" {
		return catalogPrice{}, catalogProduct{}, errors.New("Catalog product is missing or archived")
	}
	return price, productOut.Product, nil
}

func createMembershipInvoice(ctx *sdk.AppCtx, msID string, subID, cycleID, customerID int64, periodStart, periodEnd string, plan *MembershipPlan, urls map[string]any, initial bool) (map[string]any, error) {
	key := fmt.Sprintf("community-membership:%d:%d", subID, cycleID)
	var prepared map[string]any
	if err := callAppResult(ctx, "subscriptions", "subscriptions_invoice_prepare", map[string]any{
		"subscription_id": subID, "cycle_id": cycleID, "period_start": periodStart, "period_end": periodEnd,
		"include_flat": true, "include_metered": true,
	}, &prepared); err != nil {
		return nil, failMembership(ctx.AppDB(), msID, err)
	}
	lines, _ := prepared["line_items"].([]any)
	if len(lines) == 0 {
		return nil, failMembership(ctx.AppDB(), msID, errors.New("Subscriptions prepared no invoice lines"))
	}
	invoiceID, err := mappedMembershipInvoice(ctx.AppDB(), msID, subID, cycleID, initial)
	if err != nil {
		return nil, err
	}
	if invoiceID == 0 {
		invoiceID, err = recoverMembershipInvoice(ctx, customerID, msID, subID, cycleID)
		if err != nil {
			return nil, failMembership(ctx.AppDB(), msID, err)
		}
	}
	if invoiceID == 0 {
		var invoiceOut struct {
			Invoice billingInvoice `json:"invoice"`
		}
		if err := callAppResult(ctx, "billing", "invoices_create_from_prepared_lines", map[string]any{
			"customer_id": customerID, "currency": plan.Currency, "provider": "stripe", "line_items": lines, "finalize": true,
			"metadata": map[string]any{"source_app": "community", "flow": "course_membership", "member_subscription_id": msID,
				"subscription_id": subID, "cycle_id": cycleID, "membership_plan_id": plan.ID, "idempotency_key": key},
		}, &invoiceOut); err != nil {
			return nil, failMembership(ctx.AppDB(), msID, err)
		}
		invoiceID = invoiceOut.Invoice.ID
	}
	if invoiceID == 0 {
		return nil, failMembership(ctx.AppDB(), msID, errors.New("Billing returned no invoice id"))
	}
	if err := callAppResult(ctx, "subscriptions", "subscription_cycles_update", map[string]any{"id": cycleID, "invoice_id": invoiceID, "payment_status": "pending"}, &map[string]any{}); err != nil {
		return nil, failMembership(ctx.AppDB(), msID, err)
	}
	if initial {
		_, err := ctx.AppDB().Exec(`UPDATE membership_checkouts SET billing_invoice_id=?,updated_at=CURRENT_TIMESTAMP
			WHERE member_subscription_id=? AND cycle_id=?`, invoiceID, msID, cycleID)
		if err != nil {
			return nil, err
		}
	} else {
		_, err := ctx.AppDB().Exec(`UPDATE membership_cycle_operations
			SET billing_invoice_id=?,status='invoiced',last_error='',updated_at=CURRENT_TIMESTAMP
			WHERE subscription_id=? AND cycle_id=?`, invoiceID, subID, cycleID)
		if err != nil {
			return nil, err
		}
	}
	if !initial && plan.CollectionMethod == "automatic" {
		ms, err := loadMemberSubscription(ctx.AppDB(), msID)
		if err != nil {
			return nil, err
		}
		out := membershipResponse(ms)
		out["invoice_id"] = invoiceID
		out["cycle_id"] = cycleID
		return out, nil
	}
	return createMembershipPaymentLink(ctx, msID, subID, cycleID, invoiceID, key, urls, initial)
}

func mappedMembershipInvoice(db *sql.DB, msID string, subID, cycleID int64, initial bool) (int64, error) {
	var invoice sql.NullInt64
	var err error
	if initial {
		err = db.QueryRow(`SELECT billing_invoice_id FROM membership_checkouts
			WHERE member_subscription_id=? AND cycle_id=? ORDER BY created_at DESC LIMIT 1`, msID, cycleID).Scan(&invoice)
	} else {
		err = db.QueryRow(`SELECT billing_invoice_id FROM membership_cycle_operations
			WHERE subscription_id=? AND cycle_id=?`, subID, cycleID).Scan(&invoice)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return invoice.Int64, nil
}

func recoverMembershipInvoice(ctx *sdk.AppCtx, customerID int64, msID string, subID, cycleID int64) (int64, error) {
	var out struct {
		Invoices []billingInvoice `json:"invoices"`
	}
	if err := callAppResult(ctx, "billing", "invoices_search", map[string]any{"customer_id": customerID, "limit": 200}, &out); err != nil {
		return 0, fmt.Errorf("search Billing invoices: %w", err)
	}
	for _, invoice := range out.Invoices {
		if metadataString(invoice.Metadata, "member_subscription_id") == msID &&
			metadataString(invoice.Metadata, "subscription_id") == fmt.Sprint(subID) &&
			metadataString(invoice.Metadata, "cycle_id") == fmt.Sprint(cycleID) {
			return invoice.ID, nil
		}
	}
	return 0, nil
}

func createMembershipPaymentLink(ctx *sdk.AppCtx, msID string, subID, cycleID, invoiceID int64, key string, urls map[string]any, initial bool) (map[string]any, error) {
	var payment struct {
		URL             string `json:"url"`
		StripeSessionID string `json:"stripe_session_id"`
	}
	successURL := strings.TrimSpace(strArg(urls, "success_url", ""))
	cancelURL := strings.TrimSpace(strArg(urls, "cancel_url", ""))
	if successURL == "" || cancelURL == "" {
		var storedSuccess, storedCancel string
		if err := ctx.AppDB().QueryRow(`SELECT payment_success_url,payment_cancel_url FROM member_subscriptions WHERE id=?`, msID).Scan(&storedSuccess, &storedCancel); err == nil {
			if successURL == "" {
				successURL = storedSuccess
			}
			if cancelURL == "" {
				cancelURL = storedCancel
			}
		}
	}
	paymentArgs := map[string]any{"invoice_id": invoiceID, "idempotency_key": key,
		"save_payment_method": true, "set_default_payment_method": true,
		"success_url": successURL, "cancel_url": cancelURL}
	if err := callAppResult(ctx, "billing", "invoices_send_payment_link", paymentArgs, &payment); err != nil {
		return nil, failMembership(ctx.AppDB(), msID, err)
	}
	if initial {
		_, err := ctx.AppDB().Exec(`UPDATE membership_checkouts SET billing_session_id=?,checkout_url=?,status='awaiting_payment',updated_at=CURRENT_TIMESTAMP WHERE billing_invoice_id=?`, nullIfEmpty(payment.StripeSessionID), nullIfEmpty(payment.URL), invoiceID)
		if err == nil {
			_, err = ctx.AppDB().Exec(`UPDATE member_subscriptions SET checkout_url=?,last_error='',updated_at=CURRENT_TIMESTAMP WHERE id=?`, nullIfEmpty(payment.URL), msID)
		}
		if err != nil {
			return nil, err
		}
	} else {
		_, err := ctx.AppDB().Exec(`UPDATE membership_cycle_operations SET checkout_url=?,status='action_required',updated_at=CURRENT_TIMESTAMP WHERE subscription_id=? AND cycle_id=?`, nullIfEmpty(payment.URL), subID, cycleID)
		if err != nil {
			return nil, err
		}
	}
	ms, err := loadMemberSubscription(ctx.AppDB(), msID)
	if err != nil {
		return nil, err
	}
	out := membershipResponse(ms)
	out["checkout_url"] = payment.URL
	out["invoice_id"] = invoiceID
	out["cycle_id"] = cycleID
	return out, nil
}

func (a *App) handleMembershipBillingEvent(ctx *sdk.AppCtx, event sdk.Event) error {
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
	var msID string
	var cycleID sql.NullInt64
	var periodStart, periodEnd sql.NullString
	err := ctx.AppDB().QueryRow(`SELECT member_subscription_id,cycle_id,NULL,NULL FROM membership_checkouts WHERE billing_invoice_id=?
		UNION ALL SELECT member_subscription_id,cycle_id,period_start,period_end FROM membership_cycle_operations WHERE billing_invoice_id=? LIMIT 1`, invoiceID, invoiceID).Scan(&msID, &cycleID, &periodStart, &periodEnd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ms, err := loadMemberSubscription(ctx.AppDB(), msID)
	if err != nil || ms.SubscriptionID == nil {
		return err
	}
	switch event.Name() {
	case "invoice.paid":
		if cycleID.Valid {
			_ = callAppResult(ctx, "subscriptions", "subscription_cycles_update", map[string]any{"id": cycleID.Int64, "payment_status": "paid", "fulfillment_status": "fulfilled", "invoice_id": invoiceID}, &map[string]any{})
		}
		var subOut struct {
			Subscription subscriptionSnapshot `json:"subscription"`
		}
		statusArgs := map[string]any{"id": *ms.SubscriptionID, "status": "active", "actor": "community", "note": "Billing invoice paid"}
		if periodStart.Valid && periodEnd.Valid {
			statusArgs["current_period_start"] = periodStart.String
			statusArgs["current_period_end"] = periodEnd.String
			statusArgs["next_renewal_at"] = periodEnd.String
		}
		if err = callAppResult(ctx, "subscriptions", "subscriptions_update_status", statusArgs, &subOut); err != nil {
			return err
		}
		_, err = ctx.AppDB().Exec(`UPDATE member_subscriptions SET status='active',checkout_url=NULL,last_error='',
			access_started_at=COALESCE(access_started_at,CURRENT_TIMESTAMP),past_due_at=NULL,
			current_period_start=COALESCE(NULLIF(?,''),current_period_start),current_period_end=COALESCE(NULLIF(?,''),current_period_end),
			next_renewal_at=COALESCE(NULLIF(?,''),next_renewal_at),updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			subOut.Subscription.CurrentPeriodStart, subOut.Subscription.CurrentPeriodEnd, subOut.Subscription.NextRenewalAt, msID)
		_, _ = ctx.AppDB().Exec(`UPDATE membership_checkouts SET status='paid',paid_at=COALESCE(paid_at,CURRENT_TIMESTAMP),checkout_url=NULL,updated_at=CURRENT_TIMESTAMP WHERE billing_invoice_id=?`, invoiceID)
		_, _ = ctx.AppDB().Exec(`UPDATE membership_cycle_operations SET status='paid',paid_at=COALESCE(paid_at,CURRENT_TIMESTAMP),checkout_url=NULL,updated_at=CURRENT_TIMESTAMP WHERE billing_invoice_id=?`, invoiceID)
		if err == nil {
			emit(ctx, "membership.active", membershipEventPayload(ms))
		}
	case "invoice.payment_failed", "invoice.payment_action_required":
		status := "payment_failed"
		if event.Name() == "invoice.payment_action_required" {
			status = "action_required"
		}
		_, _ = ctx.AppDB().Exec(`UPDATE membership_cycle_operations SET status=?,last_error=?,updated_at=CURRENT_TIMESTAMP WHERE billing_invoice_id=?`, status, event.Name(), invoiceID)
		_, _ = ctx.AppDB().Exec(`UPDATE member_subscriptions SET status='past_due',past_due_at=COALESCE(past_due_at,CURRENT_TIMESTAMP),last_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, event.Name(), msID)
		_ = callAppResult(ctx, "subscriptions", "subscriptions_update_status", map[string]any{"id": *ms.SubscriptionID, "status": "past_due", "actor": "community", "note": event.Name()}, &map[string]any{})
	case "invoice.voided":
		_, _ = ctx.AppDB().Exec(`UPDATE membership_checkouts SET status='cancelled',updated_at=CURRENT_TIMESTAMP WHERE billing_invoice_id=? AND status!='paid'`, invoiceID)
	}
	return err
}

func (a *App) handleMembershipSubscriptionEvent(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "subscriptions" {
		return nil
	}
	subID := numberFromAny(event.Data["subscription_id"])
	if subID == 0 {
		subID = numberFromAny(event.Data["id"])
	}
	if subID == 0 {
		return nil
	}
	var msID string
	err := ctx.AppDB().QueryRow(`SELECT id FROM member_subscriptions WHERE subscription_id=?`, subID).Scan(&msID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if event.Name() == "subscription.cycle_due" {
		return processMembershipCycleDue(ctx, msID, subID, event.Data)
	}
	// Subscriptions deliberately emits subscription.cancelled for both an
	// immediate cancellation and a scheduled period-end cancellation. Fetch
	// the authoritative record so Community never revokes access early.
	_, err = reconcileMemberSubscription(ctx, msID)
	return err
}

func processMembershipCycleDue(ctx *sdk.AppCtx, msID string, subID int64, data map[string]any) error {
	cycleID := numberFromAny(data["cycle_id"])
	periodStart, periodEnd := strings.TrimSpace(fmt.Sprint(data["period_start"])), strings.TrimSpace(fmt.Sprint(data["period_end"]))
	if cycleID == 0 || periodStart == "" || periodEnd == "" {
		return errors.New("subscription.cycle_due missing cycle or period")
	}
	var existingStatus string
	err := ctx.AppDB().QueryRow(`SELECT status FROM membership_cycle_operations WHERE subscription_id=? AND cycle_id=?`, subID, cycleID).Scan(&existingStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existingStatus == "paid" || existingStatus == "collecting" || existingStatus == "action_required" {
		return nil
	}
	ms, err := loadMemberSubscription(ctx.AppDB(), msID)
	if err != nil || ms.BillingCustomerID == nil {
		return firstMembershipErr(err, errors.New("membership has no Billing customer"))
	}
	plan, err := loadMembershipPlan(ctx.AppDB(), ms.PlanID, true)
	if err != nil {
		return err
	}
	if _, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO membership_cycle_operations(member_subscription_id,subscription_id,cycle_id,period_start,period_end,status,idempotency_key)
		VALUES(?,?,?,?,?,'pending',?)`, msID, subID, cycleID, periodStart, periodEnd, fmt.Sprintf("community-membership:%d:%d", subID, cycleID)); err != nil {
		return err
	}
	result, err := createMembershipInvoice(ctx, msID, subID, cycleID, *ms.BillingCustomerID, periodStart, periodEnd, plan, map[string]any{}, false)
	if err != nil {
		return err
	}
	invoiceID := numberFromAny(result["invoice_id"])
	if plan.CollectionMethod == "automatic" {
		var collect map[string]any
		err = callAppResult(ctx, "billing", "invoices_collect", map[string]any{"invoice_id": invoiceID, "idempotency_key": fmt.Sprintf("community-membership:%d:%d:collect", subID, cycleID)}, &collect)
		if err == nil {
			_, _ = ctx.AppDB().Exec(`UPDATE membership_cycle_operations SET status='collecting',updated_at=CURRENT_TIMESTAMP WHERE subscription_id=? AND cycle_id=?`, subID, cycleID)
			return nil
		}
		// A hosted payment link is the deterministic recovery path when no
		// reusable method exists or off-session collection cannot start.
		_, linkErr := createMembershipPaymentLink(ctx, msID, subID, cycleID, invoiceID,
			fmt.Sprintf("community-membership:%d:%d", subID, cycleID), map[string]any{}, false)
		if linkErr != nil {
			return linkErr
		}
		_, _ = ctx.AppDB().Exec(`UPDATE membership_cycle_operations SET status='action_required',last_error=?,updated_at=CURRENT_TIMESTAMP WHERE subscription_id=? AND cycle_id=?`, err.Error(), subID, cycleID)
	}
	return nil
}

func reconcileMemberSubscription(ctx *sdk.AppCtx, id string) (*MemberSubscription, error) {
	ms, err := loadMemberSubscription(ctx.AppDB(), id)
	if err != nil || ms.SubscriptionID == nil {
		return ms, firstMembershipErr(err, errors.New("membership has no Subscriptions record"))
	}
	var out struct {
		Subscription subscriptionSnapshot `json:"subscription"`
	}
	if err = callAppResult(ctx, "subscriptions", "subscriptions_get", map[string]any{"id": *ms.SubscriptionID}, &out); err != nil {
		return nil, err
	}
	sub := out.Subscription
	_, err = ctx.AppDB().Exec(`UPDATE member_subscriptions SET status=?,
		current_period_start=NULLIF(?,''),current_period_end=NULLIF(?,''),next_renewal_at=NULLIF(?,''),cancel_at=NULLIF(?,''),
		past_due_at=CASE WHEN ?='past_due' THEN COALESCE(past_due_at,CURRENT_TIMESTAMP) WHEN ?='active' THEN NULL ELSE past_due_at END,
		ended_at=CASE WHEN ? IN ('cancelled','ended') THEN COALESCE(ended_at,CURRENT_TIMESTAMP) ELSE ended_at END,
		updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		sub.Status, sub.CurrentPeriodStart, sub.CurrentPeriodEnd, sub.NextRenewalAt, sub.CancelAt,
		sub.Status, sub.Status, sub.Status, id)
	if err != nil {
		return nil, err
	}
	if sub.Status == "cancelled" || sub.Status == "ended" {
		_ = revokeMembershipEnrollments(ctx.AppDB(), id)
	}
	return loadMemberSubscription(ctx.AppDB(), id)
}

func resolveCourseAccess(db *sql.DB, spaceID, memberID string, materialize bool) (bool, string, string, error) {
	var source string
	var ref sql.NullString
	err := db.QueryRow(`SELECT source,source_ref FROM course_enrollments
		WHERE space_id=? AND member_id=? AND status IN ('active','completed') AND access_revoked_at IS NULL
		AND (access_expires_at IS NULL OR datetime(access_expires_at)>=CURRENT_TIMESTAMP)`, spaceID, memberID).Scan(&source, &ref)
	if err == nil && source != membershipEnrollmentSource {
		return true, source, ref.String, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, "", "", err
	}
	var msID string
	err = db.QueryRow(`SELECT ms.id FROM member_subscriptions ms
		JOIN membership_plans p ON p.id=ms.plan_id
		JOIN spaces s ON s.id=? AND s.community_id=ms.community_id AND s.kind='course' AND s.archived_at IS NULL
		LEFT JOIN course_details cm ON cm.space_id=s.id
		WHERE ms.member_id=? AND (
			ms.status IN ('active','trialing') OR
			(ms.status='past_due' AND ms.access_started_at IS NOT NULL AND ms.past_due_at IS NOT NULL
			 AND p.grace_days>0 AND datetime(ms.past_due_at,'+'||p.grace_days||' days')>=CURRENT_TIMESTAMP)
		) AND (
			p.scope_type='all_courses' OR
			(p.scope_type='selected_courses' AND EXISTS(SELECT 1 FROM membership_plan_courses pc WHERE pc.plan_id=p.id AND pc.space_id=s.id)) OR
			(p.scope_type='course_tags' AND EXISTS(
				SELECT 1 FROM membership_plan_tags pt, json_each(COALESCE(cm.tags_json,'[]')) jt
				WHERE pt.plan_id=p.id AND lower(pt.tag)=lower(jt.value)
			))
		) ORDER BY CASE ms.status WHEN 'active' THEN 0 WHEN 'trialing' THEN 1 ELSE 2 END,ms.updated_at DESC LIMIT 1`, spaceID, memberID).Scan(&msID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", "", nil
	}
	if err != nil {
		return false, "", "", err
	}
	if materialize {
		_, err = db.Exec(`INSERT INTO course_enrollments(space_id,member_id,status,source,source_ref,access_revoked_at)
			VALUES(?,?,'active',?,?,NULL)
			ON CONFLICT(space_id,member_id) DO UPDATE SET status='active',source=excluded.source,source_ref=excluded.source_ref,
			access_revoked_at=NULL WHERE course_enrollments.source=?`,
			spaceID, memberID, membershipEnrollmentSource, msID, membershipEnrollmentSource)
		if err != nil {
			return false, "", "", err
		}
	}
	return true, membershipEnrollmentSource, msID, nil
}

func revokeMembershipEnrollments(db *sql.DB, msID string) error {
	_, err := db.Exec(`UPDATE course_enrollments SET status='cancelled',access_revoked_at=CURRENT_TIMESTAMP
		WHERE source=? AND source_ref=?`, membershipEnrollmentSource, msID)
	return err
}

func recoverMembershipOperations(_ context.Context, ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.AppDB() == nil {
		return nil
	}
	rows, err := ctx.AppDB().Query(`SELECT id FROM member_subscriptions WHERE subscription_id IS NOT NULL AND status IN ('creating','past_due','active','trialing','paused') AND updated_at<datetime('now','-5 minutes') ORDER BY updated_at LIMIT 25`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		if _, e := reconcileMemberSubscription(ctx, id); e != nil {
			ctx.Logger().Warn("membership reconcile failed", "id", id, "err", e)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	initialRows, err := ctx.AppDB().Query(`SELECT ms.id,ms.subscription_id,mc.cycle_id,ms.billing_customer_id,
		ms.current_period_start,ms.current_period_end,ms.plan_id
		FROM membership_checkouts mc JOIN member_subscriptions ms ON ms.id=mc.member_subscription_id
		WHERE mc.status='creating' AND ms.subscription_id IS NOT NULL AND ms.billing_customer_id IS NOT NULL
		ORDER BY mc.updated_at LIMIT 25`)
	if err != nil {
		return err
	}
	type initialRecovery struct {
		msID, start, end, planID   string
		subID, cycleID, customerID int64
	}
	var initials []initialRecovery
	for initialRows.Next() {
		var item initialRecovery
		if err = initialRows.Scan(&item.msID, &item.subID, &item.cycleID, &item.customerID, &item.start, &item.end, &item.planID); err != nil {
			initialRows.Close()
			return err
		}
		initials = append(initials, item)
	}
	if err = initialRows.Close(); err != nil {
		return err
	}
	for _, item := range initials {
		plan, loadErr := loadMembershipPlan(ctx.AppDB(), item.planID, true)
		if loadErr != nil {
			continue
		}
		if _, recoverErr := createMembershipInvoice(ctx, item.msID, item.subID, item.cycleID, item.customerID, item.start, item.end, plan, map[string]any{}, true); recoverErr != nil {
			ctx.Logger().Warn("membership checkout recovery failed", "id", item.msID, "err", recoverErr)
		}
	}

	cycleRows, err := ctx.AppDB().Query(`SELECT member_subscription_id,subscription_id,cycle_id,period_start,period_end
		FROM membership_cycle_operations WHERE status IN ('pending','invoiced','payment_failed')
		AND updated_at<datetime('now','-1 minute') ORDER BY updated_at LIMIT 25`)
	if err != nil {
		return err
	}
	var cycles []struct {
		msID, start, end string
		subID, cycleID   int64
	}
	for cycleRows.Next() {
		var item struct {
			msID, start, end string
			subID, cycleID   int64
		}
		if err = cycleRows.Scan(&item.msID, &item.subID, &item.cycleID, &item.start, &item.end); err != nil {
			cycleRows.Close()
			return err
		}
		cycles = append(cycles, item)
	}
	if err = cycleRows.Close(); err != nil {
		return err
	}
	for _, item := range cycles {
		if recoverErr := processMembershipCycleDue(ctx, item.msID, item.subID, map[string]any{
			"cycle_id": item.cycleID, "period_start": item.start, "period_end": item.end,
		}); recoverErr != nil {
			ctx.Logger().Warn("membership renewal recovery failed", "id", item.msID, "cycle_id", item.cycleID, "err", recoverErr)
		}
	}
	return nil
}

const memberSubscriptionCols = `ms.id,ms.community_id,ms.member_id,ms.plan_id,ms.billing_customer_id,ms.subscription_id,
	ms.status,ms.current_period_start,ms.current_period_end,ms.next_renewal_at,ms.cancel_at,ms.checkout_url,
	ms.payment_success_url,ms.payment_cancel_url,ms.access_started_at,ms.past_due_at,ms.last_error,ms.created_at,ms.updated_at,ms.ended_at`

func loadMemberSubscription(db *sql.DB, id string) (*MemberSubscription, error) {
	ms, err := scanMemberSubscription(db.QueryRow(`SELECT `+memberSubscriptionCols+` FROM member_subscriptions ms WHERE ms.id=?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("membership subscription not found")
	}
	if err != nil {
		return nil, err
	}
	ms.Plan, _ = loadMembershipPlan(db, ms.PlanID, true)
	return ms, nil
}

func loadLiveMemberSubscription(db *sql.DB, communityID, memberID string) (*MemberSubscription, error) {
	ms, err := scanMemberSubscription(db.QueryRow(`SELECT `+memberSubscriptionCols+` FROM member_subscriptions ms
		WHERE ms.community_id=? AND ms.member_id=? AND ms.status IN ('creating','trialing','past_due','active','paused')
		ORDER BY ms.updated_at DESC LIMIT 1`, communityID, memberID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ms.Plan, _ = loadMembershipPlan(db, ms.PlanID, true)
	return ms, nil
}

func scanMemberSubscription(scan func(...any) error) (*MemberSubscription, error) {
	var ms MemberSubscription
	var customerID, subID sql.NullInt64
	var start, end, renewal, cancelAt, checkout, accessStarted, pastDue, ended sql.NullString
	err := scan(&ms.ID, &ms.CommunityID, &ms.MemberID, &ms.PlanID, &customerID, &subID, &ms.Status,
		&start, &end, &renewal, &cancelAt, &checkout, &ms.PaymentSuccessURL, &ms.PaymentCancelURL,
		&accessStarted, &pastDue, &ms.LastError, &ms.CreatedAt, &ms.UpdatedAt, &ended)
	if err != nil {
		return nil, err
	}
	if customerID.Valid {
		ms.BillingCustomerID = &customerID.Int64
	}
	if subID.Valid {
		ms.SubscriptionID = &subID.Int64
	}
	ms.CurrentPeriodStart = stringPtr(start)
	ms.CurrentPeriodEnd = stringPtr(end)
	ms.NextRenewalAt = stringPtr(renewal)
	ms.CancelAt = stringPtr(cancelAt)
	ms.CheckoutURL = stringPtr(checkout)
	ms.AccessStartedAt = stringPtr(accessStarted)
	ms.PastDueAt = stringPtr(pastDue)
	ms.EndedAt = stringPtr(ended)
	return &ms, nil
}

func loadMembershipPlan(db *sql.DB, id string, includeArchived bool) (*MembershipPlan, error) {
	query := `SELECT id,community_id,name,description,catalog_product_id,catalog_price_id,product_name,price_nickname,
		unit_amount_cents,currency,interval,interval_count,scope_type,collection_method,trial_days,grace_days,
		active,created_at,updated_at,archived_at FROM membership_plans WHERE id=?`
	if !includeArchived {
		query += ` AND active=1 AND archived_at IS NULL`
	}
	var p MembershipPlan
	var active int
	var archived sql.NullString
	err := db.QueryRow(query, id).Scan(&p.ID, &p.CommunityID, &p.Name, &p.Description, &p.CatalogProductID, &p.CatalogPriceID,
		&p.ProductName, &p.PriceNickname, &p.UnitAmountCents, &p.Currency, &p.Interval, &p.IntervalCount, &p.ScopeType,
		&p.CollectionMethod, &p.TrialDays, &p.GraceDays, &active, &p.CreatedAt, &p.UpdatedAt, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("membership plan not found")
	}
	if err != nil {
		return nil, err
	}
	p.Active = active != 0
	if archived.Valid {
		p.ArchivedAt = &archived.String
	}
	p.CourseIDs, _ = queryStrings(db, `SELECT space_id FROM membership_plan_courses WHERE plan_id=? ORDER BY space_id`, id)
	p.Tags, _ = queryStrings(db, `SELECT tag FROM membership_plan_tags WHERE plan_id=? ORDER BY tag`, id)
	return &p, nil
}

func listMembershipPlans(db *sql.DB, communityID string, includeArchived bool) ([]*MembershipPlan, error) {
	query := `SELECT id FROM membership_plans WHERE community_id=?`
	if !includeArchived {
		query += ` AND active=1 AND archived_at IS NULL`
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := db.Query(query, communityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	out := make([]*MembershipPlan, 0, len(ids))
	for _, id := range ids {
		p, e := loadMembershipPlan(db, id, true)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func membershipResponse(ms *MemberSubscription) map[string]any {
	out := map[string]any{"subscription": ms, "has_membership": ms != nil}
	if ms != nil && ms.CheckoutURL != nil {
		out["checkout_url"] = *ms.CheckoutURL
	}
	if ms != nil {
		out["access_active"] = ms.Status == "active" || ms.Status == "trialing" || ms.Status == "past_due"
	}
	return out
}

func membershipEventPayload(ms *MemberSubscription) map[string]any {
	if ms == nil {
		return map[string]any{}
	}
	out := map[string]any{"member_subscription_id": ms.ID, "community_id": ms.CommunityID, "member_id": ms.MemberID, "plan_id": ms.PlanID, "status": ms.Status}
	if ms.SubscriptionID != nil {
		out["subscription_id"] = *ms.SubscriptionID
	}
	return out
}

func failMembership(db *sql.DB, id string, cause error) error {
	if cause == nil {
		cause = errors.New("membership operation failed")
	}
	_, _ = db.Exec(`UPDATE member_subscriptions
		SET status=CASE WHEN subscription_id IS NULL THEN 'failed' ELSE status END,
		    last_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, cause.Error(), id)
	return cause
}

func queryStrings(db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func stringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}
func maxInt64(v, min int64) int64 {
	if v < min {
		return min
	}
	return v
}
func firstMembershipErr(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}
func membershipBoolArg(args map[string]any, key string, fallback bool) bool {
	if value, ok := args[key].(bool); ok {
		return value
	}
	return fallback
}
func membershipPeriodEnd(start time.Time, interval string, count int64) time.Time {
	if count < 1 {
		count = 1
	}
	switch interval {
	case "day":
		return start.AddDate(0, 0, int(count))
	case "week":
		return start.AddDate(0, 0, int(count*7))
	case "year":
		return start.AddDate(int(count), 0, 0)
	default:
		return start.AddDate(0, int(count), 0)
	}
}
