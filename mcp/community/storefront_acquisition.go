package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"regexp"
	"sort"
	"strings"
	"time"
)

type storefrontAcquisitionInput struct {
	PromotionCode string            `json:"promotion_code"`
	Attribution   map[string]string `json:"attribution"`
}
type storefrontCheckoutContext struct {
	SessionID   int64
	CommunityID string
	PriceID     int64
	Subtotal    int64
	Discount    int64
	Currency    string
	Code        string
	FirstOnly   bool
	Reservation string
	Attribution json.RawMessage
	PurchaseID  sql.NullString
}
type storefrontDiscountQuote struct {
	Eligible    bool   `json:"eligible"`
	DiscountID  int64  `json:"discount_id"`
	Discount    int64  `json:"discount_cents"`
	Total       int64  `json:"total_cents"`
	Currency    string `json:"currency"`
	Application struct {
		Metadata json.RawMessage `json:"metadata"`
	} `json:"application"`
}

var acquisitionValue = regexp.MustCompile(`^[a-zA-Z0-9_.~:+ /=-]{1,200}$`)

func sanitizeStorefrontAttribution(input map[string]string) json.RawMessage {
	out := map[string]string{}
	for _, k := range []string{"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term", "campaign_id", "adset_id", "ad_id", "fbclid", "captured_at"} {
		if v := input[k]; acquisitionValue.MatchString(v) {
			out[k] = v
		}
	}
	if stamp := out["captured_at"]; stamp != "" {
		if captured, err := time.Parse(time.RFC3339, stamp); err != nil || captured.After(time.Now().Add(5*time.Minute)) || time.Since(captured) > 7*24*time.Hour {
			out = map[string]string{}
		}
	} else {
		out = map[string]string{}
	}
	encoded, _ := json.Marshal(out)
	return encoded
}
func checkoutContext(db *sql.DB, id int64) (*storefrontCheckoutContext, error) {
	c := &storefrontCheckoutContext{SessionID: id}
	var attribution string
	err := db.QueryRow(`SELECT community_id,catalog_price_id,subtotal_cents,discount_cents,currency,promotion_code,first_purchase_only,reservation_id,attribution_json,purchase_id FROM storefront_checkout_context WHERE checkout_session_id=?`, id).Scan(&c.CommunityID, &c.PriceID, &c.Subtotal, &c.Discount, &c.Currency, &c.Code, &c.FirstOnly, &c.Reservation, &attribution, &c.PurchaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	c.Attribution = json.RawMessage(attribution)
	return c, err
}
func discountArgs(target storefrontOfferTarget, code, customer, context string) map[string]any {
	return map[string]any{"code": code, "customer_ref": customer, "context_ref": context, "product_id": target.Product, "price_id": target.Price, "currency": target.Currency, "subtotal_cents": target.Amount, "quantity": 1}
}
func prepareStorefrontContext(ctx *sdk.AppCtx, communityID string, target storefrontOfferTarget, session guestCheckoutSession, input storefrontAcquisitionInput) error {
	c := &storefrontCheckoutContext{SessionID: session.ID, CommunityID: communityID, PriceID: target.Price, Subtotal: target.Amount, Currency: target.Currency, Code: strings.TrimSpace(input.PromotionCode), Attribution: sanitizeStorefrontAttribution(input.Attribution)}
	if len(c.Code) > 100 {
		return errors.New("invalid promotion")
	}
	if c.Code != "" {
		if target.Kind != "one_time" {
			return errors.New("promotions currently require a one-time course")
		}
		var out struct {
			Quote storefrontDiscountQuote `json:"quote"`
		}
		if err := callAppResult(ctx, "catalog", "catalog_discounts_quote", discountArgs(target, c.Code, fmt.Sprintf("preview:%s:%d", communityID, session.ID), ""), &out); err != nil {
			return err
		}
		q := out.Quote
		if !q.Eligible || q.Discount <= 0 || q.Discount >= target.Amount || q.Total != target.Amount-q.Discount || !strings.EqualFold(q.Currency, target.Currency) {
			return errors.New("welcome offer is unavailable")
		}
		c.Discount = q.Discount
		c.FirstOnly = metadataString(q.Application.Metadata, "first_purchase_only") == "true"
	}
	_, err := ctx.AppDB().Exec(`INSERT INTO storefront_checkout_context(checkout_session_id,community_id,catalog_price_id,subtotal_cents,discount_cents,currency,promotion_code,first_purchase_only,attribution_json) VALUES(?,?,?,?,?,?,?,?,?)`, session.ID, communityID, target.Price, target.Amount, c.Discount, target.Currency, c.Code, c.FirstOnly, string(c.Attribution))
	if err != nil {
		return err
	}
	if c.Discount > 0 {
		var out any
		if err = callAppResult(ctx, "checkout", "checkout_set_adjustments", map[string]any{"session_id": session.ID, "discount_cents": c.Discount, "adjustments": map[string]any{"catalog_promotion_code": c.Code, "catalog_subtotal_cents": target.Amount}}, &out); err != nil {
			return err
		}
	}
	// Community snapshots campaign context and copies it to Billing when claiming.
	var updated any
	return callAppResult(ctx, "checkout", "checkout_update", map[string]any{"session_id": session.ID, "patch": map[string]any{"metadata": map[string]any{"source_app": "community", "community_id": communityID, "catalog_price_id": target.Price, "offer_kind": target.Kind, "promotion_code": c.Code, "attribution": json.RawMessage(c.Attribution), "storefront_context_version": 1}}}, &updated)
}
func validateStorefrontContext(c *storefrontCheckoutContext, communityID string, target storefrontOfferTarget, session guestCheckoutSession) error {
	if c == nil {
		if session.TotalCents-session.BumpCents != target.Amount {
			return errors.New("checkout total does not match course")
		}
		return nil
	}
	if c.CommunityID != communityID || c.PriceID != target.Price || c.Subtotal != target.Amount || !strings.EqualFold(c.Currency, target.Currency) || session.TotalCents-session.BumpCents != c.Subtotal-c.Discount {
		return errors.New("checkout no longer matches the saved offer")
	}
	return nil
}
func storefrontCustomerRef(communityID, email string) string {
	sum := sha256.Sum256([]byte(communityID + ":" + strings.ToLower(strings.TrimSpace(email))))
	return "community:" + hex.EncodeToString(sum[:])
}
func hasPriorCoursePayment(db *sql.DB, communityID, email string, sessionID int64) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM course_purchases p LEFT JOIN storefront_checkout_context c ON c.purchase_id=p.id WHERE p.community_id=? AND lower(trim(p.customer_email))=? AND p.paid_at IS NOT NULL AND (c.checkout_session_id IS NULL OR c.checkout_session_id<>?)`, communityID, strings.ToLower(strings.TrimSpace(email)), sessionID).Scan(&count)
	return count > 0, err
}
func reserveStorefrontPromotion(ctx *sdk.AppCtx, communityID string, target storefrontOfferTarget, session guestCheckoutSession, email string) error {
	c, err := checkoutContext(ctx.AppDB(), session.ID)
	if err != nil {
		return err
	}
	if err = validateStorefrontContext(c, communityID, target, session); err != nil {
		return err
	}
	if c == nil || c.Code == "" {
		return nil
	}
	if c.FirstOnly {
		prior, err := hasPriorCoursePayment(ctx.AppDB(), communityID, email, session.ID)
		if err != nil {
			return err
		}
		if prior {
			return errors.New("this welcome offer is for first-time course customers")
		}
	}
	// An awaiting-payment retry retains the same reservation and authoritative invoice.
	if session.Status == "awaiting_payment" && c.Reservation != "" {
		return nil
	}
	args := discountArgs(target, c.Code, storefrontCustomerRef(communityID, email), fmt.Sprintf("community-checkout:%d", session.ID))
	args["idempotency_key"] = fmt.Sprintf("community:%s:checkout:%d", communityID, session.ID)
	args["expires_in_seconds"] = int64(86400)
	var out struct {
		Reservation struct {
			ID       string `json:"reservation_id"`
			Discount int64  `json:"discount_cents"`
			Total    int64  `json:"total_cents"`
			Status   string `json:"status"`
		} `json:"reservation"`
	}
	if err = callAppResult(ctx, "catalog", "catalog_discounts_reserve", args, &out); err != nil {
		return fmt.Errorf("welcome offer eligibility: %w", err)
	}
	if out.Reservation.ID == "" || out.Reservation.Status != "reserved" || out.Reservation.Discount != c.Discount || out.Reservation.Total != target.Amount-c.Discount {
		return errors.New("welcome offer changed; restart checkout")
	}
	_, err = ctx.AppDB().Exec(`UPDATE storefront_checkout_context SET reservation_id=? WHERE checkout_session_id=?`, out.Reservation.ID, session.ID)
	return err
}
func bindStorefrontPurchase(ctx *sdk.AppCtx, sessionID int64, purchaseID string) error {
	_, err := ctx.AppDB().Exec(`UPDATE storefront_checkout_context SET purchase_id=? WHERE checkout_session_id=? AND (purchase_id IS NULL OR purchase_id=?)`, purchaseID, sessionID, purchaseID)
	return err
}
func settleStorefrontPromotion(ctx *sdk.AppCtx, purchaseID string, paid bool) error {
	var id string
	err := ctx.AppDB().QueryRow(`SELECT reservation_id FROM storefront_checkout_context WHERE purchase_id=?`, purchaseID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && id == "") {
		return nil
	}
	if err != nil {
		return err
	}
	tool := "catalog_discounts_release"
	if paid {
		tool = "catalog_discounts_redeem"
	}
	var out any
	return callAppResult(ctx, "catalog", tool, map[string]any{"reservation_id": id}, &out)
}

// Read the authoritative paid purchase ledger, not browser success events.
// Every customer is keyed by normalized verified email across this community.
func toolStorefrontAcquisitionReport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	if err = ensureCommunityVisible(ctx, ctx.AppDB(), communityID); err != nil {
		return nil, err
	}
	since := strArg(args, "since", "")
	until := strArg(args, "until", "")
	for _, v := range []string{since, until} {
		if _, e := time.Parse("2006-01-02", v); e != nil {
			return nil, errors.New("since and until must be YYYY-MM-DD")
		}
	}
	if since > until {
		return nil, errors.New("since must precede until")
	}
	zone := strArg(args, "timezone", "UTC")
	location, err := time.LoadLocation(zone)
	if err != nil {
		return nil, errors.New("timezone must be a valid IANA zone")
	}
	// Rank across lifetime before applying the reporting window in the ad account's timezone.
	rows, err := ctx.AppDB().Query(`WITH ranked AS (
 SELECT p.id,p.paid_at,p.refunded_cents,p.unit_amount_cents,p.currency,
 CASE WHEN julianday(p.paid_at)-julianday(json_extract(c.attribution_json,'$.captured_at')) BETWEEN 0 AND 7 THEN c.attribution_json ELSE '{}' END attribution_json,
 ROW_NUMBER() OVER(PARTITION BY lower(trim(p.customer_email)) ORDER BY datetime(p.paid_at),p.created_at,p.id) rn
 FROM course_purchases p LEFT JOIN storefront_checkout_context c ON c.purchase_id=p.id
 WHERE p.community_id=? AND p.paid_at IS NOT NULL)
 SELECT COALESCE(attribution_json,'{}'),currency,unit_amount_cents,refunded_cents,paid_at FROM ranked WHERE rn=1`, communityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type cohort struct {
		Attribution  json.RawMessage `json:"attribution"`
		Currency     string          `json:"currency"`
		NewCustomers int64           `json:"unique_new_paying_customers"`
		Revenue      int64           `json:"first_purchase_revenue_cents"`
		Refunds      int64           `json:"refund_cents"`
	}
	grouped := map[string]*cohort{}
	for rows.Next() {
		var attribution, currency, stamp string
		var amount, refunded int64
		if err = rows.Scan(&attribution, &currency, &amount, &refunded, &stamp); err != nil {
			return nil, err
		}
		paid, err := parseStorefrontTimestamp(stamp)
		if err != nil {
			return nil, err
		}
		day := paid.In(location).Format("2006-01-02")
		if day < since || day > until {
			continue
		}
		key := currency + ":" + attribution
		c := grouped[key]
		if c == nil {
			c = &cohort{Attribution: json.RawMessage(attribution), Currency: currency}
			grouped[key] = c
		}
		c.NewCustomers++
		c.Revenue += amount
		c.Refunds += refunded
	}
	keys := []string{}
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := []cohort{}
	for _, k := range keys {
		result = append(result, *grouped[k])
	}
	return map[string]any{"cohorts": result, "since": since, "until": until, "timezone": zone, "scope": "community course purchases; first confirmed payment across lifetime; refunds reported separately", "attribution_model": "first tagged session, 7-day click; unattributed purchases remain separate"}, rows.Err()
}

func parseStorefrontTimestamp(stamp string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, stamp); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("invalid confirmed payment timestamp")
}
func emitStorefrontAcquisition(ctx *sdk.AppCtx, purchase *CoursePurchase) error {
	var firstID string
	if err := ctx.AppDB().QueryRow(`SELECT id FROM course_purchases WHERE community_id=? AND lower(trim(customer_email))=? AND paid_at IS NOT NULL ORDER BY datetime(paid_at),created_at,id LIMIT 1`, purchase.CommunityID, strings.ToLower(strings.TrimSpace(purchase.CustomerEmail))).Scan(&firstID); err != nil {
		return err
	}
	if firstID != purchase.ID {
		return nil
	}
	saved, err := loadCoursePurchase(ctx.AppDB(), purchase.ID)
	if err != nil {
		return err
	}
	if saved.PaidAt == nil {
		return nil
	}
	attribution := map[string]string{}
	var encoded string
	err = ctx.AppDB().QueryRow(`SELECT attribution_json FROM storefront_checkout_context WHERE purchase_id=?`, purchase.ID).Scan(&encoded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if encoded != "" {
		if err = json.Unmarshal([]byte(encoded), &attribution); err != nil {
			return err
		}
	}
	paid, err := parseStorefrontTimestamp(*saved.PaidAt)
	if err != nil {
		return err
	}
	captured, err := time.Parse(time.RFC3339, attribution["captured_at"])
	if err != nil || paid.Before(captured) || paid.Sub(captured) > 7*24*time.Hour {
		attribution = map[string]string{}
	}
	emit(ctx, "course.customer_acquired", map[string]any{"community_id": purchase.CommunityID, "customer_ref": storefrontCustomerRef(purchase.CommunityID, purchase.CustomerEmail), "purchase_id": purchase.ID, "invoice_id": purchase.BillingInvoiceID, "acquired_at": paid.UTC().Format(time.RFC3339), "amount_cents": purchase.UnitAmountCents, "currency": purchase.Currency, "campaign_id": attribution["campaign_id"], "utm_campaign": attribution["utm_campaign"], "utm_source": attribution["utm_source"], "utm_medium": attribution["utm_medium"], "adset_id": attribution["adset_id"], "ad_id": attribution["ad_id"]})
	return nil
}
