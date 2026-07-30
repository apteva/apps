package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
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
	ctx.Logger().Info("commerce mounted", "project_id", ctx.CurrentProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{Name: "provider-dispatch-reconcile", Schedule: "@every 30s", Run: a.reconcileProviderDispatches},
		{Name: "provider-source-sync", Schedule: "@every 15m", Run: a.reconcileProviderSources},
	}
}
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		{Topic: "invoice.paid", Handler: a.handleInvoicePaid},
		{Topic: "checkout.paid", Handler: a.handleCheckoutPaid},
		{Topic: "checkout.expired", Handler: a.handleCheckoutExpired},
		{Topic: "inventory.reservation.expired", Handler: a.handleInventoryReservationExpired},
	}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/admin/summary", Handler: a.handleSummary},
		{Pattern: "/admin/stores", Handler: a.handleStores},
		{Pattern: "/admin/stores/", Handler: a.handleStore},
		{Pattern: "/admin/store-settings", Handler: a.handleStoreSettings},
		{Pattern: "/admin/products", Handler: a.handleProducts},
		{Pattern: "/admin/products/", Handler: a.handleProduct},
		{Pattern: "/admin/variants/", Handler: a.handleVariant},
		{Pattern: "/admin/collections", Handler: a.handleCollections},
		{Pattern: "/admin/collections/", Handler: a.handleCollection},
		{Pattern: "/admin/carts", Handler: a.handleCarts},
		{Pattern: "/admin/sales", Handler: a.handleSales},
		{Pattern: "/admin/sales/", Handler: a.handleSale},
		{Pattern: "/admin/providers", Handler: a.handleProviders},
		{Pattern: "/admin/provider-catalog", Handler: a.handleProviderCatalog},
		{Pattern: "/admin/provider-products", Handler: a.handleProviderProduct},
		{Pattern: "/admin/provider-products/import", Handler: a.handleProviderProduct},
		{Pattern: "/admin/variant-sources", Handler: a.handleVariantSources},
		{Pattern: "/admin/shipping/quote", Handler: a.handleShippingQuote},
		{Pattern: "/admin/dispatches", Handler: a.handleDispatches},
		{Pattern: "/admin/dispatches/", Handler: a.handleDispatch},
		{Pattern: "/admin/storefront", Handler: a.handleStorefrontConfiguration},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "commerce_stores_create", Description: "Create a store. Args: slug, name, default_currency?, default_locale?, timezone?.", InputSchema: schemaObject(storeProps(), []string{"slug", "name"}), Handler: a.toolStoresCreate},
		{Name: "commerce_stores_list", Description: "List stores.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolStoresList},
		{Name: "commerce_stores_get", Description: "Fetch one store by id or slug.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "slug": typ("string")}, nil), Handler: a.toolStoresGet},
		{Name: "commerce_stores_update", Description: "Patch a store. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolStoresUpdate},
		{Name: "commerce_store_settings_get", Description: "Return Commerce-owned shipping, transactional tax, market, and payment settings for a store.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "store_slug": typ("string")}, nil), Handler: a.toolStoreSettingsGet},
		{Name: "commerce_store_settings_update", Description: "Patch Commerce-owned shipping, transactional tax, market, or payment settings without replacing unrelated store metadata. Args: store_id, patch.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "patch": typ("object")}, []string{"store_id", "patch"}), Handler: a.toolStoreSettingsUpdate},
		{Name: "commerce_products_create", Description: "Create a storefront listing and optional first variant. Args: store_id, title, handle?, description_html?, catalog_product_id?, price_cents?, sku?, inventory_item_id?.", InputSchema: schemaObject(productCreateProps(), []string{"title"}), Handler: a.toolProductsCreate},
		{Name: "commerce_products_list", Description: "List storefront listings. Args: store_id?, status?, q?, limit?.", InputSchema: schemaObject(listingFilterProps(), nil), Handler: a.toolProductsList},
		{Name: "commerce_products_get", Description: "Fetch one listing with variants. Args: id? or handle? plus store_id?, status?.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "handle": typ("string"), "store_id": typ("integer"), "status": typ("string")}, nil), Handler: a.toolProductsGet},
		{Name: "commerce_products_update", Description: "Patch a storefront listing. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolProductsUpdate},
		{Name: "commerce_products_publish", Description: "Publish a listing. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolProductsPublish},
		{Name: "commerce_products_archive", Description: "Archive a listing. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolProductsArchive},
		{Name: "commerce_variants_create", Description: "Create a listing variant. Args: listing_id, sku?, title?, catalog_price_id?, inventory_item_id?, price_cents?, currency?.", InputSchema: schemaObject(variantProps(), []string{"listing_id"}), Handler: a.toolVariantsCreate},
		{Name: "commerce_variants_update", Description: "Patch a variant. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolVariantsUpdate},
		{Name: "commerce_collections_create", Description: "Create a collection. Args: store_id, handle?, title, description_html?.", InputSchema: schemaObject(collectionProps(), []string{"title"}), Handler: a.toolCollectionsCreate},
		{Name: "commerce_collections_list", Description: "List collections. Args: store_id?, status?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "status": typ("string")}, nil), Handler: a.toolCollectionsList},
		{Name: "commerce_collections_get", Description: "Fetch a collection with products. Args: id? or handle? plus store_id?, status?.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "handle": typ("string"), "store_id": typ("integer"), "status": typ("string")}, nil), Handler: a.toolCollectionsGet},
		{Name: "commerce_collections_update", Description: "Patch a collection. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolCollectionsUpdate},
		{Name: "commerce_collections_add_product", Description: "Add a listing to a collection. Args: collection_id, listing_id.", InputSchema: schemaObject(map[string]any{"collection_id": typ("integer"), "listing_id": typ("integer"), "sort_order": typ("integer")}, []string{"collection_id", "listing_id"}), Handler: a.toolCollectionsAddProduct},
		{Name: "commerce_collections_remove_product", Description: "Remove a listing from a collection. Args: collection_id, listing_id.", InputSchema: schemaObject(map[string]any{"collection_id": typ("integer"), "listing_id": typ("integer")}, []string{"collection_id", "listing_id"}), Handler: a.toolCollectionsRemoveProduct},
		{Name: "commerce_cart_create", Description: "Create a Commerce cart backed by Checkout. Args: store_id? or store_slug?, session_token?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "store_slug": typ("string"), "session_token": typ("string")}, nil), Handler: a.toolCartCreate},
		{Name: "commerce_carts_list", Description: "List Commerce carts, including inactive abandonment candidates. Args: store_id?, status?, updated_before? (RFC3339), updated_after? (RFC3339), inactive_for_minutes?, has_items?, abandoned_only? (nonempty open/checkout carts; defaults to 24h inactivity), sort? (updated_desc|updated_asc), limit?.", InputSchema: schemaObject(cartFilterProps(), nil), Handler: a.toolCartsList},
		{Name: "commerce_cart_get", Description: "Fetch a Commerce cart with items. Args: cart_id? or session_token? plus store_id?.", InputSchema: schemaObject(map[string]any{"cart_id": typ("integer"), "session_token": typ("string"), "store_id": typ("integer")}, nil), Handler: a.toolCartGet},
		{Name: "commerce_cart_add_item", Description: "Add a Commerce variant to a cart and delegate item snapshotting to Checkout. Args: cart_id, variant_id, quantity?.", InputSchema: schemaObject(map[string]any{"cart_id": typ("integer"), "variant_id": typ("integer"), "quantity": typ("number")}, []string{"cart_id", "variant_id"}), Handler: a.toolCartAddItem},
		{Name: "commerce_cart_set_quantity", Description: "Set a Commerce cart item's quantity. Args: cart_id, item_id, quantity.", InputSchema: schemaObject(map[string]any{"cart_id": typ("integer"), "item_id": typ("integer"), "quantity": typ("number")}, []string{"cart_id", "item_id", "quantity"}), Handler: a.toolCartSetQuantity},
		{Name: "commerce_checkout_start", Description: "Reserve inventory where configured, then start the backing Checkout session. Args: cart_id.", InputSchema: schemaObject(map[string]any{"cart_id": typ("integer")}, []string{"cart_id"}), Handler: a.toolCheckoutStart},
		{Name: "commerce_checkout_bootstrap", Description: "Return browser-safe durable cart, quote, checkout, sale, and optional resumed payment state. Args: store_id, session_token, include_payment?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "store_slug": typ("string"), "session_token": typ("string"), "include_payment": typ("boolean")}, []string{"session_token"}), Handler: a.toolCheckoutBootstrap},
		{Name: "commerce_checkout_quote", Description: "Calculate and persist a unified shipping, Catalog discount, and transactional tax quote. Args: cart_id, shipping_address?, shipping_option_id?, discount_code?, remove_discount?.", InputSchema: schemaObject(map[string]any{"cart_id": typ("integer"), "shipping_address": typ("object"), "shipping_option_id": typ("string"), "discount_code": typ("string"), "remove_discount": typ("boolean"), "customer_ref": typ("string")}, []string{"cart_id"}), Handler: a.toolCheckoutQuote},
		{Name: "commerce_checkout_update", Description: "Update buyer contact/address on Commerce and Checkout sessions. Args: checkout_id, patch.", InputSchema: schemaObject(map[string]any{"checkout_id": typ("integer"), "patch": typ("object")}, []string{"checkout_id", "patch"}), Handler: a.toolCheckoutUpdate},
		{Name: "commerce_checkout_cancel", Description: "Cancel a checkout and release active inventory reservations. Args: checkout_id.", InputSchema: schemaObject(map[string]any{"checkout_id": typ("integer")}, []string{"checkout_id"}), Handler: a.toolCheckoutCancel},
		{Name: "commerce_checkout_pay", Description: "Submit the backing Checkout session using the store payment configuration and create a Commerce sale record. Args: checkout_id.", InputSchema: schemaObject(map[string]any{"checkout_id": typ("integer")}, []string{"checkout_id"}), Handler: a.toolCheckoutPay},
		{Name: "commerce_checkout_status", Description: "Return the browser-safe checkout status for a storefront session. Args: store_id, session_token.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "session_token": typ("string")}, []string{"store_id", "session_token"}), Handler: a.toolCheckoutStatus},
		{Name: "commerce_checkout_mark_paid", Description: "Mark a sale paid after Billing confirms payment; commits inventory reservations and creates an Orders record when available. Args: sale_id.", InputSchema: schemaObject(map[string]any{"sale_id": typ("integer")}, []string{"sale_id"}), Handler: a.toolCheckoutMarkPaid},
		{Name: "commerce_sales_list", Description: "List sales. Args: store_id?, status?, payment_status?, limit?.", InputSchema: schemaObject(salesFilterProps(), nil), Handler: a.toolSalesList},
		{Name: "commerce_sales_get", Description: "Fetch one sale. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolSalesGet},
		{Name: "commerce_providers_list", Description: "List supplier provider connections and store policies. Args: store_id?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer")}, nil), Handler: a.toolProvidersList},
		{Name: "commerce_provider_policy_set", Description: "Set a store's provider policy. Args: store_id, connection_id, enabled?, fulfillment_mode?, margin_bps?, settings?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "connection_id": typ("integer"), "enabled": typ("boolean"), "fulfillment_mode": typ("string"), "margin_bps": typ("integer"), "settings": typ("object")}, []string{"store_id", "connection_id"}), Handler: a.toolProviderPolicySet},
		{Name: "commerce_provider_catalog", Description: "Browse a bound provider using provider-specific input. Args: store_id, connection_id, provider_input?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "connection_id": typ("integer"), "provider_input": typ("object")}, []string{"store_id", "connection_id"}), Handler: a.toolProviderCatalog},
		{Name: "commerce_provider_product_get", Description: "Fetch and adapt one provider product. Args: store_id, connection_id, external_product_id, provider_input?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "connection_id": typ("integer"), "external_product_id": typ("string"), "provider_input": typ("object")}, []string{"store_id", "connection_id", "external_product_id"}), Handler: a.toolProviderProductGet},
		{Name: "commerce_provider_product_import", Description: "Import selected provider variants into Catalog and a draft Commerce listing. Args: store_id, connection_id, external_product_id or product, variant_ids?, price_cents_by_variant?, provider_input?. Prodigi requires provider_input.assets with printArea and a public url.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "connection_id": typ("integer"), "external_product_id": typ("string"), "product": typ("object"), "variant_ids": typ("array"), "price_cents_by_variant": typ("object"), "provider_input": typ("object"), "title": typ("string"), "handle": typ("string"), "vendor": typ("string"), "product_type": typ("string")}, []string{"store_id", "connection_id"}), Handler: a.toolProviderProductImport},
		{Name: "commerce_variant_sources_list", Description: "List imported provider variant mappings. Args: store_id?, listing_id?, provider?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "listing_id": typ("integer"), "provider": typ("string")}, nil), Handler: a.toolVariantSourcesList},
		{Name: "commerce_variant_source_update", Description: "Patch an imported variant's supplier SKU, cost, currency, availability, or quantity. Args: id, patch.", InputSchema: schemaObject(map[string]any{"id": typ("integer"), "patch": typ("object")}, []string{"id", "patch"}), Handler: a.toolVariantSourceUpdate},
		{Name: "commerce_shipping_quote", Description: "Quote provider shipping for sourced cart lines and freeze the selected total on the Commerce cart. Args: cart_id, shipping_address, apply?.", InputSchema: schemaObject(map[string]any{"cart_id": typ("integer"), "shipping_address": typ("object"), "apply": typ("boolean")}, []string{"cart_id", "shipping_address"}), Handler: a.toolShippingQuote},
		{Name: "commerce_dispatches_list", Description: "List provider dispatch jobs. Args: store_id?, sale_id?, status?, limit?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "sale_id": typ("integer"), "status": typ("string"), "limit": typ("integer")}, nil), Handler: a.toolDispatchesList},
		{Name: "commerce_dispatch_submit", Description: "Approve or retry one provider dispatch job. Args: id.", InputSchema: schemaObject(map[string]any{"id": typ("integer")}, []string{"id"}), Handler: a.toolDispatchSubmit},
		{Name: "commerce_sources_sync", Description: "Synchronize imported provider variant cost and availability. Args: store_id?, listing_id?, provider?, limit?.", InputSchema: schemaObject(map[string]any{"store_id": typ("integer"), "listing_id": typ("integer"), "provider": typ("string"), "limit": typ("integer")}, nil), Handler: a.toolSourcesSync},
	}
}

type Store struct {
	ID                  int64          `json:"id"`
	Slug                string         `json:"slug"`
	Name                string         `json:"name"`
	Status              string         `json:"status"`
	PublicBaseURL       string         `json:"public_base_url"`
	DefaultCurrency     string         `json:"default_currency"`
	DefaultLocale       string         `json:"default_locale"`
	Timezone            string         `json:"timezone"`
	OrderNumberFormat   string         `json:"order_number_format"`
	CheckoutMode        string         `json:"checkout_mode"`
	PaymentProvider     string         `json:"payment_provider"`
	PaymentPresentation string         `json:"payment_presentation"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	CreatedAt           string         `json:"created_at"`
	UpdatedAt           string         `json:"updated_at"`
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
	ID              int64      `json:"id"`
	StoreID         int64      `json:"store_id"`
	Handle          string     `json:"handle"`
	Title           string     `json:"title"`
	DescriptionHTML string     `json:"description_html"`
	Status          string     `json:"status"`
	SortOrder       int        `json:"sort_order"`
	Products        []*Listing `json:"products,omitempty"`
	CreatedAt       string     `json:"created_at"`
	UpdatedAt       string     `json:"updated_at"`
}

type Cart struct {
	ID             int64          `json:"id"`
	StoreID        int64          `json:"store_id"`
	CheckoutCartID *int64         `json:"checkout_cart_id,omitempty"`
	SessionToken   string         `json:"session_token"`
	Status         string         `json:"status"`
	SubtotalCents  int64          `json:"subtotal_cents"`
	DiscountCents  int64          `json:"discount_cents"`
	TaxCents       int64          `json:"tax_cents"`
	ShippingCents  int64          `json:"shipping_cents"`
	TotalCents     int64          `json:"total_cents"`
	Currency       string         `json:"currency"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Items          []*CartItem    `json:"items,omitempty"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
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
	ImageURL        string  `json:"image_url,omitempty"`
	UnitAmountCents int64   `json:"unit_amount_cents"`
	Currency        string  `json:"currency"`
	Quantity        float64 `json:"quantity"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

type CheckoutSession struct {
	ID                    int64          `json:"id"`
	StoreID               int64          `json:"store_id"`
	CartID                int64          `json:"cart_id"`
	CheckoutSessionID     *int64         `json:"checkout_session_id,omitempty"`
	Status                string         `json:"status"`
	ReservationIDs        []int64        `json:"reservation_ids"`
	InvoiceID             *int64         `json:"invoice_id,omitempty"`
	InvoiceNumber         string         `json:"invoice_number"`
	CustomerEmail         string         `json:"customer_email"`
	CustomerName          string         `json:"customer_name"`
	ShippingAddress       map[string]any `json:"shipping_address,omitempty"`
	BillingAddress        map[string]any `json:"billing_address,omitempty"`
	DiscountReservationID string         `json:"discount_reservation_id,omitempty"`
	Quote                 map[string]any `json:"quote,omitempty"`
	CreatedAt             string         `json:"created_at"`
	UpdatedAt             string         `json:"updated_at"`
}

type Sale struct {
	ID                int64          `json:"id"`
	StoreID           int64          `json:"store_id"`
	CartID            *int64         `json:"cart_id,omitempty"`
	CheckoutID        *int64         `json:"checkout_id,omitempty"`
	CheckoutSessionID *int64         `json:"checkout_session_id,omitempty"`
	InvoiceID         *int64         `json:"invoice_id,omitempty"`
	InvoiceNumber     string         `json:"invoice_number"`
	OrderID           *int64         `json:"order_id,omitempty"`
	Status            string         `json:"status"`
	PaymentStatus     string         `json:"payment_status"`
	FulfillmentStatus string         `json:"fulfillment_status"`
	SubtotalCents     int64          `json:"subtotal_cents"`
	DiscountCents     int64          `json:"discount_cents"`
	TaxCents          int64          `json:"tax_cents"`
	ShippingCents     int64          `json:"shipping_cents"`
	TotalCents        int64          `json:"total_cents"`
	Currency          string         `json:"currency"`
	CustomerEmail     string         `json:"customer_email"`
	CustomerName      string         `json:"customer_name"`
	ShippingAddress   map[string]any `json:"shipping_address,omitempty"`
	BillingAddress    map[string]any `json:"billing_address,omitempty"`
	ProcessingError   string         `json:"processing_error,omitempty"`
	Items             []*SaleItem    `json:"items,omitempty"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
	PaidAt            string         `json:"paid_at,omitempty"`
}

type SaleItem struct {
	ID               int64          `json:"id"`
	SaleID           int64          `json:"sale_id"`
	VariantID        *int64         `json:"variant_id,omitempty"`
	ListingID        *int64         `json:"listing_id,omitempty"`
	InventoryItemID  *int64         `json:"inventory_item_id,omitempty"`
	CatalogProductID *int64         `json:"catalog_product_id,omitempty"`
	CatalogPriceID   *int64         `json:"catalog_price_id,omitempty"`
	SKU              string         `json:"sku"`
	TitleSnapshot    string         `json:"title_snapshot"`
	UnitAmountCents  int64          `json:"unit_amount_cents"`
	Currency         string         `json:"currency"`
	Quantity         float64        `json:"quantity"`
	RequiresShipping bool           `json:"requires_shipping"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type catalogPrice struct {
	ID              int64  `json:"id"`
	ProductID       int64  `json:"product_id"`
	UnitAmountCents int64  `json:"unit_amount_cents"`
	Currency        string `json:"currency"`
	Active          bool   `json:"active"`
	ArchivedAt      string `json:"archived_at"`
}

func (a *App) toolStoresCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := dbStoreCreate(ctx.AppDB(), pid, args)
	if err == nil {
		ctx.Emit("commerce.store.created", map[string]any{"store_id": store.ID, "slug": store.Slug})
	}
	return map[string]any{"store": store}, err
}

func (a *App) toolStoresList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	stores, err := dbStoresList(ctx.AppDB(), pid)
	return map[string]any{"stores": stores, "count": len(stores)}, err
}

func (a *App) toolStoresGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"store": store}, nil
}

func (a *App) toolStoresUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := dbStoreUpdate(ctx.AppDB(), pid, intArg(args, "id"), mapArg(args, "patch"))
	return map[string]any{"store": store}, err
}

func (a *App) toolProductsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	cp := copyMap(args)
	cp["store_id"] = store.ID
	requestedStatus := firstNonEmpty(strArg(cp, "status"), "draft")
	if requestedStatus != "draft" && requestedStatus != "active" {
		return nil, errors.New("status must be draft or active")
	}
	cp["status"] = "draft"
	createdCatalogProduct := false
	if intArg(cp, "catalog_product_id") == 0 {
		productID, priceID, err := a.ensureCatalogProductAndPrice(ctx, pid, store, cp)
		if err != nil {
			return nil, err
		}
		cp["catalog_product_id"] = productID
		createdCatalogProduct = true
		if priceID != 0 {
			cp["catalog_price_id"] = priceID
		}
	}
	listing, err := dbListingCreate(ctx.AppDB(), pid, cp)
	if err != nil {
		if createdCatalogProduct {
			a.archiveCatalogProduct(ctx, pid, intArg(cp, "catalog_product_id"))
		}
		return nil, err
	}
	if intArg(cp, "catalog_price_id") != 0 || intArg(cp, "price_cents") != 0 || strArg(cp, "sku") != "" {
		cp["listing_id"] = listing.ID
		canonical, err := a.canonicalVariantArgs(ctx, pid, listing, cp)
		if err == nil {
			_, err = dbVariantCreate(ctx.AppDB(), pid, canonical)
		}
		if err != nil {
			_, _ = ctx.AppDB().Exec(`DELETE FROM commerce_listings WHERE project_id=? AND id=?`, pid, listing.ID)
			if createdCatalogProduct {
				a.archiveCatalogProduct(ctx, pid, intArg(cp, "catalog_product_id"))
			}
			return nil, err
		}
	}
	if requestedStatus == "active" {
		listing, err = dbListingStatus(ctx.AppDB(), pid, listing.ID, "active")
		if err != nil {
			return nil, err
		}
	} else {
		listing, err = dbListingGet(ctx.AppDB(), pid, listing.ID, true)
		if err != nil {
			return nil, err
		}
	}
	ctx.Emit("commerce.product.created", map[string]any{"listing_id": listing.ID, "store_id": listing.StoreID})
	return map[string]any{"product": listing}, nil
}

func (a *App) archiveCatalogProduct(ctx *sdk.AppCtx, pid string, productID int64) {
	if productID == 0 || ctx.PlatformAPI() == nil {
		return
	}
	var ignored map[string]any
	_ = ctx.PlatformAPI().CallAppResult("catalog", "catalog_products_archive", map[string]any{"_project_id": pid, "id": productID}, &ignored)
}

func (a *App) toolProductsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := dbListingsList(ctx.AppDB(), pid, args)
	if err == nil {
		for _, product := range out {
			product.Variants, err = dbVariantsForListing(ctx.AppDB(), pid, product.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{"products": out, "count": len(out)}, err
}

func (a *App) toolProductsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := resolveListing(ctx.AppDB(), pid, args)
	if err == nil && out != nil {
		if status := strArg(args, "status"); status != "" && out.Status != status {
			return nil, errors.New("product not found")
		}
	}
	return map[string]any{"product": out}, err
}

func (a *App) toolProductsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := dbListingUpdate(ctx.AppDB(), pid, intArg(args, "id"), mapArg(args, "patch"))
	return map[string]any{"product": out}, err
}

func (a *App) toolProductsPublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := dbListingStatus(ctx.AppDB(), pid, intArg(args, "id"), "active")
	if err == nil {
		ctx.Emit("commerce.product.published", map[string]any{"listing_id": out.ID, "store_id": out.StoreID})
	}
	return map[string]any{"product": out}, err
}

func (a *App) toolProductsArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := dbListingStatus(ctx.AppDB(), pid, intArg(args, "id"), "archived")
	return map[string]any{"product": out}, err
}

func (a *App) toolVariantsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	listing, err := dbListingGet(ctx.AppDB(), pid, intArg(args, "listing_id"), false)
	if err != nil || listing == nil {
		return nil, firstErr(err, errors.New("listing not found"))
	}
	canonical, err := a.canonicalVariantArgs(ctx, pid, listing, args)
	if err != nil {
		return nil, err
	}
	out, err := dbVariantCreate(ctx.AppDB(), pid, canonical)
	return map[string]any{"variant": out}, err
}

func (a *App) toolVariantsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	variant, err := dbVariantGet(ctx.AppDB(), pid, intArg(args, "id"))
	if err != nil || variant == nil {
		return nil, firstErr(err, errors.New("variant not found"))
	}
	listing, err := dbListingGet(ctx.AppDB(), pid, variant.ListingID, false)
	if err != nil || listing == nil {
		return nil, firstErr(err, errors.New("listing not found"))
	}
	patch := mapArg(args, "patch")
	if hasKey(patch, "price_cents") || hasKey(patch, "currency") || hasKey(patch, "catalog_price_id") {
		if !hasKey(patch, "catalog_price_id") && variant.CatalogPriceID != nil && !hasKey(patch, "price_cents") {
			patch["catalog_price_id"] = *variant.CatalogPriceID
		}
		patch, err = a.canonicalVariantArgs(ctx, pid, listing, patch)
		if err != nil {
			return nil, err
		}
	}
	out, err := dbVariantUpdate(ctx.AppDB(), pid, variant.ID, patch)
	return map[string]any{"variant": out}, err
}

func (a *App) toolCollectionsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
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
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := dbCollectionsList(ctx.AppDB(), pid, args)
	return map[string]any{"collections": out, "count": len(out)}, err
}

func (a *App) toolCollectionsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	collection, err := resolveCollection(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	status := strArg(args, "status")
	if status != "" && collection.Status != status {
		return nil, errors.New("collection not found")
	}
	out, err := dbCollectionGetWithProducts(ctx.AppDB(), pid, collection.ID, status)
	return map[string]any{"collection": out}, err
}

func (a *App) toolCollectionsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := dbCollectionUpdate(ctx.AppDB(), pid, intArg(args, "id"), mapArg(args, "patch"))
	return map[string]any{"collection": out}, err
}

func (a *App) toolCollectionsAddProduct(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	err = dbCollectionAddListing(ctx.AppDB(), pid, intArg(args, "collection_id"), intArg(args, "listing_id"), int(intArg(args, "sort_order")))
	return map[string]any{"ok": err == nil}, err
}

func (a *App) toolCollectionsRemoveProduct(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	err = dbCollectionRemoveListing(ctx.AppDB(), pid, intArg(args, "collection_id"), intArg(args, "listing_id"))
	return map[string]any{"ok": err == nil}, err
}

func (a *App) toolCartCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	cart, err := a.createCart(ctx, args)
	return map[string]any{"cart": cart}, err
}

func (a *App) toolCartsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	carts, err := dbCartsList(ctx.AppDB(), pid, args)
	return map[string]any{"carts": carts, "count": len(carts)}, err
}

func (a *App) toolCartGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	cart, err := resolveCart(ctx.AppDB(), pid, args)
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

func (a *App) toolCheckoutCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := a.checkoutCancel(ctx, args)
	return map[string]any{"checkout": out}, err
}

func (a *App) toolCheckoutPay(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	sale, checkout, payment, err := a.checkoutPay(ctx, args)
	if err == nil {
		ctx.Emit("commerce.sale.created", map[string]any{"sale_id": sale.ID, "checkout_id": checkout.ID, "store_id": sale.StoreID})
	}
	return map[string]any{"sale": sale, "checkout": checkout, "payment": payment}, err
}

func (a *App) toolCheckoutStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(strArg(args, "session_token"))
	if token == "" {
		return nil, errors.New("session_token required")
	}
	cart, err := dbCartGetBySession(ctx.AppDB(), pid, intArg(args, "store_id"), token)
	if err != nil || cart == nil {
		return map[string]any{"status": "not_found"}, err
	}
	checkout, err := dbCheckoutGetByCart(ctx.AppDB(), pid, cart.ID)
	if err != nil || checkout == nil {
		return map[string]any{"status": cart.Status, "payment_status": "unpaid"}, err
	}
	sale, err := dbSaleGetByCheckout(ctx.AppDB(), pid, checkout.ID)
	if err != nil {
		return nil, err
	}
	if sale == nil {
		return map[string]any{
			"status": checkout.Status, "payment_status": "unpaid",
		}, nil
	}
	return map[string]any{
		"status":             sale.Status,
		"payment_status":     sale.PaymentStatus,
		"fulfillment_status": sale.FulfillmentStatus,
		"invoice_number":     sale.InvoiceNumber,
	}, nil
}

func (a *App) toolCheckoutMarkPaid(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	sale, err := a.markSalePaid(ctx, args)
	return map[string]any{"sale": sale}, err
}

func (a *App) toolSalesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := dbSalesList(ctx.AppDB(), pid, args)
	return map[string]any{"sales": out, "count": len(out)}, err
}

func (a *App) toolSalesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := dbSaleGet(ctx.AppDB(), pid, intArg(args, "id"))
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
	if priceCents < 0 {
		a.archiveCatalogProduct(ctx, pid, productID)
		return 0, 0, errors.New("price_cents must be positive")
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
		a.archiveCatalogProduct(ctx, pid, productID)
		return 0, 0, fmt.Errorf("create catalog price: %w", err)
	}
	priceID := intArg(unwrap(priceResp, "price"), "id")
	if priceID == 0 {
		priceID = intArg(priceResp, "id")
	}
	if priceID == 0 {
		a.archiveCatalogProduct(ctx, pid, productID)
		return 0, 0, errors.New("catalog price response missing id")
	}
	return productID, priceID, nil
}

func (a *App) createCart(ctx *sdk.AppCtx, args map[string]any) (*Cart, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if store.Status != "active" {
		return nil, errors.New("store is not active")
	}
	token := firstNonEmpty(strArg(args, "session_token"), newToken())
	if existing, err := dbCartGetBySession(ctx.AppDB(), pid, store.ID, token); err != nil {
		return nil, err
	} else if existing != nil {
		if existing.Status == "checkout" {
			checkout, err := dbCheckoutGetByCart(ctx.AppDB(), pid, existing.ID)
			if err != nil {
				return nil, err
			}
			if checkout == nil {
				return nil, errors.New("checkout cart is missing its checkout session")
			}
			if _, err := a.checkoutCancel(ctx, map[string]any{
				"_project_id": pid,
				"checkout_id": checkout.ID,
			}); err != nil {
				return nil, fmt.Errorf("reopen storefront cart: %w", err)
			}
			reopened, err := dbCartGet(ctx.AppDB(), pid, existing.ID, true)
			if err != nil {
				return nil, err
			}
			return a.rebuildCheckoutCart(ctx, pid, reopened)
		}
		if existing.Status != "open" && existing.Status != "checkout" && existing.Status != "awaiting_payment" {
			return nil, fmt.Errorf("session cart is %s; start a new storefront session", existing.Status)
		}
		return existing, nil
	}
	var checkoutResp map[string]any
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	if err := ctx.PlatformAPI().CallAppResult("checkout", "cart_create", map[string]any{
		"_project_id":   pid,
		"session_token": token,
		"metadata":      map[string]any{"source": "commerce", "store_id": store.ID},
	}, &checkoutResp); err != nil {
		return nil, fmt.Errorf("create checkout cart: %w", err)
	}
	checkoutCartID := intArg(unwrap(checkoutResp, "cart"), "id")
	if checkoutCartID == 0 {
		return nil, errors.New("checkout cart response missing id")
	}
	cart, err := dbCartCreate(ctx.AppDB(), pid, map[string]any{"store_id": store.ID, "session_token": token, "checkout_cart_id": checkoutCartID, "currency": store.DefaultCurrency})
	if err != nil {
		return nil, err
	}
	return dbCartGet(ctx.AppDB(), pid, cart.ID, true)
}

func (a *App) rebuildCheckoutCart(ctx *sdk.AppCtx, pid string, cart *Cart) (*Cart, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	var created map[string]any
	if err := ctx.PlatformAPI().CallAppResult("checkout", "cart_create", map[string]any{
		"_project_id":   pid,
		"session_token": newToken(),
		"metadata": map[string]any{
			"source":           "commerce",
			"store_id":         cart.StoreID,
			"commerce_cart_id": cart.ID,
			"recovered":        true,
		},
	}, &created); err != nil {
		return nil, fmt.Errorf("create replacement Checkout cart: %w", err)
	}
	checkoutCartID := intArg(unwrap(created, "cart"), "id")
	if checkoutCartID == 0 {
		return nil, errors.New("replacement Checkout cart response missing id")
	}
	checkoutItemIDs := make(map[int64]int64, len(cart.Items))
	for _, item := range cart.Items {
		if item.CatalogPriceID == nil || *item.CatalogPriceID == 0 {
			return nil, fmt.Errorf("cart item %d is missing its Catalog price", item.ID)
		}
		var replayed map[string]any
		if err := ctx.PlatformAPI().CallAppResult("checkout", "cart_add_item", map[string]any{
			"_project_id": pid,
			"cart_id":     checkoutCartID,
			"price_id":    *item.CatalogPriceID,
			"quantity":    item.Quantity,
		}, &replayed); err != nil {
			return nil, fmt.Errorf("replay cart item %d: %w", item.ID, err)
		}
		checkoutItemID, checkoutQty := checkoutCartItem(replayed, *item.CatalogPriceID)
		if checkoutItemID == 0 || math.Abs(checkoutQty-item.Quantity) > 1e-9 {
			return nil, fmt.Errorf("replacement Checkout cart returned an inconsistent item %d", item.ID)
		}
		checkoutItemIDs[item.ID] = checkoutItemID
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE commerce_carts SET checkout_cart_id=?, status='open', updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, checkoutCartID, pid, cart.ID); err != nil {
		return nil, err
	}
	for itemID, checkoutItemID := range checkoutItemIDs {
		if _, err := tx.Exec(`UPDATE commerce_cart_items SET checkout_item_id=?, updated_at=CURRENT_TIMESTAMP
			WHERE project_id=? AND cart_id=? AND id=?`, checkoutItemID, pid, cart.ID, itemID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbCartGet(ctx.AppDB(), pid, cart.ID, true)
}

func checkoutCartRequiresRebuild(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "only 'open' carts") ||
		strings.Contains(message, "only open carts") ||
		strings.Contains(message, "cart is converted") ||
		strings.Contains(message, "cart is cancelled") ||
		strings.Contains(message, "cart is expired")
}

func (a *App) addCartItem(ctx *sdk.AppCtx, args map[string]any) (*Cart, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	cart, err := resolveCart(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if cart.Status != "open" {
		return nil, fmt.Errorf("cart is %s; only open carts accept item changes", cart.Status)
	}
	variant, err := dbVariantGet(ctx.AppDB(), pid, intArg(args, "variant_id"))
	if err != nil || variant == nil {
		return nil, firstErr(err, errors.New("variant not found"))
	}
	if variant.CatalogPriceID == nil || *variant.CatalogPriceID == 0 {
		return nil, errors.New("variant must have catalog_price_id before it can be added to cart")
	}
	if variant.StoreID != cart.StoreID {
		return nil, errors.New("variant and cart must belong to the same store")
	}
	listing, err := dbListingGet(ctx.AppDB(), pid, variant.ListingID, false)
	if err != nil || listing == nil || listing.Status != "active" {
		return nil, errors.New("product is not active")
	}
	qty := floatArg(args, "quantity", 1)
	if qty <= 0 {
		return nil, errors.New("quantity must be positive")
	}
	price, err := a.catalogPriceGet(ctx, pid, *variant.CatalogPriceID)
	if err != nil {
		return nil, err
	}
	if listing.CatalogProductID == nil || *listing.CatalogProductID != price.ProductID {
		return nil, errors.New("variant price does not belong to the product's Catalog record")
	}
	if len(cart.Items) > 0 && cart.Currency != "" && cart.Currency != price.Currency {
		return nil, errors.New("all cart items must use the same currency")
	}
	if variant.InventoryItemID != nil && *variant.InventoryItemID > 0 && ctx.PlatformAPI() != nil {
		var avail map[string]any
		if err := ctx.PlatformAPI().CallAppResult("inventory", "inventory_availability_check", map[string]any{"_project_id": pid, "item_id": *variant.InventoryItemID, "quantity": qty}, &avail); err != nil {
			return nil, fmt.Errorf("check inventory: %w", err)
		}
		if ok, _ := avail["can_reserve"].(bool); !ok {
			return nil, errors.New("insufficient inventory available")
		}
	}
	if cart.CheckoutCartID == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("cart is not linked to Checkout")
	}
	previousQty := 0.0
	for _, item := range cart.Items {
		if item.VariantID == variant.ID {
			previousQty = item.Quantity
			break
		}
	}
	var checkoutCart map[string]any
	addArgs := map[string]any{"_project_id": pid, "cart_id": *cart.CheckoutCartID, "price_id": price.ID, "quantity": qty}
	if err := ctx.PlatformAPI().CallAppResult("checkout", "cart_add_item", addArgs, &checkoutCart); err != nil {
		if !checkoutCartRequiresRebuild(err) {
			return nil, fmt.Errorf("checkout cart add item: %w", err)
		}
		cart, err = a.rebuildCheckoutCart(ctx, pid, cart)
		if err != nil {
			return nil, fmt.Errorf("rebuild stale Checkout cart: %w", err)
		}
		addArgs["cart_id"] = *cart.CheckoutCartID
		if err := ctx.PlatformAPI().CallAppResult("checkout", "cart_add_item", addArgs, &checkoutCart); err != nil {
			return nil, fmt.Errorf("checkout cart add item after rebuild: %w", err)
		}
	}
	checkoutItemID, checkoutQty := checkoutCartItem(checkoutCart, price.ID)
	if checkoutItemID == 0 || math.Abs(checkoutQty-(previousQty+qty)) > 1e-9 {
		if checkoutItemID != 0 {
			if compensationErr := setCheckoutItemQuantity(ctx, pid, *cart.CheckoutCartID, checkoutItemID, previousQty); compensationErr != nil {
				return nil, fmt.Errorf("Checkout returned an inconsistent cart item snapshot; compensation failed: %w", compensationErr)
			}
		}
		return nil, errors.New("Checkout returned an inconsistent cart item snapshot")
	}
	canonicalVariant := *variant
	canonicalVariant.PriceCents = price.UnitAmountCents
	canonicalVariant.Currency = price.Currency
	if err := dbCartAddItem(ctx.AppDB(), pid, cart.ID, &canonicalVariant, qty, checkoutItemID); err != nil {
		if compensationErr := setCheckoutItemQuantity(ctx, pid, *cart.CheckoutCartID, checkoutItemID, previousQty); compensationErr != nil {
			return nil, fmt.Errorf("persist cart item: %w; compensation failed: %v", err, compensationErr)
		}
		return nil, err
	}
	return dbCartGet(ctx.AppDB(), pid, cart.ID, true)
}

func (a *App) setCartItemQuantity(ctx *sdk.AppCtx, args map[string]any) (*Cart, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	cart, err := resolveCart(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if cart.Status != "open" {
		return nil, fmt.Errorf("cart is %s; only open carts accept item changes", cart.Status)
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	item, err := dbCartItemGet(ctx.AppDB(), pid, cart.ID, intArg(args, "item_id"))
	if err != nil || item == nil {
		return nil, firstErr(err, errors.New("cart item not found"))
	}
	qty := floatArg(args, "quantity", -1)
	if qty < 0 {
		return nil, errors.New("quantity must be >= 0")
	}
	if item.InventoryItemID != nil && *item.InventoryItemID > 0 && qty > item.Quantity {
		var availability map[string]any
		if err := ctx.PlatformAPI().CallAppResult("inventory", "inventory_availability_check", map[string]any{
			"_project_id": pid, "item_id": *item.InventoryItemID, "quantity": qty,
		}, &availability); err != nil {
			return nil, fmt.Errorf("check inventory: %w", err)
		}
		if ok, _ := availability["can_reserve"].(bool); !ok {
			return nil, errors.New("insufficient inventory available")
		}
	}
	if cart.CheckoutCartID == nil || item.CheckoutItemID == nil || *item.CheckoutItemID == 0 {
		return nil, errors.New("cart item is not synchronized with Checkout")
	}
	var checkoutCart map[string]any
	setArgs := map[string]any{
		"_project_id": pid, "cart_id": *cart.CheckoutCartID, "item_id": *item.CheckoutItemID, "quantity": qty,
	}
	if err := ctx.PlatformAPI().CallAppResult("checkout", "cart_set_quantity", setArgs, &checkoutCart); err != nil {
		if !checkoutCartRequiresRebuild(err) {
			return nil, fmt.Errorf("checkout cart set quantity: %w", err)
		}
		cart, err = a.rebuildCheckoutCart(ctx, pid, cart)
		if err != nil {
			return nil, fmt.Errorf("rebuild stale Checkout cart: %w", err)
		}
		item, err = dbCartItemGet(ctx.AppDB(), pid, cart.ID, item.ID)
		if err != nil || item == nil || item.CheckoutItemID == nil {
			return nil, firstErr(err, errors.New("rebuilt cart item is not synchronized with Checkout"))
		}
		setArgs["cart_id"] = *cart.CheckoutCartID
		setArgs["item_id"] = *item.CheckoutItemID
		if err := ctx.PlatformAPI().CallAppResult("checkout", "cart_set_quantity", setArgs, &checkoutCart); err != nil {
			return nil, fmt.Errorf("checkout cart set quantity after rebuild: %w", err)
		}
	}
	if err := dbCartSetQuantity(ctx.AppDB(), pid, cart.ID, item.ID, qty); err != nil {
		if compensationErr := setCheckoutItemQuantity(ctx, pid, *cart.CheckoutCartID, *item.CheckoutItemID, item.Quantity); compensationErr != nil {
			return nil, fmt.Errorf("persist cart quantity: %w; compensation failed: %v", err, compensationErr)
		}
		return nil, err
	}
	return dbCartGet(ctx.AppDB(), pid, cart.ID, true)
}

func (a *App) checkoutStart(ctx *sdk.AppCtx, args map[string]any) (*CheckoutSession, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	cart, err := resolveCart(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if existing, err := dbCheckoutGetByCart(ctx.AppDB(), pid, cart.ID); err != nil {
		return nil, err
	} else if existing != nil && existing.Status != "cancelled" && existing.Status != "expired" {
		return existing, nil
	}
	if cart.Status != "open" {
		return nil, fmt.Errorf("cart is %s; only open carts can start checkout", cart.Status)
	}
	if len(cart.Items) == 0 {
		return nil, errors.New("cannot checkout an empty cart")
	}
	if _, err := cartSourceGroups(ctx.AppDB(), pid, cart); err != nil {
		return nil, err
	}
	discountReservationID, err := a.reserveCartDiscount(ctx, pid, cart)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started && discountReservationID != "" {
			_ = a.releaseDiscountReservation(ctx, pid, discountReservationID)
		}
	}()
	resIDs := []int64{}
	if ctx.PlatformAPI() == nil || cart.CheckoutCartID == nil {
		return nil, errors.New("cart is not linked to Checkout")
	}
	for _, it := range cart.Items {
		if it.InventoryItemID == nil || *it.InventoryItemID == 0 {
			continue
		}
		reservationID, err := a.reserveCartItem(ctx, pid, cart, it)
		if err != nil {
			releaseErr := a.releaseReservations(ctx, pid, resIDs)
			if releaseErr != nil {
				return nil, fmt.Errorf("reserve inventory for cart item %d: %w; compensation failed: %v", it.ID, err, releaseErr)
			}
			return nil, fmt.Errorf("reserve inventory for cart item %d: %w", it.ID, err)
		}
		resIDs = append(resIDs, reservationID)
	}
	var checkoutResp map[string]any
	if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_start", map[string]any{"_project_id": pid, "cart_id": *cart.CheckoutCartID}, &checkoutResp); err != nil {
		releaseErr := a.releaseReservations(ctx, pid, resIDs)
		if releaseErr != nil {
			return nil, fmt.Errorf("checkout_start: %w; compensation failed: %v", err, releaseErr)
		}
		return nil, fmt.Errorf("checkout_start: %w", err)
	}
	checkoutSession := unwrap(checkoutResp, "session")
	checkoutSessionID := intArg(checkoutSession, "id")
	if checkoutSessionID == 0 {
		if releaseErr := a.releaseReservations(ctx, pid, resIDs); releaseErr != nil {
			return nil, fmt.Errorf("Checkout response missing session id; compensation failed: %w", releaseErr)
		}
		return nil, errors.New("Checkout response missing session id")
	}
	var adjustmentResp map[string]any
	quoteSnapshot := mapArg(cart.Metadata, "checkout_quote")
	if len(quoteSnapshot) == 0 {
		quoteSnapshot = mapArg(cart.Metadata, "shipping_quote")
	}
	if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_set_adjustments", map[string]any{
		"_project_id":    pid,
		"session_id":     checkoutSessionID,
		"shipping_cents": cart.ShippingCents,
		"discount_cents": cart.DiscountCents,
		"tax_cents":      cart.TaxCents,
		"adjustments":    quoteSnapshot,
	}, &adjustmentResp); err != nil {
		if compensationErr := a.cancelCheckoutAndRelease(ctx, pid, checkoutSessionID, resIDs); compensationErr != nil {
			return nil, fmt.Errorf("checkout_set_adjustments: %w; compensation failed: %v", err, compensationErr)
		}
		return nil, fmt.Errorf("checkout_set_adjustments: %w", err)
	}
	checkoutSession = unwrap(adjustmentResp, "session")
	if intArg(checkoutSession, "total_cents") != cart.TotalCents || strings.ToUpper(strArg(checkoutSession, "currency")) != cart.Currency {
		if compensationErr := a.cancelCheckoutAndRelease(ctx, pid, checkoutSessionID, resIDs); compensationErr != nil {
			return nil, fmt.Errorf("Commerce and Checkout cart totals do not match; compensation failed: %w", compensationErr)
		}
		return nil, errors.New("Commerce and Checkout cart totals do not match")
	}
	out, err := dbCheckoutCreate(ctx.AppDB(), pid, cart, checkoutSessionID, resIDs, discountReservationID, quoteSnapshot)
	if err != nil {
		if compensationErr := a.cancelCheckoutAndRelease(ctx, pid, checkoutSessionID, resIDs); compensationErr != nil {
			return nil, fmt.Errorf("persist checkout: %w; compensation failed: %v", err, compensationErr)
		}
		return nil, err
	}
	started = true
	return out, nil
}

func (a *App) checkoutUpdate(ctx *sdk.AppCtx, args map[string]any) (*CheckoutSession, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	ch, err := dbCheckoutGet(ctx.AppDB(), pid, intArg(args, "checkout_id"))
	if err != nil || ch == nil {
		return nil, firstErr(err, errors.New("checkout not found"))
	}
	patch := copyMap(mapArg(args, "patch"))
	if email := firstNonEmpty(strArg(patch, "email"), strArg(patch, "customer_email")); email != "" {
		patch["email"] = strings.ToLower(email)
		delete(patch, "customer_email")
	}
	if name := firstNonEmpty(strArg(patch, "customer_name"), strArg(patch, "name")); name != "" {
		patch["customer_name"] = name
		delete(patch, "name")
	}
	if checkoutPatchMatches(ch, patch) {
		return ch, nil
	}
	if ch.Status != "started" {
		if ch.Status == "awaiting_payment" && ch.CheckoutSessionID != nil && ctx.PlatformAPI() != nil {
			if matches, err := checkoutRemotePatchMatches(ctx, pid, *ch.CheckoutSessionID, patch); err != nil {
				return nil, err
			} else if matches {
				return ch, nil
			}
		}
		return nil, fmt.Errorf("checkout is %s; only started checkouts accept updates", ch.Status)
	}
	if ch.CheckoutSessionID != nil && ctx.PlatformAPI() != nil {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_update", map[string]any{"_project_id": pid, "session_id": *ch.CheckoutSessionID, "patch": patch}, &out); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "awaiting_payment") {
				if matches, matchErr := checkoutRemotePatchMatches(ctx, pid, *ch.CheckoutSessionID, patch); matchErr != nil {
					return nil, matchErr
				} else if matches {
					return ch, nil
				}
			}
			return nil, fmt.Errorf("checkout_update: %w", err)
		}
		if step := firstNonEmpty(strArg(patch, "current_step"), strArg(patch, "step")); step != "" {
			advanceArgs := map[string]any{
				"_project_id": pid, "session_id": *ch.CheckoutSessionID, "step": step,
			}
			if value, ok := patch["buyer_details"]; ok {
				advanceArgs["buyer_details"] = value
			}
			if selected := mapArg(ch.Quote, "selected_shipping"); len(selected) > 0 {
				advanceArgs["selected_shipping"] = selected
			}
			if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_advance", advanceArgs, &out); err != nil {
				return nil, fmt.Errorf("checkout_advance: %w", err)
			}
		}
	}
	if err := dbCheckoutPatch(ctx.AppDB(), pid, ch.ID, patch); err != nil {
		return nil, err
	}
	return dbCheckoutGet(ctx.AppDB(), pid, ch.ID)
}

func checkoutPatchMatches(ch *CheckoutSession, patch map[string]any) bool {
	if hasKey(patch, "current_step") || hasKey(patch, "step") || hasKey(patch, "buyer_details") {
		return false
	}
	if hasKey(patch, "email") && !strings.EqualFold(ch.CustomerEmail, strArg(patch, "email")) {
		return false
	}
	if hasKey(patch, "customer_name") && strings.TrimSpace(ch.CustomerName) != strArg(patch, "customer_name") {
		return false
	}
	if hasKey(patch, "shipping_address") && jsonText(ch.ShippingAddress, "{}") != jsonText(patch["shipping_address"], "{}") {
		return false
	}
	if hasKey(patch, "billing_address") && jsonText(ch.BillingAddress, "{}") != jsonText(patch["billing_address"], "{}") {
		return false
	}
	return true
}

func checkoutRemotePatchMatches(ctx *sdk.AppCtx, pid string, sessionID int64, patch map[string]any) (bool, error) {
	var response map[string]any
	if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_get", map[string]any{
		"_project_id": pid,
		"session_id":  sessionID,
	}, &response); err != nil {
		return false, fmt.Errorf("checkout_get: %w", err)
	}
	return checkoutExternalPatchMatches(unwrap(response, "session"), patch), nil
}

func checkoutExternalPatchMatches(session, patch map[string]any) bool {
	for key, value := range patch {
		switch key {
		case "current_step", "step":
			if !strings.EqualFold(strArg(session, "current_step"), strings.TrimSpace(fmt.Sprint(value))) {
				return false
			}
		case "buyer_details", "shipping_address", "billing_address":
			if jsonText(session[key], "{}") != jsonText(value, "{}") {
				return false
			}
		case "email", "customer_email":
			if !strings.EqualFold(strArg(session, "email"), strings.TrimSpace(fmt.Sprint(value))) {
				return false
			}
		case "customer_name", "name":
			if strArg(session, "customer_name") != strings.TrimSpace(fmt.Sprint(value)) {
				return false
			}
		default:
			return false
		}
	}
	return len(patch) > 0
}

func (a *App) checkoutPay(ctx *sdk.AppCtx, args map[string]any) (*Sale, *CheckoutSession, map[string]any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, nil, nil, err
	}
	ch, err := dbCheckoutGet(ctx.AppDB(), pid, intArg(args, "checkout_id"))
	if err != nil || ch == nil {
		return nil, nil, nil, firstErr(err, errors.New("checkout not found"))
	}
	if ch.CheckoutSessionID == nil || ctx.PlatformAPI() == nil {
		return nil, nil, nil, errors.New("checkout is not linked to Checkout")
	}
	existing, err := dbSaleGetByCheckout(ctx.AppDB(), pid, ch.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	var checkoutResponse map[string]any
	if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_get", map[string]any{"_project_id": pid, "session_id": *ch.CheckoutSessionID}, &checkoutResponse); err != nil {
		return nil, nil, nil, fmt.Errorf("checkout_get: %w", err)
	}
	external := unwrap(checkoutResponse, "session")
	externalStatus := strArg(external, "status")
	if externalStatus != "started" && externalStatus != "awaiting_payment" && externalStatus != "paid" && externalStatus != "completed" {
		return nil, nil, nil, fmt.Errorf("Checkout session cannot be paid from status %q", externalStatus)
	}
	cart, err := dbCartGet(ctx.AppDB(), pid, ch.CartID, true)
	if err != nil || cart == nil {
		return nil, nil, nil, firstErr(err, errors.New("cart not found"))
	}
	if intArg(external, "total_cents") != cart.TotalCents || strings.ToUpper(strArg(external, "currency")) != cart.Currency {
		return nil, nil, nil, errors.New("Commerce and Checkout totals do not match")
	}
	store, err := dbStoreGetByID(ctx.AppDB(), pid, ch.StoreID)
	if err != nil || store == nil {
		return nil, nil, nil, firstErr(err, errors.New("store not found"))
	}
	payment := map[string]any{
		"provider": store.PaymentProvider, "presentation": store.PaymentPresentation,
	}
	if externalStatus == "started" || externalStatus == "awaiting_payment" {
		paymentArgs, err := commercePaymentArgs(ctx, pid, store, ch)
		if err != nil {
			return nil, nil, nil, err
		}
		checkoutResponse = nil
		paymentArgs["_project_id"] = pid
		paymentArgs["session_id"] = *ch.CheckoutSessionID
		if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_pay", paymentArgs, &checkoutResponse); err != nil {
			return nil, nil, nil, fmt.Errorf("checkout_pay: %w", err)
		}
		external = unwrap(checkoutResponse, "session")
		externalStatus = strArg(external, "status")
		if value := mapArg(checkoutResponse, "payment"); len(value) > 0 {
			payment = value
		}
	}
	invoiceID := firstNonZero(intArg(checkoutResponse, "invoice_id"), intArg(external, "invoice_id"))
	invoiceNumber := strArg(checkoutResponse, "invoice_number")
	if invoiceID == 0 {
		return nil, nil, nil, errors.New("Checkout has not produced an invoice")
	}
	if invoiceNumber == "" {
		var billingResponse map[string]any
		if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_get", map[string]any{"_project_id": pid, "id": invoiceID}, &billingResponse); err == nil {
			invoiceNumber = strArg(unwrap(billingResponse, "invoice"), "number")
		}
	}
	if err := dbCheckoutInvoice(ctx.AppDB(), pid, ch.ID, invoiceID, invoiceNumber); err != nil {
		return nil, nil, nil, err
	}
	ch, err = dbCheckoutGet(ctx.AppDB(), pid, ch.ID)
	if err != nil || ch == nil {
		return nil, nil, nil, firstErr(err, errors.New("checkout disappeared after invoice creation"))
	}
	sale := existing
	if sale == nil {
		sale, err = dbSaleCreateFromCheckout(ctx.AppDB(), pid, ch)
	}
	if err == nil && (externalStatus == "paid" || externalStatus == "completed") {
		sale, err = a.completePaidSale(ctx, sale, true)
	}
	return sale, ch, payment, err
}

func commercePaymentArgs(ctx *sdk.AppCtx, pid string, store *Store, checkout *CheckoutSession) (map[string]any, error) {
	provider := strings.ToLower(strings.TrimSpace(store.PaymentProvider))
	if provider == "" {
		provider = "manual"
	}
	if provider == "manual" {
		return map[string]any{"provider": "manual", "presentation": "manual"}, nil
	}
	if provider != "stripe" {
		return nil, fmt.Errorf("unsupported store payment provider %q", provider)
	}
	presentation := strings.ToLower(strings.TrimSpace(store.PaymentPresentation))
	if presentation == "" {
		presentation = "elements"
	}
	if presentation != "elements" && presentation != "hosted" {
		return nil, fmt.Errorf("unsupported Stripe presentation %q", presentation)
	}
	returnURL, cancelURL, err := commerceCheckoutURLs(ctx, pid, store)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"provider":        provider,
		"presentation":    presentation,
		"idempotency_key": fmt.Sprintf("commerce-stripe-%s-v2-%s-%d-%s", presentation, pid, checkout.ID, checkoutPaymentFingerprint(checkout)),
	}
	paymentSettings := mapArg(store.Metadata, "payments")
	if methods := providerRows(paymentSettings["payment_method_types"]); len(methods) > 0 {
		out["payment_method_types"] = methods
	}
	if boolArg(paymentSettings, "save_payment_method") {
		out["save_payment_method"] = true
	}
	if boolArg(paymentSettings, "set_default_payment_method") {
		out["set_default_payment_method"] = true
	}
	if presentation == "elements" {
		out["return_url"] = returnURL
	} else {
		out["success_url"] = returnURL
		out["cancel_url"] = cancelURL
	}
	return out, nil
}

func checkoutPaymentFingerprint(checkout *CheckoutSession) string {
	payload, _ := json.Marshal(map[string]any{
		"checkout_session_id": checkout.CheckoutSessionID,
		"quote":               checkout.Quote,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:8])
}

func commerceReturnURL(ctx *sdk.AppCtx, pid string, store *Store) (string, error) {
	returnURL, _, err := commerceCheckoutURLs(ctx, pid, store)
	return returnURL, err
}

func commerceCheckoutURLs(ctx *sdk.AppCtx, pid string, store *Store) (string, string, error) {
	if base := strings.TrimRight(strings.TrimSpace(store.PublicBaseURL), "/"); base != "" {
		u, err := url.Parse(base)
		if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return "", "", errors.New("store public_base_url must be an absolute http(s) URL")
		}
		return base + "/checkout/return", base + "/checkout", nil
	}
	info, err := ctx.PlatformInfo()
	if err != nil || info == nil || strings.TrimSpace(info.PublicURL) == "" {
		return "", "", errors.New("Stripe checkout requires the store public_base_url or the platform public_url")
	}
	siteSlug := strArg(store.Metadata, "content_site_slug")
	if siteSlug == "" {
		return "", "", errors.New("Stripe checkout requires a configured Content storefront")
	}
	query := url.Values{"project_id": []string{pid}, "site": []string{siteSlug}}
	base := strings.TrimRight(info.PublicURL, "/") + "/api/apps/content/public"
	return base + "/checkout/return?" + query.Encode(), base + "/checkout?" + query.Encode(), nil
}

func (a *App) checkoutCancel(ctx *sdk.AppCtx, args map[string]any) (*CheckoutSession, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	ch, err := dbCheckoutGet(ctx.AppDB(), pid, intArg(args, "checkout_id"))
	if err != nil || ch == nil {
		return nil, firstErr(err, errors.New("checkout not found"))
	}
	if ch.Status == "paid" {
		return nil, errors.New("paid checkout cannot be cancelled")
	}
	if ch.Status == "cancelled" {
		return ch, nil
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	if ch.CheckoutSessionID != nil {
		var response map[string]any
		if err := ctx.PlatformAPI().CallAppResult("checkout", "checkout_cancel", map[string]any{
			"_project_id": pid, "session_id": *ch.CheckoutSessionID,
		}, &response); err != nil {
			message := strings.ToLower(err.Error())
			if !strings.Contains(message, "already cancelled") && !strings.Contains(message, "already expired") {
				return nil, fmt.Errorf("checkout_cancel: %w", err)
			}
		}
	}
	if err := a.releaseDiscountReservation(ctx, pid, ch.DiscountReservationID); err != nil {
		return nil, err
	}
	if err := dbReservationLinksEnsure(ctx.AppDB(), ch.ID, ch.ReservationIDs); err != nil {
		return nil, err
	}
	links, err := dbReservationLinks(ctx.AppDB(), ch.ID)
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		if link.Status != "active" {
			continue
		}
		if err := a.releaseReservations(ctx, pid, []int64{link.ReservationID}); err != nil {
			_ = dbReservationLinkStatus(ctx.AppDB(), ch.ID, link.ReservationID, "active", err.Error())
			return nil, err
		}
		if err := dbReservationLinkStatus(ctx.AppDB(), ch.ID, link.ReservationID, "released", ""); err != nil {
			return nil, err
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE commerce_checkout_sessions SET status='cancelled', completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, ch.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE commerce_carts SET status='open', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=? AND status='checkout'`, pid, ch.CartID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbCheckoutGet(ctx.AppDB(), pid, ch.ID)
}

func (a *App) markSalePaid(ctx *sdk.AppCtx, args map[string]any) (*Sale, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	sale, err := dbSaleGet(ctx.AppDB(), pid, intArg(args, "sale_id"))
	if err != nil || sale == nil {
		return nil, firstErr(err, errors.New("sale not found"))
	}
	return a.completePaidSale(ctx, sale, false)
}

func (a *App) completePaidSale(ctx *sdk.AppCtx, sale *Sale, trustedBillingEvent bool) (*Sale, error) {
	pid := ctx.CurrentProject()
	if sale.PaymentStatus == "paid" && sale.Status == "paid" {
		if err := a.queueSaleDispatches(ctx, pid, sale); err != nil {
			message := "queue provider fulfillment: " + err.Error()
			_ = dbSaleSetProcessingError(ctx.AppDB(), pid, sale.ID, message)
			ctx.Emit("commerce.sale.processing_failed", map[string]any{"sale_id": sale.ID, "error": message})
			return nil, errors.New(message)
		}
		return sale, nil
	}
	if sale.InvoiceID == nil || *sale.InvoiceID == 0 {
		return nil, errors.New("sale has no Billing invoice")
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	if !trustedBillingEvent {
		var response map[string]any
		if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_get", map[string]any{
			"_project_id": pid, "id": *sale.InvoiceID,
		}, &response); err != nil {
			return nil, fmt.Errorf("verify Billing invoice: %w", err)
		}
		invoice := unwrap(response, "invoice")
		status := strArg(invoice, "status")
		paid := intArg(invoice, "amount_paid_cents")
		total := intArg(invoice, "total_cents")
		currency := strings.ToUpper(strArg(invoice, "currency"))
		if status != "paid" || total != sale.TotalCents || paid < sale.TotalCents || currency != "" && currency != sale.Currency {
			return nil, errors.New("Billing invoice is not fully paid")
		}
	}
	if _, err := ctx.AppDB().Exec(`UPDATE commerce_sales SET payment_status='paid', status='processing',
		paid_at=COALESCE(paid_at, CURRENT_TIMESTAMP), processing_error='', updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, pid, sale.ID); err != nil {
		return nil, err
	}
	ch, err := dbCheckoutGet(ctx.AppDB(), pid, ptrValue(sale.CheckoutID))
	if err != nil || ch == nil {
		return nil, firstErr(err, errors.New("checkout not found for sale"))
	}
	if err := dbReservationLinksEnsure(ctx.AppDB(), ch.ID, ch.ReservationIDs); err != nil {
		return nil, err
	}
	links, err := dbReservationLinks(ctx.AppDB(), ch.ID)
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		if link.Status == "committed" {
			continue
		}
		var response map[string]any
		if err := ctx.PlatformAPI().CallAppResult("inventory", "inventory_commit_reservation", map[string]any{
			"_project_id": pid, "reservation_id": link.ReservationID,
		}, &response); err != nil {
			message := fmt.Sprintf("commit inventory reservation %d: %v", link.ReservationID, err)
			_ = dbReservationLinkStatus(ctx.AppDB(), ch.ID, link.ReservationID, link.Status, message)
			_ = dbSaleSetProcessingError(ctx.AppDB(), pid, sale.ID, message)
			ctx.Emit("commerce.sale.processing_failed", map[string]any{"sale_id": sale.ID, "error": message})
			return nil, errors.New(message)
		}
		if err := dbReservationLinkStatus(ctx.AppDB(), ch.ID, link.ReservationID, "committed", ""); err != nil {
			return nil, err
		}
	}
	if err := a.redeemDiscountReservation(ctx, pid, ch.DiscountReservationID); err != nil {
		message := err.Error()
		_ = dbSaleSetProcessingError(ctx.AppDB(), pid, sale.ID, message)
		ctx.Emit("commerce.sale.processing_failed", map[string]any{"sale_id": sale.ID, "error": message})
		return nil, err
	}
	orderID := ptrValue(sale.OrderID)
	if orderID == 0 {
		orderID, err = a.createOrderForSale(ctx, pid, sale)
		if err != nil {
			message := "create order: " + err.Error()
			_ = dbSaleSetProcessingError(ctx.AppDB(), pid, sale.ID, message)
			ctx.Emit("commerce.sale.processing_failed", map[string]any{"sale_id": sale.ID, "error": message})
			return nil, errors.New(message)
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE commerce_sales SET status='paid', payment_status='paid', order_id=?,
		fulfillment_status='unsubmitted', processing_error='', paid_at=COALESCE(paid_at, CURRENT_TIMESTAMP), updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, nullableInt(orderID), pid, sale.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE commerce_checkout_sessions SET status='paid', completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, ch.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE commerce_carts SET status='converted', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, ptrValue(sale.CartID)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	completed, err := dbSaleGet(ctx.AppDB(), pid, sale.ID)
	if err != nil || completed == nil {
		return completed, err
	}
	if err := a.queueSaleDispatches(ctx, pid, completed); err != nil {
		message := "queue provider fulfillment: " + err.Error()
		_ = dbSaleSetProcessingError(ctx.AppDB(), pid, completed.ID, message)
		ctx.Emit("commerce.sale.processing_failed", map[string]any{"sale_id": completed.ID, "error": message})
		return nil, errors.New(message)
	}
	ctx.Emit("commerce.sale.paid", map[string]any{"sale_id": completed.ID, "store_id": completed.StoreID})
	return completed, nil
}

func (a *App) createOrderForSale(ctx *sdk.AppCtx, pid string, sale *Sale) (int64, error) {
	if len(sale.Items) == 0 {
		items, err := dbSaleItems(ctx.AppDB(), pid, sale.ID)
		if err != nil {
			return 0, err
		}
		sale.Items = items
	}
	if len(sale.Items) == 0 {
		return 0, errors.New("sale has no immutable line items")
	}
	sourceRef := fmt.Sprintf("sale:%d", sale.ID)
	var search map[string]any
	if err := ctx.PlatformAPI().CallAppResult("orders", "orders_search", map[string]any{
		"_project_id": pid, "source": "commerce", "q": sourceRef, "limit": 10,
	}, &search); err != nil {
		return 0, fmt.Errorf("search existing order: %w", err)
	}
	if rows, ok := search["orders"].([]any); ok {
		for _, raw := range rows {
			order, _ := raw.(map[string]any)
			if strArg(order, "source_ref") == sourceRef && intArg(order, "id") != 0 {
				return intArg(order, "id"), nil
			}
		}
	}
	lines := []map[string]any{}
	for _, it := range sale.Items {
		lines = append(lines, map[string]any{
			"catalog_price_id":   ptrValue(it.CatalogPriceID),
			"catalog_product_id": ptrValue(it.CatalogProductID),
			"sku":                it.SKU,
			"title":              it.TitleSnapshot,
			"quantity":           it.Quantity,
			"unit_amount_cents":  it.UnitAmountCents,
			"currency":           it.Currency,
			"metadata": map[string]any{
				"commerce_variant_id": ptrValue(it.VariantID), "commerce_listing_id": ptrValue(it.ListingID),
				"inventory_item_id": ptrValue(it.InventoryItemID),
			},
		})
	}
	var out map[string]any
	err := ctx.PlatformAPI().CallAppResult("orders", "orders_create", map[string]any{
		"_project_id":         pid,
		"source":              "commerce",
		"source_ref":          sourceRef,
		"checkout_session_id": ptrValue(sale.CheckoutSessionID),
		"cart_id":             ptrValue(sale.CartID),
		"invoice_id":          ptrValue(sale.InvoiceID),
		"customer_email":      sale.CustomerEmail,
		"customer_name":       sale.CustomerName,
		"currency":            sale.Currency,
		"subtotal_cents":      sale.SubtotalCents,
		"discount_cents":      sale.DiscountCents,
		"tax_cents":           sale.TaxCents,
		"shipping_cents":      sale.ShippingCents,
		"total_cents":         sale.TotalCents,
		"shipping_address":    sale.ShippingAddress,
		"billing_address":     sale.BillingAddress,
		"payment_status":      "paid",
		"order_status":        "paid",
		"fulfillment_status":  "unsubmitted",
		"items":               lines,
	}, &out)
	if err != nil {
		return 0, err
	}
	orderID := intArg(unwrap(out, "order"), "id")
	if orderID == 0 {
		return 0, errors.New("Orders response missing order id")
	}
	return orderID, nil
}

func dbStoreCreate(db *sql.DB, pid string, args map[string]any) (*Store, error) {
	slug := slugify(firstNonEmpty(strArg(args, "slug"), strArg(args, "name")))
	name := strings.TrimSpace(strArg(args, "name"))
	if slug == "" || name == "" {
		return nil, errors.New("slug and name required")
	}
	currency := strings.ToUpper(firstNonEmpty(strArg(args, "default_currency"), "USD"))
	if !looksLikeCurrency(currency) {
		return nil, errors.New("default_currency must be a 3-letter ISO code")
	}
	paymentProvider := strings.ToLower(firstNonEmpty(strArg(args, "payment_provider"), "manual"))
	paymentPresentation := strings.ToLower(firstNonEmpty(strArg(args, "payment_presentation"), "elements"))
	if err := validateStorePayment(paymentProvider, paymentPresentation); err != nil {
		return nil, err
	}
	res, err := db.Exec(`INSERT INTO commerce_stores
		(project_id, slug, name, public_base_url, default_currency, default_locale, timezone, payment_provider, payment_presentation, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, slug, name, strArg(args, "public_base_url"), currency,
		firstNonEmpty(strArg(args, "default_locale"), "en"), firstNonEmpty(strArg(args, "timezone"), "UTC"),
		paymentProvider, paymentPresentation, jsonText(args["metadata"], "{}"))
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
	if len(rows) > 1 {
		return nil, errors.New("store_id or store_slug required when multiple stores exist")
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
	store, err := dbStoreGetByID(db, pid, id)
	if err != nil || store == nil {
		return nil, firstErr(err, errors.New("store not found"))
	}
	sets, vals := []string{}, []any{}
	for _, k := range []string{"name", "public_base_url", "default_locale", "timezone", "order_number_format", "checkout_mode"} {
		if v := strArg(patch, k); v != "" {
			sets = append(sets, k+"=?")
			vals = append(vals, v)
		}
	}
	if status := strArg(patch, "status"); status != "" {
		if status != "active" && status != "inactive" && status != "archived" {
			return nil, errors.New("status must be active, inactive, or archived")
		}
		sets = append(sets, "status=?", "archived_at=CASE WHEN ?='archived' THEN CURRENT_TIMESTAMP ELSE NULL END")
		vals = append(vals, status, status)
	}
	if v := strArg(patch, "default_currency"); v != "" {
		v = strings.ToUpper(v)
		if !looksLikeCurrency(v) {
			return nil, errors.New("default_currency must be a 3-letter ISO code")
		}
		sets = append(sets, "default_currency=?")
		vals = append(vals, v)
	}
	paymentProvider := firstNonEmpty(strArg(patch, "payment_provider"), store.PaymentProvider)
	paymentPresentation := firstNonEmpty(strArg(patch, "payment_presentation"), store.PaymentPresentation)
	if hasKey(patch, "payment_provider") || hasKey(patch, "payment_presentation") {
		paymentProvider = strings.ToLower(paymentProvider)
		paymentPresentation = strings.ToLower(paymentPresentation)
		if err := validateStorePayment(paymentProvider, paymentPresentation); err != nil {
			return nil, err
		}
		sets = append(sets, "payment_provider=?", "payment_presentation=?")
		vals = append(vals, paymentProvider, paymentPresentation)
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
	status := firstNonEmpty(strArg(args, "status"), "draft")
	if status != "draft" && status != "active" {
		return nil, errors.New("new product status must be draft or active")
	}
	res, err := db.Exec(`INSERT INTO commerce_listings
		(project_id, store_id, catalog_product_id, handle, title, description_html, vendor, product_type, status, seo_title, seo_description, featured_media_id, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, storeID, nullableInt(intArg(args, "catalog_product_id")), handle, title, strArg(args, "description_html"),
		strArg(args, "vendor"), strArg(args, "product_type"), status,
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
	if status := strArg(args, "status"); status != "" {
		where = append(where, "status=?")
		vals = append(vals, status)
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
	l.Variants, err = dbVariantsForListing(db, pid, l.ID)
	if err != nil {
		return nil, err
	}
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
		l.Variants, err = dbVariantsForListing(db, pid, l.ID)
		if err != nil {
			return nil, err
		}
	}
	return l, nil
}

func dbListingStatus(db *sql.DB, pid string, id int64, status string) (*Listing, error) {
	listing, err := dbListingGet(db, pid, id, true)
	if err != nil || listing == nil {
		return nil, firstErr(err, errors.New("product not found"))
	}
	if status == "active" {
		if len(listing.Variants) == 0 {
			return nil, errors.New("product requires at least one variant before publishing")
		}
		for _, variant := range listing.Variants {
			if variant.CatalogPriceID == nil || *variant.CatalogPriceID == 0 || variant.PriceCents <= 0 || !looksLikeCurrency(variant.Currency) {
				return nil, fmt.Errorf("variant %d requires a canonical Catalog price before publishing", variant.ID)
			}
		}
	}
	if _, err := db.Exec(`UPDATE commerce_listings SET status=?, archived_at=CASE WHEN ?='archived' THEN CURRENT_TIMESTAMP ELSE NULL END, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, status, status, pid, id); err != nil {
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
	if intArg(args, "price_cents") < 0 {
		return nil, errors.New("price_cents cannot be negative")
	}
	if !looksLikeCurrency(strings.ToUpper(currency)) {
		return nil, errors.New("currency must be a 3-letter ISO code")
	}
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
	if existing, err := dbVariantGet(db, pid, id); err != nil || existing == nil {
		return nil, firstErr(err, errors.New("variant not found"))
	}
	sets, vals := []string{}, []any{}
	for _, k := range []string{"sku", "title", "option1", "option2", "option3", "currency"} {
		if v := strArg(patch, k); v != "" {
			if k == "sku" || k == "currency" {
				v = strings.ToUpper(v)
			}
			if k == "currency" && !looksLikeCurrency(v) {
				return nil, errors.New("currency must be a 3-letter ISO code")
			}
			sets = append(sets, k+"=?")
			vals = append(vals, v)
		}
	}
	for _, k := range []string{"catalog_price_id", "inventory_item_id", "price_cents", "compare_at_price_cents", "sort_order"} {
		if hasKey(patch, k) {
			if (k == "price_cents" || k == "compare_at_price_cents") && intArg(patch, k) < 0 {
				return nil, fmt.Errorf("%s cannot be negative", k)
			}
			sets = append(sets, k+"=?")
			if k == "catalog_price_id" || k == "inventory_item_id" {
				vals = append(vals, nullableInt(intArg(patch, k)))
			} else {
				vals = append(vals, intArg(patch, k))
			}
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
	title := strings.TrimSpace(strArg(args, "title"))
	handle := slugify(firstNonEmpty(strArg(args, "handle"), title))
	if storeID == 0 || title == "" || handle == "" {
		return nil, errors.New("store_id and title required")
	}
	store, err := dbStoreGetByID(db, pid, storeID)
	if err != nil || store == nil {
		return nil, firstErr(err, errors.New("store not found"))
	}
	status := firstNonEmpty(strArg(args, "status"), "active")
	if status != "active" && status != "draft" {
		return nil, errors.New("new collection status must be active or draft")
	}
	res, err := db.Exec(`INSERT INTO commerce_collections (project_id, store_id, handle, title, description_html, status, sort_order, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, pid, storeID, handle, title, strArg(args, "description_html"), status, intArg(args, "sort_order"), jsonText(args["metadata"], "{}"))
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

func resolveCollection(db *sql.DB, pid string, args map[string]any) (*Collection, error) {
	if id := intArg(args, "id"); id != 0 {
		collection, err := dbCollectionGet(db, pid, id)
		if err != nil || collection == nil {
			return nil, firstErr(err, errors.New("collection not found"))
		}
		return collection, nil
	}
	handle := strArg(args, "handle")
	if handle == "" {
		return nil, errors.New("id or handle required")
	}
	store, err := resolveStore(db, pid, args)
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(collectionSelect()+` WHERE project_id=? AND store_id=? AND handle=?`, pid, store.ID, slugify(handle))
	collection, err := scanCollection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("collection not found")
	}
	return collection, err
}

func dbCollectionAddListing(db *sql.DB, pid string, collectionID, listingID int64, sortOrder int) error {
	if collectionID == 0 || listingID == 0 {
		return errors.New("collection_id and listing_id required")
	}
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
	_, err = db.Exec(`INSERT INTO commerce_collection_listings (collection_id, listing_id, sort_order)
		VALUES (?, ?, ?) ON CONFLICT(collection_id, listing_id) DO UPDATE SET sort_order=excluded.sort_order`, collectionID, listingID, sortOrder)
	return err
}

func dbCartCreate(db *sql.DB, pid string, args map[string]any) (*Cart, error) {
	storeID := intArg(args, "store_id")
	token := firstNonEmpty(strArg(args, "session_token"), newToken())
	if storeID == 0 {
		return nil, errors.New("store_id required")
	}
	var id int64
	err := db.QueryRow(`INSERT INTO commerce_carts
		(project_id, store_id, checkout_cart_id, session_token, currency, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, store_id, session_token) DO UPDATE SET
		checkout_cart_id=excluded.checkout_cart_id, updated_at=CURRENT_TIMESTAMP
		RETURNING id`,
		pid, storeID, nullableInt(intArg(args, "checkout_cart_id")), token, firstNonEmpty(strArg(args, "currency"), "USD"), jsonText(args["metadata"], "{}"),
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	return dbCartGet(db, pid, id, true)
}

func dbCartGetBySession(db *sql.DB, pid string, storeID int64, token string) (*Cart, error) {
	row := db.QueryRow(cartSelect()+` WHERE project_id=? AND store_id=? AND session_token=?`, pid, storeID, token)
	cart, err := scanCart(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cart.Items, err = dbCartItems(db, pid, cart.ID)
	return cart, err
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
	c, err := dbCartGetBySession(db, pid, storeID, token)
	if err != nil || c == nil {
		return nil, firstErr(err, errors.New("cart not found"))
	}
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
		c.Items, err = dbCartItems(db, pid, c.ID)
		if err != nil {
			return nil, err
		}
	}
	return c, nil
}

func dbCartAddItem(db *sql.DB, pid string, cartID int64, v *Variant, qty float64, checkoutItemID int64) error {
	listing, err := dbListingGet(db, pid, v.ListingID, false)
	if err != nil || listing == nil {
		return firstErr(err, errors.New("listing not found"))
	}
	title := cartItemTitle(listing.Title, v.Title)
	_, err = db.Exec(`INSERT INTO commerce_cart_items
		(project_id, cart_id, checkout_item_id, variant_id, listing_id, inventory_item_id, catalog_price_id, sku, title_snapshot, unit_amount_cents, currency, quantity)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(cart_id, variant_id) DO UPDATE SET quantity=quantity+excluded.quantity,
		checkout_item_id=excluded.checkout_item_id, unit_amount_cents=excluded.unit_amount_cents,
		currency=excluded.currency, title_snapshot=excluded.title_snapshot, updated_at=CURRENT_TIMESTAMP`,
		pid, cartID, nullableInt(checkoutItemID), v.ID, v.ListingID, nullablePtrInt(v.InventoryItemID), nullablePtrInt(v.CatalogPriceID), v.SKU, title, v.PriceCents, v.Currency, qty)
	if err != nil {
		return err
	}
	return recomputeCart(db, pid, cartID)
}

func cartItemTitle(productTitle, variantTitle string) string {
	productTitle = strings.TrimSpace(productTitle)
	variantTitle = strings.TrimSpace(variantTitle)
	if variantTitle == "" || strings.EqualFold(variantTitle, "Default") || strings.EqualFold(variantTitle, productTitle) {
		return productTitle
	}
	return productTitle + " - " + variantTitle
}

func dbCartSetQuantity(db *sql.DB, pid string, cartID, itemID int64, qty float64) error {
	if qty < 0 {
		return errors.New("quantity must be >= 0")
	}
	if qty == 0 {
		result, err := db.Exec(`DELETE FROM commerce_cart_items WHERE project_id=? AND cart_id=? AND id=?`, pid, cartID, itemID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return errors.New("cart item not found")
		}
	} else {
		result, err := db.Exec(`UPDATE commerce_cart_items SET quantity=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND cart_id=? AND id=?`, qty, pid, cartID, itemID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return errors.New("cart item not found")
		}
	}
	return recomputeCart(db, pid, cartID)
}

func recomputeCart(db *sql.DB, pid string, cartID int64) error {
	rows, err := db.Query(`SELECT unit_amount_cents, quantity, currency FROM commerce_cart_items WHERE project_id=? AND cart_id=?`, pid, cartID)
	if err != nil {
		return err
	}
	var subtotal int64
	var currency string
	for rows.Next() {
		var amount int64
		var quantity float64
		var lineCurrency string
		if err := rows.Scan(&amount, &quantity, &lineCurrency); err != nil {
			rows.Close()
			return err
		}
		if currency == "" {
			currency = lineCurrency
		} else if currency != lineCurrency {
			rows.Close()
			return errors.New("cart contains mixed currencies")
		}
		subtotal += int64(math.Round(float64(amount) * quantity))
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if currency == "" {
		currency = "USD"
	}
	var metadataText string
	if err := db.QueryRow(
		`SELECT metadata_json FROM commerce_carts WHERE project_id=? AND id=?`,
		pid, cartID).Scan(&metadataText); err != nil {
		return err
	}
	metadata := jsonMap(metadataText)
	delete(metadata, "checkout_quote")
	delete(metadata, "shipping_quote")
	_, err = db.Exec(`UPDATE commerce_carts
		SET subtotal_cents=?, discount_cents=0, tax_cents=0, shipping_cents=0,
		    total_cents=?, currency=?, metadata_json=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`,
		subtotal, subtotal, currency, jsonText(metadata, "{}"), pid, cartID)
	return err
}

func dbCartItems(db *sql.DB, pid string, cartID int64) ([]*CartItem, error) {
	rows, err := db.Query(`SELECT ci.id, ci.cart_id, ci.checkout_item_id, ci.variant_id, ci.listing_id,
		ci.inventory_item_id, ci.catalog_price_id, ci.sku, ci.title_snapshot,
		COALESCE(l.metadata_json, '{}'), ci.unit_amount_cents, ci.currency, ci.quantity,
		ci.created_at, ci.updated_at
		FROM commerce_cart_items ci
		LEFT JOIN commerce_listings l ON l.project_id=ci.project_id AND l.id=ci.listing_id
		WHERE ci.project_id=? AND ci.cart_id=? ORDER BY ci.id`, pid, cartID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CartItem
	for rows.Next() {
		var it CartItem
		var checkoutID, invID, priceID sql.NullInt64
		var listingMetadata string
		if err := rows.Scan(&it.ID, &it.CartID, &checkoutID, &it.VariantID, &it.ListingID, &invID, &priceID,
			&it.SKU, &it.TitleSnapshot, &listingMetadata, &it.UnitAmountCents, &it.Currency, &it.Quantity,
			&it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.CheckoutItemID = ptrIfValid(checkoutID)
		it.InventoryItemID = ptrIfValid(invID)
		it.CatalogPriceID = ptrIfValid(priceID)
		it.ImageURL = strArg(jsonMap(listingMetadata), "image_url")
		out = append(out, &it)
	}
	return out, rows.Err()
}

func dbCheckoutCreate(db *sql.DB, pid string, cart *Cart, checkoutSessionID int64, reservations []int64, discountReservationID string, quote map[string]any) (*CheckoutSession, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(`SELECT id FROM commerce_checkout_sessions WHERE project_id=? AND cart_id=?`, pid, cart.ID).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if id == 0 {
		res, err := tx.Exec(`INSERT INTO commerce_checkout_sessions
			(project_id, store_id, cart_id, checkout_session_id, status, reservation_ids_json, discount_reservation_id, quote_json)
			VALUES (?, ?, ?, ?, 'started', ?, ?, ?)`, pid, cart.StoreID, cart.ID, nullableInt(checkoutSessionID), jsonText(reservations, "[]"), discountReservationID, jsonText(quote, "{}"))
		if err != nil {
			return nil, err
		}
		id, _ = res.LastInsertId()
	} else {
		if _, err := tx.Exec(`UPDATE commerce_checkout_sessions SET checkout_session_id=?, status='started', reservation_ids_json=?,
			invoice_id=NULL, invoice_number='', customer_email='', customer_name='', shipping_address_json='{}', billing_address_json='{}',
			discount_reservation_id=?, quote_json=?, completed_at=NULL, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
			nullableInt(checkoutSessionID), jsonText(reservations, "[]"), discountReservationID, jsonText(quote, "{}"), pid, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM commerce_reservation_links WHERE checkout_id=?`, id); err != nil {
			return nil, err
		}
	}
	for _, reservationID := range reservations {
		if _, err := tx.Exec(`INSERT INTO commerce_reservation_links (checkout_id, reservation_id) VALUES (?, ?)`, id, reservationID); err != nil {
			return nil, err
		}
	}
	result, err := tx.Exec(`UPDATE commerce_carts SET status='checkout', updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=? AND status='open'`, pid, cart.ID)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, errors.New("cart is no longer open")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbCheckoutGet(db, pid, id)
}

func dbCheckoutGet(db *sql.DB, pid string, id int64) (*CheckoutSession, error) {
	row := db.QueryRow(`SELECT id, store_id, cart_id, checkout_session_id, status, reservation_ids_json, invoice_id, invoice_number, customer_email, customer_name,
		shipping_address_json, billing_address_json, discount_reservation_id, quote_json, created_at, updated_at
		FROM commerce_checkout_sessions WHERE project_id=? AND id=?`, pid, id)
	var ch CheckoutSession
	var checkoutID, invoiceID sql.NullInt64
	var reservations, shipping, billing, quote string
	if err := row.Scan(&ch.ID, &ch.StoreID, &ch.CartID, &checkoutID, &ch.Status, &reservations, &invoiceID, &ch.InvoiceNumber, &ch.CustomerEmail, &ch.CustomerName,
		&shipping, &billing, &ch.DiscountReservationID, &quote, &ch.CreatedAt, &ch.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	ch.CheckoutSessionID = ptrIfValid(checkoutID)
	ch.InvoiceID = ptrIfValid(invoiceID)
	if err := json.Unmarshal([]byte(reservations), &ch.ReservationIDs); err != nil {
		return nil, fmt.Errorf("decode reservation ids for checkout %d: %w", ch.ID, err)
	}
	ch.ShippingAddress = jsonMap(shipping)
	ch.BillingAddress = jsonMap(billing)
	ch.Quote = jsonMap(quote)
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
	if existing, err := dbSaleGetByCheckout(db, pid, ch.ID); err != nil || existing != nil {
		return existing, err
	}
	cart, err := dbCartGet(db, pid, ch.CartID, true)
	if err != nil || cart == nil {
		return nil, firstErr(err, errors.New("cart not found"))
	}
	if len(cart.Items) == 0 {
		return nil, errors.New("cart has no items")
	}
	type snapshot struct {
		item    *CartItem
		variant *Variant
		listing *Listing
	}
	snapshots := make([]snapshot, 0, len(cart.Items))
	for _, item := range cart.Items {
		variant, err := dbVariantGet(db, pid, item.VariantID)
		if err != nil || variant == nil {
			return nil, firstErr(err, errors.New("variant missing while snapshotting sale"))
		}
		listing, err := dbListingGet(db, pid, item.ListingID, false)
		if err != nil || listing == nil {
			return nil, firstErr(err, errors.New("listing missing while snapshotting sale"))
		}
		snapshots = append(snapshots, snapshot{item: item, variant: variant, listing: listing})
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO commerce_sales
		(project_id, store_id, cart_id, checkout_id, checkout_session_id, invoice_id, invoice_number, status, payment_status,
		subtotal_cents, discount_cents, tax_cents, shipping_cents, total_cents, currency, customer_email, customer_name,
		shipping_address_json, billing_address_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'awaiting_payment', 'unpaid', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, ch.StoreID, cart.ID, ch.ID, nullableInt(ptrValue(ch.CheckoutSessionID)), nullableInt(ptrValue(ch.InvoiceID)), ch.InvoiceNumber,
		cart.SubtotalCents, cart.DiscountCents, cart.TaxCents, cart.ShippingCents, cart.TotalCents, cart.Currency, ch.CustomerEmail, ch.CustomerName,
		jsonText(ch.ShippingAddress, "{}"), jsonText(ch.BillingAddress, "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	for _, snapshot := range snapshots {
		item, variant, listing := snapshot.item, snapshot.variant, snapshot.listing
		if _, err := tx.Exec(`INSERT INTO commerce_sale_items
			(project_id, sale_id, variant_id, listing_id, inventory_item_id, catalog_product_id, catalog_price_id,
			sku, title_snapshot, unit_amount_cents, currency, quantity, requires_shipping, metadata_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			pid, id, item.VariantID, item.ListingID, nullablePtrInt(item.InventoryItemID), nullablePtrInt(listing.CatalogProductID),
			nullablePtrInt(item.CatalogPriceID), item.SKU, item.TitleSnapshot, item.UnitAmountCents, item.Currency, item.Quantity,
			boolToInt(variant.RequiresShipping), jsonText(map[string]any{"commerce_cart_item_id": item.ID}, "{}")); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbSaleGet(db, pid, id)
}

func dbSaleGet(db *sql.DB, pid string, id int64) (*Sale, error) {
	row := db.QueryRow(saleSelect()+` WHERE project_id=? AND id=?`, pid, id)
	s, err := scanSale(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil || s == nil {
		return s, err
	}
	s.Items, err = dbSaleItems(db, pid, s.ID)
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
	return `SELECT id, slug, name, status, public_base_url, default_currency, default_locale, timezone, order_number_format, checkout_mode, payment_provider, payment_presentation, metadata_json, created_at, updated_at FROM commerce_stores`
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
	return `SELECT id, store_id, checkout_cart_id, session_token, status, subtotal_cents, discount_cents, tax_cents, shipping_cents, total_cents, currency, metadata_json, created_at, updated_at FROM commerce_carts`
}

func saleSelect() string {
	return `SELECT id, store_id, cart_id, checkout_id, checkout_session_id, invoice_id, invoice_number, order_id, status, payment_status, fulfillment_status,
		subtotal_cents, discount_cents, tax_cents, shipping_cents, total_cents, currency, customer_email, customer_name,
		shipping_address_json, billing_address_json, processing_error, created_at, updated_at, COALESCE(paid_at,'') FROM commerce_sales`
}

type scanner interface{ Scan(dest ...any) error }

func scanStore(s scanner) (*Store, error) {
	var st Store
	var meta string
	if err := s.Scan(&st.ID, &st.Slug, &st.Name, &st.Status, &st.PublicBaseURL, &st.DefaultCurrency, &st.DefaultLocale, &st.Timezone, &st.OrderNumberFormat, &st.CheckoutMode, &st.PaymentProvider, &st.PaymentPresentation, &meta, &st.CreatedAt, &st.UpdatedAt); err != nil {
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
	var metadata string
	if err := s.Scan(&c.ID, &c.StoreID, &checkoutID, &c.SessionToken, &c.Status, &c.SubtotalCents, &c.DiscountCents, &c.TaxCents, &c.ShippingCents, &c.TotalCents, &c.Currency, &metadata, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.CheckoutCartID = ptrIfValid(checkoutID)
	c.Metadata = jsonMap(metadata)
	return &c, nil
}

func scanSale(s scanner) (*Sale, error) {
	var sale Sale
	var cartID, checkoutID, checkoutSessionID, invoiceID, orderID sql.NullInt64
	var shipping, billing string
	if err := s.Scan(&sale.ID, &sale.StoreID, &cartID, &checkoutID, &checkoutSessionID, &invoiceID, &sale.InvoiceNumber, &orderID,
		&sale.Status, &sale.PaymentStatus, &sale.FulfillmentStatus, &sale.SubtotalCents, &sale.DiscountCents, &sale.TaxCents,
		&sale.ShippingCents, &sale.TotalCents, &sale.Currency, &sale.CustomerEmail, &sale.CustomerName,
		&shipping, &billing, &sale.ProcessingError, &sale.CreatedAt, &sale.UpdatedAt, &sale.PaidAt); err != nil {
		return nil, err
	}
	sale.CartID = ptrIfValid(cartID)
	sale.CheckoutID = ptrIfValid(checkoutID)
	sale.CheckoutSessionID = ptrIfValid(checkoutSessionID)
	sale.InvoiceID = ptrIfValid(invoiceID)
	sale.OrderID = ptrIfValid(orderID)
	sale.ShippingAddress = jsonMap(shipping)
	sale.BillingAddress = jsonMap(billing)
	return &sale, nil
}

func (a *App) handleSummary(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var out struct {
		Stores   int `json:"stores"`
		Products int `json:"products"`
		Carts    int `json:"open_carts"`
		Sales    int `json:"sales"`
	}
	for _, query := range []struct {
		sql  string
		dest *int
	}{
		{`SELECT COUNT(*) FROM commerce_stores WHERE project_id=? AND archived_at IS NULL`, &out.Stores},
		{`SELECT COUNT(*) FROM commerce_listings WHERE project_id=? AND status!='archived'`, &out.Products},
		{`SELECT COUNT(*) FROM commerce_carts WHERE project_id=? AND status='open'`, &out.Carts},
		{`SELECT COUNT(*) FROM commerce_sales WHERE project_id=?`, &out.Sales},
	} {
		if err := ctx.AppDB().QueryRow(query.sql, pid).Scan(query.dest); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	httpJSON(w, out)
}

func (a *App) handleStores(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := dbStoresList(ctx.AppDB(), pid)
		httpResult(w, out, err)
	case http.MethodPost:
		var body map[string]any
		if err := readJSON(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := dbStoreCreate(ctx.AppDB(), pid, body)
		if err == nil {
			ctx.Emit("commerce.store.created", map[string]any{"store_id": out.ID, "slug": out.Slug})
		}
		httpResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleStore(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/admin/stores/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "store id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := dbStoreGetByID(ctx.AppDB(), pid, id)
		httpResult(w, out, notFoundErr(out, err, "store not found"))
	case http.MethodPatch:
		var patch map[string]any
		if err := readJSON(r, &patch); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := dbStoreUpdate(ctx.AppDB(), pid, id, patch)
		httpResult(w, out, notFoundErr(out, err, "store not found"))
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleStoreSettings(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	storeID, _ := strconv.ParseInt(r.URL.Query().Get("store_id"), 10, 64)
	if storeID == 0 {
		httpErr(w, http.StatusBadRequest, "store_id required")
		return
	}
	args := map[string]any{"_project_id": pid, "store_id": storeID}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolStoreSettingsGet(ctx, args)
		httpResult(w, out, err)
	case http.MethodPatch:
		var body map[string]any
		if err := readJSON(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		args["patch"] = mapArg(body, "patch")
		out, err := a.toolStoreSettingsUpdate(ctx, args)
		httpResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleProducts(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := dbListingsList(ctx.AppDB(), pid, queryArgs(r))
		if err == nil {
			for _, product := range out {
				product.Variants, err = dbVariantsForListing(ctx.AppDB(), pid, product.ID)
				if err != nil {
					break
				}
			}
		}
		httpResult(w, out, err)
	case http.MethodPost:
		var body map[string]any
		if err := readJSON(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := a.toolProductsCreate(ctx, body)
		httpResult(w, resultValue(result, "product"), err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleProduct(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/admin/products/")
	parts := pathParts(r.URL.Path, "/admin/products/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "product id required")
		return
	}
	if len(parts) > 1 && parts[1] == "publish" && r.Method == http.MethodPost {
		result, err := a.toolProductsPublish(ctx, map[string]any{"id": id})
		httpResult(w, resultValue(result, "product"), err)
		return
	}
	if len(parts) > 1 && parts[1] == "archive" && r.Method == http.MethodPost {
		result, err := a.toolProductsArchive(ctx, map[string]any{"id": id})
		httpResult(w, resultValue(result, "product"), err)
		return
	}
	if len(parts) > 1 && parts[1] == "variants" && r.Method == http.MethodPost {
		var body map[string]any
		if err := readJSON(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		body["listing_id"] = id
		result, err := a.toolVariantsCreate(ctx, body)
		httpResult(w, resultValue(result, "variant"), err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := dbListingGet(ctx.AppDB(), pid, id, true)
		httpResult(w, out, notFoundErr(out, err, "product not found"))
	case http.MethodPatch:
		var patch map[string]any
		if err := readJSON(r, &patch); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := dbListingUpdate(ctx.AppDB(), pid, id, patch)
		httpResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleVariant(w http.ResponseWriter, r *http.Request) {
	ctx, _, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method != http.MethodPatch {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := pathInt(r.URL.Path, "/admin/variants/")
	var patch map[string]any
	if err := readJSON(r, &patch); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := a.toolVariantsUpdate(ctx, map[string]any{"id": id, "patch": patch})
	httpResult(w, resultValue(result, "variant"), err)
}

func (a *App) handleCollections(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := dbCollectionsList(ctx.AppDB(), pid, queryArgs(r))
		httpResult(w, out, err)
	case http.MethodPost:
		var body map[string]any
		if err := readJSON(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := a.toolCollectionsCreate(ctx, body)
		httpResult(w, resultValue(result, "collection"), err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleCollection(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/admin/collections/")
	parts := pathParts(r.URL.Path, "/admin/collections/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "collection id required")
		return
	}
	if len(parts) > 1 && parts[1] == "products" && (r.Method == http.MethodPost || r.Method == http.MethodDelete) {
		var body map[string]any
		if err := readJSON(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if r.Method == http.MethodPost {
			err = dbCollectionAddListing(ctx.AppDB(), pid, id, intArg(body, "listing_id"), int(intArg(body, "sort_order")))
		} else {
			err = dbCollectionRemoveListing(ctx.AppDB(), pid, id, intArg(body, "listing_id"))
		}
		httpResult(w, map[string]any{"ok": err == nil}, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := dbCollectionGetWithProducts(ctx.AppDB(), pid, id, "")
		httpResult(w, out, err)
	case http.MethodPatch:
		var patch map[string]any
		if err := readJSON(r, &patch); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := dbCollectionUpdate(ctx.AppDB(), pid, id, patch)
		httpResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleCarts(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	args := queryArgs(r)
	if id := intArg(args, "cart_id"); id != 0 {
		out, err := dbCartGet(ctx.AppDB(), pid, id, true)
		httpResult(w, out, notFoundErr(out, err, "cart not found"))
		return
	}
	out, err := dbCartsList(ctx.AppDB(), pid, args)
	httpResult(w, out, err)
}

func (a *App) handleSales(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	out, err := dbSalesList(ctx.AppDB(), pid, queryArgs(r))
	httpResult(w, out, err)
}

func (a *App) handleSale(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/admin/sales/")
	parts := pathParts(r.URL.Path, "/admin/sales/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "sale id required")
		return
	}
	if len(parts) > 1 && parts[1] == "retry" && r.Method == http.MethodPost {
		sale, loadErr := dbSaleGet(ctx.AppDB(), pid, id)
		if loadErr != nil || sale == nil {
			httpResult(w, sale, notFoundErr(sale, loadErr, "sale not found"))
			return
		}
		out, retryErr := a.completePaidSale(ctx, sale, false)
		httpResult(w, out, retryErr)
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	out, err := dbSaleGet(ctx.AppDB(), pid, id)
	httpResult(w, out, notFoundErr(out, err, "sale not found"))
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
	return map[string]any{"slug": typ("string"), "name": typ("string"), "public_base_url": typ("string"), "default_currency": typ("string"), "default_locale": typ("string"), "timezone": typ("string"), "payment_provider": typ("string"), "payment_presentation": typ("string"), "metadata": typ("object")}
}

func validateStorePayment(provider, presentation string) error {
	if provider != "manual" && provider != "stripe" {
		return errors.New("payment_provider must be 'manual' or 'stripe'")
	}
	if presentation != "elements" && presentation != "hosted" {
		return errors.New("payment_presentation must be 'elements' or 'hosted'")
	}
	return nil
}
func productCreateProps() map[string]any {
	return map[string]any{"store_id": typ("integer"), "store_slug": typ("string"), "title": typ("string"), "handle": typ("string"), "description_html": typ("string"), "vendor": typ("string"), "product_type": typ("string"), "catalog_product_id": typ("integer"), "catalog_price_id": typ("integer"), "price_cents": typ("integer"), "currency": typ("string"), "sku": typ("string"), "inventory_item_id": typ("integer"), "metadata": typ("object")}
}
func listingFilterProps() map[string]any {
	return map[string]any{"store_id": typ("integer"), "status": typ("string"), "q": typ("string"), "limit": typ("integer")}
}
func cartFilterProps() map[string]any {
	return map[string]any{
		"store_id": typ("integer"),
		"status":   typ("string"),
		"updated_before": map[string]any{
			"type": "string", "format": "date-time",
			"description": "Return carts last updated before this RFC3339 timestamp.",
		},
		"updated_after": map[string]any{
			"type": "string", "format": "date-time",
			"description": "Return carts last updated after this RFC3339 timestamp.",
		},
		"inactive_for_minutes": map[string]any{
			"type": "integer", "minimum": 1, "maximum": 525600,
			"description": "Return carts with no activity for at least this many minutes.",
		},
		"has_items": map[string]any{
			"type": "boolean", "description": "Filter by whether the cart contains a positive-quantity item.",
		},
		"abandoned_only": map[string]any{
			"type": "boolean", "description": "Return nonempty open or checkout carts past the inactivity cutoff; defaults to 24 hours.",
		},
		"sort": map[string]any{
			"type": "string", "enum": []string{"updated_desc", "updated_asc"},
		},
		"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
	}
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
func readJSON(r *http.Request, dst any) error {
	return decodeJSON(r, dst)
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
		return n
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
func nullablePtrInt(v *int64) any {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
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
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("com_%d", time.Now().UnixNano())
	}
	return "com_" + hex.EncodeToString(buf)
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
	sdk.Run(&App{})
}
