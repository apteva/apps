// Catalog v0.1.0 — the source-of-truth app for "what your business sells".
//
// Two entities (Stripe-shaped):
//   - Product: the thing (SaaS plan, ecommerce SKU, consulting service).
//   - Price:   amount + currency + optional recurrence. A product has many.
//
// Key invariant: a Price's financial fields (unit_amount_cents, currency,
// interval) are immutable after create. dbPriceUpdate rejects changes to
// them — to alter pricing, create a new Price and archive the old one.
// This mirrors Stripe's behaviour and keeps historical invoice/subscription
// snapshots sound.
//
// Lower layer in the commerce stack: catalog calls no other app. Billing,
// future subscriptions, future checkout — all call here.
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
name: catalog
display_name: Catalog
version: 0.1.0
description: |
  Products and prices — source of truth for what the business sells.
  Modelled after Stripe's Product + Price split. Self-contained: calls
  no other app; downstream apps (billing, subscriptions, checkout)
  call catalog.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
provides:
  http_routes:
    - prefix: /
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/catalog
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/catalog.db
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
		return errors.New("catalog requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("catalog mounted",
		"version", "0.1.0",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP ───────────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/products", Handler: a.handleHTTPProductsCollection},
		{Pattern: "/products/", Handler: a.handleHTTPProductItem},
		{Pattern: "/prices", Handler: a.handleHTTPPricesCollection},
		{Pattern: "/prices/", Handler: a.handleHTTPPriceItem},
	}
}

func (a *App) handleHTTPProductsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPProductsList(w, r)
	case http.MethodPost:
		a.handleHTTPProductCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPProductItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/products/")
	parts := strings.SplitN(rest, "/", 2)
	// /products/by-slug/<slug>
	if len(parts) >= 1 && parts[0] == "by-slug" && r.Method == http.MethodGet {
		if len(parts) >= 2 && parts[1] != "" {
			a.handleHTTPProductGetBySlug(w, r, parts[1])
			return
		}
	}
	// /products/<id>/prices
	if len(parts) == 2 && parts[1] == "prices" {
		switch r.Method {
		case http.MethodGet:
			a.handleHTTPProductPricesList(w, r)
			return
		case http.MethodPost:
			a.handleHTTPProductPriceCreate(w, r)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPProductGet(w, r)
	case http.MethodPatch:
		a.handleHTTPProductUpdate(w, r)
	case http.MethodDelete:
		a.handleHTTPProductArchive(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPPricesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPPricesList(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPPriceItem(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPPriceGet(w, r)
	case http.MethodPatch:
		a.handleHTTPPriceUpdate(w, r)
	case http.MethodDelete:
		a.handleHTTPPriceArchive(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─── MCP tools ──────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		// ── Products ─────────────────────────────────────────────────
		{
			Name:        "catalog_products_list",
			Description: "List products. Args: type (one_time|recurring|service), archived (default false), q (search), limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"type":     map[string]any{"type": "string"},
				"archived": map[string]any{"type": "boolean"},
				"q":        map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolProductsList,
		},
		{
			Name:        "catalog_products_create",
			Description: "Create a product. Args: name (required), type (required: one_time|recurring|service), slug, description, category, color, tax_category (standard|reduced|zero|exempt), metadata.",
			InputSchema: schemaObject(map[string]any{
				"name":         map[string]any{"type": "string"},
				"type":         map[string]any{"type": "string"},
				"slug":         map[string]any{"type": "string"},
				"description":  map[string]any{"type": "string"},
				"category":     map[string]any{"type": "string"},
				"color":        map[string]any{"type": "string"},
				"tax_category": map[string]any{"type": "string"},
				"metadata":     map[string]any{"type": "object"},
			}, []string{"name", "type"}),
			Handler: a.toolProductsCreate,
		},
		{
			Name:        "catalog_products_get",
			Description: "Fetch one product by id OR slug.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"slug": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolProductsGet,
		},
		{
			Name:        "catalog_products_update",
			Description: "Partial-patch a product. All product fields are editable (financial immutability is enforced on Prices, not Products). Args: id, patch.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"patch": map[string]any{"type": "object"},
			}, []string{"id", "patch"}),
			Handler: a.toolProductsUpdate,
		},
		{
			Name:        "catalog_products_archive",
			Description: "Soft-delete a product. Existing invoice/subscription references continue resolving; the row is hidden from list/create. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolProductsArchive,
		},
		{
			Name:        "catalog_products_search",
			Description: "Free-text search across name + description + slug + category. Args: q, limit (default 20, max 100).",
			InputSchema: schemaObject(map[string]any{
				"q":     map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			}, []string{"q"}),
			Handler: a.toolProductsSearch,
		},

		// ── Prices ───────────────────────────────────────────────────
		{
			Name:        "catalog_prices_list",
			Description: "List prices. Args: product_id, active (default true for new sales contexts), archived (default false), limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"product_id": map[string]any{"type": "integer"},
				"active":     map[string]any{"type": "boolean"},
				"archived":   map[string]any{"type": "boolean"},
				"limit":      map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolPricesList,
		},
		{
			Name:        "catalog_prices_create",
			Description: "Create a price under a product. Args: product_id (required), unit_amount_cents (required), currency (required, ISO 4217 or empty for install default), nickname, interval (day|week|month|year for recurring; omit for one-time), interval_count (default 1), trial_days (default 0), tax_inclusive (default false), metadata.",
			InputSchema: schemaObject(map[string]any{
				"product_id":        map[string]any{"type": "integer"},
				"unit_amount_cents": map[string]any{"type": "integer"},
				"currency":          map[string]any{"type": "string"},
				"nickname":          map[string]any{"type": "string"},
				"interval":          map[string]any{"type": "string"},
				"interval_count":    map[string]any{"type": "integer"},
				"trial_days":        map[string]any{"type": "integer"},
				"tax_inclusive":     map[string]any{"type": "boolean"},
				"metadata":          map[string]any{"type": "object"},
			}, []string{"product_id", "unit_amount_cents"}),
			Handler: a.toolPricesCreate,
		},
		{
			Name:        "catalog_prices_get",
			Description: "Fetch one price by id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolPricesGet,
		},
		{
			Name:        "catalog_prices_update",
			Description: "Patch a price — LIMITED FIELDS ONLY: nickname, active, metadata, tax_inclusive. Amount, currency, interval, and interval_count are immutable after create; attempts to change them are rejected. To alter pricing, create a new price and archive the old one. Args: id, patch.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"patch": map[string]any{"type": "object"},
			}, []string{"id", "patch"}),
			Handler: a.toolPricesUpdate,
		},
		{
			Name:        "catalog_prices_archive",
			Description: "Soft-delete a price. Existing invoice/subscription references continue resolving; the row is hidden from new-sales contexts. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolPricesArchive,
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

type Product struct {
	ID          int64           `json:"id"`
	ProjectID   string          `json:"project_id,omitempty"`
	Name        string          `json:"name"`
	Slug        string          `json:"slug,omitempty"`
	Description string          `json:"description,omitempty"`
	Type        string          `json:"type"`
	Category    string          `json:"category,omitempty"`
	ImageFileID *int64          `json:"image_file_id,omitempty"`
	Color       string          `json:"color,omitempty"`
	TaxCategory string          `json:"tax_category,omitempty"`
	ExternalID  string          `json:"external_id,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	CreatedAt   string          `json:"created_at,omitempty"`
	UpdatedAt   string          `json:"updated_at,omitempty"`
	ArchivedAt  string          `json:"archived_at,omitempty"`
	Prices      []*Price        `json:"prices,omitempty"`
}

type Price struct {
	ID              int64           `json:"id"`
	ProductID       int64           `json:"product_id"`
	ProjectID       string          `json:"project_id,omitempty"`
	Nickname        string          `json:"nickname,omitempty"`
	UnitAmountCents int64           `json:"unit_amount_cents"`
	Currency        string          `json:"currency"`
	Interval        string          `json:"interval,omitempty"`
	IntervalCount   int             `json:"interval_count"`
	TrialDays       int             `json:"trial_days"`
	Active          bool            `json:"active"`
	TaxInclusive    bool            `json:"tax_inclusive"`
	ExternalID      string          `json:"external_id,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	CreatedAt       string          `json:"created_at,omitempty"`
	UpdatedAt       string          `json:"updated_at,omitempty"`
	ArchivedAt      string          `json:"archived_at,omitempty"`
}

var validTypes = map[string]bool{"one_time": true, "recurring": true, "service": true}
var validIntervals = map[string]bool{"day": true, "week": true, "month": true, "year": true}
var validTaxCategories = map[string]bool{"standard": true, "reduced": true, "zero": true, "exempt": true}

// ─── MCP tool handlers ──────────────────────────────────────────────

func (a *App) toolProductsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbProductsList(ctx.AppDB(), pid, productFilters{
		typeFilter:      strArg(args, "type"),
		includeArchived: boolArg(args, "archived", false),
		query:           strArg(args, "q"),
		limit:           clampLimit(intArg(args, "limit", 50), 200),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"products": out, "count": len(out)}, nil
}

func (a *App) toolProductsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := dbProductCreate(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	emitProduct(ctx, "product.added", p)
	return map[string]any{"product": p}, nil
}

func (a *App) toolProductsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	slug := strArg(args, "slug")
	if id == 0 && slug == "" {
		return nil, errors.New("id or slug required")
	}
	var p *Product
	if id != 0 {
		p, err = dbProductGetByID(ctx.AppDB(), pid, id)
	} else {
		p, err = dbProductGetBySlug(ctx.AppDB(), pid, slug)
	}
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("product not found")
	}
	if err := loadProductPrices(ctx.AppDB(), pid, p); err != nil {
		return nil, err
	}
	return map[string]any{"product": p}, nil
}

func (a *App) toolProductsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	patch, _ := args["patch"].(map[string]any)
	if patch == nil {
		return nil, errors.New("patch required (object)")
	}
	p, err := dbProductUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		return nil, err
	}
	emitProduct(ctx, "product.updated", p)
	return map[string]any{"product": p}, nil
}

func (a *App) toolProductsArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	p, err := dbProductArchive(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	emitProduct(ctx, "product.archived", p)
	return map[string]any{"product": p}, nil
}

func (a *App) toolProductsSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	q := strArg(args, "q")
	if q == "" {
		return nil, errors.New("q required")
	}
	limit := clampLimit(intArg(args, "limit", 20), 100)
	out, err := dbProductsList(ctx.AppDB(), pid, productFilters{query: q, limit: limit})
	if err != nil {
		return nil, err
	}
	return map[string]any{"products": out, "count": len(out)}, nil
}

func (a *App) toolPricesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbPricesList(ctx.AppDB(), pid, priceFilters{
		productID:       int64Arg(args, "product_id"),
		activeOnly:      boolArg(args, "active", false), // default false = include inactive
		includeArchived: boolArg(args, "archived", false),
		limit:           clampLimit(intArg(args, "limit", 50), 200),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"prices": out, "count": len(out)}, nil
}

func (a *App) toolPricesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	productID := int64Arg(args, "product_id")
	if productID == 0 {
		return nil, errors.New("product_id required")
	}
	p, err := dbPriceCreate(ctx.AppDB(), pid, productID, args)
	if err != nil {
		return nil, err
	}
	emitPrice(ctx, "price.added", p)
	return map[string]any{"price": p}, nil
}

func (a *App) toolPricesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	p, err := dbPriceGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("price not found")
	}
	return map[string]any{"price": p}, nil
}

func (a *App) toolPricesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	patch, _ := args["patch"].(map[string]any)
	if patch == nil {
		return nil, errors.New("patch required (object)")
	}
	p, err := dbPriceUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		return nil, err
	}
	emitPrice(ctx, "price.updated", p)
	return map[string]any{"price": p}, nil
}

func (a *App) toolPricesArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	p, err := dbPriceArchive(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	emitPrice(ctx, "price.archived", p)
	return map[string]any{"price": p}, nil
}

// ─── HTTP handlers ──────────────────────────────────────────────────

func (a *App) handleHTTPProductsList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := dbProductsList(ctx.AppDB(), pid, productFilters{
		typeFilter:      r.URL.Query().Get("type"),
		includeArchived: r.URL.Query().Get("archived") == "true",
		query:           r.URL.Query().Get("q"),
		limit:           clampLimit(atoiOr(r.URL.Query().Get("limit"), 50), 200),
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"products": out, "count": len(out)})
}

func (a *App) handleHTTPProductCreate(w http.ResponseWriter, r *http.Request) {
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
	p, err := dbProductCreate(ctx.AppDB(), pid, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitProduct(ctx, "product.added", p)
	httpJSON(w, map[string]any{"product": p})
}

func (a *App) handleHTTPProductGet(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/products/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	p, err := dbProductGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := loadProductPrices(ctx.AppDB(), pid, p); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"product": p})
}

func (a *App) handleHTTPProductGetBySlug(w http.ResponseWriter, r *http.Request, slug string) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := dbProductGetBySlug(ctx.AppDB(), pid, slug)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := loadProductPrices(ctx.AppDB(), pid, p); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"product": p})
}

func (a *App) handleHTTPProductUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/products/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	p, err := dbProductUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitProduct(ctx, "product.updated", p)
	httpJSON(w, map[string]any{"product": p})
}

func (a *App) handleHTTPProductArchive(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/products/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	p, err := dbProductArchive(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitProduct(ctx, "product.archived", p)
	httpJSON(w, map[string]any{"product": p})
}

func (a *App) handleHTTPProductPricesList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/products/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "product id required")
		return
	}
	out, err := dbPricesList(ctx.AppDB(), pid, priceFilters{
		productID:       id,
		includeArchived: r.URL.Query().Get("archived") == "true",
		limit:           clampLimit(atoiOr(r.URL.Query().Get("limit"), 50), 200),
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"prices": out, "count": len(out)})
}

func (a *App) handleHTTPProductPriceCreate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/products/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "product id required")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	p, err := dbPriceCreate(ctx.AppDB(), pid, id, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitPrice(ctx, "price.added", p)
	httpJSON(w, map[string]any{"price": p})
}

func (a *App) handleHTTPPricesList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := dbPricesList(ctx.AppDB(), pid, priceFilters{
		productID:       int64(atoiOr(r.URL.Query().Get("product_id"), 0)),
		activeOnly:      r.URL.Query().Get("active") == "true",
		includeArchived: r.URL.Query().Get("archived") == "true",
		limit:           clampLimit(atoiOr(r.URL.Query().Get("limit"), 50), 200),
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"prices": out, "count": len(out)})
}

func (a *App) handleHTTPPriceGet(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/prices/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	p, err := dbPriceGet(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"price": p})
}

func (a *App) handleHTTPPriceUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/prices/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	p, err := dbPriceUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitPrice(ctx, "price.updated", p)
	httpJSON(w, map[string]any{"price": p})
}

func (a *App) handleHTTPPriceArchive(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/prices/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	p, err := dbPriceArchive(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitPrice(ctx, "price.archived", p)
	httpJSON(w, map[string]any{"price": p})
}

// ─── DB: products ──────────────────────────────────────────────────

type productFilters struct {
	typeFilter      string
	includeArchived bool
	query           string
	limit           int
}

func dbProductsList(db *sql.DB, pid string, f productFilters) ([]*Product, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if !f.includeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if f.typeFilter != "" {
		where = append(where, "type = ?")
		args = append(args, f.typeFilter)
	}
	if f.query != "" {
		where = append(where, "(name LIKE ? OR COALESCE(description,'') LIKE ? OR COALESCE(slug,'') LIKE ? OR COALESCE(category,'') LIKE ?)")
		pat := "%" + f.query + "%"
		args = append(args, pat, pat, pat, pat)
	}
	limit := f.limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, project_id, name, COALESCE(slug,''), COALESCE(description,''),
		        type, COALESCE(category,''), image_file_id, COALESCE(color,''),
		        COALESCE(tax_category,''), COALESCE(external_id,''), metadata,
		        created_at, updated_at, archived_at
		 FROM products
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY updated_at DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func dbProductGetByID(db *sql.DB, pid string, id int64) (*Product, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, COALESCE(slug,''), COALESCE(description,''),
		        type, COALESCE(category,''), image_file_id, COALESCE(color,''),
		        COALESCE(tax_category,''), COALESCE(external_id,''), metadata,
		        created_at, updated_at, archived_at
		 FROM products
		 WHERE id = ? AND project_id = ?`, id, pid)
	p, err := scanProduct(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func dbProductGetBySlug(db *sql.DB, pid, slug string) (*Product, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, COALESCE(slug,''), COALESCE(description,''),
		        type, COALESCE(category,''), image_file_id, COALESCE(color,''),
		        COALESCE(tax_category,''), COALESCE(external_id,''), metadata,
		        created_at, updated_at, archived_at
		 FROM products
		 WHERE slug = ? AND project_id = ? AND archived_at IS NULL`, slug, pid)
	p, err := scanProduct(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func dbProductCreate(db *sql.DB, pid string, args map[string]any) (*Product, error) {
	name := strings.TrimSpace(strArg(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	typ := strings.TrimSpace(strArg(args, "type"))
	if !validTypes[typ] {
		return nil, fmt.Errorf("type must be one of: one_time, recurring, service (got %q)", typ)
	}
	taxCat := strings.TrimSpace(strArg(args, "tax_category"))
	if taxCat != "" && !validTaxCategories[taxCat] {
		return nil, fmt.Errorf("tax_category must be one of: standard, reduced, zero, exempt (got %q)", taxCat)
	}
	slug := strings.TrimSpace(strArg(args, "slug"))
	desc := strArg(args, "description")
	cat := strArg(args, "category")
	color := strArg(args, "color")
	meta := jsonOrEmpty(args["metadata"], "{}")
	now := nowRFC3339()

	res, err := db.Exec(
		`INSERT INTO products
		     (project_id, name, slug, description, type, category, color,
		      tax_category, metadata, created_at, updated_at)
		 VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''),
		         NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`,
		pid, name, slug, desc, typ, cat, color, taxCat, meta, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("a product with slug %q already exists in this project", slug)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbProductGetByID(db, pid, id)
}

func dbProductUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Product, error) {
	if len(patch) == 0 {
		return dbProductGetByID(db, pid, id)
	}
	allowed := map[string]bool{
		"name": true, "slug": true, "description": true, "type": true,
		"category": true, "color": true, "tax_category": true,
		"image_file_id": true, "metadata": true,
	}
	var sets []string
	var args []any
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		switch k {
		case "type":
			s, _ := v.(string)
			if !validTypes[s] {
				return nil, fmt.Errorf("type must be one of: one_time, recurring, service")
			}
			args = append(args, s)
		case "tax_category":
			s, _ := v.(string)
			if s != "" && !validTaxCategories[s] {
				return nil, fmt.Errorf("tax_category must be one of: standard, reduced, zero, exempt")
			}
			if s == "" {
				args = append(args, nil)
			} else {
				args = append(args, s)
			}
		case "metadata":
			args = append(args, jsonOrEmpty(v, "{}"))
		case "slug":
			s, _ := v.(string)
			if s == "" {
				args = append(args, nil)
			} else {
				args = append(args, s)
			}
		default:
			args = append(args, v)
		}
		sets = append(sets, k+" = ?")
	}
	if len(sets) == 0 {
		return dbProductGetByID(db, pid, id)
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id, pid)
	if _, err := db.Exec(
		`UPDATE products SET `+strings.Join(sets, ", ")+
			` WHERE id = ? AND project_id = ? AND archived_at IS NULL`, args...); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errors.New("slug already in use by another product")
		}
		return nil, err
	}
	return dbProductGetByID(db, pid, id)
}

func dbProductArchive(db *sql.DB, pid string, id int64) (*Product, error) {
	if _, err := db.Exec(
		`UPDATE products SET archived_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ? AND archived_at IS NULL`, id, pid); err != nil {
		return nil, err
	}
	return dbProductGetByID(db, pid, id)
}

func scanProduct(s rowScanner) (*Product, error) {
	var p Product
	var imageFileID sql.NullInt64
	var meta sql.NullString
	var archivedAt sql.NullString
	if err := s.Scan(
		&p.ID, &p.ProjectID, &p.Name, &p.Slug, &p.Description,
		&p.Type, &p.Category, &imageFileID, &p.Color,
		&p.TaxCategory, &p.ExternalID, &meta,
		&p.CreatedAt, &p.UpdatedAt, &archivedAt); err != nil {
		return nil, err
	}
	if imageFileID.Valid {
		v := imageFileID.Int64
		p.ImageFileID = &v
	}
	if meta.Valid {
		p.Metadata = json.RawMessage(meta.String)
	}
	if archivedAt.Valid {
		p.ArchivedAt = archivedAt.String
	}
	return &p, nil
}

func loadProductPrices(db *sql.DB, pid string, p *Product) error {
	prices, err := dbPricesList(db, pid, priceFilters{productID: p.ID, limit: 200})
	if err != nil {
		return err
	}
	p.Prices = prices
	return nil
}

// ─── DB: prices ────────────────────────────────────────────────────

type priceFilters struct {
	productID       int64
	activeOnly      bool
	includeArchived bool
	limit           int
}

func dbPricesList(db *sql.DB, pid string, f priceFilters) ([]*Price, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if !f.includeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if f.productID != 0 {
		where = append(where, "product_id = ?")
		args = append(args, f.productID)
	}
	if f.activeOnly {
		where = append(where, "active = 1")
	}
	limit := f.limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, product_id, project_id, COALESCE(nickname,''), unit_amount_cents, currency,
		        COALESCE(interval,''), interval_count, trial_days, active, tax_inclusive,
		        COALESCE(external_id,''), metadata, created_at, updated_at, archived_at
		 FROM prices
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY created_at ASC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Price
	for rows.Next() {
		p, err := scanPrice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func dbPriceGet(db *sql.DB, pid string, id int64) (*Price, error) {
	row := db.QueryRow(
		`SELECT id, product_id, project_id, COALESCE(nickname,''), unit_amount_cents, currency,
		        COALESCE(interval,''), interval_count, trial_days, active, tax_inclusive,
		        COALESCE(external_id,''), metadata, created_at, updated_at, archived_at
		 FROM prices
		 WHERE id = ? AND project_id = ?`, id, pid)
	p, err := scanPrice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func dbPriceCreate(db *sql.DB, pid string, productID int64, args map[string]any) (*Price, error) {
	// Validate product exists in this project + isn't archived.
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM products WHERE id = ? AND project_id = ? AND archived_at IS NULL`,
		productID, pid).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("product %d not found or archived", productID)
	}
	amount := int64Arg(args, "unit_amount_cents")
	if amount == 0 {
		return nil, errors.New("unit_amount_cents required (non-zero)")
	}
	currency := strings.ToUpper(strings.TrimSpace(strArg(args, "currency")))
	if currency == "" {
		currency = strings.ToUpper(configString(globalCtx, "default_currency", "USD"))
	}
	if !looksLikeISO4217(currency) {
		return nil, fmt.Errorf("currency %q not a 3-letter ISO 4217 code", currency)
	}
	interval := strings.ToLower(strings.TrimSpace(strArg(args, "interval")))
	if interval != "" && !validIntervals[interval] {
		return nil, fmt.Errorf("interval must be one of: day, week, month, year (got %q)", interval)
	}
	intervalCount := intArg(args, "interval_count", 1)
	if intervalCount < 1 {
		return nil, errors.New("interval_count must be >= 1")
	}
	trialDays := intArg(args, "trial_days", 0)
	if trialDays < 0 {
		return nil, errors.New("trial_days must be >= 0")
	}
	nickname := strArg(args, "nickname")
	taxIncl := boolArg(args, "tax_inclusive", false)
	meta := jsonOrEmpty(args["metadata"], "{}")
	now := nowRFC3339()

	res, err := db.Exec(
		`INSERT INTO prices
		     (product_id, project_id, nickname, unit_amount_cents, currency,
		      interval, interval_count, trial_days, active, tax_inclusive,
		      metadata, created_at, updated_at)
		 VALUES (?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?, ?, 1, ?, ?, ?, ?)`,
		productID, pid, nickname, amount, currency, interval, intervalCount,
		trialDays, taxIncl, meta, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbPriceGet(db, pid, id)
}

func dbPriceUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Price, error) {
	if len(patch) == 0 {
		return dbPriceGet(db, pid, id)
	}
	// Reject changes to immutable financial fields. Stripe enforces
	// the same rule — to change pricing, create a new Price and
	// archive the old one. Historical invoice snapshots stay sound.
	for _, locked := range []string{"unit_amount_cents", "currency", "interval", "interval_count", "trial_days", "product_id"} {
		if _, present := patch[locked]; present {
			return nil, fmt.Errorf("%s cannot be changed after price creation; create a new price and archive this one", locked)
		}
	}
	allowed := map[string]bool{
		"nickname": true, "active": true, "metadata": true, "tax_inclusive": true,
	}
	var sets []string
	var args []any
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		switch k {
		case "metadata":
			args = append(args, jsonOrEmpty(v, "{}"))
		case "nickname":
			s, _ := v.(string)
			if s == "" {
				args = append(args, nil)
			} else {
				args = append(args, s)
			}
		case "active":
			b, _ := v.(bool)
			if b {
				args = append(args, 1)
			} else {
				args = append(args, 0)
			}
		case "tax_inclusive":
			b, _ := v.(bool)
			if b {
				args = append(args, 1)
			} else {
				args = append(args, 0)
			}
		default:
			args = append(args, v)
		}
		sets = append(sets, k+" = ?")
	}
	if len(sets) == 0 {
		return dbPriceGet(db, pid, id)
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id, pid)
	if _, err := db.Exec(
		`UPDATE prices SET `+strings.Join(sets, ", ")+
			` WHERE id = ? AND project_id = ? AND archived_at IS NULL`, args...); err != nil {
		return nil, err
	}
	return dbPriceGet(db, pid, id)
}

func dbPriceArchive(db *sql.DB, pid string, id int64) (*Price, error) {
	if _, err := db.Exec(
		`UPDATE prices SET archived_at = CURRENT_TIMESTAMP, active = 0, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ? AND archived_at IS NULL`, id, pid); err != nil {
		return nil, err
	}
	return dbPriceGet(db, pid, id)
}

func scanPrice(s rowScanner) (*Price, error) {
	var p Price
	var meta sql.NullString
	var archivedAt sql.NullString
	var active, taxIncl int
	if err := s.Scan(
		&p.ID, &p.ProductID, &p.ProjectID, &p.Nickname, &p.UnitAmountCents,
		&p.Currency, &p.Interval, &p.IntervalCount, &p.TrialDays,
		&active, &taxIncl, &p.ExternalID, &meta,
		&p.CreatedAt, &p.UpdatedAt, &archivedAt); err != nil {
		return nil, err
	}
	p.Active = active != 0
	p.TaxInclusive = taxIncl != 0
	if meta.Valid {
		p.Metadata = json.RawMessage(meta.String)
	}
	if archivedAt.Valid {
		p.ArchivedAt = archivedAt.String
	}
	return &p, nil
}

// ─── Event emission ─────────────────────────────────────────────────

func emitProduct(ctx *sdk.AppCtx, topic string, p *Product) {
	if ctx == nil || p == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"id":   p.ID,
		"name": p.Name,
		"type": p.Type,
		"slug": p.Slug,
	})
}

func emitPrice(ctx *sdk.AppCtx, topic string, p *Price) {
	if ctx == nil || p == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"id":         p.ID,
		"product_id": p.ProductID,
		"currency":   p.Currency,
		"amount":     p.UnitAmountCents,
		"interval":   p.Interval,
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

func intArg(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return def
		}
		return n
	}
	return def
}

func boolArg(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	if v, ok := m[key].(bool); ok {
		return v
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

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil || ctx.Config() == nil {
		return def
	}
	if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
		return v
	}
	return def
}
