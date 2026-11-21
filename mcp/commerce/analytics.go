package main

import (
	"context"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/url"
	"strings"
	"time"
)

// Guest actors identify server-owned carts, not authenticated people. Nothing
// supplied by the browser can select an Analytics user, alias, sale or invoice.
func analyticsEnabled(s *Store) bool {
	return s != nil && boolArg(mapArg(s.Metadata, "analytics"), "enabled")
}
func cleanCartAttribution(in map[string]any) (map[string]any, error) {
	if strArg(in, "consent") != "granted" {
		return map[string]any{"consent": "denied"}, nil
	}
	visitor := strings.TrimSpace(strArg(in, "visitor_id"))
	if visitor == "" || len(visitor) > 128 {
		return nil, errors.New("valid visitor_id required")
	}
	for _, c := range visitor {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return nil, errors.New("invalid visitor_id")
		}
	}
	out := map[string]any{"consent": "granted", "visitor_id": visitor}
	for _, k := range []string{"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term", "first_utm_source", "first_utm_medium", "first_utm_campaign", "first_utm_content", "first_utm_term", "campaign_id", "adset_id", "ad_id", "experiment_key", "qa_run"} {
		v := strings.TrimSpace(strArg(in, k))
		if len(v) > 128 {
			return nil, fmt.Errorf("attribution %s too long", k)
		}
		if v != "" {
			out[k] = v
		}
	}
	for _, k := range []string{"touch_landing", "first_landing", "touch_referrer", "first_referrer"} {
		v := strArg(in, k)
		u, e := url.Parse(v)
		if e != nil || len(v) > 2048 {
			continue
		}
		if u.IsAbs() {
			if u.Scheme != "https" && u.Scheme != "http" {
				continue
			}
			u.User = nil
			u.RawQuery = ""
			u.Fragment = ""
			out[k] = u.String()
		} else if strings.HasPrefix(u.Path, "/") {
			out[k] = u.Path
		}
	}
	out["test_submission"] = boolArg(in, "test_submission")
	return out, nil
}
func (a *App) toolCartAttributionSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, e := requireProject(ctx, args)
	if e != nil {
		return nil, e
	}
	var cart *Cart
	if intArg(args, "cart_id") == 0 && strArg(args, "session_token") != "" && intArg(args, "store_id") != 0 {
		cart, e = dbCartGetBySession(ctx.AppDB(), pid, intArg(args, "store_id"), strArg(args, "session_token"))
	} else {
		cart, e = resolveCart(ctx.AppDB(), pid, args)
	}
	if e != nil {
		return nil, e
	}
	if cart == nil {
		return map[string]any{"cart": nil, "analytics_identity_linked": false}, nil
	}
	store, e := dbStoreGetByID(ctx.AppDB(), pid, cart.StoreID)
	if e != nil {
		return nil, e
	}
	if !analyticsEnabled(store) {
		return map[string]any{"cart": cart, "analytics_enabled": false}, nil
	}
	attr, e := cleanCartAttribution(mapArg(args, "attribution"))
	if e != nil {
		return nil, e
	}
	if cart.Status == "converted" && strArg(attr, "consent") == "granted" {
		return map[string]any{"cart": cart}, nil
	}
	meta := copyMap(cart.Metadata)
	meta["analytics_attribution"] = attr
	_, e = ctx.AppDB().Exec(`UPDATE commerce_carts SET metadata_json=? WHERE project_id=? AND id=?`, jsonText(meta, "{}"), pid, cart.ID)
	if e != nil {
		return nil, e
	}
	cart, e = dbCartGet(ctx.AppDB(), pid, cart.ID, true)
	linked := false
	// The identity is a server-owned cart actor, never a browser-supplied user.
	// Linking is optional: paid deliveries retry identity if Analytics is offline.
	if e == nil && strArg(attr, "consent") == "granted" && ctx.PlatformAPI() != nil {
		var result map[string]any
		linkErr := ctx.PlatformAPI().CallAppResult("analytics", "analytics_identify", map[string]any{
			"user_id":    fmt.Sprintf("commerce:store:%d:cart:%d", store.ID, cart.ID),
			"visitor_id": strArg(attr, "visitor_id"),
		}, &result)
		linked = linkErr == nil
	}
	return map[string]any{"cart": cart, "analytics_identity_linked": linked}, e
}

type analyticsDelivery struct {
	SaleID   int64
	Payload  map[string]any
	Attempts int64
}

func paidAnalyticsPayload(ctx *sdk.AppCtx, store *Store, sale *Sale) (map[string]any, error) {
	if sale == nil || sale.InvoiceID == nil || sale.PaidAt == "" || (sale.PaymentStatus != "paid" && sale.PaymentStatus != "partially_refunded" && sale.PaymentStatus != "refunded") {
		return nil, nil
	}
	props := map[string]any{"store_id": store.ID, "store_key": store.Slug, "sale_id": sale.ID, "checkout_id": ptrValue(sale.CheckoutID), "invoice_id": ptrValue(sale.InvoiceID), "order_id": ptrValue(sale.OrderID), "total_cents": sale.TotalCents, "currency": sale.Currency, "evidence": "commerce_verified_paid_invoice", "test_submission": false}
	p := map[string]any{"app": "commerce", "event": "purchase_confirmed", "delivery_id": fmt.Sprintf("commerce:store:%d:sale:%d:paid:v1", store.ID, sale.ID), "props": props}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if ts, e := time.Parse(layout, sale.PaidAt); e == nil {
			p["ts"] = ts.UnixMilli()
			break
		}
	}
	if p["ts"] == nil {
		return nil, errors.New("invalid paid_at for Analytics")
	}
	cart, e := dbCartGet(ctx.AppDB(), ctx.CurrentProject(), ptrValue(sale.CartID), true)
	if e != nil {
		return nil, e
	}
	props["attribution_status"] = "no_consented_visitor"
	if cart != nil {
		attr := mapArg(cart.Metadata, "analytics_attribution")
		if strArg(attr, "consent") == "granted" && strArg(attr, "visitor_id") != "" {
			actor := fmt.Sprintf("commerce:store:%d:cart:%d", store.ID, cart.ID)
			p["user_id"] = actor
			p["identity"] = map[string]any{"user_id": actor, "visitor_id": strArg(attr, "visitor_id"), "aliases": []string{fmt.Sprintf("commerce:store:%d:sale:%d", store.ID, sale.ID), fmt.Sprintf("billing:invoice:%d", ptrValue(sale.InvoiceID))}}
			for k, v := range attr {
				if k != "consent" && k != "visitor_id" {
					props[k] = v
				}
			}
			props["attribution_status"] = "consented_cart_link"
		}
	}
	return p, nil
}

// Persist requests before delivery; retries reuse the exact body and delivery
// ID. Scanning paid rows also survives a closed browser or callback retry.
func (a *App) reconcileSaleAnalytics(_ context.Context, ctx *sdk.AppCtx) error {
	pid := ctx.CurrentProject()
	if pid == "" || ctx.PlatformAPI() == nil {
		return nil
	}
	rows, e := ctx.AppDB().Query(`SELECT s.id FROM commerce_sales s JOIN commerce_stores st ON st.id=s.store_id AND st.project_id=s.project_id WHERE s.project_id=? AND s.paid_at IS NOT NULL AND s.invoice_id IS NOT NULL AND s.payment_status IN ('paid','partially_refunded','refunded') AND json_extract(st.metadata_json,'$.analytics.enabled')=1 AND datetime(s.paid_at)>=datetime(json_extract(st.metadata_json,'$.analytics.enabled_at')) AND NOT EXISTS(SELECT 1 FROM commerce_analytics_deliveries d WHERE d.project_id=s.project_id AND d.sale_id=s.id) ORDER BY s.id LIMIT 100`, pid)
	if e != nil {
		return e
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		sale, err := dbSaleGet(ctx.AppDB(), pid, id)
		if err != nil {
			return err
		}
		store, err := dbStoreGetByID(ctx.AppDB(), pid, sale.StoreID)
		if err != nil {
			return err
		}
		p, err := paidAnalyticsPayload(ctx, store, sale)
		if err != nil {
			return err
		}
		if p == nil {
			continue
		}
		if _, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO commerce_analytics_deliveries(project_id,sale_id,payload_json) VALUES(?,?,?)`, pid, id, jsonText(p, "{}")); err != nil {
			return err
		}
	}
	rows, e = ctx.AppDB().Query(`SELECT d.sale_id,d.payload_json,d.attempts FROM commerce_analytics_deliveries d JOIN commerce_sales s ON s.id=d.sale_id AND s.project_id=d.project_id JOIN commerce_stores st ON st.id=s.store_id AND st.project_id=s.project_id WHERE d.project_id=? AND d.delivered_at IS NULL AND datetime(d.next_attempt_at)<=CURRENT_TIMESTAMP AND json_extract(st.metadata_json,'$.analytics.enabled')=1 ORDER BY d.sale_id LIMIT 100`, pid)
	if e != nil {
		return e
	}
	var pending []analyticsDelivery
	for rows.Next() {
		var d analyticsDelivery
		var body string
		if e = rows.Scan(&d.SaleID, &body, &d.Attempts); e != nil {
			rows.Close()
			return e
		}
		d.Payload = jsonMap(body)
		pending = append(pending, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, d := range pending {
		p := copyMap(d.Payload)
		identity := mapArg(p, "identity")
		delete(p, "identity")
		var result map[string]any
		var err error
		if len(identity) > 0 {
			err = ctx.PlatformAPI().CallAppResult("analytics", "analytics_identify", identity, &result)
		}
		if err == nil {
			err = ctx.PlatformAPI().CallAppResult("analytics", "analytics_track", p, &result)
		}
		if err != nil {
			delay := int64(60) << min(d.Attempts, int64(6))
			if delay > 3600 {
				delay = 3600
			}
			msg := err.Error()
			if len(msg) > 500 {
				msg = msg[:500]
			}
			_, e = ctx.AppDB().Exec(`UPDATE commerce_analytics_deliveries SET attempts=attempts+1,last_error=?,next_attempt_at=datetime('now',?) WHERE project_id=? AND sale_id=?`, msg, fmt.Sprintf("+%d seconds", delay), pid, d.SaleID)
		} else {
			_, e = ctx.AppDB().Exec(`UPDATE commerce_analytics_deliveries SET attempts=attempts+1,last_error='',delivered_at=CURRENT_TIMESTAMP WHERE project_id=? AND sale_id=?`, pid, d.SaleID)
		}
		if e != nil {
			return e
		}
	}
	return nil
}
