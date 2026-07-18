package main

import (
	"errors"
	"fmt"
	"strings"

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
		}, &response); err != nil && !strings.Contains(err.Error(), "active reservation not found") {
			failures = append(failures, fmt.Sprintf("%d: %v", id, err))
		}
	}
	if len(failures) > 0 {
		return errors.New("release reservations: " + strings.Join(failures, "; "))
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
