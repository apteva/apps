package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const checkoutQuoteTTL = 30 * time.Minute

type CheckoutShippingOption struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	AmountCents      int64            `json:"amount_cents"`
	Currency         string           `json:"currency"`
	EstimatedDaysMin int64            `json:"estimated_days_min,omitempty"`
	EstimatedDaysMax int64            `json:"estimated_days_max,omitempty"`
	Components       []ShippingOption `json:"components,omitempty"`
}

func (a *App) toolStoreSettingsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"store_id": store.ID, "settings": commerceOwnedSettings(store)}, nil
}

func (a *App) toolStoreSettingsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	patch := mapArg(args, "patch")
	if len(patch) == 0 {
		return nil, errors.New("patch required")
	}
	allowed := map[string]bool{"shipping": true, "tax": true, "markets": true, "payments": true}
	for key, value := range patch {
		if !allowed[key] {
			return nil, fmt.Errorf("unsupported Commerce setting %q", key)
		}
		if _, ok := value.(map[string]any); !ok {
			return nil, fmt.Errorf("%s must be an object", key)
		}
	}
	if tax := mapArg(patch, "tax"); len(tax) > 0 {
		if rate := intArg(tax, "default_rate_bps"); rate < 0 || rate > 10000 {
			return nil, errors.New("tax.default_rate_bps must be between 0 and 10000")
		}
	}
	metadata := copyMap(store.Metadata)
	for key, value := range patch {
		metadata[key] = value
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE commerce_stores SET metadata_json=?, updated_at=CURRENT_TIMESTAMP
		 WHERE project_id=? AND id=?`, jsonText(metadata, "{}"), pid, store.ID); err != nil {
		return nil, err
	}
	updated, err := dbStoreGetByID(ctx.AppDB(), pid, store.ID)
	if err == nil {
		ctx.Emit("commerce.store.settings_updated", map[string]any{"store_id": store.ID})
	}
	return map[string]any{"store": updated, "settings": commerceOwnedSettings(updated)}, err
}

func commerceOwnedSettings(store *Store) map[string]any {
	if store == nil {
		return map[string]any{}
	}
	return map[string]any{
		"shipping": defaultMap(mapArg(store.Metadata, "shipping"), defaultShippingSettings(store)),
		"tax":      defaultMap(mapArg(store.Metadata, "tax"), defaultTaxSettings()),
		"markets":  defaultMap(mapArg(store.Metadata, "markets"), map[string]any{"enabled": []any{}}),
		"payments": defaultMap(mapArg(store.Metadata, "payments"), map[string]any{}),
	}
}

func defaultMap(value, fallback map[string]any) map[string]any {
	if len(value) == 0 {
		return fallback
	}
	return value
}

func defaultShippingSettings(store *Store) map[string]any {
	currency := "USD"
	if store != nil && store.DefaultCurrency != "" {
		currency = store.DefaultCurrency
	}
	return map[string]any{"zones": []any{map[string]any{
		"id": "default", "name": "Everywhere", "countries": []any{"*"},
		"methods": []any{map[string]any{
			"id": "standard", "name": "Standard shipping", "amount_cents": int64(0),
			"currency": currency, "estimated_days_min": int64(3), "estimated_days_max": int64(7),
		}},
	}}}
}

func defaultTaxSettings() map[string]any {
	return map[string]any{
		"default_rate_bps": int64(0), "country_rates": map[string]any{},
		"prices_include_tax": false, "shipping_taxable": false,
	}
}

func (a *App) toolCheckoutQuote(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	cart, err := resolveCart(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if cart.Status != "open" {
		quote := mapArg(cart.Metadata, "checkout_quote")
		if len(quote) == 0 {
			return nil, fmt.Errorf("cart is %s and has no frozen quote", cart.Status)
		}
		return map[string]any{"cart": cart, "quote": quote}, nil
	}
	store, err := dbStoreGetByID(ctx.AppDB(), pid, cart.StoreID)
	if err != nil || store == nil {
		return nil, firstErr(err, errors.New("store not found"))
	}
	address := mapArg(args, "shipping_address")
	previous := mapArg(cart.Metadata, "checkout_quote")
	if len(address) == 0 {
		address = mapArg(previous, "shipping_address")
	}
	if err := validateCheckoutMarket(store, address); err != nil {
		return nil, err
	}
	options, err := a.checkoutShippingOptions(ctx, pid, store, cart, address)
	if err != nil {
		return nil, err
	}
	selectedID := strings.TrimSpace(strArg(args, "shipping_option_id"))
	if selectedID == "" {
		selectedID = strArg(mapArg(previous, "selected_shipping"), "id")
	}
	selected, err := selectShippingOption(options, selectedID)
	if err != nil {
		return nil, err
	}
	discount, discountRequest, err := a.checkoutDiscountQuote(ctx, pid, cart, selected.AmountCents, args, previous)
	if err != nil {
		return nil, err
	}
	discountCents := intArg(discount, "discount_cents")
	merchandiseDiscount := intArg(discount, "merchandise_discount_cents")
	shippingDiscount := intArg(discount, "shipping_discount_cents")
	if merchandiseDiscount == 0 && shippingDiscount == 0 {
		merchandiseDiscount = discountCents
	}
	tax := checkoutTaxQuote(store, cart, selected.AmountCents, merchandiseDiscount, shippingDiscount, address)
	addedTax := intArg(tax, "added_tax_cents")
	total := cart.SubtotalCents - discountCents + selected.AmountCents + addedTax
	if total < 0 {
		return nil, errors.New("quote produced a negative total")
	}
	now := time.Now().UTC()
	quote := map[string]any{
		"version":           "2026-07-26",
		"shipping_address":  address,
		"shipping_options":  options,
		"selected_shipping": selected,
		"discount":          discount,
		"discount_request":  discountRequest,
		"tax":               tax,
		"subtotal_cents":    cart.SubtotalCents,
		"discount_cents":    discountCents,
		"shipping_cents":    selected.AmountCents,
		"tax_cents":         addedTax,
		"total_cents":       total,
		"currency":          cart.Currency,
		"quoted_at":         now.Format(time.RFC3339),
		"expires_at":        now.Add(checkoutQuoteTTL).Format(time.RFC3339),
	}
	if err := dbCartApplyCheckoutQuote(ctx.AppDB(), pid, cart.ID, quote); err != nil {
		return nil, err
	}
	cart, err = dbCartGet(ctx.AppDB(), pid, cart.ID, true)
	if err == nil {
		ctx.Emit("commerce.checkout.quoted", map[string]any{
			"store_id": store.ID, "cart_id": cart.ID, "total_cents": cart.TotalCents, "currency": cart.Currency,
		})
	}
	return map[string]any{"cart": cart, "quote": quote}, err
}

func validateCheckoutMarket(store *Store, address map[string]any) error {
	markets := mapArg(commerceOwnedSettings(store), "markets")
	enabled := providerRows(markets["enabled"])
	if len(enabled) == 0 {
		return nil
	}
	country := strings.ToUpper(firstNonEmpty(strArg(address, "country_code"), strArg(address, "country")))
	for _, raw := range enabled {
		if strings.ToUpper(strings.TrimSpace(fmt.Sprint(raw))) == country {
			return nil
		}
	}
	return fmt.Errorf("store does not currently sell to country %q", country)
}

func (a *App) checkoutDiscountQuote(ctx *sdk.AppCtx, pid string, cart *Cart, shippingCents int64, args, previous map[string]any) (map[string]any, map[string]any, error) {
	if boolArg(args, "remove_discount") {
		return map[string]any{}, map[string]any{}, nil
	}
	code := strings.TrimSpace(strArg(args, "discount_code"))
	if code == "" && !hasKey(args, "discount_code") {
		code = strArg(mapArg(previous, "discount_request"), "code")
	}
	request := map[string]any{
		"_project_id": pid, "subtotal_cents": cart.SubtotalCents,
		"currency": cart.Currency, "context_ref": fmt.Sprintf("commerce_cart:%d", cart.ID),
	}
	request["shipping_cents"] = shippingCents
	var lines []any
	for _, item := range cart.Items {
		listing, err := dbListingGet(ctx.AppDB(), pid, item.ListingID, false)
		if err != nil {
			return nil, nil, err
		}
		line := map[string]any{
			"price_id": int64(0), "product_id": int64(0),
			"quantity":       int64(math.Round(item.Quantity)),
			"subtotal_cents": int64(math.Round(float64(item.UnitAmountCents) * item.Quantity)),
			"currency":       item.Currency,
		}
		if item.CatalogPriceID != nil {
			line["price_id"] = *item.CatalogPriceID
		}
		if listing != nil && listing.CatalogProductID != nil {
			line["product_id"] = *listing.CatalogProductID
		}
		if intArg(line, "quantity") < 1 {
			line["quantity"] = int64(1)
		}
		if intArg(line, "price_id") != 0 || intArg(line, "product_id") != 0 {
			lines = append(lines, line)
		}
	}
	if len(lines) == len(cart.Items) && len(lines) > 0 {
		request["lines"] = lines
	}
	if customerRef := strings.TrimSpace(strArg(args, "customer_ref")); customerRef != "" {
		request["customer_ref"] = customerRef
	}
	if code != "" {
		request["code"] = code
		var response map[string]any
		if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_discounts_quote", request, &response); err != nil {
			return nil, nil, fmt.Errorf("quote discount: %w", err)
		}
		quote := unwrap(response, "quote")
		if !boolArg(quote, "eligible") {
			return nil, nil, fmt.Errorf("discount code is not eligible: %s", firstNonEmpty(strArg(quote, "reason"), "not eligible"))
		}
		return quote, request, nil
	}

	var listed map[string]any
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_discounts_list", map[string]any{
		"_project_id": pid, "active": true, "limit": 100,
	}, &listed); err != nil {
		return map[string]any{}, map[string]any{}, nil
	}
	var best map[string]any
	var bestRequest map[string]any
	for _, raw := range providerRows(listed["discounts"]) {
		discount := anyMap(raw)
		metadata := anyMap(discount["metadata"])
		if !boolArg(metadata, "automatic") {
			continue
		}
		candidate := copyMap(request)
		candidate["discount_id"] = intArg(discount, "id")
		var response map[string]any
		if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_discounts_quote", candidate, &response); err != nil {
			continue
		}
		quote := unwrap(response, "quote")
		if boolArg(quote, "eligible") && intArg(quote, "discount_cents") > intArg(best, "discount_cents") {
			best, bestRequest = quote, candidate
		}
	}
	return best, bestRequest, nil
}

func (a *App) checkoutShippingOptions(ctx *sdk.AppCtx, pid string, store *Store, cart *Cart, address map[string]any) ([]CheckoutShippingOption, error) {
	var groups [][]ShippingOption
	sourceGroups, err := cartSourceGroups(ctx.AppDB(), pid, cart)
	if err != nil {
		return nil, err
	}
	sourceItemIDs := map[int64]bool{}
	connectionIDs := make([]int64, 0, len(sourceGroups))
	for connectionID, lines := range sourceGroups {
		connectionIDs = append(connectionIDs, connectionID)
		for _, line := range lines {
			if line.SaleItem != nil && line.SaleItem.VariantID != nil {
				sourceItemIDs[*line.SaleItem.VariantID] = true
			}
		}
	}
	sort.Slice(connectionIDs, func(i, j int) bool { return connectionIDs[i] < connectionIDs[j] })
	for _, connectionID := range connectionIDs {
		bound := providerBinding(ctx, connectionID)
		if bound == nil {
			return nil, fmt.Errorf("supplier connection %d is no longer bound", connectionID)
		}
		policy, err := dbProviderPolicyGet(ctx.AppDB(), pid, cart.StoreID, connectionID)
		if err != nil {
			return nil, err
		}
		options, err := quoteProviderShipping(ctx, bound, policy, address, sourceGroups[connectionID], cart.Currency)
		if err != nil {
			return nil, err
		}
		if len(options) == 0 {
			return nil, fmt.Errorf("%s returned no shipping options", bound.AppSlug)
		}
		groups = append(groups, options)
	}

	needsStoreRate := len(sourceGroups) == 0
	for _, item := range cart.Items {
		variant, err := dbVariantGet(ctx.AppDB(), pid, item.VariantID)
		if err != nil {
			return nil, err
		}
		if variant != nil && variant.RequiresShipping && !sourceItemIDs[item.VariantID] {
			needsStoreRate = true
		}
	}
	if needsStoreRate {
		methods, err := storeShippingMethods(store, cart, address)
		if err != nil {
			return nil, err
		}
		groups = append(groups, methods)
	}
	if len(groups) == 0 {
		return []CheckoutShippingOption{{ID: "not-required", Name: "No shipping required", Currency: cart.Currency}}, nil
	}
	return combineShippingGroups(groups, cart.Currency), nil
}

func storeShippingMethods(store *Store, cart *Cart, address map[string]any) ([]ShippingOption, error) {
	shipping := mapArg(commerceOwnedSettings(store), "shipping")
	country := strings.ToUpper(firstNonEmpty(strArg(address, "country_code"), strArg(address, "country")))
	var out []ShippingOption
	for _, rawZone := range providerRows(shipping["zones"]) {
		zone := anyMap(rawZone)
		if !shippingZoneMatches(providerRows(zone["countries"]), country) {
			continue
		}
		for _, rawMethod := range providerRows(zone["methods"]) {
			method := anyMap(rawMethod)
			minimum := intArg(method, "minimum_subtotal_cents")
			maximum := intArg(method, "maximum_subtotal_cents")
			if cart.SubtotalCents < minimum || maximum > 0 && cart.SubtotalCents > maximum {
				continue
			}
			currency := strings.ToUpper(firstNonEmpty(strArg(method, "currency"), cart.Currency))
			if currency != cart.Currency {
				continue
			}
			amount := intArg(method, "amount_cents")
			if amount < 0 {
				return nil, errors.New("shipping method amount cannot be negative")
			}
			out = append(out, ShippingOption{
				ID:          firstNonEmpty(strArg(method, "id"), slugify(strArg(method, "name"))),
				Name:        firstNonEmpty(strArg(method, "name"), "Standard shipping"),
				AmountCents: amount, Currency: currency, Provider: "commerce",
				Raw: copyMap(method),
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no shipping method serves country %q", country)
	}
	return out, nil
}

func shippingZoneMatches(rawCountries []any, country string) bool {
	for _, raw := range rawCountries {
		value := strings.ToUpper(strings.TrimSpace(fmt.Sprint(raw)))
		if value == "*" || value != "" && value == country {
			return true
		}
	}
	return false
}

func combineShippingGroups(groups [][]ShippingOption, currency string) []CheckoutShippingOption {
	out := []CheckoutShippingOption{{ID: "", Currency: currency}}
	for _, group := range groups {
		var next []CheckoutShippingOption
		for _, base := range out {
			for _, component := range group {
				if component.Currency != "" && strings.ToUpper(component.Currency) != currency {
					continue
				}
				components := append(append([]ShippingOption{}, base.Components...), component)
				ids, names := []string{}, []string{}
				var minDays, maxDays int64
				for _, item := range components {
					ids = append(ids, item.Provider+":"+item.ID)
					names = append(names, item.Name)
					minDays = maxInt64Quote(minDays, intArg(item.Raw, "estimated_days_min"))
					maxDays = maxInt64Quote(maxDays, intArg(item.Raw, "estimated_days_max"))
				}
				next = append(next, CheckoutShippingOption{
					ID: strings.Join(ids, "+"), Name: strings.Join(names, " + "),
					AmountCents: base.AmountCents + component.AmountCents, Currency: currency,
					EstimatedDaysMin: minDays, EstimatedDaysMax: maxDays, Components: components,
				})
				if len(next) >= 32 {
					break
				}
			}
			if len(next) >= 32 {
				break
			}
		}
		out = next
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].AmountCents == out[j].AmountCents {
			return out[i].Name < out[j].Name
		}
		return out[i].AmountCents < out[j].AmountCents
	})
	return out
}

func selectShippingOption(options []CheckoutShippingOption, id string) (CheckoutShippingOption, error) {
	if len(options) == 0 {
		return CheckoutShippingOption{}, errors.New("no shipping options available")
	}
	if id == "" {
		return options[0], nil
	}
	for _, option := range options {
		if option.ID == id {
			return option, nil
		}
	}
	return CheckoutShippingOption{}, errors.New("selected shipping option is no longer available")
}

func checkoutTaxQuote(store *Store, cart *Cart, shippingCents, merchandiseDiscountCents, shippingDiscountCents int64, address map[string]any) map[string]any {
	settings := mapArg(commerceOwnedSettings(store), "tax")
	country := strings.ToUpper(firstNonEmpty(strArg(address, "country_code"), strArg(address, "country")))
	rate := intArg(settings, "default_rate_bps")
	if countryRates := mapArg(settings, "country_rates"); country != "" && hasKey(countryRates, country) {
		rate = intArg(countryRates, country)
	}
	if rate < 0 {
		rate = 0
	}
	if rate > 10000 {
		rate = 10000
	}
	taxable := cart.SubtotalCents - merchandiseDiscountCents
	if taxable < 0 {
		taxable = 0
	}
	if boolArg(settings, "shipping_taxable") {
		taxable += shippingCents - shippingDiscountCents
	}
	pricesIncludeTax := boolArg(settings, "prices_include_tax")
	var included, added int64
	if pricesIncludeTax && rate > 0 {
		included = (taxable*rate + 10000 + rate/2) / (10000 + rate)
	} else {
		added = (taxable*rate + 5000) / 10000
	}
	return map[string]any{
		"country_code": country, "rate_bps": rate, "taxable_cents": taxable,
		"prices_include_tax": pricesIncludeTax, "shipping_taxable": boolArg(settings, "shipping_taxable"),
		"included_tax_cents": included, "added_tax_cents": added,
	}
}

func dbCartApplyCheckoutQuote(db *sql.DB, pid string, cartID int64, quote map[string]any) error {
	var metadataText string
	if err := db.QueryRow(
		`SELECT metadata_json FROM commerce_carts WHERE project_id=? AND id=? AND status='open'`,
		pid, cartID).Scan(&metadataText); err != nil {
		return err
	}
	metadata := jsonMap(metadataText)
	metadata["checkout_quote"] = quote
	_, err := db.Exec(
		`UPDATE commerce_carts
		    SET discount_cents=?, tax_cents=?, shipping_cents=?, total_cents=?,
		        metadata_json=?, updated_at=CURRENT_TIMESTAMP
		  WHERE project_id=? AND id=? AND status='open'`,
		intArg(quote, "discount_cents"), intArg(quote, "tax_cents"),
		intArg(quote, "shipping_cents"), intArg(quote, "total_cents"),
		jsonText(metadata, "{}"), pid, cartID)
	return err
}

func (a *App) toolCheckoutBootstrap(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	store, err := resolveStore(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(strArg(args, "session_token"))
	if token == "" {
		return nil, errors.New("session_token required")
	}
	cart, err := dbCartGetBySession(ctx.AppDB(), pid, store.ID, token)
	if err != nil {
		return nil, err
	}
	if cart == nil {
		return map[string]any{"store": store, "status": "not_found"}, nil
	}
	out := map[string]any{
		"store": store, "cart": cart, "quote": mapArg(cart.Metadata, "checkout_quote"), "status": cart.Status,
	}
	checkout, err := dbCheckoutGetByCart(ctx.AppDB(), pid, cart.ID)
	if err != nil || checkout == nil {
		return out, err
	}
	out["checkout"] = checkout
	out["status"] = checkout.Status
	sale, err := dbSaleGetByCheckout(ctx.AppDB(), pid, checkout.ID)
	if err != nil {
		return nil, err
	}
	if sale != nil {
		out["sale"] = sale
		out["status"] = sale.Status
	}
	if boolArg(args, "include_payment") && checkout.Status == "awaiting_payment" {
		resumedSale, resumedCheckout, payment, err := a.checkoutPay(ctx, map[string]any{
			"_project_id": pid, "checkout_id": checkout.ID,
		})
		if err != nil {
			return nil, err
		}
		out["checkout"], out["sale"], out["payment"] = resumedCheckout, resumedSale, payment
	}
	return out, nil
}

func maxInt64Quote(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
