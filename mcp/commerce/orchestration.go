package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func requireProject(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	if ctx != nil {
		if pid := strings.TrimSpace(ctx.CurrentProject()); pid != "" {
			return pid, nil
		}
	}
	if pid := strings.TrimSpace(strArg(args, "_project_id")); pid != "" {
		return pid, nil
	}
	return "", errors.New("project_id required")
}

func (a *App) catalogPriceGet(ctx *sdk.AppCtx, pid string, id int64) (*catalogPrice, error) {
	if id == 0 {
		return nil, errors.New("catalog_price_id required")
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	var response map[string]any
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_prices_get", map[string]any{
		"_project_id": pid,
		"id":          id,
	}, &response); err != nil {
		return nil, fmt.Errorf("catalog price %d lookup: %w", id, err)
	}
	raw := response
	if nested := unwrap(response, "price"); nested != nil {
		raw = nested
	}
	price := &catalogPrice{
		ID:              intArg(raw, "id"),
		ProductID:       intArg(raw, "product_id"),
		UnitAmountCents: intArg(raw, "unit_amount_cents"),
		Currency:        strings.ToUpper(strArg(raw, "currency")),
		Active:          boolArg(raw, "active"),
		ArchivedAt:      strArg(raw, "archived_at"),
	}
	if price.ID == 0 || price.UnitAmountCents <= 0 || price.Currency == "" {
		return nil, fmt.Errorf("catalog price %d returned an invalid snapshot", id)
	}
	if !price.Active || price.ArchivedAt != "" {
		return nil, fmt.Errorf("catalog price %d is inactive or archived", id)
	}
	return price, nil
}

func (a *App) catalogPriceCreate(ctx *sdk.AppCtx, pid string, listing *Listing, args map[string]any) (*catalogPrice, error) {
	if listing == nil || listing.CatalogProductID == nil || *listing.CatalogProductID == 0 {
		return nil, errors.New("listing must reference a Catalog product before creating a price")
	}
	amount := intArg(args, "price_cents")
	if amount <= 0 {
		return nil, errors.New("price_cents must be positive")
	}
	currency := strings.ToUpper(firstNonEmpty(strArg(args, "currency"), "USD"))
	if !looksLikeCurrency(currency) {
		return nil, errors.New("currency must be a 3-letter ISO code")
	}
	var response map[string]any
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_prices_create", map[string]any{
		"_project_id":       pid,
		"product_id":        *listing.CatalogProductID,
		"unit_amount_cents": amount,
		"currency":          currency,
		"nickname":          firstNonEmpty(strArg(args, "title"), listing.Title),
		"tax_inclusive":     boolArg(args, "tax_inclusive"),
		"metadata":          map[string]any{"source": "commerce", "store_id": listing.StoreID, "listing_id": listing.ID},
	}, &response); err != nil {
		return nil, fmt.Errorf("create catalog price: %w", err)
	}
	priceID := intArg(unwrap(response, "price"), "id")
	if priceID == 0 {
		priceID = intArg(response, "id")
	}
	return a.catalogPriceGet(ctx, pid, priceID)
}

func (a *App) canonicalVariantArgs(ctx *sdk.AppCtx, pid string, listing *Listing, args map[string]any) (map[string]any, error) {
	out := copyMap(args)
	priceID := intArg(out, "catalog_price_id")
	var price *catalogPrice
	var err error
	if priceID != 0 {
		price, err = a.catalogPriceGet(ctx, pid, priceID)
	} else if intArg(out, "price_cents") != 0 {
		price, err = a.catalogPriceCreate(ctx, pid, listing, out)
	} else {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if listing.CatalogProductID == nil || *listing.CatalogProductID != price.ProductID {
		return nil, errors.New("catalog price must belong to the listing's Catalog product")
	}
	if hasKey(out, "price_cents") && intArg(out, "price_cents") != 0 && intArg(out, "price_cents") != price.UnitAmountCents {
		return nil, errors.New("price_cents does not match the canonical Catalog price")
	}
	if requested := strings.ToUpper(strArg(out, "currency")); requested != "" && requested != price.Currency {
		return nil, errors.New("currency does not match the canonical Catalog price")
	}
	out["catalog_price_id"] = price.ID
	out["price_cents"] = price.UnitAmountCents
	out["currency"] = price.Currency
	return out, nil
}

func checkoutCartItem(response map[string]any, priceID int64) (int64, float64) {
	cart := unwrap(response, "cart")
	items, _ := cart["items"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if intArg(item, "price_id") == priceID {
			return intArg(item, "id"), floatArg(item, "quantity", 0)
		}
	}
	return 0, 0
}

func (a *App) releaseReservations(ctx *sdk.AppCtx, pid string, ids []int64) error {
	var failures []string
	for _, id := range ids {
		if id == 0 {
			continue
		}
		var response map[string]any
		if err := ctx.PlatformAPI().CallAppResult("inventory", "inventory_release_reservation", map[string]any{
			"_project_id":    pid,
			"reservation_id": id,
		}, &response); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "active reservation not found") &&
			!strings.Contains(strings.ToLower(err.Error()), "already expired") &&
			!strings.Contains(strings.ToLower(err.Error()), "already released") {
			failures = append(failures, fmt.Sprintf("%d: %v", id, err))
		}
	}
	if len(failures) > 0 {
		return errors.New("release reservations: " + strings.Join(failures, "; "))
	}
	return nil
}

func (a *App) reserveCartDiscount(ctx *sdk.AppCtx, pid string, cart *Cart) (string, error) {
	if cart == nil {
		return "", nil
	}
	quote := mapArg(cart.Metadata, "checkout_quote")
	discount := mapArg(quote, "discount")
	request := copyMap(mapArg(quote, "discount_request"))
	if !boolArg(discount, "eligible") || len(request) == 0 || intArg(discount, "discount_cents") == 0 {
		return "", nil
	}
	request["_project_id"] = pid
	request["idempotency_key"] = fmt.Sprintf("commerce-cart-%s-%d-discount-v1", pid, cart.ID)
	request["expires_in_seconds"] = int64(checkoutQuoteTTL / time.Second)
	var response map[string]any
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_discounts_reserve", request, &response); err != nil {
		return "", fmt.Errorf("reserve Catalog discount: %w", err)
	}
	reservationID := strArg(unwrap(response, "reservation"), "reservation_id")
	if reservationID == "" {
		return "", errors.New("Catalog discount reservation response missing reservation_id")
	}
	return reservationID, nil
}

func (a *App) releaseDiscountReservation(ctx *sdk.AppCtx, pid, reservationID string) error {
	if strings.TrimSpace(reservationID) == "" {
		return nil
	}
	var response map[string]any
	err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_discounts_release", map[string]any{
		"_project_id": pid, "reservation_id": reservationID,
	}, &response)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "redeemed") &&
		!strings.Contains(strings.ToLower(err.Error()), "released") {
		return fmt.Errorf("release Catalog discount: %w", err)
	}
	return nil
}

func (a *App) redeemDiscountReservation(ctx *sdk.AppCtx, pid, reservationID string) error {
	if strings.TrimSpace(reservationID) == "" {
		return nil
	}
	var response map[string]any
	err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_discounts_redeem", map[string]any{
		"_project_id": pid, "reservation_id": reservationID,
	}, &response)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "redeemed") {
		return fmt.Errorf("redeem Catalog discount: %w", err)
	}
	return nil
}

func setCheckoutItemQuantity(ctx *sdk.AppCtx, pid string, cartID, itemID int64, quantity float64) error {
	var response map[string]any
	return ctx.PlatformAPI().CallAppResult("checkout", "cart_set_quantity", map[string]any{
		"_project_id": pid, "cart_id": cartID, "item_id": itemID, "quantity": quantity,
	}, &response)
}

func (a *App) cancelCheckoutAndRelease(ctx *sdk.AppCtx, pid string, checkoutSessionID int64, reservationIDs []int64) error {
	var failures []string
	var response map[string]any
	if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_cancel", map[string]any{
		"_project_id": pid, "session_id": checkoutSessionID,
	}, &response); err != nil {
		failures = append(failures, "cancel Checkout: "+err.Error())
	}
	if err := a.releaseReservations(ctx, pid, reservationIDs); err != nil {
		failures = append(failures, err.Error())
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (a *App) reserveCartItem(ctx *sdk.AppCtx, pid string, cart *Cart, item *CartItem) (int64, error) {
	ref := fmt.Sprint(item.ID)
	var listed map[string]any
	if err := ctx.PlatformAPI().CallAppResult("inventory", "inventory_reservations_list", map[string]any{
		"_project_id": pid, "reference_app": "commerce", "reference_type": "cart_item", "reference_id": ref, "status": "active", "limit": 10,
	}, &listed); err != nil {
		return 0, fmt.Errorf("list existing inventory reservations: %w", err)
	}
	if rows, ok := listed["reservations"].([]any); ok {
		for _, raw := range rows {
			reservation, _ := raw.(map[string]any)
			reservationID := intArg(reservation, "id")
			if intArg(reservation, "item_id") == ptrValue(item.InventoryItemID) && mathAbs(floatArg(reservation, "quantity", 0)-item.Quantity) <= 1e-9 {
				if reservationID != 0 {
					return reservationID, nil
				}
			}
			if reservationID != 0 {
				if err := a.releaseReservations(ctx, pid, []int64{reservationID}); err != nil {
					return 0, fmt.Errorf("release stale inventory reservation: %w", err)
				}
			}
		}
	}
	var response map[string]any
	if err := ctx.PlatformAPI().CallAppResult("inventory", "inventory_reserve", map[string]any{
		"_project_id":    pid,
		"item_id":        *item.InventoryItemID,
		"quantity":       item.Quantity,
		"reference_app":  "commerce",
		"reference_type": "cart_item",
		"reference_id":   ref,
		"metadata":       map[string]any{"cart_id": cart.ID, "variant_id": item.VariantID},
	}, &response); err != nil {
		return 0, err
	}
	id := intArg(unwrap(response, "reservation"), "id")
	if id == 0 {
		return 0, errors.New("inventory reservation response missing id")
	}
	return id, nil
}

func mathAbs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func (a *App) handleInvoicePaid(ctx *sdk.AppCtx, event sdk.Event) error {
	pid := strings.TrimSpace(event.ProjectID)
	if pid == "" {
		pid = ctx.CurrentProject()
	}
	invoiceID := intArg(event.Data, "id")
	if pid == "" || invoiceID == 0 || strArg(event.Data, "status") != "paid" {
		return nil
	}
	sale, err := dbSaleGetByInvoice(ctx.AppDB(), pid, invoiceID)
	if err != nil || sale == nil || sale.PaymentStatus == "paid" && sale.Status == "paid" {
		return err
	}
	_, err = a.completePaidSale(ctx.WithProject(pid), sale, true)
	return err
}

func (a *App) handleCheckoutPaid(ctx *sdk.AppCtx, event sdk.Event) error {
	pid := strings.TrimSpace(event.ProjectID)
	if pid == "" {
		pid = ctx.CurrentProject()
	}
	invoiceID := intArg(event.Data, "invoice_id")
	if pid == "" || invoiceID == 0 || strArg(event.Data, "status") != "paid" {
		return nil
	}
	sale, err := dbSaleGetByInvoice(ctx.AppDB(), pid, invoiceID)
	if err != nil || sale == nil || sale.PaymentStatus == "paid" && sale.Status == "paid" {
		return err
	}
	_, err = a.completePaidSale(ctx.WithProject(pid), sale, true)
	return err
}

func (a *App) handleCheckoutExpired(ctx *sdk.AppCtx, event sdk.Event) error {
	pid := strings.TrimSpace(event.ProjectID)
	if pid == "" {
		pid = ctx.CurrentProject()
	}
	externalID := intArg(event.Data, "session_id")
	if pid == "" || externalID == 0 {
		return nil
	}
	var checkoutID int64
	err := ctx.AppDB().QueryRow(
		`SELECT id FROM commerce_checkout_sessions
		 WHERE project_id=? AND checkout_session_id=? AND status='started'`,
		pid, externalID).Scan(&checkoutID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.expireCommerceCheckout(ctx.WithProject(pid), pid, checkoutID)
}

func (a *App) handleInventoryReservationExpired(ctx *sdk.AppCtx, event sdk.Event) error {
	pid := strings.TrimSpace(event.ProjectID)
	if pid == "" {
		pid = ctx.CurrentProject()
	}
	reservationID := intArg(event.Data, "reservation_id")
	if pid == "" || reservationID == 0 {
		return nil
	}
	var checkoutID int64
	err := ctx.AppDB().QueryRow(
		`SELECT c.id
		   FROM commerce_reservation_links l
		   JOIN commerce_checkout_sessions c ON c.id=l.checkout_id
		  WHERE c.project_id=? AND l.reservation_id=? AND c.status='started'`,
		pid, reservationID).Scan(&checkoutID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.expireCommerceCheckout(ctx.WithProject(pid), pid, checkoutID)
}

func (a *App) expireCommerceCheckout(ctx *sdk.AppCtx, pid string, checkoutID int64) error {
	checkout, err := dbCheckoutGet(ctx.AppDB(), pid, checkoutID)
	if err != nil || checkout == nil || checkout.Status != "started" {
		return err
	}
	if err := a.releaseDiscountReservation(ctx, pid, checkout.DiscountReservationID); err != nil {
		return err
	}
	if err := dbReservationLinksEnsure(ctx.AppDB(), checkout.ID, checkout.ReservationIDs); err != nil {
		return err
	}
	links, err := dbReservationLinks(ctx.AppDB(), checkout.ID)
	if err != nil {
		return err
	}
	for _, link := range links {
		if link.Status != "active" {
			continue
		}
		if err := a.releaseReservations(ctx, pid, []int64{link.ReservationID}); err != nil {
			return err
		}
		if err := dbReservationLinkStatus(ctx.AppDB(), checkout.ID, link.ReservationID, "released", ""); err != nil {
			return err
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE commerce_checkout_sessions
		    SET status='expired', completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		  WHERE project_id=? AND id=? AND status='started'`, pid, checkout.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE commerce_carts SET status='open', updated_at=CURRENT_TIMESTAMP
		 WHERE project_id=? AND id=? AND status='checkout'`, pid, checkout.CartID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ctx.Emit("commerce.checkout.expired", map[string]any{
		"checkout_id": checkout.ID, "cart_id": checkout.CartID, "store_id": checkout.StoreID,
	})
	return nil
}

func looksLikeCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, r := range value {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
