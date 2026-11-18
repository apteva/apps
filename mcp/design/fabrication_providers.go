package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ---- JSON helpers ---------------------------------------------------------

func decodeAny(raw json.RawMessage) any {
	var v any
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

// pick walks a dotted path through maps; the first path that resolves wins.
func pick(v any, paths ...string) any {
	for _, path := range paths {
		cur := v
		ok := true
		for _, key := range strings.Split(path, ".") {
			m, isMap := cur.(map[string]any)
			if !isMap {
				ok = false
				break
			}
			cur, ok = m[key]
			if !ok {
				break
			}
		}
		if ok && cur != nil {
			return cur
		}
	}
	return nil
}

func pickString(v any, paths ...string) string {
	switch x := pick(v, paths...).(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	return ""
}

func pickNumber(v any, paths ...string) (float64, bool) {
	switch x := pick(v, paths...).(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func pickList(v any, paths ...string) []any {
	if list, ok := pick(v, paths...).([]any); ok {
		return list
	}
	if list, ok := v.([]any); ok && len(paths) == 0 {
		return list
	}
	return nil
}

func refJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func settingString(settings map[string]any, key, fallback string) string {
	if settings != nil {
		if s, ok := settings[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return fallback
}

func leadDays(text string) int {
	// "3-5" -> 5, "4" -> 4
	parts := strings.Split(strings.TrimSpace(text), "-")
	n, _ := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1]))
	return n
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// pollInterval is shortened by tests.
var pollInterval = 2 * time.Second

const pollAttempts = 25

// ---- Craftcloud -----------------------------------------------------------
//
// Upload each part, request prices for all parts at once, poll until every
// print shop answered, then group quotes by shop and material so one option
// prints every part. Ordering creates a cart and an unpaid order; Craftcloud
// takes payment through its own checkout (or invoice for approved accounts),
// so a person always completes payment.

type craftcloudProvider struct{}

func (craftcloudProvider) Caps() ProviderCaps {
	return ProviderCaps{Slug: "craftcloud", Name: "Craftcloud", Processes: []string{"fdm", "sla", "sls", "mjf", "metal"},
		AutoPay: false, Cancel: false, Settings: []string{"payment (none | invoice)", "test_orders (true | false)"},
		PaymentNote: "Craftcloud creates an unpaid order; a person pays it in Craftcloud unless the account is approved for invoice billing."}
}

func (craftcloudProvider) Quote(ctx context.Context, call ToolCaller, req QuoteRequest) ([]QuoteOption, error) {
	models := make([]map[string]any, 0, len(req.Parts))
	modelPart := map[string]PartFile{}
	for _, part := range req.Parts {
		raw, status, err := call(ctx, "upload_model", map[string]any{
			"file": base64.StdEncoding.EncodeToString(part.Body), "filename": part.Filename, "unit": "mm",
		})
		if err != nil {
			if craftcloudNeedsPartnerAccess(status) {
				return nil, fmt.Errorf("%w (upload %s: %v)", errCraftcloudPartnerAccess, part.PartID, err)
			}
			return nil, fmt.Errorf("upload %s: %w", part.PartID, err)
		}
		body := decodeAny(raw)
		modelID := pickString(body, "modelId", "id", "model.modelId")
		if modelID == "" {
			return nil, fmt.Errorf("upload %s: Craftcloud returned no model id", part.PartID)
		}
		for attempt := 0; attempt < pollAttempts; attempt++ {
			_, status, err := call(ctx, "get_model", map[string]any{"modelId": modelID})
			if err != nil {
				return nil, fmt.Errorf("model %s: %w", part.PartID, err)
			}
			if status != 206 {
				break
			}
			if err := sleepCtx(ctx, pollInterval); err != nil {
				return nil, err
			}
		}
		models = append(models, map[string]any{"modelId": modelID, "quantity": part.Quantity})
		modelPart[modelID] = part
	}
	raw, _, err := call(ctx, "create_price_request", map[string]any{
		"currency": req.Currency, "countryCode": req.Country, "models": models, "topMaterialConfigsOnly": true,
	})
	if err != nil {
		return nil, fmt.Errorf("price request: %w", err)
	}
	priceID := pickString(decodeAny(raw), "priceId")
	if priceID == "" {
		return nil, errors.New("price request returned no priceId")
	}
	var prices any
	for attempt := 0; attempt < pollAttempts; attempt++ {
		raw, _, err := call(ctx, "get_price", map[string]any{"priceId": priceID})
		if err != nil {
			return nil, fmt.Errorf("prices: %w", err)
		}
		prices = decodeAny(raw)
		if done, _ := pick(prices, "allComplete").(bool); done {
			break
		}
		if err := sleepCtx(ctx, pollInterval); err != nil {
			return nil, err
		}
	}
	return craftcloudOptions(prices, req, modelPart, priceID), nil
}

// errCraftcloudPartnerAccess explains the one Craftcloud failure users hit
// first: the public v5 API has no model upload, so quoting needs the partner
// model API URL (and key) on the Craftcloud connection.
var errCraftcloudPartnerAccess = errors.New("Craftcloud quotes need partner API access: its public API cannot accept model uploads. " +
	"Ask Craftcloud for partner model API access, then set the partner model API URL and key on the Craftcloud connection")

// craftcloudNeedsPartnerAccess reports upload failures that mean the partner
// model API is missing or not authorized: no HTTP response (unset URL),
// 401/403 (blocked or wrong key) and 404 (public host without upload).
func craftcloudNeedsPartnerAccess(status int) bool {
	return status == 0 || status == 401 || status == 403 || status == 404
}

func craftcloudOptions(prices any, req QuoteRequest, modelPart map[string]PartFile, priceID string) []QuoteOption {
	type key struct{ vendor, material string }
	groups := map[key][]map[string]any{}
	for _, item := range pickList(prices, "quotes") {
		q, ok := item.(map[string]any)
		if !ok {
			continue
		}
		k := key{pickString(q, "vendorId"), pickString(q, "materialConfigId")}
		groups[k] = append(groups[k], q)
	}
	cheapestShip := map[string]map[string]any{}
	for _, item := range pickList(prices, "shippings") {
		s, ok := item.(map[string]any)
		if !ok {
			continue
		}
		vendor := pickString(s, "vendorId")
		price, _ := pickNumber(s, "priceInclVat", "price")
		if cur, ok := cheapestShip[vendor]; !ok || price < must(pickNumber(cur, "priceInclVat", "price")) {
			cheapestShip[vendor] = s
		}
	}
	expires := ""
	if ms, ok := pickNumber(prices, "expiresAt"); ok && ms > 0 {
		expires = time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339)
	}
	var options []QuoteOption
	for k, quotes := range groups {
		byModel := map[string]map[string]any{}
		for _, q := range quotes {
			model := pickString(q, "modelId")
			if cur, ok := byModel[model]; !ok || must(pickNumber(q, "priceInclVat", "price")) < must(pickNumber(cur, "priceInclVat", "price")) {
				byModel[model] = q
			}
		}
		if len(byModel) != len(modelPart) {
			continue // this shop cannot print every part in this material
		}
		ship, ok := cheapestShip[k.vendor]
		if !ok {
			continue
		}
		opt := QuoteOption{Provider: "craftcloud", Vendor: k.vendor, Material: k.material, MaterialRef: k.material, Currency: req.Currency, ExpiresAt: expires}
		quoteIDs := []string{}
		lead := 0
		for model, q := range byModel {
			part := modelPart[model]
			total, _ := pickNumber(q, "priceInclVat", "price")
			qty := part.Quantity
			opt.Lines = append(opt.Lines, QuoteLine{PartID: part.PartID, SHA256: part.SHA256, Quantity: qty, UnitPrice: round2(total / float64(max(qty, 1))), LineTotal: round2(total), Ref: pickString(q, "quoteId")})
			opt.Subtotal += total
			quoteIDs = append(quoteIDs, pickString(q, "quoteId"))
			if days, ok := pickNumber(q, "productionTimeSlow", "productionTimeFast"); ok && int(days) > lead {
				lead = int(days)
			}
		}
		opt.Shipping, _ = pickNumber(ship, "priceInclVat", "price")
		opt.ShippingName = strings.TrimSpace(pickString(ship, "carrier") + " " + pickString(ship, "name"))
		opt.LeadDays = lead + leadDays(pickString(ship, "deliveryTime"))
		opt.Subtotal = round2(opt.Subtotal)
		opt.Shipping = round2(opt.Shipping)
		opt.Total = round2(opt.Subtotal + opt.Shipping)
		opt.Process = "3d-print"
		opt.Ref = refJSON(map[string]any{"price_id": priceID, "quote_ids": quoteIDs, "shipping_id": pickString(ship, "shippingId")})
		options = append(options, opt)
	}
	return options
}

func must(v float64, _ bool) float64 { return v }

type craftcloudRef struct {
	PriceID    string   `json:"price_id"`
	QuoteIDs   []string `json:"quote_ids"`
	ShippingID string   `json:"shipping_id"`
	CartID     string   `json:"cart_id,omitempty"`
}

func (craftcloudProvider) PrepareOrder(ctx context.Context, call ToolCaller, opt QuoteOption, _ Address, _ map[string]any) (Draft, error) {
	var ref craftcloudRef
	if err := json.Unmarshal(opt.Ref, &ref); err != nil || len(ref.QuoteIDs) == 0 || ref.ShippingID == "" {
		return Draft{}, errors.New("quote option has no Craftcloud quote or shipping ids")
	}
	quotes := make([]map[string]any, 0, len(ref.QuoteIDs))
	for _, id := range ref.QuoteIDs {
		quotes = append(quotes, map[string]any{"id": id})
	}
	raw, _, err := call(ctx, "create_cart", map[string]any{"shippingIds": []string{ref.ShippingID}, "currency": opt.Currency, "quotes": quotes})
	if err != nil {
		return Draft{}, fmt.Errorf("create cart: %w", err)
	}
	cart := decodeAny(raw)
	ref.CartID = pickString(cart, "cartId")
	if ref.CartID == "" {
		return Draft{}, errors.New("Craftcloud returned no cartId")
	}
	total, ok := pickNumber(cart, "amounts.total.totalGrossPrice", "amounts.totalGrossPrice", "amounts.total.totalNetPrice")
	if !ok {
		total = opt.Total
	}
	return Draft{Total: round2(total), Currency: opt.Currency, Ref: refJSON(ref), Note: "Cart created; nothing charged."}, nil
}

func (craftcloudProvider) Submit(ctx context.Context, call ToolCaller, opt QuoteOption, draft Draft, addr Address, settings map[string]any, _ string) (OrderRef, error) {
	var ref craftcloudRef
	if err := json.Unmarshal(draft.Ref, &ref); err != nil || ref.CartID == "" {
		return OrderRef{}, errors.New("draft has no Craftcloud cart")
	}
	if addr.Email == "" || addr.Phone == "" {
		return OrderRef{}, errors.New("Craftcloud orders need an email and phone number on the ship-to address")
	}
	shipping := map[string]any{"firstName": addr.FirstName, "lastName": addr.LastName, "address": addr.Line1, "addressLine2": addr.Line2,
		"city": addr.City, "zipCode": addr.Zip, "stateCode": addr.State, "countryCode": addr.Country, "companyName": addr.Company, "phoneNumber": addr.Phone}
	billing := map[string]any{"firstName": addr.FirstName, "lastName": addr.LastName, "address": addr.Line1, "addressLine2": addr.Line2,
		"city": addr.City, "zipCode": addr.Zip, "stateCode": addr.State, "countryCode": addr.Country, "companyName": addr.Company,
		"phoneNumber": addr.Phone, "isCompany": addr.Company != ""}
	raw, _, err := call(ctx, "create_order", map[string]any{"cartId": ref.CartID, "user": map[string]any{"emailAddress": addr.Email, "shipping": shipping, "billing": billing}})
	if err != nil {
		return OrderRef{}, fmt.Errorf("create order: %w", err)
	}
	order := decodeAny(raw)
	orderID := pickString(order, "orderId")
	if orderID == "" {
		return OrderRef{}, errors.New("Craftcloud returned no orderId")
	}
	result := OrderRef{ProviderOrderID: orderID, Status: orderAwaitingPayment,
		Note: "Order " + pickString(order, "orderNumber") + " created unpaid. Pay it in Craftcloud to start production."}
	if settingString(settings, "payment", "none") == "invoice" {
		testOrder := settingString(settings, "test_orders", "false") == "true"
		if _, _, err := call(ctx, "create_invoice_payment", map[string]any{"orderId": orderID, "isTestOrder": testOrder}); err != nil {
			result.Note += " Invoice payment request failed: " + err.Error()
		} else {
			result.Status = orderSubmitted
			result.Note = "Order " + pickString(order, "orderNumber") + " placed on invoice."
		}
	}
	return result, nil
}

func (craftcloudProvider) Status(ctx context.Context, call ToolCaller, id string) (OrderStatus, error) {
	raw, _, err := call(ctx, "get_order_status", map[string]any{"orderId": id})
	if err != nil {
		return OrderStatus{}, err
	}
	body := decodeAny(raw)
	rank := map[string]int{"ordered": 1, "in_production": 2, "shipped": 3, "received": 4, "blocked": 0, "cancelled": -1}
	latest, best := "", -2
	for _, part := range pickList(body, "status") {
		steps := pickList(part, "orderStatus")
		if len(steps) == 0 {
			continue
		}
		t := pickString(steps[len(steps)-1], "type")
		if r, ok := rank[t]; ok && (best == -2 || r < best) {
			best, latest = r, t // the slowest part decides
		}
	}
	mapped := map[string]string{"ordered": orderSubmitted, "in_production": orderInProduction, "shipped": orderShipped, "received": orderDelivered, "cancelled": orderCancelled, "blocked": orderSubmitted}
	status := mapped[latest]
	if status == "" {
		status = orderAwaitingPayment
	}
	return OrderStatus{Status: status, ProviderStatus: latest}, nil
}

func (craftcloudProvider) Cancel(context.Context, ToolCaller, string) error { return errNotSupported }

// ---- Slant 3D -------------------------------------------------------------

type slant3dProvider struct{}

func (slant3dProvider) Caps() ProviderCaps {
	return ProviderCaps{Slug: "slant3d", Name: "Slant 3D", Processes: []string{"fdm"}, AutoPay: true, Cancel: true,
		Settings:    []string{"platform_id (required)", "filament_id (default PLA black)"},
		PaymentNote: "Slant 3D charges the payment method saved on the Slant 3D account when the order is processed."}
}

func (slant3dProvider) Quote(ctx context.Context, call ToolCaller, req QuoteRequest) ([]QuoteOption, error) {
	platform := settingString(req.Settings, "platform_id", "")
	if platform == "" {
		return nil, errors.New("set provider_settings.slant3d.platform_id in Fabrication settings")
	}
	filament := settingString(req.Settings, "filament_id", "")
	opt := QuoteOption{Provider: "slant3d", Process: "fdm", Material: ternary(filament == "", "PLA (Slant 3D default)", filament), MaterialRef: filament, Currency: "USD"}
	files := []map[string]any{}
	for _, part := range req.Parts {
		raw, _, err := call(ctx, "create_direct_upload", map[string]any{"name": part.Filename, "platformId": platform})
		if err != nil {
			return nil, fmt.Errorf("upload slot %s: %w", part.PartID, err)
		}
		slot := decodeAny(raw)
		uploadURL := pickString(slot, "uploadUrl", "data.uploadUrl", "presignedUrl")
		placeholder := pick(slot, "filePlaceholder", "data.filePlaceholder")
		if uploadURL == "" || placeholder == nil {
			return nil, fmt.Errorf("upload slot %s: missing uploadUrl or filePlaceholder", part.PartID)
		}
		if _, _, err := call(ctx, "upload_presigned_file", map[string]any{"uploadUrl": uploadURL, "file": base64.StdEncoding.EncodeToString(part.Body)}); err != nil {
			return nil, fmt.Errorf("upload %s: %w", part.PartID, err)
		}
		raw, _, err = call(ctx, "confirm_direct_upload", map[string]any{"filePlaceholder": placeholder})
		if err != nil {
			return nil, fmt.Errorf("confirm %s: %w", part.PartID, err)
		}
		fileID := pickString(decodeAny(raw), "publicFileServiceId", "data.publicFileServiceId", "file.publicFileServiceId")
		if fileID == "" {
			fileID = pickString(placeholder, "publicFileServiceId")
		}
		options := map[string]any{"quantity": part.Quantity}
		if filament != "" {
			options["filamentId"] = filament
		}
		raw, _, err = call(ctx, "estimate_file_price", map[string]any{"publicFileServiceId": fileID, "options": options})
		if err != nil {
			return nil, fmt.Errorf("estimate %s: %w", part.PartID, err)
		}
		price, ok := pickNumber(decodeAny(raw), "totalPrice", "price", "data.totalPrice", "data.price", "estimate.total")
		if !ok {
			return nil, fmt.Errorf("estimate %s: no price in response", part.PartID)
		}
		opt.Lines = append(opt.Lines, QuoteLine{PartID: part.PartID, SHA256: part.SHA256, Quantity: part.Quantity, UnitPrice: round2(price / float64(max(part.Quantity, 1))), LineTotal: round2(price), Ref: fileID})
		opt.Subtotal += price
		files = append(files, map[string]any{"publicFileServiceId": fileID, "quantity": part.Quantity})
	}
	opt.Subtotal = round2(opt.Subtotal)
	opt.Total = opt.Subtotal
	opt.ShippingName = "priced on the draft order"
	opt.Ref = refJSON(map[string]any{"platform_id": platform, "filament_id": filament, "files": files})
	return []QuoteOption{opt}, nil
}

func (slant3dProvider) PrepareOrder(ctx context.Context, call ToolCaller, opt QuoteOption, addr Address, _ map[string]any) (Draft, error) {
	var ref struct {
		PlatformID string           `json:"platform_id"`
		FilamentID string           `json:"filament_id"`
		Files      []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(opt.Ref, &ref); err != nil || ref.PlatformID == "" {
		return Draft{}, errors.New("quote option has no Slant 3D platform")
	}
	items := []map[string]any{}
	for _, f := range ref.Files {
		item := map[string]any{"type": "PRINT", "publicFileServiceId": f["publicFileServiceId"], "quantity": f["quantity"]}
		if ref.FilamentID != "" {
			item["filamentId"] = ref.FilamentID
		}
		items = append(items, item)
	}
	raw, _, err := call(ctx, "create_draft_order", map[string]any{"platformId": ref.PlatformID, "items": items,
		"customer": map[string]any{"details": map[string]any{"email": addr.Email, "address": map[string]any{
			"name": addr.FirstName + " " + addr.LastName, "line1": addr.Line1, "line2": addr.Line2, "city": addr.City, "state": addr.State, "zip": addr.Zip, "country": addr.Country}}}})
	if err != nil {
		return Draft{}, fmt.Errorf("draft order: %w", err)
	}
	body := decodeAny(raw)
	orderID := pickString(body, "publicOrderId", "data.publicOrderId", "order.publicOrderId")
	if orderID == "" {
		return Draft{}, errors.New("Slant 3D returned no publicOrderId")
	}
	total, ok := pickNumber(body, "total", "data.total", "pricing.total", "data.pricing.total", "order.total")
	if !ok {
		total = opt.Total
	}
	return Draft{Total: round2(total), Currency: opt.Currency, Ref: refJSON(map[string]any{"public_order_id": orderID}), Note: "Draft order priced; nothing charged."}, nil
}

func (slant3dProvider) Submit(ctx context.Context, call ToolCaller, _ QuoteOption, draft Draft, _ Address, _ map[string]any, _ string) (OrderRef, error) {
	id := pickString(decodeAny(draft.Ref), "public_order_id")
	if id == "" {
		return OrderRef{}, errors.New("draft has no Slant 3D order id")
	}
	if _, _, err := call(ctx, "process_order", map[string]any{"publicOrderId": id}); err != nil {
		return OrderRef{}, err
	}
	return OrderRef{ProviderOrderID: id, Status: orderSubmitted, Note: "Charged to the Slant 3D account and sent to production."}, nil
}

func (slant3dProvider) Status(ctx context.Context, call ToolCaller, id string) (OrderStatus, error) {
	raw, _, err := call(ctx, "get_order", map[string]any{"publicOrderId": id})
	if err != nil {
		return OrderStatus{}, err
	}
	body := decodeAny(raw)
	ps := strings.ToLower(pickString(body, "status", "data.status", "order.status"))
	return OrderStatus{Status: mapGenericStatus(ps), ProviderStatus: ps,
		TrackingNumber: pickString(body, "trackingNumber", "data.trackingNumber", "shipment.trackingNumber"),
		TrackingURL:    pickString(body, "trackingUrl", "data.trackingUrl", "shipment.trackingUrl")}, nil
}

func (slant3dProvider) Cancel(ctx context.Context, call ToolCaller, id string) error {
	_, _, err := call(ctx, "cancel_order", map[string]any{"publicOrderId": id})
	return err
}

// ---- Shapeways ------------------------------------------------------------

type shapewaysProvider struct{}

func (shapewaysProvider) Caps() ProviderCaps {
	return ProviderCaps{Slug: "shapeways", Name: "Shapeways", Processes: []string{"sls", "mjf", "sla", "metal"}, AutoPay: true,
		Settings:    []string{"payment_method (default credit_card)", "shipping_option (default Cheapest)"},
		PaymentNote: "Shapeways charges the account's saved payment method when the order is placed."}
}

func (shapewaysProvider) Quote(ctx context.Context, call ToolCaller, req QuoteRequest) ([]QuoteOption, error) {
	type priced struct {
		part  PartFile
		model string
		mats  map[string]map[string]any
	}
	var all []priced
	for _, part := range req.Parts {
		raw, _, err := call(ctx, "upload_model", map[string]any{"file": base64.StdEncoding.EncodeToString(part.Body), "fileName": part.Filename,
			"uploadScale": 0.001, "hasRightsToModel": true, "acceptTermsAndConditions": true, "title": part.Name, "isPublic": false})
		if err != nil {
			return nil, fmt.Errorf("upload %s: %w", part.PartID, err)
		}
		modelID := pickString(decodeAny(raw), "modelId", "id")
		if modelID == "" {
			return nil, fmt.Errorf("upload %s: no modelId", part.PartID)
		}
		var model any
		for attempt := 0; attempt < pollAttempts; attempt++ {
			raw, _, err := call(ctx, "get_model", map[string]any{"modelId": atoiOr(modelID)})
			if err != nil {
				return nil, fmt.Errorf("model %s: %w", part.PartID, err)
			}
			model = decodeAny(raw)
			if shapewaysModelReady(model) {
				break
			}
			if err := sleepCtx(ctx, pollInterval); err != nil {
				return nil, err
			}
		}
		mats := map[string]map[string]any{}
		if m, ok := pick(model, "materials").(map[string]any); ok {
			for id, v := range m {
				if mm, ok := v.(map[string]any); ok {
					if printable, ok := mm["isPrintable"].(bool); ok && !printable {
						continue
					}
					mats[id] = mm
				}
			}
		}
		if !shapewaysModelReady(model) {
			return nil, fmt.Errorf("Shapeways is still processing %s (model %s); quote again in a minute", part.PartID, modelID)
		}
		if len(mats) == 0 {
			return nil, fmt.Errorf("Shapeways cannot print %s in any material (model %s)", part.PartID, modelID)
		}
		all = append(all, priced{part, modelID, mats})
	}
	if len(all) == 0 {
		return nil, nil
	}
	var options []QuoteOption
	for matID, first := range all[0].mats {
		name := pickString(first, "name", "title")
		if !materialMatches(req.Material, name, matID) {
			continue
		}
		opt := QuoteOption{Provider: "shapeways", Process: "3d-print", Material: ternary(name == "", "material "+matID, name), MaterialRef: matID, Currency: "USD", ShippingName: "chosen at order time"}
		ok := true
		models := []map[string]any{}
		for _, p := range all {
			m, has := p.mats[matID]
			if !has {
				ok = false
				break
			}
			unit, _ := pickNumber(m, "price")
			line := unit * float64(p.part.Quantity)
			opt.Lines = append(opt.Lines, QuoteLine{PartID: p.part.PartID, SHA256: p.part.SHA256, Quantity: p.part.Quantity, UnitPrice: round2(unit), LineTotal: round2(line), Ref: p.model})
			opt.Subtotal += line
			models = append(models, map[string]any{"modelId": atoiOr(p.model), "materialId": atoiOr(matID), "quantity": p.part.Quantity})
		}
		if !ok {
			continue
		}
		opt.Subtotal = round2(opt.Subtotal)
		opt.Total = opt.Subtotal
		opt.Ref = refJSON(map[string]any{"items": models})
		options = append(options, opt)
	}
	return options, nil
}

// shapewaysModelReady reports whether Shapeways finished analysing an
// uploaded model. Until then the material list can already be present with
// every material marked not printable, which would look like "no options".
func shapewaysModelReady(model any) bool {
	mats, _ := pick(model, "materials").(map[string]any)
	if len(mats) == 0 {
		return false
	}
	switch strings.ToLower(pickString(model, "printable")) {
	case "", "processing", "pending", "unknown":
		return false
	}
	return true
}

func (shapewaysProvider) PrepareOrder(_ context.Context, _ ToolCaller, opt QuoteOption, _ Address, _ map[string]any) (Draft, error) {
	return Draft{Total: opt.Total, Currency: opt.Currency, Ref: opt.Ref, Note: "Shapeways has no draft step; the order is created and charged at submit."}, nil
}

func (shapewaysProvider) Submit(ctx context.Context, call ToolCaller, _ QuoteOption, draft Draft, addr Address, settings map[string]any, _ string) (OrderRef, error) {
	items := pick(decodeAny(draft.Ref), "items")
	raw, _, err := call(ctx, "place_order", map[string]any{"firstName": addr.FirstName, "lastName": addr.LastName, "country": addr.Country, "state": addr.State,
		"city": addr.City, "address1": addr.Line1, "address2": addr.Line2, "address3": addr.Company, "zipCode": addr.Zip, "phoneNumber": addr.Phone,
		"items": items, "paymentMethod": settingString(settings, "payment_method", "credit_card"), "shippingOption": settingString(settings, "shipping_option", "Cheapest")})
	if err != nil {
		return OrderRef{}, err
	}
	id := pickString(decodeAny(raw), "orderId", "id", "order.id")
	if id == "" {
		return OrderRef{}, errors.New("Shapeways returned no order id")
	}
	return OrderRef{ProviderOrderID: id, Status: orderSubmitted, Note: "Placed and charged at Shapeways."}, nil
}

func (shapewaysProvider) Status(ctx context.Context, call ToolCaller, id string) (OrderStatus, error) {
	raw, _, err := call(ctx, "get_order", map[string]any{"orderId": atoiOr(id)})
	if err != nil {
		return OrderStatus{}, err
	}
	body := decodeAny(raw)
	ps := strings.ToLower(pickString(body, "status", "order.status", "orderStatus"))
	return OrderStatus{Status: mapGenericStatus(ps), ProviderStatus: ps, TrackingNumber: pickString(body, "trackingNumber", "shipments.0.trackingNumber")}, nil
}

func (shapewaysProvider) Cancel(context.Context, ToolCaller, string) error { return errNotSupported }

// ---- Treatstock -----------------------------------------------------------

type treatstockProvider struct{}

func (treatstockProvider) Caps() ProviderCaps {
	return ProviderCaps{Slug: "treatstock", Name: "Treatstock", Processes: []string{"fdm", "sla", "sls"}, AutoPay: false,
		Settings:    []string{"material_group", "color"},
		PaymentNote: "Treatstock orders are paid according to the API account's billing terms."}
}

func (treatstockProvider) Quote(ctx context.Context, call ToolCaller, req QuoteRequest) ([]QuoteOption, error) {
	files := []string{}
	for _, part := range req.Parts {
		for i := 0; i < part.Quantity; i++ { // Treatstock prices a pack of files; repeat copies
			files = append(files, base64.StdEncoding.EncodeToString(part.Body))
		}
	}
	raw, _, err := call(ctx, "create_printable_pack", map[string]any{"files": files, "filename": req.Parts[0].Filename, "location[country]": req.Country})
	if err != nil {
		return nil, fmt.Errorf("printable pack: %w", err)
	}
	packID := pickString(decodeAny(raw), "id", "printablePackId", "printable_pack_id")
	if packID == "" {
		return nil, errors.New("Treatstock returned no printable pack id")
	}
	input := map[string]any{"printablePackId": atoiOr(packID), "location[country]": req.Country}
	if g := settingString(req.Settings, "material_group", ""); g != "" {
		input["printerMaterialGroup"] = g
	}
	if c := settingString(req.Settings, "color", ""); c != "" {
		input["printerColor"] = c
	}
	raw, _, err = call(ctx, "list_printing_options", input)
	if err != nil {
		return nil, fmt.Errorf("printing options: %w", err)
	}
	body := decodeAny(raw)
	list := pickList(body, "items", "costs", "providers")
	if list == nil {
		list, _ = body.([]any)
	}
	var options []QuoteOption
	for _, item := range list {
		total, ok := pickNumber(item, "price", "total", "cost.total", "totalPrice")
		if !ok {
			continue
		}
		material := strings.TrimSpace(pickString(item, "materialGroup", "material", "printerMaterialGroup") + " " + pickString(item, "color", "printerColor"))
		if !materialMatches(req.Material, material, "") {
			continue
		}
		providerID := pickString(item, "providerId", "provider_id", "id")
		currency := strings.ToUpper(pickString(item, "currency"))
		if currency == "" {
			currency = "USD"
		}
		opt := QuoteOption{Provider: "treatstock", Process: "3d-print", Vendor: pickString(item, "providerName", "provider.name", "name"), Material: ternary(material == "", "provider default", material),
			Currency: currency, Total: round2(total), Subtotal: round2(total), CheckoutURL: pickString(item, "checkoutUrl", "checkout_url", "url"),
			LeadDays: leadDays(pickString(item, "deliveryTime", "productionTime")),
			Ref:      refJSON(map[string]any{"printable_pack_id": packID, "provider_id": providerID})}
		for _, part := range req.Parts {
			opt.Lines = append(opt.Lines, QuoteLine{PartID: part.PartID, SHA256: part.SHA256, Quantity: part.Quantity})
		}
		options = append(options, opt)
	}
	return options, nil
}

func (treatstockProvider) PrepareOrder(_ context.Context, _ ToolCaller, opt QuoteOption, _ Address, _ map[string]any) (Draft, error) {
	return Draft{Total: opt.Total, Currency: opt.Currency, Ref: opt.Ref, Note: "Treatstock has no draft step; the order is created at submit."}, nil
}

func (treatstockProvider) Submit(ctx context.Context, call ToolCaller, _ QuoteOption, draft Draft, addr Address, _ map[string]any, _ string) (OrderRef, error) {
	ref := decodeAny(draft.Ref)
	raw, _, err := call(ctx, "place_order", map[string]any{"printablePackId": atoiOr(pickString(ref, "printable_pack_id")), "providerId": atoiOr(pickString(ref, "provider_id")),
		"location":         map[string]any{"email": addr.Email, "company": addr.Company},
		"shippingAddress":  map[string]any{"country": addr.Country, "zip": addr.Zip, "city": addr.City, "state": addr.State, "street": strings.TrimSpace(addr.Line1 + " " + addr.Line2), "firstName": addr.FirstName, "lastName": addr.LastName},
		"modelTextureInfo": map[string]any{}})
	if err != nil {
		return OrderRef{}, err
	}
	body := decodeAny(raw)
	id := pickString(body, "orderId", "id", "order.id")
	if id == "" {
		return OrderRef{}, errors.New("Treatstock returned no order id")
	}
	return OrderRef{ProviderOrderID: id, Status: orderSubmitted, PaymentURL: pickString(body, "paymentUrl", "payment_url"), Note: "Order placed at Treatstock."}, nil
}

func (treatstockProvider) Status(context.Context, ToolCaller, string) (OrderStatus, error) {
	return OrderStatus{}, errNotSupported // the connector has no order status tool yet
}

func (treatstockProvider) Cancel(context.Context, ToolCaller, string) error { return errNotSupported }

// ---- Sculpteo -------------------------------------------------------------

type sculpteoProvider struct{}

func (sculpteoProvider) Caps() ProviderCaps {
	return ProviderCaps{Slug: "sculpteo", Name: "Sculpteo", Processes: []string{"sls", "mjf", "sla", "fdm"}, AutoPay: false,
		Settings:    []string{"materials (comma-separated Sculpteo material ids, default PA12 white)"},
		PaymentNote: "Sculpteo API orders are invoiced to the account."}
}

func (sculpteoProvider) Quote(ctx context.Context, call ToolCaller, req QuoteRequest) ([]QuoteOption, error) {
	materials := strings.Split(settingString(req.Settings, "materials", "white_plastic"), ",")
	uuids := map[string]string{}
	for _, part := range req.Parts {
		raw, _, err := call(ctx, "upload_design", map[string]any{"language": "en", "file": base64.StdEncoding.EncodeToString(part.Body), "filename": part.Filename, "name": part.Name, "unit": "mm", "share": 0})
		if err != nil {
			return nil, fmt.Errorf("upload %s: %w", part.PartID, err)
		}
		uuid := pickString(decodeAny(raw), "uuid", "design.uuid", "result.uuid")
		if uuid == "" {
			return nil, fmt.Errorf("upload %s: no uuid", part.PartID)
		}
		uuids[part.PartID] = uuid
	}
	var options []QuoteOption
	for _, mat := range materials {
		mat = strings.TrimSpace(mat)
		if mat == "" || !materialMatches(req.Material, mat, "") {
			continue
		}
		opt := QuoteOption{Provider: "sculpteo", Process: "3d-print", Material: mat, MaterialRef: mat, Currency: req.Currency, ShippingName: "invoiced with the order"}
		ok := true
		for _, part := range req.Parts {
			raw, _, err := call(ctx, "get_price_by_uuid", map[string]any{"language": "en", "uuid": uuids[part.PartID], "quantity": part.Quantity, "unit": "mm", "currency": req.Currency, "productname": mat})
			if err != nil {
				ok = false
				break
			}
			total, has := pickNumber(decodeAny(raw), "body.total_cost", "total_cost", "body.price", "price")
			if !has {
				ok = false
				break
			}
			opt.Lines = append(opt.Lines, QuoteLine{PartID: part.PartID, SHA256: part.SHA256, Quantity: part.Quantity, UnitPrice: round2(total / float64(max(part.Quantity, 1))), LineTotal: round2(total), Ref: uuids[part.PartID]})
			opt.Subtotal += total
		}
		if !ok {
			continue
		}
		opt.Subtotal = round2(opt.Subtotal)
		opt.Total = opt.Subtotal
		opt.Ref = refJSON(map[string]any{"uuids": uuids})
		options = append(options, opt)
	}
	return options, nil
}

func (sculpteoProvider) PrepareOrder(_ context.Context, _ ToolCaller, opt QuoteOption, _ Address, _ map[string]any) (Draft, error) {
	if len(opt.Lines) > 3 {
		return Draft{}, errors.New("the Sculpteo connector accepts at most 3 parts per order")
	}
	return Draft{Total: opt.Total, Currency: opt.Currency, Ref: opt.Ref, Note: "Sculpteo has no draft step; the order is created at submit."}, nil
}

func (sculpteoProvider) Submit(ctx context.Context, call ToolCaller, opt QuoteOption, _ Draft, addr Address, _ map[string]any, _ string) (OrderRef, error) {
	input := map[string]any{"language": "en", "ship_addressee": addr.FirstName + " " + addr.LastName, "ship_street1": addr.Line1, "ship_street2": addr.Line2,
		"ship_city": addr.City, "ship_state": addr.State, "ship_postal_code": addr.Zip, "ship_country": addr.Country}
	for i, line := range opt.Lines {
		input[fmt.Sprintf("item[%d]", i)] = fmt.Sprintf("%s:%d:1:mm:%s", line.Ref, line.Quantity, opt.MaterialRef)
	}
	raw, _, err := call(ctx, "create_order", input)
	if err != nil {
		return OrderRef{}, err
	}
	id := pickString(decodeAny(raw), "reference", "order.reference", "body.reference", "id")
	if id == "" {
		return OrderRef{}, errors.New("Sculpteo returned no order reference")
	}
	return OrderRef{ProviderOrderID: id, Status: orderSubmitted, Note: "Order placed at Sculpteo on invoice."}, nil
}

func (sculpteoProvider) Status(ctx context.Context, call ToolCaller, id string) (OrderStatus, error) {
	raw, _, err := call(ctx, "get_order_tracking", map[string]any{"reference": id})
	if err != nil {
		return OrderStatus{}, err
	}
	body := decodeAny(raw)
	ps := strings.ToLower(pickString(body, "status", "body.status"))
	return OrderStatus{Status: mapGenericStatus(ps), ProviderStatus: ps, TrackingNumber: pickString(body, "tracking_number", "body.tracking_number"), TrackingURL: pickString(body, "tracking_url", "body.tracking_url")}, nil
}

func (sculpteoProvider) Cancel(context.Context, ToolCaller, string) error { return errNotSupported }

// ---- shared ---------------------------------------------------------------

func mapGenericStatus(s string) string {
	s = strings.ToLower(s)
	switch {
	case s == "":
		return orderSubmitted
	case strings.Contains(s, "deliver") || strings.Contains(s, "received") || strings.Contains(s, "complete"):
		return orderDelivered
	case strings.Contains(s, "ship"):
		return orderShipped
	case strings.Contains(s, "cancel") || strings.Contains(s, "refund"):
		return orderCancelled
	case strings.Contains(s, "produc") || strings.Contains(s, "print") || strings.Contains(s, "manufactur") || strings.Contains(s, "process"):
		return orderInProduction
	}
	return orderSubmitted
}

func atoiOr(s string) any {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return s
}
