package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type DispatchJob struct {
	ID              int64          `json:"id"`
	StoreID         int64          `json:"store_id"`
	SaleID          int64          `json:"sale_id"`
	OrderID         int64          `json:"order_id"`
	FulfillmentID   *int64         `json:"fulfillment_id,omitempty"`
	ConnectionID    int64          `json:"connection_id"`
	ProviderSlug    string         `json:"provider_slug"`
	Status          string         `json:"status"`
	IdempotencyKey  string         `json:"idempotency_key"`
	ExternalOrderID string         `json:"external_order_id,omitempty"`
	Request         map[string]any `json:"request,omitempty"`
	Response        map[string]any `json:"response,omitempty"`
	Error           string         `json:"error,omitempty"`
	AttemptCount    int64          `json:"attempt_count"`
	NextAttemptAt   string         `json:"next_attempt_at,omitempty"`
	SubmittedAt     string         `json:"submitted_at,omitempty"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
}

type ShippingOption struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	AmountCents  int64          `json:"amount_cents"`
	Currency     string         `json:"currency"`
	Provider     string         `json:"provider"`
	ConnectionID int64          `json:"connection_id"`
	Raw          map[string]any `json:"raw,omitempty"`
}

type dispatchLine struct {
	SaleItem    *SaleItem
	OrderItemID int64
	Source      *VariantSource
}

func (a *App) toolShippingQuote(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	cart, err := resolveCart(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	address := mapArg(args, "shipping_address")
	if len(address) == 0 {
		return nil, errors.New("shipping_address required")
	}
	groups, err := cartSourceGroups(ctx.AppDB(), pid, cart)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return map[string]any{"quotes": []ShippingOption{}, "shipping_cents": 0, "currency": cart.Currency}, nil
	}
	var quotes []ShippingOption
	var selected []ShippingOption
	var total int64
	for connectionID, lines := range groups {
		bound := providerBinding(ctx, connectionID)
		if bound == nil {
			return nil, fmt.Errorf("supplier connection %d is no longer bound", connectionID)
		}
		policy, err := dbProviderPolicyGet(ctx.AppDB(), pid, cart.StoreID, connectionID)
		if err != nil {
			return nil, err
		}
		options, err := quoteProviderShipping(ctx, bound, policy, address, lines, cart.Currency)
		if err != nil {
			return nil, err
		}
		if len(options) == 0 {
			return nil, fmt.Errorf("%s returned no shipping options", bound.AppSlug)
		}
		quotes = append(quotes, options...)
		lowest := options[0]
		for _, option := range options[1:] {
			if option.Currency == lowest.Currency && option.AmountCents < lowest.AmountCents {
				lowest = option
			}
		}
		selected = append(selected, lowest)
		total += lowest.AmountCents
	}
	quoteSnapshot := map[string]any{
		"selected": selected, "all": quotes, "shipping_address": address,
		"quoted_at": time.Now().UTC().Format(time.RFC3339), "expires_at": time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339),
	}
	if !hasKey(args, "apply") || boolArg(args, "apply") {
		if err := dbCartApplyShipping(ctx.AppDB(), pid, cart.ID, total, quoteSnapshot); err != nil {
			return nil, err
		}
		cart, err = dbCartGet(ctx.AppDB(), pid, cart.ID, true)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"quotes": quotes, "selected": selected, "shipping_cents": total,
		"currency": cart.Currency, "cart": cart, "quote": quoteSnapshot,
	}, nil
}

func (a *App) toolDispatchesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	jobs, err := dbDispatchJobs(ctx.AppDB(), pid, args)
	return map[string]any{"dispatches": jobs, "count": len(jobs)}, err
}

func (a *App) toolDispatchSubmit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	jobID := intArg(args, "id")
	if jobID == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE commerce_dispatch_jobs
		    SET status='queued', error='', next_attempt_at=NULL, updated_at=CURRENT_TIMESTAMP
		  WHERE project_id=? AND id=? AND status IN ('review','failed')`, pid, jobID); err != nil {
		return nil, err
	}
	job, err := dbDispatchJobGet(ctx.AppDB(), pid, jobID)
	if err != nil || job == nil {
		return nil, firstErr(err, errors.New("dispatch not found"))
	}
	if err := a.submitDispatch(ctx.WithProject(pid), pid, job); err != nil {
		return nil, err
	}
	job, err = dbDispatchJobGet(ctx.AppDB(), pid, jobID)
	return map[string]any{"dispatch": job}, err
}

func (a *App) toolSourcesSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	sources, err := dbVariantSources(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	limit := clamp(intArg(args, "limit"), 1, 100, 25)
	if len(sources) > int(limit) {
		sources = sources[:limit]
	}
	var synced int
	var failures []map[string]any
	for _, source := range sources {
		if err := a.syncVariantSource(ctx.WithProject(pid), pid, source); err != nil {
			failures = append(failures, map[string]any{"source_id": source.ID, "error": err.Error()})
			continue
		}
		synced++
	}
	return map[string]any{"synced": synced, "failed": failures, "count": len(sources)}, nil
}

func (a *App) handleShippingQuote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := map[string]any{"_project_id": pid}
	if err := readJSON(r, &args); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	args["_project_id"] = pid
	result, callErr := a.toolShippingQuote(ctx, args)
	httpResult(w, result, callErr)
}

func (a *App) handleDispatches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := queryArgs(r)
	args["_project_id"] = pid
	result, callErr := a.toolDispatchesList(ctx, args)
	httpToolResult(w, result, callErr, "dispatches")
}

func (a *App) handleDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/submit") {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, pid, err := requestAppContext(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin/dispatches/"), "/submit")
	id, err := strconv.ParseInt(strings.Trim(path, "/"), 10, 64)
	if err != nil || id == 0 {
		httpErr(w, http.StatusBadRequest, "invalid dispatch id")
		return
	}
	result, callErr := a.toolDispatchSubmit(ctx, map[string]any{"_project_id": pid, "id": id})
	httpResult(w, result, callErr)
}

func (a *App) reconcileProviderDispatches(_ context.Context, ctx *sdk.AppCtx) error {
	pid := ctx.CurrentProject()
	if pid == "" {
		return nil
	}
	jobs, err := dbDispatchJobs(ctx.AppDB(), pid, map[string]any{"runnable": true, "limit": 20})
	if err != nil {
		return err
	}
	var failures []string
	for _, job := range jobs {
		var callErr error
		if job.Status == "queued" || job.Status == "failed" {
			callErr = a.submitDispatch(ctx, pid, job)
		} else {
			callErr = a.syncDispatch(ctx, pid, job)
		}
		if callErr != nil {
			failures = append(failures, fmt.Sprintf("%d: %v", job.ID, callErr))
		}
	}
	if len(failures) > 0 {
		return errors.New("provider dispatch reconciliation: " + strings.Join(failures, "; "))
	}
	return nil
}

func (a *App) reconcileProviderSources(_ context.Context, ctx *sdk.AppCtx) error {
	pid := ctx.CurrentProject()
	if pid == "" {
		return nil
	}
	sources, err := dbVariantSources(ctx.AppDB(), pid, map[string]any{})
	if err != nil {
		return err
	}
	var failures []string
	for i, source := range sources {
		if i >= 50 {
			break
		}
		if err := a.syncVariantSource(ctx, pid, source); err != nil {
			failures = append(failures, fmt.Sprintf("%d: %v", source.ID, err))
		}
	}
	if len(failures) > 0 {
		return errors.New("provider source reconciliation: " + strings.Join(failures, "; "))
	}
	return nil
}

func (a *App) queueSaleDispatches(ctx *sdk.AppCtx, pid string, sale *Sale) error {
	if sale == nil || sale.OrderID == nil || *sale.OrderID == 0 {
		return nil
	}
	if len(sale.Items) == 0 {
		items, err := dbSaleItems(ctx.AppDB(), pid, sale.ID)
		if err != nil {
			return err
		}
		sale.Items = items
	}
	groups := map[int64][]dispatchLine{}
	for _, item := range sale.Items {
		if item.VariantID == nil || *item.VariantID == 0 {
			continue
		}
		source, err := dbVariantSourceByVariant(ctx.AppDB(), pid, *item.VariantID)
		if err != nil {
			return err
		}
		if source == nil {
			continue
		}
		groups[source.ConnectionID] = append(groups[source.ConnectionID], dispatchLine{
			SaleItem: item, Source: source,
		})
	}
	if len(groups) == 0 {
		return nil
	}
	var orderResponse map[string]any
	if err := ctx.PlatformAPI().CallAppResult("orders", "orders_get", map[string]any{
		"_project_id": pid, "id": *sale.OrderID,
	}, &orderResponse); err != nil {
		return fmt.Errorf("load order for provider dispatch: %w", err)
	}
	order := unwrap(orderResponse, "order")
	orderByVariant := map[int64]int64{}
	orderBySKU := map[string]int64{}
	for _, raw := range anySlice(order["items"]) {
		item := anyMap(raw)
		metadata := mapArg(item, "metadata")
		if variantID := intArg(metadata, "commerce_variant_id"); variantID != 0 {
			orderByVariant[variantID] = intArg(item, "id")
		}
		if sku := strings.ToUpper(strArg(item, "sku")); sku != "" {
			orderBySKU[sku] = intArg(item, "id")
		}
	}
	for connectionID, lines := range groups {
		for i := range lines {
			line := &lines[i]
			line.OrderItemID = orderByVariant[ptrValue(line.SaleItem.VariantID)]
			if line.OrderItemID == 0 {
				line.OrderItemID = orderBySKU[strings.ToUpper(line.SaleItem.SKU)]
			}
			if line.OrderItemID == 0 {
				return fmt.Errorf("order item for Commerce variant %d not found", ptrValue(line.SaleItem.VariantID))
			}
		}
		groups[connectionID] = lines
	}
	for connectionID, lines := range groups {
		bound := providerBinding(ctx, connectionID)
		if bound == nil {
			return fmt.Errorf("supplier connection %d is no longer bound", connectionID)
		}
		policy, err := dbProviderPolicyGet(ctx.AppDB(), pid, sale.StoreID, connectionID)
		if err != nil {
			return err
		}
		if policy == nil {
			policy = &ProviderPolicy{
				StoreID: sale.StoreID, ConnectionID: connectionID, ProviderSlug: bound.AppSlug,
				Enabled: true, FulfillmentMode: "review", MarginBPS: 3000, Settings: map[string]any{},
			}
		}
		if !policy.Enabled {
			continue
		}
		request, err := providerOrderRequest(bound.AppSlug, policy, sale, lines)
		if err != nil {
			return err
		}
		status := "review"
		if policy.FulfillmentMode == "automatic" {
			status = "queued"
		}
		key := fmt.Sprintf("commerce:sale:%d:connection:%d", sale.ID, connectionID)
		job, err := dbDispatchJobEnsure(ctx.AppDB(), pid, sale, connectionID, bound.AppSlug, status, key, request)
		if err != nil {
			return err
		}
		if job.FulfillmentID == nil {
			items := make([]any, 0, len(lines))
			for _, line := range lines {
				items = append(items, map[string]any{"order_item_id": line.OrderItemID, "quantity": line.SaleItem.Quantity})
			}
			var fulfillmentResponse map[string]any
			if err := ctx.PlatformAPI().CallAppResult("orders", "fulfillments_create", map[string]any{
				"_project_id": pid, "order_id": *sale.OrderID, "provider": bound.AppSlug,
				"fulfillment_app": "commerce", "fulfillment_type": "supplier_shipment",
				"idempotency_key": key, "status": "queued", "submit": false, "items": items, "payload": request,
				"metadata": map[string]any{"commerce_sale_id": sale.ID, "connection_id": connectionID},
			}, &fulfillmentResponse); err != nil {
				_ = dbDispatchFailure(ctx.AppDB(), pid, job.ID, err)
				return fmt.Errorf("create Orders fulfillment: %w", err)
			}
			fulfillmentID := intArg(unwrap(fulfillmentResponse, "fulfillment"), "id")
			if fulfillmentID == 0 {
				return errors.New("Orders fulfillment response missing id")
			}
			if _, err := ctx.AppDB().Exec(
				`UPDATE commerce_dispatch_jobs
				    SET fulfillment_id=?, status=?, error='', next_attempt_at=NULL, updated_at=CURRENT_TIMESTAMP
				  WHERE project_id=? AND id=?`,
				fulfillmentID, status, pid, job.ID); err != nil {
				return err
			}
			job.FulfillmentID = &fulfillmentID
			job.Status = status
		}
		if job.Status == "queued" {
			if err := a.submitDispatch(ctx, pid, job); err != nil {
				return err
			}
		}
	}
	return updateSaleFulfillmentStatus(ctx.AppDB(), pid, sale.ID)
}

func providerOrderRequest(provider string, policy *ProviderPolicy, sale *Sale, lines []dispatchLine) (map[string]any, error) {
	if sale == nil || len(lines) == 0 {
		return nil, errors.New("sale and sourced lines required")
	}
	reference := fmt.Sprintf("commerce-%d-%d", sale.ID, policy.ConnectionID)
	switch provider {
	case "printful":
		items := make([]any, 0, len(lines))
		for _, line := range lines {
			item := map[string]any{"quantity": int(math.Ceil(line.SaleItem.Quantity))}
			if id, err := strconvInt64(line.Source.ExternalVariantID); err == nil {
				item["sync_variant_id"] = id
			} else {
				item["external_variant_id"] = line.Source.ExternalVariantID
			}
			items = append(items, item)
		}
		return map[string]any{
			"external_id": compactExternalID(reference, 32), "recipient": printfulAddress(sale.ShippingAddress),
			"items": items, "confirm": true,
		}, nil
	case "printify":
		shopID := fmt.Sprint(policy.Settings["shop_id"])
		if shopID == "" || shopID == "<nil>" {
			return nil, errors.New("Printify provider policy requires settings.shop_id")
		}
		items := make([]any, 0, len(lines))
		for _, line := range lines {
			items = append(items, map[string]any{
				"product_id": line.Source.ExternalProductID,
				"variant_id": providerNumberOrString(line.Source.ExternalVariantID),
				"quantity":   int(math.Ceil(line.SaleItem.Quantity)),
			})
		}
		return map[string]any{
			"shop_id": shopID, "external_id": reference, "line_items": items,
			"shipping_method":            intSetting(policy, "shipping_method", 1),
			"send_shipping_notification": false, "address_to": printifyAddress(sale.ShippingAddress, sale),
		}, nil
	case "bigbuy":
		products := make([]any, 0, len(lines))
		for _, line := range lines {
			products = append(products, map[string]any{
				"reference": firstNonEmpty(line.Source.ProviderSKU, line.SaleItem.SKU),
				"quantity":  int(math.Ceil(line.SaleItem.Quantity)),
			})
		}
		order := map[string]any{
			"internalReference": reference, "language": firstNonEmpty(stringSetting(policy, "language"), "en"),
			"paymentMethod":   firstNonEmpty(stringSetting(policy, "payment_method"), "moneybox"),
			"shippingAddress": bigBuyAddress(sale.ShippingAddress, sale), "products": products,
		}
		for key, value := range mapArg(policy.Settings, "order_template") {
			order[key] = value
		}
		return map[string]any{"order": order}, nil
	case "cjdropshipping":
		products := make([]any, 0, len(lines))
		for _, line := range lines {
			products = append(products, map[string]any{
				"vid": line.Source.ExternalVariantID, "quantity": int(math.Ceil(line.SaleItem.Quantity)),
			})
		}
		return map[string]any{
			"orderNumber": reference, "shippingAddress": cjAddress(sale.ShippingAddress, sale), "products": products,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported supplier provider %q", provider)
	}
}

func (a *App) submitDispatch(ctx *sdk.AppCtx, pid string, job *DispatchJob) error {
	if job == nil {
		return errors.New("dispatch required")
	}
	if job.Status == "submitted" || job.Status == "shipped" || job.Status == "delivered" {
		return nil
	}
	bound := providerBinding(ctx, job.ConnectionID)
	if bound == nil || bound.AppSlug != job.ProviderSlug {
		err := errors.New("supplier connection is no longer bound")
		_ = dbDispatchFailure(ctx.AppDB(), pid, job.ID, err)
		_ = updateSaleFulfillmentStatus(ctx.AppDB(), pid, job.SaleID)
		return err
	}
	tool := map[string]string{
		"printful": "create_order", "printify": "create_order",
		"bigbuy": "order_create", "cjdropshipping": "orders_create",
	}[job.ProviderSlug]
	if tool == "" {
		return fmt.Errorf("unsupported supplier provider %q", job.ProviderSlug)
	}
	raw, err := executeProviderTool(ctx, bound, tool, copyMap(job.Request))
	if err != nil {
		_ = dbDispatchFailure(ctx.AppDB(), pid, job.ID, err)
		_ = updateSaleFulfillmentStatus(ctx.AppDB(), pid, job.SaleID)
		if job.FulfillmentID != nil {
			a.updateOrdersFulfillment(ctx, pid, *job.FulfillmentID, "failed", "", nil, err.Error())
		}
		return err
	}
	externalID := extractProviderID(raw)
	if externalID == "" {
		err := errors.New("provider order response missing external order id")
		_ = dbDispatchFailure(ctx.AppDB(), pid, job.ID, err)
		_ = updateSaleFulfillmentStatus(ctx.AppDB(), pid, job.SaleID)
		return err
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE commerce_dispatch_jobs
		    SET status='submitted', external_order_id=?, response_json=?, error='',
		        attempt_count=attempt_count+1, submitted_at=COALESCE(submitted_at,CURRENT_TIMESTAMP),
		        next_attempt_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		  WHERE project_id=? AND id=?`,
		externalID, jsonText(raw, "{}"), pid, job.ID); err != nil {
		return err
	}
	if job.FulfillmentID != nil {
		if err := a.updateOrdersFulfillment(ctx, pid, *job.FulfillmentID, "submitted", externalID, raw, ""); err != nil {
			return err
		}
	}
	ctx.Emit("commerce.fulfillment.submitted", map[string]any{
		"dispatch_id": job.ID, "sale_id": job.SaleID, "provider": job.ProviderSlug, "external_order_id": externalID,
	})
	return updateSaleFulfillmentStatus(ctx.AppDB(), pid, job.SaleID)
}

func (a *App) syncDispatch(ctx *sdk.AppCtx, pid string, job *DispatchJob) error {
	if job == nil || job.ExternalOrderID == "" {
		return nil
	}
	bound := providerBinding(ctx, job.ConnectionID)
	if bound == nil {
		return errors.New("supplier connection is no longer bound")
	}
	policy, _ := dbProviderPolicyGet(ctx.AppDB(), pid, job.StoreID, job.ConnectionID)
	input := map[string]any{}
	tool := ""
	switch job.ProviderSlug {
	case "printful":
		tool, input["id"] = "get_order", job.ExternalOrderID
	case "printify":
		tool, input["order_id"] = "get_order", job.ExternalOrderID
		applyPolicySetting(input, policy, "shop_id")
	case "bigbuy":
		tool, input["orderId"] = "order_get", providerNumberOrString(job.ExternalOrderID)
	case "cjdropshipping":
		tool, input["orderId"] = "order_get", job.ExternalOrderID
	}
	raw, err := executeProviderTool(ctx, bound, tool, input)
	if err != nil {
		return err
	}
	status := normalizeProviderOrderStatus(extractProviderStatus(raw))
	if status == "" {
		status = job.Status
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE commerce_dispatch_jobs SET status=?, response_json=?, error='', next_attempt_at=datetime('now','+5 minutes'), updated_at=CURRENT_TIMESTAMP
		  WHERE project_id=? AND id=?`, status, jsonText(raw, "{}"), pid, job.ID); err != nil {
		return err
	}
	if job.FulfillmentID != nil {
		fulfillmentStatus := status
		if status == "submitted" {
			fulfillmentStatus = "accepted"
		}
		if err := a.updateOrdersFulfillment(ctx, pid, *job.FulfillmentID, fulfillmentStatus, job.ExternalOrderID, raw, ""); err != nil {
			return err
		}
	}
	tracking := extractProviderTracking(raw)
	if tracking["tracking_number"] != "" && job.FulfillmentID != nil {
		var ignored map[string]any
		if err := ctx.PlatformAPI().CallAppResult("orders", "shipments_upsert", map[string]any{
			"_project_id": pid, "order_id": job.OrderID, "fulfillment_id": *job.FulfillmentID,
			"provider": job.ProviderSlug, "provider_shipment_id": tracking["shipment_id"],
			"carrier": tracking["carrier"], "tracking_number": tracking["tracking_number"],
			"tracking_url": tracking["tracking_url"], "status": status, "raw_payload": raw,
		}, &ignored); err != nil {
			return err
		}
	}
	return updateSaleFulfillmentStatus(ctx.AppDB(), pid, job.SaleID)
}

func (a *App) updateOrdersFulfillment(ctx *sdk.AppCtx, pid string, id int64, status, externalRef string, response any, errorText string) error {
	if id == 0 || ctx.PlatformAPI() == nil {
		return nil
	}
	input := map[string]any{
		"_project_id": pid, "id": id, "status": status, "external_ref": externalRef,
		"error": errorText, "actor": "commerce",
	}
	if response != nil {
		input["response_payload"] = response
	}
	var ignored map[string]any
	return ctx.PlatformAPI().CallAppResult("orders", "fulfillments_update", input, &ignored)
}

func (a *App) syncVariantSource(ctx *sdk.AppCtx, pid string, source *VariantSource) error {
	if source == nil {
		return nil
	}
	bound := providerBinding(ctx, source.ConnectionID)
	if bound == nil {
		return errors.New("supplier connection is no longer bound")
	}
	policy, _ := dbProviderPolicyGet(ctx.AppDB(), pid, source.StoreID, source.ConnectionID)
	product, err := fetchProviderProduct(ctx, bound, policy, source.ExternalProductID, map[string]any{})
	if err != nil {
		return err
	}
	for _, variant := range product.Variants {
		if variant.ID != source.ExternalVariantID {
			continue
		}
		availability := "available"
		if !variant.Available {
			availability = "unavailable"
		}
		_, err := ctx.AppDB().Exec(
			`UPDATE commerce_variant_sources
			    SET provider_sku=?, unit_cost_cents=?, currency=?, availability=?,
			        available_quantity=?, source_json=?, last_synced_at=CURRENT_TIMESTAMP,
			        updated_at=CURRENT_TIMESTAMP
			  WHERE project_id=? AND id=?`,
			variant.SKU, variant.CostCents, firstNonEmpty(variant.Currency, source.Currency), availability,
			nullableFloat(variant.AvailableQuantity), jsonText(variant.Raw, "{}"), pid, source.ID)
		return err
	}
	return errors.New("provider variant no longer exists")
}

func cartSourceGroups(db *sql.DB, pid string, cart *Cart) (map[int64][]dispatchLine, error) {
	groups := map[int64][]dispatchLine{}
	if cart == nil {
		return groups, nil
	}
	for _, item := range cart.Items {
		source, err := dbVariantSourceByVariant(db, pid, item.VariantID)
		if err != nil {
			return nil, err
		}
		if source == nil {
			continue
		}
		policy, err := dbProviderPolicyGet(db, pid, cart.StoreID, source.ConnectionID)
		if err != nil {
			return nil, err
		}
		if policy != nil && !policy.Enabled {
			return nil, fmt.Errorf("%s is unavailable because %s is disabled for this store", item.TitleSnapshot, source.ProviderSlug)
		}
		if source.Availability == "unavailable" {
			return nil, fmt.Errorf("%s is unavailable from %s", item.TitleSnapshot, source.ProviderSlug)
		}
		if source.AvailableQuantity != nil && item.Quantity > *source.AvailableQuantity {
			return nil, fmt.Errorf("%s only has %.2f available from %s", item.TitleSnapshot, *source.AvailableQuantity, source.ProviderSlug)
		}
		saleItem := &SaleItem{
			VariantID: &item.VariantID, SKU: item.SKU, TitleSnapshot: item.TitleSnapshot,
			Quantity: item.Quantity, UnitAmountCents: item.UnitAmountCents, Currency: item.Currency,
		}
		groups[source.ConnectionID] = append(groups[source.ConnectionID], dispatchLine{SaleItem: saleItem, Source: source})
	}
	return groups, nil
}

func quoteProviderShipping(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, policy *ProviderPolicy, address map[string]any, lines []dispatchLine, currency string) ([]ShippingOption, error) {
	if policy != nil {
		if flat := intSetting(policy, "flat_shipping_cents", 0); flat > 0 {
			return []ShippingOption{{
				ID: "flat", Name: "Standard", AmountCents: int64(flat), Currency: currency,
				Provider: bound.AppSlug, ConnectionID: bound.ConnectionID,
			}}, nil
		}
	}
	var raw any
	var err error
	switch bound.AppSlug {
	case "printful":
		items := make([]any, 0, len(lines))
		for _, line := range lines {
			item := map[string]any{"quantity": int(math.Ceil(line.SaleItem.Quantity))}
			if id, parseErr := strconvInt64(line.Source.ExternalVariantID); parseErr == nil {
				item["sync_variant_id"] = id
			} else {
				item["external_variant_id"] = line.Source.ExternalVariantID
			}
			items = append(items, item)
		}
		raw, err = executeProviderTool(ctx, bound, "get_shipping_rates", map[string]any{
			"recipient": printfulAddress(address), "items": items, "currency": currency,
		})
	case "bigbuy":
		products := make([]any, 0, len(lines))
		for _, line := range lines {
			products = append(products, map[string]any{
				"reference": firstNonEmpty(line.Source.ProviderSKU, line.SaleItem.SKU),
				"quantity":  int(math.Ceil(line.SaleItem.Quantity)),
			})
		}
		raw, err = executeProviderTool(ctx, bound, "shipping_quote_order", map[string]any{
			"shipping_request": map[string]any{
				"isoCountry": firstString(address, "country_code", "country"), "postcode": firstString(address, "postal_code", "zip"),
				"products": products,
			},
		})
	case "cjdropshipping":
		var total int64
		var parts []any
		for _, line := range lines {
			part, callErr := executeProviderTool(ctx, bound, "shipping_calculate", map[string]any{
				"vid": line.Source.ExternalVariantID, "quantity": int(math.Ceil(line.SaleItem.Quantity)),
				"countryCode": firstString(address, "country_code", "country"), "zip": firstString(address, "postal_code", "zip"),
			})
			if callErr != nil {
				return nil, callErr
			}
			amount, partCurrency := extractShippingAmount(part)
			total += amount
			if partCurrency != "" {
				currency = partCurrency
			}
			parts = append(parts, part)
		}
		return []ShippingOption{{
			ID: "cj-standard", Name: "Standard", AmountCents: total, Currency: currency,
			Provider: bound.AppSlug, ConnectionID: bound.ConnectionID, Raw: map[string]any{"parts": parts},
		}}, nil
	case "printify":
		return nil, errors.New("Printify requires provider policy settings.flat_shipping_cents for checkout quotes")
	default:
		return nil, fmt.Errorf("unsupported supplier provider %q", bound.AppSlug)
	}
	if err != nil {
		return nil, err
	}
	return normalizeShippingOptions(bound, raw, currency), nil
}

func normalizeShippingOptions(bound *sdk.BoundIntegration, raw any, fallbackCurrency string) []ShippingOption {
	rows := providerRows(raw)
	if len(rows) == 0 {
		rows = []any{firstObject(raw)}
	}
	var out []ShippingOption
	for i, row := range rows {
		item := anyMap(row)
		amount, currency := extractShippingAmount(item)
		if amount <= 0 {
			continue
		}
		if currency == "" {
			currency = fallbackCurrency
		}
		out = append(out, ShippingOption{
			ID:          firstNonEmpty(firstString(item, "id", "service", "shippingMethod"), fmt.Sprintf("%s-%d", bound.AppSlug, i)),
			Name:        firstNonEmpty(firstString(item, "name", "serviceName", "shippingMethod"), "Standard"),
			AmountCents: amount, Currency: strings.ToUpper(currency),
			Provider: bound.AppSlug, ConnectionID: bound.ConnectionID, Raw: item,
		})
	}
	return out
}

func extractShippingAmount(raw any) (int64, string) {
	root := firstObject(raw)
	currency := strings.ToUpper(firstString(root, "currency", "currencyCode"))
	for _, key := range []string{"rate", "price", "shippingCost", "logisticPrice", "amount", "total"} {
		if amount := numberCents(root[key], false); amount > 0 {
			return amount, currency
		}
	}
	for _, key := range []string{"result", "data", "logisticList"} {
		rows := providerRows(root[key])
		if len(rows) > 0 {
			return extractShippingAmount(rows[0])
		}
	}
	return 0, currency
}

func dbCartApplyShipping(db *sql.DB, pid string, cartID, shipping int64, quote map[string]any) error {
	if shipping < 0 {
		return errors.New("shipping cannot be negative")
	}
	var metadataText string
	if err := db.QueryRow(`SELECT metadata_json FROM commerce_carts WHERE project_id=? AND id=?`, pid, cartID).Scan(&metadataText); err != nil {
		return err
	}
	metadata := jsonMap(metadataText)
	metadata["shipping_quote"] = quote
	_, err := db.Exec(
		`UPDATE commerce_carts
		    SET shipping_cents=?, total_cents=subtotal_cents-discount_cents+tax_cents+?,
		        metadata_json=?, updated_at=CURRENT_TIMESTAMP
		  WHERE project_id=? AND id=?`,
		shipping, shipping, jsonText(metadata, "{}"), pid, cartID)
	return err
}

func dbDispatchJobEnsure(db *sql.DB, pid string, sale *Sale, connectionID int64, provider, status, key string, request map[string]any) (*DispatchJob, error) {
	_, err := db.Exec(
		`INSERT INTO commerce_dispatch_jobs
		   (project_id, store_id, sale_id, order_id, connection_id, provider_slug, status, idempotency_key, request_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, idempotency_key) DO NOTHING`,
		pid, sale.StoreID, sale.ID, ptrValue(sale.OrderID), connectionID, provider, status, key, jsonText(request, "{}"))
	if err != nil {
		return nil, err
	}
	return dbDispatchJobByKey(db, pid, key)
}

func dbDispatchJobGet(db *sql.DB, pid string, id int64) (*DispatchJob, error) {
	return scanDispatchJob(db.QueryRow(dispatchSelect()+` WHERE project_id=? AND id=?`, pid, id))
}

func dbDispatchJobByKey(db *sql.DB, pid, key string) (*DispatchJob, error) {
	return scanDispatchJob(db.QueryRow(dispatchSelect()+` WHERE project_id=? AND idempotency_key=?`, pid, key))
}

func dbDispatchJobs(db *sql.DB, pid string, args map[string]any) ([]*DispatchJob, error) {
	where := []string{"project_id=?"}
	values := []any{pid}
	if storeID := intArg(args, "store_id"); storeID != 0 {
		where = append(where, "store_id=?")
		values = append(values, storeID)
	}
	if saleID := intArg(args, "sale_id"); saleID != 0 {
		where = append(where, "sale_id=?")
		values = append(values, saleID)
	}
	if status := strArg(args, "status"); status != "" {
		where = append(where, "status=?")
		values = append(values, status)
	}
	if boolArg(args, "runnable") {
		where = append(where, `(
			(status IN ('queued','failed') AND (next_attempt_at IS NULL OR next_attempt_at<=CURRENT_TIMESTAMP))
			OR (status IN ('submitted','accepted','shipped') AND (next_attempt_at IS NULL OR next_attempt_at<=CURRENT_TIMESTAMP))
		)`)
	}
	limit := clamp(intArg(args, "limit"), 1, 200, 100)
	values = append(values, limit)
	rows, err := db.Query(dispatchSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC LIMIT ?`, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DispatchJob
	for rows.Next() {
		job, err := scanDispatchJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func dispatchSelect() string {
	return `SELECT id, store_id, sale_id, order_id, fulfillment_id, connection_id, provider_slug,
	        status, idempotency_key, external_order_id, request_json, response_json, error,
	        attempt_count, next_attempt_at, submitted_at, created_at, updated_at
	   FROM commerce_dispatch_jobs`
}

func scanDispatchJob(row scanner) (*DispatchJob, error) {
	var job DispatchJob
	var fulfillmentID sql.NullInt64
	var request, response string
	var nextAttempt, submitted sql.NullString
	err := row.Scan(
		&job.ID, &job.StoreID, &job.SaleID, &job.OrderID, &fulfillmentID, &job.ConnectionID, &job.ProviderSlug,
		&job.Status, &job.IdempotencyKey, &job.ExternalOrderID, &request, &response, &job.Error,
		&job.AttemptCount, &nextAttempt, &submitted, &job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.FulfillmentID = ptrIfValid(fulfillmentID)
	job.Request = jsonMap(request)
	job.Response = jsonMap(response)
	if nextAttempt.Valid {
		job.NextAttemptAt = nextAttempt.String
	}
	if submitted.Valid {
		job.SubmittedAt = submitted.String
	}
	return &job, nil
}

func dbDispatchFailure(db *sql.DB, pid string, id int64, failure error) error {
	_, err := db.Exec(
		`UPDATE commerce_dispatch_jobs
		    SET status='failed', error=?, attempt_count=attempt_count+1,
		        next_attempt_at=datetime('now', '+' || MIN(60, (attempt_count+1)*(attempt_count+1)) || ' minutes'),
		        updated_at=CURRENT_TIMESTAMP
		  WHERE project_id=? AND id=?`,
		failure.Error(), pid, id)
	return err
}

func updateSaleFulfillmentStatus(db *sql.DB, pid string, saleID int64) error {
	rows, err := db.Query(
		`SELECT status, COUNT(*) FROM commerce_dispatch_jobs
		  WHERE project_id=? AND sale_id=? GROUP BY status`, pid, saleID)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	total := 0
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return err
		}
		counts[status] = count
		total += count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil || total == 0 {
		return err
	}
	status := "review"
	switch {
	case counts["delivered"] == total:
		status = "delivered"
	case counts["cancelled"] == total:
		status = "cancelled"
	case counts["delivered"]+counts["shipped"] > 0:
		status = "partially_fulfilled"
	case counts["failed"] > 0:
		status = "failed"
	case counts["accepted"]+counts["submitted"] > 0:
		status = "submitted"
	case counts["queued"] > 0:
		status = "queued"
	}
	_, err = db.Exec(
		`UPDATE commerce_sales SET fulfillment_status=?, updated_at=CURRENT_TIMESTAMP
		  WHERE project_id=? AND id=?`, status, pid, saleID)
	return err
}

func extractProviderID(value any) string {
	return recursiveString(value, []string{"id", "order_id", "orderId", "orderNumber", "orderCode", "order_id", "code"})
}

func extractProviderStatus(value any) string {
	return recursiveString(value, []string{"status", "order_status", "orderStatus", "statusName", "orderStatusName"})
}

func extractProviderTracking(value any) map[string]string {
	return map[string]string{
		"shipment_id":     recursiveString(value, []string{"shipment_id", "shipmentId", "packageId"}),
		"carrier":         recursiveString(value, []string{"carrier", "carrierName", "logisticName"}),
		"tracking_number": recursiveString(value, []string{"tracking_number", "trackingNumber", "trackingCode", "trackNumber"}),
		"tracking_url":    recursiveString(value, []string{"tracking_url", "trackingUrl", "trackUrl"}),
	}
}

func recursiveString(value any, keys []string) string {
	switch current := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if result := firstString(current, key); result != "" {
				return result
			}
		}
		for _, nested := range current {
			if result := recursiveString(nested, keys); result != "" {
				return result
			}
		}
	case []any:
		for _, nested := range current {
			if result := recursiveString(nested, keys); result != "" {
				return result
			}
		}
	}
	return ""
}

func normalizeProviderOrderStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.Contains(status, "deliver"):
		return "delivered"
	case strings.Contains(status, "ship"), strings.Contains(status, "fulfill"):
		return "shipped"
	case strings.Contains(status, "cancel"):
		return "cancelled"
	case strings.Contains(status, "fail"), strings.Contains(status, "error"):
		return "failed"
	case strings.Contains(status, "accept"), strings.Contains(status, "process"), strings.Contains(status, "production"):
		return "accepted"
	case status != "":
		return "submitted"
	default:
		return ""
	}
}

func printfulAddress(address map[string]any) map[string]any {
	return map[string]any{
		"name": firstString(address, "name", "full_name"), "company": firstString(address, "company"),
		"address1": firstString(address, "address1", "line1"), "address2": firstString(address, "address2", "line2"),
		"city": firstString(address, "city"), "state_code": firstString(address, "state_code", "state", "region"),
		"country_code": firstString(address, "country_code", "country"), "zip": firstString(address, "zip", "postal_code"),
		"phone": firstString(address, "phone"), "email": firstString(address, "email"),
	}
}

func printifyAddress(address map[string]any, sale *Sale) map[string]any {
	firstName, lastName := splitName(firstNonEmpty(firstString(address, "name", "full_name"), sale.CustomerName))
	return map[string]any{
		"first_name": firstName, "last_name": lastName, "email": sale.CustomerEmail,
		"phone": firstString(address, "phone"), "country": firstString(address, "country", "country_code"),
		"region": firstString(address, "region", "state", "state_code"), "address1": firstString(address, "address1", "line1"),
		"address2": firstString(address, "address2", "line2"), "city": firstString(address, "city"),
		"zip": firstString(address, "zip", "postal_code"),
	}
}

func bigBuyAddress(address map[string]any, sale *Sale) map[string]any {
	return map[string]any{
		"firstName": firstNonEmpty(firstString(address, "name", "full_name"), sale.CustomerName),
		"email":     sale.CustomerEmail, "phone": firstString(address, "phone"),
		"country": firstString(address, "country", "country_code"), "postcode": firstString(address, "postal_code", "zip"),
		"town": firstString(address, "city"), "address": firstString(address, "address1", "line1"),
		"comment": firstString(address, "address2", "line2"),
	}
}

func cjAddress(address map[string]any, sale *Sale) map[string]any {
	firstName, lastName := splitName(firstNonEmpty(firstString(address, "name", "full_name"), sale.CustomerName))
	return map[string]any{
		"firstName": firstName, "lastName": lastName, "email": sale.CustomerEmail,
		"phone": firstString(address, "phone"), "countryCode": firstString(address, "country_code", "country"),
		"province": firstString(address, "state", "region", "state_code"), "city": firstString(address, "city"),
		"address": firstString(address, "address1", "line1"), "address2": firstString(address, "address2", "line2"),
		"zip": firstString(address, "postal_code", "zip"),
	}
}

func splitName(name string) (string, string) {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], "."
	}
	return strings.Join(parts[:len(parts)-1], " "), parts[len(parts)-1]
}

func stringSetting(policy *ProviderPolicy, key string) string {
	if policy == nil {
		return ""
	}
	value := policy.Settings[key]
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func intSetting(policy *ProviderPolicy, key string, fallback int) int {
	if policy == nil {
		return fallback
	}
	if value, ok := numberValue(policy.Settings[key]); ok {
		return int(value)
	}
	return fallback
}

func compactExternalID(value string, max int) string {
	value = strings.NewReplacer(":", "-", "/", "-", " ", "-").Replace(value)
	if len(value) > max {
		value = value[:max]
	}
	return value
}

func providerNumberOrString(value string) any {
	if number, err := strconvInt64(value); err == nil {
		return number
	}
	return value
}

func strconvInt64(value string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
}
