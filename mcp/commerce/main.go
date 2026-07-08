package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: commerce
display_name: Commerce
version: 0.1.0
description: Shopify-style multi-store commerce layer for Apteva.
author: Apteva
scopes: [project, global]
requires:
  permissions: [db.write.app, platform.apps.call]
  apps:
    - { name: catalog, optional: false }
    - { name: checkout, optional: false }
    - { name: inventory, optional: true }
    - { name: orders, optional: true }
    - { name: billing, optional: true }
    - { name: storage, optional: true }
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: commerce_stores_create, description: "Create a store." }
    - { name: commerce_stores_list, description: "List stores." }
    - { name: commerce_stores_get, description: "Fetch a store." }
    - { name: commerce_stores_update, description: "Patch a store." }
    - { name: commerce_products_create, description: "Create a storefront listing." }
    - { name: commerce_products_list, description: "List storefront listings." }
    - { name: commerce_products_get, description: "Fetch one listing with variants." }
    - { name: commerce_products_publish, description: "Publish a listing." }
    - { name: commerce_products_archive, description: "Archive a listing." }
    - { name: commerce_variants_create, description: "Create a listing variant." }
    - { name: commerce_variants_update, description: "Patch a listing variant." }
    - { name: commerce_collections_create, description: "Create a collection." }
    - { name: commerce_collections_list, description: "List collections." }
    - { name: commerce_collections_add_product, description: "Add a listing to a collection." }
    - { name: commerce_cart_create, description: "Create a Commerce cart backed by Checkout." }
    - { name: commerce_cart_get, description: "Fetch a Commerce cart." }
    - { name: commerce_cart_add_item, description: "Add a variant to a Commerce cart." }
    - { name: commerce_cart_set_quantity, description: "Set Commerce cart item quantity." }
    - { name: commerce_checkout_start, description: "Reserve inventory and start Checkout." }
    - { name: commerce_checkout_update, description: "Update buyer info on Checkout." }
    - { name: commerce_checkout_pay, description: "Submit Checkout for payment and create a sale." }
    - { name: commerce_checkout_mark_paid, description: "Commit reservations and create an order." }
    - { name: commerce_sales_list, description: "List sales." }
    - { name: commerce_sales_get, description: "Fetch a sale." }
  ui_panels:
    - slot: project.page
      label: Commerce
      icon: shopping-bag
      entry: /ui/CommercePanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/commerce
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/commerce.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("commerce requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("commerce mounted", "project_id", projectScope(ctx))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/admin/summary", Handler: a.handleSummary},
		{Pattern: "/admin/stores", Handler: a.handleStores},
		{Pattern: "/admin/products", Handler: a.handleProducts},
		{Pattern: "/admin/products/", Handler: a.handleProduct},
		{Pattern: "/admin/carts", Handler: a.handleCarts},
		{Pattern: "/admin/sales", Handler: a.handleSales},
		{Pattern: "/s/", Handler: a.handlePublic, NoAuth: true},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "commerce_stores_create", Description: "Create a store. Args: slug, name, default_currency?, default_locale?, timezone?.", InputSchema: schemaObject(storeProps(), []string{"slug", "name"}), Handler: a.toolStoresCreate},
		{Name: "commerce_stores_list", Description: "List stores.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolStoresList},
		{Name: "commerce_stores_get", Description: "Fetch one store by id or slug.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "slug": typ("string")}, nil), Handler: a.toolStoresGet},
		{Name: "commerce_stores_update", Description: "Patch a store. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolStoresUpdate},
		{Name: "commerce_products_create", Description: "Create a storefront listing and optional first variant. Args: store_id, title, handle?, description_html?, catalog_product_id?, price_cents?, sku?, inventory_item_id?.", InputSchema: schemaObject(productCreateProps(), []string{"title"}), Handler: a.toolProductsCreate},
		{Name: "commerce_products_list", Description: "List storefront listings. Args: store_id?, status?, q?, limit?.", InputSchema: schemaObject(listingFilterProps(), nil), Handler: a.toolProductsList},
		{Name: "commerce_products_get", Description: "Fetch one listing with variants. Args: id? or handle? plus store_id?.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "handle": typ("string"), "store_id": typ("integer")}, nil), Handler: a.toolProductsGet},
		{Name: "commerce_products_publish", Description: "Publish a listing. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolProductsPublish},
		{Name: "commerce_products_archive", Description: "Archive a listing. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolProductsArchive},
		{Name: "commerce_variants_create", Description: "Create a listing variant. Args: listing_id, sku?, title?, catalog_price_id?, inventory_item_id?, price_cents?, currency?.", InputSchema: schemaObject(variantProps(), []string{"listing_id"}), Handler: a.toolVariantsCreate},
		{Name: "commerce_variants_update", Description: "Patch a variant. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolVariantsUpdate},
		{Name: "commerce_collections_create", Description: "Create a collection. Args: store_id, handle?, title, description_html?.", InputSchema: schemaObject(collectionProps(), []string{"title"}), Handler: a.toolCollectionsCreate},
		{Name: "commerce_collections_list", Description: "List collections. Args: store_id?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer")}, nil), Handler: a.toolCollectionsList},
		{Name: "commerce_collections_add_product", Description: "Add a listing to a collection. Args: collection_id, listing_id.", InputSchema: schemaObject(map[string]any{"collection_id": typ("integer"), "listing_id": typ("integer"), "sort_order": typ("integer")}, []string{"collection_id", "listing_id"}), Handler: a.toolCollectionsAddProduct},
		{Name: "commerce_cart_create", Description: "Create a Commerce cart backed by Checkout. Args: store_id? or store_slug?, session_token?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "store_slug": typ("string"), "session_token": typ("string")}, nil), Handler: a.toolCartCreate},
		{Name: "commerce_cart_get", Description: "Fetch a Commerce cart with items. Args: cart_id? or session_token? plus store_id?.", InputSchema: schemaObject(map[string]any{"cart_id": typ("integer"), "session_token": typ("string"), "store_id": typ("integer")}, nil), Handler: a.toolCartGet},
		{Name: "commerce_cart_add_item", Description: "Add a Commerce variant to a cart and delegate item snapshotting to Checkout. Args: cart_id, variant_id, quantity?.", InputSchema: schemaObject(map[string]any{"cart_id": typ("integer"), "variant_id": typ("integer"), "quantity": typ("number")}, []string{"cart_id", "variant_id"}), Handler: a.toolCartAddItem},
		{Name: "commerce_cart_set_quantity", Description: "Set a Commerce cart item's quantity. Args: cart_id, item_id, quantity.", InputSchema: schemaObject(map[string]any{"cart_id": typ("integer"), "item_id": typ("integer"), "quantity": typ("number")}, []string{"cart_id", "item_id", "quantity"}), Handler: a.toolCartSetQuantity},
		{Name: "commerce_checkout_start", Description: "Reserve inventory where configured, then start the backing Checkout session. Args: cart_id.", InputSchema: schemaObject(map[string]any{"cart_id": typ("integer")}, []string{"cart_id"}), Handler: a.toolCheckoutStart},
		{Name: "commerce_checkout_update", Description: "Update buyer contact/address on Commerce and Checkout sessions. Args: checkout_id, patch.", InputSchema: schemaObject(map[string]any{"checkout_id": typ("integer"), "patch": typ("object")}, []string{"checkout_id", "patch"}), Handler: a.toolCheckoutUpdate},
		{Name: "commerce_checkout_pay", Description: "Submit the backing Checkout session for payment and create a Commerce sale record. Args: checkout_id.", InputSchema: schemaObject(map[string]any{"checkout_id": typ("integer")}, []string{"checkout_id"}), Handler: a.toolCheckoutPay},
		{Name: "commerce_checkout_mark_paid", Description: "Mark a sale paid after Billing confirms payment; commits inventory reservations and creates an Orders record when available. Args: sale_id.", InputSchema: schemaObject(map[string]any{"sale_id": typ("integer")}, []string{"sale_id"}), Handler: a.toolCheckoutMarkPaid},
		{Name: "commerce_sales_list", Description: "List sales. Args: store_id?, status?, payment_status?, limit?.", InputSchema: schemaObject(salesFilterProps(), nil), Handler: a.toolSalesList},
		{Name: "commerce_sales_get", Description: "Fetch one sale. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolSalesGet},
	}
}

type Store struct {
	ID                int64          `json:"id"`
	Slug              string         `json:"slug"`
	Name              string         `json:"name"`
	Status            string         `json:"status"`
	PublicBaseURL     string         `json:"public_base_url"`
	DefaultCurrency   string         `json:"default_currency"`
	DefaultLocale     string         `json:"default_locale"`
	Timezone          string         `json:"timezone"`
	OrderNumberFormat string         `json:"order_number_format"`
	CheckoutMode      string         `json:"checkout_mode"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
}

type Listing struct {
	ID               int64          `json:"id"`
	StoreID          int64          `json:"store_id"`
	CatalogProductID *int64         `json:"catalog_product_id,omitempty"`
	Handle           string         `json:"handle"`
	Title            string         `json:"title"`
	DescriptionHTML  string         `json:"description_html"`
	Vendor           string         `json:"vendor"`
	ProductType      string         `json:"product_type"`
	Status           string         `json:"status"`
	SEOTitle         string         `json:"seo_title"`
	SEODescription   string         `json:"seo_description"`
	FeaturedMediaID  *int64         `json:"featured_media_id,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	Variants         []*Variant     `json:"variants,omitempty"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
}

type Variant struct {
	ID                  int64          `json:"id"`
	StoreID             int64          `json:"store_id"`
	ListingID           int64          `json:"listing_id"`
	CatalogPriceID      *int64         `json:"catalog_price_id,omitempty"`
	InventoryItemID     *int64         `json:"inventory_item_id,omitempty"`
	SKU                 string         `json:"sku"`
	Title               string         `json:"title"`
	Option1             string         `json:"option1"`
	Option2             string         `json:"option2"`
	Option3             string         `json:"option3"`
	PriceCents          int64          `json:"price_cents"`
	CompareAtPriceCents int64          `json:"compare_at_price_cents"`
	Currency            string         `json:"currency"`
	Taxable             bool           `json:"taxable"`
	RequiresShipping    bool           `json:"requires_shipping"`
	SortOrder           int            `json:"sort_order"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	CreatedAt           string         `json:"created_at"`
	UpdatedAt           string         `json:"updated_at"`
}

type Collection struct {
	ID              int64  `json:"id"`
	StoreID         int64  `json:"store_id"`
	Handle          string `json:"handle"`
	Title           string `json:"title"`
	DescriptionHTML string `json:"description_html"`
	Status          string `json:"status"`
	SortOrder       int    `json:"sort_order"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type Cart struct {
	ID             int64       `json:"id"`
	StoreID        int64       `json:"store_id"`
	CheckoutCartID *int64      `json:"checkout_cart_id,omitempty"`
	SessionToken   string      `json:"session_token"`
	Status         string      `json:"status"`
	SubtotalCents  int64       `json:"subtotal_cents"`
	DiscountCents  int64       `json:"discount_cents"`
	TaxCents       int64       `json:"tax_cents"`
	ShippingCents  int64       `json:"shipping_cents"`
	TotalCents     int64       `json:"total_cents"`
	Currency       string      `json:"currency"`
	Items          []*CartItem `json:"items,omitempty"`
	CreatedAt      string      `json:"created_at"`
	UpdatedAt      string      `json:"updated_at"`
}

type CartItem struct {
	ID              int64   `json:"id"`
	CartID          int64   `json:"cart_id"`
	CheckoutItemID  *int64  `json:"checkout_item_id,omitempty"`
	VariantID       int64   `json:"variant_id"`
	ListingID       int64   `json:"listing_id"`
	InventoryItemID *int64  `json:"inventory_item_id,omitempty"`
	CatalogPriceID  *int64  `json:"catalog_price_id,omitempty"`
	SKU             string  `json:"sku"`
	TitleSnapshot   string  `json:"title_snapshot"`
	UnitAmountCents int64   `json:"unit_amount_cents"`
	Currency        string  `json:"currency"`
	Quantity        float64 `json:"quantity"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

type CheckoutSession struct {
	ID                int64   `json:"id"`
	StoreID           int64   `json:"store_id"`
	CartID            int64   `json:"cart_id"`
	CheckoutSessionID *int64  `json:"checkout_session_id,omitempty"`
	Status            string  `json:"status"`
	ReservationIDs    []int64 `json:"reservation_ids"`
	InvoiceID         *int64  `json:"invoice_id,omitempty"`
	InvoiceNumber     string  `json:"invoice_number"`
	CustomerEmail     string  `json:"customer_email"`
	CustomerName      string  `json:"customer_name"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

type Sale struct {
	ID                int64  `json:"id"`
	StoreID           int64  `json:"store_id"`
	CartID            *int64 `json:"cart_id,omitempty"`
	CheckoutID        *int64 `json:"checkout_id,omitempty"`
	CheckoutSessionID *int64 `json:"checkout_session_id,omitempty"`
	InvoiceID         *int64 `json:"invoice_id,omitempty"`
	InvoiceNumber     string `json:"invoice_number"`
	OrderID           *int64 `json:"order_id,omitempty"`
	Status            string `json:"status"`
	PaymentStatus     string `json:"payment_status"`
	FulfillmentStatus string `json:"fulfillment_status"`
	SubtotalCents     int64  `json:"subtotal_cents"`
	DiscountCents     int64  `json:"discount_cents"`
	TaxCents          int64  `json:"tax_cents"`
	ShippingCents     int64  `json:"shipping_cents"`
	TotalCents        int64  `json:"total_cents"`
	Currency          string `json:"currency"`
	CustomerEmail     string `json:"customer_email"`
	CustomerName      string `json:"customer_name"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	PaidAt            string `json:"paid_at,omitempty"`
}

func (a *App) toolStoresCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	store, err := dbStoreCreate(ctx.AppDB(), projectScope(ctx), args)
	if err == nil {
		ctx.Emit("commerce.store.created", map[string]any{"store_id": store.ID, "slug": store.Slug})
	}
	return map[string]any{"store": store}, err
}

func (a *App) toolStoresList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	stores, err := dbStoresList(ctx.AppDB(), projectScope(ctx))
	return map[string]any{"stores": stores, "count": len(stores)}, err
}

func (a *App) toolStoresGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	store, err := resolveStore(ctx.AppDB(), projectScope(ctx), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"store": store}, nil
}

func (a *App) toolStoresUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	store, err := dbStoreUpdate(ctx.AppDB(), projectScope(ctx), intArg(args, "id"), mapArg(args, "patch"))
	return map[string]any{"store": store}, err
}

func (a *App) toolProductsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx)
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	cp := copyMap(args)
	cp["store_id"] = store.ID
	if intArg(cp, "catalog_product_id") == 0 {
		productID, priceID, err := a.ensureCatalogProductAndPrice(ctx, pid, store, cp)
		if err != nil {
			return nil, err
		}
		cp["catalog_product_id"] = productID
		if priceID != 0 {
			cp["catalog_price_id"] = priceID
		}
	}
	listing, err := dbListingCreate(ctx.AppDB(), pid, cp)
	if err != nil {
		return nil, err
	}
	if intArg(cp, "catalog_price_id") != 0 || intArg(cp, "price_cents") != 0 || strArg(cp, "sku") != "" {
		cp["listing_id"] = listing.ID
		if _, err := dbVariantCreate(ctx.AppDB(), pid, cp); err != nil {
			return nil, err
		}
	}
	listing, _ = dbListingGet(ctx.AppDB(), pid, listing.ID, true)
	ctx.Emit("commerce.product.created", map[string]any{"listing_id": listing.ID, "store_id": listing.StoreID})
	return map[string]any{"product": listing}, nil
}

func (a *App) toolProductsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbListingsList(ctx.AppDB(), projectScope(ctx), args)
	return map[string]any{"products": out, "count": len(out)}, err
}

func (a *App) toolProductsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := resolveListing(ctx.AppDB(), projectScope(ctx), args)
	return map[string]any{"product": out}, err
}

func (a *App) toolProductsPublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbListingStatus(ctx.AppDB(), projectScope(ctx), intArg(args, "id"), "active")
	if err == nil {
		ctx.Emit("commerce.product.published", map[string]any{"listing_id": out.ID, "store_id": out.StoreID})
	}
	return map[string]any{"product": out}, err
}

func (a *App) toolProductsArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbListingStatus(ctx.AppDB(), projectScope(ctx), intArg(args, "id"), "archived")
	return map[string]any{"product": out}, err
}

func (a *App) toolVariantsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbVariantCreate(ctx.AppDB(), projectScope(ctx), args)
	return map[string]any{"variant": out}, err
}

func (a *App) toolVariantsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbVariantUpdate(ctx.AppDB(), projectScope(ctx), intArg(args, "id"), mapArg(args, "patch"))
	return map[string]any{"variant": out}, err
}

func (a *App) toolCollectionsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx)
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	cp := copyMap(args)
	cp["store_id"] = store.ID
	out, err := dbCollectionCreate(ctx.AppDB(), pid, cp)
	return map[string]any{"collection": out}, err
}

func (a *App) toolCollectionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbCollectionsList(ctx.AppDB(), projectScope(ctx), args)
	return map[string]any{"collections": out, "count": len(out)}, err
}

func (a *App) toolCollectionsAddProduct(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	err := dbCollectionAddListing(ctx.AppDB(), projectScope(ctx), intArg(args, "collection_id"), intArg(args, "listing_id"), int(intArg(args, "sort_order")))
	return map[string]any{"ok": err == nil}, err
}

func (a *App) toolCartCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cart, err := a.createCart(ctx, args)
	return map[string]any{"cart": cart}, err
}

func (a *App) toolCartGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cart, err := resolveCart(ctx.AppDB(), projectScope(ctx), args)
	return map[string]any{"cart": cart}, err
}

func (a *App) toolCartAddItem(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cart, err := a.addCartItem(ctx, args)
	return map[string]any{"cart": cart}, err
}

func (a *App) toolCartSetQuantity(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cart, err := a.setCartItemQuantity(ctx, args)
	return map[string]any{"cart": cart}, err
}

func (a *App) toolCheckoutStart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := a.checkoutStart(ctx, args)
	if err == nil {
		ctx.Emit("commerce.checkout.started", map[string]any{"checkout_id": out.ID, "cart_id": out.CartID, "store_id": out.StoreID})
	}
	return map[string]any{"checkout": out}, err
}

func (a *App) toolCheckoutUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := a.checkoutUpdate(ctx, args)
	return map[string]any{"checkout": out}, err
}

func (a *App) toolCheckoutPay(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	sale, checkout, err := a.checkoutPay(ctx, args)
	if err == nil {
		ctx.Emit("commerce.sale.created", map[string]any{"sale_id": sale.ID, "checkout_id": checkout.ID, "store_id": sale.StoreID})
	}
	return map[string]any{"sale": sale, "checkout": checkout}, err
}

func (a *App) toolCheckoutMarkPaid(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	sale, err := a.markSalePaid(ctx, args)
	if err == nil {
		ctx.Emit("commerce.sale.paid", map[string]any{"sale_id": sale.ID, "store_id": sale.StoreID})
	}
	return map[string]any{"sale": sale}, err
}

func (a *App) toolSalesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbSalesList(ctx.AppDB(), projectScope(ctx), args)
	return map[string]any{"sales": out, "count": len(out)}, err
}

func (a *App) toolSalesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbSaleGet(ctx.AppDB(), projectScope(ctx), intArg(args, "id"))
	if err != nil || out == nil {
		return nil, firstErr(err, errors.New("sale not found"))
	}
	return map[string]any{"sale": out}, nil
}

func (a *App) ensureCatalogProductAndPrice(ctx *sdk.AppCtx, pid string, store *Store, args map[string]any) (int64, int64, error) {
	if ctx.PlatformAPI() == nil {
		return 0, 0, errors.New("platform API unavailable; pass catalog_product_id/catalog_price_id or install catalog")
	}
	var productResp map[string]any
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_products_create", map[string]any{
		"_project_id":  pid,
		"name":         strArg(args, "title"),
		"type":         firstNonEmpty(strArg(args, "catalog_type"), "one_time"),
		"slug":         firstNonEmpty(strArg(args, "handle"), slugify(strArg(args, "title"))),
		"description":  strArg(args, "description_html"),
		"category":     strArg(args, "product_type"),
		"tax_category": firstNonEmpty(strArg(args, "tax_category"), "standard"),
		"metadata":     map[string]any{"source": "commerce", "store_id": store.ID},
	}, &productResp); err != nil {
		return 0, 0, fmt.Errorf("create catalog product: %w", err)
	}
	productID := intArg(unwrap(productResp, "product"), "id")
	if productID == 0 {
		productID = intArg(productResp, "id")
	}
	if productID == 0 {
		return 0, 0, errors.New("catalog product response missing id")
	}
	priceCents := intArg(args, "price_cents")
	if priceCents == 0 {
		return productID, 0, nil
	}
	var priceResp map[string]any
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_prices_create", map[string]any{
		"_project_id":       pid,
		"product_id":        productID,
		"unit_amount_cents": priceCents,
		"currency":          firstNonEmpty(strArg(args, "currency"), store.DefaultCurrency),
		"nickname":          firstNonEmpty(strArg(args, "variant_title"), strArg(args, "title")),
		"tax_inclusive":     boolArg(args, "tax_inclusive"),
		"metadata":          map[string]any{"source": "commerce", "store_id": store.ID},
	}, &priceResp); err != nil {
		return productID, 0, fmt.Errorf("create catalog price: %w", err)
	}
	priceID := intArg(unwrap(priceResp, "price"), "id")
	if priceID == 0 {
		priceID = intArg(priceResp, "id")
	}
	return productID, priceID, nil
}

func (a *App) createCart(ctx *sdk.AppCtx, args map[string]any) (*Cart, error) {
	pid := projectScope(ctx)
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	token := firstNonEmpty(strArg(args, "session_token"), newToken())
	var checkoutResp map[string]any
	var checkoutCartID int64
	if ctx.PlatformAPI() != nil {
		err := ctx.PlatformAPI().CallAppResult("checkout", "cart_create", map[string]any{
			"_project_id":   pid,
			"session_token": token,
			"metadata":      map[string]any{"source": "commerce", "store_id": store.ID},
		}, &checkoutResp)
		if err != nil {
			return nil, fmt.Errorf("create checkout cart: %w", err)
		}
		checkoutCartID = intArg(unwrap(checkoutResp, "cart"), "id")
		if checkoutCartID == 0 {
			checkoutCartID = intArg(unwrap(checkoutResp, "cart"), "cart_id")
		}
	}
	cart, err := dbCartCreate(ctx.AppDB(), pid, map[string]any{"store_id": store.ID, "session_token": token, "checkout_cart_id": checkoutCartID, "currency": store.DefaultCurrency})
	if err != nil {
		return nil, err
	}
	return dbCartGet(ctx.AppDB(), pid, cart.ID, true)
}

func (a *App) addCartItem(ctx *sdk.AppCtx, args map[string]any) (*Cart, error) {
	pid := projectScope(ctx)
	cart, err := resolveCart(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	variant, err := dbVariantGet(ctx.AppDB(), pid, intArg(args, "variant_id"))
	if err != nil || variant == nil {
		return nil, firstErr(err, errors.New("variant not found"))
	}
	if variant.CatalogPriceID == nil || *variant.CatalogPriceID == 0 {
		return nil, errors.New("variant must have catalog_price_id before it can be added to cart")
	}
	qty := floatArg(args, "quantity", 1)
	if qty <= 0 {
		return nil, errors.New("quantity must be positive")
	}
	if variant.InventoryItemID != nil && ctx.PlatformAPI() != nil {
		var avail map[string]any
		if err := ctx.PlatformAPI().CallAppResult("inventory", "inventory_availability_check", map[string]any{"_project_id": pid, "item_id": *variant.InventoryItemID, "quantity": qty}, &avail); err != nil {
			return nil, fmt.Errorf("check inventory: %w", err)
		}
		if ok, _ := avail["can_reserve"].(bool); !ok {
			return nil, errors.New("insufficient inventory available")
		}
	}
	var checkoutCart map[string]any
	if cart.CheckoutCartID != nil && ctx.PlatformAPI() != nil {
		if err := ctx.PlatformAPI().CallAppResult("checkout", "cart_add_item", map[string]any{"_project_id": pid, "cart_id": *cart.CheckoutCartID, "price_id": *variant.CatalogPriceID, "quantity": qty}, &checkoutCart); err != nil {
			return nil, fmt.Errorf("checkout cart add item: %w", err)
		}
	}
	if err := dbCartAddItem(ctx.AppDB(), pid, cart.ID, variant, qty); err != nil {
		return nil, err
	}
	return dbCartGet(ctx.AppDB(), pid, cart.ID, true)
}

func (a *App) setCartItemQuantity(ctx *sdk.AppCtx, args map[string]any) (*Cart, error) {
	pid := projectScope(ctx)
	cart, err := resolveCart(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if err := dbCartSetQuantity(ctx.AppDB(), pid, cart.ID, intArg(args, "item_id"), floatArg(args, "quantity", -1)); err != nil {
		return nil, err
	}
	return dbCartGet(ctx.AppDB(), pid, cart.ID, true)
}

func (a *App) checkoutStart(ctx *sdk.AppCtx, args map[string]any) (*CheckoutSession, error) {
	pid := projectScope(ctx)
	cart, err := resolveCart(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if len(cart.Items) == 0 {
		return nil, errors.New("cannot checkout an empty cart")
	}
	resIDs := []int64{}
	if ctx.PlatformAPI() != nil {
		for _, it := range cart.Items {
			if it.InventoryItemID == nil {
				continue
			}
			var res map[string]any
			if err := ctx.PlatformAPI().CallAppResult("inventory", "inventory_reserve", map[string]any{
				"_project_id":    pid,
				"item_id":        *it.InventoryItemID,
				"quantity":       it.Quantity,
				"reference_app":  "commerce",
				"reference_type": "cart",
				"reference_id":   fmt.Sprint(cart.ID),
				"metadata":       map[string]any{"cart_item_id": it.ID, "variant_id": it.VariantID},
			}, &res); err != nil {
				return nil, fmt.Errorf("reserve inventory for cart item %d: %w", it.ID, err)
			}
			if id := intArg(unwrap(res, "reservation"), "id"); id != 0 {
				resIDs = append(resIDs, id)
			}
		}
	}
	var checkoutResp map[string]any
	var checkoutSessionID int64
	if cart.CheckoutCartID != nil && ctx.PlatformAPI() != nil {
		if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_start", map[string]any{"_project_id": pid, "cart_id": *cart.CheckoutCartID}, &checkoutResp); err != nil {
			return nil, fmt.Errorf("checkout_start: %w", err)
		}
		checkoutSessionID = intArg(unwrap(checkoutResp, "session"), "id")
	}
	out, err := dbCheckoutCreate(ctx.AppDB(), pid, cart, checkoutSessionID, resIDs)
	if err != nil {
		return nil, err
	}
	_, _ = ctx.AppDB().Exec(`UPDATE commerce_carts SET status='checkout', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, cart.ID)
	return out, nil
}

func (a *App) checkoutUpdate(ctx *sdk.AppCtx, args map[string]any) (*CheckoutSession, error) {
	pid := projectScope(ctx)
	ch, err := dbCheckoutGet(ctx.AppDB(), pid, intArg(args, "checkout_id"))
	if err != nil || ch == nil {
		return nil, firstErr(err, errors.New("checkout not found"))
	}
	patch := mapArg(args, "patch")
	if ch.CheckoutSessionID != nil && ctx.PlatformAPI() != nil {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_update", map[string]any{"_project_id": pid, "session_id": *ch.CheckoutSessionID, "patch": patch}, &out); err != nil {
			return nil, fmt.Errorf("checkout_update: %w", err)
		}
	}
	if err := dbCheckoutPatch(ctx.AppDB(), pid, ch.ID, patch); err != nil {
		return nil, err
	}
	return dbCheckoutGet(ctx.AppDB(), pid, ch.ID)
}

func (a *App) checkoutPay(ctx *sdk.AppCtx, args map[string]any) (*Sale, *CheckoutSession, error) {
	pid := projectScope(ctx)
	ch, err := dbCheckoutGet(ctx.AppDB(), pid, intArg(args, "checkout_id"))
	if err != nil || ch == nil {
		return nil, nil, firstErr(err, errors.New("checkout not found"))
	}
	var invoiceID int64
	var invoiceNumber string
	if ch.CheckoutSessionID != nil && ctx.PlatformAPI() != nil {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_pay", map[string]any{"_project_id": pid, "session_id": *ch.CheckoutSessionID}, &out); err != nil {
			return nil, nil, fmt.Errorf("checkout_pay: %w", err)
		}
		invoiceID = intArg(out, "invoice_id")
		invoiceNumber = strArg(out, "invoice_number")
	}
	if err := dbCheckoutInvoice(ctx.AppDB(), pid, ch.ID, invoiceID, invoiceNumber); err != nil {
		return nil, nil, err
	}
	ch, _ = dbCheckoutGet(ctx.AppDB(), pid, ch.ID)
	sale, err := dbSaleCreateFromCheckout(ctx.AppDB(), pid, ch)
	return sale, ch, err
}

func (a *App) markSalePaid(ctx *sdk.AppCtx, args map[string]any) (*Sale, error) {
	pid := projectScope(ctx)
	sale, err := dbSaleGet(ctx.AppDB(), pid, intArg(args, "sale_id"))
	if err != nil || sale == nil {
		return nil, firstErr(err, errors.New("sale not found"))
	}
	ch, err := dbCheckoutGet(ctx.AppDB(), pid, ptrValue(sale.CheckoutID))
	if err != nil || ch == nil {
		return nil, firstErr(err, errors.New("checkout not found for sale"))
	}
	if ctx.PlatformAPI() != nil {
		for _, id := range ch.ReservationIDs {
			var out map[string]any
			_ = ctx.PlatformAPI().CallAppResult("inventory", "inventory_commit_reservation", map[string]any{"_project_id": pid, "reservation_id": id}, &out)
		}
		if sale.OrderID == nil {
			orderID, _ := a.createOrderForSale(ctx, pid, sale)
			if orderID != 0 {
				_, _ = ctx.AppDB().Exec(`UPDATE commerce_sales SET order_id=?, fulfillment_status='unsubmitted', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, orderID, pid, sale.ID)
			}
		}
	}
	if _, err := ctx.AppDB().Exec(`UPDATE commerce_sales SET status='paid', payment_status='paid', paid_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, sale.ID); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`UPDATE commerce_checkout_sessions SET status='paid', completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, ch.ID); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`UPDATE commerce_carts SET status='converted', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, ptrValue(sale.CartID)); err != nil {
		return nil, err
	}
	return dbSaleGet(ctx.AppDB(), pid, sale.ID)
}

func (a *App) createOrderForSale(ctx *sdk.AppCtx, pid string, sale *Sale) (int64, error) {
	cart, err := dbCartGet(ctx.AppDB(), pid, ptrValue(sale.CartID), true)
	if err != nil || cart == nil {
		return 0, firstErr(err, errors.New("cart not found"))
	}
	lines := []map[string]any{}
	for _, it := range cart.Items {
		lines = append(lines, map[string]any{
			"catalog_price_id":   ptrValue(it.CatalogPriceID),
			"catalog_product_id": 0,
			"sku":                it.SKU,
			"title":              it.TitleSnapshot,
			"quantity":           it.Quantity,
			"unit_amount_cents":  it.UnitAmountCents,
			"currency":           it.Currency,
			"metadata":           map[string]any{"commerce_variant_id": it.VariantID},
		})
	}
	var out map[string]any
	err = ctx.PlatformAPI().CallAppResult("orders", "orders_create", map[string]any{
		"_project_id":         pid,
		"source":              "commerce",
		"source_ref":          fmt.Sprintf("sale:%d", sale.ID),
		"checkout_session_id": ptrValue(sale.CheckoutSessionID),
		"invoice_id":          ptrValue(sale.InvoiceID),
		"customer_email":      sale.CustomerEmail,
		"customer_name":       sale.CustomerName,
		"currency":            sale.Currency,
		"payment_status":      "paid",
		"order_status":        "paid",
		"fulfillment_status":  "unsubmitted",
		"items":               lines,
	}, &out)
	if err != nil {
		return 0, err
	}
	return intArg(unwrap(out, "order"), "id"), nil
}

func dbStoreCreate(db *sql.DB, pid string, args map[string]any) (*Store, error) {
	slug := slugify(firstNonEmpty(strArg(args, "slug"), strArg(args, "name")))
	name := strings.TrimSpace(strArg(args, "name"))
	if slug == "" || name == "" {
		return nil, errors.New("slug and name required")
	}
	res, err := db.Exec(`INSERT INTO commerce_stores
		(project_id, slug, name, public_base_url, default_currency, default_locale, timezone, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, slug, name, strArg(args, "public_base_url"), strings.ToUpper(firstNonEmpty(strArg(args, "default_currency"), "USD")),
		firstNonEmpty(strArg(args, "default_locale"), "en"), firstNonEmpty(strArg(args, "timezone"), "UTC"), jsonText(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbStoreGetByID(db, pid, id)
}

func dbStoresList(db *sql.DB, pid string) ([]*Store, error) {
	rows, err := db.Query(storeSelect()+` WHERE project_id=? AND archived_at IS NULL ORDER BY name`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Store
	for rows.Next() {
		s, err := scanStore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func resolveStore(db *sql.DB, pid string, args map[string]any) (*Store, error) {
	if id := firstNonZero(intArg(args, "store_id"), intArg(args, "id")); id != 0 {
		s, err := dbStoreGetByID(db, pid, id)
		if err != nil || s == nil {
			return nil, firstErr(err, errors.New("store not found"))
		}
		return s, nil
	}
	if slug := firstNonEmpty(strArg(args, "store_slug"), strArg(args, "slug")); slug != "" {
		s, err := dbStoreGetBySlug(db, pid, slug)
		if err != nil || s == nil {
			return nil, firstErr(err, errors.New("store not found"))
		}
		return s, nil
	}
	rows, err := dbStoresList(db, pid)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("no stores exist; create one first")
	}
	return rows[0], nil
}

func dbStoreGetByID(db *sql.DB, pid string, id int64) (*Store, error) {
	row := db.QueryRow(storeSelect()+` WHERE project_id=? AND id=?`, pid, id)
	s, err := scanStore(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func dbStoreGetBySlug(db *sql.DB, pid, slug string) (*Store, error) {
	row := db.QueryRow(storeSelect()+` WHERE project_id=? AND slug=? AND archived_at IS NULL`, pid, slugify(slug))
	s, err := scanStore(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func dbStoreUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Store, error) {
	sets, vals := []string{}, []any{}
	for _, k := range []string{"name", "status", "public_base_url", "default_locale", "timezone", "order_number_format", "checkout_mode"} {
		if v := strArg(patch, k); v != "" {
			sets = append(sets, k+"=?")
			vals = append(vals, v)
		}
	}
	if v := strArg(patch, "default_currency"); v != "" {
		sets = append(sets, "default_currency=?")
		vals = append(vals, strings.ToUpper(v))
	}
	if _, ok := patch["metadata"]; ok {
		sets = append(sets, "metadata_json=?")
		vals = append(vals, jsonText(patch["metadata"], "{}"))
	}
	if len(sets) == 0 {
		return dbStoreGetByID(db, pid, id)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, pid, id)
	if _, err := db.Exec(`UPDATE commerce_stores SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, vals...); err != nil {
		return nil, err
	}
	return dbStoreGetByID(db, pid, id)
}

func dbListingCreate(db *sql.DB, pid string, args map[string]any) (*Listing, error) {
	storeID := intArg(args, "store_id")
	title := strings.TrimSpace(strArg(args, "title"))
	handle := slugify(firstNonEmpty(strArg(args, "handle"), title))
	if storeID == 0 || title == "" || handle == "" {
		return nil, errors.New("store_id and title required")
	}
	res, err := db.Exec(`INSERT INTO commerce_listings
		(project_id, store_id, catalog_product_id, handle, title, description_html, vendor, product_type, status, seo_title, seo_description, featured_media_id, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, storeID, nullableInt(intArg(args, "catalog_product_id")), handle, title, strArg(args, "description_html"),
		strArg(args, "vendor"), strArg(args, "product_type"), firstNonEmpty(strArg(args, "status"), "draft"),
		strArg(args, "seo_title"), strArg(args, "seo_description"), nullableInt(intArg(args, "featured_media_id")), jsonText(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbListingGet(db, pid, id, true)
}

func dbListingsList(db *sql.DB, pid string, args map[string]any) ([]*Listing, error) {
	where := []string{"project_id=?"}
	vals := []any{pid}
	if id := intArg(args, "store_id"); id != 0 {
		where = append(where, "store_id=?")
		vals = append(vals, id)
	}
	if st := strArg(args, "status"); st != "" {
		where = append(where, "status=?")
		vals = append(vals, st)
	} else {
		where = append(where, "status!='archived'")
	}
	if q := strArg(args, "q"); q != "" {
		like := "%" + q + "%"
		where = append(where, "(title LIKE ? OR handle LIKE ? OR vendor LIKE ?)")
		vals = append(vals, like, like, like)
	}
	limit := clamp(intArg(args, "limit"), 1, 500, 100)
	vals = append(vals, limit)
	rows, err := db.Query(listingSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC LIMIT ?`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Listing
	for rows.Next() {
		l, err := scanListing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func resolveListing(db *sql.DB, pid string, args map[string]any) (*Listing, error) {
	if id := intArg(args, "id"); id != 0 {
		l, err := dbListingGet(db, pid, id, true)
		if err != nil || l == nil {
			return nil, firstErr(err, errors.New("product not found"))
		}
		return l, nil
	}
	handle := strArg(args, "handle")
	if handle == "" {
		return nil, errors.New("id or handle required")
	}
	store, err := resolveStore(db, pid, args)
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(listingSelect()+` WHERE project_id=? AND store_id=? AND handle=?`, pid, store.ID, slugify(handle))
	l, err := scanListing(row)
	if err != nil {
		return nil, firstErr(skipNoRows(err), errors.New("product not found"))
	}
	l.Variants, _ = dbVariantsForListing(db, pid, l.ID)
	return l, nil
}

func dbListingGet(db *sql.DB, pid string, id int64, withVariants bool) (*Listing, error) {
	row := db.QueryRow(listingSelect()+` WHERE project_id=? AND id=?`, pid, id)
	l, err := scanListing(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if withVariants {
		l.Variants, _ = dbVariantsForListing(db, pid, l.ID)
	}
	return l, nil
}

func dbListingStatus(db *sql.DB, pid string, id int64, status string) (*Listing, error) {
	if _, err := db.Exec(`UPDATE commerce_listings SET status=?, archived_at=CASE WHEN ?='archived' THEN CURRENT_TIMESTAMP ELSE archived_at END, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, status, status, pid, id); err != nil {
		return nil, err
	}
	return dbListingGet(db, pid, id, true)
}

func dbVariantCreate(db *sql.DB, pid string, args map[string]any) (*Variant, error) {
	listing, err := dbListingGet(db, pid, intArg(args, "listing_id"), false)
	if err != nil || listing == nil {
		return nil, firstErr(err, errors.New("listing not found"))
	}
	currency := firstNonEmpty(strArg(args, "currency"), "USD")
	res, err := db.Exec(`INSERT INTO commerce_variants
		(project_id, store_id, listing_id, catalog_price_id, inventory_item_id, sku, title, option1, option2, option3, price_cents, compare_at_price_cents, currency, taxable, requires_shipping, sort_order, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, listing.StoreID, listing.ID, nullableInt(intArg(args, "catalog_price_id")), nullableInt(intArg(args, "inventory_item_id")),
		strings.ToUpper(strArg(args, "sku")), firstNonEmpty(strArg(args, "title"), "Default"), strArg(args, "option1"), strArg(args, "option2"), strArg(args, "option3"),
		intArg(args, "price_cents"), intArg(args, "compare_at_price_cents"), strings.ToUpper(currency),
		boolToInt(!hasKey(args, "taxable") || boolArg(args, "taxable")), boolToInt(!hasKey(args, "requires_shipping") || boolArg(args, "requires_shipping")),
		intArg(args, "sort_order"), jsonText(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbVariantGet(db, pid, id)
}

func dbVariantGet(db *sql.DB, pid string, id int64) (*Variant, error) {
	row := db.QueryRow(variantSelect()+` WHERE project_id=? AND id=?`, pid, id)
	v, err := scanVariant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

func dbVariantUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Variant, error) {
	sets, vals := []string{}, []any{}
	for _, k := range []string{"sku", "title", "option1", "option2", "option3", "currency"} {
		if v := strArg(patch, k); v != "" {
			if k == "sku" || k == "currency" {
				v = strings.ToUpper(v)
			}
			sets = append(sets, k+"=?")
			vals = append(vals, v)
		}
	}
	for _, k := range []string{"catalog_price_id", "inventory_item_id", "price_cents", "compare_at_price_cents", "sort_order"} {
		if hasKey(patch, k) {
			sets = append(sets, k+"=?")
			vals = append(vals, intArg(patch, k))
		}
	}
	for _, k := range []string{"taxable", "requires_shipping"} {
		if hasKey(patch, k) {
			sets = append(sets, k+"=?")
			vals = append(vals, boolToInt(boolArg(patch, k)))
		}
	}
	if hasKey(patch, "metadata") {
		sets = append(sets, "metadata_json=?")
		vals = append(vals, jsonText(patch["metadata"], "{}"))
	}
	if len(sets) == 0 {
		return dbVariantGet(db, pid, id)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, pid, id)
	if _, err := db.Exec(`UPDATE commerce_variants SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, vals...); err != nil {
		return nil, err
	}
	return dbVariantGet(db, pid, id)
}

func dbVariantsForListing(db *sql.DB, pid string, listingID int64) ([]*Variant, error) {
	rows, err := db.Query(variantSelect()+` WHERE project_id=? AND listing_id=? ORDER BY sort_order, id`, pid, listingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Variant
	for rows.Next() {
		v, err := scanVariant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func dbCollectionCreate(db *sql.DB, pid string, args map[string]any) (*Collection, error) {
	storeID := intArg(args, "store_id")
	title := strArg(args, "title")
	handle := slugify(firstNonEmpty(strArg(args, "handle"), title))
	if storeID == 0 || title == "" {
		return nil, errors.New("store_id and title required")
	}
	res, err := db.Exec(`INSERT INTO commerce_collections (project_id, store_id, handle, title, description_html, status, sort_order, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, pid, storeID, handle, title, strArg(args, "description_html"), firstNonEmpty(strArg(args, "status"), "active"), intArg(args, "sort_order"), jsonText(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbCollectionGet(db, pid, id)
}

func dbCollectionsList(db *sql.DB, pid string, args map[string]any) ([]*Collection, error) {
	where := []string{"project_id=?"}
	vals := []any{pid}
	if id := intArg(args, "store_id"); id != 0 {
		where = append(where, "store_id=?")
		vals = append(vals, id)
	}
	rows, err := db.Query(collectionSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY sort_order, title`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func dbCollectionGet(db *sql.DB, pid string, id int64) (*Collection, error) {
	row := db.QueryRow(collectionSelect()+` WHERE project_id=? AND id=?`, pid, id)
	c, err := scanCollection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func dbCollectionAddListing(db *sql.DB, pid string, collectionID, listingID int64, sortOrder int) error {
	if collectionID == 0 || listingID == 0 {
		return errors.New("collection_id and listing_id required")
	}
	_, err := db.Exec(`INSERT INTO commerce_collection_listings (collection_id, listing_id, sort_order)
		VALUES (?, ?, ?) ON CONFLICT(collection_id, listing_id) DO UPDATE SET sort_order=excluded.sort_order`, collectionID, listingID, sortOrder)
	return err
}

func dbCartCreate(db *sql.DB, pid string, args map[string]any) (*Cart, error) {
	storeID := intArg(args, "store_id")
	token := firstNonEmpty(strArg(args, "session_token"), newToken())
	if storeID == 0 {
		return nil, errors.New("store_id required")
	}
	res, err := db.Exec(`INSERT INTO commerce_carts
		(project_id, store_id, checkout_cart_id, session_token, currency, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, store_id, session_token) DO UPDATE SET updated_at=CURRENT_TIMESTAMP`,
		pid, storeID, nullableInt(intArg(args, "checkout_cart_id")), token, firstNonEmpty(strArg(args, "currency"), "USD"), jsonText(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		return resolveCart(db, pid, args)
	}
	return dbCartGet(db, pid, id, true)
}

func resolveCart(db *sql.DB, pid string, args map[string]any) (*Cart, error) {
	if id := firstNonZero(intArg(args, "cart_id"), intArg(args, "id")); id != 0 {
		c, err := dbCartGet(db, pid, id, true)
		if err != nil || c == nil {
			return nil, firstErr(err, errors.New("cart not found"))
		}
		return c, nil
	}
	token := strArg(args, "session_token")
	storeID := intArg(args, "store_id")
	if token == "" || storeID == 0 {
		return nil, errors.New("cart_id or session_token+store_id required")
	}
	row := db.QueryRow(cartSelect()+` WHERE project_id=? AND store_id=? AND session_token=?`, pid, storeID, token)
	c, err := scanCart(row)
	if err != nil {
		return nil, firstErr(skipNoRows(err), errors.New("cart not found"))
	}
	c.Items, _ = dbCartItems(db, pid, c.ID)
	return c, nil
}

func dbCartGet(db *sql.DB, pid string, id int64, withItems bool) (*Cart, error) {
	row := db.QueryRow(cartSelect()+` WHERE project_id=? AND id=?`, pid, id)
	c, err := scanCart(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if withItems {
		c.Items, _ = dbCartItems(db, pid, c.ID)
	}
	return c, nil
}

func dbCartAddItem(db *sql.DB, pid string, cartID int64, v *Variant, qty float64) error {
	listing, err := dbListingGet(db, pid, v.ListingID, false)
	if err != nil || listing == nil {
		return firstErr(err, errors.New("listing not found"))
	}
	title := listing.Title
	if v.Title != "" && v.Title != "Default" {
		title += " - " + v.Title
	}
	_, err = db.Exec(`INSERT INTO commerce_cart_items
		(project_id, cart_id, variant_id, listing_id, inventory_item_id, catalog_price_id, sku, title_snapshot, unit_amount_cents, currency, quantity)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(cart_id, variant_id) DO UPDATE SET quantity=quantity+excluded.quantity, updated_at=CURRENT_TIMESTAMP`,
		pid, cartID, v.ID, v.ListingID, ptrValue(v.InventoryItemID), ptrValue(v.CatalogPriceID), v.SKU, title, v.PriceCents, v.Currency, qty)
	if err != nil {
		return err
	}
	return recomputeCart(db, pid, cartID)
}

func dbCartSetQuantity(db *sql.DB, pid string, cartID, itemID int64, qty float64) error {
	if qty < 0 {
		return errors.New("quantity must be >= 0")
	}
	if qty == 0 {
		if _, err := db.Exec(`DELETE FROM commerce_cart_items WHERE project_id=? AND cart_id=? AND id=?`, pid, cartID, itemID); err != nil {
			return err
		}
	} else {
		if _, err := db.Exec(`UPDATE commerce_cart_items SET quantity=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND cart_id=? AND id=?`, qty, pid, cartID, itemID); err != nil {
			return err
		}
	}
	return recomputeCart(db, pid, cartID)
}

func recomputeCart(db *sql.DB, pid string, cartID int64) error {
	var subtotal int64
	var currency string
	_ = db.QueryRow(`SELECT COALESCE(SUM(unit_amount_cents * quantity),0), COALESCE(MAX(currency),'USD') FROM commerce_cart_items WHERE project_id=? AND cart_id=?`, pid, cartID).Scan(&subtotal, &currency)
	_, err := db.Exec(`UPDATE commerce_carts SET subtotal_cents=?, total_cents=?-discount_cents+tax_cents+shipping_cents, currency=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, subtotal, subtotal, currency, pid, cartID)
	return err
}

func dbCartItems(db *sql.DB, pid string, cartID int64) ([]*CartItem, error) {
	rows, err := db.Query(`SELECT id, cart_id, checkout_item_id, variant_id, listing_id, inventory_item_id, catalog_price_id, sku, title_snapshot, unit_amount_cents, currency, quantity, created_at, updated_at
		FROM commerce_cart_items WHERE project_id=? AND cart_id=? ORDER BY id`, pid, cartID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CartItem
	for rows.Next() {
		var it CartItem
		var checkoutID, invID, priceID sql.NullInt64
		if err := rows.Scan(&it.ID, &it.CartID, &checkoutID, &it.VariantID, &it.ListingID, &invID, &priceID, &it.SKU, &it.TitleSnapshot, &it.UnitAmountCents, &it.Currency, &it.Quantity, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.CheckoutItemID = ptrIfValid(checkoutID)
		it.InventoryItemID = ptrIfValid(invID)
		it.CatalogPriceID = ptrIfValid(priceID)
		out = append(out, &it)
	}
	return out, rows.Err()
}

func dbCheckoutCreate(db *sql.DB, pid string, cart *Cart, checkoutSessionID int64, reservations []int64) (*CheckoutSession, error) {
	res, err := db.Exec(`INSERT INTO commerce_checkout_sessions
		(project_id, store_id, cart_id, checkout_session_id, status, reservation_ids_json)
		VALUES (?, ?, ?, ?, 'started', ?)`, pid, cart.StoreID, cart.ID, nullableInt(checkoutSessionID), jsonText(reservations, "[]"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbCheckoutGet(db, pid, id)
}

func dbCheckoutGet(db *sql.DB, pid string, id int64) (*CheckoutSession, error) {
	row := db.QueryRow(`SELECT id, store_id, cart_id, checkout_session_id, status, reservation_ids_json, invoice_id, invoice_number, customer_email, customer_name, created_at, updated_at
		FROM commerce_checkout_sessions WHERE project_id=? AND id=?`, pid, id)
	var ch CheckoutSession
	var checkoutID, invoiceID sql.NullInt64
	var reservations string
	if err := row.Scan(&ch.ID, &ch.StoreID, &ch.CartID, &checkoutID, &ch.Status, &reservations, &invoiceID, &ch.InvoiceNumber, &ch.CustomerEmail, &ch.CustomerName, &ch.CreatedAt, &ch.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	ch.CheckoutSessionID = ptrIfValid(checkoutID)
	ch.InvoiceID = ptrIfValid(invoiceID)
	_ = json.Unmarshal([]byte(reservations), &ch.ReservationIDs)
	return &ch, nil
}

func dbCheckoutPatch(db *sql.DB, pid string, id int64, patch map[string]any) error {
	sets, vals := []string{}, []any{}
	for _, k := range []string{"email", "customer_email"} {
		if v := strArg(patch, k); v != "" {
			sets = append(sets, "customer_email=?")
			vals = append(vals, v)
			break
		}
	}
	for _, k := range []string{"customer_name", "name"} {
		if v := strArg(patch, k); v != "" {
			sets = append(sets, "customer_name=?")
			vals = append(vals, v)
			break
		}
	}
	if hasKey(patch, "shipping_address") {
		sets = append(sets, "shipping_address_json=?")
		vals = append(vals, jsonText(patch["shipping_address"], "{}"))
	}
	if hasKey(patch, "billing_address") {
		sets = append(sets, "billing_address_json=?")
		vals = append(vals, jsonText(patch["billing_address"], "{}"))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, pid, id)
	_, err := db.Exec(`UPDATE commerce_checkout_sessions SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, vals...)
	return err
}

func dbCheckoutInvoice(db *sql.DB, pid string, id, invoiceID int64, invoiceNumber string) error {
	_, err := db.Exec(`UPDATE commerce_checkout_sessions SET status='awaiting_payment', invoice_id=?, invoice_number=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, nullableInt(invoiceID), invoiceNumber, pid, id)
	return err
}

func dbSaleCreateFromCheckout(db *sql.DB, pid string, ch *CheckoutSession) (*Sale, error) {
	cart, err := dbCartGet(db, pid, ch.CartID, false)
	if err != nil || cart == nil {
		return nil, firstErr(err, errors.New("cart not found"))
	}
	res, err := db.Exec(`INSERT INTO commerce_sales
		(project_id, store_id, cart_id, checkout_id, checkout_session_id, invoice_id, invoice_number, status, payment_status, subtotal_cents, discount_cents, tax_cents, shipping_cents, total_cents, currency, customer_email, customer_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'awaiting_payment', 'unpaid', ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, ch.StoreID, cart.ID, ch.ID, nullableInt(ptrValue(ch.CheckoutSessionID)), nullableInt(ptrValue(ch.InvoiceID)), ch.InvoiceNumber,
		cart.SubtotalCents, cart.DiscountCents, cart.TaxCents, cart.ShippingCents, cart.TotalCents, cart.Currency, ch.CustomerEmail, ch.CustomerName)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbSaleGet(db, pid, id)
}

func dbSaleGet(db *sql.DB, pid string, id int64) (*Sale, error) {
	row := db.QueryRow(saleSelect()+` WHERE project_id=? AND id=?`, pid, id)
	s, err := scanSale(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func dbSalesList(db *sql.DB, pid string, args map[string]any) ([]*Sale, error) {
	where := []string{"project_id=?"}
	vals := []any{pid}
	for _, k := range []string{"status", "payment_status"} {
		if v := strArg(args, k); v != "" {
			where = append(where, k+"=?")
			vals = append(vals, v)
		}
	}
	if id := intArg(args, "store_id"); id != 0 {
		where = append(where, "store_id=?")
		vals = append(vals, id)
	}
	limit := clamp(intArg(args, "limit"), 1, 500, 100)
	vals = append(vals, limit)
	rows, err := db.Query(saleSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Sale
	for rows.Next() {
		s, err := scanSale(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func storeSelect() string {
	return `SELECT id, slug, name, status, public_base_url, default_currency, default_locale, timezone, order_number_format, checkout_mode, metadata_json, created_at, updated_at FROM commerce_stores`
}

func listingSelect() string {
	return `SELECT id, store_id, catalog_product_id, handle, title, description_html, vendor, product_type, status, seo_title, seo_description, featured_media_id, metadata_json, created_at, updated_at FROM commerce_listings`
}

func variantSelect() string {
	return `SELECT id, store_id, listing_id, catalog_price_id, inventory_item_id, sku, title, option1, option2, option3, price_cents, compare_at_price_cents, currency, taxable, requires_shipping, sort_order, metadata_json, created_at, updated_at FROM commerce_variants`
}

func collectionSelect() string {
	return `SELECT id, store_id, handle, title, description_html, status, sort_order, created_at, updated_at FROM commerce_collections`
}

func cartSelect() string {
	return `SELECT id, store_id, checkout_cart_id, session_token, status, subtotal_cents, discount_cents, tax_cents, shipping_cents, total_cents, currency, created_at, updated_at FROM commerce_carts`
}

func saleSelect() string {
	return `SELECT id, store_id, cart_id, checkout_id, checkout_session_id, invoice_id, invoice_number, order_id, status, payment_status, fulfillment_status, subtotal_cents, discount_cents, tax_cents, shipping_cents, total_cents, currency, customer_email, customer_name, created_at, updated_at, COALESCE(paid_at,'') FROM commerce_sales`
}

type scanner interface{ Scan(dest ...any) error }

func scanStore(s scanner) (*Store, error) {
	var st Store
	var meta string
	if err := s.Scan(&st.ID, &st.Slug, &st.Name, &st.Status, &st.PublicBaseURL, &st.DefaultCurrency, &st.DefaultLocale, &st.Timezone, &st.OrderNumberFormat, &st.CheckoutMode, &meta, &st.CreatedAt, &st.UpdatedAt); err != nil {
		return nil, err
	}
	st.Metadata = jsonMap(meta)
	return &st, nil
}

func scanListing(s scanner) (*Listing, error) {
	var l Listing
	var productID, mediaID sql.NullInt64
	var meta string
	if err := s.Scan(&l.ID, &l.StoreID, &productID, &l.Handle, &l.Title, &l.DescriptionHTML, &l.Vendor, &l.ProductType, &l.Status, &l.SEOTitle, &l.SEODescription, &mediaID, &meta, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	l.CatalogProductID = ptrIfValid(productID)
	l.FeaturedMediaID = ptrIfValid(mediaID)
	l.Metadata = jsonMap(meta)
	return &l, nil
}

func scanVariant(s scanner) (*Variant, error) {
	var v Variant
	var priceID, invID sql.NullInt64
	var taxable, shipping int
	var meta string
	if err := s.Scan(&v.ID, &v.StoreID, &v.ListingID, &priceID, &invID, &v.SKU, &v.Title, &v.Option1, &v.Option2, &v.Option3, &v.PriceCents, &v.CompareAtPriceCents, &v.Currency, &taxable, &shipping, &v.SortOrder, &meta, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	v.CatalogPriceID = ptrIfValid(priceID)
	v.InventoryItemID = ptrIfValid(invID)
	v.Taxable = taxable != 0
	v.RequiresShipping = shipping != 0
	v.Metadata = jsonMap(meta)
	return &v, nil
}

func scanCollection(s scanner) (*Collection, error) {
	var c Collection
	if err := s.Scan(&c.ID, &c.StoreID, &c.Handle, &c.Title, &c.DescriptionHTML, &c.Status, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func scanCart(s scanner) (*Cart, error) {
	var c Cart
	var checkoutID sql.NullInt64
	if err := s.Scan(&c.ID, &c.StoreID, &checkoutID, &c.SessionToken, &c.Status, &c.SubtotalCents, &c.DiscountCents, &c.TaxCents, &c.ShippingCents, &c.TotalCents, &c.Currency, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.CheckoutCartID = ptrIfValid(checkoutID)
	return &c, nil
}

func scanSale(s scanner) (*Sale, error) {
	var sale Sale
	var cartID, checkoutID, checkoutSessionID, invoiceID, orderID sql.NullInt64
	if err := s.Scan(&sale.ID, &sale.StoreID, &cartID, &checkoutID, &checkoutSessionID, &invoiceID, &sale.InvoiceNumber, &orderID, &sale.Status, &sale.PaymentStatus, &sale.FulfillmentStatus, &sale.SubtotalCents, &sale.DiscountCents, &sale.TaxCents, &sale.ShippingCents, &sale.TotalCents, &sale.Currency, &sale.CustomerEmail, &sale.CustomerName, &sale.CreatedAt, &sale.UpdatedAt, &sale.PaidAt); err != nil {
		return nil, err
	}
	sale.CartID = ptrIfValid(cartID)
	sale.CheckoutID = ptrIfValid(checkoutID)
	sale.CheckoutSessionID = ptrIfValid(checkoutSessionID)
	sale.InvoiceID = ptrIfValid(invoiceID)
	sale.OrderID = ptrIfValid(orderID)
	return &sale, nil
}

func (a *App) handleSummary(w http.ResponseWriter, r *http.Request) {
	pid := projectScope(globalCtx)
	var out struct {
		Stores   int `json:"stores"`
		Products int `json:"products"`
		Carts    int `json:"open_carts"`
		Sales    int `json:"sales"`
	}
	_ = globalCtx.AppDB().QueryRow(`SELECT COUNT(*) FROM commerce_stores WHERE project_id=? AND archived_at IS NULL`, pid).Scan(&out.Stores)
	_ = globalCtx.AppDB().QueryRow(`SELECT COUNT(*) FROM commerce_listings WHERE project_id=? AND status!='archived'`, pid).Scan(&out.Products)
	_ = globalCtx.AppDB().QueryRow(`SELECT COUNT(*) FROM commerce_carts WHERE project_id=? AND status='open'`, pid).Scan(&out.Carts)
	_ = globalCtx.AppDB().QueryRow(`SELECT COUNT(*) FROM commerce_sales WHERE project_id=?`, pid).Scan(&out.Sales)
	httpJSON(w, out)
}

func (a *App) handleStores(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := dbStoresList(globalCtx.AppDB(), projectScope(globalCtx))
		httpResult(w, out, err)
	case http.MethodPost:
		var body map[string]any
		readJSON(r, &body)
		out, err := dbStoreCreate(globalCtx.AppDB(), projectScope(globalCtx), body)
		httpResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleProducts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	out, err := dbListingsList(globalCtx.AppDB(), projectScope(globalCtx), queryArgs(r))
	httpResult(w, out, err)
}

func (a *App) handleProduct(w http.ResponseWriter, r *http.Request) {
	id := pathInt(r.URL.Path, "/admin/products/")
	out, err := dbListingGet(globalCtx.AppDB(), projectScope(globalCtx), id, true)
	httpResult(w, out, err)
}

func (a *App) handleCarts(w http.ResponseWriter, r *http.Request) {
	args := queryArgs(r)
	if id := intArg(args, "cart_id"); id != 0 {
		out, err := dbCartGet(globalCtx.AppDB(), projectScope(globalCtx), id, true)
		httpResult(w, out, err)
		return
	}
	httpErr(w, http.StatusBadRequest, "cart_id required")
}

func (a *App) handleSales(w http.ResponseWriter, r *http.Request) {
	out, err := dbSalesList(globalCtx.AppDB(), projectScope(globalCtx), queryArgs(r))
	httpResult(w, out, err)
}

func (a *App) handlePublic(w http.ResponseWriter, r *http.Request) {
	pid := projectScope(globalCtx)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/s/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	store, err := dbStoreGetBySlug(globalCtx.AppDB(), pid, parts[0])
	if err != nil || store == nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) >= 3 && parts[1] == "products" {
		product, err := resolveListing(globalCtx.AppDB(), pid, map[string]any{"store_id": store.ID, "handle": parts[2]})
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writeProductHTML(w, store, product)
		return
	}
	products, _ := dbListingsList(globalCtx.AppDB(), pid, map[string]any{"store_id": store.ID, "status": "active", "limit": int64(100)})
	writeStoreHTML(w, store, products)
}

func writeStoreHTML(w http.ResponseWriter, store *Store, products []*Listing) {
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html><html><head><meta charset=utf-8><title>%s</title><style>body{font-family:system-ui;margin:40px}.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:20px}.card{border:1px solid #ddd;padding:16px;border-radius:8px}a{color:#111}</style></head><body>", template.HTMLEscapeString(store.Name))
	fmt.Fprintf(&b, "<h1>%s</h1><div class=grid>", template.HTMLEscapeString(store.Name))
	for _, p := range products {
		fmt.Fprintf(&b, "<div class=card><h2><a href=\"/s/%s/products/%s\">%s</a></h2><p>%s</p></div>", template.URLQueryEscaper(store.Slug), template.URLQueryEscaper(p.Handle), template.HTMLEscapeString(p.Title), template.HTMLEscapeString(stripTags(p.DescriptionHTML)))
	}
	b.WriteString("</div></body></html>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func writeProductHTML(w http.ResponseWriter, store *Store, p *Listing) {
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html><html><head><meta charset=utf-8><title>%s</title><style>body{font-family:system-ui;margin:40px;max-width:760px}.price{font-size:22px;font-weight:700}</style></head><body>", template.HTMLEscapeString(p.Title))
	fmt.Fprintf(&b, "<p><a href=\"/s/%s\">%s</a></p><h1>%s</h1><div>%s</div>", template.URLQueryEscaper(store.Slug), template.HTMLEscapeString(store.Name), template.HTMLEscapeString(p.Title), template.HTMLEscapeString(stripTags(p.DescriptionHTML)))
	if len(p.Variants) > 0 {
		v := p.Variants[0]
		fmt.Fprintf(&b, "<p class=price>%s %.2f</p><p>SKU: %s</p>", template.HTMLEscapeString(v.Currency), float64(v.PriceCents)/100, template.HTMLEscapeString(v.SKU))
	}
	b.WriteString("</body></html>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func typ(t string) map[string]any { return map[string]any{"type": t} }
func storeProps() map[string]any {
	return map[string]any{"slug": typ("string"), "name": typ("string"), "public_base_url": typ("string"), "default_currency": typ("string"), "default_locale": typ("string"), "timezone": typ("string"), "metadata": typ("object")}
}
func productCreateProps() map[string]any {
	return map[string]any{"store_id": typ("integer"), "store_slug": typ("string"), "title": typ("string"), "handle": typ("string"), "description_html": typ("string"), "vendor": typ("string"), "product_type": typ("string"), "catalog_product_id": typ("integer"), "catalog_price_id": typ("integer"), "price_cents": typ("integer"), "currency": typ("string"), "sku": typ("string"), "inventory_item_id": typ("integer"), "metadata": typ("object")}
}
func listingFilterProps() map[string]any {
	return map[string]any{"store_id": typ("integer"), "status": typ("string"), "q": typ("string"), "limit": typ("integer")}
}
func variantProps() map[string]any {
	return map[string]any{"listing_id": typ("integer"), "catalog_price_id": typ("integer"), "inventory_item_id": typ("integer"), "sku": typ("string"), "title": typ("string"), "option1": typ("string"), "option2": typ("string"), "option3": typ("string"), "price_cents": typ("integer"), "compare_at_price_cents": typ("integer"), "currency": typ("string"), "taxable": typ("boolean"), "requires_shipping": typ("boolean"), "sort_order": typ("integer"), "metadata": typ("object")}
}
func collectionProps() map[string]any {
	return map[string]any{"store_id": typ("integer"), "store_slug": typ("string"), "handle": typ("string"), "title": typ("string"), "description_html": typ("string"), "status": typ("string"), "sort_order": typ("integer"), "metadata": typ("object")}
}
func salesFilterProps() map[string]any {
	return map[string]any{"store_id": typ("integer"), "status": typ("string"), "payment_status": typ("string"), "limit": typ("integer")}
}

func projectScope(ctxs ...*sdk.AppCtx) string {
	if len(ctxs) > 0 && ctxs[0] != nil {
		if pid := strings.TrimSpace(ctxs[0].CurrentProject()); pid != "" {
			return pid
		}
	}
	if pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); pid != "" {
		return pid
	}
	return "default"
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func httpResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, v)
}
func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}
func readJSON(r *http.Request, dst any) {
	if r.Body == nil {
		return
	}
	defer r.Body.Close()
	_ = json.NewDecoder(r.Body).Decode(dst)
}
func queryArgs(r *http.Request) map[string]any {
	out := map[string]any{}
	for k, vals := range r.URL.Query() {
		if len(vals) == 0 {
			continue
		}
		v := vals[0]
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			out[k] = i
		} else if f, err := strconv.ParseFloat(v, 64); err == nil {
			out[k] = f
		} else if v == "true" || v == "false" {
			out[k] = v == "true"
		} else {
			out[k] = v
		}
	}
	return out
}

func intArg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}
func floatArg(args map[string]any, key string, def float64) float64 {
	switch v := args[key].(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	case json.Number:
		n, _ := v.Float64()
		return n
	case string:
		n, _ := strconv.ParseFloat(v, 64)
		if n != 0 {
			return n
		}
	}
	return def
}
func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	case int:
		return v != 0
	case float64:
		return v != 0
	default:
		return false
	}
}
func mapArg(args map[string]any, key string) map[string]any {
	if m, ok := args[key].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}
func copyMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func jsonText(v any, def string) string {
	if v == nil {
		return def
	}
	b, err := json.Marshal(v)
	if err != nil {
		return def
	}
	return string(b)
}
func jsonMap(s string) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal([]byte(firstNonEmpty(s, "{}")), &out)
	return out
}
func nullableInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
func ptrIfValid(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
func ptrValue(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func firstNonZero(vals ...int64) int64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
func firstErr(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
func skipNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
func hasKey(args map[string]any, key string) bool {
	_, ok := args[key]
	return ok
}
func unwrap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if x, ok := m[key].(map[string]any); ok {
		return x
	}
	return nil
}
func clamp(v int64, min, max, def int) int {
	if v == 0 {
		return def
	}
	if v < int64(min) {
		return min
	}
	if v > int64(max) {
		return max
	}
	return int(v)
}
func pathInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func newToken() string {
	return fmt.Sprintf("com_%d", time.Now().UnixNano())
}
func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch r {
		case '<':
			in = true
		case '>':
			in = false
		default:
			if !in {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func main() {
	app := &App{}
	wrapped := wrapApp{app: app}
	sdk.Run(&wrapped)
}

type wrapApp struct{ app *App }

func (w *wrapApp) Manifest() sdk.Manifest            { return w.app.Manifest() }
func (w *wrapApp) OnMount(ctx *sdk.AppCtx) error     { globalCtx = ctx; return w.app.OnMount(ctx) }
func (w *wrapApp) OnUnmount(c *sdk.AppCtx) error     { return w.app.OnUnmount(c) }
func (w *wrapApp) HTTPRoutes() []sdk.Route           { return w.app.HTTPRoutes() }
func (w *wrapApp) MCPTools() []sdk.Tool              { return w.app.MCPTools() }
func (w *wrapApp) Channels() []sdk.ChannelFactory    { return w.app.Channels() }
func (w *wrapApp) Workers() []sdk.Worker             { return w.app.Workers() }
func (w *wrapApp) EventHandlers() []sdk.EventHandler { return w.app.EventHandlers() }
