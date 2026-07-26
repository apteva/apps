// Checkout v0.1.0 — cart + buy flow.
//
// Three tables (carts, cart_items, checkout_sessions) and a state
// machine that takes "customer adds things to a basket" through to
// "customer has a real billing invoice."
//
// v0.1.0 ships the manual-payment path: on pay, checkout creates a
// finalized invoice in billing and returns it; the user/agent records
// the payment in billing separately when the wire arrives. Stripe
// Checkout Session and the webhook flow land in v0.2.0 (paired with
// billing v0.8.0's Stripe integration). The schema columns
// (provider, provider_session_id, processed_event_ids) are
// forward-compat for that, populated in v0.1.0 with defaults.
//
// Cross-app calls:
//
//	catalog → catalog_prices_get  (snapshot price into cart_items)
//	billing → customers_upsert_by_email, invoices_create, invoices_finalize
//
// All cross-app calls inject _project_id so global-scope installs
// (where both apps share a multi-project SQLite) route to the
// correct project's data partition.
package main

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
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

//go:embed apteva.yaml
var manifestYAML []byte

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("checkout requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("checkout mounted",
		"version", "0.1.3",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker          { return nil }
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{{Topic: "invoice.paid", Handler: a.handleInvoicePaid}}
}

func (a *App) handleInvoicePaid(ctx *sdk.AppCtx, event sdk.Event) error {
	pid := strings.TrimSpace(event.ProjectID)
	if pid == "" {
		pid = ctx.CurrentProject()
	}
	invoiceID := int64Arg(event.Data, "id")
	if pid == "" || invoiceID == 0 || strArg(event.Data, "status") != "paid" {
		return nil
	}
	result, err := ctx.AppDB().Exec(`UPDATE checkout_sessions
		SET status='paid', completed_at=COALESCE(completed_at, CURRENT_TIMESTAMP), updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND invoice_id=? AND status='awaiting_payment'`, pid, invoiceID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return nil
	}
	session, err := dbCheckoutGetByInvoice(ctx.AppDB(), pid, invoiceID)
	if err == nil && session != nil {
		emitSession(ctx.WithProject(pid), "checkout.paid", session)
	}
	return err
}

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/carts", Handler: a.handleHTTPCartsCollection},
		{Pattern: "/carts/", Handler: a.handleHTTPCartItem},
		{Pattern: "/sessions", Handler: a.handleHTTPSessionsCollection},
		{Pattern: "/sessions/", Handler: a.handleHTTPSessionItem},
	}
}

func (a *App) handleHTTPCartsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPCartsList(w, r)
	case http.MethodPost:
		a.handleHTTPCartCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPCartItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/carts/")
	parts := strings.SplitN(rest, "/", 4)
	// /carts/by-token/<token>
	if len(parts) >= 2 && parts[0] == "by-token" && parts[1] != "" {
		if r.Method == http.MethodGet {
			a.handleHTTPCartGetByToken(w, r, parts[1])
			return
		}
	}
	// /carts/<id>/items
	if len(parts) >= 2 && parts[1] == "items" {
		if len(parts) == 2 {
			switch r.Method {
			case http.MethodPost:
				a.handleHTTPCartItemAdd(w, r)
				return
			case http.MethodDelete:
				a.handleHTTPCartClear(w, r)
				return
			}
		}
		// /carts/<id>/items/<itemId>
		if len(parts) >= 3 {
			switch r.Method {
			case http.MethodPatch:
				a.handleHTTPCartItemSetQty(w, r)
				return
			case http.MethodDelete:
				a.handleHTTPCartItemRemove(w, r)
				return
			}
		}
	}
	if r.Method == http.MethodGet {
		a.handleHTTPCartGet(w, r)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleHTTPSessionsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPSessionsList(w, r)
	case http.MethodPost:
		a.handleHTTPSessionStart(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPSessionItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/sessions/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 2 {
		switch parts[1] {
		case "pay":
			if r.Method == http.MethodPost {
				a.handleHTTPSessionPay(w, r)
				return
			}
		case "cancel":
			if r.Method == http.MethodPost {
				a.handleHTTPSessionCancel(w, r)
				return
			}
		}
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPSessionGet(w, r)
	case http.MethodPatch:
		a.handleHTTPSessionUpdate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─── MCP tools ──────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "cart_create",
			Description: "Create or fetch an open cart for a session_token or customer_id. If neither is provided, a server session_token is generated.",
			InputSchema: schemaObject(map[string]any{
				"session_token": map[string]any{"type": "string"},
				"customer_id":   map[string]any{"type": "integer"},
				"metadata":      map[string]any{"type": "object"},
			}, nil),
			Handler: a.toolCartCreate,
		},
		{
			Name:        "cart_get",
			Description: "Fetch a cart by id or session_token. Returns the cart row plus items and materialised totals.",
			InputSchema: schemaObject(map[string]any{
				"cart_id":       map[string]any{"type": "integer"},
				"session_token": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolCartGet,
		},
		{
			Name:        "cart_add_item",
			Description: "Add a catalog price to a cart. If the price is already in the cart, quantity is incremented. Snapshots description / unit_amount_cents / currency from catalog. Cart must be in 'open' status. Args: cart_id, price_id (required), quantity (default 1).",
			InputSchema: schemaObject(map[string]any{
				"cart_id":  map[string]any{"type": "integer"},
				"price_id": map[string]any{"type": "integer"},
				"quantity": map[string]any{"type": "number"},
			}, []string{"cart_id", "price_id"}),
			Handler: a.toolCartAddItem,
		},
		{
			Name:        "cart_set_quantity",
			Description: "Set an item's quantity. Setting quantity=0 removes the item. Cart must be in 'open' status. Args: cart_id, item_id, quantity.",
			InputSchema: schemaObject(map[string]any{
				"cart_id":  map[string]any{"type": "integer"},
				"item_id":  map[string]any{"type": "integer"},
				"quantity": map[string]any{"type": "number"},
			}, []string{"cart_id", "item_id", "quantity"}),
			Handler: a.toolCartSetQuantity,
		},
		{
			Name:        "cart_clear",
			Description: "Remove all items from a cart. Cart must be in 'open' status. Args: cart_id.",
			InputSchema: schemaObject(map[string]any{
				"cart_id": map[string]any{"type": "integer"},
			}, []string{"cart_id"}),
			Handler: a.toolCartClear,
		},
		{
			Name:        "checkout_start",
			Description: "Lock a cart and create a checkout_session. The cart transitions to 'checkout' (no more item changes). Args: cart_id.",
			InputSchema: schemaObject(map[string]any{
				"cart_id": map[string]any{"type": "integer"},
			}, []string{"cart_id"}),
			Handler: a.toolCheckoutStart,
		},
		{
			Name:        "checkout_update",
			Description: "Capture or update buyer info on a started session. Args: session_id, patch (any subset of email, customer_name, shipping_address, billing_address).",
			InputSchema: schemaObject(map[string]any{
				"session_id": map[string]any{"type": "integer"},
				"patch":      map[string]any{"type": "object"},
			}, []string{"session_id", "patch"}),
			Handler: a.toolCheckoutUpdate,
		},
		{
			Name:        "checkout_set_adjustments",
			Description: "Replace frozen checkout adjustments before payment. Intended for trusted commerce orchestrators. Args: session_id, shipping_cents?, discount_cents?, tax_cents?, adjustments?.",
			InputSchema: schemaObject(map[string]any{
				"session_id":     map[string]any{"type": "integer"},
				"shipping_cents": map[string]any{"type": "integer"},
				"discount_cents": map[string]any{"type": "integer"},
				"tax_cents":      map[string]any{"type": "integer"},
				"adjustments":    map[string]any{"type": "object"},
			}, []string{"session_id"}),
			Handler: a.toolCheckoutSetAdjustments,
		},
		{
			Name:        "checkout_pay",
			Description: "Freeze a session into a finalized Billing invoice and prepare its configured payment session. provider='manual' returns the invoice; provider='stripe' calls Billing and returns hosted or Elements browser configuration. Retry with the same idempotency_key reuses the invoice and Stripe session. Args: session_id, provider, presentation, idempotency_key, return_url, success_url, cancel_url, expires_at, payment_method_types.",
			InputSchema: schemaObject(map[string]any{
				"session_id":           map[string]any{"type": "integer"},
				"provider":             map[string]any{"type": "string", "enum": []string{"manual", "stripe"}},
				"presentation":         map[string]any{"type": "string", "enum": []string{"elements", "hosted"}},
				"idempotency_key":      map[string]any{"type": "string"},
				"return_url":           map[string]any{"type": "string"},
				"success_url":          map[string]any{"type": "string"},
				"cancel_url":           map[string]any{"type": "string"},
				"expires_at":           map[string]any{"type": "integer"},
				"payment_method_types": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}, []string{"session_id"}),
			Handler: a.toolCheckoutPay,
		},
		{
			Name:        "checkout_get",
			Description: "Fetch a checkout_session by id. Includes the linked invoice_id when status is paid or awaiting_payment.",
			InputSchema: schemaObject(map[string]any{
				"session_id": map[string]any{"type": "integer"},
			}, []string{"session_id"}),
			Handler: a.toolCheckoutGet,
		},
		{
			Name:        "checkout_cancel",
			Description: "Cancel a started or awaiting-payment session. Releases the cart back to 'open' so the buyer can retry. Args: session_id.",
			InputSchema: schemaObject(map[string]any{
				"session_id": map[string]any{"type": "integer"},
			}, []string{"session_id"}),
			Handler: a.toolCheckoutCancel,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution ─────────────────────────────────────────────

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

// ─── Types ──────────────────────────────────────────────────────────

type Cart struct {
	ID            int64           `json:"id"`
	ProjectID     string          `json:"project_id,omitempty"`
	SessionToken  string          `json:"session_token,omitempty"`
	CustomerID    *int64          `json:"customer_id,omitempty"`
	SubtotalCents int64           `json:"subtotal_cents"`
	Currency      string          `json:"currency"`
	ItemCount     int             `json:"item_count"`
	Status        string          `json:"status"`
	InvoiceID     *int64          `json:"invoice_id,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     string          `json:"created_at,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
	ExpiresAt     string          `json:"expires_at,omitempty"`
	Items         []*CartItem     `json:"items,omitempty"`
}

type CartItem struct {
	ID              int64           `json:"id"`
	CartID          int64           `json:"cart_id"`
	PriceID         int64           `json:"price_id"`
	ProductID       int64           `json:"product_id"`
	Description     string          `json:"description"`
	UnitAmountCents int64           `json:"unit_amount_cents"`
	Currency        string          `json:"currency"`
	Quantity        float64         `json:"quantity"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	CreatedAt       string          `json:"created_at,omitempty"`
	UpdatedAt       string          `json:"updated_at,omitempty"`
}

type CheckoutSession struct {
	ID                int64           `json:"id"`
	ProjectID         string          `json:"project_id,omitempty"`
	CartID            int64           `json:"cart_id"`
	Provider          string          `json:"provider"`
	ProviderSessionID string          `json:"provider_session_id,omitempty"`
	Presentation      string          `json:"presentation,omitempty"`
	Email             string          `json:"email,omitempty"`
	CustomerName      string          `json:"customer_name,omitempty"`
	ShippingAddress   json.RawMessage `json:"shipping_address,omitempty"`
	BillingAddress    json.RawMessage `json:"billing_address,omitempty"`
	Status            string          `json:"status"`
	InvoiceID         *int64          `json:"invoice_id,omitempty"`
	SubtotalCents     int64           `json:"subtotal_cents"`
	ShippingCents     int64           `json:"shipping_cents"`
	DiscountCents     int64           `json:"discount_cents"`
	TaxCents          int64           `json:"tax_cents"`
	TotalCents        int64           `json:"total_cents"`
	Currency          string          `json:"currency"`
	Adjustments       json.RawMessage `json:"adjustments,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         string          `json:"created_at,omitempty"`
	UpdatedAt         string          `json:"updated_at,omitempty"`
	CompletedAt       string          `json:"completed_at,omitempty"`
	ExpiresAt         string          `json:"expires_at,omitempty"`
}

// ─── MCP tool handlers ──────────────────────────────────────────────

func (a *App) toolCartCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cart, err := dbCartCreate(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	emitCart(ctx, "cart.created", cart)
	return map[string]any{"cart": cart}, nil
}

func (a *App) toolCartGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cart, err := resolveCart(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"cart": cart}, nil
}

func (a *App) toolCartAddItem(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cartID := int64Arg(args, "cart_id")
	priceID := int64Arg(args, "price_id")
	qty := float64Arg(args, "quantity", 1)
	if cartID == 0 || priceID == 0 {
		return nil, errors.New("cart_id and price_id required")
	}
	if qty <= 0 {
		return nil, errors.New("quantity must be > 0")
	}
	cart, err := dbCartAddItem(ctx, pid, cartID, priceID, qty)
	if err != nil {
		return nil, err
	}
	emitCart(ctx, "cart.item_added", cart)
	return map[string]any{"cart": cart}, nil
}

func (a *App) toolCartSetQuantity(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cartID := int64Arg(args, "cart_id")
	itemID := int64Arg(args, "item_id")
	if cartID == 0 || itemID == 0 {
		return nil, errors.New("cart_id and item_id required")
	}
	qtyAny, hasQty := args["quantity"]
	if !hasQty {
		return nil, errors.New("quantity required")
	}
	qty := float64Arg(map[string]any{"q": qtyAny}, "q", -1)
	if qty < 0 {
		return nil, errors.New("quantity must be >= 0")
	}
	cart, err := dbCartSetQuantity(ctx.AppDB(), pid, cartID, itemID, qty)
	if err != nil {
		return nil, err
	}
	if qty == 0 {
		emitCart(ctx, "cart.item_removed", cart)
	}
	return map[string]any{"cart": cart}, nil
}

func (a *App) toolCartClear(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cartID := int64Arg(args, "cart_id")
	if cartID == 0 {
		return nil, errors.New("cart_id required")
	}
	cart, err := dbCartClear(ctx.AppDB(), pid, cartID)
	if err != nil {
		return nil, err
	}
	emitCart(ctx, "cart.cleared", cart)
	return map[string]any{"cart": cart}, nil
}

func (a *App) toolCheckoutStart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cartID := int64Arg(args, "cart_id")
	if cartID == 0 {
		return nil, errors.New("cart_id required")
	}
	session, err := dbCheckoutStart(ctx, pid, cartID)
	if err != nil {
		return nil, err
	}
	emitSession(ctx, "checkout.started", session)
	return map[string]any{"session": session}, nil
}

func (a *App) toolCheckoutUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sessionID := int64Arg(args, "session_id")
	if sessionID == 0 {
		return nil, errors.New("session_id required")
	}
	patch, _ := args["patch"].(map[string]any)
	if patch == nil {
		return nil, errors.New("patch required (object)")
	}
	session, err := dbCheckoutUpdate(ctx.AppDB(), pid, sessionID, patch)
	if err != nil {
		return nil, err
	}
	emitSession(ctx, "checkout.contact_captured", session)
	return map[string]any{"session": session}, nil
}

func (a *App) toolCheckoutSetAdjustments(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sessionID := int64Arg(args, "session_id")
	if sessionID == 0 {
		return nil, errors.New("session_id required")
	}
	session, err := dbCheckoutSetAdjustments(ctx.AppDB(), pid, sessionID, args)
	if err != nil {
		return nil, err
	}
	emitSession(ctx, "checkout.adjustments_updated", session)
	return map[string]any{"session": session}, nil
}

func (a *App) toolCheckoutPay(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sessionID := int64Arg(args, "session_id")
	if sessionID == 0 {
		return nil, errors.New("session_id required")
	}
	session, invoiceID, invoiceNumber, payment, err := dbCheckoutPay(ctx, pid, sessionID, args)
	if err != nil {
		return nil, err
	}
	emitSession(ctx, "checkout.payment_started", session)
	return map[string]any{
		"session":         session,
		"invoice_id":      invoiceID,
		"invoice_number":  invoiceNumber,
		"payment":         payment,
		"redirect_url":    strArg(payment, "url"),
		"client_secret":   strArg(payment, "client_secret"),
		"publishable_key": strArg(payment, "publishable_key"),
	}, nil
}

func (a *App) toolCheckoutGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sessionID := int64Arg(args, "session_id")
	if sessionID == 0 {
		return nil, errors.New("session_id required")
	}
	session, err := dbCheckoutGet(ctx.AppDB(), pid, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("session not found")
	}
	return map[string]any{"session": session}, nil
}

func (a *App) toolCheckoutCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sessionID := int64Arg(args, "session_id")
	if sessionID == 0 {
		return nil, errors.New("session_id required")
	}
	session, err := dbCheckoutCancel(ctx.AppDB(), pid, sessionID)
	if err != nil {
		return nil, err
	}
	emitSession(ctx, "checkout.cancelled", session)
	return map[string]any{"session": session}, nil
}

// ─── HTTP handlers ──────────────────────────────────────────────────

func (a *App) handleHTTPCartsList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	carts, err := dbCartsList(ctx.AppDB(), pid, cartFilters{
		status: r.URL.Query().Get("status"),
		limit:  clampLimit(atoiOr(r.URL.Query().Get("limit"), 50), 200),
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"carts": carts, "count": len(carts)})
}

func (a *App) handleHTTPCartCreate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body) // body is optional; nil → guest cart with fresh token
	cart, err := dbCartCreate(ctx, pid, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"cart": cart})
}

func (a *App) handleHTTPCartGet(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/carts/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	cart, err := dbCartGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cart == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"cart": cart})
}

func (a *App) handleHTTPCartGetByToken(w http.ResponseWriter, r *http.Request, token string) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cart, err := dbCartGetByToken(ctx.AppDB(), pid, token)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cart == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"cart": cart})
}

func (a *App) handleHTTPCartItemAdd(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cartID := pathIntSegment(r.URL.Path, "/carts/", 0)
	if cartID == 0 {
		httpErr(w, http.StatusBadRequest, "cart id required")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	priceID := int64Arg(body, "price_id")
	qty := float64Arg(body, "quantity", 1)
	if priceID == 0 || qty <= 0 {
		httpErr(w, http.StatusBadRequest, "price_id and quantity>0 required")
		return
	}
	cart, err := dbCartAddItem(ctx, pid, cartID, priceID, qty)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitCart(ctx, "cart.item_added", cart)
	httpJSON(w, map[string]any{"cart": cart})
}

func (a *App) handleHTTPCartItemSetQty(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cartID := pathIntSegment(r.URL.Path, "/carts/", 0)
	itemID := pathIntSegment(r.URL.Path, "/carts/", 2)
	if cartID == 0 || itemID == 0 {
		httpErr(w, http.StatusBadRequest, "cart id and item id required")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	qty := float64Arg(body, "quantity", -1)
	if qty < 0 {
		httpErr(w, http.StatusBadRequest, "quantity required (>=0)")
		return
	}
	cart, err := dbCartSetQuantity(ctx.AppDB(), pid, cartID, itemID, qty)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"cart": cart})
}

func (a *App) handleHTTPCartItemRemove(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cartID := pathIntSegment(r.URL.Path, "/carts/", 0)
	itemID := pathIntSegment(r.URL.Path, "/carts/", 2)
	if cartID == 0 || itemID == 0 {
		httpErr(w, http.StatusBadRequest, "cart id and item id required")
		return
	}
	cart, err := dbCartSetQuantity(ctx.AppDB(), pid, cartID, itemID, 0)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitCart(ctx, "cart.item_removed", cart)
	httpJSON(w, map[string]any{"cart": cart})
}

func (a *App) handleHTTPCartClear(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cartID := pathIntSegment(r.URL.Path, "/carts/", 0)
	if cartID == 0 {
		httpErr(w, http.StatusBadRequest, "cart id required")
		return
	}
	cart, err := dbCartClear(ctx.AppDB(), pid, cartID)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitCart(ctx, "cart.cleared", cart)
	httpJSON(w, map[string]any{"cart": cart})
}

func (a *App) handleHTTPSessionsList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sessions, err := dbSessionsList(ctx.AppDB(), pid, sessionFilters{
		status: r.URL.Query().Get("status"),
		limit:  clampLimit(atoiOr(r.URL.Query().Get("limit"), 50), 200),
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"sessions": sessions, "count": len(sessions)})
}

func (a *App) handleHTTPSessionStart(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	cartID := int64Arg(body, "cart_id")
	if cartID == 0 {
		httpErr(w, http.StatusBadRequest, "cart_id required")
		return
	}
	session, err := dbCheckoutStart(ctx, pid, cartID)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitSession(ctx, "checkout.started", session)
	httpJSON(w, map[string]any{"session": session})
}

func (a *App) handleHTTPSessionGet(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/sessions/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	session, err := dbCheckoutGet(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if session == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"session": session})
}

func (a *App) handleHTTPSessionUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/sessions/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	session, err := dbCheckoutUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitSession(ctx, "checkout.contact_captured", session)
	httpJSON(w, map[string]any{"session": session})
}

func (a *App) handleHTTPSessionPay(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/sessions/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	args := map[string]any{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&args)
	}
	session, invoiceID, invoiceNumber, payment, err := dbCheckoutPay(ctx, pid, id, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitSession(ctx, "checkout.payment_started", session)
	httpJSON(w, map[string]any{
		"session":        session,
		"invoice_id":     invoiceID,
		"invoice_number": invoiceNumber,
		"payment":        payment,
	})
}

func (a *App) handleHTTPSessionCancel(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/sessions/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	session, err := dbCheckoutCancel(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitSession(ctx, "checkout.cancelled", session)
	httpJSON(w, map[string]any{"session": session})
}

// ─── DB: carts ─────────────────────────────────────────────────────

type cartFilters struct {
	status string
	limit  int
}

func dbCartsList(db *sql.DB, pid string, f cartFilters) ([]*Cart, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.status != "" {
		where = append(where, "status = ?")
		args = append(args, f.status)
	}
	limit := f.limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, project_id, COALESCE(session_token,''), customer_id,
		        subtotal_cents, currency, item_count, status, invoice_id,
		        metadata, created_at, updated_at, expires_at
		 FROM carts
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY updated_at DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Cart
	for rows.Next() {
		c, err := scanCart(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func dbCartGetByID(db *sql.DB, pid string, id int64) (*Cart, error) {
	row := db.QueryRow(
		`SELECT id, project_id, COALESCE(session_token,''), customer_id,
		        subtotal_cents, currency, item_count, status, invoice_id,
		        metadata, created_at, updated_at, expires_at
		 FROM carts WHERE id = ? AND project_id = ?`, id, pid)
	c, err := scanCart(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := loadCartItems(db, c); err != nil {
		return nil, err
	}
	return c, nil
}

func dbCartGetByToken(db *sql.DB, pid, token string) (*Cart, error) {
	row := db.QueryRow(
		`SELECT id, project_id, COALESCE(session_token,''), customer_id,
		        subtotal_cents, currency, item_count, status, invoice_id,
		        metadata, created_at, updated_at, expires_at
		 FROM carts
		 WHERE project_id = ? AND session_token = ? AND status = 'open'`, pid, token)
	c, err := scanCart(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := loadCartItems(db, c); err != nil {
		return nil, err
	}
	return c, nil
}

// dbCartCreate creates a new open cart. body may carry session_token
// (caller-supplied) or customer_id; if neither, a fresh server-issued
// session_token is generated.
func dbCartCreate(ctx *sdk.AppCtx, pid string, body map[string]any) (*Cart, error) {
	db := ctx.AppDB()
	token := strArg(body, "session_token")
	customerID := int64Arg(body, "customer_id")
	if token == "" && customerID == 0 {
		token = newSessionToken()
	}
	ttlDays := configInt64(ctx, "cart_ttl_days", 30)
	expires := time.Now().UTC().Add(time.Duration(ttlDays) * 24 * time.Hour).Format(time.RFC3339)
	now := nowRFC3339()

	args := []any{pid, nullStr(token), nullableInt64(customerID), "USD", "open", "{}", now, now}
	q := `INSERT INTO carts (project_id, session_token, customer_id, currency, status, metadata, created_at, updated_at`
	vals := `VALUES (?, ?, ?, ?, ?, ?, ?, ?`
	if customerID == 0 {
		q += ", expires_at)"
		vals += ", ?)"
		args = append(args, expires)
	} else {
		q += ")"
		vals += ")"
	}
	res, err := db.Exec(q+" "+vals, args...)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			// Already an open cart for this token — return it.
			if token != "" {
				return dbCartGetByToken(db, pid, token)
			}
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbCartGetByID(db, pid, id)
}

func dbCartAddItem(ctx *sdk.AppCtx, pid string, cartID, priceID int64, qty float64) (*Cart, error) {
	db := ctx.AppDB()
	// Cart must be open.
	cart, err := dbCartGetByID(db, pid, cartID)
	if err != nil {
		return nil, err
	}
	if cart == nil {
		return nil, fmt.Errorf("cart %d not found", cartID)
	}
	if cart.Status != "open" {
		return nil, fmt.Errorf("cart %d is %s — only 'open' carts accept item changes", cartID, cart.Status)
	}
	// Fetch the catalog price for the snapshot.
	api := ctx.PlatformAPI()
	if api == nil {
		return nil, errors.New("platform API unavailable (catalog app must be installed)")
	}
	type catalogPrice struct {
		ID              int64  `json:"id"`
		ProductID       int64  `json:"product_id"`
		Nickname        string `json:"nickname"`
		UnitAmountCents int64  `json:"unit_amount_cents"`
		Currency        string `json:"currency"`
		Active          bool   `json:"active"`
		ArchivedAt      string `json:"archived_at"`
	}
	var priceResponse struct {
		Price catalogPrice `json:"price"`
	}
	if err := api.CallAppResult("catalog", "catalog_prices_get",
		map[string]any{"id": priceID, "_project_id": pid}, &priceResponse); err != nil {
		return nil, fmt.Errorf("catalog price %d lookup failed (is the catalog app installed?): %w", priceID, err)
	}
	price := priceResponse.Price
	if price.ArchivedAt != "" || !price.Active {
		return nil, fmt.Errorf("catalog price %d is inactive/archived", priceID)
	}
	// First-line currency wins for the cart.
	cartCurrency := cart.Currency
	if cart.ItemCount == 0 {
		cartCurrency = price.Currency
	}
	// Snapshot fields
	desc := price.Nickname
	if desc == "" {
		var productResponse struct {
			Product struct {
				Name string `json:"name"`
			} `json:"product"`
		}
		_ = api.CallAppResult("catalog", "catalog_products_get",
			map[string]any{"id": price.ProductID, "_project_id": pid}, &productResponse)
		desc = productResponse.Product.Name
		if desc == "" {
			desc = fmt.Sprintf("Product #%d", price.ProductID)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := nowRFC3339()
	// UPSERT — if (cart_id, price_id) exists, bump quantity.
	if _, err := tx.Exec(
		`INSERT INTO cart_items
		     (cart_id, price_id, product_id, description, unit_amount_cents,
		      currency, quantity, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (cart_id, price_id) DO UPDATE SET
		     quantity = quantity + excluded.quantity,
		     updated_at = excluded.updated_at`,
		cartID, priceID, price.ProductID, desc, price.UnitAmountCents,
		price.Currency, qty, now, now); err != nil {
		return nil, err
	}
	if err := recomputeCartTotalsTx(tx, cartID, cartCurrency); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbCartGetByID(db, pid, cartID)
}

func dbCartSetQuantity(db *sql.DB, pid string, cartID, itemID int64, qty float64) (*Cart, error) {
	cart, err := dbCartGetByID(db, pid, cartID)
	if err != nil {
		return nil, err
	}
	if cart == nil {
		return nil, fmt.Errorf("cart %d not found", cartID)
	}
	if cart.Status != "open" {
		return nil, fmt.Errorf("cart %d is %s — only 'open' carts accept item changes", cartID, cart.Status)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if qty == 0 {
		if _, err := tx.Exec(`DELETE FROM cart_items WHERE id = ? AND cart_id = ?`, itemID, cartID); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.Exec(
			`UPDATE cart_items SET quantity = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ? AND cart_id = ?`, qty, itemID, cartID); err != nil {
			return nil, err
		}
	}
	if err := recomputeCartTotalsTx(tx, cartID, cart.Currency); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbCartGetByID(db, pid, cartID)
}

func dbCartClear(db *sql.DB, pid string, cartID int64) (*Cart, error) {
	cart, err := dbCartGetByID(db, pid, cartID)
	if err != nil {
		return nil, err
	}
	if cart == nil {
		return nil, fmt.Errorf("cart %d not found", cartID)
	}
	if cart.Status != "open" {
		return nil, fmt.Errorf("cart %d is %s — only 'open' carts can be cleared", cartID, cart.Status)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM cart_items WHERE cart_id = ?`, cartID); err != nil {
		return nil, err
	}
	if err := recomputeCartTotalsTx(tx, cartID, cart.Currency); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbCartGetByID(db, pid, cartID)
}

func recomputeCartTotalsTx(tx *sql.Tx, cartID int64, currency string) error {
	var subtotal int64
	var itemCount int
	rows, err := tx.Query(
		`SELECT unit_amount_cents, quantity FROM cart_items WHERE cart_id = ?`, cartID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var unit int64
		var qty float64
		if err := rows.Scan(&unit, &qty); err != nil {
			return err
		}
		subtotal += roundCents(float64(unit) * qty)
		itemCount++
	}
	_, err = tx.Exec(
		`UPDATE carts SET subtotal_cents = ?, item_count = ?, currency = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, subtotal, itemCount, currency, cartID)
	return err
}

func scanCart(s rowScanner) (*Cart, error) {
	var c Cart
	var customerID sql.NullInt64
	var invoiceID sql.NullInt64
	var meta sql.NullString
	var expiresAt sql.NullString
	if err := s.Scan(
		&c.ID, &c.ProjectID, &c.SessionToken, &customerID,
		&c.SubtotalCents, &c.Currency, &c.ItemCount, &c.Status, &invoiceID,
		&meta, &c.CreatedAt, &c.UpdatedAt, &expiresAt); err != nil {
		return nil, err
	}
	if customerID.Valid {
		v := customerID.Int64
		c.CustomerID = &v
	}
	if invoiceID.Valid {
		v := invoiceID.Int64
		c.InvoiceID = &v
	}
	if meta.Valid {
		c.Metadata = json.RawMessage(meta.String)
	}
	if expiresAt.Valid {
		c.ExpiresAt = expiresAt.String
	}
	return &c, nil
}

func loadCartItems(db *sql.DB, cart *Cart) error {
	rows, err := db.Query(
		`SELECT id, cart_id, price_id, product_id, description, unit_amount_cents,
		        currency, quantity, metadata, created_at, updated_at
		 FROM cart_items WHERE cart_id = ?
		 ORDER BY id ASC`, cart.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var it CartItem
		var meta sql.NullString
		if err := rows.Scan(&it.ID, &it.CartID, &it.PriceID, &it.ProductID,
			&it.Description, &it.UnitAmountCents, &it.Currency, &it.Quantity,
			&meta, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return err
		}
		if meta.Valid {
			it.Metadata = json.RawMessage(meta.String)
		}
		cart.Items = append(cart.Items, &it)
	}
	return rows.Err()
}

func resolveCart(db *sql.DB, pid string, args map[string]any) (*Cart, error) {
	if id := int64Arg(args, "cart_id"); id != 0 {
		c, err := dbCartGetByID(db, pid, id)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, fmt.Errorf("cart %d not found", id)
		}
		return c, nil
	}
	if tok := strArg(args, "session_token"); tok != "" {
		c, err := dbCartGetByToken(db, pid, tok)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, fmt.Errorf("no open cart for session_token")
		}
		return c, nil
	}
	return nil, errors.New("cart_id or session_token required")
}

// ─── DB: sessions ──────────────────────────────────────────────────

type sessionFilters struct {
	status string
	limit  int
}

func dbSessionsList(db *sql.DB, pid string, f sessionFilters) ([]*CheckoutSession, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.status != "" {
		where = append(where, "status = ?")
		args = append(args, f.status)
	}
	limit := f.limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, project_id, cart_id, provider, COALESCE(provider_session_id,''), presentation,
		        COALESCE(email,''), COALESCE(customer_name,''),
		        shipping_address, billing_address, status, invoice_id,
		        subtotal_cents, shipping_cents, discount_cents, tax_cents, total_cents, currency,
		        adjustments_json, metadata, created_at, updated_at, completed_at, expires_at
		 FROM checkout_sessions
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY updated_at DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CheckoutSession
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func dbCheckoutGet(db *sql.DB, pid string, id int64) (*CheckoutSession, error) {
	row := db.QueryRow(
		`SELECT id, project_id, cart_id, provider, COALESCE(provider_session_id,''), presentation,
		        COALESCE(email,''), COALESCE(customer_name,''),
		        shipping_address, billing_address, status, invoice_id,
		        subtotal_cents, shipping_cents, discount_cents, tax_cents, total_cents, currency,
		        adjustments_json, metadata, created_at, updated_at, completed_at, expires_at
		 FROM checkout_sessions WHERE id = ? AND project_id = ?`, id, pid)
	s, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func dbCheckoutGetByInvoice(db *sql.DB, pid string, invoiceID int64) (*CheckoutSession, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM checkout_sessions WHERE project_id=? AND invoice_id=?`, pid, invoiceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbCheckoutGet(db, pid, id)
}

func dbCheckoutStart(ctx *sdk.AppCtx, pid string, cartID int64) (*CheckoutSession, error) {
	db := ctx.AppDB()
	cart, err := dbCartGetByID(db, pid, cartID)
	if err != nil {
		return nil, err
	}
	if cart == nil {
		return nil, fmt.Errorf("cart %d not found", cartID)
	}
	if cart.Status != "open" {
		return nil, fmt.Errorf("cart %d is %s — only 'open' carts can start checkout", cartID, cart.Status)
	}
	if cart.ItemCount == 0 {
		return nil, errors.New("cannot start checkout on an empty cart")
	}

	ttlMin := configInt64(ctx, "session_ttl_minutes", 30)
	expires := time.Now().UTC().Add(time.Duration(ttlMin) * time.Minute).Format(time.RFC3339)
	now := nowRFC3339()

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO checkout_sessions
		     (project_id, cart_id, provider, subtotal_cents, total_cents, currency,
		      status, created_at, updated_at, expires_at)
		 VALUES (?, ?, 'manual', ?, ?, ?, 'started', ?, ?, ?)`,
		pid, cartID, cart.SubtotalCents, cart.SubtotalCents, cart.Currency,
		now, now, expires)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE carts SET status = 'checkout', updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		cartID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbCheckoutGet(db, pid, id)
}

func dbCheckoutUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*CheckoutSession, error) {
	session, err := dbCheckoutGet(db, pid, id)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("session %d not found", id)
	}
	if session.Status != "started" {
		return nil, fmt.Errorf("session %d is %s — only 'started' sessions accept updates", id, session.Status)
	}
	allowed := map[string]bool{
		"email": true, "customer_name": true,
		"shipping_address": true, "billing_address": true,
		"metadata": true,
	}
	var sets []string
	var args []any
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		switch k {
		case "shipping_address", "billing_address", "metadata":
			args = append(args, jsonOrEmpty(v, "{}"))
		case "email":
			s, _ := v.(string)
			args = append(args, strings.ToLower(strings.TrimSpace(s)))
		default:
			args = append(args, v)
		}
		sets = append(sets, k+" = ?")
	}
	if len(sets) == 0 {
		return session, nil
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id, pid)
	if _, err := db.Exec(
		`UPDATE checkout_sessions SET `+strings.Join(sets, ", ")+
			` WHERE id = ? AND project_id = ?`, args...); err != nil {
		return nil, err
	}
	return dbCheckoutGet(db, pid, id)
}

func dbCheckoutSetAdjustments(db *sql.DB, pid string, id int64, args map[string]any) (*CheckoutSession, error) {
	session, err := dbCheckoutGet(db, pid, id)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("session %d not found", id)
	}
	if session.Status != "started" {
		return nil, fmt.Errorf("session %d is %s - only 'started' sessions accept adjustments", id, session.Status)
	}
	shipping := int64Arg(args, "shipping_cents")
	discount := int64Arg(args, "discount_cents")
	tax := int64Arg(args, "tax_cents")
	if shipping < 0 || discount < 0 || tax < 0 {
		return nil, errors.New("shipping_cents, discount_cents, and tax_cents must be non-negative")
	}
	total := session.SubtotalCents + shipping + tax - discount
	if total < 0 {
		return nil, errors.New("discount_cents cannot exceed subtotal plus shipping and tax")
	}
	if _, err := db.Exec(
		`UPDATE checkout_sessions
		    SET shipping_cents=?, discount_cents=?, tax_cents=?, total_cents=?,
		        adjustments_json=?, updated_at=CURRENT_TIMESTAMP
		  WHERE id=? AND project_id=?`,
		shipping, discount, tax, total, jsonOrEmpty(args["adjustments"], "{}"), id, pid); err != nil {
		return nil, err
	}
	return dbCheckoutGet(db, pid, id)
}

// dbCheckoutPay is the v0.1.0 manual-payment path: upserts the
// customer in billing, creates + finalizes an invoice, links it to
// the session, marks the cart converted. Returns the session, the
// new invoice id, and the minted invoice number.
//
// v0.2.0 will branch on provider here: 'stripe' creates a Stripe
// Checkout Session and returns a redirect URL; the actual invoice
// is created by the webhook handler on payment success.
func dbCheckoutPay(ctx *sdk.AppCtx, pid string, sessionID int64, paymentArgs map[string]any) (*CheckoutSession, int64, string, map[string]any, error) {
	db := ctx.AppDB()
	session, err := dbCheckoutGet(db, pid, sessionID)
	if err != nil {
		return nil, 0, "", nil, err
	}
	if session == nil {
		return nil, 0, "", nil, fmt.Errorf("session %d not found", sessionID)
	}
	provider, presentation, err := checkoutPaymentConfig(paymentArgs)
	if err != nil {
		return nil, 0, "", nil, err
	}
	if session.Status == "awaiting_payment" && session.InvoiceID != nil {
		if session.Provider != provider || session.Presentation != presentation {
			return nil, 0, "", nil, errors.New("payment provider and presentation cannot change after payment preparation")
		}
		return prepareCheckoutPayment(ctx, pid, session, *session.InvoiceID, "", paymentArgs)
	}
	if session.Status != "started" {
		return nil, 0, "", nil, fmt.Errorf("session %d is %s — only 'started' or 'awaiting_payment' sessions can be paid", sessionID, session.Status)
	}
	if strings.TrimSpace(session.Email) == "" {
		return nil, 0, "", nil, errors.New("session requires email before payment (call checkout_update)")
	}
	cart, err := dbCartGetByID(db, pid, session.CartID)
	if err != nil || cart == nil {
		return nil, 0, "", nil, errors.New("cart no longer exists")
	}
	if len(cart.Items) == 0 {
		return nil, 0, "", nil, errors.New("cart is empty")
	}
	api := ctx.PlatformAPI()
	if api == nil {
		return nil, 0, "", nil, errors.New("platform API unavailable (billing app must be installed)")
	}

	// 1. Upsert the billing customer by email.
	var custResp struct {
		Customer struct {
			ID int64 `json:"id"`
		} `json:"customer"`
		WasCreated bool `json:"was_created"`
	}
	defaults := map[string]any{}
	if session.CustomerName != "" {
		defaults["name"] = session.CustomerName
	}
	if len(session.BillingAddress) > 2 { // not "{}"
		var addr map[string]any
		if json.Unmarshal(session.BillingAddress, &addr) == nil {
			defaults["billing_address"] = addr
		}
	}
	if err := api.CallAppResult("billing", "customers_upsert_by_email", map[string]any{
		"email":       session.Email,
		"defaults":    defaults,
		"_project_id": pid,
	}, &custResp); err != nil {
		return nil, 0, "", nil, fmt.Errorf("billing customer upsert failed (is the billing app installed?): %w", err)
	}

	// 2. Build line items from cart snapshots.
	lineItems := make([]map[string]any, 0, len(cart.Items))
	for _, it := range cart.Items {
		lineItems = append(lineItems, map[string]any{
			"description":      it.Description,
			"quantity":         it.Quantity,
			"unit_price_cents": it.UnitAmountCents,
			"price_id":         it.PriceID,
			"product_id":       it.ProductID,
			"tax_rate_bps":     0,
		})
	}
	if session.ShippingCents > 0 {
		lineItems = append(lineItems, map[string]any{
			"description": "Shipping", "quantity": 1, "unit_price_cents": session.ShippingCents,
			"tax_rate_bps": 0, "metadata": map[string]any{"checkout_adjustment": "shipping"},
		})
	}
	if session.TaxCents > 0 {
		lineItems = append(lineItems, map[string]any{
			"description": "Tax", "quantity": 1, "unit_price_cents": session.TaxCents,
			"tax_rate_bps": 0, "metadata": map[string]any{"checkout_adjustment": "tax"},
		})
	}
	if session.DiscountCents > 0 {
		lineItems = append(lineItems, map[string]any{
			"description": "Discount", "quantity": 1, "unit_price_cents": -session.DiscountCents,
			"tax_rate_bps": 0, "metadata": map[string]any{"checkout_adjustment": "discount"},
		})
	}

	// 3. Create the invoice (draft).
	var invResp struct {
		Invoice struct {
			ID     int64  `json:"id"`
			Number string `json:"number"`
		} `json:"invoice"`
	}
	invoiceBody := map[string]any{
		"customer_id": custResp.Customer.ID,
		"currency":    cart.Currency,
		"line_items":  lineItems,
		"_project_id": pid,
	}
	if session.CustomerName != "" || len(session.ShippingAddress) > 2 {
		invMeta := map[string]any{
			"checkout_session_id": sessionID,
		}
		if len(session.ShippingAddress) > 2 {
			var ship map[string]any
			if json.Unmarshal(session.ShippingAddress, &ship) == nil {
				invMeta["shipping_address"] = ship
			}
		}
		invoiceBody["metadata"] = invMeta
	}
	if err := api.CallAppResult("billing", "invoices_create", invoiceBody, &invResp); err != nil {
		return nil, 0, "", nil, fmt.Errorf("billing invoice create failed: %w", err)
	}

	// 4. Finalize → mints invoice number, transitions to 'open'.
	var finalResp struct {
		Invoice struct {
			ID     int64  `json:"id"`
			Number string `json:"number"`
		} `json:"invoice"`
	}
	if err := api.CallAppResult("billing", "invoices_finalize", map[string]any{
		"invoice_id":  invResp.Invoice.ID,
		"_project_id": pid,
	}, &finalResp); err != nil {
		return nil, 0, "", nil, fmt.Errorf("billing invoice finalize failed: %w", err)
	}

	// 5. Update our session + cart in one tx.
	now := nowRFC3339()
	tx, err := db.Begin()
	if err != nil {
		return nil, 0, "", nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE checkout_sessions
		 SET status = 'awaiting_payment', invoice_id = ?, provider = ?, presentation = ?, updated_at = ?
		 WHERE id = ?`, invResp.Invoice.ID, provider, presentation, now, sessionID); err != nil {
		return nil, 0, "", nil, err
	}
	if _, err := tx.Exec(
		`UPDATE carts SET status = 'converted', invoice_id = ?, updated_at = ?
		 WHERE id = ?`, invResp.Invoice.ID, now, session.CartID); err != nil {
		return nil, 0, "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, "", nil, err
	}

	updated, err := dbCheckoutGet(db, pid, sessionID)
	if err != nil {
		return nil, 0, "", nil, err
	}
	return prepareCheckoutPayment(ctx, pid, updated, invResp.Invoice.ID, finalResp.Invoice.Number, paymentArgs)
}

func checkoutPaymentConfig(args map[string]any) (string, string, error) {
	provider := strings.ToLower(strings.TrimSpace(strArg(args, "provider")))
	if provider == "" {
		provider = "manual"
	}
	if provider != "manual" && provider != "stripe" {
		return "", "", errors.New("provider must be 'manual' or 'stripe'")
	}
	presentation := strings.ToLower(strings.TrimSpace(strArg(args, "presentation")))
	if provider == "manual" {
		presentation = "manual"
	} else if presentation == "" {
		presentation = "elements"
	}
	if provider == "stripe" && presentation != "elements" && presentation != "hosted" {
		return "", "", errors.New("Stripe presentation must be 'elements' or 'hosted'")
	}
	return provider, presentation, nil
}

func prepareCheckoutPayment(ctx *sdk.AppCtx, pid string, session *CheckoutSession, invoiceID int64, invoiceNumber string, args map[string]any) (*CheckoutSession, int64, string, map[string]any, error) {
	if session.Provider != "stripe" {
		return session, invoiceID, invoiceNumber, map[string]any{
			"provider": "manual", "presentation": "manual",
		}, nil
	}
	idempotencyKey := strings.TrimSpace(strArg(args, "idempotency_key"))
	if idempotencyKey == "" {
		idempotencyKey = fmt.Sprintf("checkout-%s-%d", pid, session.ID)
	}
	billingArgs := map[string]any{
		"_project_id":     pid,
		"invoice_id":      invoiceID,
		"presentation":    session.Presentation,
		"idempotency_key": idempotencyKey,
	}
	for _, name := range []string{"return_url", "success_url", "cancel_url", "expires_at", "payment_method_types", "save_payment_method", "set_default_payment_method"} {
		if value, ok := args[name]; ok {
			billingArgs[name] = value
		}
	}
	var payment map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_create_payment_session", billingArgs, &payment); err != nil {
		return nil, 0, "", nil, fmt.Errorf("billing payment session failed: %w", err)
	}
	providerSessionID := strings.TrimSpace(strArg(payment, "stripe_session_id"))
	if providerSessionID == "" {
		return nil, 0, "", nil, errors.New("Billing returned no Stripe session id")
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE checkout_sessions
		 SET provider_session_id=?, updated_at=CURRENT_TIMESTAMP
		 WHERE project_id=? AND id=?`,
		providerSessionID, pid, session.ID,
	); err != nil {
		return nil, 0, "", nil, err
	}
	updated, err := dbCheckoutGet(ctx.AppDB(), pid, session.ID)
	if err != nil {
		return nil, 0, "", nil, err
	}
	return updated, invoiceID, invoiceNumber, payment, nil
}

func dbCheckoutCancel(db *sql.DB, pid string, id int64) (*CheckoutSession, error) {
	session, err := dbCheckoutGet(db, pid, id)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("session %d not found", id)
	}
	if session.Status == "paid" || session.Status == "cancelled" || session.Status == "expired" {
		return nil, fmt.Errorf("session %d already %s", id, session.Status)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE checkout_sessions SET status = 'cancelled', updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, id); err != nil {
		return nil, err
	}
	// Release the cart back to open IF the cart's current status is 'checkout'.
	// Don't touch carts that already converted (shouldn't happen given the
	// 'paid' guard above, but defensive).
	if _, err := tx.Exec(
		`UPDATE carts SET status = 'open', updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = 'checkout'`, session.CartID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbCheckoutGet(db, pid, id)
}

func scanSession(s rowScanner) (*CheckoutSession, error) {
	var sess CheckoutSession
	var shipping, billing, adjustments, meta sql.NullString
	var invoiceID sql.NullInt64
	var completedAt, expiresAt sql.NullString
	if err := s.Scan(
		&sess.ID, &sess.ProjectID, &sess.CartID, &sess.Provider, &sess.ProviderSessionID,
		&sess.Presentation,
		&sess.Email, &sess.CustomerName,
		&shipping, &billing, &sess.Status, &invoiceID,
		&sess.SubtotalCents, &sess.ShippingCents, &sess.DiscountCents, &sess.TaxCents, &sess.TotalCents, &sess.Currency,
		&adjustments, &meta, &sess.CreatedAt, &sess.UpdatedAt, &completedAt, &expiresAt); err != nil {
		return nil, err
	}
	if shipping.Valid {
		sess.ShippingAddress = json.RawMessage(shipping.String)
	}
	if billing.Valid {
		sess.BillingAddress = json.RawMessage(billing.String)
	}
	if meta.Valid {
		sess.Metadata = json.RawMessage(meta.String)
	}
	if adjustments.Valid {
		sess.Adjustments = json.RawMessage(adjustments.String)
	}
	if invoiceID.Valid {
		v := invoiceID.Int64
		sess.InvoiceID = &v
	}
	if completedAt.Valid {
		sess.CompletedAt = completedAt.String
	}
	if expiresAt.Valid {
		sess.ExpiresAt = expiresAt.String
	}
	return &sess, nil
}

// ─── Event emission ─────────────────────────────────────────────────

func emitCart(ctx *sdk.AppCtx, topic string, c *Cart) {
	if ctx == nil || c == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"cart_id":       c.ID,
		"session_token": c.SessionToken,
		"item_count":    c.ItemCount,
		"subtotal":      c.SubtotalCents,
		"currency":      c.Currency,
	})
}

func emitSession(ctx *sdk.AppCtx, topic string, s *CheckoutSession) {
	if ctx == nil || s == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"session_id": s.ID,
		"cart_id":    s.CartID,
		"status":     s.Status,
		"email":      s.Email,
		"total":      s.TotalCents,
		"currency":   s.Currency,
		"invoice_id": s.InvoiceID,
	})
}

// ─── Tiny utils ─────────────────────────────────────────────────────

type rowScanner interface {
	Scan(dest ...any) error
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func strArg(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
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
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
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
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return def
		}
		return n
	}
	return def
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

func pathIntSegment(path, prefix string, idx int) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" || rest == path {
		return 0
	}
	parts := strings.Split(rest, "/")
	if idx >= len(parts) {
		return 0
	}
	n, _ := strconv.ParseInt(parts[idx], 10, 64)
	return n
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func jsonOrEmpty(v any, sentinel string) string {
	if v == nil {
		return sentinel
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return sentinel
	}
	return string(raw)
}

func roundCents(f float64) int64 {
	if f >= 0 {
		return int64(f + 0.5)
	}
	return -int64(-f + 0.5)
}

// newSessionToken returns 16 random bytes hex-encoded. Sufficient
// entropy that a guess is computationally infeasible.
func newSessionToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// Catastrophic — but rand.Read on Linux/Mac doesn't fail under
		// normal conditions; if it does, fall back to a time-based token
		// so the app doesn't panic. Caller can retry.
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	// no-store: dynamic per-request reads. Prevents the panel from
	// showing stale data after a mutation until a hard reload.
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

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
