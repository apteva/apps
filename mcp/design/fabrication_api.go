package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func fabricationTools(a *App) []sdk.Tool {
	id := map[string]any{"type": "integer", "minimum": 1}
	str := map[string]any{"type": "string"}
	return []sdk.Tool{
		{Name: "fabrication_providers", Description: "List print services bound to Design Studio (fabrication_provider role), what each adapter supports, and the current order policy (auto-order, limits, allowed providers, saved ship-to address labels). Policy and addresses are edited only by a person in the Design panel.",
			InputSchema: schemaObject(nil, nil), HandlerCtx: a.toolFabricationProviders},
		{Name: "fabrication_quote", Description: "Get print quotes for the printed parts of a validated revision from every bound print service (or chosen connection_ids). Builds STL exports, uploads only those files to the services, and returns options ranked by total. Free; nothing is ordered.",
			InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id,
				"part_ids":       map[string]any{"type": "array", "items": str},
				"quantity":       map[string]any{"type": "integer", "minimum": 1, "maximum": 10000},
				"quantities":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer", "minimum": 1}},
				"connection_ids": map[string]any{"type": "array", "items": id},
				"currency":       map[string]any{"type": "string", "enum": []string{"USD", "EUR", "GBP", "CAD", "AUD", "CHF"}},
				"country":        map[string]any{"type": "string", "pattern": "^[A-Z]{2}$"},
				"material":       str, "max_options": map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			}, []string{"design_id"}), HandlerCtx: a.toolFabricationQuote},
		{Name: "fabrication_quotes_list", Description: "List earlier quotes for a design, newest first, with their options and expiry.",
			InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, []string{"design_id"}), HandlerCtx: a.toolFabricationQuotes},
		{Name: "fabrication_order_prepare", Description: "Turn one quote option into a draft order shipping to a saved address. Nothing is charged. Returns the policy verdict: approved (within the operator's auto-order policy) or awaiting_approval (a person must approve it in the Design panel).",
			InputSchema: schemaObject(map[string]any{"quote_id": id, "option_id": str, "address_id": str}, []string{"quote_id", "option_id"}), HandlerCtx: a.toolFabricationPrepare},
		{Name: "fabrication_order_submit", Description: "Submit an approved draft order to its print service. This is the only step that spends money. Refused unless a person approved the draft in the Design panel or it is within the operator's auto-order policy. Runs at most once per order.",
			InputSchema: schemaObject(map[string]any{"order_id": id}, []string{"order_id"}), HandlerCtx: a.toolFabricationSubmit},
		{Name: "fabrication_orders", Description: "List fabrication orders for a design (or all designs) with status, totals, approval, provider order id and tracking. refresh=true polls the providers now.",
			InputSchema: schemaObject(map[string]any{"design_id": id, "status": str, "refresh": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}, nil), HandlerCtx: a.toolFabricationOrders},
		{Name: "fabrication_order_cancel", Description: "Cancel an order: drops a draft that was not submitted, or asks the provider to cancel a submitted order when its API supports it.",
			InputSchema: schemaObject(map[string]any{"order_id": id}, []string{"order_id"}), HandlerCtx: a.toolFabricationCancel},
	}
}

type providerView struct {
	boundProvider
	Allowed bool `json:"allowed_by_policy"`
}

func fabricationOverview(service *Service) (map[string]any, error) {
	settings, err := service.store.GetFabricationSettings(service.project)
	if err != nil {
		return nil, err
	}
	providers := []providerView{}
	for _, p := range service.fabricationProviders() {
		providers = append(providers, providerView{p, settings.providerAllowed(p.Slug)})
	}
	addresses := []map[string]any{}
	for _, a := range settings.Addresses {
		addresses = append(addresses, map[string]any{"id": a.ID, "label": a.Label, "country": a.Country, "default": a.ID == settings.DefaultAddressID})
	}
	supported := []ProviderCaps{}
	for _, slug := range []string{"craftcloud", "slant3d", "shapeways", "treatstock", "sculpteo"} {
		p, _ := providerFor(slug)
		supported = append(supported, p.Caps())
	}
	return map[string]any{"providers": providers, "supported": supported, "policy": map[string]any{
		"auto_order": settings.AutoOrder, "max_order_total": settings.MaxOrderTotal, "monthly_budget": settings.MonthlyBudget,
		"currency": settings.Currency, "country": settings.Country, "allowed_providers": settings.AllowedProviders,
		"price_drift_tolerance": priceDriftTolerance, "addresses": addresses}}, nil
}

func (a *App) toolFabricationProviders(_ context.Context, app *sdk.AppCtx, _ map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return fabricationOverview(service)
}

func quoteArgsFrom(args map[string]any) QuoteArgs {
	q := QuoteArgs{DesignID: int64Arg(args, "design_id"), RevisionID: int64Arg(args, "revision_id"), PartIDs: stringSliceArg(args, "part_ids"),
		Quantity: intArg(args, "quantity"), Currency: stringArg(args, "currency"), Country: stringArg(args, "country"),
		Material: stringArg(args, "material"), MaxOptions: intArg(args, "max_options"), Quantities: map[string]int{}}
	if m, ok := args["quantities"].(map[string]any); ok {
		for k := range m {
			q.Quantities[k] = int(int64Arg(m, k))
		}
	}
	if list, ok := args["connection_ids"].([]any); ok {
		for _, v := range list {
			q.ConnectionIDs = append(q.ConnectionIDs, int64Arg(map[string]any{"v": v}, "v"))
		}
	}
	return q
}

func (a *App) toolFabricationQuote(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	quote, err := service.CreateQuote(ctx, quoteArgsFrom(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"quote": quote, "next": "Pick an option id and call fabrication_order_prepare to create a draft. Nothing has been ordered."}, nil
}

func (a *App) toolFabricationQuotes(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	quotes, err := service.store.ListFabricationQuotes(service.project, int64Arg(args, "design_id"), int64Arg(args, "revision_id"), intArg(args, "limit"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"quotes": quotes}, nil
}

func (a *App) toolFabricationPrepare(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	order, err := service.PrepareOrder(ctx, int64Arg(args, "quote_id"), stringArg(args, "option_id"), stringArg(args, "address_id"), "agent")
	if err != nil {
		return nil, err
	}
	return map[string]any{"order": order, "next": ternary(order.Status == orderApproved,
		"Within the auto-order policy. Call fabrication_order_submit to place it.",
		"Waiting for a person to approve it in the Design panel ("+strings.Join(order.Policy.Reasons, "; ")+").")}, nil
}

func (a *App) toolFabricationSubmit(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	order, err := service.Submit(ctx, int64Arg(args, "order_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"order": order}, nil
}

func (a *App) toolFabricationOrders(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	var statuses []string
	if st := stringArg(args, "status"); st != "" {
		statuses = strings.Split(st, ",")
	}
	orders, err := service.store.ListFabricationOrders(service.project, int64Arg(args, "design_id"), statuses, intArg(args, "limit"))
	if err != nil {
		return nil, err
	}
	if refresh, _ := args["refresh"].(bool); refresh {
		for i, o := range orders {
			if updated, err := service.RefreshOrder(ctx, o.ID); err == nil && updated != nil {
				orders[i] = *updated
			}
		}
	}
	return map[string]any{"orders": orders}, nil
}

func (a *App) toolFabricationCancel(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	order, err := service.CancelOrder(ctx, int64Arg(args, "order_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"order": order}, nil
}

// ---- HTTP routes for the panel --------------------------------------------
//
// Settings, approval and decline exist only here, never as MCP tools, so an
// agent cannot raise its own limits or approve its own order.

func requestActor(r *http.Request) map[string]any {
	actor := map[string]any{"via": "panel"}
	if p, err := sdk.PrincipalFromRequest(r); err == nil && p != nil {
		if p.UserID > 0 {
			actor["user_id"] = p.UserID
		}
		if p.Email != "" {
			actor["email"] = p.Email
		} else if p.SubjectEmail != "" {
			actor["email"] = p.SubjectEmail
		}
	}
	return actor
}

func actorLabel(actor map[string]any) string {
	if e, ok := actor["email"].(string); ok && e != "" {
		return e
	}
	if id, ok := actor["user_id"].(int64); ok {
		return "user:" + strconv.FormatInt(id, 10)
	}
	return "panel"
}

func (a *App) handleFabrication(w http.ResponseWriter, r *http.Request) {
	service, err := a.service(a.projectContext(r))
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/fabrication"))
	q := r.URL.Query()
	queryInt := func(k string) int64 { n, _ := strconv.ParseInt(q.Get(k), 10, 64); return n }
	reply := func(v any, err error) {
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
	if len(parts) == 0 {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	switch parts[0] {
	case "providers":
		reply(fabricationOverview(service))
	case "settings":
		switch r.Method {
		case http.MethodGet:
			reply(service.store.GetFabricationSettings(service.project))
		case http.MethodPut:
			var settings FabricationSettings
			if err := decodeBody(r, &settings); err != nil {
				writeHTTPError(w, err)
				return
			}
			if err := settings.normalize(); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := service.store.SaveFabricationSettings(service.project, settings, actorLabel(requestActor(r))); err != nil {
				writeHTTPError(w, err)
				return
			}
			reply(settings, nil)
		default:
			writeMethodNotAllowed(w, http.MethodGet, http.MethodPut)
		}
	case "quotes":
		switch r.Method {
		case http.MethodGet:
			quotes, err := service.store.ListFabricationQuotes(service.project, queryInt("design_id"), queryInt("revision_id"), int(queryInt("limit")))
			reply(map[string]any{"quotes": quotes}, err)
		case http.MethodPost:
			var args map[string]any
			if err := decodeBody(r, &args); err != nil {
				writeHTTPError(w, err)
				return
			}
			quote, err := service.CreateQuote(r.Context(), quoteArgsFrom(args))
			reply(map[string]any{"quote": quote}, err)
		default:
			writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
	case "orders":
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				var statuses []string
				if st := q.Get("status"); st != "" {
					statuses = strings.Split(st, ",")
				}
				orders, err := service.store.ListFabricationOrders(service.project, queryInt("design_id"), statuses, int(queryInt("limit")))
				reply(map[string]any{"orders": orders}, err)
			case http.MethodPost:
				var body struct {
					QuoteID   int64  `json:"quote_id"`
					OptionID  string `json:"option_id"`
					AddressID string `json:"address_id"`
				}
				if err := decodeBody(r, &body); err != nil {
					writeHTTPError(w, err)
					return
				}
				order, err := service.PrepareOrder(r.Context(), body.QuoteID, body.OptionID, body.AddressID, actorLabel(requestActor(r)))
				reply(map[string]any{"order": order}, err)
			default:
				writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
			}
			return
		}
		orderID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || orderID <= 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid order id")
			return
		}
		if len(parts) == 2 {
			reply(service.store.GetFabricationOrder(service.project, orderID))
			return
		}
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var order *FabricationOrder
		switch parts[2] {
		case "approve":
			var body struct {
				ConfirmTotal *float64 `json:"confirm_total"`
			}
			if err := decodeBody(r, &body); err != nil {
				writeHTTPError(w, err)
				return
			}
			if body.ConfirmTotal == nil {
				writeJSONError(w, http.StatusBadRequest, "confirm_total is required")
				return
			}
			order, err = service.Approve(orderID, *body.ConfirmTotal, requestActor(r))
		case "decline":
			order, err = service.Decline(orderID, requestActor(r))
		case "submit":
			order, err = service.Submit(r.Context(), orderID)
		case "refresh":
			order, err = service.RefreshOrder(r.Context(), orderID)
		case "cancel":
			order, err = service.CancelOrder(r.Context(), orderID)
		default:
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}
		reply(map[string]any{"order": order}, err)
	default:
		writeJSONError(w, http.StatusNotFound, "not found")
	}
}
