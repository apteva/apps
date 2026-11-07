package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// The same lock covers selection and invoice creation. No cart mutation can
// race the snapshot passed to Billing within an app process.
var checkoutLocks [128]sync.Mutex

func lockCheckout(id int64) func() { m := &checkoutLocks[uint64(id)%128]; m.Lock(); return m.Unlock }

type OrderBump struct {
	Available        bool   `json:"available"`
	ID               int64  `json:"id"`
	TriggerPriceID   int64  `json:"trigger_price_id"`
	TriggerProductID int64  `json:"trigger_product_id"`
	SourceApp        string `json:"source_app"`
	ContextRef       string `json:"context_ref"`
	PriceID          int64  `json:"price_id"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	ImageURL         string `json:"image_url"`
	SortOrder        int64  `json:"sort_order"`
	Active           bool   `json:"active"`
	StartsAt         string `json:"starts_at"`
	EndsAt           string `json:"ends_at"`
	FulfillmentApp   string `json:"fulfillment_app"`
	FulfillmentRef   string `json:"fulfillment_ref"`
	ProductID        int64  `json:"product_id,omitempty"`
	AmountCents      int64  `json:"amount_cents"`
	Currency         string `json:"currency"`
	Selected         bool   `json:"selected"`
	Impressions      int64  `json:"impressions,omitempty"`
	Selections       int64  `json:"selections,omitempty"`
	PaidConversions  int64  `json:"paid_conversions,omitempty"`
	RevenueCents     int64  `json:"revenue_cents,omitempty"`
}

type bumpPrice struct {
	ID              int64           `json:"id"`
	ProductID       int64           `json:"product_id"`
	UnitAmountCents int64           `json:"unit_amount_cents"`
	Currency        string          `json:"currency"`
	Active          bool            `json:"active"`
	ArchivedAt      string          `json:"archived_at"`
	Interval        string          `json:"interval"`
	BillingScheme   string          `json:"billing_scheme"`
	Metadata        json.RawMessage `json:"metadata"`
}

func fetchBumpPrice(ctx *sdk.AppCtx, pid string, id int64) (bumpPrice, error) {
	var out struct {
		Price bumpPrice `json:"price"`
	}
	if ctx.PlatformAPI() == nil {
		return out.Price, errors.New("Catalog unavailable")
	}
	err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_prices_get", map[string]any{"_project_id": pid, "id": id}, &out)
	p := out.Price
	if err != nil {
		return p, err
	}
	if p.ID != id || p.ProductID <= 0 || !p.Active || p.ArchivedAt != "" || p.UnitAmountCents <= 0 || p.UnitAmountCents > 1000000000 || p.Interval != "" || (p.BillingScheme != "" && p.BillingScheme != "flat") {
		return p, errors.New("bump requires an active, fixed, one-time Catalog price")
	}
	var product struct {
		Product struct {
			Type       string `json:"type"`
			ArchivedAt string `json:"archived_at"`
		} `json:"product"`
	}
	if err = ctx.PlatformAPI().CallAppResult("catalog", "catalog_products_get", map[string]any{"_project_id": pid, "id": p.ProductID}, &product); err != nil {
		return p, err
	}
	if product.Product.Type == "recurring" || product.Product.ArchivedAt != "" {
		return p, errors.New("bump product must be active and one-time")
	}
	return p, nil
}

func orderBumpTools(a *App) []sdk.Tool {
	s := map[string]any{"type": "string"}
	i := map[string]any{"type": "integer"}
	b := map[string]any{"type": "boolean"}
	tools := []sdk.Tool{
		{Name: "order_bumps_list", Description: "Operator: list order bumps and conversion statistics in this project.", InputSchema: schemaObject(map[string]any{}, nil), Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			pid, e := resolveProjectFromArgs(args)
			if e != nil {
				return nil, e
			}
			bumps, e := listOrderBumps(ctx, pid)
			return map[string]any{"bumps": bumps}, e
		}},
		{Name: "order_bumps_upsert", Description: "Operator: configure an opt-in one-time add-on triggered by a Catalog product/price. context_ref and source_app restrict the selling surface; fulfillment_app/ref identify the delivery owner.", InputSchema: schemaObject(map[string]any{"id": i, "trigger_price_id": i, "trigger_product_id": i, "source_app": s, "context_ref": s, "price_id": i, "title": s, "description": s, "image_url": s, "sort_order": i, "active": b, "starts_at": s, "ends_at": s, "fulfillment_app": s, "fulfillment_ref": s}, []string{"price_id", "title", "fulfillment_app", "fulfillment_ref"}), Handler: upsertOrderBump},
		{Name: "checkout_bumps_get", Description: "Return eligible order bumps and persist one impression per session/offer. Uses the server-side cart context.", InputSchema: schemaObject(map[string]any{"session_id": i}, []string{"session_id"}), Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			pid, e := resolveProjectFromArgs(args)
			if e != nil {
				return nil, e
			}
			bs, e := sessionBumps(ctx, pid, int64Arg(args, "session_id"), true)
			return map[string]any{"bumps": bs}, e
		}},
		{Name: "checkout_bump_set", Description: "Select/unselect an eligible bump (quantity one) on a started checkout. Returns authoritative totals; never accepts a browser price or amount.", InputSchema: schemaObject(map[string]any{"session_id": i, "bump_id": i, "selected": b}, []string{"session_id", "bump_id", "selected"}), Handler: setCheckoutBump},
		{Name: "checkout_receipt", Description: "Read item-level fulfillment and refund state from an authoritative Billing invoice. Consumers fulfill only paid items and deduplicate by session_id/item_id.", InputSchema: schemaObject(map[string]any{"session_id": i}, []string{"session_id"}), Handler: checkoutReceipt},
		{Name: "checkout_item_refund", Description: "Operator: refund only the remaining balance of one checkout item. Keeps refund attribution and reuses a durable Billing idempotency key.", InputSchema: schemaObject(map[string]any{"session_id": i, "item_id": i, "amount_cents": i, "idempotency_key": s}, []string{"session_id", "item_id", "idempotency_key"}), Handler: refundCheckoutItem},
	}
	for n := range tools {
		handler := tools[n].Handler
		tools[n].HandlerCtx = func(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
			if caller := sdk.CallerFrom(callCtx); caller != nil && caller.SubjectType == "user" {
				return nil, errors.New("use the selling app's scoped buyer checkout API")
			}
			return handler(ctx, args)
		}
	}
	return tools
}

func listOrderBumps(ctx *sdk.AppCtx, pid string) ([]OrderBump, error) {
	rows, e := ctx.AppDB().Query(`SELECT id,trigger_price_id,trigger_product_id,source_app,context_ref,price_id,title,description,image_url,sort_order,active,starts_at,ends_at,fulfillment_app,fulfillment_ref FROM order_bumps WHERE project_id=? ORDER BY sort_order,id`, pid)
	if e != nil {
		return nil, e
	}
	bs := []OrderBump{}
	for rows.Next() {
		var b OrderBump
		if e = rows.Scan(&b.ID, &b.TriggerPriceID, &b.TriggerProductID, &b.SourceApp, &b.ContextRef, &b.PriceID, &b.Title, &b.Description, &b.ImageURL, &b.SortOrder, &b.Active, &b.StartsAt, &b.EndsAt, &b.FulfillmentApp, &b.FulfillmentRef); e != nil {
			rows.Close()
			return nil, e
		}
		bs = append(bs, b)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for n := range bs {
		b := &bs[n]
		if e = ctx.AppDB().QueryRow(`SELECT COUNT(*),COALESCE(SUM(selected_once),0) FROM checkout_bump_views v JOIN checkout_sessions s ON s.id=v.session_id WHERE v.bump_id=? AND s.project_id=?`, b.ID, pid).Scan(&b.Impressions, &b.Selections); e != nil {
			return nil, e
		}
		if e = ctx.AppDB().QueryRow(`SELECT COUNT(*),COALESCE(SUM(i.unit_amount_cents*i.quantity),0) FROM cart_items i JOIN checkout_sessions s ON s.cart_id=i.cart_id WHERE s.project_id=? AND s.status='paid' AND json_extract(i.metadata,'$.order_bump_id')=?`, pid, b.ID).Scan(&b.PaidConversions, &b.RevenueCents); e != nil {
			return nil, e
		}
	}
	return bs, nil
}
func upsertOrderBump(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, e := resolveProjectFromArgs(args)
	if e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(args)
	var b OrderBump
	if e = json.Unmarshal(raw, &b); e != nil {
		return nil, e
	}
	if args["active"] == nil {
		b.Active = true
	}
	b.Title = strings.TrimSpace(b.Title)
	b.FulfillmentApp = strings.TrimSpace(b.FulfillmentApp)
	b.FulfillmentRef = strings.TrimSpace(b.FulfillmentRef)
	if (b.TriggerPriceID <= 0 && b.TriggerProductID <= 0) || b.Title == "" || len(b.Title) > 160 || len(b.Description) > 2000 || b.FulfillmentApp == "" || b.FulfillmentRef == "" || len(b.FulfillmentRef) > 300 || len(b.ContextRef) > 300 || len(b.SourceApp) > 100 {
		return nil, errors.New("trigger, title and fulfillment owner/reference required (within size limits)")
	}
	for _, v := range []string{b.StartsAt, b.EndsAt} {
		if v != "" {
			if _, e = time.Parse(time.RFC3339, v); e != nil {
				return nil, e
			}
		}
	}
	if b.StartsAt != "" && b.EndsAt != "" {
		start, _ := time.Parse(time.RFC3339, b.StartsAt)
		end, _ := time.Parse(time.RFC3339, b.EndsAt)
		if !end.After(start) {
			return nil, errors.New("ends_at must follow starts_at")
		}
	}
	if b.ImageURL != "" {
		u, e := url.Parse(b.ImageURL)
		if e != nil || u.Scheme != "https" || u.Host == "" {
			return nil, errors.New("image_url must use HTTPS")
		}
	}
	if _, e = fetchBumpPrice(ctx, pid, b.PriceID); e != nil {
		return nil, e
	}
	values := []any{b.TriggerPriceID, b.TriggerProductID, b.SourceApp, b.ContextRef, b.PriceID, b.Title, b.Description, b.ImageURL, b.SortOrder, b.Active, b.StartsAt, b.EndsAt, b.FulfillmentApp, b.FulfillmentRef}
	if b.ID == 0 {
		res, e := ctx.AppDB().Exec(`INSERT INTO order_bumps(trigger_price_id,trigger_product_id,source_app,context_ref,price_id,title,description,image_url,sort_order,active,starts_at,ends_at,fulfillment_app,fulfillment_ref,project_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, append(values, pid)...)
		if e != nil {
			return nil, e
		}
		b.ID, _ = res.LastInsertId()
	} else {
		res, e := ctx.AppDB().Exec(`UPDATE order_bumps SET trigger_price_id=?,trigger_product_id=?,source_app=?,context_ref=?,price_id=?,title=?,description=?,image_url=?,sort_order=?,active=?,starts_at=?,ends_at=?,fulfillment_app=?,fulfillment_ref=? WHERE id=? AND project_id=?`, append(values, b.ID, pid)...)
		if e != nil {
			return nil, e
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, errors.New("bump not found")
		}
	}
	ctx.Emit("order_bump.updated", map[string]any{"bump_id": b.ID})
	return map[string]any{"bump": b}, nil
}

func sessionBumps(ctx *sdk.AppCtx, pid string, id int64, impression bool) ([]OrderBump, error) {
	s, e := dbCheckoutGet(ctx.AppDB(), pid, id)
	if e != nil || s == nil {
		return nil, firstCheckoutErr(e, errors.New("session not found"))
	}
	cart, e := dbCartGetByID(ctx.AppDB(), pid, s.CartID)
	if e != nil || cart == nil {
		return nil, firstCheckoutErr(e, errors.New("cart not found"))
	}
	if s.Status != "started" {
		frozen := []OrderBump{}
		for _, it := range cart.Items {
			meta := mapFromAny(it.Metadata)
			bid := int64Arg(meta, "order_bump_id")
			if bid == 0 {
				continue
			}
			frozen = append(frozen, OrderBump{ID: bid, PriceID: it.PriceID, ProductID: it.ProductID, Title: it.Description, AmountCents: it.UnitAmountCents, Currency: it.Currency, Selected: true, Available: true, FulfillmentApp: strArg(meta, "fulfillment_app"), FulfillmentRef: strArg(meta, "fulfillment_ref")})
		}
		return frozen, nil
	}
	bs, e := listOrderBumps(ctx, pid)
	if e != nil {
		return nil, e
	}
	out := []OrderBump{}
	for _, b := range bs {
		if !b.Active {
			continue
		}
		now := time.Now()
		if b.StartsAt != "" {
			t, _ := time.Parse(time.RFC3339, b.StartsAt)
			if now.Before(t) {
				continue
			}
		}
		if b.EndsAt != "" {
			t, _ := time.Parse(time.RFC3339, b.EndsAt)
			if !now.Before(t) {
				continue
			}
		}
		meta := mapFromAny(cart.Metadata)
		if b.SourceApp != "" && b.SourceApp != strArg(meta, "source_app") {
			continue
		}
		if b.ContextRef != "" && b.ContextRef != strArg(meta, "context_ref") {
			continue
		}
		matches := false
		for _, it := range cart.Items {
			if int64Arg(mapFromAny(it.Metadata), "order_bump_id") != 0 {
				continue
			}
			if (b.TriggerPriceID == 0 || b.TriggerPriceID == it.PriceID) && (b.TriggerProductID == 0 || b.TriggerProductID == it.ProductID) {
				matches = true
			}
		}
		if !matches {
			continue
		}
		for _, it := range cart.Items {
			if int64Arg(mapFromAny(it.Metadata), "order_bump_id") == b.ID {
				b.Selected = true
			}
		}
		p, err := fetchBumpPrice(ctx, pid, b.PriceID)
		if err != nil {
			if b.Selected {
				continue
			}
			return nil, fmt.Errorf("order bump %d: %w", b.ID, err)
		}
		if !strings.EqualFold(p.Currency, cart.Currency) {
			continue
		}
		duplicate := false
		for _, it := range cart.Items {
			if it.ProductID == p.ProductID {
				if int64Arg(mapFromAny(it.Metadata), "order_bump_id") == b.ID {
					b.Selected = true
				} else {
					duplicate = true
				}
			}
		}
		if duplicate {
			continue
		}
		b.Available = true
		b.AmountCents = p.UnitAmountCents
		b.Currency = p.Currency
		b.ProductID = p.ProductID
		if impression {
			if _, e = ctx.AppDB().Exec(`INSERT OR IGNORE INTO checkout_bump_views(session_id,bump_id) VALUES(?,?)`, s.ID, b.ID); e != nil {
				return nil, e
			}
		}
		out = append(out, b)
	}

	for _, it := range cart.Items {
		bid := int64Arg(mapFromAny(it.Metadata), "order_bump_id")
		if bid == 0 {
			continue
		}
		found := false
		for _, b := range out {
			if b.ID == bid {
				found = true
			}
		}
		if !found {
			out = append(out, OrderBump{ID: bid, PriceID: it.PriceID, ProductID: it.ProductID, Title: it.Description, Description: "This offer is no longer available. Remove it to continue.", AmountCents: it.UnitAmountCents, Currency: it.Currency, Selected: true, FulfillmentApp: strArg(mapFromAny(it.Metadata), "fulfillment_app"), FulfillmentRef: strArg(mapFromAny(it.Metadata), "fulfillment_ref")})
		}
	}
	return out, nil
}

func validateSelectedBumps(ctx *sdk.AppCtx, pid string, s *CheckoutSession) error {
	bs, e := sessionBumps(ctx, pid, s.ID, false)
	if e != nil {
		return e
	}
	cart, e := dbCartGetByID(ctx.AppDB(), pid, s.CartID)
	if e != nil {
		return e
	}
	for _, it := range cart.Items {
		id := int64Arg(mapFromAny(it.Metadata), "order_bump_id")
		if id == 0 {
			continue
		}
		valid := false
		for _, b := range bs {
			if b.ID == id && b.Available && b.Selected && b.FulfillmentApp == strArg(mapFromAny(it.Metadata), "fulfillment_app") && b.FulfillmentRef == strArg(mapFromAny(it.Metadata), "fulfillment_ref") && b.PriceID == it.PriceID && b.ProductID == it.ProductID && b.AmountCents == it.UnitAmountCents && it.Quantity == 1 && strings.EqualFold(b.Currency, it.Currency) {
				valid = true
			}
		}
		if !valid {
			return errors.New("a selected bump is no longer available; remove it before paying")
		}
	}
	return nil
}

func setCheckoutBump(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, e := resolveProjectFromArgs(args)
	if e != nil {
		return nil, e
	}
	id := int64Arg(args, "session_id")
	bid := int64Arg(args, "bump_id")
	selected, ok := args["selected"].(bool)
	if !ok || id <= 0 || bid <= 0 {
		return nil, errors.New("session_id, bump_id and selected required")
	}
	unlock := lockCheckout(id)
	defer unlock()
	s, e := dbCheckoutGet(ctx.AppDB(), pid, id)
	if e != nil || s == nil {
		return nil, firstCheckoutErr(e, errors.New("session not found"))
	}
	if s.Status != "started" || s.InvoiceID != nil {
		return nil, errors.New("selections are locked after payment starts")
	}
	if s.ExpiresAt != "" {
		t, _ := time.Parse(time.RFC3339, s.ExpiresAt)
		if !t.IsZero() && time.Now().After(t) {
			return nil, errors.New("checkout expired")
		}
	}
	var b OrderBump
	if selected {
		bs, e := sessionBumps(ctx, pid, id, false)
		if e != nil {
			return nil, e
		}
		for _, candidate := range bs {
			if candidate.ID == bid && candidate.Available {
				b = candidate
			}
		}
		if b.ID == 0 {
			return nil, errors.New("bump is not eligible for this checkout")
		}
	}
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if selected {
		p, e := fetchBumpPrice(ctx, pid, b.PriceID)
		if e != nil {
			return nil, e
		}
		meta := mapFromAny(p.Metadata)
		meta["order_bump_id"] = bid
		meta["fulfillment_app"] = b.FulfillmentApp
		meta["fulfillment_ref"] = b.FulfillmentRef
		meta["billing_scheme"] = "flat"
		if _, e = tx.Exec(`INSERT INTO cart_items(cart_id,price_id,product_id,description,unit_amount_cents,currency,quantity,metadata) VALUES(?,?,?,?,?,?,1,?) ON CONFLICT(cart_id,price_id) DO NOTHING`, s.CartID, b.PriceID, b.ProductID, b.Title, b.AmountCents, b.Currency, jsonOrEmpty(meta, "{}")); e != nil {
			return nil, e
		}
		if _, e = tx.Exec(`INSERT INTO checkout_bump_views(session_id,bump_id,selected_once) VALUES(?,?,1) ON CONFLICT(session_id,bump_id) DO UPDATE SET selected_once=1`, id, bid); e != nil {
			return nil, e
		}
	} else {
		if _, e = tx.Exec(`DELETE FROM cart_items WHERE cart_id=? AND json_extract(metadata,'$.order_bump_id')=?`, s.CartID, bid); e != nil {
			return nil, e
		}
	}
	if e = recomputeCartTotalsTx(tx, s.CartID, s.Currency); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(`UPDATE checkout_sessions SET subtotal_cents=(SELECT subtotal_cents FROM carts WHERE id=cart_id),total_cents=(SELECT subtotal_cents FROM carts WHERE id=cart_id)+shipping_cents+tax_cents-discount_cents,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='started'`, id); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	s, e = dbCheckoutGet(ctx.AppDB(), pid, id)
	if e != nil {
		return nil, e
	}
	bs, e := sessionBumps(ctx, pid, id, false)
	emitSession(ctx, "checkout.bump_changed", s)
	return map[string]any{"session": s, "bumps": bs}, e
}

// Only the admin surface exposes configuration. Buyer pages use their selling
// app's scoped proxy or authenticated tools, never an unrestricted price API.
func (a *App) httpOrderBumps(w http.ResponseWriter, r *http.Request) {
	if caller := sdk.CallerFrom(r.Context()); caller != nil && caller.SubjectType == "user" {
		httpErr(w, 403, "operator access required")
		return
	}
	pid, e := resolveProjectFromRequest(r)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	args := map[string]any{"_project_id": pid}
	if r.Method == http.MethodPost {
		if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768)).Decode(&args); e != nil {
			httpErr(w, 400, "invalid order bump")
			return
		}
		args["_project_id"] = pid
		out, e := upsertOrderBump(getAppCtx(r), args)
		if e != nil {
			httpErr(w, 400, e.Error())
			return
		}
		httpJSON(w, out)
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, 405, "method not allowed")
		return
	}
	bs, e := listOrderBumps(getAppCtx(r), pid)
	if e != nil {
		httpErr(w, 500, e.Error())
		return
	}
	httpJSON(w, map[string]any{"bumps": bs})
}
