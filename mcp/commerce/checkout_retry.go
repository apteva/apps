package main

import (
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"math"
)

// Once a sale exists, preserve its checkout/invoice forever. A retry gets a
// new Commerce cart and checkout instead of repointing the cancelled sale.
func (a *App) replaceRetriedCart(ctx *sdk.AppCtx, pid string, store *Store, old *Cart, token string) (*Cart, error) {
	var result map[string]any
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	if e := ctx.PlatformAPI().CallAppResult("checkout", "cart_create", map[string]any{"_project_id": pid, "session_token": newToken(), "metadata": map[string]any{"source": "commerce", "store_id": store.ID, "previous_commerce_cart_id": old.ID}}, &result); e != nil {
		return nil, e
	}
	externalID := intArg(unwrap(result, "cart"), "id")
	if externalID == 0 {
		return nil, errors.New("replacement Checkout cart missing id")
	}
	itemIDs := map[int64]int64{}
	for _, item := range old.Items {
		if item.CatalogPriceID == nil {
			return nil, errors.New("retry item lacks Catalog price")
		}
		if e := ctx.PlatformAPI().CallAppResult("checkout", "cart_add_item", map[string]any{"_project_id": pid, "cart_id": externalID, "price_id": *item.CatalogPriceID, "quantity": item.Quantity}, &result); e != nil {
			return nil, e
		}
		id, qty := checkoutCartItem(result, *item.CatalogPriceID)
		if id == 0 || math.Abs(qty-item.Quantity) > 1e-9 {
			return nil, errors.New("replacement item snapshot mismatch")
		}
		itemIDs[item.ID] = id
	}
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	updated, e := tx.Exec(`UPDATE commerce_carts SET session_token=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=? AND session_token=? AND status='open'`, fmt.Sprintf("%s:attempt:%d:%s", token, old.ID, newToken()), pid, old.ID, token)
	if e != nil {
		return nil, e
	}
	n, _ := updated.RowsAffected()
	if n != 1 {
		return nil, errors.New("cart changed while preparing retry")
	}
	meta := copyMap(old.Metadata)
	delete(meta, "checkout_quote")
	delete(meta, "shipping_quote")
	meta["retry_of_cart_id"] = old.ID
	inserted, e := tx.Exec(`INSERT INTO commerce_carts(project_id,store_id,checkout_cart_id,session_token,currency,metadata_json,subtotal_cents,total_cents) VALUES(?,?,?,?,?,?,?,?)`, pid, store.ID, externalID, token, old.Currency, jsonText(meta, "{}"), old.SubtotalCents, old.SubtotalCents)
	if e != nil {
		return nil, e
	}
	newID, _ := inserted.LastInsertId()
	var currentCount int
	if e = tx.QueryRow(`SELECT COUNT(*) FROM commerce_cart_items WHERE project_id=? AND cart_id=?`, pid, old.ID).Scan(&currentCount); e != nil {
		return nil, e
	}
	if currentCount != len(old.Items) {
		return nil, errors.New("cart items changed during retry")
	}
	for _, item := range old.Items {
		var itemMeta string
		var quantity float64
		var cents int64
		if e = tx.QueryRow(`SELECT metadata_json,quantity,unit_amount_cents FROM commerce_cart_items WHERE project_id=? AND cart_id=? AND id=?`, pid, old.ID, item.ID).Scan(&itemMeta, &quantity, &cents); e != nil {
			return nil, e
		}
		if math.Abs(quantity-item.Quantity) > 1e-9 || cents != item.UnitAmountCents {
			return nil, errors.New("cart item changed during retry")
		}
		_, e = tx.Exec(`INSERT INTO commerce_cart_items(project_id,cart_id,checkout_item_id,variant_id,listing_id,inventory_item_id,catalog_price_id,sku,title_snapshot,unit_amount_cents,currency,quantity,metadata_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, pid, newID, itemIDs[item.ID], item.VariantID, item.ListingID, item.InventoryItemID, item.CatalogPriceID, item.SKU, item.TitleSnapshot, item.UnitAmountCents, item.Currency, item.Quantity, itemMeta)
		if e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return dbCartGet(ctx.AppDB(), pid, newID, true)
}
func (a *App) cartHasSale(ctx *sdk.AppCtx, pid string, cart *Cart) (bool, error) {
	ch, e := dbCheckoutGetByCart(ctx.AppDB(), pid, cart.ID)
	if e != nil || ch == nil {
		return false, e
	}
	sale, e := dbSaleGetByCheckout(ctx.AppDB(), pid, ch.ID)
	return sale != nil, e
}
