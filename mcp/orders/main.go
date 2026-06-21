package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: orders
display_name: Orders
version: 0.1.0
description: Physical commerce order ledger for fulfillment, shipment tracking, and returns.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
    - platform.connections.read_credentials
  apps:
    - name: catalog
      optional: false
    - name: billing
      optional: true
    - name: checkout
      optional: true
  integrations:
    - role: fulfillment_provider
      kind: integration
      compatible_slugs: [huboo, hive-fulfillment, byrd]
      required: false
provides:
  http_routes:
    - prefix: /
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/orders
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/orders.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("orders requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("orders mounted",
		"version", "0.1.0",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// HTTP routes.

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/orders", Handler: a.handleHTTPOrdersCollection},
		{Pattern: "/orders/", Handler: a.handleHTTPOrderItem},
		{Pattern: "/fulfillments", Handler: a.handleHTTPFulfillmentsCollection},
		{Pattern: "/fulfillments/", Handler: a.handleHTTPFulfillmentItem},
		{Pattern: "/shipments", Handler: a.handleHTTPShipmentsCollection},
		{Pattern: "/returns", Handler: a.handleHTTPReturnsCollection},
		{Pattern: "/returns/", Handler: a.handleHTTPReturnItem},
	}
}

func (a *App) handleHTTPOrdersCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPOrdersSearch(w, r)
	case http.MethodPost:
		a.handleHTTPOrderCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPOrderItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/orders/")
	parts := strings.Split(rest, "/")
	if len(parts) >= 2 {
		switch parts[1] {
		case "cancel":
			if r.Method == http.MethodPost {
				a.handleHTTPOrderCancel(w, r)
				return
			}
		case "status":
			if r.Method == http.MethodPatch {
				a.handleHTTPOrderStatus(w, r)
				return
			}
		case "events":
			if r.Method == http.MethodGet {
				a.handleHTTPOrderEvents(w, r)
				return
			}
		case "shipments":
			if r.Method == http.MethodGet {
				a.handleHTTPOrderShipments(w, r)
				return
			}
		}
	}
	if r.Method == http.MethodGet {
		a.handleHTTPOrderGet(w, r)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleHTTPFulfillmentsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a.handleHTTPFulfillmentCreate(w, r)
}

func (a *App) handleHTTPFulfillmentItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/fulfillments/")
	parts := strings.Split(rest, "/")
	if len(parts) >= 2 && parts[1] == "sync" && r.Method == http.MethodPost {
		a.handleHTTPFulfillmentSync(w, r)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleHTTPShipmentsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a.handleHTTPShipmentsList(w, r)
}

func (a *App) handleHTTPReturnsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a.handleHTTPReturnCreate(w, r)
}

func (a *App) handleHTTPReturnItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a.handleHTTPReturnGet(w, r)
}

// MCP tools.

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "orders_create",
			Description: "Create a NEW durable physical order when no order id exists yet. Use this for paid invoice-to-fulfillment handoffs. Args: source, source_ref, invoice_id, customer_email, customer_name, addresses, totals, payment_status, order_status, fulfillment_status, items, metadata.",
			InputSchema: schemaObject(map[string]any{
				"source":              map[string]any{"type": "string"},
				"source_ref":          map[string]any{"type": "string"},
				"checkout_session_id": map[string]any{"type": "integer"},
				"cart_id":             map[string]any{"type": "integer"},
				"invoice_id":          map[string]any{"type": "integer"},
				"customer_id":         map[string]any{"type": "integer"},
				"customer_email":      map[string]any{"type": "string"},
				"customer_name":       map[string]any{"type": "string"},
				"shipping_address":    map[string]any{"type": "object"},
				"billing_address":     map[string]any{"type": "object"},
				"currency":            map[string]any{"type": "string"},
				"subtotal_cents":      map[string]any{"type": "integer"},
				"tax_cents":           map[string]any{"type": "integer"},
				"shipping_cents":      map[string]any{"type": "integer"},
				"discount_cents":      map[string]any{"type": "integer"},
				"total_cents":         map[string]any{"type": "integer"},
				"payment_status":      map[string]any{"type": "string"},
				"order_status":        map[string]any{"type": "string"},
				"fulfillment_status":  map[string]any{"type": "string"},
				"source_payload":      map[string]any{"type": "object"},
				"items":               map[string]any{"type": "array"},
				"metadata":            map[string]any{"type": "object"},
			}, []string{"items"}),
			Handler: a.toolOrdersCreate,
		},
		{
			Name:        "orders_create_from_checkout",
			Description: "Create an order from a checkout session. Args: session_id, source_ref, metadata.",
			InputSchema: schemaObject(map[string]any{
				"session_id": map[string]any{"type": "integer"},
				"source_ref": map[string]any{"type": "string"},
				"metadata":   map[string]any{"type": "object"},
			}, []string{"session_id"}),
			Handler: a.toolOrdersCreateFromCheckout,
		},
		{
			Name:        "orders_import_from_channel",
			Description: "Import an order from an external sales channel payload. Args: source, source_ref, payload.",
			InputSchema: schemaObject(map[string]any{
				"source":     map[string]any{"type": "string"},
				"source_ref": map[string]any{"type": "string"},
				"payload":    map[string]any{"type": "object"},
				"items":      map[string]any{"type": "array"},
			}, []string{"source", "source_ref"}),
			Handler: a.toolOrdersImportFromChannel,
		},
		{
			Name:        "orders_get",
			Description: "Fetch one order by id or order_number.",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "integer"},
				"order_number": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolOrdersGet,
		},
		{
			Name:        "orders_search",
			Description: "Search orders. Args: q, source, order_status, payment_status, fulfillment_status, limit.",
			InputSchema: schemaObject(map[string]any{
				"q":                  map[string]any{"type": "string"},
				"source":             map[string]any{"type": "string"},
				"order_status":       map[string]any{"type": "string"},
				"payment_status":     map[string]any{"type": "string"},
				"fulfillment_status": map[string]any{"type": "string"},
				"limit":              map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolOrdersSearch,
		},
		{
			Name:        "orders_update_status",
			Description: "Update an EXISTING order only. Requires id from orders_create, orders_get, or orders_search; do not use this to create a new order. Args: id, order_status, payment_status, fulfillment_status, actor, note.",
			InputSchema: schemaObject(map[string]any{
				"id":                 map[string]any{"type": "integer"},
				"order_status":       map[string]any{"type": "string"},
				"payment_status":     map[string]any{"type": "string"},
				"fulfillment_status": map[string]any{"type": "string"},
				"actor":              map[string]any{"type": "string"},
				"note":               map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolOrdersUpdateStatus,
		},
		{
			Name:        "orders_cancel",
			Description: "Cancel an order. Args: id, reason, actor.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"reason": map[string]any{"type": "string"},
				"actor":  map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolOrdersCancel,
		},
		{
			Name:        "fulfillments_create",
			Description: "Create or submit a fulfillment. Args: order_id, provider, warehouse_id, service, submit, payload.",
			InputSchema: schemaObject(map[string]any{
				"order_id":     map[string]any{"type": "integer"},
				"provider":     map[string]any{"type": "string"},
				"warehouse_id": map[string]any{"type": "string"},
				"service":      map[string]any{"type": "string"},
				"submit":       map[string]any{"type": "boolean"},
				"payload":      map[string]any{"type": "object"},
				"metadata":     map[string]any{"type": "object"},
			}, []string{"order_id", "provider"}),
			Handler: a.toolFulfillmentsCreate,
		},
		{
			Name:        "fulfillments_sync",
			Description: "Refresh a fulfillment's provider status. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolFulfillmentsSync,
		},
		{
			Name:        "shipments_list",
			Description: "List shipments for an order. Args: order_id.",
			InputSchema: schemaObject(map[string]any{
				"order_id": map[string]any{"type": "integer"},
			}, []string{"order_id"}),
			Handler: a.toolShipmentsList,
		},
		{
			Name:        "shipments_sync_tracking",
			Description: "Refresh tracking for an order or fulfillment. Args: order_id, fulfillment_id.",
			InputSchema: schemaObject(map[string]any{
				"order_id":       map[string]any{"type": "integer"},
				"fulfillment_id": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolShipmentsSyncTracking,
		},
		{
			Name:        "returns_create",
			Description: "Create a return record. Args: order_id, provider, reason, payload, metadata.",
			InputSchema: schemaObject(map[string]any{
				"order_id": map[string]any{"type": "integer"},
				"provider": map[string]any{"type": "string"},
				"reason":   map[string]any{"type": "string"},
				"payload":  map[string]any{"type": "object"},
				"metadata": map[string]any{"type": "object"},
			}, []string{"order_id"}),
			Handler: a.toolReturnsCreate,
		},
		{
			Name:        "returns_get",
			Description: "Fetch one return. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolReturnsGet,
		},
		{
			Name:        "order_events_list",
			Description: "List events for an order. Args: order_id, limit.",
			InputSchema: schemaObject(map[string]any{
				"order_id": map[string]any{"type": "integer"},
				"limit":    map[string]any{"type": "integer"},
			}, []string{"order_id"}),
			Handler: a.toolOrderEventsList,
		},
	}
}

func main() { sdk.Run(&App{}) }

// Tool handlers.

func (a *App) toolOrdersCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	order, err := dbOrderCreate(ctx, pid, args, "order.created")
	if err != nil {
		return nil, err
	}
	emitOrder(ctx, "order.created", order)
	return map[string]any{"order": order}, nil
}

func (a *App) toolOrdersCreateFromCheckout(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sessionID := int64Arg(args, "session_id")
	if sessionID == 0 {
		return nil, errors.New("session_id required")
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable (checkout app must be installed)")
	}
	cctx := ctx.WithProject(pid)
	var sessResp struct {
		Session map[string]any `json:"session"`
	}
	if err := cctx.PlatformAPI().CallAppResult("checkout", "checkout_get", map[string]any{"session_id": sessionID}, &sessResp); err != nil {
		return nil, fmt.Errorf("checkout session lookup failed: %w", err)
	}
	if sessResp.Session == nil {
		return nil, errors.New("checkout session not found")
	}
	cartID := int64Arg(sessResp.Session, "cart_id")
	var cartResp struct {
		Cart map[string]any `json:"cart"`
	}
	if cartID != 0 {
		_ = cctx.PlatformAPI().CallAppResult("checkout", "cart_get", map[string]any{"cart_id": cartID}, &cartResp)
	}
	body := map[string]any{
		"source":              "checkout",
		"source_ref":          firstNonEmpty(strArg(args, "source_ref"), fmt.Sprintf("checkout_session:%d", sessionID)),
		"checkout_session_id": sessionID,
		"cart_id":             cartID,
		"invoice_id":          int64Arg(sessResp.Session, "invoice_id"),
		"customer_email":      strArg(sessResp.Session, "email"),
		"customer_name":       strArg(sessResp.Session, "customer_name"),
		"shipping_address":    sessResp.Session["shipping_address"],
		"billing_address":     sessResp.Session["billing_address"],
		"currency":            strArg(sessResp.Session, "currency"),
		"subtotal_cents":      int64Arg(sessResp.Session, "subtotal_cents"),
		"tax_cents":           int64Arg(sessResp.Session, "tax_cents"),
		"total_cents":         int64Arg(sessResp.Session, "total_cents"),
		"payment_status":      "paid",
		"order_status":        "paid",
		"fulfillment_status":  "unsubmitted",
		"source_payload":      map[string]any{"session": sessResp.Session, "cart": cartResp.Cart},
		"metadata":            args["metadata"],
	}
	if cartResp.Cart != nil {
		body["items"] = cartResp.Cart["items"]
		if body["currency"] == "" {
			body["currency"] = cartResp.Cart["currency"]
		}
	}
	order, err := dbOrderCreate(ctx, pid, body, "order.created_from_checkout")
	if err != nil {
		return nil, err
	}
	emitOrder(ctx, "order.created", order)
	return map[string]any{"order": order}, nil
}

func (a *App) toolOrdersImportFromChannel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	source := strings.TrimSpace(strArg(args, "source"))
	sourceRef := strings.TrimSpace(strArg(args, "source_ref"))
	if source == "" || sourceRef == "" {
		return nil, errors.New("source and source_ref required")
	}
	payload, _ := args["payload"].(map[string]any)
	body := map[string]any{
		"source":             source,
		"source_ref":         sourceRef,
		"source_payload":     payload,
		"items":              args["items"],
		"customer_email":     stringFromMap(payload, "email", "customer_email"),
		"customer_name":      stringFromMap(payload, "customer_name", "name"),
		"shipping_address":   firstAny(payload["shipping_address"], payload["shippingAddress"]),
		"billing_address":    firstAny(payload["billing_address"], payload["billingAddress"]),
		"currency":           firstNonEmpty(stringFromMap(payload, "currency"), configString(ctx, "default_currency", "USD")),
		"subtotal_cents":     int64FromMap(payload, "subtotal_cents", "subtotal"),
		"tax_cents":          int64FromMap(payload, "tax_cents", "tax"),
		"shipping_cents":     int64FromMap(payload, "shipping_cents", "shipping"),
		"discount_cents":     int64FromMap(payload, "discount_cents", "discount"),
		"total_cents":        int64FromMap(payload, "total_cents", "total"),
		"payment_status":     firstNonEmpty(stringFromMap(payload, "payment_status"), "paid"),
		"order_status":       firstNonEmpty(stringFromMap(payload, "order_status"), "paid"),
		"fulfillment_status": "unsubmitted",
	}
	order, err := dbOrderCreate(ctx, pid, body, "order.imported")
	if err != nil {
		return nil, err
	}
	emitOrder(ctx, "order.imported", order)
	return map[string]any{"order": order}, nil
}

func (a *App) toolOrdersGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	order, err := resolveOrder(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, errors.New("order not found")
	}
	return map[string]any{"order": order}, nil
}

func (a *App) toolOrdersSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbOrdersSearch(ctx.AppDB(), pid, orderFilters{
		query:             strArg(args, "q"),
		source:            strArg(args, "source"),
		orderStatus:       strArg(args, "order_status"),
		paymentStatus:     strArg(args, "payment_status"),
		fulfillmentStatus: strArg(args, "fulfillment_status"),
		limit:             clampLimit(int(int64Arg(args, "limit")), 200),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"orders": out, "count": len(out)}, nil
}

func (a *App) toolOrdersUpdateStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	order, err := dbOrderUpdateStatus(ctx.AppDB(), pid, int64Arg(args, "id"), statusPatch{
		OrderStatus:       strArg(args, "order_status"),
		PaymentStatus:     strArg(args, "payment_status"),
		FulfillmentStatus: strArg(args, "fulfillment_status"),
		Actor:             strArg(args, "actor"),
		Note:              strArg(args, "note"),
	})
	if err != nil {
		return nil, err
	}
	emitOrder(ctx, "order.updated", order)
	return map[string]any{"order": order}, nil
}

func (a *App) toolOrdersCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	order, err := dbOrderCancel(ctx.AppDB(), pid, int64Arg(args, "id"), strArg(args, "reason"), strArg(args, "actor"))
	if err != nil {
		return nil, err
	}
	emitOrder(ctx, "order.cancelled", order)
	return map[string]any{"order": order}, nil
}

func (a *App) toolFulfillmentsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	f, order, err := dbFulfillmentCreate(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	emitOrder(ctx, "fulfillment.created", order)
	return map[string]any{"fulfillment": f, "order": order}, nil
}

func (a *App) toolFulfillmentsSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	f, err := dbFulfillmentGet(ctx.AppDB(), pid, int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("fulfillment not found")
	}
	return map[string]any{"fulfillment": f, "synced": false, "message": "provider status sync is provider-specific; stored fulfillment returned"}, nil
}

func (a *App) toolShipmentsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbShipmentsList(ctx.AppDB(), pid, int64Arg(args, "order_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"shipments": out, "count": len(out)}, nil
}

func (a *App) toolShipmentsSyncTracking(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	orderID := int64Arg(args, "order_id")
	fulfillmentID := int64Arg(args, "fulfillment_id")
	shipments, err := dbTrackingSync(ctx, pid, orderID, fulfillmentID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"shipments": shipments, "count": len(shipments)}, nil
}

func (a *App) toolReturnsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	ret, order, err := dbReturnCreate(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	emitOrder(ctx, "return.created", order)
	return map[string]any{"return": ret, "order": order}, nil
}

func (a *App) toolReturnsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	ret, err := dbReturnGet(ctx.AppDB(), pid, int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	if ret == nil {
		return nil, errors.New("return not found")
	}
	return map[string]any{"return": ret}, nil
}

func (a *App) toolOrderEventsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	events, err := dbEventsList(ctx.AppDB(), pid, int64Arg(args, "order_id"), clampLimit(int(int64Arg(args, "limit")), 200))
	if err != nil {
		return nil, err
	}
	return map[string]any{"events": events, "count": len(events)}, nil
}

// HTTP handlers.

func (a *App) handleHTTPOrdersSearch(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := dbOrdersSearch(ctx.AppDB(), pid, orderFilters{
		query:             r.URL.Query().Get("q"),
		source:            r.URL.Query().Get("source"),
		orderStatus:       r.URL.Query().Get("order_status"),
		paymentStatus:     r.URL.Query().Get("payment_status"),
		fulfillmentStatus: r.URL.Query().Get("fulfillment_status"),
		limit:             clampLimit(atoiOr(r.URL.Query().Get("limit"), 50), 200),
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"orders": out, "count": len(out)})
}

func (a *App) handleHTTPOrderCreate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	order, err := dbOrderCreate(ctx, pid, body, "order.created")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitOrder(ctx, "order.created", order)
	httpJSON(w, map[string]any{"order": order})
}

func (a *App) handleHTTPOrderGet(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	order, err := dbOrderGet(ctx.AppDB(), pid, pathInt(r.URL.Path, "/orders/"), true)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if order == nil {
		httpErr(w, http.StatusNotFound, "order not found")
		return
	}
	httpJSON(w, map[string]any{"order": order})
}

func (a *App) handleHTTPOrderStatus(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	body["id"] = pathInt(r.URL.Path, "/orders/")
	order, err := dbOrderUpdateStatus(ctx.AppDB(), pid, int64Arg(body, "id"), statusPatch{
		OrderStatus:       strArg(body, "order_status"),
		PaymentStatus:     strArg(body, "payment_status"),
		FulfillmentStatus: strArg(body, "fulfillment_status"),
		Actor:             strArg(body, "actor"),
		Note:              strArg(body, "note"),
	})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"order": order})
}

func (a *App) handleHTTPOrderCancel(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	order, err := dbOrderCancel(ctx.AppDB(), pid, pathInt(r.URL.Path, "/orders/"), strArg(body, "reason"), strArg(body, "actor"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"order": order})
}

func (a *App) handleHTTPFulfillmentCreate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	f, order, err := dbFulfillmentCreate(ctx, pid, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"fulfillment": f, "order": order})
}

func (a *App) handleHTTPFulfillmentSync(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := dbFulfillmentGet(ctx.AppDB(), pid, pathInt(r.URL.Path, "/fulfillments/"))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if f == nil {
		httpErr(w, http.StatusNotFound, "fulfillment not found")
		return
	}
	httpJSON(w, map[string]any{"fulfillment": f, "synced": false})
}

func (a *App) handleHTTPShipmentsList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	orderID, _ := strconv.ParseInt(r.URL.Query().Get("order_id"), 10, 64)
	out, err := dbShipmentsList(ctx.AppDB(), pid, orderID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"shipments": out, "count": len(out)})
}

func (a *App) handleHTTPOrderShipments(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := dbShipmentsList(ctx.AppDB(), pid, pathInt(r.URL.Path, "/orders/"))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"shipments": out, "count": len(out)})
}

func (a *App) handleHTTPReturnCreate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	ret, order, err := dbReturnCreate(ctx, pid, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"return": ret, "order": order})
}

func (a *App) handleHTTPReturnGet(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ret, err := dbReturnGet(ctx.AppDB(), pid, pathInt(r.URL.Path, "/returns/"))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ret == nil {
		httpErr(w, http.StatusNotFound, "return not found")
		return
	}
	httpJSON(w, map[string]any{"return": ret})
}

func (a *App) handleHTTPOrderEvents(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := dbEventsList(ctx.AppDB(), pid, pathInt(r.URL.Path, "/orders/"), 100)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"events": out, "count": len(out)})
}

// Types.

type Order struct {
	ID                int64           `json:"id"`
	ProjectID         string          `json:"project_id"`
	OrderNumber       string          `json:"order_number"`
	Source            string          `json:"source"`
	SourceRef         string          `json:"source_ref,omitempty"`
	SourcePayload     json.RawMessage `json:"source_payload,omitempty"`
	CheckoutSessionID *int64          `json:"checkout_session_id,omitempty"`
	CartID            *int64          `json:"cart_id,omitempty"`
	InvoiceID         *int64          `json:"invoice_id,omitempty"`
	CustomerID        *int64          `json:"customer_id,omitempty"`
	CustomerEmail     string          `json:"customer_email,omitempty"`
	CustomerName      string          `json:"customer_name,omitempty"`
	ShippingAddress   json.RawMessage `json:"shipping_address,omitempty"`
	BillingAddress    json.RawMessage `json:"billing_address,omitempty"`
	Currency          string          `json:"currency"`
	SubtotalCents     int64           `json:"subtotal_cents"`
	TaxCents          int64           `json:"tax_cents"`
	ShippingCents     int64           `json:"shipping_cents"`
	DiscountCents     int64           `json:"discount_cents"`
	TotalCents        int64           `json:"total_cents"`
	PaymentStatus     string          `json:"payment_status"`
	OrderStatus       string          `json:"order_status"`
	FulfillmentStatus string          `json:"fulfillment_status"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
	PaidAt            string          `json:"paid_at,omitempty"`
	CancelledAt       string          `json:"cancelled_at,omitempty"`
	FulfilledAt       string          `json:"fulfilled_at,omitempty"`
	DeliveredAt       string          `json:"delivered_at,omitempty"`
	Items             []*OrderItem    `json:"items,omitempty"`
	Fulfillments      []*Fulfillment  `json:"fulfillments,omitempty"`
	Shipments         []*Shipment     `json:"shipments,omitempty"`
	Returns           []*Return       `json:"returns,omitempty"`
	Events            []*OrderEvent   `json:"events,omitempty"`
}

type OrderItem struct {
	ID               int64           `json:"id"`
	OrderID          int64           `json:"order_id"`
	Position         int             `json:"position"`
	CatalogProductID *int64          `json:"catalog_product_id,omitempty"`
	CatalogPriceID   *int64          `json:"catalog_price_id,omitempty"`
	SKU              string          `json:"sku,omitempty"`
	Title            string          `json:"title"`
	Quantity         float64         `json:"quantity"`
	UnitAmountCents  int64           `json:"unit_amount_cents"`
	Currency         string          `json:"currency"`
	SourceItemRef    string          `json:"source_item_ref,omitempty"`
	FulfillmentSKU   string          `json:"fulfillment_sku,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
}

type Fulfillment struct {
	ID              int64           `json:"id"`
	ProjectID       string          `json:"project_id"`
	OrderID         int64           `json:"order_id"`
	Provider        string          `json:"provider"`
	ProviderOrderID string          `json:"provider_order_id,omitempty"`
	WarehouseID     string          `json:"warehouse_id,omitempty"`
	Service         string          `json:"service,omitempty"`
	Status          string          `json:"status"`
	RequestPayload  json.RawMessage `json:"request_payload,omitempty"`
	ResponsePayload json.RawMessage `json:"response_payload,omitempty"`
	Error           string          `json:"error,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
	SubmittedAt     string          `json:"submitted_at,omitempty"`
	AcceptedAt      string          `json:"accepted_at,omitempty"`
	CancelledAt     string          `json:"cancelled_at,omitempty"`
}

type Shipment struct {
	ID                 int64           `json:"id"`
	ProjectID          string          `json:"project_id"`
	OrderID            int64           `json:"order_id"`
	FulfillmentID      *int64          `json:"fulfillment_id,omitempty"`
	Provider           string          `json:"provider,omitempty"`
	ProviderShipmentID string          `json:"provider_shipment_id,omitempty"`
	Carrier            string          `json:"carrier,omitempty"`
	Service            string          `json:"service,omitempty"`
	TrackingNumber     string          `json:"tracking_number,omitempty"`
	TrackingURL        string          `json:"tracking_url,omitempty"`
	Status             string          `json:"status"`
	RawPayload         json.RawMessage `json:"raw_payload,omitempty"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	ShippedAt          string          `json:"shipped_at,omitempty"`
	DeliveredAt        string          `json:"delivered_at,omitempty"`
}

type Return struct {
	ID               int64           `json:"id"`
	ProjectID        string          `json:"project_id"`
	OrderID          int64           `json:"order_id"`
	Provider         string          `json:"provider,omitempty"`
	ProviderReturnID string          `json:"provider_return_id,omitempty"`
	Status           string          `json:"status"`
	Reason           string          `json:"reason,omitempty"`
	RequestPayload   json.RawMessage `json:"request_payload,omitempty"`
	ResponsePayload  json.RawMessage `json:"response_payload,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
	ReceivedAt       string          `json:"received_at,omitempty"`
	CompletedAt      string          `json:"completed_at,omitempty"`
}

type OrderEvent struct {
	ID        int64           `json:"id"`
	ProjectID string          `json:"project_id"`
	OrderID   int64           `json:"order_id"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Details   json.RawMessage `json:"details,omitempty"`
	CreatedAt string          `json:"created_at"`
}

// DB.

func dbOrderCreate(ctx *sdk.AppCtx, pid string, args map[string]any, eventAction string) (*Order, error) {
	itemsRaw := arrayArg(args, "items")
	if len(itemsRaw) == 0 {
		return nil, errors.New("items required")
	}
	items := normalizeItems(itemsRaw, strArg(args, "currency"))
	if len(items) == 0 {
		return nil, errors.New("at least one valid item required")
	}
	currency := strings.ToUpper(firstNonEmpty(strArg(args, "currency"), items[0].Currency, configString(ctx, "default_currency", "USD")))
	if !looksLikeISO4217(currency) {
		return nil, errors.New("currency must be a 3-letter ISO code")
	}
	subtotal := int64Arg(args, "subtotal_cents")
	if subtotal == 0 {
		for _, it := range items {
			subtotal += int64(float64(it.UnitAmountCents) * it.Quantity)
		}
	}
	tax := int64Arg(args, "tax_cents")
	shipping := int64Arg(args, "shipping_cents")
	discount := int64Arg(args, "discount_cents")
	total := int64Arg(args, "total_cents")
	if total == 0 {
		total = subtotal + tax + shipping - discount
	}
	source := firstNonEmpty(strArg(args, "source"), "manual")
	paymentStatus := firstNonEmpty(strArg(args, "payment_status"), "unpaid")
	orderStatus := firstNonEmpty(strArg(args, "order_status"), defaultOrderStatus(paymentStatus))
	fulfillmentStatus := firstNonEmpty(strArg(args, "fulfillment_status"), "unsubmitted")
	if err := validateStatuses(orderStatus, paymentStatus, fulfillmentStatus); err != nil {
		return nil, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	number, err := nextOrderNumberTx(tx, pid, configString(ctx, "order_number_format", "ORD-{yyyy}-{seq:04}"), configInt64(ctx, "order_seq_start", 1001))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var id int64
	err = tx.QueryRow(
		`INSERT INTO orders
		   (project_id, order_number, source, source_ref, source_payload,
		    checkout_session_id, cart_id, invoice_id, customer_id,
		    customer_email, customer_name, shipping_address, billing_address,
		    currency, subtotal_cents, tax_cents, shipping_cents, discount_cents, total_cents,
		    payment_status, order_status, fulfillment_status, metadata, paid_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id`,
		pid, number, source, nullStr(strArg(args, "source_ref")), jsonOrEmpty(args["source_payload"], "{}"),
		nullableInt64(int64Arg(args, "checkout_session_id")), nullableInt64(int64Arg(args, "cart_id")),
		nullableInt64(int64Arg(args, "invoice_id")), nullableInt64(int64Arg(args, "customer_id")),
		nullStr(strArg(args, "customer_email")), nullStr(strArg(args, "customer_name")),
		jsonOrEmpty(args["shipping_address"], "{}"), jsonOrEmpty(args["billing_address"], "{}"),
		currency, subtotal, tax, shipping, discount, total,
		paymentStatus, orderStatus, fulfillmentStatus, jsonOrEmpty(args["metadata"], "{}"),
		nullableTime(paymentStatus == "paid", now),
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "ux_orders_source") {
			return nil, fmt.Errorf("order source %s/%s already imported", source, strArg(args, "source_ref"))
		}
		return nil, err
	}
	for i, it := range items {
		if it.Currency == "" {
			it.Currency = currency
		}
		if it.Title == "" {
			return nil, fmt.Errorf("items[%d].title required", i)
		}
		if it.Quantity <= 0 {
			return nil, fmt.Errorf("items[%d].quantity must be positive", i)
		}
		_, err := tx.Exec(
			`INSERT INTO order_items
			   (order_id, position, catalog_product_id, catalog_price_id, sku, title,
			    quantity, unit_amount_cents, currency, source_item_ref, fulfillment_sku, metadata)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i, nullablePtr(it.CatalogProductID), nullablePtr(it.CatalogPriceID), nullStr(it.SKU), it.Title,
			it.Quantity, it.UnitAmountCents, strings.ToUpper(firstNonEmpty(it.Currency, currency)),
			nullStr(it.SourceItemRef), nullStr(it.FulfillmentSKU), jsonOrEmpty(it.Metadata, "{}"),
		)
		if err != nil {
			return nil, err
		}
	}
	if err := writeEventTx(tx, pid, id, "system", eventAction, map[string]any{
		"source":     source,
		"source_ref": strArg(args, "source_ref"),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbOrderGet(ctx.AppDB(), pid, id, true)
}

type orderFilters struct {
	query, source, orderStatus, paymentStatus, fulfillmentStatus string
	limit                                                        int
}

func dbOrdersSearch(db *sql.DB, pid string, f orderFilters) ([]*Order, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.source != "" {
		where = append(where, "source = ?")
		args = append(args, f.source)
	}
	if f.orderStatus != "" {
		where = append(where, "order_status = ?")
		args = append(args, f.orderStatus)
	}
	if f.paymentStatus != "" {
		where = append(where, "payment_status = ?")
		args = append(args, f.paymentStatus)
	}
	if f.fulfillmentStatus != "" {
		where = append(where, "fulfillment_status = ?")
		args = append(args, f.fulfillmentStatus)
	}
	if q := strings.TrimSpace(f.query); q != "" {
		where = append(where, "(order_number LIKE ? OR source_ref LIKE ? OR customer_email LIKE ? OR customer_name LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like, like)
	}
	limit := f.limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT `+orderSelectColumns()+`
		   FROM orders
		  WHERE `+strings.Join(where, " AND ")+`
		  ORDER BY updated_at DESC, id DESC
		  LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func dbOrderGet(db *sql.DB, pid string, id int64, nested bool) (*Order, error) {
	if id == 0 {
		return nil, nil
	}
	o, err := scanOrder(db.QueryRow(`SELECT `+orderSelectColumns()+` FROM orders WHERE id = ? AND project_id = ?`, id, pid))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if nested {
		if err := loadOrderNested(db, pid, o); err != nil {
			return nil, err
		}
	}
	return o, nil
}

func dbOrderGetByNumber(db *sql.DB, pid, number string, nested bool) (*Order, error) {
	if strings.TrimSpace(number) == "" {
		return nil, nil
	}
	o, err := scanOrder(db.QueryRow(`SELECT `+orderSelectColumns()+` FROM orders WHERE order_number = ? AND project_id = ?`, number, pid))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if nested {
		if err := loadOrderNested(db, pid, o); err != nil {
			return nil, err
		}
	}
	return o, nil
}

type statusPatch struct {
	OrderStatus, PaymentStatus, FulfillmentStatus, Actor, Note string
}

func dbOrderUpdateStatus(db *sql.DB, pid string, id int64, patch statusPatch) (*Order, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	order, err := dbOrderGet(db, pid, id, false)
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, errors.New("order not found")
	}
	orderStatus := firstNonEmpty(patch.OrderStatus, order.OrderStatus)
	paymentStatus := firstNonEmpty(patch.PaymentStatus, order.PaymentStatus)
	fulfillmentStatus := firstNonEmpty(patch.FulfillmentStatus, order.FulfillmentStatus)
	if err := validateStatuses(orderStatus, paymentStatus, fulfillmentStatus); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		`UPDATE orders
		    SET order_status = ?, payment_status = ?, fulfillment_status = ?,
		        paid_at = CASE WHEN ? = 'paid' AND paid_at IS NULL THEN CURRENT_TIMESTAMP ELSE paid_at END,
		        fulfilled_at = CASE WHEN ? = 'fulfilled' AND fulfilled_at IS NULL THEN CURRENT_TIMESTAMP ELSE fulfilled_at END,
		        delivered_at = CASE WHEN ? = 'delivered' AND delivered_at IS NULL THEN CURRENT_TIMESTAMP ELSE delivered_at END,
		        updated_at = CURRENT_TIMESTAMP
		  WHERE id = ? AND project_id = ?`,
		orderStatus, paymentStatus, fulfillmentStatus, paymentStatus, fulfillmentStatus, fulfillmentStatus, id, pid)
	if err != nil {
		return nil, err
	}
	if err := writeEventTx(tx, pid, id, actorOrSystem(patch.Actor), "order.status_updated", map[string]any{
		"order_status": orderStatus, "payment_status": paymentStatus, "fulfillment_status": fulfillmentStatus, "note": patch.Note,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbOrderGet(db, pid, id, true)
}

func dbOrderCancel(db *sql.DB, pid string, id int64, reason, actor string) (*Order, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	order, err := dbOrderGet(db, pid, id, false)
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, errors.New("order not found")
	}
	if order.FulfillmentStatus == "shipped" || order.FulfillmentStatus == "delivered" {
		return nil, fmt.Errorf("cannot cancel order with fulfillment_status=%s", order.FulfillmentStatus)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE orders
		    SET order_status = 'cancelled', fulfillment_status = 'cancelled',
		        cancelled_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		  WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		return nil, err
	}
	if err := writeEventTx(tx, pid, id, actorOrSystem(actor), "order.cancelled", map[string]any{"reason": reason}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbOrderGet(db, pid, id, true)
}

func dbFulfillmentCreate(ctx *sdk.AppCtx, pid string, args map[string]any) (*Fulfillment, *Order, error) {
	orderID := int64Arg(args, "order_id")
	provider := strings.TrimSpace(strArg(args, "provider"))
	if orderID == 0 || provider == "" {
		return nil, nil, errors.New("order_id and provider required")
	}
	order, err := dbOrderGet(ctx.AppDB(), pid, orderID, true)
	if err != nil {
		return nil, nil, err
	}
	if order == nil {
		return nil, nil, errors.New("order not found")
	}
	payload := args["payload"]
	if payload == nil {
		payload = fulfillmentPayload(order, args)
	}
	status := "queued"
	response := "{}"
	errText := ""
	providerOrderID := ""
	if boolArg(args, "submit") {
		resRaw, extID, callErr := submitFulfillment(ctx.WithProject(pid), provider, payload)
		response = string(resRaw)
		providerOrderID = extID
		if callErr != nil {
			status = "failed"
			errText = callErr.Error()
		} else {
			status = "submitted"
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(
		`INSERT INTO fulfillments
		   (project_id, order_id, provider, provider_order_id, warehouse_id, service, status,
		    request_payload, response_payload, error, metadata, submitted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id`,
		pid, orderID, provider, nullStr(providerOrderID), nullStr(strArg(args, "warehouse_id")),
		nullStr(strArg(args, "service")), status, jsonOrEmpty(payload, "{}"), response,
		nullStr(errText), jsonOrEmpty(args["metadata"], "{}"), nullableTime(status == "submitted", time.Now().UTC().Format(time.RFC3339)),
	).Scan(&id)
	if err != nil {
		return nil, nil, err
	}
	nextFulfillmentStatus := "queued"
	if status == "submitted" {
		nextFulfillmentStatus = "submitted"
	} else if status == "failed" {
		nextFulfillmentStatus = "failed"
	}
	if _, err := tx.Exec(
		`UPDATE orders SET fulfillment_status = ?, order_status = CASE
		    WHEN order_status IN ('paid', 'ready_to_fulfill') THEN 'fulfilling'
		    ELSE order_status END,
		    updated_at = CURRENT_TIMESTAMP
		  WHERE id = ? AND project_id = ?`, nextFulfillmentStatus, orderID, pid); err != nil {
		return nil, nil, err
	}
	if err := writeEventTx(tx, pid, orderID, "system", "fulfillment.created", map[string]any{
		"fulfillment_id": id, "provider": provider, "status": status, "submitted": boolArg(args, "submit"), "error": errText,
	}); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	f, err := dbFulfillmentGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, nil, err
	}
	order, err = dbOrderGet(ctx.AppDB(), pid, orderID, true)
	return f, order, err
}

func dbTrackingSync(ctx *sdk.AppCtx, pid string, orderID, fulfillmentID int64) ([]*Shipment, error) {
	if orderID == 0 && fulfillmentID == 0 {
		return nil, errors.New("order_id or fulfillment_id required")
	}
	if fulfillmentID != 0 {
		f, err := dbFulfillmentGet(ctx.AppDB(), pid, fulfillmentID)
		if err != nil {
			return nil, err
		}
		if f == nil {
			return nil, errors.New("fulfillment not found")
		}
		orderID = f.OrderID
	}
	return dbShipmentsList(ctx.AppDB(), pid, orderID)
}

func dbReturnCreate(ctx *sdk.AppCtx, pid string, args map[string]any) (*Return, *Order, error) {
	orderID := int64Arg(args, "order_id")
	if orderID == 0 {
		return nil, nil, errors.New("order_id required")
	}
	order, err := dbOrderGet(ctx.AppDB(), pid, orderID, false)
	if err != nil {
		return nil, nil, err
	}
	if order == nil {
		return nil, nil, errors.New("order not found")
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(
		`INSERT INTO returns
		   (project_id, order_id, provider, status, reason, request_payload, metadata)
		 VALUES (?, ?, ?, 'requested', ?, ?, ?)
		 RETURNING id`,
		pid, orderID, nullStr(strArg(args, "provider")), nullStr(strArg(args, "reason")),
		jsonOrEmpty(args["payload"], "{}"), jsonOrEmpty(args["metadata"], "{}"),
	).Scan(&id)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(
		`UPDATE orders SET order_status = 'returned', fulfillment_status = 'returned', updated_at = CURRENT_TIMESTAMP
		  WHERE id = ? AND project_id = ?`, orderID, pid); err != nil {
		return nil, nil, err
	}
	if err := writeEventTx(tx, pid, orderID, "system", "return.created", map[string]any{"return_id": id, "reason": strArg(args, "reason")}); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	ret, err := dbReturnGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, nil, err
	}
	order, err = dbOrderGet(ctx.AppDB(), pid, orderID, true)
	return ret, order, err
}

// Loaders and scanners.

type rowScanner interface {
	Scan(dest ...any) error
}

func orderSelectColumns() string {
	return `id, project_id, COALESCE(order_number,''), source, COALESCE(source_ref,''), source_payload,
	        checkout_session_id, cart_id, invoice_id, customer_id,
	        COALESCE(customer_email,''), COALESCE(customer_name,''), shipping_address, billing_address,
	        currency, subtotal_cents, tax_cents, shipping_cents, discount_cents, total_cents,
	        payment_status, order_status, fulfillment_status, metadata,
	        created_at, updated_at, paid_at, cancelled_at, fulfilled_at, delivered_at`
}

func scanOrder(row rowScanner) (*Order, error) {
	var o Order
	var checkoutID, cartID, invoiceID, customerID sql.NullInt64
	var paidAt, cancelledAt, fulfilledAt, deliveredAt sql.NullString
	var sourcePayload, shippingAddress, billingAddress, metadata string
	err := row.Scan(&o.ID, &o.ProjectID, &o.OrderNumber, &o.Source, &o.SourceRef, &sourcePayload,
		&checkoutID, &cartID, &invoiceID, &customerID,
		&o.CustomerEmail, &o.CustomerName, &shippingAddress, &billingAddress,
		&o.Currency, &o.SubtotalCents, &o.TaxCents, &o.ShippingCents, &o.DiscountCents, &o.TotalCents,
		&o.PaymentStatus, &o.OrderStatus, &o.FulfillmentStatus, &metadata,
		&o.CreatedAt, &o.UpdatedAt, &paidAt, &cancelledAt, &fulfilledAt, &deliveredAt)
	if err != nil {
		return nil, err
	}
	o.SourcePayload = json.RawMessage(sourcePayload)
	o.ShippingAddress = json.RawMessage(shippingAddress)
	o.BillingAddress = json.RawMessage(billingAddress)
	o.Metadata = json.RawMessage(metadata)
	o.CheckoutSessionID = ptrIfValid(checkoutID)
	o.CartID = ptrIfValid(cartID)
	o.InvoiceID = ptrIfValid(invoiceID)
	o.CustomerID = ptrIfValid(customerID)
	if paidAt.Valid {
		o.PaidAt = paidAt.String
	}
	if cancelledAt.Valid {
		o.CancelledAt = cancelledAt.String
	}
	if fulfilledAt.Valid {
		o.FulfilledAt = fulfilledAt.String
	}
	if deliveredAt.Valid {
		o.DeliveredAt = deliveredAt.String
	}
	return &o, nil
}

func loadOrderNested(db *sql.DB, pid string, o *Order) error {
	items, err := dbOrderItemsList(db, o.ID)
	if err != nil {
		return err
	}
	o.Items = items
	fulfillments, err := dbFulfillmentsList(db, pid, o.ID)
	if err != nil {
		return err
	}
	o.Fulfillments = fulfillments
	shipments, err := dbShipmentsList(db, pid, o.ID)
	if err != nil {
		return err
	}
	o.Shipments = shipments
	returns, err := dbReturnsList(db, pid, o.ID)
	if err != nil {
		return err
	}
	o.Returns = returns
	events, err := dbEventsList(db, pid, o.ID, 50)
	if err != nil {
		return err
	}
	o.Events = events
	return nil
}

func dbOrderItemsList(db *sql.DB, orderID int64) ([]*OrderItem, error) {
	rows, err := db.Query(
		`SELECT id, order_id, position, catalog_product_id, catalog_price_id, COALESCE(sku,''), title,
		        quantity, unit_amount_cents, currency, COALESCE(source_item_ref,''), COALESCE(fulfillment_sku,''),
		        metadata, created_at, updated_at
		   FROM order_items WHERE order_id = ? ORDER BY position ASC, id ASC`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*OrderItem
	for rows.Next() {
		var it OrderItem
		var productID, priceID sql.NullInt64
		var metadata string
		if err := rows.Scan(&it.ID, &it.OrderID, &it.Position, &productID, &priceID, &it.SKU, &it.Title,
			&it.Quantity, &it.UnitAmountCents, &it.Currency, &it.SourceItemRef, &it.FulfillmentSKU,
			&metadata, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.Metadata = json.RawMessage(metadata)
		it.CatalogProductID = ptrIfValid(productID)
		it.CatalogPriceID = ptrIfValid(priceID)
		out = append(out, &it)
	}
	return out, rows.Err()
}

func dbFulfillmentGet(db *sql.DB, pid string, id int64) (*Fulfillment, error) {
	if id == 0 {
		return nil, nil
	}
	f, err := scanFulfillment(db.QueryRow(
		`SELECT id, project_id, order_id, provider, COALESCE(provider_order_id,''), COALESCE(warehouse_id,''),
		        COALESCE(service,''), status, request_payload, response_payload, COALESCE(error,''), metadata,
		        created_at, updated_at, submitted_at, accepted_at, cancelled_at
		   FROM fulfillments WHERE id = ? AND project_id = ?`, id, pid))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return f, err
}

func dbFulfillmentsList(db *sql.DB, pid string, orderID int64) ([]*Fulfillment, error) {
	rows, err := db.Query(
		`SELECT id, project_id, order_id, provider, COALESCE(provider_order_id,''), COALESCE(warehouse_id,''),
		        COALESCE(service,''), status, request_payload, response_payload, COALESCE(error,''), metadata,
		        created_at, updated_at, submitted_at, accepted_at, cancelled_at
		   FROM fulfillments WHERE order_id = ? AND project_id = ? ORDER BY updated_at DESC`, orderID, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Fulfillment
	for rows.Next() {
		f, err := scanFulfillment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func scanFulfillment(row rowScanner) (*Fulfillment, error) {
	var f Fulfillment
	var submittedAt, acceptedAt, cancelledAt sql.NullString
	var requestPayload, responsePayload, metadata string
	err := row.Scan(&f.ID, &f.ProjectID, &f.OrderID, &f.Provider, &f.ProviderOrderID, &f.WarehouseID,
		&f.Service, &f.Status, &requestPayload, &responsePayload, &f.Error, &metadata,
		&f.CreatedAt, &f.UpdatedAt, &submittedAt, &acceptedAt, &cancelledAt)
	if err != nil {
		return nil, err
	}
	f.RequestPayload = json.RawMessage(requestPayload)
	f.ResponsePayload = json.RawMessage(responsePayload)
	f.Metadata = json.RawMessage(metadata)
	if submittedAt.Valid {
		f.SubmittedAt = submittedAt.String
	}
	if acceptedAt.Valid {
		f.AcceptedAt = acceptedAt.String
	}
	if cancelledAt.Valid {
		f.CancelledAt = cancelledAt.String
	}
	return &f, nil
}

func dbShipmentsList(db *sql.DB, pid string, orderID int64) ([]*Shipment, error) {
	if orderID == 0 {
		return []*Shipment{}, nil
	}
	rows, err := db.Query(
		`SELECT id, project_id, order_id, fulfillment_id, COALESCE(provider,''), COALESCE(provider_shipment_id,''),
		        COALESCE(carrier,''), COALESCE(service,''), COALESCE(tracking_number,''), COALESCE(tracking_url,''),
		        status, raw_payload, created_at, updated_at, shipped_at, delivered_at
		   FROM shipments WHERE order_id = ? AND project_id = ? ORDER BY updated_at DESC`, orderID, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Shipment
	for rows.Next() {
		var s Shipment
		var fulfillmentID sql.NullInt64
		var shippedAt, deliveredAt sql.NullString
		var rawPayload string
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.OrderID, &fulfillmentID, &s.Provider, &s.ProviderShipmentID,
			&s.Carrier, &s.Service, &s.TrackingNumber, &s.TrackingURL,
			&s.Status, &rawPayload, &s.CreatedAt, &s.UpdatedAt, &shippedAt, &deliveredAt); err != nil {
			return nil, err
		}
		s.RawPayload = json.RawMessage(rawPayload)
		s.FulfillmentID = ptrIfValid(fulfillmentID)
		if shippedAt.Valid {
			s.ShippedAt = shippedAt.String
		}
		if deliveredAt.Valid {
			s.DeliveredAt = deliveredAt.String
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

func dbReturnGet(db *sql.DB, pid string, id int64) (*Return, error) {
	if id == 0 {
		return nil, nil
	}
	ret, err := scanReturn(db.QueryRow(
		`SELECT id, project_id, order_id, COALESCE(provider,''), COALESCE(provider_return_id,''),
		        status, COALESCE(reason,''), request_payload, response_payload, metadata,
		        created_at, updated_at, received_at, completed_at
		   FROM returns WHERE id = ? AND project_id = ?`, id, pid))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return ret, err
}

func dbReturnsList(db *sql.DB, pid string, orderID int64) ([]*Return, error) {
	rows, err := db.Query(
		`SELECT id, project_id, order_id, COALESCE(provider,''), COALESCE(provider_return_id,''),
		        status, COALESCE(reason,''), request_payload, response_payload, metadata,
		        created_at, updated_at, received_at, completed_at
		   FROM returns WHERE order_id = ? AND project_id = ? ORDER BY updated_at DESC`, orderID, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Return
	for rows.Next() {
		ret, err := scanReturn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ret)
	}
	return out, rows.Err()
}

func scanReturn(row rowScanner) (*Return, error) {
	var ret Return
	var receivedAt, completedAt sql.NullString
	var requestPayload, responsePayload, metadata string
	err := row.Scan(&ret.ID, &ret.ProjectID, &ret.OrderID, &ret.Provider, &ret.ProviderReturnID,
		&ret.Status, &ret.Reason, &requestPayload, &responsePayload, &metadata,
		&ret.CreatedAt, &ret.UpdatedAt, &receivedAt, &completedAt)
	if err != nil {
		return nil, err
	}
	ret.RequestPayload = json.RawMessage(requestPayload)
	ret.ResponsePayload = json.RawMessage(responsePayload)
	ret.Metadata = json.RawMessage(metadata)
	if receivedAt.Valid {
		ret.ReceivedAt = receivedAt.String
	}
	if completedAt.Valid {
		ret.CompletedAt = completedAt.String
	}
	return &ret, nil
}

func dbEventsList(db *sql.DB, pid string, orderID int64, limit int) ([]*OrderEvent, error) {
	if orderID == 0 {
		return []*OrderEvent{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.Query(
		`SELECT id, project_id, order_id, actor, action, details, created_at
		   FROM order_events WHERE order_id = ? AND project_id = ?
		  ORDER BY created_at DESC, id DESC LIMIT ?`, orderID, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*OrderEvent
	for rows.Next() {
		var e OrderEvent
		var details string
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.OrderID, &e.Actor, &e.Action, &details, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Details = json.RawMessage(details)
		out = append(out, &e)
	}
	return out, rows.Err()
}

func writeEventTx(tx *sql.Tx, pid string, orderID int64, actor, action string, details map[string]any) error {
	_, err := tx.Exec(
		`INSERT INTO order_events (project_id, order_id, actor, action, details)
		 VALUES (?, ?, ?, ?, ?)`, pid, orderID, actorOrSystem(actor), action, jsonOrEmpty(details, "{}"))
	return err
}

// Fulfillment provider normalization.

func submitFulfillment(ctx *sdk.AppCtx, provider string, payload any) ([]byte, string, error) {
	bound := ctx.IntegrationFor("fulfillment_provider")
	if bound == nil {
		return []byte(`{}`), "", errors.New("no fulfillment_provider bound")
	}
	if bound.AppSlug != "" && bound.AppSlug != provider {
		return []byte(`{}`), "", fmt.Errorf("bound fulfillment_provider is %s, not %s", bound.AppSlug, provider)
	}
	tool := "orders_create"
	if provider == "byrd" {
		tool = "shipments_create"
	}
	input, _ := payload.(map[string]any)
	if input == nil {
		input = map[string]any{"payload": payload}
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, input)
	if err != nil {
		return []byte(`{}`), "", err
	}
	if res == nil || !res.Success {
		status := 0
		data := []byte(`{}`)
		if res != nil {
			status = res.Status
			data = res.Data
		}
		return data, "", fmt.Errorf("%s %s failed (HTTP %d)", provider, tool, status)
	}
	extID := extractExternalID(res.Data)
	return res.Data, extID, nil
}

func fulfillmentPayload(order *Order, args map[string]any) map[string]any {
	lines := make([]map[string]any, 0, len(order.Items))
	for _, it := range order.Items {
		lines = append(lines, map[string]any{
			"sku":             firstNonEmpty(it.FulfillmentSKU, it.SKU),
			"title":           it.Title,
			"quantity":        it.Quantity,
			"unit_amount":     it.UnitAmountCents,
			"currency":        it.Currency,
			"catalog_product": it.CatalogProductID,
			"catalog_price":   it.CatalogPriceID,
		})
	}
	return map[string]any{
		"client_order_id":  order.OrderNumber,
		"order_id":         order.OrderNumber,
		"recipient":        map[string]any{"name": order.CustomerName, "email": order.CustomerEmail},
		"shipping_address": jsonRawToAny(order.ShippingAddress),
		"items":            lines,
		"warehouse_id":     strArg(args, "warehouse_id"),
		"service":          strArg(args, "service"),
		"metadata":         map[string]any{"apteva_order_id": order.ID},
	}
}

func extractExternalID(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	var walk func(any) string
	walk = func(x any) string {
		switch t := x.(type) {
		case map[string]any:
			for _, key := range []string{"id", "order_id", "orderId", "provider_order_id", "shipment_id", "shipmentId"} {
				if s, ok := t[key].(string); ok && s != "" {
					return s
				}
			}
			for _, v := range t {
				if s := walk(v); s != "" {
					return s
				}
			}
		case []any:
			for _, v := range t {
				if s := walk(v); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(v)
}

// Order numbering.

func nextOrderNumberTx(tx *sql.Tx, pid, format string, seqStart int64) (string, error) {
	now := time.Now().UTC()
	year := now.Format("2006")
	var count int64
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM orders WHERE project_id = ? AND order_number LIKE ?`,
		pid, "%"+year+"%").Scan(&count); err != nil {
		return "", err
	}
	seq := count + seqStart
	out := strings.TrimSpace(format)
	if out == "" {
		out = "ORD-{yyyy}-{seq:04}"
	}
	out = strings.ReplaceAll(out, "{yyyy}", now.Format("2006"))
	out = strings.ReplaceAll(out, "{yy}", now.Format("06"))
	out = strings.ReplaceAll(out, "{mm}", now.Format("01"))
	out = strings.ReplaceAll(out, "{dd}", now.Format("02"))
	for strings.Contains(out, "{seq:") {
		start := strings.Index(out, "{seq:")
		end := strings.Index(out[start:], "}")
		if end < 0 {
			break
		}
		token := out[start : start+end+1]
		widthStr := strings.TrimSuffix(strings.TrimPrefix(token, "{seq:"), "}")
		width, _ := strconv.Atoi(widthStr)
		out = strings.ReplaceAll(out, token, fmt.Sprintf("%0*d", width, seq))
	}
	out = strings.ReplaceAll(out, "{seq}", fmt.Sprintf("%d", seq))
	return out, nil
}

// Helpers.

func resolveProjectFromArgs(args map[string]any) (string, error) {
	pid := strings.TrimSpace(strArg(args, "_project_id"))
	if pid == "" {
		pid = os.Getenv("APTEVA_PROJECT_ID")
	}
	if pid == "" {
		return "", errors.New("project_id required (pass _project_id in args or set APTEVA_PROJECT_ID)")
	}
	return pid, nil
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	pid := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if pid == "" {
		pid = os.Getenv("APTEVA_PROJECT_ID")
	}
	if pid == "" {
		return "", errors.New("project_id query parameter required")
	}
	return pid, nil
}

func resolveOrder(db *sql.DB, pid string, args map[string]any) (*Order, error) {
	if id := int64Arg(args, "id"); id != 0 {
		return dbOrderGet(db, pid, id, true)
	}
	if num := strArg(args, "order_number"); num != "" {
		return dbOrderGetByNumber(db, pid, num, true)
	}
	return nil, errors.New("id or order_number required")
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func normalizeItems(raw []any, defaultCurrency string) []*OrderItem {
	out := make([]*OrderItem, 0, len(raw))
	for i, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		title := firstNonEmpty(strArg(m, "title"), strArg(m, "description"), strArg(m, "name"))
		it := &OrderItem{
			Position:        i,
			SKU:             firstNonEmpty(strArg(m, "sku"), strArg(m, "SKU")),
			Title:           title,
			Quantity:        float64Arg(m, "quantity", 1),
			UnitAmountCents: firstNonZero(int64Arg(m, "unit_amount_cents"), int64Arg(m, "unit_price_cents"), int64Arg(m, "price_cents")),
			Currency:        strings.ToUpper(firstNonEmpty(strArg(m, "currency"), defaultCurrency)),
			SourceItemRef:   strArg(m, "source_item_ref"),
			FulfillmentSKU:  firstNonEmpty(strArg(m, "fulfillment_sku"), strArg(m, "fulfillmentSKU")),
			Metadata:        json.RawMessage(jsonOrEmpty(m["metadata"], "{}")),
		}
		if id := int64Arg(m, "catalog_product_id"); id != 0 {
			it.CatalogProductID = &id
		} else if id := int64Arg(m, "product_id"); id != 0 {
			it.CatalogProductID = &id
		}
		if id := int64Arg(m, "catalog_price_id"); id != 0 {
			it.CatalogPriceID = &id
		} else if id := int64Arg(m, "price_id"); id != 0 {
			it.CatalogPriceID = &id
		}
		out = append(out, it)
	}
	return out
}

func validateStatuses(orderStatus, paymentStatus, fulfillmentStatus string) error {
	if !validOrderStatuses[orderStatus] {
		return fmt.Errorf("invalid order_status %q", orderStatus)
	}
	if !validPaymentStatuses[paymentStatus] {
		return fmt.Errorf("invalid payment_status %q", paymentStatus)
	}
	if !validFulfillmentStatuses[fulfillmentStatus] {
		return fmt.Errorf("invalid fulfillment_status %q", fulfillmentStatus)
	}
	return nil
}

var validOrderStatuses = map[string]bool{
	"draft": true, "pending_payment": true, "paid": true, "ready_to_fulfill": true,
	"fulfilling": true, "partially_fulfilled": true, "fulfilled": true, "delivered": true,
	"cancelled": true, "returned": true, "error": true,
}

var validPaymentStatuses = map[string]bool{
	"unpaid": true, "authorized": true, "paid": true, "partially_refunded": true,
	"refunded": true, "failed": true,
}

var validFulfillmentStatuses = map[string]bool{
	"unsubmitted": true, "queued": true, "submitted": true, "accepted": true,
	"picking": true, "packed": true, "shipped": true, "delivered": true,
	"cancelled": true, "failed": true, "returned": true,
}

func defaultOrderStatus(payment string) string {
	if payment == "paid" {
		return "paid"
	}
	return "draft"
}

func emitOrder(ctx *sdk.AppCtx, topic string, o *Order) {
	if ctx == nil || o == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"order_id":           o.ID,
		"order_number":       o.OrderNumber,
		"source":             o.Source,
		"payment_status":     o.PaymentStatus,
		"order_status":       o.OrderStatus,
		"fulfillment_status": o.FulfillmentStatus,
		"total_cents":        o.TotalCents,
		"currency":           o.Currency,
	})
}

func strArg(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func int64Arg(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

func arrayArg(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	switch v := m[key].(type) {
	case []any:
		return v
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil
		}
		var out []any
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		if err := dec.Decode(&out); err == nil {
			return out
		}
	}
	return nil
}

func int64FromMap(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		if n := int64Arg(m, k); n != 0 {
			return n
		}
	}
	return 0
}

func float64Arg(m map[string]any, key string, def float64) float64 {
	if m == nil {
		return def
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, err := v.Float64()
		if err == nil {
			return n
		}
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil {
			return n
		}
	}
	return def
}

func boolArg(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	}
	return false
}

func stringFromMap(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := strArg(m, k); s != "" {
			return s
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstAny(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func firstNonZero(vals ...int64) int64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

func nullableInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullablePtr(p *int64) any {
	if p == nil || *p == 0 {
		return nil
	}
	return *p
}

func nullableTime(ok bool, value string) any {
	if !ok {
		return nil
	}
	return value
}

func ptrIfValid(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func jsonOrEmpty(v any, sentinel string) string {
	if v == nil {
		return sentinel
	}
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return sentinel
		}
		return s
	case json.RawMessage:
		if len(t) == 0 {
			return sentinel
		}
		return string(t)
	case []byte:
		if len(t) == 0 {
			return sentinel
		}
		return string(t)
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return sentinel
	}
	return string(raw)
}

func jsonRawToAny(raw json.RawMessage) any {
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func actorOrSystem(actor string) string {
	if strings.TrimSpace(actor) == "" {
		return "system"
	}
	return actor
}

func clampLimit(n, max int) int {
	if n <= 0 {
		return 50
	}
	if n > max {
		return max
	}
	return n
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func pathInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" || rest == path {
		return 0
	}
	rest = strings.SplitN(rest, "/", 2)[0]
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}

func looksLikeISO4217(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil || ctx.Config() == nil {
		return def
	}
	if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
		return v
	}
	return def
}

func configInt64(ctx *sdk.AppCtx, key string, def int64) int64 {
	if ctx == nil || ctx.Config() == nil {
		return def
	}
	v := strings.TrimSpace(ctx.Config().Get(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }
