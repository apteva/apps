package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type storefrontOrderBump struct {
	Available      bool   `json:"available"`
	ID             int64  `json:"id"`
	PriceID        int64  `json:"price_id"`
	ProductID      int64  `json:"product_id"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	ImageURL       string `json:"image_url"`
	AmountCents    int64  `json:"amount_cents"`
	Currency       string `json:"currency"`
	Selected       bool   `json:"selected"`
	FulfillmentApp string `json:"fulfillment_app"`
	FulfillmentRef string `json:"fulfillment_ref"`
}
type storefrontReceiptItem struct {
	ID            int64           `json:"id"`
	PriceID       int64           `json:"price_id"`
	ProductID     int64           `json:"product_id"`
	NetCents      int64           `json:"net_cents"`
	RefundedCents int64           `json:"refunded_cents"`
	Status        string          `json:"status"`
	Metadata      json.RawMessage `json:"metadata"`
}
type storefrontReceipt struct {
	Items                  []storefrontReceiptItem `json:"items"`
	UnallocatedRefundCents int64                   `json:"unallocated_refund_cents"`
}

func getStorefrontBumps(ctx *sdk.AppCtx, communityID string, sessionID int64) ([]storefrontOrderBump, error) {
	var out struct {
		Bumps []storefrontOrderBump `json:"bumps"`
	}
	if err := callAppResult(ctx, "checkout", "checkout_bumps_get", map[string]any{"_project_id": scopeProject(ctx), "session_id": sessionID}, &out); err != nil {
		return nil, err
	}
	saved, err := checkoutContext(ctx.AppDB(), sessionID)
	if err != nil {
		return nil, err
	}
	var primary storefrontOfferTarget
	if saved != nil {
		primary, _ = resolveStorefrontOffer(ctx.AppDB(), communityID, saved.PriceID)
	}
	result := []storefrontOrderBump{}
	for _, b := range out.Bumps {
		if b.FulfillmentApp == "community" {
			err := validateCommunityBumpCourse(ctx, communityID, b.ProductID, b.FulfillmentRef)
			if err != nil {
				if !b.Selected {
					continue
				}
				b.Available = false
			}
			if primary.Kind == "recurring" {
				included, err := membershipPlanIncludesCourse(ctx.AppDB(), primary.PlanID, b.FulfillmentRef)
				if err != nil {
					return nil, err
				}
				if included {
					if !b.Selected {
						continue
					}
					b.Available = false
					b.Description = "This course is already included in your membership. Remove the extra to continue."
				}
			}
		}
		result = append(result, b)
	}
	return result, nil
}

func (a *App) httpPortalCheckoutBump(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	community, err := publicPortalCommunity(r)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	var input struct {
		CatalogPriceID int64  `json:"catalog_price_id"`
		RecoveryToken  string `json:"recovery_token"`
		BumpID         int64  `json:"bump_id"`
		Selected       *bool  `json:"selected"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || input.Selected == nil || input.RecoveryToken == "" {
		writeErr(w, 400, "invalid bump selection")
		return
	}
	ctx := requestAppCtx(r)
	target, err := resolveStorefrontOffer(ctx.AppDB(), community.ID, input.CatalogPriceID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var restored struct {
		Session guestCheckoutSession `json:"session"`
		Cart    guestCheckoutCart    `json:"cart"`
	}
	if err = callAppResult(ctx, "checkout", "checkout_bootstrap", map[string]any{"_project_id": scopeProject(ctx), "recovery_token": input.RecoveryToken}, &restored); err != nil {
		writeErr(w, 400, "checkout could not be restored")
		return
	}
	if err = validatePreparedCheckout(community.ID, target, restored.Session, restored.Cart); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if *input.Selected {
		bs, e := getStorefrontBumps(ctx, community.ID, restored.Session.ID)
		if e != nil {
			writeErr(w, 503, "offers unavailable")
			return
		}
		found := false
		for _, b := range bs {
			if b.ID == input.BumpID && b.Available {
				found = true
			}
		}
		if !found {
			writeErr(w, 400, "offer is unavailable")
			return
		}
	}
	var result any
	if err = callAppResult(ctx, "checkout", "checkout_bump_set", map[string]any{"_project_id": scopeProject(ctx), "session_id": restored.Session.ID, "bump_id": input.BumpID, "selected": *input.Selected}, &result); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// Refresh deferred configuration without changing campaign or promotion state.
	prepared, err := prepareCheckoutWithCheckoutApp(ctx, community.ID, target, "", "", input.RecoveryToken, nil)
	if err != nil {
		writeErr(w, 503, "payment could not be refreshed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, prepared)
}

func validateStorefrontCartBumps(ctx *sdk.AppCtx, communityID string, target storefrontOfferTarget, session guestCheckoutSession, cart guestCheckoutCart) error {
	bs, err := getStorefrontBumps(ctx, communityID, session.ID)
	if err != nil {
		return err
	}
	for _, it := range cart.Items {
		if it.PriceID == target.Price {
			continue
		}
		found := false
		for _, b := range bs {
			if b.Available && b.Selected && b.ID == numberFromAny(mapJSON(it.Metadata)["order_bump_id"]) && b.PriceID == it.PriceID && b.ProductID == it.ProductID && b.AmountCents == it.UnitAmountCents && it.Quantity == 1 && strings.EqualFold(b.Currency, it.Currency) {
				found = true
			}
		}
		if !found {
			return errors.New("checkout contains an unapproved order bump")
		}
	}
	return nil
}
func mapJSON(raw json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}
func readStorefrontReceipt(ctx *sdk.AppCtx, id int64) (*storefrontReceipt, error) {
	var out storefrontReceipt
	err := callAppResult(ctx, "checkout", "checkout_receipt", map[string]any{"_project_id": scopeProject(ctx), "session_id": id}, &out)
	return &out, err
}

func registerStorefrontItems(ctx *sdk.AppCtx, communityID, memberID string, target storefrontOfferTarget, session guestCheckoutSession, cart guestCheckoutCart) error {
	if len(cart.Items) < 2 {
		return nil
	}
	if session.InvoiceID == nil {
		return errors.New("checkout has no invoice")
	}
	// Freeze the verified identity before any payment confirmation reaches Stripe.
	_, err := ctx.AppDB().Exec(`INSERT INTO community_checkout_orders(session_id,invoice_id,project_id,community_id,member_id,primary_price_id) VALUES(?,?,?,?,?,?) ON CONFLICT(session_id) DO NOTHING`, session.ID, *session.InvoiceID, scopeProject(ctx), communityID, memberID, target.Price)
	if err != nil {
		return err
	}
	var savedMember string
	if err = ctx.AppDB().QueryRow(`SELECT member_id FROM community_checkout_orders WHERE session_id=? AND invoice_id=? AND community_id=? AND project_id=?`, session.ID, *session.InvoiceID, communityID, scopeProject(ctx)).Scan(&savedMember); err != nil {
		return err
	}
	if savedMember != memberID {
		return errors.New("checkout belongs to another member")
	}
	receipt, err := readStorefrontReceipt(ctx, session.ID)
	if err != nil {
		return err
	}
	for _, it := range receipt.Items {
		if it.PriceID == target.Price || metadataString(it.Metadata, "fulfillment_app") != "community" {
			continue
		}
		spaceID := metadataString(it.Metadata, "fulfillment_ref")
		if e := validateCommunityBumpCourse(ctx, communityID, it.ProductID, spaceID); e != nil {
			return errors.New("add-on course cannot be delivered")
		}
		if _, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO community_checkout_items(session_id,item_id,space_id,price_id) VALUES(?,?,?,?)`, session.ID, it.ID, spaceID, it.PriceID); err != nil {
			return err
		}
	}
	return nil
}

// Existing course accounting sees only its own line's payment/refund balance.
func projectStorefrontInvoice(ctx *sdk.AppCtx, invoice billingInvoice) (billingInvoice, error) {
	var sessionID, priceID int64
	err := ctx.AppDB().QueryRow(`SELECT session_id,primary_price_id FROM community_checkout_orders WHERE invoice_id=? AND project_id=?`, invoice.ID, scopeProject(ctx)).Scan(&sessionID, &priceID)
	if errors.Is(err, sql.ErrNoRows) {
		return invoice, nil
	}
	if err != nil {
		return invoice, err
	}
	receipt, err := readStorefrontReceipt(ctx, sessionID)
	if err != nil {
		return invoice, err
	}
	for _, it := range receipt.Items {
		if it.PriceID != priceID {
			continue
		}
		invoice.TotalCents = it.NetCents
		invoice.AmountPaidCents = 0
		invoice.Payments = nil
		if it.Status == "paid" || it.Status == "partially_refunded" || it.Status == "refunded" {
			invoice.AmountPaidCents = it.NetCents - it.RefundedCents
			invoice.Status = "paid"
			if it.RefundedCents > 0 {
				invoice.Payments = []billingPayment{{AmountCents: -it.RefundedCents}}
			}
		}
		return invoice, nil
	}
	return invoice, errors.New("primary item missing from Checkout receipt")
}

func reconcileStorefrontItems(ctx *sdk.AppCtx, sessionID int64) error {
	var communityID, memberID string
	if err := ctx.AppDB().QueryRow(`SELECT community_id,member_id FROM community_checkout_orders WHERE session_id=? AND project_id=?`, sessionID, scopeProject(ctx)).Scan(&communityID, &memberID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	receipt, err := readStorefrontReceipt(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, it := range receipt.Items {
		var spaceID string
		if err = ctx.AppDB().QueryRow(`SELECT space_id FROM community_checkout_items WHERE session_id=? AND item_id=?`, sessionID, it.ID).Scan(&spaceID); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return err
		}
		ref := fmt.Sprintf("%d:%d", sessionID, it.ID)
		if it.Status == "paid" || it.Status == "partially_refunded" {
			// Preserve independent manual/purchase grants. Membership-derived access can
			// be replaced by this permanent one-time purchase, so later cancellation is safe.
			_, err = ctx.AppDB().Exec(`INSERT INTO course_enrollments(space_id,member_id,status,source,source_ref) VALUES(?,?,'active','checkout_item',?) ON CONFLICT(space_id,member_id) DO UPDATE SET status=CASE WHEN course_enrollments.status='completed' THEN 'completed' ELSE 'active' END,access_revoked_at=NULL,source='checkout_item',source_ref=excluded.source_ref WHERE course_enrollments.source='checkout_item' AND course_enrollments.source_ref=excluded.source_ref OR course_enrollments.status='cancelled' OR course_enrollments.source='membership'`, spaceID, memberID, ref)
		} else if it.Status == "refunded" {
			_, err = ctx.AppDB().Exec(`UPDATE course_enrollments SET status='cancelled',access_revoked_at=CURRENT_TIMESTAMP WHERE space_id=? AND member_id=? AND source='checkout_item' AND source_ref=?`, spaceID, memberID, ref)
		}
		if err != nil {
			return err
		}
	}
	emit(ctx, "checkout.items_updated", map[string]any{"community_id": communityID, "member_id": memberID, "session_id": sessionID, "unallocated_refund_cents": receipt.UnallocatedRefundCents})
	return nil
}
func (a *App) handleStorefrontCheckoutEvent(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "checkout" {
		return nil
	}
	id := numberFromAny(event.Data["session_id"])
	if id <= 0 {
		return nil
	}
	if event.ProjectID != "" {
		ctx = ctx.WithProject(event.ProjectID)
	}
	var invoiceID int64
	if err := ctx.AppDB().QueryRow(`SELECT invoice_id FROM community_checkout_orders WHERE session_id=? AND project_id=?`, id, scopeProject(ctx)).Scan(&invoiceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if purchase, err := loadCoursePurchaseByInvoice(ctx.AppDB(), scopeProject(ctx), invoiceID); err == nil {
		name := "invoice.paid"
		if event.Name() == "checkout.refunded" {
			name = "invoice.refunded"
		}
		if _, err = reconcileCoursePurchase(ctx, purchase, name, "billing", event.Data); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return reconcileStorefrontItems(ctx, id)
}

func storefrontBumpTotal(cart guestCheckoutCart, primary int64) int64 {
	var n int64
	for _, it := range cart.Items {
		if it.PriceID != primary {
			n += it.UnitAmountCents
		}
	}
	return n
}

func membershipPlanIncludesCourse(db *sql.DB, planID, spaceID string) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM membership_plans p JOIN spaces s ON s.id=? AND s.community_id=p.community_id LEFT JOIN course_details cm ON cm.space_id=s.id WHERE p.id=? AND (p.scope_type='all_courses' OR (p.scope_type='selected_courses' AND EXISTS(SELECT 1 FROM membership_plan_courses pc WHERE pc.plan_id=p.id AND pc.space_id=s.id)) OR (p.scope_type='course_tags' AND EXISTS(SELECT 1 FROM membership_plan_tags pt,json_each(COALESCE(cm.tags_json,'[]')) jt WHERE pt.plan_id=p.id AND lower(pt.tag)=lower(jt.value))))`, spaceID, planID).Scan(&count)
	return count > 0, err
}

func checkoutBuyerMetadata(raw json.RawMessage, memberID, subjectID string) map[string]any {
	m := mapJSON(raw)
	if m == nil {
		m = map[string]any{}
	}
	m["community_member_id"] = memberID
	m["auth_subject_id"] = subjectID
	return m
}

// A special bump price can differ from the normal course price, but it must
// reference the same Catalog product and an active paid course in this scope.
func validateCommunityBumpCourse(ctx *sdk.AppCtx, communityID string, productID int64, spaceID string) error {
	var n int
	err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM spaces s JOIN communities c ON c.id=s.community_id JOIN course_offers o ON o.space_id=s.id WHERE s.id=? AND c.id=? AND c.project_id=? AND s.kind='course' AND s.archived_at IS NULL AND c.archived_at IS NULL AND o.active=1 AND o.archived_at IS NULL AND o.catalog_product_id=?`, spaceID, communityID, scopeProject(ctx), productID).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("add-on must deliver an active course with the same Catalog product in this community")
	}
	return nil
}
