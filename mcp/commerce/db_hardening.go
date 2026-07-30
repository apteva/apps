package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func requestAppContext(r *http.Request) (*sdk.AppCtx, string, error) {
	if globalCtx == nil {
		return nil, "", errors.New("commerce is not mounted")
	}
	pid := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if pid == "" {
		pid = strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	}
	if pid == "" {
		pid = strings.TrimSpace(globalCtx.CurrentProject())
	}
	if pid == "" {
		return nil, "", errors.New("project_id query parameter required")
	}
	return globalCtx.WithProject(pid), pid, nil
}

func pathParts(path, prefix string) []string {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" {
		return nil
	}
	return strings.Split(rest, "/")
}

func resultValue(result any, key string) any {
	values, _ := result.(map[string]any)
	return values[key]
}

func notFoundErr(value any, err error, message string) error {
	if err != nil {
		return err
	}
	if value == nil {
		return errors.New(message)
	}
	rv := reflect.ValueOf(value)
	if (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) && rv.IsNil() {
		return errors.New(message)
	}
	return nil
}

func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("JSON body required")
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func dbListingUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Listing, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	if existing, err := dbListingGet(db, pid, id, false); err != nil || existing == nil {
		return nil, firstErr(err, errors.New("product not found"))
	}
	sets, vals := []string{}, []any{}
	for _, key := range []string{"title", "description_html", "vendor", "product_type", "seo_title", "seo_description"} {
		if value, ok := patch[key]; ok {
			s, isString := value.(string)
			if !isString {
				return nil, fmt.Errorf("%s must be a string", key)
			}
			if key == "title" && strings.TrimSpace(s) == "" {
				return nil, errors.New("title cannot be empty")
			}
			sets = append(sets, key+"=?")
			vals = append(vals, strings.TrimSpace(s))
		}
	}
	if value, ok := patch["handle"]; ok {
		handle := slugify(fmt.Sprint(value))
		if handle == "" {
			return nil, errors.New("handle cannot be empty")
		}
		sets = append(sets, "handle=?")
		vals = append(vals, handle)
	}
	if hasKey(patch, "featured_media_id") {
		sets = append(sets, "featured_media_id=?")
		vals = append(vals, nullableInt(intArg(patch, "featured_media_id")))
	}
	if hasKey(patch, "metadata") {
		sets = append(sets, "metadata_json=?")
		vals = append(vals, jsonText(patch["metadata"], "{}"))
	}
	if len(sets) == 0 {
		return dbListingGet(db, pid, id, true)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, pid, id)
	if _, err := db.Exec(`UPDATE commerce_listings SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, vals...); err != nil {
		return nil, err
	}
	return dbListingGet(db, pid, id, true)
}

func dbCollectionGetWithProducts(db *sql.DB, pid string, id int64, listingStatus string) (*Collection, error) {
	collection, err := dbCollectionGet(db, pid, id)
	if err != nil || collection == nil {
		return nil, firstErr(err, errors.New("collection not found"))
	}
	query := `SELECT commerce_listings.id
		FROM commerce_listings
		JOIN commerce_collection_listings cl ON cl.listing_id=commerce_listings.id
		WHERE commerce_listings.project_id=? AND cl.collection_id=?`
	values := []any{pid, id}
	if listingStatus != "" {
		query += ` AND commerce_listings.status=?`
		values = append(values, listingStatus)
	}
	query += ` ORDER BY cl.sort_order, commerce_listings.title`
	rows, err := db.Query(query, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var listingIDs []int64
	for rows.Next() {
		var listingID int64
		if err := rows.Scan(&listingID); err != nil {
			return nil, err
		}
		listingIDs = append(listingIDs, listingID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, listingID := range listingIDs {
		listing, err := dbListingGet(db, pid, listingID, true)
		if err != nil {
			return nil, err
		}
		collection.Products = append(collection.Products, listing)
	}
	return collection, rows.Err()
}

func dbCollectionUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Collection, error) {
	collection, err := dbCollectionGet(db, pid, id)
	if err != nil || collection == nil {
		return nil, firstErr(err, errors.New("collection not found"))
	}
	sets, vals := []string{}, []any{}
	for _, key := range []string{"title", "description_html"} {
		if value, ok := patch[key]; ok {
			s, isString := value.(string)
			if !isString || (key == "title" && strings.TrimSpace(s) == "") {
				return nil, fmt.Errorf("%s must be a non-empty string", key)
			}
			sets = append(sets, key+"=?")
			vals = append(vals, strings.TrimSpace(s))
		}
	}
	if value, ok := patch["handle"]; ok {
		handle := slugify(fmt.Sprint(value))
		if handle == "" {
			return nil, errors.New("handle cannot be empty")
		}
		sets = append(sets, "handle=?")
		vals = append(vals, handle)
	}
	if value := strArg(patch, "status"); value != "" {
		if value != "active" && value != "draft" && value != "archived" {
			return nil, errors.New("status must be active, draft, or archived")
		}
		sets = append(sets, "status=?")
		vals = append(vals, value)
	}
	if hasKey(patch, "sort_order") {
		sets = append(sets, "sort_order=?")
		vals = append(vals, intArg(patch, "sort_order"))
	}
	if hasKey(patch, "metadata") {
		sets = append(sets, "metadata_json=?")
		vals = append(vals, jsonText(patch["metadata"], "{}"))
	}
	if len(sets) == 0 {
		return dbCollectionGetWithProducts(db, pid, id, "")
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, pid, id)
	if _, err := db.Exec(`UPDATE commerce_collections SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, vals...); err != nil {
		return nil, err
	}
	return dbCollectionGetWithProducts(db, pid, id, "")
}

func dbCollectionRemoveListing(db *sql.DB, pid string, collectionID, listingID int64) error {
	collection, err := dbCollectionGet(db, pid, collectionID)
	if err != nil || collection == nil {
		return firstErr(err, errors.New("collection not found"))
	}
	listing, err := dbListingGet(db, pid, listingID, false)
	if err != nil || listing == nil {
		return firstErr(err, errors.New("product not found"))
	}
	if collection.StoreID != listing.StoreID {
		return errors.New("collection and product must belong to the same store")
	}
	result, err := db.Exec(`DELETE FROM commerce_collection_listings WHERE collection_id=? AND listing_id=?`, collectionID, listingID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return errors.New("product is not in collection")
	}
	return nil
}

func dbCartsList(db *sql.DB, pid string, args map[string]any) ([]*Cart, error) {
	where, vals := []string{"project_id=?"}, []any{pid}
	if id := intArg(args, "store_id"); id != 0 {
		where = append(where, "store_id=?")
		vals = append(vals, id)
	}
	if status := strArg(args, "status"); status != "" {
		where = append(where, "status=?")
		vals = append(vals, status)
	}

	updatedBefore, err := cartFilterTimestamp(args, "updated_before")
	if err != nil {
		return nil, err
	}
	updatedAfter, err := cartFilterTimestamp(args, "updated_after")
	if err != nil {
		return nil, err
	}
	if updatedBefore != "" && updatedAfter != "" && updatedAfter >= updatedBefore {
		return nil, errors.New("updated_after must be earlier than updated_before")
	}

	inactiveMinutes := intArg(args, "inactive_for_minutes")
	if hasKey(args, "inactive_for_minutes") {
		if inactiveMinutes <= 0 {
			return nil, errors.New("inactive_for_minutes must be greater than zero")
		}
		if inactiveMinutes > 525600 {
			return nil, errors.New("inactive_for_minutes must not exceed 525600")
		}
		if updatedBefore != "" {
			return nil, errors.New("use either updated_before or inactive_for_minutes, not both")
		}
		updatedBefore = time.Now().UTC().Add(-time.Duration(inactiveMinutes) * time.Minute).Format("2006-01-02 15:04:05")
	}

	abandonedOnly := boolArg(args, "abandoned_only")
	if abandonedOnly && hasKey(args, "has_items") && !boolArg(args, "has_items") {
		return nil, errors.New("abandoned_only requires has_items to be true")
	}
	if abandonedOnly {
		where = append(where, "status IN ('open','checkout')")
		where = append(where, `EXISTS (
			SELECT 1 FROM commerce_cart_items item
			WHERE item.project_id=commerce_carts.project_id
			  AND item.cart_id=commerce_carts.id
			  AND item.quantity>0
		)`)
		if updatedBefore == "" {
			updatedBefore = time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
		}
	} else if hasKey(args, "has_items") {
		operator := "EXISTS"
		if !boolArg(args, "has_items") {
			operator = "NOT EXISTS"
		}
		where = append(where, operator+` (
			SELECT 1 FROM commerce_cart_items item
			WHERE item.project_id=commerce_carts.project_id
			  AND item.cart_id=commerce_carts.id
			  AND item.quantity>0
		)`)
	}
	if updatedBefore != "" {
		where = append(where, "updated_at<?")
		vals = append(vals, updatedBefore)
	}
	if updatedAfter != "" {
		where = append(where, "updated_at>?")
		vals = append(vals, updatedAfter)
	}

	orderBy, err := cartFilterOrder(strArg(args, "sort"))
	if err != nil {
		return nil, err
	}
	vals = append(vals, clamp(intArg(args, "limit"), 1, 500, 100))
	rows, err := db.Query(cartSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY `+orderBy+` LIMIT ?`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var carts []*Cart
	for rows.Next() {
		cart, err := scanCart(rows)
		if err != nil {
			return nil, err
		}
		carts = append(carts, cart)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, cart := range carts {
		var err error
		cart.Items, err = dbCartItems(db, pid, cart.ID)
		if err != nil {
			return nil, err
		}
	}
	return carts, nil
}

func cartFilterTimestamp(args map[string]any, key string) (string, error) {
	raw := strArg(args, key)
	if raw == "" {
		return "", nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", fmt.Errorf("%s must be an RFC3339 timestamp", key)
	}
	return parsed.UTC().Format("2006-01-02 15:04:05"), nil
}

func cartFilterOrder(sort string) (string, error) {
	switch sort {
	case "", "updated_desc":
		return "updated_at DESC, id DESC", nil
	case "updated_asc":
		return "updated_at ASC, id ASC", nil
	default:
		return "", errors.New("sort must be 'updated_desc' or 'updated_asc'")
	}
}

func dbCartItemGet(db *sql.DB, pid string, cartID, itemID int64) (*CartItem, error) {
	items, err := dbCartItems(db, pid, cartID)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == itemID {
			return item, nil
		}
	}
	return nil, nil
}

func dbCheckoutGetByCart(db *sql.DB, pid string, cartID int64) (*CheckoutSession, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM commerce_checkout_sessions WHERE project_id=? AND cart_id=?`, pid, cartID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbCheckoutGet(db, pid, id)
}

func dbReservationLinksEnsure(db *sql.DB, checkoutID int64, reservationIDs []int64) error {
	for _, reservationID := range reservationIDs {
		if reservationID == 0 {
			continue
		}
		if _, err := db.Exec(`INSERT INTO commerce_reservation_links (checkout_id, reservation_id)
			VALUES (?, ?) ON CONFLICT(checkout_id, reservation_id) DO NOTHING`, checkoutID, reservationID); err != nil {
			return err
		}
	}
	return nil
}

type reservationLink struct {
	ReservationID int64
	Status        string
	LastError     string
}

func dbReservationLinks(db *sql.DB, checkoutID int64) ([]reservationLink, error) {
	rows, err := db.Query(`SELECT reservation_id, status, last_error FROM commerce_reservation_links WHERE checkout_id=? ORDER BY reservation_id`, checkoutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []reservationLink
	for rows.Next() {
		var link reservationLink
		if err := rows.Scan(&link.ReservationID, &link.Status, &link.LastError); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func dbReservationLinkStatus(db *sql.DB, checkoutID, reservationID int64, status, lastError string) error {
	_, err := db.Exec(`UPDATE commerce_reservation_links SET status=?, last_error=?, updated_at=CURRENT_TIMESTAMP
		WHERE checkout_id=? AND reservation_id=?`, status, lastError, checkoutID, reservationID)
	return err
}

func dbSaleGetByInvoice(db *sql.DB, pid string, invoiceID int64) (*Sale, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM commerce_sales WHERE project_id=? AND invoice_id=?`, pid, invoiceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbSaleGet(db, pid, id)
}

func dbSaleGetByCheckout(db *sql.DB, pid string, checkoutID int64) (*Sale, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM commerce_sales WHERE project_id=? AND checkout_id=?`, pid, checkoutID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbSaleGet(db, pid, id)
}

func dbSaleItems(db *sql.DB, pid string, saleID int64) ([]*SaleItem, error) {
	rows, err := db.Query(`SELECT id, sale_id, variant_id, listing_id, inventory_item_id, catalog_product_id,
		catalog_price_id, sku, title_snapshot, unit_amount_cents, currency, quantity, requires_shipping, metadata_json
		FROM commerce_sale_items WHERE project_id=? AND sale_id=? ORDER BY id`, pid, saleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*SaleItem
	for rows.Next() {
		var item SaleItem
		var variantID, listingID, inventoryID, productID, priceID sql.NullInt64
		var shipping int
		var metadata string
		if err := rows.Scan(&item.ID, &item.SaleID, &variantID, &listingID, &inventoryID, &productID,
			&priceID, &item.SKU, &item.TitleSnapshot, &item.UnitAmountCents, &item.Currency, &item.Quantity, &shipping, &metadata); err != nil {
			return nil, err
		}
		item.VariantID = ptrIfValid(variantID)
		item.ListingID = ptrIfValid(listingID)
		item.InventoryItemID = ptrIfValid(inventoryID)
		item.CatalogProductID = ptrIfValid(productID)
		item.CatalogPriceID = ptrIfValid(priceID)
		item.RequiresShipping = shipping != 0
		item.Metadata = jsonMap(metadata)
		items = append(items, &item)
	}
	return items, rows.Err()
}

func dbSaleSetProcessingError(db *sql.DB, pid string, saleID int64, message string) error {
	_, err := db.Exec(`UPDATE commerce_sales SET processing_error=?, status=CASE WHEN payment_status='paid' THEN 'processing' ELSE status END,
		updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, message, pid, saleID)
	return err
}
