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

	checkout, err := dbCheckoutCreate(db, pid, cart, 301, []int64{401, 402})
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
	checkout, err := dbCheckoutCreate(ctx.AppDB(), "proj-paid", cart, 401, nil)
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
	platform.responses["checkout:cart_create"] = map[string]any{"cart": map[string]any{"id": 451}}
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
}

func TestHardeningMigrationReconcilesExistingDuplicates(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execMigration(t, db, "migrations/001_init.sql")
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
			if strArg(anyMap(anySlice(request["products"])[0]), "vid") != "123" {
				t.Fatalf("unexpected CJ request: %#v", request)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			request, err := providerOrderRequest(test.provider, &ProviderPolicy{
				ConnectionID: 9, ProviderSlug: test.provider, Settings: test.settings,
			}, sale, []dispatchLine{line})
			if err != nil {
				t.Fatal(err)
			}
			test.assert(t, request)
		})
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
