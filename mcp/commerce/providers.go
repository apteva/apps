package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type ProviderPolicy struct {
	ProjectID       string         `json:"project_id,omitempty"`
	StoreID         int64          `json:"store_id"`
	ConnectionID    int64          `json:"connection_id"`
	ProviderSlug    string         `json:"provider_slug"`
	Enabled         bool           `json:"enabled"`
	FulfillmentMode string         `json:"fulfillment_mode"`
	MarginBPS       int64          `json:"margin_bps"`
	Settings        map[string]any `json:"settings,omitempty"`
	IsDefault       bool           `json:"is_default,omitempty"`
	Connected       bool           `json:"connected"`
	CreatedAt       string         `json:"created_at,omitempty"`
	UpdatedAt       string         `json:"updated_at,omitempty"`
}

type VariantSource struct {
	ID                int64          `json:"id"`
	StoreID           int64          `json:"store_id"`
	VariantID         int64          `json:"variant_id"`
	ConnectionID      int64          `json:"connection_id"`
	ProviderSlug      string         `json:"provider_slug"`
	ExternalProductID string         `json:"external_product_id"`
	ExternalVariantID string         `json:"external_variant_id"`
	ProviderSKU       string         `json:"provider_sku"`
	UnitCostCents     int64          `json:"unit_cost_cents"`
	Currency          string         `json:"currency"`
	Availability      string         `json:"availability"`
	AvailableQuantity *float64       `json:"available_quantity,omitempty"`
	Source            map[string]any `json:"source,omitempty"`
	LastSyncedAt      string         `json:"last_synced_at,omitempty"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
}

type ProviderProduct struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Vendor      string            `json:"vendor,omitempty"`
	ProductType string            `json:"product_type,omitempty"`
	ImageURL    string            `json:"image_url,omitempty"`
	Variants    []ProviderVariant `json:"variants,omitempty"`
	Raw         any               `json:"raw,omitempty"`
}

type ProviderVariant struct {
	ID                string         `json:"id"`
	SKU               string         `json:"sku,omitempty"`
	Title             string         `json:"title"`
	CostCents         int64          `json:"cost_cents"`
	SuggestedPrice    int64          `json:"suggested_price_cents,omitempty"`
	Currency          string         `json:"currency"`
	Available         bool           `json:"available"`
	AvailableQuantity *float64       `json:"available_quantity,omitempty"`
	Options           map[string]any `json:"options,omitempty"`
	Raw               any            `json:"raw,omitempty"`
}

func (a *App) toolProvidersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	storeID := intArg(args, "store_id")
	policies, err := dbProviderPolicies(ctx.AppDB(), pid, storeID)
	if err != nil {
		return nil, err
	}
	byConnection := map[int64]*ProviderPolicy{}
	for _, policy := range policies {
		byConnection[policy.ConnectionID] = policy
	}
	for _, bound := range ctx.IntegrationsFor("supplier_provider") {
		if bound == nil {
			continue
		}
		policy := byConnection[bound.ConnectionID]
		if policy == nil {
			policy = &ProviderPolicy{
				StoreID: storeID, ConnectionID: bound.ConnectionID, ProviderSlug: bound.AppSlug,
				Enabled: true, FulfillmentMode: "review", MarginBPS: 3000, Settings: map[string]any{},
			}
			policies = append(policies, policy)
		}
		policy.Connected = true
		policy.IsDefault = bound.IsDefault
	}
	return map[string]any{"providers": policies, "count": len(policies)}, nil
}

func (a *App) toolProviderPolicySet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	storeID := intArg(args, "store_id")
	connectionID := intArg(args, "connection_id")
	if storeID == 0 || connectionID == 0 {
		return nil, errors.New("store_id and connection_id required")
	}
	bound := providerBinding(ctx, connectionID)
	if bound == nil {
		return nil, errors.New("connection is not bound to Commerce as a supplier provider")
	}
	args["provider_slug"] = bound.AppSlug
	policy, err := dbProviderPolicyUpsert(ctx.AppDB(), pid, args)
	return map[string]any{"provider": policy}, err
}

func (a *App) toolProviderCatalog(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	bound, policy, err := a.resolveProvider(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	input := mapArg(args, "provider_input")
	tool := ""
	switch bound.AppSlug {
	case "printful":
		tool = "get_products"
	case "printify":
		tool = "get_products"
		applyPolicySetting(input, policy, "shop_id")
	case "bigbuy":
		tool = "products_list"
	case "cjdropshipping":
		tool = "products_search"
	case "prodigi":
		skus := prodigiCatalogSKUs(input, policy)
		if len(skus) == 0 {
			return nil, errors.New("Prodigi catalog requires provider_input.skus or provider policy settings.catalog_skus")
		}
		products := make([]ProviderProduct, 0, len(skus))
		rawProducts := make([]any, 0, len(skus))
		for _, sku := range skus {
			rawProduct, callErr := executeProviderTool(ctx, bound, "get_product", map[string]any{"sku": sku})
			if callErr != nil {
				return nil, callErr
			}
			rawProducts = append(rawProducts, rawProduct)
			products = append(products, normalizeProviderProducts("prodigi", rawProduct, true)...)
		}
		return map[string]any{
			"provider": bound.AppSlug, "connection_id": bound.ConnectionID,
			"products": products, "count": len(products), "raw": rawProducts,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported supplier provider %q", bound.AppSlug)
	}
	raw, err := executeProviderTool(ctx, bound, tool, input)
	if err != nil {
		return nil, err
	}
	products := normalizeProviderProducts(bound.AppSlug, raw, false)
	return map[string]any{
		"provider": bound.AppSlug, "connection_id": bound.ConnectionID,
		"products": products, "count": len(products), "raw": raw,
	}, nil
}

func (a *App) toolProviderProductGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	bound, policy, err := a.resolveProvider(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	productID := firstNonEmpty(strArg(args, "external_product_id"), strArg(args, "product_id"))
	if productID == "" {
		return nil, errors.New("external_product_id required")
	}
	product, err := fetchProviderProduct(ctx, bound, policy, productID, mapArg(args, "provider_input"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": bound.AppSlug, "connection_id": bound.ConnectionID, "product": product}, nil
}

func (a *App) toolProviderProductImport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	args["store_id"] = store.ID
	bound, policy, err := a.resolveProvider(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	var product *ProviderProduct
	if raw := mapArg(args, "product"); len(raw) > 0 {
		product = providerProductFromMap(raw)
	}
	if product == nil || product.ID == "" || len(product.Variants) == 0 {
		productID := firstNonEmpty(strArg(args, "external_product_id"), strArg(args, "product_id"))
		product, err = fetchProviderProduct(ctx, bound, policy, productID, mapArg(args, "provider_input"))
		if err != nil {
			return nil, err
		}
	}
	selected := stringSet(args["variant_ids"])
	var variants []ProviderVariant
	for _, variant := range product.Variants {
		if len(selected) == 0 || selected[variant.ID] {
			variants = append(variants, variant)
		}
	}
	if len(variants) == 0 {
		return nil, errors.New("no provider variants selected")
	}
	if bound.AppSlug == "prodigi" {
		variants, err = prepareProdigiVariants(variants, mapArg(args, "provider_input"))
		if err != nil {
			return nil, err
		}
		for i := range variants {
			variants[i].Currency = store.DefaultCurrency
		}
	}
	for _, variant := range variants {
		if variant.ID == "" {
			return nil, errors.New("provider variant is missing an id")
		}
		if existing, _ := dbVariantSourceByExternal(ctx.AppDB(), pid, bound.ConnectionID, variant.ID); existing != nil {
			return nil, fmt.Errorf("provider variant %s is already imported", variant.ID)
		}
	}
	priceOverrides := mapArg(args, "price_cents_by_variant")
	first := variants[0]
	firstPrice, err := providerRetailPrice(first, policy.MarginBPS, priceOverrides)
	if err != nil {
		return nil, err
	}
	createArgs := map[string]any{
		"_project_id": pid, "store_id": store.ID, "title": firstNonEmpty(strArg(args, "title"), product.Title),
		"handle": strArg(args, "handle"), "description_html": firstNonEmpty(strArg(args, "description_html"), product.Description),
		"vendor":       firstNonEmpty(strArg(args, "vendor"), product.Vendor, bound.AppSlug),
		"product_type": firstNonEmpty(strArg(args, "product_type"), product.ProductType),
		"price_cents":  firstPrice, "currency": firstNonEmpty(first.Currency, store.DefaultCurrency),
		"sku": first.SKU, "variant_title": first.Title, "status": "draft",
		"metadata": map[string]any{"provider": bound.AppSlug, "external_product_id": product.ID, "image_url": product.ImageURL},
	}
	created, err := a.toolProductsCreate(ctx, createArgs)
	if err != nil {
		return nil, err
	}
	listing, _ := created.(map[string]any)["product"].(*Listing)
	if listing == nil || len(listing.Variants) == 0 {
		return nil, errors.New("created Commerce product has no first variant")
	}
	if _, err := dbVariantSourceUpsert(ctx.AppDB(), pid, store.ID, listing.Variants[0].ID, bound, product.ID, first); err != nil {
		return nil, err
	}
	for i := 1; i < len(variants); i++ {
		variant := variants[i]
		price, err := providerRetailPrice(variant, policy.MarginBPS, priceOverrides)
		if err != nil {
			return nil, err
		}
		result, err := a.toolVariantsCreate(ctx, map[string]any{
			"_project_id": pid, "listing_id": listing.ID, "title": variant.Title, "sku": variant.SKU,
			"price_cents": price, "currency": firstNonEmpty(variant.Currency, store.DefaultCurrency),
			"metadata": map[string]any{"provider": bound.AppSlug, "external_variant_id": variant.ID},
		})
		if err != nil {
			return nil, err
		}
		localVariant, _ := result.(map[string]any)["variant"].(*Variant)
		if localVariant == nil {
			return nil, errors.New("created Commerce variant response missing variant")
		}
		if _, err := dbVariantSourceUpsert(ctx.AppDB(), pid, store.ID, localVariant.ID, bound, product.ID, variant); err != nil {
			return nil, err
		}
	}
	listing, err = dbListingGet(ctx.AppDB(), pid, listing.ID, true)
	if err != nil {
		return nil, err
	}
	sources, err := dbVariantSourcesForListing(ctx.AppDB(), pid, listing.ID)
	if err != nil {
		return nil, err
	}
	ctx.Emit("commerce.provider_product.imported", map[string]any{
		"listing_id": listing.ID, "store_id": store.ID, "provider": bound.AppSlug, "external_product_id": product.ID,
	})
	return map[string]any{"product": listing, "sources": sources}, nil
}

func (a *App) toolVariantSourcesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	sources, err := dbVariantSources(ctx.AppDB(), pid, args)
	return map[string]any{"sources": sources, "count": len(sources)}, err
}

func (a *App) toolVariantSourceUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	source, err := dbVariantSourceUpdate(ctx.AppDB(), pid, intArg(args, "id"), mapArg(args, "patch"))
	return map[string]any{"source": source}, err
}

func (a *App) resolveProvider(ctx *sdk.AppCtx, pid string, args map[string]any) (*sdk.BoundIntegration, *ProviderPolicy, error) {
	connectionID := intArg(args, "connection_id")
	bound := providerBinding(ctx, connectionID)
	if bound == nil {
		return nil, nil, errors.New("supplier provider connection is not bound")
	}
	storeID := intArg(args, "store_id")
	policy, err := dbProviderPolicyGet(ctx.AppDB(), pid, storeID, bound.ConnectionID)
	if err != nil {
		return nil, nil, err
	}
	if policy == nil {
		policy = &ProviderPolicy{
			ProjectID: pid, StoreID: storeID, ConnectionID: bound.ConnectionID, ProviderSlug: bound.AppSlug,
			Enabled: true, FulfillmentMode: "review", MarginBPS: 3000, Settings: map[string]any{}, Connected: true,
		}
	}
	if !policy.Enabled {
		return nil, nil, errors.New("supplier provider is disabled for this store")
	}
	return bound, policy, nil
}

func providerBinding(ctx *sdk.AppCtx, connectionID int64) *sdk.BoundIntegration {
	if ctx == nil {
		return nil
	}
	for _, bound := range ctx.IntegrationsFor("supplier_provider") {
		if bound == nil {
			continue
		}
		if connectionID == 0 && bound.IsDefault || connectionID != 0 && bound.ConnectionID == connectionID {
			return bound
		}
	}
	if connectionID == 0 {
		bounds := ctx.IntegrationsFor("supplier_provider")
		if len(bounds) > 0 {
			return bounds[0]
		}
	}
	return nil
}

func executeProviderTool(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, tool string, input map[string]any) (any, error) {
	if ctx == nil || ctx.PlatformAPI() == nil || bound == nil {
		return nil, errors.New("provider integration unavailable")
	}
	result, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, input)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %w", bound.AppSlug, tool, err)
	}
	if result == nil || !result.Success {
		status := 0
		var detail string
		if result != nil {
			status = result.Status
			detail = strings.TrimSpace(string(result.Data))
		}
		return nil, fmt.Errorf("%s.%s failed (HTTP %d): %s", bound.AppSlug, tool, status, detail)
	}
	if len(result.Data) == 0 {
		return map[string]any{}, nil
	}
	var data any
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return nil, fmt.Errorf("%s.%s returned invalid JSON: %w", bound.AppSlug, tool, err)
	}
	return data, nil
}

func fetchProviderProduct(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, policy *ProviderPolicy, productID string, input map[string]any) (*ProviderProduct, error) {
	if strings.TrimSpace(productID) == "" {
		return nil, errors.New("external_product_id required")
	}
	var raw any
	var err error
	switch bound.AppSlug {
	case "printful":
		input["id"] = productID
		raw, err = executeProviderTool(ctx, bound, "get_product", input)
	case "printify":
		input["product_id"] = productID
		applyPolicySetting(input, policy, "shop_id")
		raw, err = executeProviderTool(ctx, bound, "get_product", input)
	case "bigbuy":
		id, parseErr := strconv.ParseInt(productID, 10, 64)
		if parseErr != nil {
			return nil, errors.New("BigBuy product id must be numeric")
		}
		productRaw, callErr := executeProviderTool(ctx, bound, "product_get", map[string]any{"id": id})
		if callErr != nil {
			return nil, callErr
		}
		infoInput := map[string]any{"id": id}
		applyPolicySetting(infoInput, policy, "isoCode")
		infoRaw, _ := executeProviderTool(ctx, bound, "product_information_get", infoInput)
		variantsRaw, _ := executeProviderTool(ctx, bound, "product_variations_get", map[string]any{"id": id})
		raw = map[string]any{"product": productRaw, "information": infoRaw, "variations": variantsRaw}
	case "cjdropshipping":
		productRaw, callErr := executeProviderTool(ctx, bound, "product_get", map[string]any{"pid": productID})
		if callErr != nil {
			return nil, callErr
		}
		variantsRaw, _ := executeProviderTool(ctx, bound, "product_variants_list", map[string]any{"pid": productID})
		raw = map[string]any{"product": productRaw, "variants": variantsRaw}
	case "prodigi":
		input["sku"] = productID
		raw, err = executeProviderTool(ctx, bound, "get_product", input)
	default:
		return nil, fmt.Errorf("unsupported supplier provider %q", bound.AppSlug)
	}
	if err != nil {
		return nil, err
	}
	products := normalizeProviderProducts(bound.AppSlug, raw, true)
	if len(products) == 0 {
		return nil, errors.New("provider product response could not be interpreted")
	}
	product := products[0]
	product.Raw = raw
	if product.ID == "" {
		product.ID = productID
	}
	return &product, nil
}

func normalizeProviderProducts(provider string, raw any, detail bool) []ProviderProduct {
	switch provider {
	case "printful":
		return normalizePrintfulProducts(raw, detail)
	case "printify":
		return normalizePrintifyProducts(raw)
	case "bigbuy":
		return normalizeBigBuyProducts(raw, detail)
	case "cjdropshipping":
		return normalizeCJProducts(raw, detail)
	case "prodigi":
		return normalizeProdigiProducts(raw)
	default:
		return nil
	}
}

func normalizeProdigiProducts(raw any) []ProviderProduct {
	root := anyMap(raw)
	productMap := anyMap(root["product"])
	if len(productMap) == 0 {
		productMap = root
	}
	sku := firstString(productMap, "sku")
	if sku == "" {
		return nil
	}
	product := ProviderProduct{
		ID: sku, Title: firstNonEmpty(firstString(productMap, "description"), sku),
		Description: firstString(productMap, "description"), Vendor: "Prodigi",
		Raw: raw,
	}
	printAreas := productMap["printAreas"]
	rows := anySlice(productMap["variants"])
	if len(rows) == 0 {
		rows = []any{map[string]any{"attributes": map[string]any{}}}
	}
	for _, row := range rows {
		variantMap := anyMap(row)
		attributes := mapArg(variantMap, "attributes")
		rawVariant := copyMap(variantMap)
		rawVariant["sku"] = sku
		rawVariant["printAreas"] = printAreas
		product.Variants = append(product.Variants, ProviderVariant{
			ID:        prodigiVariantID(sku, attributes),
			SKU:       sku,
			Title:     prodigiVariantTitle(attributes),
			Currency:  "USD",
			Available: true,
			Options:   attributes,
			Raw:       rawVariant,
		})
	}
	return []ProviderProduct{product}
}

func prodigiVariantID(sku string, attributes map[string]any) string {
	if len(attributes) == 0 {
		return sku
	}
	encoded, _ := json.Marshal(attributes)
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%s#%x", sku, sum[:6])
}

func prodigiVariantTitle(attributes map[string]any) string {
	if len(attributes) == 0 {
		return "Default"
	}
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s: %v", key, attributes[key]))
	}
	if len(parts) == 0 {
		return "Default"
	}
	return strings.Join(parts, " / ")
}

func prodigiCatalogSKUs(input map[string]any, policy *ProviderPolicy) []string {
	value := input["skus"]
	if value == nil && policy != nil {
		value = policy.Settings["catalog_skus"]
	}
	var candidates []any
	switch current := value.(type) {
	case []any:
		candidates = current
	case []string:
		for _, item := range current {
			candidates = append(candidates, item)
		}
	case string:
		for _, item := range strings.FieldsFunc(current, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\r'
		}) {
			candidates = append(candidates, item)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, candidate := range candidates {
		sku := strings.TrimSpace(fmt.Sprint(candidate))
		if sku == "" || seen[sku] {
			continue
		}
		seen[sku] = true
		out = append(out, sku)
	}
	return out
}

func prepareProdigiVariants(variants []ProviderVariant, input map[string]any) ([]ProviderVariant, error) {
	assets := anySlice(input["assets"])
	if len(assets) == 0 {
		return nil, errors.New("Prodigi import requires provider_input.assets with public artwork URLs")
	}
	for _, raw := range assets {
		asset := anyMap(raw)
		assetURL := firstString(asset, "url")
		if firstString(asset, "printArea") == "" || assetURL == "" {
			return nil, errors.New("each Prodigi asset requires printArea and url")
		}
		parsed, err := url.ParseRequestURI(assetURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return nil, errors.New("each Prodigi asset url must be a public HTTPS URL")
		}
	}
	designKey := strings.TrimSpace(strArg(input, "design_key"))
	if designKey == "" {
		encoded, _ := json.Marshal(assets)
		sum := sha256.Sum256(encoded)
		designKey = fmt.Sprintf("%x", sum[:6])
	}
	sizing := firstNonEmpty(strArg(input, "sizing"), "fitPrintArea")
	switch sizing {
	case "fillPrintArea", "fitPrintArea", "stretchToPrintArea":
	default:
		return nil, errors.New("Prodigi sizing must be fillPrintArea, fitPrintArea, or stretchToPrintArea")
	}
	out := make([]ProviderVariant, 0, len(variants))
	for _, variant := range variants {
		raw := anyMap(variant.Raw)
		raw["assets"] = assets
		raw["sizing"] = sizing
		raw["designKey"] = designKey
		variant.Raw = raw
		variant.ID = variant.ID + "@" + compactExternalID(designKey, 48)
		out = append(out, variant)
	}
	return out, nil
}

func normalizePrintfulProducts(raw any, detail bool) []ProviderProduct {
	root := anyMap(raw)
	result := root["result"]
	if detail {
		detailMap := anyMap(result)
		productMap := anyMap(detailMap["sync_product"])
		if len(productMap) == 0 {
			productMap = detailMap
		}
		product := providerProductBase(productMap)
		product.Variants = normalizeVariants("printful", anySlice(detailMap["sync_variants"]))
		product.Raw = result
		return []ProviderProduct{product}
	}
	rows := anySlice(result)
	out := make([]ProviderProduct, 0, len(rows))
	for _, row := range rows {
		product := providerProductBase(anyMap(row))
		product.Raw = row
		out = append(out, product)
	}
	return out
}

func normalizePrintifyProducts(raw any) []ProviderProduct {
	root := anyMap(raw)
	rows := anySlice(root["data"])
	if len(rows) == 0 && root["id"] != nil {
		rows = []any{root}
	}
	out := make([]ProviderProduct, 0, len(rows))
	for _, row := range rows {
		m := anyMap(row)
		product := providerProductBase(m)
		product.Description = stringField(m, "description")
		product.Variants = normalizeVariants("printify", anySlice(m["variants"]))
		if images := anySlice(m["images"]); len(images) > 0 {
			product.ImageURL = stringField(anyMap(images[0]), "src")
		}
		product.Raw = row
		out = append(out, product)
	}
	return out
}

func normalizeBigBuyProducts(raw any, detail bool) []ProviderProduct {
	if detail {
		root := anyMap(raw)
		productMap := firstObject(root["product"])
		infoMap := firstObject(root["information"])
		for key, value := range infoMap {
			if productMap[key] == nil || productMap[key] == "" {
				productMap[key] = value
			}
		}
		product := providerProductBase(productMap)
		product.Variants = normalizeVariants("bigbuy", providerRows(root["variations"]))
		if len(product.Variants) == 0 {
			product.Variants = normalizeVariants("bigbuy", []any{productMap})
		}
		product.Raw = raw
		return []ProviderProduct{product}
	}
	rows := providerRows(raw)
	out := make([]ProviderProduct, 0, len(rows))
	for _, row := range rows {
		product := providerProductBase(anyMap(row))
		product.Raw = row
		out = append(out, product)
	}
	return out
}

func normalizeCJProducts(raw any, detail bool) []ProviderProduct {
	if detail {
		root := anyMap(raw)
		productMap := firstObject(root["product"])
		product := providerProductBase(productMap)
		product.Variants = normalizeVariants("cjdropshipping", providerRows(root["variants"]))
		product.Raw = raw
		return []ProviderProduct{product}
	}
	rows := providerRows(raw)
	out := make([]ProviderProduct, 0, len(rows))
	for _, row := range rows {
		product := providerProductBase(anyMap(row))
		product.Raw = row
		out = append(out, product)
	}
	return out
}

func providerProductBase(m map[string]any) ProviderProduct {
	return ProviderProduct{
		ID:          firstString(m, "id", "pid", "productId", "product_id"),
		Title:       firstString(m, "title", "name", "productNameEn", "productName", "nameEn"),
		Description: firstString(m, "description", "descriptionHtml", "description_html"),
		Vendor:      firstString(m, "brand", "manufacturer", "vendor"),
		ProductType: firstString(m, "type_name", "categoryName", "category"),
		ImageURL:    firstString(m, "thumbnail_url", "image", "bigImage", "productImage", "imageUrl"),
	}
}

func normalizeVariants(provider string, rows []any) []ProviderVariant {
	out := make([]ProviderVariant, 0, len(rows))
	for _, row := range rows {
		m := anyMap(row)
		if len(m) == 0 {
			continue
		}
		variant := ProviderVariant{
			ID:        firstString(m, "id", "vid", "variant_id", "variantId", "external_id"),
			SKU:       firstString(m, "sku", "variantSku", "reference"),
			Title:     firstString(m, "title", "name", "variantNameEn", "variantName", "variant_name"),
			Currency:  strings.ToUpper(firstString(m, "currency", "currencyCode")),
			Available: true,
			Raw:       row,
		}
		if variant.Title == "" {
			variant.Title = firstNonEmpty(variant.SKU, "Default")
		}
		switch provider {
		case "printify":
			variant.CostCents = numberCents(m["cost"], true)
			variant.SuggestedPrice = numberCents(m["price"], true)
			if enabled, ok := m["is_enabled"].(bool); ok {
				variant.Available = enabled
			}
			if available, ok := m["is_available"].(bool); ok {
				variant.Available = variant.Available && available
			}
		case "printful":
			variant.SuggestedPrice = numberCents(m["retail_price"], false)
			product := anyMap(m["product"])
			if variant.ID == "" {
				variant.ID = firstString(product, "variant_id", "id")
			}
		case "bigbuy":
			variant.CostCents = firstMoneyCents(m, "wholesalePrice", "price", "costPrice")
			qty := firstNumber(m, "stock", "quantity", "availableStock")
			if qty >= 0 {
				variant.AvailableQuantity = &qty
				variant.Available = qty > 0
			}
		case "cjdropshipping":
			variant.CostCents = firstMoneyCents(m, "variantSellPrice", "sellPrice", "variantPrice", "price")
			qty := firstNumber(m, "inventory", "variantInventory", "stock")
			if qty >= 0 {
				variant.AvailableQuantity = &qty
				variant.Available = qty > 0
			}
		}
		if variant.Currency == "" {
			variant.Currency = "USD"
		}
		out = append(out, variant)
	}
	return out
}

func providerProductFromMap(m map[string]any) *ProviderProduct {
	product := &ProviderProduct{
		ID: strArg(m, "id"), Title: strArg(m, "title"), Description: strArg(m, "description"),
		Vendor: strArg(m, "vendor"), ProductType: strArg(m, "product_type"), ImageURL: strArg(m, "image_url"),
		Raw: m["raw"],
	}
	for _, row := range anySlice(m["variants"]) {
		vm := anyMap(row)
		variant := ProviderVariant{
			ID: strArg(vm, "id"), SKU: strArg(vm, "sku"), Title: strArg(vm, "title"),
			CostCents: intArg(vm, "cost_cents"), SuggestedPrice: intArg(vm, "suggested_price_cents"),
			Currency: strArg(vm, "currency"), Available: boolArg(vm, "available"),
			Options: mapArg(vm, "options"), Raw: vm["raw"],
		}
		if value, ok := numberValue(vm["available_quantity"]); ok {
			variant.AvailableQuantity = &value
		}
		product.Variants = append(product.Variants, variant)
	}
	return product
}

func providerRetailPrice(variant ProviderVariant, marginBPS int64, overrides map[string]any) (int64, error) {
	if override := intArg(overrides, variant.ID); override > 0 {
		return override, nil
	}
	if baseID := strings.SplitN(variant.ID, "@", 2)[0]; baseID != variant.ID {
		if override := intArg(overrides, baseID); override > 0 {
			return override, nil
		}
	}
	if variant.SuggestedPrice > 0 {
		return variant.SuggestedPrice, nil
	}
	if variant.CostCents <= 0 {
		return 0, fmt.Errorf("provider variant %s has no cost or suggested retail price; provide price_cents_by_variant", variant.ID)
	}
	if marginBPS < 0 || marginBPS >= 9500 {
		return 0, errors.New("margin_bps must be between 0 and 9499")
	}
	return int64(math.Ceil(float64(variant.CostCents) * 10000 / float64(10000-marginBPS))), nil
}

func dbProviderPolicyUpsert(db *sql.DB, pid string, args map[string]any) (*ProviderPolicy, error) {
	mode := firstNonEmpty(strArg(args, "fulfillment_mode"), "review")
	if mode != "review" && mode != "automatic" {
		return nil, errors.New("fulfillment_mode must be review or automatic")
	}
	margin := intArg(args, "margin_bps")
	if _, ok := args["margin_bps"]; !ok {
		margin = 3000
	}
	if margin < 0 || margin >= 9500 {
		return nil, errors.New("margin_bps must be between 0 and 9499")
	}
	enabled := true
	if _, ok := args["enabled"]; ok {
		enabled = boolArg(args, "enabled")
	}
	_, err := db.Exec(
		`INSERT INTO commerce_provider_policies
		   (project_id, store_id, connection_id, provider_slug, enabled, fulfillment_mode, margin_bps, settings_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, store_id, connection_id) DO UPDATE SET
		   provider_slug=excluded.provider_slug, enabled=excluded.enabled,
		   fulfillment_mode=excluded.fulfillment_mode, margin_bps=excluded.margin_bps,
		   settings_json=excluded.settings_json, updated_at=CURRENT_TIMESTAMP`,
		pid, intArg(args, "store_id"), intArg(args, "connection_id"), strArg(args, "provider_slug"),
		boolToInt(enabled), mode, margin, jsonText(args["settings"], "{}"))
	if err != nil {
		return nil, err
	}
	return dbProviderPolicyGet(db, pid, intArg(args, "store_id"), intArg(args, "connection_id"))
}

func dbProviderPolicyGet(db *sql.DB, pid string, storeID, connectionID int64) (*ProviderPolicy, error) {
	if storeID == 0 || connectionID == 0 {
		return nil, nil
	}
	var policy ProviderPolicy
	var enabled int
	var settings string
	err := db.QueryRow(
		`SELECT project_id, store_id, connection_id, provider_slug, enabled, fulfillment_mode,
		        margin_bps, settings_json, created_at, updated_at
		   FROM commerce_provider_policies
		  WHERE project_id=? AND store_id=? AND connection_id=?`,
		pid, storeID, connectionID).Scan(
		&policy.ProjectID, &policy.StoreID, &policy.ConnectionID, &policy.ProviderSlug, &enabled,
		&policy.FulfillmentMode, &policy.MarginBPS, &settings, &policy.CreatedAt, &policy.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	policy.Enabled = enabled != 0
	policy.Settings = jsonMap(settings)
	return &policy, nil
}

func dbProviderPolicies(db *sql.DB, pid string, storeID int64) ([]*ProviderPolicy, error) {
	where := "project_id=?"
	args := []any{pid}
	if storeID != 0 {
		where += " AND store_id=?"
		args = append(args, storeID)
	}
	rows, err := db.Query(
		`SELECT project_id, store_id, connection_id, provider_slug, enabled, fulfillment_mode,
		        margin_bps, settings_json, created_at, updated_at
		   FROM commerce_provider_policies WHERE `+where+` ORDER BY store_id, provider_slug`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProviderPolicy
	for rows.Next() {
		var policy ProviderPolicy
		var enabled int
		var settings string
		if err := rows.Scan(&policy.ProjectID, &policy.StoreID, &policy.ConnectionID, &policy.ProviderSlug,
			&enabled, &policy.FulfillmentMode, &policy.MarginBPS, &settings, &policy.CreatedAt, &policy.UpdatedAt); err != nil {
			return nil, err
		}
		policy.Enabled = enabled != 0
		policy.Settings = jsonMap(settings)
		out = append(out, &policy)
	}
	return out, rows.Err()
}

func dbVariantSourceUpsert(db *sql.DB, pid string, storeID, variantID int64, bound *sdk.BoundIntegration, productID string, variant ProviderVariant) (*VariantSource, error) {
	availability := "available"
	if !variant.Available {
		availability = "unavailable"
	}
	_, err := db.Exec(
		`INSERT INTO commerce_variant_sources
		   (project_id, store_id, variant_id, connection_id, provider_slug, external_product_id,
		    external_variant_id, provider_sku, unit_cost_cents, currency, availability,
		    available_quantity, source_json, last_synced_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(project_id, variant_id) DO UPDATE SET
		   connection_id=excluded.connection_id, provider_slug=excluded.provider_slug,
		   external_product_id=excluded.external_product_id, external_variant_id=excluded.external_variant_id,
		   provider_sku=excluded.provider_sku, unit_cost_cents=excluded.unit_cost_cents,
		   currency=excluded.currency, availability=excluded.availability,
		   available_quantity=excluded.available_quantity, source_json=excluded.source_json,
		   last_synced_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP`,
		pid, storeID, variantID, bound.ConnectionID, bound.AppSlug, productID, variant.ID,
		variant.SKU, variant.CostCents, firstNonEmpty(variant.Currency, "USD"), availability,
		nullableFloat(variant.AvailableQuantity), jsonText(variant.Raw, "{}"))
	if err != nil {
		return nil, err
	}
	return dbVariantSourceByVariant(db, pid, variantID)
}

func dbVariantSourceByVariant(db *sql.DB, pid string, variantID int64) (*VariantSource, error) {
	return scanVariantSource(db.QueryRow(variantSourceSelect()+` WHERE project_id=? AND variant_id=?`, pid, variantID))
}

func dbVariantSourceByExternal(db *sql.DB, pid string, connectionID int64, externalVariantID string) (*VariantSource, error) {
	return scanVariantSource(db.QueryRow(
		variantSourceSelect()+` WHERE project_id=? AND connection_id=? AND external_variant_id=?`,
		pid, connectionID, externalVariantID))
}

func dbVariantSourcesForListing(db *sql.DB, pid string, listingID int64) ([]*VariantSource, error) {
	return dbVariantSources(db, pid, map[string]any{"listing_id": listingID})
}

func dbVariantSources(db *sql.DB, pid string, args map[string]any) ([]*VariantSource, error) {
	where := []string{"s.project_id=?"}
	values := []any{pid}
	if storeID := intArg(args, "store_id"); storeID != 0 {
		where = append(where, "s.store_id=?")
		values = append(values, storeID)
	}
	if listingID := intArg(args, "listing_id"); listingID != 0 {
		where = append(where, "v.listing_id=?")
		values = append(values, listingID)
	}
	if provider := strArg(args, "provider"); provider != "" {
		where = append(where, "s.provider_slug=?")
		values = append(values, provider)
	}
	rows, err := db.Query(
		`SELECT s.id, s.store_id, s.variant_id, s.connection_id, s.provider_slug,
		        s.external_product_id, s.external_variant_id, s.provider_sku,
		        s.unit_cost_cents, s.currency, s.availability, s.available_quantity,
		        s.source_json, s.last_synced_at, s.created_at, s.updated_at
		   FROM commerce_variant_sources s
		   JOIN commerce_variants v ON v.id=s.variant_id
		  WHERE `+strings.Join(where, " AND ")+` ORDER BY s.updated_at DESC`, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*VariantSource
	for rows.Next() {
		source, err := scanVariantSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	return out, rows.Err()
}

func dbVariantSourceUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*VariantSource, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	allowed := map[string]string{
		"provider_sku": "provider_sku", "unit_cost_cents": "unit_cost_cents",
		"currency": "currency", "availability": "availability", "available_quantity": "available_quantity",
	}
	var sets []string
	var values []any
	for key, column := range allowed {
		value, ok := patch[key]
		if !ok {
			continue
		}
		sets = append(sets, column+"=?")
		values = append(values, value)
	}
	if len(sets) == 0 {
		return dbVariantSourceByID(db, pid, id)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	values = append(values, pid, id)
	if _, err := db.Exec(
		`UPDATE commerce_variant_sources SET `+strings.Join(sets, ",")+` WHERE project_id=? AND id=?`, values...); err != nil {
		return nil, err
	}
	return dbVariantSourceByID(db, pid, id)
}

func dbVariantSourceByID(db *sql.DB, pid string, id int64) (*VariantSource, error) {
	return scanVariantSource(db.QueryRow(variantSourceSelect()+` WHERE project_id=? AND id=?`, pid, id))
}

func variantSourceSelect() string {
	return `SELECT id, store_id, variant_id, connection_id, provider_slug,
	        external_product_id, external_variant_id, provider_sku,
	        unit_cost_cents, currency, availability, available_quantity,
	        source_json, last_synced_at, created_at, updated_at
	   FROM commerce_variant_sources`
}

func scanVariantSource(row scanner) (*VariantSource, error) {
	var source VariantSource
	var quantity sql.NullFloat64
	var raw string
	var synced sql.NullString
	err := row.Scan(
		&source.ID, &source.StoreID, &source.VariantID, &source.ConnectionID, &source.ProviderSlug,
		&source.ExternalProductID, &source.ExternalVariantID, &source.ProviderSKU,
		&source.UnitCostCents, &source.Currency, &source.Availability, &quantity,
		&raw, &synced, &source.CreatedAt, &source.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if quantity.Valid {
		source.AvailableQuantity = &quantity.Float64
	}
	if synced.Valid {
		source.LastSyncedAt = synced.String
	}
	source.Source = jsonMap(raw)
	return &source, nil
}

func (a *App) handleProviders(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := queryArgs(r)
	args["_project_id"] = pid
	switch r.Method {
	case http.MethodGet:
		result, callErr := a.toolProvidersList(ctx, args)
		httpToolResult(w, result, callErr, "providers")
	case http.MethodPatch, http.MethodPost:
		if err := readJSON(r, &args); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		args["_project_id"] = pid
		result, callErr := a.toolProviderPolicySet(ctx, args)
		httpToolResult(w, result, callErr, "provider")
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleProviderCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := map[string]any{}
	if err := readJSON(r, &args); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	args["_project_id"] = pid
	result, callErr := a.toolProviderCatalog(ctx, args)
	httpResult(w, result, callErr)
}

func (a *App) handleProviderProduct(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := map[string]any{}
	if err := readJSON(r, &args); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	args["_project_id"] = pid
	var result any
	var callErr error
	if strings.HasSuffix(r.URL.Path, "/import") {
		result, callErr = a.toolProviderProductImport(ctx, args)
	} else {
		result, callErr = a.toolProviderProductGet(ctx, args)
	}
	httpResult(w, result, callErr)
}

func (a *App) handleVariantSources(w http.ResponseWriter, r *http.Request) {
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := queryArgs(r)
	args["_project_id"] = pid
	if r.Method == http.MethodGet {
		result, callErr := a.toolVariantSourcesList(ctx, args)
		httpToolResult(w, result, callErr, "sources")
		return
	}
	if r.Method == http.MethodPost {
		if err := readJSON(r, &args); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		args["_project_id"] = pid
		result, callErr := a.toolSourcesSync(ctx, args)
		httpResult(w, result, callErr)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func httpToolResult(w http.ResponseWriter, result any, err error, key string) {
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	body, _ := result.(map[string]any)
	httpJSON(w, body[key])
}

func applyPolicySetting(input map[string]any, policy *ProviderPolicy, key string) {
	if input[key] != nil || policy == nil {
		return
	}
	if value := policy.Settings[key]; value != nil {
		input[key] = value
	}
}

func anyMap(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func anySlice(value any) []any {
	switch rows := value.(type) {
	case []any:
		return rows
	case []map[string]any:
		out := make([]any, len(rows))
		for i := range rows {
			out[i] = rows[i]
		}
		return out
	default:
		return nil
	}
}

func providerRows(value any) []any {
	if rows := anySlice(value); len(rows) > 0 {
		return rows
	}
	root := anyMap(value)
	for _, key := range []string{"result", "data", "list", "content", "products", "variants"} {
		candidate := root[key]
		if rows := anySlice(candidate); len(rows) > 0 {
			return rows
		}
		if nested := anyMap(candidate); len(nested) > 0 {
			if rows := providerRows(nested); len(rows) > 0 {
				return rows
			}
		}
	}
	return nil
}

func firstObject(value any) map[string]any {
	if m := anyMap(value); len(m) > 0 {
		for _, key := range []string{"result", "data"} {
			if nested := anyMap(m[key]); len(nested) > 0 {
				return nested
			}
		}
		return m
	}
	rows := providerRows(value)
	if len(rows) > 0 {
		return anyMap(rows[0])
	}
	return map[string]any{}
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		value := m[key]
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case json.Number:
			return v.String()
		}
	}
	return ""
}

func stringField(m map[string]any, key string) string { return firstString(m, key) }

func numberValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func firstNumber(m map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := numberValue(m[key]); ok {
			return value
		}
	}
	return -1
}

func numberCents(value any, alreadyCents bool) int64 {
	number, ok := numberValue(value)
	if !ok || number <= 0 {
		return 0
	}
	if alreadyCents {
		return int64(math.Round(number))
	}
	return int64(math.Round(number * 100))
}

func firstMoneyCents(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if cents := numberCents(m[key], false); cents > 0 {
			return cents
		}
	}
	return 0
}

func stringSet(value any) map[string]bool {
	out := map[string]bool{}
	for _, item := range anySlice(value) {
		key := ""
		switch v := item.(type) {
		case string:
			key = v
		case float64:
			key = strconv.FormatFloat(v, 'f', -1, 64)
		}
		if key != "" {
			out[key] = true
		}
	}
	return out
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func providerNow() string { return time.Now().UTC().Format(time.RFC3339) }
