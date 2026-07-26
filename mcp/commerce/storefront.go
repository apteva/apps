package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	sdk "github.com/apteva/app-sdk"
)

const commerceStorefrontExtensionVersion = "4"

type StorefrontStatus struct {
	StoreID      int64  `json:"store_id"`
	Configured   bool   `json:"configured"`
	ContentReady bool   `json:"content_ready"`
	SiteID       int64  `json:"site_id,omitempty"`
	SiteSlug     string `json:"site_slug,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	ExtensionKey string `json:"extension_key,omitempty"`
	Version      string `json:"version,omitempty"`
	PreviewURL   string `json:"preview_url,omitempty"`
	Error        string `json:"error,omitempty"`
}

func storefrontExtensionKey(storeID int64) string {
	return fmt.Sprintf("commerce-store-%d", storeID)
}

func commerceStorefrontManifest(store *Store) map[string]any {
	fixedStore := map[string]any{"store_id": store.ID}
	activeStore := map[string]any{"store_id": store.ID, "status": "active"}
	shippingAddress := map[string]any{
		"name":         "{{ input.customer_name }}",
		"company":      "{{ input.company }}",
		"line1":        "{{ input.address_line1 }}",
		"line2":        "{{ input.address_line2 }}",
		"city":         "{{ input.city }}",
		"region":       "{{ input.region }}",
		"postal_code":  "{{ input.postal_code }}",
		"country":      "{{ input.country_code }}",
		"country_code": "{{ input.country_code }}",
		"phone":        "{{ input.phone }}",
	}
	billingAddress := map[string]any{
		"name":         "{{ input.billing_name }}",
		"company":      "{{ input.billing_company }}",
		"line1":        "{{ input.billing_address_line1 }}",
		"line2":        "{{ input.billing_address_line2 }}",
		"city":         "{{ input.billing_city }}",
		"region":       "{{ input.billing_region }}",
		"postal_code":  "{{ input.billing_postal_code }}",
		"country":      "{{ input.billing_country_code }}",
		"country_code": "{{ input.billing_country_code }}",
	}
	checkoutInput := []any{
		"email", "customer_name", "company", "address_line1", "address_line2",
		"city", "region", "postal_code", "country_code", "phone",
		"billing_name", "billing_company", "billing_address_line1", "billing_address_line2",
		"billing_city", "billing_region", "billing_postal_code", "billing_country_code",
	}
	return map[string]any{
		"name":    store.Name,
		"version": commerceStorefrontExtensionVersion,
		"settings": map[string]any{
			"announcement": "Free shipping options shown at checkout",
			"accent":       "#176b45",
			"logo_text":    store.Name,
		},
		"settings_schema": []any{
			map[string]any{"key": "announcement", "label": "Announcement", "type": "text", "default": "Free shipping options shown at checkout"},
			map[string]any{"key": "accent", "label": "Accent color", "type": "color", "default": "#176b45"},
			map[string]any{"key": "logo_text", "label": "Store name", "type": "text", "default": store.Name},
		},
		"browser_policy": map[string]any{
			"script_origins":  []any{"https://js.stripe.com", "https://checkout.stripe.com"},
			"frame_origins":   []any{"https://js.stripe.com", "https://hooks.stripe.com", "https://checkout.stripe.com"},
			"connect_origins": []any{"https://api.stripe.com", "https://checkout.stripe.com"},
			"image_origins":   []any{"https://*.stripe.com"},
		},
		"routes": []any{
			map[string]any{"name": "home", "pattern": "/", "template": "home", "data_sources": []any{"products", "collections"}},
			map[string]any{"name": "product", "pattern": "/products/:handle", "template": "product", "data_sources": []any{"product"}},
			map[string]any{"name": "collections", "pattern": "/collections", "template": "collections", "data_sources": []any{"collections"}},
			map[string]any{"name": "collection", "pattern": "/collections/:handle", "template": "collection", "data_sources": []any{"collection"}},
			map[string]any{"name": "search", "pattern": "/search", "template": "search", "data_sources": []any{"search"}},
			map[string]any{"name": "cart", "pattern": "/cart", "template": "cart"},
			map[string]any{"name": "checkout", "pattern": "/checkout", "template": "checkout"},
			map[string]any{"name": "checkout_return", "pattern": "/checkout/return", "template": "checkout_return"},
		},
		"data_sources": map[string]any{
			"products":    map[string]any{"tool": "commerce_products_list", "args": mergeMaps(activeStore, map[string]any{"limit": 24})},
			"product":     map[string]any{"tool": "commerce_products_get", "args": mergeMaps(activeStore, map[string]any{"handle": "{{ route.handle }}"})},
			"collections": map[string]any{"tool": "commerce_collections_list", "args": activeStore},
			"collection":  map[string]any{"tool": "commerce_collections_get", "args": mergeMaps(activeStore, map[string]any{"handle": "{{ route.handle }}"})},
			"search":      map[string]any{"tool": "commerce_products_list", "args": mergeMaps(activeStore, map[string]any{"q": "{{ query.q }}", "limit": 48})},
		},
		"actions": map[string]any{
			"cart": map[string]any{
				"steps": []any{map[string]any{"tool": "commerce_cart_create", "args": mergeMaps(fixedStore, map[string]any{"session_token": "{{ session.token }}"})}},
			},
			"add_to_cart": map[string]any{
				"allowed_input": []any{"variant_id", "quantity"},
				"steps": []any{
					map[string]any{"tool": "commerce_cart_create", "args": mergeMaps(fixedStore, map[string]any{"session_token": "{{ session.token }}"})},
					map[string]any{"tool": "commerce_cart_add_item", "args": map[string]any{"cart_id": "{{ steps.0.cart.id }}", "variant_id": "{{ input.variant_id }}", "quantity": "{{ input.quantity }}"}},
				},
			},
			"set_quantity": map[string]any{
				"allowed_input": []any{"item_id", "quantity"},
				"steps": []any{
					map[string]any{"tool": "commerce_cart_create", "args": mergeMaps(fixedStore, map[string]any{"session_token": "{{ session.token }}"})},
					map[string]any{"tool": "commerce_cart_set_quantity", "args": map[string]any{"cart_id": "{{ steps.0.cart.id }}", "item_id": "{{ input.item_id }}", "quantity": "{{ input.quantity }}"}},
				},
			},
			"checkout_quote": map[string]any{
				"allowed_input": checkoutInput,
				"steps": []any{
					map[string]any{"tool": "commerce_cart_create", "args": mergeMaps(fixedStore, map[string]any{"session_token": "{{ session.token }}"})},
					map[string]any{"tool": "commerce_shipping_quote", "args": map[string]any{
						"cart_id":          "{{ steps.0.cart.id }}",
						"shipping_address": shippingAddress,
						"apply":            true,
					}},
				},
			},
			"checkout_submit": map[string]any{
				"allowed_input":             checkoutInput,
				"rotate_session_on_success": false,
				"steps": []any{
					map[string]any{"tool": "commerce_cart_create", "args": mergeMaps(fixedStore, map[string]any{"session_token": "{{ session.token }}"})},
					map[string]any{"tool": "commerce_shipping_quote", "args": map[string]any{
						"cart_id":          "{{ steps.0.cart.id }}",
						"shipping_address": shippingAddress,
						"apply":            true,
					}},
					map[string]any{"tool": "commerce_checkout_start", "args": map[string]any{"cart_id": "{{ steps.1.cart.id }}"}},
					map[string]any{"tool": "commerce_checkout_update", "args": map[string]any{
						"checkout_id": "{{ steps.2.checkout.id }}",
						"patch": map[string]any{
							"email":            "{{ input.email }}",
							"customer_name":    "{{ input.customer_name }}",
							"shipping_address": shippingAddress,
							"billing_address":  billingAddress,
						},
					}},
					map[string]any{"tool": "commerce_checkout_pay", "args": map[string]any{"checkout_id": "{{ steps.2.checkout.id }}"}},
				},
			},
			"checkout_status": map[string]any{
				"steps": []any{map[string]any{
					"tool": "commerce_checkout_status",
					"args": mergeMaps(fixedStore, map[string]any{"session_token": "{{ session.token }}"}),
				}},
			},
		},
		"templates": map[string]any{
			"home":            storefrontDocument(homeStorefrontBody),
			"product":         storefrontDocument(productStorefrontBody),
			"collections":     storefrontDocument(collectionsStorefrontBody),
			"collection":      storefrontDocument(collectionStorefrontBody),
			"search":          storefrontDocument(searchStorefrontBody),
			"cart":            storefrontDocument(cartStorefrontBody),
			"checkout":        checkoutStorefrontDocument(checkoutStorefrontBody),
			"checkout_return": checkoutStorefrontDocument(checkoutReturnStorefrontBody),
		},
		"assets": map[string]any{
			"store.css": storefrontCSS,
			"store.js":  storefrontJS,
		},
	}
}

func mergeMaps(base map[string]any, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func storefrontDocument(body string) string {
	return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta name="theme-color" content="{{default "#176b45" (get .Settings "accent")}}">
  <title>{{default .SiteTitle (get .Settings "logo_text")}}</title>
  <link rel="stylesheet" href="{{asset "store.css"}}">
  <script defer src="{{asset "store.js"}}"></script>
</head>
<body style="--accent:{{default "#176b45" (get .Settings "accent")}}">
  <div class="announcement">{{default "Free shipping options shown at checkout" (get .Settings "announcement")}}</div>
  <header class="site-header">
    <a class="brand" href="{{href "/"}}">{{default .SiteTitle (get .Settings "logo_text")}}</a>
    <nav aria-label="Main navigation">
      <a href="{{href "/collections"}}">Collections</a>
      <a href="{{href "/search"}}">Search</a>
      <a class="cart-link" href="{{href "/cart"}}">Cart <span data-cart-count></span></a>
    </nav>
  </header>
  <main>` + body + `</main>
  <footer class="site-footer"><span>{{default .SiteTitle (get .Settings "logo_text")}}</span><span>Secure commerce by Apteva</span></footer>
  <div class="toast" data-toast role="status" aria-live="polite"></div>
</body>
</html>`
}

func checkoutStorefrontDocument(body string) string {
	return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta name="theme-color" content="#ffffff">
  <title>Checkout | {{default .SiteTitle (get .Settings "logo_text")}}</title>
  <link rel="stylesheet" href="{{asset "store.css"}}">
  <script src="https://js.stripe.com/clover/stripe.js"></script>
  <script defer src="{{asset "store.js"}}"></script>
</head>
<body class="checkout-page" style="--accent:{{default "#176b45" (get .Settings "accent")}}">
  <header class="checkout-header">
    <a class="checkout-brand" href="{{href "/"}}">{{default .SiteTitle (get .Settings "logo_text")}}</a>
    <span class="secure-label">Secure checkout</span>
  </header>
  <main>` + body + `</main>
  <footer class="checkout-footer"><span>Secure checkout</span><span>Encrypted session and protected order processing</span></footer>
  <div class="toast" data-toast role="status" aria-live="polite"></div>
</body>
</html>`
}

const productCardTemplate = `
{{define "product-card"}}
  {{$product := .}}{{$meta := get . "metadata"}}{{$variants := get . "variants"}}{{$variant := first $variants}}
  <article class="product-card">
    <a class="product-media" href="{{href (printf "/products/%s" (get . "handle"))}}">
      {{with get $meta "image_url"}}<img src="{{.}}" alt="{{get $product "title"}}" loading="lazy" width="720" height="720">{{else}}<span class="media-placeholder">{{get $product "title"}}</span>{{end}}
    </a>
    <div class="product-copy">
      <p class="eyebrow">{{get . "vendor"}}</p>
      <h2><a href="{{href (printf "/products/%s" (get . "handle"))}}">{{get . "title"}}</a></h2>
      {{with $variant}}<p class="price">{{money (get . "price_cents") (get . "currency")}}</p>{{end}}
    </div>
  </article>
{{end}}`

const homeStorefrontBody = productCardTemplate + `
  {{$result := index .Data "products"}}{{$products := get $result "products"}}{{$lead := first $products}}{{$leadMeta := get $lead "metadata"}}{{$leadImage := get $leadMeta "image_url"}}
  <section class="hero{{with $leadImage}} hero-with-image{{end}}">
    {{with $leadImage}}<img class="hero-image" src="{{.}}" alt="{{get $lead "title"}}" width="1800" height="1000">{{end}}
    <div class="hero-copy">
      <p class="eyebrow">New collection</p>
      <h1>Objects made for everyday use.</h1>
      <p>Thoughtful products, clear pricing, and a straightforward checkout.</p>
      <a class="button" href="#products">Shop all products</a>
    </div>
  </section>
  <section class="section" id="products">
    <div class="section-heading"><div><p class="eyebrow">The store</p><h2>Featured products</h2></div><a href="{{href "/search"}}">Browse everything</a></div>
    <div class="product-grid">{{range $products}}{{template "product-card" .}}{{else}}<p class="empty">No published products yet.</p>{{end}}</div>
  </section>`

const productStorefrontBody = `
  {{$result := index .Data "product"}}{{$product := get $result "product"}}{{$meta := get $product "metadata"}}{{$variants := get $product "variants"}}{{$variant := first $variants}}
  <section class="product-detail">
    <div class="detail-media">{{with get $meta "image_url"}}<img src="{{.}}" alt="{{get $product "title"}}" width="1200" height="1200">{{else}}<span class="media-placeholder">{{get $product "title"}}</span>{{end}}</div>
    <div class="detail-copy">
      <p class="eyebrow">{{get $product "vendor"}}</p>
      <h1>{{get $product "title"}}</h1>
      {{with $variant}}<p class="detail-price">{{money (get . "price_cents") (get . "currency")}}</p>{{end}}
      <div class="description">{{safeHTML (text (get $product "description_html"))}}</div>
      {{if $variant}}
      <form data-storefront-action="{{action "add_to_cart"}}" data-success="Added to cart">
        <input type="hidden" name="variant_id" value="{{get $variant "id"}}">
        <label class="quantity">Quantity <input type="number" name="quantity" value="1" min="1" step="1"></label>
        <button class="button wide" type="submit">Add to cart</button>
      </form>
      {{else}}<p class="empty">This product is not currently available.</p>{{end}}
      <ul class="assurances"><li>Secure checkout</li><li>Tracked fulfillment</li><li>Support from the merchant</li></ul>
    </div>
  </section>`

const collectionsStorefrontBody = `
  <section class="page-header"><p class="eyebrow">Browse</p><h1>Collections</h1></section>
  {{$result := index .Data "collections"}}{{$collections := get $result "collections"}}
  <section class="collection-grid">{{range $collections}}<a class="collection-card" href="{{href (printf "/collections/%s" (get . "handle"))}}"><span>{{get . "title"}}</span><small>{{get . "description_html"}}</small></a>{{else}}<p class="empty">No published collections yet.</p>{{end}}</section>`

const collectionStorefrontBody = productCardTemplate + `
  {{$result := index .Data "collection"}}{{$collection := get $result "collection"}}
  <section class="page-header"><p class="eyebrow">Collection</p><h1>{{get $collection "title"}}</h1><div class="lede">{{safeHTML (text (get $collection "description_html"))}}</div></section>
  <section class="section"><div class="product-grid">{{range get $collection "products"}}{{template "product-card" .}}{{else}}<p class="empty">No products in this collection.</p>{{end}}</div></section>`

const searchStorefrontBody = productCardTemplate + `
  <section class="page-header"><p class="eyebrow">Find something</p><h1>Search</h1>
    <form class="search-form" action="{{href "/search"}}" method="get" data-search-form><input type="search" name="q" value="{{get .Query "q"}}" placeholder="Search products" autofocus><button class="button" type="submit">Search</button></form>
  </section>
  {{$result := index .Data "search"}}{{$products := get $result "products"}}
  <section class="section"><div class="product-grid">{{range $products}}{{template "product-card" .}}{{else}}<p class="empty">No matching products.</p>{{end}}</div></section>`

const cartStorefrontBody = `
  <section class="page-header"><p class="eyebrow">Your order</p><h1>Cart</h1></section>
  <section class="cart-shell" data-cart data-cart-action="{{action "cart"}}" data-quantity-action="{{action "set_quantity"}}" data-checkout-url="{{href "/checkout"}}">
    <div class="cart-loading">Loading your cart…</div>
  </section>`

const checkoutStorefrontBody = `
  <div class="checkout-layout">
    <section class="checkout-form-column">
      <a class="checkout-back" href="{{href "/cart"}}">Back to cart</a>
      <div class="checkout-intro">
        <p class="eyebrow">Complete your order</p>
        <h1>Checkout</h1>
      </div>
      <div class="checkout-error" data-checkout-error role="alert" hidden></div>
      <form class="checkout-form" data-checkout-form
        data-storefront-action="{{action "checkout_submit"}}"
        data-quote-action="{{action "checkout_quote"}}"
        data-cart-action="{{action "cart"}}"
        data-cart-url="{{href "/cart"}}">
        <section class="checkout-section">
          <div class="checkout-section-heading"><span class="step-number">1</span><div><h2>Contact</h2><p>Order updates and your receipt will be sent here.</p></div></div>
          <div class="checkout-fields">
            <label class="field full"><span>Email address</span><input type="email" name="email" autocomplete="email" inputmode="email" required></label>
            <label class="field full"><span>Phone <small>Optional</small></span><input type="tel" name="phone" autocomplete="tel" inputmode="tel"></label>
          </div>
        </section>

        <section class="checkout-section">
          <div class="checkout-section-heading"><span class="step-number">2</span><div><h2>Delivery</h2><p>Enter the address where you want your order delivered.</p></div></div>
          <div class="checkout-fields">
            <label class="field full"><span>Country or region</span><select name="country_code" autocomplete="country" data-country-select required><option value="">Select a country or region</option></select></label>
            <label class="field full"><span>Full name</span><input type="text" name="customer_name" autocomplete="name" required></label>
            <label class="field full"><span>Company <small>Optional</small></span><input type="text" name="company" autocomplete="organization"></label>
            <label class="field full"><span>Address</span><input type="text" name="address_line1" autocomplete="address-line1" required></label>
            <label class="field full"><span>Apartment, suite, etc. <small>Optional</small></span><input type="text" name="address_line2" autocomplete="address-line2"></label>
            <label class="field"><span>City</span><input type="text" name="city" autocomplete="address-level2" required></label>
            <label class="field"><span>State or region <small>Where applicable</small></span><input type="text" name="region" autocomplete="address-level1"></label>
            <label class="field full"><span>Postal code</span><input type="text" name="postal_code" autocomplete="postal-code" required></label>
          </div>
        </section>

        <section class="checkout-section shipping-review" data-shipping-review>
          <div class="checkout-section-heading"><span class="step-number">3</span><div><h2>Shipping</h2><p data-shipping-message>Enter your delivery address to review shipping.</p></div></div>
          <div class="shipping-method" data-shipping-method hidden>
            <span><strong>Standard delivery</strong><small>Best available rate for your order</small></span>
            <strong data-shipping-price>Calculated</strong>
          </div>
        </section>

        <section class="checkout-section">
          <div class="checkout-section-heading"><span class="step-number">4</span><div><h2>Payment</h2><p>Payment details are handled by the store's secure billing provider.</p></div></div>
          <div class="payment-method">
            <span class="payment-radio" aria-hidden="true"></span>
            <span><strong>Secure payment</strong><small>Your payment details are encrypted and sent directly to the payment provider.</small></span>
          </div>
          <label class="billing-toggle"><input type="checkbox" data-billing-same checked><span>Use delivery address as billing address</span></label>
          <div class="billing-fields" data-billing-fields hidden>
            <div class="checkout-fields">
              <label class="field full"><span>Country or region</span><select name="billing_country_code" autocomplete="billing country" data-country-select disabled required><option value="">Select a country or region</option></select></label>
              <label class="field full"><span>Full name</span><input type="text" name="billing_name" autocomplete="billing name" disabled required></label>
              <label class="field full"><span>Company <small>Optional</small></span><input type="text" name="billing_company" autocomplete="billing organization" disabled></label>
              <label class="field full"><span>Address</span><input type="text" name="billing_address_line1" autocomplete="billing address-line1" disabled required></label>
              <label class="field full"><span>Apartment, suite, etc. <small>Optional</small></span><input type="text" name="billing_address_line2" autocomplete="billing address-line2" disabled></label>
              <label class="field"><span>City</span><input type="text" name="billing_city" autocomplete="billing address-level2" disabled required></label>
              <label class="field"><span>State or region <small>Where applicable</small></span><input type="text" name="billing_region" autocomplete="billing address-level1" disabled></label>
              <label class="field full"><span>Postal code</span><input type="text" name="billing_postal_code" autocomplete="billing postal-code" disabled required></label>
            </div>
          </div>
          <div class="payment-element-shell" data-payment-stage hidden>
            <div id="payment-element"></div>
            <div class="payment-element-error" data-payment-error role="alert" hidden></div>
            <button class="button checkout-submit" type="button" data-payment-submit disabled><span>Pay now</span><span aria-hidden="true">-&gt;</span></button>
          </div>
        </section>

        <button class="button checkout-submit" type="submit" data-order-submit><span data-submit-label>Review order</span><span aria-hidden="true">-&gt;</span></button>
        <p class="checkout-note">Inventory and totals are verified before the order is placed. Your cart is reserved when checkout begins.</p>
      </form>
      <div class="checkout-result" data-checkout-result hidden></div>
    </section>

    <aside class="checkout-summary-column">
      <details class="order-summary" data-order-summary open>
        <summary><span>Order summary</span><strong data-summary-total>--</strong></summary>
        <div class="order-summary-body" data-checkout-summary>
          <div class="summary-loading">Loading order summary...</div>
        </div>
      </details>
    </aside>
  </div>`

const checkoutReturnStorefrontBody = `
  <section class="checkout-return" data-payment-return data-status-action="{{action "checkout_status"}}">
    <span class="status-pill" data-return-pill>Payment processing</span>
    <h1 data-return-title>Confirming your order</h1>
    <p data-return-message>Payment confirmation can take a moment. You can safely keep this page open.</p>
    <a class="button" href="{{href "/"}}" data-return-link hidden>Continue shopping</a>
  </section>`

const storefrontCSS = `
*{box-sizing:border-box}html{color:#171a17;background:#fff;font-family:Inter,ui-sans-serif,system-ui,-apple-system,sans-serif;letter-spacing:0}body{margin:0}a{color:inherit;text-decoration:none}img{display:block;max-width:100%;height:auto}.announcement{background:#171a17;color:#fff;padding:9px 20px;text-align:center;font-size:12px}.site-header{height:72px;padding:0 clamp(20px,5vw,72px);display:flex;align-items:center;justify-content:space-between;border-bottom:1px solid #e7e9e6;position:sticky;top:0;background:rgba(255,255,255,.96);z-index:20}.brand{font-family:Georgia,serif;font-size:23px;font-weight:700}.site-header nav{display:flex;gap:24px;align-items:center;font-size:14px}.site-header nav a:hover{color:var(--accent)}main{min-height:70vh}.hero{min-height:min(70vh,680px);padding:clamp(64px,9vw,128px) clamp(20px,8vw,120px);display:flex;align-items:center;position:relative;overflow:hidden;background:#edf2ec;border-bottom:1px solid #dce3dc}.hero-image{position:absolute;inset:0;width:100%;height:100%;object-fit:cover}.hero-with-image:after{content:"";position:absolute;inset:0;background:rgba(17,20,17,.58)}.hero-copy{position:relative;z-index:1;max-width:900px}.hero h1{font-family:Georgia,serif;font-size:clamp(48px,7vw,96px);line-height:.98;margin:12px 0 24px}.hero-copy>p:not(.eyebrow){font-size:18px;line-height:1.6;max-width:560px;margin:0 0 30px;color:#4d554e}.hero-with-image .hero-copy,.hero-with-image .hero-copy>p,.hero-with-image .eyebrow{color:#fff}.eyebrow{text-transform:uppercase;font-size:11px;letter-spacing:1.6px;font-weight:700;color:var(--accent);margin:0 0 8px}.button{display:inline-flex;min-height:44px;padding:0 20px;border:0;background:var(--accent);color:#fff;align-items:center;justify-content:center;font:600 14px inherit;cursor:pointer;width:max-content}.button:hover{filter:brightness(.92)}.button.wide{width:100%}.section{padding:72px clamp(20px,5vw,72px)}.section-heading{display:flex;justify-content:space-between;align-items:end;margin-bottom:30px}.section-heading h2,.page-header h1{font-family:Georgia,serif;font-size:clamp(36px,5vw,62px);margin:0}.section-heading>a{font-size:13px;border-bottom:1px solid}.product-grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:28px 18px}.product-media{display:block;aspect-ratio:1;background:#f2f3f1;overflow:hidden}.product-media img,.detail-media img{width:100%;height:100%;object-fit:cover;transition:transform .35s}.product-card:hover img{transform:scale(1.02)}.media-placeholder{display:flex;width:100%;height:100%;align-items:center;justify-content:center;padding:30px;color:#778078;text-align:center}.product-copy{padding-top:14px}.product-copy h2{font-size:15px;margin:3px 0 8px;font-weight:600}.price{font-size:14px;margin:0;color:#505650}.page-header{padding:72px clamp(20px,8vw,120px) 42px;border-bottom:1px solid #e7e9e6}.lede{max-width:700px;color:#525a53;line-height:1.7}.collection-grid{padding:36px clamp(20px,5vw,72px) 80px;display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px}.collection-card{min-height:180px;padding:28px;background:#edf2ec;display:flex;flex-direction:column;justify-content:end}.collection-card span{font:600 28px Georgia,serif}.collection-card small{margin-top:8px;color:#5c655e}.product-detail{display:grid;grid-template-columns:minmax(0,1.25fr) minmax(340px,.75fr);min-height:calc(100vh - 105px)}.detail-media{background:#f2f3f1;min-height:620px}.detail-copy{padding:clamp(46px,7vw,100px);align-self:center}.detail-copy h1{font:500 clamp(42px,5vw,70px)/1 Georgia,serif;margin:10px 0 20px}.detail-price{font-size:20px;font-weight:650}.description{line-height:1.7;color:#505750;margin:28px 0}.quantity{display:grid;gap:8px;font-size:12px;margin-bottom:12px}.quantity input{height:44px;border:1px solid #cfd4cf;padding:0 12px;width:100%}.assurances{list-style:none;padding:22px 0 0;margin:22px 0 0;border-top:1px solid #e1e4e1;display:grid;gap:8px;font-size:12px;color:#606860}.search-form{display:flex;gap:10px;max-width:680px;margin-top:28px}.search-form input{height:48px;flex:1;border:1px solid #cbd0cb;padding:0 15px;font-size:16px}.cart-shell{padding:0 clamp(20px,8vw,120px) 90px;max-width:1100px}.cart-row{display:grid;grid-template-columns:minmax(0,1fr) 90px 132px;gap:20px;padding:20px 0;border-bottom:1px solid #e3e6e3;align-items:center}.cart-row input{width:72px;height:40px;border:1px solid #ccd1cc;padding:8px;justify-self:end}.line-total{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}.cart-total{display:flex;justify-content:space-between;font-size:20px;font-weight:700;padding:28px 0}.cart-total span:last-child{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}.empty,.cart-loading{padding:40px 0;color:#667067}.site-footer{border-top:1px solid #e1e4e1;padding:34px clamp(20px,5vw,72px);display:flex;justify-content:space-between;color:#606760;font-size:12px}.toast{position:fixed;right:20px;bottom:20px;background:#171a17;color:#fff;padding:13px 18px;opacity:0;transform:translateY(12px);transition:.2s;pointer-events:none;z-index:50}.toast.visible{opacity:1;transform:none}
.checkout-page{background:#fff;color:#202420}.checkout-page main{min-height:0}.checkout-header{height:76px;border-bottom:1px solid #dfe3df;display:flex;align-items:center;justify-content:space-between;padding:0 clamp(24px,5vw,72px)}.checkout-brand{font:700 24px Georgia,serif}.secure-label{color:#5e665f;font-size:13px}.checkout-layout{display:grid;grid-template-columns:minmax(0,700px) minmax(380px,1fr);max-width:1320px;margin:0 auto;min-height:calc(100vh - 132px)}.checkout-form-column{padding:42px clamp(28px,5vw,72px) 72px}.checkout-back{display:inline-block;color:#59615a;font-size:13px;margin-bottom:34px;border-bottom:1px solid #aeb5af}.checkout-intro{margin-bottom:38px}.checkout-intro h1{font:600 42px/1.05 Georgia,serif;margin:8px 0 0}.checkout-error{border-left:3px solid #a83b2f;background:#fbf3f1;color:#792b23;padding:14px 16px;margin:0 0 24px;font-size:14px;line-height:1.5}.checkout-form{display:grid}.checkout-section{padding:0 0 34px;margin:0 0 34px;border-bottom:1px solid #dfe3df}.checkout-section-heading{display:grid;grid-template-columns:30px minmax(0,1fr);gap:12px;align-items:start;margin-bottom:22px}.step-number{width:28px;height:28px;border:1px solid #aeb5af;border-radius:50%;display:grid;place-items:center;font-size:12px;font-weight:700}.checkout-section-heading h2{font-size:19px;margin:2px 0 4px}.checkout-section-heading p{color:#677068;font-size:13px;line-height:1.5;margin:0}.checkout-fields{display:grid;grid-template-columns:1fr 1fr;gap:14px}.field{display:grid;gap:7px;color:#454c46;font-size:12px;font-weight:600}.field.full{grid-column:1/-1}.field small{color:#7a827b;font-weight:400}.field input,.field select{width:100%;height:50px;border:1px solid #bac1bb;border-radius:4px;background:#fff;color:#202420;padding:0 13px;font-family:inherit;font-size:15px;font-weight:400}.field select{appearance:auto}.field input:hover,.field select:hover{border-color:#8f9991}.field input:focus,.field select:focus{outline:2px solid var(--accent);outline-offset:1px;border-color:var(--accent)}.field input:user-invalid,.field select:user-invalid{border-color:#a83b2f}.shipping-method,.payment-method{min-height:68px;border:1px solid #aeb5af;border-radius:6px;padding:14px 16px;display:flex;align-items:center;justify-content:space-between;gap:18px}.shipping-method>span,.payment-method>span:last-child{display:grid;gap:4px}.shipping-method small,.payment-method small{color:#677068;font-size:12px;line-height:1.4;font-weight:400}.shipping-method>strong{font-variant-numeric:tabular-nums;white-space:nowrap}.payment-method{justify-content:flex-start;border-color:var(--accent);background:#f5f9f6}.payment-radio{width:16px;height:16px;border:5px solid var(--accent);border-radius:50%;flex:0 0 auto}.billing-toggle{display:flex;align-items:center;gap:10px;font-size:13px;margin-top:16px;cursor:pointer}.billing-toggle input{width:17px;height:17px;accent-color:var(--accent)}.billing-fields{padding-top:20px}.payment-element-shell{margin-top:22px;padding-top:22px;border-top:1px solid #dfe3df}.payment-element-shell .checkout-submit{margin-top:22px}.payment-element-error{color:#792b23;font-size:13px;line-height:1.5;margin-top:12px}.checkout-submit{width:100%;min-height:54px;justify-content:space-between;padding:0 20px;font-size:15px}.checkout-submit:disabled{cursor:wait;opacity:.7}.checkout-note{margin:14px 0 0;color:#69716a;font-size:12px;line-height:1.55;text-align:center}.checkout-result{padding:36px 0;border-top:1px solid #dfe3df}.checkout-result h2{font:600 36px/1.1 Georgia,serif;margin:8px 0 14px}.checkout-result p{color:#535c54;line-height:1.65}.checkout-result .confirmation-number{color:#202420;font-size:14px;font-weight:700}.status-pill{display:inline-flex;background:#edf2ec;color:#345443;padding:6px 9px;border-radius:4px;text-transform:uppercase;font-size:10px;font-weight:800;letter-spacing:1px}.checkout-return{max-width:620px;margin:0 auto;padding:clamp(72px,12vw,150px) 24px;min-height:calc(100vh - 132px);text-align:center}.checkout-return h1{font:600 42px/1.1 Georgia,serif;margin:18px 0 14px}.checkout-return p{color:#535c54;line-height:1.65;margin:0 auto 28px;max-width:520px}.checkout-return .button{margin:0 auto}.checkout-summary-column{background:#f5f6f4;border-left:1px solid #dfe3df;padding:62px clamp(28px,4vw,56px)}.order-summary{position:sticky;top:28px}.order-summary summary{list-style:none;display:flex;align-items:center;justify-content:space-between;font-size:15px;font-weight:700;padding-bottom:24px;cursor:default}.order-summary summary::-webkit-details-marker{display:none}.order-summary summary strong{font-size:18px;font-variant-numeric:tabular-nums}.summary-items{display:grid;gap:18px;padding:0 0 24px;border-bottom:1px solid #d7dbd7}.summary-item{display:grid;grid-template-columns:64px minmax(0,1fr) auto;gap:14px;align-items:center}.summary-media{width:64px;height:64px;border:1px solid #d9ddd9;border-radius:6px;background:#fff;position:relative;display:grid;place-items:center;overflow:visible;color:#777f78;font:600 11px Georgia,serif;text-align:center;padding:5px}.summary-media img{width:100%;height:100%;object-fit:cover;border-radius:5px}.summary-quantity{position:absolute;right:-7px;top:-8px;min-width:22px;height:22px;border-radius:50%;background:#646c65;color:#fff;display:grid;place-items:center;padding:0 5px;font-size:11px}.summary-copy{min-width:0}.summary-copy strong{display:block;font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.summary-copy small{display:block;color:#747c75;font-size:11px;margin-top:4px}.summary-price{justify-self:end;text-align:right;font-size:13px;font-variant-numeric:tabular-nums;white-space:nowrap}.summary-totals{display:grid;grid-template-columns:1fr auto;gap:10px 24px;padding:24px 0;font-size:13px}.summary-totals span:nth-child(even){justify-self:end;text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}.summary-total-label,.summary-total-value{border-top:1px solid #d7dbd7;padding-top:18px;margin-top:8px;font-size:17px;font-weight:750}.summary-total-value{font-size:21px}.summary-loading{color:#69716a;font-size:13px;padding:12px 0}.checkout-footer{min-height:56px;border-top:1px solid #dfe3df;display:flex;justify-content:space-between;align-items:center;gap:20px;padding:16px clamp(24px,5vw,72px);color:#69716a;font-size:11px}
@media(max-width:900px){.product-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.product-detail{grid-template-columns:1fr}.detail-media{min-height:auto;aspect-ratio:1}.collection-grid{grid-template-columns:1fr 1fr}.site-header nav a:not(.cart-link){display:none}.checkout-layout{grid-template-columns:1fr}.checkout-summary-column{grid-row:1;border-left:0;border-bottom:1px solid #dfe3df;padding:20px clamp(24px,6vw,56px)}.checkout-form-column{grid-row:2;padding-top:36px}.order-summary{position:static}.order-summary summary{cursor:pointer;padding:4px 0}.order-summary[open] summary{padding-bottom:22px}.order-summary summary:after{content:"+";margin-left:12px;color:#687069}.order-summary[open] summary:after{content:"-"}.order-summary summary strong{margin-left:auto}.checkout-header{height:68px}}
@media(max-width:560px){.hero{min-height:590px}.hero h1{font-size:52px}.product-grid{gap:24px 10px}.section{padding:52px 16px}.site-header{padding:0 16px}.collection-grid{grid-template-columns:1fr;padding:24px 16px 60px}.detail-copy{padding:40px 20px}.page-header{padding:52px 20px 30px}.cart-shell{padding:0 20px 60px}.cart-row{grid-template-columns:1fr 72px}.cart-row .line-total{grid-column:1/-1}.site-footer{padding:28px 20px;gap:20px;flex-direction:column}.checkout-header{padding:0 20px}.checkout-brand{font-size:21px}.secure-label{font-size:11px}.checkout-summary-column{padding:16px 20px}.checkout-form-column{padding:30px 20px 56px}.checkout-back{margin-bottom:26px}.checkout-intro{margin-bottom:32px}.checkout-intro h1{font-size:36px}.checkout-fields{grid-template-columns:1fr}.field.full{grid-column:auto}.checkout-section{padding-bottom:28px;margin-bottom:28px}.checkout-footer{align-items:flex-start;flex-direction:column;padding:20px}.summary-item{grid-template-columns:56px minmax(0,1fr) auto}.summary-media{width:56px;height:56px}}
`

const storefrontJS = `
const qs=(s,r=document)=>r.querySelector(s),qsa=(s,r=document)=>[...r.querySelectorAll(s)];
const esc=value=>String(value??'').replace(/[&<>"']/g,char=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
const toast=(message)=>{const el=qs('[data-toast]');if(!el)return;el.textContent=message;el.classList.add('visible');setTimeout(()=>el.classList.remove('visible'),2200)};
async function call(url,input={}){const response=await fetch(url,{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Requested-With':'storefront'},body:JSON.stringify(input)});const body=await response.json().catch(()=>({}));if(!response.ok)throw new Error(body.error||'Request failed');return body}
function cartFrom(body){return body?.result?.cart||body?.result||body?.steps?.at(-1)?.cart||null}
function updateCount(cart){qsa('[data-cart-count]').forEach(el=>el.textContent=cart?.items?.length?'('+cart.items.length+')':'')}
function cartTitle(item){const title=String(item?.title_snapshot??'');const parts=title.split(' - ');return parts.length===2&&parts[0]===parts[1]?parts[0]:title}
const money=(cents,currency)=>new Intl.NumberFormat(undefined,{style:'currency',currency:currency||'USD'}).format((cents||0)/100);
function imageURL(value){try{const url=new URL(String(value||''),location.href);return ['http:','https:'].includes(url.protocol)?url.href:''}catch{return''}}
const searchForm=qs('[data-search-form]');if(searchForm)searchForm.addEventListener('submit',event=>{event.preventDefault();const target=new URL(searchForm.action);target.searchParams.set('q',qs('input[name=q]',searchForm).value);location.assign(target)});
qsa('form[data-storefront-action]:not([data-checkout-form])').forEach(form=>form.addEventListener('submit',async event=>{event.preventDefault();const button=qs('button[type=submit]',form);if(button)button.disabled=true;try{const input=Object.fromEntries(new FormData(form));qsa('input[type=number]',form).forEach(field=>input[field.name]=Number(field.value));const body=await call(form.dataset.storefrontAction,input);updateCount(cartFrom(body));toast(form.dataset.success||'Updated')}catch(error){toast(error.message)}finally{if(button)button.disabled=false}}));
const cartRoot=qs('[data-cart]');if(cartRoot){const render=cart=>{updateCount(cart);if(!cart?.items?.length){cartRoot.innerHTML='<p class="empty">Your cart is empty.</p>';return}cartRoot.innerHTML=cart.items.map(item=>'<div class="cart-row"><div><strong>'+esc(cartTitle(item))+'</strong><br><small>'+esc(item.sku)+'</small></div><input aria-label="Quantity" data-item="'+Number(item.id)+'" type="number" min="0" step="1" value="'+Number(item.quantity)+'"><span class="line-total">'+esc(money(item.unit_amount_cents*item.quantity,item.currency))+'</span></div>').join('')+'<div class="cart-total"><span>Total</span><span>'+esc(money(cart.total_cents,cart.currency))+'</span></div><a class="button wide" href="'+esc(cartRoot.dataset.checkoutUrl)+'">Continue to checkout</a>';qsa('[data-item]',cartRoot).forEach(input=>input.addEventListener('change',async()=>{try{const body=await call(cartRoot.dataset.quantityAction,{item_id:Number(input.dataset.item),quantity:Number(input.value)});render(cartFrom(body))}catch(error){toast(error.message)}}))};call(cartRoot.dataset.cartAction).then(body=>render(cartFrom(body))).catch(error=>{cartRoot.textContent=error.message})}

const checkoutForm=qs('[data-checkout-form]');
if(checkoutForm){
  const countryCodes='AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW'.split(' ');
  const names=typeof Intl.DisplayNames==='function'?new Intl.DisplayNames([navigator.language||'en'],{type:'region'}):null;
  const countries=countryCodes.map(code=>({code,label:names?.of(code)||code})).sort((a,b)=>a.label.localeCompare(b.label));
  qsa('[data-country-select]',checkoutForm).forEach(select=>{countries.forEach(country=>{const option=document.createElement('option');option.value=country.code;option.textContent=country.label;select.append(option)})});
  const billingSame=qs('[data-billing-same]',checkoutForm);
  const billingFields=qs('[data-billing-fields]',checkoutForm);
  const checkoutError=qs('[data-checkout-error]');
  const checkoutResult=qs('[data-checkout-result]');
  const summaryRoot=qs('[data-checkout-summary]');
  const summaryTotal=qs('[data-summary-total]');
  const submitButton=qs('button[type=submit]',checkoutForm);
  const submitLabel=qs('[data-submit-label]',submitButton);
  const shippingMethod=qs('[data-shipping-method]');
  const shippingMessage=qs('[data-shipping-message]');
  const shippingPrice=qs('[data-shipping-price]');
  const paymentStage=qs('[data-payment-stage]');
  const paymentButton=qs('[data-payment-submit]');
  const paymentError=qs('[data-payment-error]');
  const orderButton=qs('[data-order-submit]');
  let latestCart=null;
  let quoteReady=false;
  let stripeActions=null;

  const shippingNames=['customer_name','company','address_line1','address_line2','city','region','postal_code','country_code','phone'];
  const billingMap={billing_name:'customer_name',billing_company:'company',billing_address_line1:'address_line1',billing_address_line2:'address_line2',billing_city:'city',billing_region:'region',billing_postal_code:'postal_code',billing_country_code:'country_code'};
  function checkoutInput(){
    const input=Object.fromEntries(new FormData(checkoutForm));
    if(billingSame.checked){Object.entries(billingMap).forEach(([billing,shipping])=>{input[billing]=input[shipping]||''})}
    input.country_code=String(input.country_code||'').toUpperCase();
    input.billing_country_code=String(input.billing_country_code||'').toUpperCase();
    delete input.billing_same;
    return input;
  }
  function setBillingMode(){
    const same=billingSame.checked;
    billingFields.hidden=same;
    qsa('input,select',billingFields).forEach(field=>field.disabled=same);
  }
  function setBusy(busy,label){
    submitButton.disabled=busy;
    checkoutForm.setAttribute('aria-busy',String(busy));
    submitLabel.textContent=busy?label:(quoteReady?'Place order':'Review order');
  }
  function showError(message){
    checkoutError.textContent=message==='storefront action unavailable'?'We could not update checkout. Review your details and try again.':message;
    checkoutError.hidden=false;
    checkoutError.scrollIntoView({behavior:'smooth',block:'center'});
  }
  async function mountStripePayment(payment){
    if(typeof window.Stripe!=='function')throw new Error('The secure payment form could not be loaded');
    if(!payment?.publishable_key||!payment?.client_secret)throw new Error('Secure payment configuration is unavailable');
    const stripe=window.Stripe(payment.publishable_key);
    const checkout=stripe.initCheckout({
      clientSecret:payment.client_secret,
      elementsOptions:{appearance:{theme:'stripe',variables:{colorPrimary:getComputedStyle(document.body).getPropertyValue('--accent').trim()||'#176b45',borderRadius:'4px'}}}
    });
    checkout.on('change',session=>{paymentButton.disabled=!session.canConfirm});
    const loaded=await checkout.loadActions();
    if(loaded.type!=='success')throw new Error(loaded.error?.message||'The secure payment form could not be initialized');
    stripeActions=loaded.actions;
    checkout.createPaymentElement().mount('#payment-element');
    paymentStage.hidden=false;
    orderButton.hidden=true;
    paymentButton.disabled=!loaded.actions.getSession().canConfirm;
    paymentStage.scrollIntoView({behavior:'smooth',block:'center'});
  }
  function renderSummary(cart,quoted){
    latestCart=cart;
    if(!cart?.items?.length){
      summaryRoot.innerHTML='<p class="empty">Your cart is empty. <a href="'+esc(checkoutForm.dataset.cartUrl)+'">Return to cart</a>.</p>';
      summaryTotal.textContent=money(0,cart?.currency||'USD');
      submitButton.disabled=true;
      return;
    }
    const items=cart.items.map(item=>{
      const image=imageURL(item.image_url);
      const media=image?'<img src="'+esc(image)+'" alt="">':esc(cartTitle(item).slice(0,2).toUpperCase());
      return '<div class="summary-item"><div class="summary-media">'+media+'<span class="summary-quantity">'+esc(item.quantity)+'</span></div><div class="summary-copy"><strong>'+esc(cartTitle(item))+'</strong><small>'+esc(item.sku)+'</small></div><span class="summary-price">'+esc(money(item.unit_amount_cents*item.quantity,item.currency))+'</span></div>';
    }).join('');
    const baseTotal=cart.subtotal_cents-cart.discount_cents+cart.tax_cents;
    const visibleTotal=quoted?cart.total_cents:baseTotal;
    const shipping=quoted?(cart.shipping_cents?money(cart.shipping_cents,cart.currency):'Free'):'Calculated after address';
    const discount=cart.discount_cents?'<span>Discount</span><span>-'+esc(money(cart.discount_cents,cart.currency))+'</span>':'';
    const tax=cart.tax_cents?'<span>Tax</span><span>'+esc(money(cart.tax_cents,cart.currency))+'</span>':'';
    summaryRoot.innerHTML='<div class="summary-items">'+items+'</div><div class="summary-totals"><span>Subtotal</span><span>'+esc(money(cart.subtotal_cents,cart.currency))+'</span>'+discount+'<span>Shipping</span><span>'+esc(shipping)+'</span>'+tax+'<span class="summary-total-label">Total</span><span class="summary-total-value">'+esc(money(visibleTotal,cart.currency))+'</span></div>';
    summaryTotal.textContent=money(visibleTotal,cart.currency);
    shippingMethod.hidden=!quoted;
    shippingMessage.textContent=quoted?'Shipping is confirmed for this address.':'Enter your delivery address to review shipping.';
    shippingPrice.textContent=quoted?(cart.shipping_cents?money(cart.shipping_cents,cart.currency):'Free'):'Calculated';
  }
  setBillingMode();
  billingSame.addEventListener('change',setBillingMode);
  checkoutForm.addEventListener('input',event=>{
    if(quoteReady&&shippingNames.includes(event.target.name)){
      quoteReady=false;
      renderSummary(latestCart,false);
      submitLabel.textContent='Review order';
    }
    checkoutError.hidden=true;
  });
  checkoutForm.addEventListener('submit',async event=>{
    event.preventDefault();
    checkoutError.hidden=true;
    if(!checkoutForm.reportValidity())return;
    const input=checkoutInput();
    if(!quoteReady){
      setBusy(true,'Reviewing order...');
      try{
        const body=await call(checkoutForm.dataset.quoteAction,input);
        const cart=cartFrom(body);
        if(!cart)throw new Error('Order totals are unavailable');
        quoteReady=true;
        renderSummary(cart,true);
        submitLabel.textContent='Place order';
        shippingMethod.scrollIntoView({behavior:'smooth',block:'center'});
      }catch(error){showError(error.message)}
      finally{setBusy(false,'')}
      return;
    }
    setBusy(true,'Placing order...');
    try{
      const body=await call(checkoutForm.dataset.storefrontAction,input);
      const confirmedCart=body?.steps?.[1]?.cart;
      if(confirmedCart)renderSummary(confirmedCart,true);
      const sale=body?.result?.sale;
      if(!sale)throw new Error('Order confirmation is unavailable');
      checkoutError.hidden=true;
      const payment=body?.result?.payment||{};
      if(payment.presentation==='hosted'){
        if(!payment.url)throw new Error('The hosted payment page is unavailable');
        location.assign(payment.url);
        return;
      }
      if(payment.presentation==='elements'){
        await mountStripePayment(payment);
        return;
      }
      checkoutForm.hidden=true;
      const paid=sale.payment_status==='paid';
      checkoutResult.hidden=false;
      checkoutResult.innerHTML='<span class="status-pill">'+(paid?'Paid':'Payment pending')+'</span><h2>Order received</h2>'+(sale.invoice_number?'<p class="confirmation-number">Invoice '+esc(sale.invoice_number)+'</p>':'')+'<p>'+(paid?'Payment is confirmed and your order is being prepared.':'Your order is reserved. Follow the invoice payment instructions from the merchant to complete payment.')+'</p>';
      checkoutResult.scrollIntoView({behavior:'smooth',block:'start'});
    }catch(error){showError(error.message)}
    finally{setBusy(false,'')}
  });
  paymentButton.addEventListener('click',async()=>{
    if(!stripeActions)return;
    paymentError.hidden=true;
    paymentButton.disabled=true;
    try{
      const result=await stripeActions.confirm();
      if(result?.type==='error')throw new Error(result.error?.message||'Payment could not be confirmed');
      paymentStage.hidden=true;
      checkoutForm.hidden=true;
      checkoutResult.hidden=false;
      checkoutResult.innerHTML='<span class="status-pill">Payment processing</span><h2>Confirming your order</h2><p>Payment was submitted. Confirmation can take a moment.</p>';
      checkoutResult.scrollIntoView({behavior:'smooth',block:'start'});
    }catch(error){
      paymentError.textContent=error.message;
      paymentError.hidden=false;
      paymentButton.disabled=false;
    }
  });
  call(checkoutForm.dataset.cartAction).then(body=>renderSummary(cartFrom(body),false)).catch(error=>showError(error.message));
  const orderSummary=qs('[data-order-summary]');
  const mobile=matchMedia('(max-width:900px)');
  const syncSummary=()=>{if(mobile.matches)orderSummary.removeAttribute('open');else orderSummary.setAttribute('open','')};
  syncSummary();
  mobile.addEventListener?.('change',syncSummary);
}

const paymentReturn=qs('[data-payment-return]');
if(paymentReturn){
  const pill=qs('[data-return-pill]',paymentReturn);
  const title=qs('[data-return-title]',paymentReturn);
  const message=qs('[data-return-message]',paymentReturn);
  const link=qs('[data-return-link]',paymentReturn);
  let attempts=0;
  const refresh=async()=>{
    attempts++;
    try{
      const body=await call(paymentReturn.dataset.statusAction);
      const status=body?.result||{};
      if(status.payment_status==='paid'){
        pill.textContent='Paid';
        title.textContent='Order confirmed';
        message.textContent=status.invoice_number?'Payment is confirmed for invoice '+status.invoice_number+'. Your order is being prepared.':'Payment is confirmed and your order is being prepared.';
        link.hidden=false;
        return;
      }
      if(status.status==='not_found'){
        pill.textContent='Session unavailable';
        title.textContent='We could not find this checkout';
        message.textContent='Return to the store and check your order again.';
        link.hidden=false;
        return;
      }
      if(attempts<20)setTimeout(refresh,1500);
      else{
        title.textContent='Payment is still processing';
        message.textContent='Your order remains reserved. Payment confirmation may arrive later.';
        link.hidden=false;
      }
    }catch{
      if(attempts<8)setTimeout(refresh,2000);
      else link.hidden=false;
    }
  };
  refresh();
}
`

func (a *App) configureContentStorefront(ctx *sdk.AppCtx, pid string, store *Store, requestedSiteID int64) (*StorefrontStatus, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	siteID := requestedSiteID
	var siteResult map[string]any
	if siteID != 0 {
		if err := ctx.PlatformAPI().CallAppResult("content", "sites_get", map[string]any{
			"_project_id": pid, "id": siteID,
		}, &siteResult); err != nil {
			return nil, fmt.Errorf("get Content site: %w", err)
		}
	} else {
		err := ctx.PlatformAPI().CallAppResult("content", "sites_get", map[string]any{
			"_project_id": pid, "slug": store.Slug,
		}, &siteResult)
		if err != nil || intArg(unwrap(siteResult, "site"), "id") == 0 {
			siteResult = map[string]any{}
			if err := ctx.PlatformAPI().CallAppResult("content", "sites_create", map[string]any{
				"_project_id": pid, "slug": store.Slug, "name": store.Name,
			}, &siteResult); err != nil {
				return nil, fmt.Errorf("create Content site: %w", err)
			}
		}
	}
	site := unwrap(siteResult, "site")
	siteID = intArg(site, "id")
	if siteID == 0 {
		return nil, errors.New("Content site response missing id")
	}
	extensionKey := storefrontExtensionKey(store.ID)
	var extensionResult map[string]any
	if err := ctx.PlatformAPI().CallAppResult("content", "extensions_upsert", map[string]any{
		"_project_id": pid, "_site_id": siteID,
		"key": extensionKey, "provider_app": "commerce",
		"manifest": commerceStorefrontManifest(store), "publish": true,
	}, &extensionResult); err != nil {
		return nil, fmt.Errorf("install Content storefront: %w", err)
	}
	metadata := copyMap(store.Metadata)
	metadata["content_site_id"] = siteID
	metadata["content_site_slug"] = strArg(site, "slug")
	metadata["content_extension_key"] = extensionKey
	metadata["content_extension_version"] = commerceStorefrontExtensionVersion
	updated, err := dbStoreUpdate(ctx.AppDB(), pid, store.ID, map[string]any{"metadata": metadata})
	if err != nil {
		return nil, err
	}
	return storefrontStatus(pid, updated, site, ""), nil
}

func storefrontStatus(pid string, store *Store, site map[string]any, errorMessage string) *StorefrontStatus {
	siteID := intArg(store.Metadata, "content_site_id")
	siteSlug := strArg(store.Metadata, "content_site_slug")
	status := &StorefrontStatus{
		StoreID: store.ID, Configured: siteID != 0, ContentReady: siteID != 0 && errorMessage == "",
		SiteID: siteID, SiteSlug: siteSlug, Hostname: strArg(site, "hostname"),
		ExtensionKey: strArg(store.Metadata, "content_extension_key"),
		Version:      strArg(store.Metadata, "content_extension_version"), Error: errorMessage,
	}
	if siteSlug != "" {
		query := url.Values{"project_id": []string{pid}, "site": []string{siteSlug}}
		status.PreviewURL = "/api/apps/content/public/?" + query.Encode()
	}
	return status
}

func (a *App) contentStorefrontStatus(ctx *sdk.AppCtx, pid string, store *Store) *StorefrontStatus {
	siteID := intArg(store.Metadata, "content_site_id")
	if siteID == 0 {
		return &StorefrontStatus{StoreID: store.ID}
	}
	if ctx.PlatformAPI() == nil {
		return storefrontStatus(pid, store, nil, "Content is not installed or available")
	}
	var result map[string]any
	if err := ctx.PlatformAPI().CallAppResult("content", "sites_get", map[string]any{
		"_project_id": pid, "id": siteID,
	}, &result); err != nil {
		return storefrontStatus(pid, store, nil, err.Error())
	}
	return storefrontStatus(pid, store, unwrap(result, "site"), "")
}

func (a *App) handleStorefrontConfiguration(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	storeID := intArg(queryArgs(r), "store_id")
	if r.Method == http.MethodPost {
		var body map[string]any
		if err := readJSON(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		storeID = intArg(body, "store_id")
		store, err := dbStoreGetByID(ctx.AppDB(), pid, storeID)
		if err != nil || store == nil {
			httpErr(w, http.StatusNotFound, "store not found")
			return
		}
		status, err := a.configureContentStorefront(ctx, pid, store, intArg(body, "content_site_id"))
		httpResult(w, status, err)
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if storeID == 0 {
		httpErr(w, http.StatusBadRequest, "store_id required")
		return
	}
	store, err := dbStoreGetByID(ctx.AppDB(), pid, storeID)
	if err != nil || store == nil {
		httpErr(w, http.StatusNotFound, "store not found")
		return
	}
	httpJSON(w, a.contentStorefrontStatus(ctx, pid, store))
}
