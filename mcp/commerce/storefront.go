package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	sdk "github.com/apteva/app-sdk"
)

const commerceStorefrontExtensionVersion = "2"

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
		"routes": []any{
			map[string]any{"name": "home", "pattern": "/", "template": "home", "data_sources": []any{"products", "collections"}},
			map[string]any{"name": "product", "pattern": "/products/:handle", "template": "product", "data_sources": []any{"product"}},
			map[string]any{"name": "collections", "pattern": "/collections", "template": "collections", "data_sources": []any{"collections"}},
			map[string]any{"name": "collection", "pattern": "/collections/:handle", "template": "collection", "data_sources": []any{"collection"}},
			map[string]any{"name": "search", "pattern": "/search", "template": "search", "data_sources": []any{"search"}},
			map[string]any{"name": "cart", "pattern": "/cart", "template": "cart"},
			map[string]any{"name": "checkout", "pattern": "/checkout", "template": "checkout"},
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
			"checkout_submit": map[string]any{
				"allowed_input":             []any{"email", "customer_name", "address_line1", "city", "postal_code", "country"},
				"rotate_session_on_success": true,
				"steps": []any{
					map[string]any{"tool": "commerce_cart_create", "args": mergeMaps(fixedStore, map[string]any{"session_token": "{{ session.token }}"})},
					map[string]any{"tool": "commerce_checkout_start", "args": map[string]any{"cart_id": "{{ steps.0.cart.id }}"}},
					map[string]any{"tool": "commerce_checkout_update", "args": map[string]any{
						"checkout_id": "{{ steps.1.checkout.id }}",
						"patch": map[string]any{
							"email":         "{{ input.email }}",
							"customer_name": "{{ input.customer_name }}",
							"shipping_address": map[string]any{
								"line1":       "{{ input.address_line1 }}",
								"city":        "{{ input.city }}",
								"postal_code": "{{ input.postal_code }}",
								"country":     "{{ input.country }}",
							},
						},
					}},
					map[string]any{"tool": "commerce_checkout_pay", "args": map[string]any{"checkout_id": "{{ steps.1.checkout.id }}"}},
				},
			},
		},
		"templates": map[string]any{
			"home":        storefrontDocument(homeStorefrontBody),
			"product":     storefrontDocument(productStorefrontBody),
			"collections": storefrontDocument(collectionsStorefrontBody),
			"collection":  storefrontDocument(collectionStorefrontBody),
			"search":      storefrontDocument(searchStorefrontBody),
			"cart":        storefrontDocument(cartStorefrontBody),
			"checkout":    storefrontDocument(checkoutStorefrontBody),
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
  <section class="page-header"><p class="eyebrow">Secure checkout</p><h1>Delivery details</h1></section>
  <section class="checkout-shell">
    <form class="checkout-form" data-storefront-action="{{action "checkout_submit"}}" data-checkout-form>
      <div class="field-grid">
        <label class="full">Email<input type="email" name="email" autocomplete="email" required></label>
        <label class="full">Full name<input type="text" name="customer_name" autocomplete="name" required></label>
        <label class="full">Address<input type="text" name="address_line1" autocomplete="address-line1" required></label>
        <label>City<input type="text" name="city" autocomplete="address-level2" required></label>
        <label>Postal code<input type="text" name="postal_code" autocomplete="postal-code" required></label>
        <label class="full">Country code<input type="text" name="country" autocomplete="country" maxlength="2" placeholder="US" required></label>
      </div>
      <button class="button wide" type="submit">Place order</button>
      <p class="checkout-note">Your order creates a secure invoice. Available payment instructions are provided after submission.</p>
    </form>
    <div class="checkout-result" data-checkout-result hidden></div>
  </section>`

const storefrontCSS = `
*{box-sizing:border-box}html{color:#171a17;background:#fff;font-family:Inter,ui-sans-serif,system-ui,-apple-system,sans-serif;letter-spacing:0}body{margin:0}a{color:inherit;text-decoration:none}img{display:block;max-width:100%;height:auto}.announcement{background:#171a17;color:#fff;padding:9px 20px;text-align:center;font-size:12px}.site-header{height:72px;padding:0 clamp(20px,5vw,72px);display:flex;align-items:center;justify-content:space-between;border-bottom:1px solid #e7e9e6;position:sticky;top:0;background:rgba(255,255,255,.96);z-index:20}.brand{font-family:Georgia,serif;font-size:23px;font-weight:700}.site-header nav{display:flex;gap:24px;align-items:center;font-size:14px}.site-header nav a:hover{color:var(--accent)}main{min-height:70vh}.hero{min-height:min(70vh,680px);padding:clamp(64px,9vw,128px) clamp(20px,8vw,120px);display:flex;align-items:center;position:relative;overflow:hidden;background:#edf2ec;border-bottom:1px solid #dce3dc}.hero-image{position:absolute;inset:0;width:100%;height:100%;object-fit:cover}.hero-with-image:after{content:"";position:absolute;inset:0;background:rgba(17,20,17,.58)}.hero-copy{position:relative;z-index:1;max-width:900px}.hero h1{font-family:Georgia,serif;font-size:clamp(48px,7vw,96px);line-height:.98;margin:12px 0 24px}.hero-copy>p:not(.eyebrow){font-size:18px;line-height:1.6;max-width:560px;margin:0 0 30px;color:#4d554e}.hero-with-image .hero-copy,.hero-with-image .hero-copy>p,.hero-with-image .eyebrow{color:#fff}.eyebrow{text-transform:uppercase;font-size:11px;letter-spacing:1.6px;font-weight:700;color:var(--accent);margin:0 0 8px}.button{display:inline-flex;min-height:44px;padding:0 20px;border:0;background:var(--accent);color:#fff;align-items:center;justify-content:center;font:600 14px inherit;cursor:pointer;width:max-content}.button:hover{filter:brightness(.92)}.button.wide{width:100%}.section{padding:72px clamp(20px,5vw,72px)}.section-heading{display:flex;justify-content:space-between;align-items:end;margin-bottom:30px}.section-heading h2,.page-header h1{font-family:Georgia,serif;font-size:clamp(36px,5vw,62px);margin:0}.section-heading>a{font-size:13px;border-bottom:1px solid}.product-grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:28px 18px}.product-media{display:block;aspect-ratio:1;background:#f2f3f1;overflow:hidden}.product-media img,.detail-media img{width:100%;height:100%;object-fit:cover;transition:transform .35s}.product-card:hover img{transform:scale(1.02)}.media-placeholder{display:flex;width:100%;height:100%;align-items:center;justify-content:center;padding:30px;color:#778078;text-align:center}.product-copy{padding-top:14px}.product-copy h2{font-size:15px;margin:3px 0 8px;font-weight:600}.price{font-size:14px;margin:0;color:#505650}.page-header{padding:72px clamp(20px,8vw,120px) 42px;border-bottom:1px solid #e7e9e6}.lede{max-width:700px;color:#525a53;line-height:1.7}.collection-grid{padding:36px clamp(20px,5vw,72px) 80px;display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px}.collection-card{min-height:180px;padding:28px;background:#edf2ec;display:flex;flex-direction:column;justify-content:end}.collection-card span{font:600 28px Georgia,serif}.collection-card small{margin-top:8px;color:#5c655e}.product-detail{display:grid;grid-template-columns:minmax(0,1.25fr) minmax(340px,.75fr);min-height:calc(100vh - 105px)}.detail-media{background:#f2f3f1;min-height:620px}.detail-copy{padding:clamp(46px,7vw,100px);align-self:center}.detail-copy h1{font:500 clamp(42px,5vw,70px)/1 Georgia,serif;margin:10px 0 20px}.detail-price{font-size:20px;font-weight:650}.description{line-height:1.7;color:#505750;margin:28px 0}.quantity{display:grid;gap:8px;font-size:12px;margin-bottom:12px}.quantity input{height:44px;border:1px solid #cfd4cf;padding:0 12px;width:100%}.assurances{list-style:none;padding:22px 0 0;margin:22px 0 0;border-top:1px solid #e1e4e1;display:grid;gap:8px;font-size:12px;color:#606860}.search-form{display:flex;gap:10px;max-width:680px;margin-top:28px}.search-form input{height:48px;flex:1;border:1px solid #cbd0cb;padding:0 15px;font-size:16px}.cart-shell{padding:0 clamp(20px,8vw,120px) 90px;max-width:1100px}.cart-row{display:grid;grid-template-columns:minmax(0,1fr) 90px 132px;gap:20px;padding:20px 0;border-bottom:1px solid #e3e6e3;align-items:center}.cart-row input{width:72px;height:40px;border:1px solid #ccd1cc;padding:8px;justify-self:end}.line-total{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}.cart-total{display:flex;justify-content:space-between;font-size:20px;font-weight:700;padding:28px 0}.cart-total span:last-child{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}.checkout-shell{max-width:720px;padding:48px clamp(20px,8vw,120px) 90px}.checkout-form{display:grid;gap:22px}.field-grid{display:grid;grid-template-columns:1fr 1fr;gap:16px}.field-grid label{display:grid;gap:7px;font-size:12px;font-weight:600}.field-grid label.full{grid-column:1/-1}.field-grid input{height:46px;border:1px solid #cbd0cb;padding:0 13px;font-size:15px}.checkout-note{margin:0;color:#667067;font-size:12px;line-height:1.6}.checkout-result{padding:28px;background:#edf2ec}.checkout-result h2{font:600 32px Georgia,serif;margin:0 0 12px}.empty,.cart-loading{padding:40px 0;color:#667067}.site-footer{border-top:1px solid #e1e4e1;padding:34px clamp(20px,5vw,72px);display:flex;justify-content:space-between;color:#606760;font-size:12px}.toast{position:fixed;right:20px;bottom:20px;background:#171a17;color:#fff;padding:13px 18px;opacity:0;transform:translateY(12px);transition:.2s;pointer-events:none;z-index:50}.toast.visible{opacity:1;transform:none}
@media(max-width:900px){.product-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.product-detail{grid-template-columns:1fr}.detail-media{min-height:auto;aspect-ratio:1}.collection-grid{grid-template-columns:1fr 1fr}.site-header nav a:not(.cart-link){display:none}}
@media(max-width:560px){.hero{min-height:590px}.hero h1{font-size:52px}.product-grid{gap:24px 10px}.section{padding:52px 16px}.site-header{padding:0 16px}.collection-grid{grid-template-columns:1fr;padding:24px 16px 60px}.detail-copy{padding:40px 20px}.page-header{padding:52px 20px 30px}.cart-shell,.checkout-shell{padding:0 20px 60px}.cart-row{grid-template-columns:1fr 72px}.cart-row .line-total{grid-column:1/-1}.field-grid{grid-template-columns:1fr}.field-grid label.full{grid-column:auto}.site-footer{padding:28px 20px;gap:20px;flex-direction:column}}
`

const storefrontJS = `
const qs=(s,r=document)=>r.querySelector(s),qsa=(s,r=document)=>[...r.querySelectorAll(s)];
const esc=value=>String(value??'').replace(/[&<>"']/g,char=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
const toast=(message)=>{const el=qs('[data-toast]');if(!el)return;el.textContent=message;el.classList.add('visible');setTimeout(()=>el.classList.remove('visible'),2200)};
async function call(url,input={}){const response=await fetch(url,{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Requested-With':'storefront'},body:JSON.stringify(input)});const body=await response.json().catch(()=>({}));if(!response.ok)throw new Error(body.error||'Request failed');return body}
function cartFrom(body){return body?.result?.cart||body?.result||body?.steps?.at(-1)?.cart||null}
function updateCount(cart){qsa('[data-cart-count]').forEach(el=>el.textContent=cart?.items?.length?'('+cart.items.length+')':'')}
function cartTitle(item){const title=String(item?.title_snapshot??'');const parts=title.split(' - ');return parts.length===2&&parts[0]===parts[1]?parts[0]:title}
const searchForm=qs('[data-search-form]');if(searchForm)searchForm.addEventListener('submit',event=>{event.preventDefault();const target=new URL(searchForm.action);target.searchParams.set('q',qs('input[name=q]',searchForm).value);location.assign(target)});
qsa('form[data-storefront-action]').forEach(form=>form.addEventListener('submit',async event=>{event.preventDefault();const button=qs('button[type=submit]',form);if(button)button.disabled=true;try{const input=Object.fromEntries(new FormData(form));qsa('input[type=number]',form).forEach(field=>input[field.name]=Number(field.value));const body=await call(form.dataset.storefrontAction,input);updateCount(cartFrom(body));if(form.matches('[data-checkout-form]')){const sale=body?.result?.sale;form.hidden=true;const result=qs('[data-checkout-result]');result.hidden=false;result.innerHTML='<h2>Order received</h2><p>Your order '+(sale?.invoice_number?'for invoice <strong>'+esc(sale.invoice_number)+'</strong> ':'')+'has been submitted. Follow the merchant payment instructions to complete payment.</p>'}else{toast(form.dataset.success||'Updated')}}catch(error){toast(error.message)}finally{if(button)button.disabled=false}}));
const cartRoot=qs('[data-cart]');if(cartRoot){const money=(cents,currency)=>new Intl.NumberFormat(undefined,{style:'currency',currency:currency||'USD'}).format((cents||0)/100);const render=cart=>{updateCount(cart);if(!cart?.items?.length){cartRoot.innerHTML='<p class="empty">Your cart is empty.</p>';return}cartRoot.innerHTML=cart.items.map(item=>'<div class="cart-row"><div><strong>'+esc(cartTitle(item))+'</strong><br><small>'+esc(item.sku)+'</small></div><input aria-label="Quantity" data-item="'+Number(item.id)+'" type="number" min="0" step="1" value="'+Number(item.quantity)+'"><span class="line-total">'+esc(money(item.unit_amount_cents*item.quantity,item.currency))+'</span></div>').join('')+'<div class="cart-total"><span>Total</span><span>'+esc(money(cart.total_cents,cart.currency))+'</span></div><a class="button wide" href="'+esc(cartRoot.dataset.checkoutUrl)+'">Continue to checkout</a>';qsa('[data-item]',cartRoot).forEach(input=>input.addEventListener('change',async()=>{try{const body=await call(cartRoot.dataset.quantityAction,{item_id:Number(input.dataset.item),quantity:Number(input.value)});render(cartFrom(body))}catch(error){toast(error.message)}}))};call(cartRoot.dataset.cartAction).then(body=>render(cartFrom(body))).catch(error=>{cartRoot.textContent=error.message})}
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
