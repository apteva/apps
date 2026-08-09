package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

type platformCall struct {
	App   string
	Tool  string
	Input map[string]any
}

type commercePlatformStub struct {
	sdk.PlatformClient
	calls     []platformCall
	responses map[string]any
	errors    map[string]error
}

type platformResponseFunc func(input map[string]any) (any, error)

func newCommercePlatformStub() *commercePlatformStub {
	return &commercePlatformStub{responses: map[string]any{}, errors: map[string]error{}}
}

func (p *commercePlatformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, platformCall{App: app, Tool: tool, Input: copyMap(input)})
	key := app + ":" + tool
	if err := p.errors[key]; err != nil {
		return err
	}
	response, ok := p.responses[key]
	if !ok {
		return errors.New("unexpected app call: " + key)
	}
	if dynamic, ok := response.(platformResponseFunc); ok {
		var err error
		response, err = dynamic(input)
		if err != nil {
			return err
		}
	}
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func TestManifestParse(t *testing.T) {
	embedded, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	fromFile, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	if embedded.Name != fromFile.Name || embedded.Version != fromFile.Version {
		t.Fatalf("embedded manifest %s@%s != file %s@%s", embedded.Name, embedded.Version, fromFile.Name, fromFile.Version)
	}
	if embedded.Name != "commerce" {
		t.Fatalf("manifest name=%q, want commerce", embedded.Name)
	}
	if len(fromFile.Provides.MCPTools) == 0 {
		t.Fatal("commerce manifest should expose MCP tools")
	}
	permissions := map[sdk.Permission]bool{}
	for _, permission := range fromFile.Requires.Permissions {
		permissions[permission] = true
	}
	if !permissions[sdk.Permission("platform.connections.execute")] {
		t.Fatal("commerce manifest must allow execution of bound supplier provider tools")
	}
	declared := map[string]bool{}
	for _, tool := range fromFile.Provides.MCPTools {
		declared[tool.Name] = true
	}
	runtimeTools := (&App{}).MCPTools()
	if len(runtimeTools) != len(declared) {
		t.Fatalf("manifest declares %d tools but runtime exposes %d", len(declared), len(runtimeTools))
	}
	for _, tool := range runtimeTools {
		if !declared[tool.Name] {
			t.Errorf("runtime tool %q is missing from the manifest", tool.Name)
		}
	}
}

func TestStoreListingCartAndSaleFlow(t *testing.T) {
	db := openCommerceTestDB(t)
	pid := "proj-test"

	store, err := dbStoreCreate(db, pid, map[string]any{
		"slug":             "main",
		"name":             "Main Store",
		"default_currency": "eur",
	})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if store.DefaultCurrency != "EUR" {
		t.Fatalf("default currency=%q, want EUR", store.DefaultCurrency)
	}

	listing, err := dbListingCreate(db, pid, map[string]any{
		"store_id":           store.ID,
		"title":              "Apteva Hoodie",
		"description_html":   "<p>Heavyweight</p>",
		"catalog_product_id": int64(101),
		"status":             "active",
		"metadata":           map[string]any{"image_url": "https://example.com/hoodie.jpg"},
	})
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	variant, err := dbVariantCreate(db, pid, map[string]any{
		"listing_id":       listing.ID,
		"sku":              "HD-001",
		"title":            "Black / M",
		"catalog_price_id": int64(201),
		"price_cents":      int64(4900),
		"currency":         "eur",
	})
	if err != nil {
		t.Fatalf("create variant: %v", err)
	}

	cart, err := dbCartCreate(db, pid, map[string]any{
		"store_id":      store.ID,
		"session_token": "sess_123",
		"currency":      store.DefaultCurrency,
	})
	if err != nil {
		t.Fatalf("create cart: %v", err)
	}
	if err := dbCartAddItem(db, pid, cart.ID, variant, 2, 301); err != nil {
		t.Fatalf("add cart item: %v", err)
	}
	cart, err = dbCartGet(db, pid, cart.ID, true)
	if err != nil {
		t.Fatalf("get cart: %v", err)
	}
	if got := cart.TotalCents; got != 9800 {
		t.Fatalf("cart total=%d, want 9800", got)
	}
	if len(cart.Items) != 1 || cart.Items[0].TitleSnapshot != "Apteva Hoodie - Black / M" {
		t.Fatalf("unexpected cart item snapshot: %#v", cart.Items)
	}
	if cart.Items[0].ImageURL != "https://example.com/hoodie.jpg" {
		t.Fatalf("cart item image=%q", cart.Items[0].ImageURL)
	}

	checkout, err := dbCheckoutCreate(db, pid, cart, 301, []int64{401, 402}, "", nil)
	if err != nil {
		t.Fatalf("create checkout: %v", err)
	}
	if err := dbCheckoutPatch(db, pid, checkout.ID, map[string]any{"email": "buyer@example.com", "name": "Buyer"}); err != nil {
		t.Fatalf("patch checkout: %v", err)
	}
	checkout, err = dbCheckoutGet(db, pid, checkout.ID)
	if err != nil {
		t.Fatalf("get checkout: %v", err)
	}
	if checkout.CustomerEmail != "buyer@example.com" || len(checkout.ReservationIDs) != 2 {
		t.Fatalf("unexpected checkout: %#v", checkout)
	}
	if err := dbCheckoutInvoice(db, pid, checkout.ID, 501, "INV-501"); err != nil {
		t.Fatalf("set invoice: %v", err)
	}
	checkout, _ = dbCheckoutGet(db, pid, checkout.ID)
	sale, err := dbSaleCreateFromCheckout(db, pid, checkout)
	if err != nil {
		t.Fatalf("create sale: %v", err)
	}
	if sale.TotalCents != 9800 || sale.InvoiceNumber != "INV-501" {
		t.Fatalf("unexpected sale: %#v", sale)
	}
}

func TestCartsListFiltersAbandonmentCandidates(t *testing.T) {
	db := openCommerceTestDB(t)
	pid := "cart-filters"

	store, err := dbStoreCreate(db, pid, map[string]any{"slug": "filters", "name": "Filters"})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := dbListingCreate(db, pid, map[string]any{
		"store_id": store.ID, "title": "Filter product", "status": "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	variant, err := dbVariantCreate(db, pid, map[string]any{
		"listing_id": listing.ID, "sku": "FILTER-1", "title": "Default",
		"price_cents": int64(2500), "currency": "USD",
	})
	if err != nil {
		t.Fatal(err)
	}

	createCart := func(token, status, updatedAt string, withItems bool) *Cart {
		t.Helper()
		cart, createErr := dbCartCreate(db, pid, map[string]any{
			"store_id": store.ID, "session_token": token, "currency": "USD",
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if withItems {
			if addErr := dbCartAddItem(db, pid, cart.ID, variant, 1, 0); addErr != nil {
				t.Fatal(addErr)
			}
		}
		if _, updateErr := db.Exec(
			`UPDATE commerce_carts SET status=?, updated_at=? WHERE project_id=? AND id=?`,
			status, updatedAt, pid, cart.ID,
		); updateErr != nil {
			t.Fatal(updateErr)
		}
		return cart
	}

	oldOpen := createCart("old-open", "open", "2024-01-01 10:00:00", true)
	oldCheckout := createCart("old-checkout", "checkout", "2024-01-02 10:00:00", true)
	createCart("old-empty", "open", "2024-01-03 10:00:00", false)
	createCart("old-converted", "converted", "2024-01-04 10:00:00", true)
	createCart("recent-open", "open", "2099-01-01 10:00:00", true)

	abandoned, err := dbCartsList(db, pid, map[string]any{
		"abandoned_only": true,
		"updated_before": "2025-01-01T00:00:00Z",
		"sort":           "updated_asc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(abandoned) != 2 || abandoned[0].ID != oldOpen.ID || abandoned[1].ID != oldCheckout.ID {
		t.Fatalf("abandoned carts=%#v, want old open and checkout carts", abandoned)
	}

	oldOpenOnly, err := dbCartsList(db, pid, map[string]any{
		"status":         "open",
		"has_items":      true,
		"updated_before": "2025-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(oldOpenOnly) != 1 || oldOpenOnly[0].ID != oldOpen.ID {
		t.Fatalf("old open carts=%#v, want cart %d", oldOpenOnly, oldOpen.ID)
	}

	inactive, err := dbCartsList(db, pid, map[string]any{
		"abandoned_only":       true,
		"inactive_for_minutes": int64(60),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inactive) != 2 {
		t.Fatalf("inactive abandoned carts=%d, want 2", len(inactive))
	}
}

func TestCartsListRejectsInvalidActivityFilters(t *testing.T) {
	db := openCommerceTestDB(t)
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "invalid timestamp", args: map[string]any{"updated_before": "yesterday"}},
		{name: "reversed range", args: map[string]any{
			"updated_after": "2025-01-02T00:00:00Z", "updated_before": "2025-01-01T00:00:00Z",
		}},
		{name: "zero inactivity", args: map[string]any{"inactive_for_minutes": int64(0)}},
		{name: "ambiguous cutoff", args: map[string]any{
			"inactive_for_minutes": int64(60), "updated_before": "2025-01-01T00:00:00Z",
		}},
		{name: "abandoned without items", args: map[string]any{
			"abandoned_only": true, "has_items": false,
		}},
		{name: "invalid sort", args: map[string]any{"sort": "oldest"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := dbCartsList(db, "cart-filters", tt.args); err == nil {
				t.Fatalf("dbCartsList(%#v) succeeded, want validation error", tt.args)
			}
		})
	}
}

func TestCheckoutMarketAllowlist(t *testing.T) {
	store := &Store{Metadata: map[string]any{
		"markets": map[string]any{"enabled": []any{"ES", "FR"}},
	}}
	if err := validateCheckoutMarket(store, map[string]any{"country_code": "es"}); err != nil {
		t.Fatalf("enabled market rejected: %v", err)
	}
	if err := validateCheckoutMarket(store, map[string]any{"country_code": "US"}); err == nil {
		t.Fatal("disabled market was accepted")
	}
}

func TestCartUsesCanonicalPriceAndKeepsCheckoutInSync(t *testing.T) {
	platform := newCommercePlatformStub()
	platform.responses["catalog:catalog_prices_get"] = map[string]any{"price": map[string]any{
		"id": int64(201), "product_id": int64(101), "unit_amount_cents": int64(4900), "currency": "EUR", "active": true,
	}}
	platform.responses["checkout:cart_add_item"] = map[string]any{"cart": map[string]any{"items": []any{
		map[string]any{"id": int64(501), "price_id": int64(201), "quantity": float64(1)},
	}}}
	platform.responses["checkout:cart_set_quantity"] = map[string]any{"cart": map[string]any{"id": int64(301)}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-sync"), tk.WithPlatform(platform))
	store, listing, variant, cart := seedCommerceCart(t, ctx.AppDB(), "proj-sync", "main", 0)
	_ = store
	variant.PriceCents = 1
	if _, err := ctx.AppDB().Exec(`UPDATE commerce_variants SET price_cents=1 WHERE id=?`, variant.ID); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	got, err := app.addCartItem(ctx, map[string]any{"cart_id": cart.ID, "variant_id": variant.ID, "quantity": 1})
	if err != nil {
		t.Fatalf("add cart item: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].UnitAmountCents != 4900 {
		t.Fatalf("cart did not use canonical price: %#v", got.Items)
	}
	if got.Items[0].InventoryItemID != nil {
		t.Fatalf("non-inventory line acquired inventory id: %#v", got.Items[0].InventoryItemID)
	}
	if got.Items[0].CheckoutItemID == nil || *got.Items[0].CheckoutItemID != 501 {
		t.Fatalf("checkout item link missing: %#v", got.Items[0])
	}

	got, err = app.setCartItemQuantity(ctx, map[string]any{"cart_id": cart.ID, "item_id": got.Items[0].ID, "quantity": 3})
	if err != nil {
		t.Fatalf("set quantity: %v", err)
	}
	if got.Items[0].Quantity != 3 || got.TotalCents != 14700 {
		t.Fatalf("unexpected synchronized cart: %#v", got)
	}
	last := platform.calls[len(platform.calls)-1]
	if last.Tool != "cart_set_quantity" || intArg(last.Input, "item_id") != 501 || floatArg(last.Input, "quantity", 0) != 3 {
		t.Fatalf("unexpected Checkout quantity call: %#v", last)
	}
	if listing.Status != "active" {
		t.Fatal("seed listing should be active")
	}
}

func TestCartItemTitle(t *testing.T) {
	tests := []struct {
		name         string
		productTitle string
		variantTitle string
		want         string
	}{
		{name: "default variant", productTitle: "Quiet Morning Mug", variantTitle: "Default", want: "Quiet Morning Mug"},
		{name: "repeated title", productTitle: "Quiet Morning Mug", variantTitle: "Quiet Morning Mug", want: "Quiet Morning Mug"},
		{name: "repeated title ignores case", productTitle: "Quiet Morning Mug", variantTitle: "quiet morning mug", want: "Quiet Morning Mug"},
		{name: "named variant", productTitle: "Apteva Hoodie", variantTitle: "Black / M", want: "Apteva Hoodie - Black / M"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cartItemTitle(tt.productTitle, tt.variantTitle); got != tt.want {
				t.Fatalf("cartItemTitle(%q, %q)=%q, want %q", tt.productTitle, tt.variantTitle, got, tt.want)
			}
		})
	}
}

func TestCheckoutUpdateReplaysIdenticalAwaitingPaymentPatch(t *testing.T) {
	platform := newCommercePlatformStub()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("checkout-replay"), tk.WithPlatform(platform))
	_, _, _, cart := seedCommerceCart(t, ctx.AppDB(), "checkout-replay", "main", 0)
	checkout, err := dbCheckoutCreate(ctx.AppDB(), "checkout-replay", cart, 401, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	patch := map[string]any{
		"email":         "buyer@example.com",
		"customer_name": "Buyer",
		"shipping_address": map[string]any{
			"line1": "1 Main Street", "city": "Madrid", "country_code": "ES",
		},
		"billing_address": map[string]any{
			"line1": "1 Main Street", "city": "Madrid", "country_code": "ES",
		},
	}
	if err := dbCheckoutPatch(ctx.AppDB(), "checkout-replay", checkout.ID, patch); err != nil {
		t.Fatal(err)
	}
	got, err := (&App{}).checkoutUpdate(ctx, map[string]any{"checkout_id": checkout.ID, "patch": patch})
	if err != nil {
		t.Fatalf("identical started retry failed: %v", err)
	}
	if len(platform.calls) != 0 {
		t.Fatalf("identical replay reached Checkout: %#v", platform.calls)
	}
	if err := dbCheckoutInvoice(ctx.AppDB(), "checkout-replay", checkout.ID, 501, "INV-501"); err != nil {
		t.Fatal(err)
	}
	got, err = (&App{}).checkoutUpdate(ctx, map[string]any{"checkout_id": checkout.ID, "patch": patch})
	if err != nil {
		t.Fatalf("identical retry failed: %v", err)
	}
	if got.Status != "awaiting_payment" || got.InvoiceNumber != "INV-501" {
		t.Fatalf("unexpected replayed checkout: %#v", got)
	}
	changed := copyMap(patch)
	changed["email"] = "other@example.com"
	if _, err := (&App{}).checkoutUpdate(ctx, map[string]any{"checkout_id": checkout.ID, "patch": changed}); err == nil {
		t.Fatal("changed buyer details were accepted after payment started")
	}
}

func TestCheckoutStartReleasesPartialReservations(t *testing.T) {
	platform := newCommercePlatformStub()
	platform.responses["inventory:inventory_reservations_list"] = map[string]any{"reservations": []any{}}
	platform.responses["inventory:inventory_reserve"] = map[string]any{"reservation": map[string]any{"id": int64(700)}}
	platform.responses["inventory:inventory_release_reservation"] = map[string]any{"reservation": map[string]any{"id": int64(700)}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-reserve"), tk.WithPlatform(platform))
	_, _, first, cart := seedCommerceCart(t, ctx.AppDB(), "proj-reserve", "main", 901)
	second, err := dbVariantCreate(ctx.AppDB(), "proj-reserve", map[string]any{
		"listing_id": first.ListingID, "catalog_price_id": int64(202), "inventory_item_id": int64(902), "price_cents": int64(2500), "currency": "USD", "title": "Second",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbCartAddItem(ctx.AppDB(), "proj-reserve", cart.ID, first, 1, 501); err != nil {
		t.Fatal(err)
	}
	if err := dbCartAddItem(ctx.AppDB(), "proj-reserve", cart.ID, second, 1, 502); err != nil {
		t.Fatal(err)
	}
	reserveCount := 0
	platform.errors["inventory:inventory_reserve"] = nil
	platform.responses["inventory:inventory_reserve"] = platformResponseFunc(func(input map[string]any) (any, error) {
		reserveCount++
		if reserveCount == 2 {
			return nil, errors.New("insufficient stock")
		}
		return map[string]any{"reservation": map[string]any{"id": int64(700)}}, nil
	})

	_, err = (&App{}).checkoutStart(ctx, map[string]any{"cart_id": cart.ID})
	if err == nil || !strings.Contains(err.Error(), "insufficient stock") {
		t.Fatalf("expected reservation failure, got %v", err)
	}
	if !hasPlatformCall(platform.calls, "inventory", "inventory_release_reservation") {
		t.Fatal("partial reservation was not released")
	}
}

func TestShippingQuoteReturnsUpdatedCartWithoutSupplierLines(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("local-shipping"))
	_, _, variant, cart := seedCommerceCart(t, ctx.AppDB(), "local-shipping", "main", 0)
	if err := dbCartAddItem(ctx.AppDB(), "local-shipping", cart.ID, variant, 1, 501); err != nil {
		t.Fatal(err)
	}
	address := map[string]any{
		"line1": "1 Main Street", "city": "Madrid", "postal_code": "28001", "country_code": "ES",
	}
	result, err := (&App{}).toolShippingQuote(ctx, map[string]any{"cart_id": cart.ID, "shipping_address": address})
	if err != nil {
		t.Fatal(err)
	}
	quoted := result.(map[string]any)["cart"].(*Cart)
	if quoted.TotalCents != 4900 || quoted.ShippingCents != 0 || len(quoted.Items) != 1 {
		t.Fatalf("unexpected local shipping quote: %#v", quoted)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE commerce_carts SET status='checkout' WHERE id=?`, cart.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolShippingQuote(ctx, map[string]any{"cart_id": cart.ID, "shipping_address": address}); err != nil {
		t.Fatalf("locked quote replay failed: %v", err)
	}
	changedAddress := copyMap(address)
	changedAddress["postal_code"] = "28002"
	if _, err := (&App{}).toolShippingQuote(ctx, map[string]any{"cart_id": cart.ID, "shipping_address": changedAddress}); err == nil {
		t.Fatal("locked cart accepted a changed shipping address")
	}
}

func seedCommerceCart(t *testing.T, db *sql.DB, pid, slug string, inventoryItemID int64) (*Store, *Listing, *Variant, *Cart) {
	t.Helper()
	store, err := dbStoreCreate(db, pid, map[string]any{"slug": slug, "name": strings.ToUpper(slug), "default_currency": "USD"})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := dbListingCreate(db, pid, map[string]any{
		"store_id": store.ID, "title": "Product", "catalog_product_id": int64(101), "status": "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	variantArgs := map[string]any{
		"listing_id": listing.ID, "catalog_price_id": int64(201), "price_cents": int64(4900), "currency": "USD", "sku": "SKU-1",
	}
	if inventoryItemID != 0 {
		variantArgs["inventory_item_id"] = inventoryItemID
	}
	variant, err := dbVariantCreate(db, pid, variantArgs)
	if err != nil {
		t.Fatal(err)
	}
	cart, err := dbCartCreate(db, pid, map[string]any{
		"store_id": store.ID, "checkout_cart_id": int64(301), "session_token": "session-" + slug, "currency": "USD",
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, listing, variant, cart
}

func hasPlatformCall(calls []platformCall, app, tool string) bool {
	for _, call := range calls {
		if call.App == app && call.Tool == tool {
			return true
		}
	}
	return false
}

func findPlatformCall(calls []platformCall, app, tool string) platformCall {
	for _, call := range calls {
		if call.App == app && call.Tool == tool {
			return call
		}
	}
	return platformCall{}
}

func countPlatformCalls(calls []platformCall, app, tool string) int {
	count := 0
	for _, call := range calls {
		if call.App == app && call.Tool == tool {
			count++
		}
	}
	return count
}

func TestPaidSaleRequiresBillingAndSnapshotsOrder(t *testing.T) {
	platform := newCommercePlatformStub()
	platform.responses["billing:invoices_get"] = map[string]any{"invoice": map[string]any{
		"id": int64(501), "status": "paid", "total_cents": int64(9800), "amount_paid_cents": int64(9800),
	}}
	platform.responses["orders:orders_search"] = map[string]any{"orders": []any{}}
	platform.responses["orders:orders_create"] = map[string]any{"order": map[string]any{"id": int64(801)}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-paid"), tk.WithPlatform(platform))
	_, _, variant, cart := seedCommerceCart(t, ctx.AppDB(), "proj-paid", "main", 0)
	if err := dbCartAddItem(ctx.AppDB(), "proj-paid", cart.ID, variant, 2, 301); err != nil {
		t.Fatal(err)
	}
	cart, _ = dbCartGet(ctx.AppDB(), "proj-paid", cart.ID, true)
	checkout, err := dbCheckoutCreate(ctx.AppDB(), "proj-paid", cart, 401, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbCheckoutPatch(ctx.AppDB(), "proj-paid", checkout.ID, map[string]any{
		"email": "buyer@example.com", "shipping_address": map[string]any{"city": "Madrid"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := dbCheckoutInvoice(ctx.AppDB(), "proj-paid", checkout.ID, 501, "INV-501"); err != nil {
		t.Fatal(err)
	}
	checkout, _ = dbCheckoutGet(ctx.AppDB(), "proj-paid", checkout.ID)
	sale, err := dbSaleCreateFromCheckout(ctx.AppDB(), "proj-paid", checkout)
	if err != nil {
		t.Fatal(err)
	}
	if len(sale.Items) != 1 || sale.ShippingAddress["city"] != "Madrid" {
		t.Fatalf("sale snapshot incomplete: %#v", sale)
	}
	if err := dbCartSetQuantity(ctx.AppDB(), "proj-paid", cart.ID, cart.Items[0].ID, 1); err != nil {
		t.Fatal(err)
	}
	platform.responses["billing:invoices_get"] = map[string]any{"invoice": map[string]any{
		"id": int64(501), "status": "open", "total_cents": int64(9800), "amount_paid_cents": int64(0),
	}}
	if _, err := (&App{}).markSalePaid(ctx, map[string]any{"sale_id": sale.ID}); err == nil || !strings.Contains(err.Error(), "not fully paid") {
		t.Fatalf("unpaid Billing invoice should be rejected, got %v", err)
	}
	if hasPlatformCall(platform.calls, "orders", "orders_create") {
		t.Fatal("order was created before Billing confirmed payment")
	}
	platform.responses["billing:invoices_get"] = map[string]any{"invoice": map[string]any{
		"id": int64(501), "status": "paid", "total_cents": int64(9800), "amount_paid_cents": int64(9800),
	}}
	paid, err := (&App{}).markSalePaid(ctx, map[string]any{"sale_id": sale.ID})
	if err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if paid.Status != "paid" || paid.PaymentStatus != "paid" || paid.OrderID == nil || *paid.OrderID != 801 {
		t.Fatalf("unexpected paid sale: %#v", paid)
	}
	orderCall := findPlatformCall(platform.calls, "orders", "orders_create")
	items, _ := orderCall.Input["items"].([]map[string]any)
	if len(items) == 0 {
		if raw, ok := orderCall.Input["items"].([]any); ok && len(raw) > 0 {
			items = []map[string]any{raw[0].(map[string]any)}
		}
	}
	if len(items) != 1 || floatArg(items[0], "quantity", 0) != 2 {
		t.Fatalf("order did not use immutable sale quantity: %#v", orderCall.Input["items"])
	}
}

func TestCommerceExposesNoPublicStorefrontRoute(t *testing.T) {
	for _, route := range (&App{}).HTTPRoutes() {
		if route.NoAuth || strings.HasPrefix(route.Pattern, "/s/") {
			t.Fatalf("commerce must not expose a public storefront route: %#v", route)
		}
	}
}

func TestConfigureStorefrontRegistersGenericContentExtension(t *testing.T) {
	platform := newCommercePlatformStub()
	platform.responses["content:sites_get"] = map[string]any{"site": map[string]any{"id": 81, "slug": "main"}}
	platform.responses["content:extensions_upsert"] = map[string]any{"extension": map[string]any{"key": "commerce-store-1"}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("storefront-project"), tk.WithPlatform(platform))
	store, err := dbStoreCreate(ctx.AppDB(), "storefront-project", map[string]any{"slug": "main", "name": "Main Store"})
	if err != nil {
		t.Fatal(err)
	}

	status, err := (&App{}).configureContentStorefront(ctx, "storefront-project", store, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || !status.ContentReady || status.SiteID != 81 || status.SiteSlug != "main" {
		t.Fatalf("unexpected storefront status: %#v", status)
	}
	if !strings.HasPrefix(status.PreviewURL, "/api/apps/content/public/") {
		t.Fatalf("preview URL must use Content's constrained public gateway: %q", status.PreviewURL)
	}
	call := findPlatformCall(platform.calls, "content", "extensions_upsert")
	if call.Tool == "" {
		t.Fatalf("Content extension was not registered: %#v", platform.calls)
	}
	manifest, ok := call.Input["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("extension manifest missing: %#v", call.Input)
	}
	dataSources, _ := manifest["data_sources"].(map[string]any)
	product, _ := dataSources["product"].(map[string]any)
	args, _ := product["args"].(map[string]any)
	if product["tool"] != "commerce_products_get" || args["status"] != "active" {
		t.Fatalf("public product source is not active-only: %#v", product)
	}
	for _, route := range (&App{}).HTTPRoutes() {
		if route.NoAuth {
			t.Fatalf("storefront configuration introduced a public Commerce route: %#v", route)
		}
	}
}

func TestPublicToolFiltersHideDraftProductsAndCollections(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("public-filter"))
	store, err := dbStoreCreate(ctx.AppDB(), "public-filter", map[string]any{"slug": "main", "name": "Main"})
	if err != nil {
		t.Fatal(err)
	}
	active, _ := dbListingCreate(ctx.AppDB(), "public-filter", map[string]any{"store_id": store.ID, "title": "Live", "status": "active"})
	draft, _ := dbListingCreate(ctx.AppDB(), "public-filter", map[string]any{"store_id": store.ID, "title": "Secret", "status": "draft"})
	collection, _ := dbCollectionCreate(ctx.AppDB(), "public-filter", map[string]any{"store_id": store.ID, "title": "Live Collection", "status": "active"})
	draftCollection, _ := dbCollectionCreate(ctx.AppDB(), "public-filter", map[string]any{"store_id": store.ID, "title": "Draft Collection", "status": "draft"})
	_ = dbCollectionAddListing(ctx.AppDB(), "public-filter", collection.ID, active.ID, 0)
	_ = dbCollectionAddListing(ctx.AppDB(), "public-filter", collection.ID, draft.ID, 1)

	if _, err := (&App{}).toolProductsGet(ctx, map[string]any{"id": draft.ID, "status": "active"}); err == nil {
		t.Fatal("active product read returned a draft")
	}
	result, err := (&App{}).toolCollectionsGet(ctx, map[string]any{"id": collection.ID, "status": "active"})
	if err != nil {
		t.Fatal(err)
	}
	got := result.(map[string]any)["collection"].(*Collection)
	if len(got.Products) != 1 || got.Products[0].ID != active.ID {
		t.Fatalf("active collection leaked draft products: %#v", got.Products)
	}
	if _, err := (&App{}).toolCollectionsGet(ctx, map[string]any{"id": draftCollection.ID, "status": "active"}); err == nil {
		t.Fatal("active collection read returned a draft collection")
	}
}

func TestCartCreateIsIdempotentForStorefrontSession(t *testing.T) {
	platform := newCommercePlatformStub()
	platform.responses["checkout:cart_create"] = platformResponseFunc(func(map[string]any) (any, error) {
		return map[string]any{"cart": map[string]any{"id": 450 + countPlatformCalls(platform.calls, "checkout", "cart_create")}}, nil
	})
	platform.errors["checkout:checkout_cancel"] = errors.New("session 752 already expired")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("cart-session"), tk.WithPlatform(platform))
	store, err := dbStoreCreate(ctx.AppDB(), "cart-session", map[string]any{"slug": "main", "name": "Main"})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"store_id": store.ID, "session_token": "signed-content-session"}
	first, err := (&App{}).createCart(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := (&App{}).createCart(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.CheckoutCartID == nil || *first.CheckoutCartID != 451 {
		t.Fatalf("session cart changed: first=%#v second=%#v", first, second)
	}
	if calls := countPlatformCalls(platform.calls, "checkout", "cart_create"); calls != 1 {
		t.Fatalf("checkout cart_create calls=%d, want 1", calls)
	}
	checkout, err := dbCheckoutCreate(ctx.AppDB(), "cart-session", first, 752, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := (&App{}).createCart(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID != first.ID || reopened.Status != "open" {
		t.Fatalf("checkout cart was not reopened: %#v", reopened)
	}
	if reopened.CheckoutCartID == nil || *reopened.CheckoutCartID != 452 {
		t.Fatalf("replacement checkout cart=%v, want 452", reopened.CheckoutCartID)
	}
	cancelled, err := dbCheckoutGet(ctx.AppDB(), "cart-session", checkout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("checkout status=%q, want cancelled", cancelled.Status)
	}
	if calls := countPlatformCalls(platform.calls, "checkout", "checkout_cancel"); calls != 1 {
		t.Fatalf("checkout checkout_cancel calls=%d, want 1", calls)
	}
	if calls := countPlatformCalls(platform.calls, "checkout", "cart_create"); calls != 2 {
		t.Fatalf("checkout cart_create calls=%d after recovery, want 2", calls)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE commerce_carts SET status='awaiting_payment' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if resumed, err := (&App{}).createCart(ctx, args); err != nil || resumed.ID != first.ID {
		t.Fatalf("awaiting-payment storefront session was not resumed: cart=%#v err=%v", resumed, err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE commerce_carts SET status='converted' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := (&App{}).createCart(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == first.ID || fresh.Status != "open" || fresh.CheckoutCartID == nil || *fresh.CheckoutCartID != 453 {
		t.Fatalf("converted storefront session did not receive a fresh cart: %#v", fresh)
	}
	archived, err := dbCartGet(ctx.AppDB(), "cart-session", first.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != "converted" || archived.SessionToken == args["session_token"] {
		t.Fatalf("converted cart history was not archived: %#v", archived)
	}
}

func TestCheckoutCancelVoidsInvoiceAndCancelsSale(t *testing.T) {
	platform := newCommercePlatformStub()
	platform.responses["checkout:checkout_cancel"] = map[string]any{"session": map[string]any{"status": "cancelled"}}
	platform.responses["billing:invoices_void"] = map[string]any{"invoice": map[string]any{"status": "void"}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("cancel-sale"), tk.WithPlatform(platform))
	_, _, variant, cart := seedCommerceCart(t, ctx.AppDB(), "cancel-sale", "main", 0)
	if err := dbCartAddItem(ctx.AppDB(), "cancel-sale", cart.ID, variant, 1, 501); err != nil {
		t.Fatal(err)
	}
	checkout, err := dbCheckoutCreate(ctx.AppDB(), "cancel-sale", cart, 752, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbCheckoutInvoice(ctx.AppDB(), "cancel-sale", checkout.ID, 88, "INV-88"); err != nil {
		t.Fatal(err)
	}
	checkout, _ = dbCheckoutGet(ctx.AppDB(), "cancel-sale", checkout.ID)
	sale, err := dbSaleCreateFromCheckout(ctx.AppDB(), "cancel-sale", checkout)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := (&App{}).checkoutCancel(ctx, map[string]any{"checkout_id": checkout.ID}); err != nil {
		t.Fatal(err)
	}
	cancelledSale, err := dbSaleGet(ctx.AppDB(), "cancel-sale", sale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelledSale.Status != "cancelled" || cancelledSale.PaymentStatus != "unpaid" {
		t.Fatalf("cancelled sale has wrong state: %#v", cancelledSale)
	}
	if calls := countPlatformCalls(platform.calls, "billing", "invoices_void"); calls != 1 {
		t.Fatalf("Billing invoices_void calls=%d, want 1", calls)
	}
}

func TestStorefrontTemplatesAndAssetsAreSelfContained(t *testing.T) {
	manifest := commerceStorefrontManifest(&Store{ID: 7, Name: "Reference Store"})
	templates, ok := manifest["templates"].(map[string]any)
	if !ok || len(templates) < 7 {
		t.Fatalf("storefront templates missing: %#v", manifest["templates"])
	}
	funcs := template.FuncMap{
		"asset":    func(string) string { return "/" },
		"action":   func(string) string { return "/" },
		"href":     func(string) string { return "/" },
		"get":      func(any, string) any { return nil },
		"first":    func(any) any { return nil },
		"text":     func(any) string { return "" },
		"default":  func(fallback string, _ any) string { return fallback },
		"money":    func(any, any) string { return "" },
		"json":     func(any) template.JS { return "{}" },
		"safeHTML": func(string) template.HTML { return "" },
	}
	for name, source := range templates {
		if _, err := template.New(name).Funcs(funcs).Parse(source.(string)); err != nil {
			t.Errorf("parse storefront template %s: %v", name, err)
		}
	}
	assets := manifest["assets"].(map[string]any)
	js := assets["store.js"].(string)
	if strings.Contains(js, "{{") {
		t.Fatal("static storefront JavaScript contains an unrendered template expression")
	}
	if !strings.Contains(js, "event.key==='Enter'") || !strings.Contains(js, "input.disabled=true") {
		t.Fatal("cart quantity control does not persist Enter-key edits safely")
	}
	if !strings.Contains(js, "cartTitle(item)") {
		t.Fatal("storefront cart does not normalize repeated snapshot titles")
	}
	css := assets["store.css"].(string)
	if !strings.Contains(css, ".line-total{text-align:right") {
		t.Fatal("storefront line totals are not right-aligned")
	}
	if !strings.Contains(css, ".cart-total span:last-child{text-align:right") {
		t.Fatal("storefront cart total is not right-aligned")
	}
	checkout := templates["checkout"].(string)
	for _, expected := range []string{"checkout-layout", "data-checkout-summary", "data-billing-same", "data-quote-action", "data-bootstrap-action", "data-reserve-action", "data-payment-action", "https://js.stripe.com/clover/stripe.js", "payment-element", "data-checkout-step=\"1\"", "data-checkout-step=\"2\"", "data-checkout-step=\"3\"", "Continue to shipping"} {
		if !strings.Contains(checkout, expected) {
			t.Fatalf("checkout template missing %q", expected)
		}
	}
	if strings.Contains(checkout, "-&gt;") {
		t.Fatal("checkout primary actions use raw text arrows")
	}
	if strings.Contains(checkout, "site-header") || strings.Contains(checkout, "announcement") {
		t.Fatal("checkout uses the distraction-heavy storefront chrome")
	}
	actions := manifest["actions"].(map[string]any)
	if _, ok := actions["checkout_status"]; !ok {
		t.Fatal("storefront is missing the session-bound checkout status action")
	}
	policy := manifest["browser_policy"].(map[string]any)
	if !strings.Contains(fmt.Sprint(policy["script_origins"]), "js.stripe.com") {
		t.Fatalf("storefront browser policy does not allow Stripe.js: %#v", policy)
	}
	quote := actions["checkout_quote"].(map[string]any)
	quoteSteps := quote["steps"].([]any)
	if len(quoteSteps) != 2 || quoteSteps[1].(map[string]any)["tool"] != "commerce_checkout_quote" {
		t.Fatalf("checkout quote flow is invalid: %#v", quoteSteps)
	}
	reserve := actions["checkout_reserve"].(map[string]any)
	reserveSteps := reserve["steps"].([]any)
	wantReserveTools := []string{
		"commerce_cart_create",
		"commerce_checkout_quote",
		"commerce_checkout_start",
		"commerce_checkout_update",
	}
	if len(reserveSteps) != len(wantReserveTools) {
		t.Fatalf("checkout reserve steps=%d, want %d", len(reserveSteps), len(wantReserveTools))
	}
	for index, want := range wantReserveTools {
		if got := reserveSteps[index].(map[string]any)["tool"]; got != want {
			t.Fatalf("checkout reserve step %d=%v, want %s", index, got, want)
		}
	}
	payment := actions["checkout_payment"].(map[string]any)
	paymentSteps := payment["steps"].([]any)
	wantPaymentTools := []string{"commerce_checkout_bootstrap", "commerce_checkout_update", "commerce_checkout_pay"}
	for index, want := range wantPaymentTools {
		if got := paymentSteps[index].(map[string]any)["tool"]; got != want {
			t.Fatalf("checkout payment step %d=%v, want %s", index, got, want)
		}
	}
	if _, ok := actions["checkout_bootstrap"]; !ok {
		t.Fatal("storefront is missing durable checkout bootstrap")
	}
	for _, expected := range []string{"renderSummary", "renderShippingOptions", "setStep", "Continue to payment", "Calculating shipping", "Reserving your order", "data-country-select", "initCheckout", "createPaymentElement", "stripeActions.confirm", "sessionStorage", "data-payment-return"} {
		if !strings.Contains(js, expected) {
			t.Fatalf("checkout JavaScript missing %q", expected)
		}
	}
	for _, expected := range []string{".checkout-layout{display:grid", ".checkout-summary-column", ".checkout-progress", "[hidden]{display:none!important}", "@media(max-width:900px)"} {
		if !strings.Contains(css, expected) {
			t.Fatalf("checkout CSS missing %q", expected)
		}
	}
}

func TestCommercePaymentArgsUseTrustedStoreURL(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("payment-args"))
	store := &Store{
		ID: 7, PublicBaseURL: "https://shop.example/",
		PaymentProvider: "stripe", PaymentPresentation: "elements",
	}
	checkout := &CheckoutSession{ID: 12, Quote: map[string]any{"total_cents": 1200, "currency": "EUR"}}
	args, err := commercePaymentArgs(ctx, "payment-args", store, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if args["return_url"] != "https://shop.example/checkout/return" ||
		args["idempotency_key"] != "commerce-stripe-elements-v2-payment-args-12-"+checkoutPaymentFingerprint(checkout) {
		t.Fatalf("unexpected payment args: %#v", args)
	}
	if _, constrained := args["payment_method_types"]; constrained {
		t.Fatalf("payment methods should default to Stripe dynamic methods: %#v", args)
	}

	store.PaymentPresentation = "hosted"
	args, err = commercePaymentArgs(ctx, "payment-args", store, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if args["success_url"] != "https://shop.example/checkout/return" ||
		args["cancel_url"] != "https://shop.example/checkout" ||
		args["idempotency_key"] != "commerce-stripe-hosted-v2-payment-args-12-"+checkoutPaymentFingerprint(checkout) {
		t.Fatalf("unexpected hosted URLs: %#v", args)
	}
	changed := *checkout
	changed.Quote = map[string]any{"total_cents": 1400, "currency": "EUR"}
	changedArgs, err := commercePaymentArgs(ctx, "payment-args", store, &changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedArgs["idempotency_key"] == args["idempotency_key"] {
		t.Fatal("changed checkout quote reused its Stripe idempotency key")
	}
	if _, exists := args["expires_at"]; exists {
		t.Fatalf("payment args contain a retry-unstable expiry: %#v", args)
	}
	external := map[string]any{
		"current_step":    "payment",
		"buyer_details":   map[string]any{"email": "buyer@example.com", "phone": ""},
		"billing_address": map[string]any{"line1": "Main Street 1", "city": "Barcelona"},
	}
	retryPatch := map[string]any{
		"current_step":    "payment",
		"buyer_details":   map[string]any{"email": "buyer@example.com", "phone": ""},
		"billing_address": map[string]any{"line1": "Main Street 1", "city": "Barcelona"},
	}
	if !checkoutExternalPatchMatches(external, retryPatch) {
		t.Fatal("identical awaiting-payment update was not treated as idempotent")
	}
	retryPatch["billing_address"] = map[string]any{"line1": "Changed Street 2", "city": "Barcelona"}
	if checkoutExternalPatchMatches(external, retryPatch) {
		t.Fatal("changed awaiting-payment address was treated as idempotent")
	}
}

func TestHardeningMigrationReconcilesExistingDuplicates(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execMigration(t, db, "migrations/001_init.sql")
	execMigration(t, db, "migrations/004_store_payments.sql")
	store, err := dbStoreCreate(db, "upgrade", map[string]any{"slug": "main", "name": "Main"})
	if err != nil {
		t.Fatal(err)
	}
	cart, err := dbCartCreate(db, "upgrade", map[string]any{"store_id": store.ID, "session_token": "duplicate"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := db.Exec(`INSERT INTO commerce_checkout_sessions (project_id, store_id, cart_id, checkout_session_id) VALUES ('upgrade', ?, ?, 44)`, store.ID, cart.ID); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query(`SELECT id FROM commerce_checkout_sessions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var checkoutIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		checkoutIDs = append(checkoutIDs, id)
	}
	rows.Close()
	for _, checkoutID := range checkoutIDs {
		if _, err := db.Exec(`INSERT INTO commerce_sales (project_id, store_id, cart_id, checkout_id, invoice_id) VALUES ('upgrade', ?, ?, ?, 55)`, store.ID, cart.ID, checkoutID); err != nil {
			t.Fatal(err)
		}
	}

	execMigration(t, db, "migrations/002_harden_checkout.sql")
	var checkoutCount, linkedSales, invoicedSales int
	if err := db.QueryRow(`SELECT COUNT(*) FROM commerce_checkout_sessions`).Scan(&checkoutCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM commerce_sales WHERE checkout_id IS NOT NULL`).Scan(&linkedSales); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM commerce_sales WHERE invoice_id IS NOT NULL`).Scan(&invoicedSales); err != nil {
		t.Fatal(err)
	}
	if checkoutCount != 1 || linkedSales != 1 || invoicedSales != 1 {
		t.Fatalf("duplicates not reconciled: checkouts=%d linked_sales=%d invoiced_sales=%d", checkoutCount, linkedSales, invoicedSales)
	}
	if _, err := db.Exec(`INSERT INTO commerce_checkout_sessions (project_id, store_id, cart_id) VALUES ('upgrade', ?, ?)`, store.ID, cart.ID); err == nil {
		t.Fatal("checkout cart uniqueness was not enforced")
	}
}

func TestCollectionRejectsCrossStoreProduct(t *testing.T) {
	db := openCommerceTestDB(t)
	first, _ := dbStoreCreate(db, "proj", map[string]any{"slug": "one", "name": "One"})
	second, _ := dbStoreCreate(db, "proj", map[string]any{"slug": "two", "name": "Two"})
	collection, _ := dbCollectionCreate(db, "proj", map[string]any{"store_id": first.ID, "title": "Featured"})
	listing, _ := dbListingCreate(db, "proj", map[string]any{"store_id": second.ID, "title": "Wrong store"})
	if err := dbCollectionAddListing(db, "proj", collection.ID, listing.ID, 0); err == nil {
		t.Fatal("cross-store collection membership should fail")
	}
}

func TestCheckoutStartFreezesCommerceAdjustments(t *testing.T) {
	platform := newCommercePlatformStub()
	platform.responses["checkout:checkout_start"] = map[string]any{"session": map[string]any{
		"id": int64(401), "subtotal_cents": int64(4900), "total_cents": int64(4900), "currency": "USD",
	}}
	platform.responses["checkout:checkout_set_adjustments"] = platformResponseFunc(func(input map[string]any) (any, error) {
		if intArg(input, "shipping_cents") != 600 || intArg(input, "session_id") != 401 {
			return nil, errors.New("Commerce adjustments were not forwarded")
		}
		adjustments := mapArg(input, "adjustments")
		if strArg(adjustments, "service") != "standard" {
			return nil, errors.New("shipping quote snapshot was not forwarded")
		}
		return map[string]any{"session": map[string]any{
			"id": int64(401), "subtotal_cents": int64(4900), "shipping_cents": int64(600),
			"total_cents": int64(5500), "currency": "USD",
		}}, nil
	})
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-adjustments"), tk.WithPlatform(platform))
	_, _, variant, cart := seedCommerceCart(t, ctx.AppDB(), "proj-adjustments", "main", 0)
	if err := dbCartAddItem(ctx.AppDB(), "proj-adjustments", cart.ID, variant, 1, 501); err != nil {
		t.Fatal(err)
	}
	if err := dbCartApplyShipping(ctx.AppDB(), "proj-adjustments", cart.ID, 600, map[string]any{"service": "standard"}); err != nil {
		t.Fatal(err)
	}
	checkout, err := (&App{}).checkoutStart(ctx, map[string]any{"cart_id": cart.ID})
	if err != nil {
		t.Fatalf("start checkout: %v", err)
	}
	if checkout.CheckoutSessionID == nil || *checkout.CheckoutSessionID != 401 {
		t.Fatalf("unexpected checkout: %#v", checkout)
	}
}

func TestProviderOrderRequestsRemainProviderSpecific(t *testing.T) {
	sale := &Sale{
		ID: 71, CustomerEmail: "buyer@example.com", CustomerName: "Ada Lovelace",
		ShippingAddress: map[string]any{
			"address1": "1 Main St", "city": "Madrid", "postal_code": "28001", "country_code": "ES",
		},
	}
	line := dispatchLine{
		SaleItem: &SaleItem{SKU: "LOCAL-1", Quantity: 2},
		Source: &VariantSource{
			ExternalProductID: "product-1", ExternalVariantID: "123", ProviderSKU: "PROVIDER-1",
			Source: map[string]any{
				"attributes": map[string]any{"color": "black"},
				"assets":     []any{map[string]any{"printArea": "front", "url": "https://cdn.example/design.png"}},
				"sizing":     "fitPrintArea",
			},
		},
	}
	tests := []struct {
		provider string
		settings map[string]any
		assert   func(*testing.T, map[string]any)
	}{
		{"printful", map[string]any{}, func(t *testing.T, request map[string]any) {
			if intArg(anyMap(anySlice(request["items"])[0]), "sync_variant_id") != 123 || !boolArg(request, "confirm") {
				t.Fatalf("unexpected Printful request: %#v", request)
			}
		}},
		{"printify", map[string]any{"shop_id": "shop-9"}, func(t *testing.T, request map[string]any) {
			if strArg(request, "shop_id") != "shop-9" || intArg(anyMap(anySlice(request["line_items"])[0]), "variant_id") != 123 {
				t.Fatalf("unexpected Printify request: %#v", request)
			}
		}},
		{"bigbuy", map[string]any{}, func(t *testing.T, request map[string]any) {
			order := mapArg(request, "order")
			if strArg(order, "language") != "en" || strArg(anyMap(anySlice(order["products"])[0]), "reference") != "PROVIDER-1" {
				t.Fatalf("unexpected BigBuy request: %#v", request)
			}
		}},
		{"cjdropshipping", map[string]any{}, func(t *testing.T, request map[string]any) {
			if strArg(anyMap(anySlice(request["products"])[0]), "vid") != "123" ||
				strArg(request, "shippingAddress") != "1 Main St" ||
				strArg(request, "logisticName") != "CJPacket Ordinary" ||
				strArg(request, "fromCountryCode") != "CN" ||
				intArg(request, "payType") != 3 {
				t.Fatalf("unexpected CJ request: %#v", request)
			}
			if _, ok := request["shippingAddress"].(map[string]any); ok {
				t.Fatalf("CJ shippingAddress must be a string, not a nested object: %#v", request)
			}
		}},
		{"prodigi", map[string]any{"shipping_method": "Standard"}, func(t *testing.T, request map[string]any) {
			item := anyMap(anySlice(request["items"])[0])
			recipient := mapArg(request, "recipient")
			address := mapArg(recipient, "address")
			if strArg(request, "shippingMethod") != "Standard" ||
				strArg(item, "sku") != "PROVIDER-1" ||
				strArg(address, "postalOrZipCode") != "28001" ||
				len(anySlice(item["assets"])) != 1 {
				t.Fatalf("unexpected Prodigi request: %#v", request)
			}
			if _, ok := address["line2"]; ok {
				t.Fatalf("empty optional Prodigi address fields must be omitted: %#v", address)
			}
			if _, ok := recipient["phoneNumber"]; ok {
				t.Fatalf("empty optional Prodigi recipient fields must be omitted: %#v", recipient)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			var shipping map[string]any
			if test.provider == "cjdropshipping" {
				shipping = map[string]any{"logisticName": "CJPacket Ordinary"}
			}
			request, err := providerOrderRequest(test.provider, &ProviderPolicy{
				ConnectionID: 9, ProviderSlug: test.provider, Settings: test.settings,
			}, sale, []dispatchLine{line}, shipping)
			if err != nil {
				t.Fatal(err)
			}
			test.assert(t, request)
		})
	}
}

func TestNormalizeCJProductListV2AndDetailFallback(t *testing.T) {
	listRaw := map[string]any{
		"code": 200,
		"data": map[string]any{
			"content": []any{
				map[string]any{"productList": []any{
					map[string]any{"id": "cat-1", "nameEn": "Cat Toy", "bigImage": "https://example.com/cat.jpg"},
					map[string]any{"id": "cat-2", "nameEn": "Cat Bed"},
				}},
			},
		},
	}
	products := normalizeCJProducts(listRaw, false)
	if len(products) != 2 || products[0].ID != "cat-1" || products[0].Title != "Cat Toy" {
		t.Fatalf("unexpected CJ V2 products: %#v", products)
	}

	detailRaw := map[string]any{
		"product": map[string]any{"code": 200, "data": map[string]any{
			"pid": "cat-1", "productNameEn": "Cat Toy", "variants": []any{
				map[string]any{
					"vid": "variant-1", "variantSku": "CAT-1", "variantNameEn": "Blue",
					"variantSellPrice": 3.64, "variantSugSellPrice": 26.32,
				},
			},
		}},
		"variants": map[string]any{},
	}
	products = normalizeCJProducts(detailRaw, true)
	if len(products) != 1 || len(products[0].Variants) != 1 {
		t.Fatalf("unexpected CJ detail products: %#v", products)
	}
	variant := products[0].Variants[0]
	if variant.ID != "variant-1" || variant.CostCents != 364 || variant.SuggestedPrice != 2632 {
		t.Fatalf("unexpected CJ variant economics: %#v", variant)
	}
}

func TestNormalizeCJShippingOptions(t *testing.T) {
	bound := &sdk.BoundIntegration{AppSlug: "cjdropshipping", ConnectionID: 112}
	raw := map[string]any{"code": 200, "data": []any{
		map[string]any{"logisticName": "CJPacket Ordinary", "logisticPrice": 4.71, "logisticAging": "2-5"},
		map[string]any{"logisticName": "USPS+", "totalPostageFee": 7.25, "logisticAging": "4-8 days"},
	}}
	options := normalizeCJShippingOptions(bound, raw)
	if len(options) != 2 || options[0].AmountCents != 471 || options[0].Currency != "USD" {
		t.Fatalf("unexpected CJ shipping options: %#v", options)
	}
	if intArg(options[0].Raw, "estimated_days_min") != 2 || intArg(options[0].Raw, "estimated_days_max") != 5 {
		t.Fatalf("unexpected CJ shipping estimate: %#v", options[0].Raw)
	}
}

func TestCJBusinessErrorsAreNotTreatedAsSuccess(t *testing.T) {
	err := providerResponseError("cjdropshipping", map[string]any{
		"code": 1600101, "result": false, "message": "Interface not found",
	})
	if err == nil || !strings.Contains(err.Error(), "1600101") {
		t.Fatalf("expected CJ business error, got %v", err)
	}
	if err := providerResponseError("cjdropshipping", map[string]any{"code": 200, "result": true}); err != nil {
		t.Fatalf("unexpected CJ success error: %v", err)
	}
}

func TestSanitizeProdigiOrderRequestRemovesLegacyEmptyOptionalFields(t *testing.T) {
	request := map[string]any{
		"recipient": map[string]any{
			"email": "buyer@example.com", "phoneNumber": " ",
			"address": map[string]any{
				"line1": "1 Main St", "line2": "", "townOrCity": "Madrid",
				"stateOrCounty": " ", "postalOrZipCode": "28001", "countryCode": "ES",
			},
		},
	}

	got := sanitizeProdigiOrderRequest(request)
	recipient := mapArg(got, "recipient")
	address := mapArg(recipient, "address")
	if _, ok := address["line2"]; ok {
		t.Fatalf("empty line2 was not removed: %#v", address)
	}
	if _, ok := address["stateOrCounty"]; ok {
		t.Fatalf("empty stateOrCounty was not removed: %#v", address)
	}
	if _, ok := recipient["phoneNumber"]; ok {
		t.Fatalf("empty phoneNumber was not removed: %#v", recipient)
	}
	if strArg(address, "line1") != "1 Main St" || strArg(recipient, "email") != "buyer@example.com" {
		t.Fatalf("required/non-empty fields changed: %#v", got)
	}
}

func TestProdigiProductNormalizationAndDesignIdentity(t *testing.T) {
	raw := map[string]any{
		"outcome": "Ok",
		"product": map[string]any{
			"sku":         "GLOBAL-TEE-01",
			"description": "Organic T-shirt",
			"printAreas":  map[string]any{"front": map[string]any{"required": true}},
			"variants": []any{
				map[string]any{"attributes": map[string]any{"size": "M", "color": "Black"}, "shipsTo": []any{"ES", "US"}},
				map[string]any{"attributes": map[string]any{"size": "L", "color": "Black"}, "shipsTo": []any{"ES", "US"}},
			},
		},
	}
	products := normalizeProdigiProducts(raw)
	if len(products) != 1 || products[0].ID != "GLOBAL-TEE-01" || len(products[0].Variants) != 2 {
		t.Fatalf("unexpected normalized product: %#v", products)
	}
	firstID := products[0].Variants[0].ID
	if firstID == "" || firstID == products[0].Variants[1].ID {
		t.Fatalf("Prodigi attribute variants need stable distinct ids: %#v", products[0].Variants)
	}

	input := map[string]any{
		"design_key": "feliqo-sleep-club",
		"assets": []any{
			map[string]any{"printArea": "front", "url": "https://cdn.example/feliqo.png"},
		},
	}
	prepared, err := prepareProdigiVariants(products[0].Variants, input)
	if err != nil {
		t.Fatal(err)
	}
	if prepared[0].ID != firstID+"@feliqo-sleep-club" {
		t.Fatalf("design identity missing from variant id: %q", prepared[0].ID)
	}
	source := anyMap(prepared[0].Raw)
	if strArg(source, "sizing") != "fitPrintArea" || len(anySlice(source["assets"])) != 1 {
		t.Fatalf("Prodigi source JSON lost provider-specific fields: %#v", source)
	}
}

func TestProdigiQuoteAndStatusNormalization(t *testing.T) {
	bound := &sdk.BoundIntegration{AppSlug: "prodigi", ConnectionID: 42}
	raw := map[string]any{
		"outcome": "Ok",
		"quotes": []any{
			map[string]any{
				"shipmentMethod": "Budget",
				"costSummary": map[string]any{
					"shipping": map[string]any{"amount": "4.25", "currency": "EUR"},
				},
			},
		},
	}
	options := normalizeProdigiShippingOptions(bound, raw, "USD")
	if len(options) != 1 || options[0].AmountCents != 425 || options[0].Currency != "EUR" || options[0].ID != "Budget" {
		t.Fatalf("unexpected Prodigi quote: %#v", options)
	}
	if status := normalizeProdigiOrderStatus("Complete"); status != "shipped" {
		t.Fatalf("Complete normalized to %q, want shipped", status)
	}
	if status := normalizeProdigiOrderStatus("InProgress"); status != "accepted" {
		t.Fatalf("InProgress normalized to %q, want accepted", status)
	}
	shipped := map[string]any{"order": map[string]any{
		"status":    map[string]any{"stage": "InProgress"},
		"shipments": []any{map[string]any{"status": "Shipped"}},
	}}
	if status := prodigiOrderStatus(shipped); status != "shipped" {
		t.Fatalf("shipped fulfillment normalized to %q, want shipped", status)
	}
}

func TestProviderStorageUsesThreeCommerceTables(t *testing.T) {
	db := openCommerceTestDB(t)
	rows, err := db.Query(`SELECT name FROM sqlite_master
		WHERE type='table' AND name IN ('commerce_provider_policies','commerce_variant_sources','commerce_dispatch_jobs')
		ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if len(names) != 3 {
		t.Fatalf("provider storage tables=%v, want exactly the three minimal Commerce tables", names)
	}
}

func TestSaleFulfillmentStatusAggregatesProviderDispatches(t *testing.T) {
	db := openCommerceTestDB(t)
	store, err := dbStoreCreate(db, "aggregate", map[string]any{"slug": "main", "name": "Main"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO commerce_sales
		(project_id, store_id, status, payment_status, fulfillment_status)
		VALUES ('aggregate', ?, 'paid', 'paid', 'unsubmitted')`, store.ID)
	if err != nil {
		t.Fatal(err)
	}
	saleID, _ := result.LastInsertId()
	for index, status := range []string{"delivered", "submitted"} {
		if _, err := db.Exec(`INSERT INTO commerce_dispatch_jobs
			(project_id, store_id, sale_id, order_id, connection_id, provider_slug, status, idempotency_key)
			VALUES ('aggregate', ?, ?, 1, ?, ?, ?, ?)`,
			store.ID, saleID, index+1, []string{"printful", "bigbuy"}[index], status, fmt.Sprintf("dispatch-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := updateSaleFulfillmentStatus(db, "aggregate", saleID); err != nil {
		t.Fatal(err)
	}
	sale, _ := dbSaleGet(db, "aggregate", saleID)
	if sale.FulfillmentStatus != "partially_fulfilled" {
		t.Fatalf("sale status=%q, want partially_fulfilled", sale.FulfillmentStatus)
	}
	if _, err := db.Exec(`UPDATE commerce_dispatch_jobs SET status='delivered' WHERE project_id='aggregate' AND sale_id=?`, saleID); err != nil {
		t.Fatal(err)
	}
	if err := updateSaleFulfillmentStatus(db, "aggregate", saleID); err != nil {
		t.Fatal(err)
	}
	sale, _ = dbSaleGet(db, "aggregate", saleID)
	if sale.FulfillmentStatus != "delivered" {
		t.Fatalf("sale status=%q, want delivered", sale.FulfillmentStatus)
	}
}

func openCommerceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	for _, path := range files {
		execMigration(t, db, path)
	}
	return db
}

func execMigration(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", path, err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("exec migration %s: %v\n%s", path, err, strings.TrimSpace(string(body)))
	}
}
