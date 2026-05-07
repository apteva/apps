// Billing v0.1.0 — local-only path.
//
// Customers, invoices (with line items), and payments. Per-invoice
// `provider` field is the single most important forward-compat
// decision: it's set at create time and frozen for the row's life,
// so v0.1.1's Stripe provider can land alongside existing local rows
// with no migration. v0.1.0 enforces provider='local' at create.
//
// Every row is project-partitioned; the same code serves both
// `scope: project` (one install per project, partition key in env)
// and `scope: global` (one install across projects, partition key
// passed by the caller). resolveProject() picks the right one.
//
// The agent calls MCP tools; the dashboard calls the REST surface
// at /api/apps/billing/*. Both end up at the same DB layer.
package main

import (
	"database/sql"
	"encoding/base64"
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

// ─── Manifest (mirrors apteva.yaml; embedded so the running binary
// is self-describing) ─────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: billing
display_name: Billing
version: 0.1.0
description: |
  Customers, invoices, and payments. Per-invoice provider — local for
  internal/wire/cash, stripe for card-payable hosted invoices (v0.1.1+).
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
    entry: mcp/billing
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/billing.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("billing requires a db block")
	}
	// Stash the ctx so HTTP handlers — which the SDK invokes without
	// passing AppCtx — can reach it. (Same pattern as crm.)
	globalCtx = ctx

	// Surface the v0.1.0 ⇄ stripe gap loud-and-clear at boot. v0.1.0
	// can't honour stripe_enabled=true; we log a warning rather than
	// fail boot so an install pre-configured for v0.1.1 doesn't get
	// stuck.
	if cfg := ctx.Config(); cfg != nil {
		if strings.EqualFold(strings.TrimSpace(cfg.Get("stripe_enabled")), "true") {
			ctx.Logger().Warn("billing v0.1.0: stripe_enabled=true ignored — Stripe provider lands in v0.1.1; running local-only")
		}
	}

	ctx.Logger().Info("billing mounted",
		"version", "0.1.0",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ────────────────────────────────────────────────────
//
// Reverse-proxied at /api/apps/billing/* by apteva-server. The
// dashboard passes ?project_id=<id> (when scope=global) and
// ?install_id=<id> on every URL.

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/customers", Handler: a.handleHTTPCustomersCollection},
		{Pattern: "/customers/", Handler: a.handleHTTPCustomerItem},
		{Pattern: "/invoices", Handler: a.handleHTTPInvoicesCollection},
		{Pattern: "/invoices/", Handler: a.handleHTTPInvoiceItem},
		{Pattern: "/payments", Handler: a.handleHTTPPaymentsCollection},
	}
}

func (a *App) handleHTTPCustomersCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPCustomersList(w, r)
	case http.MethodPost:
		a.handleHTTPCustomerUpsert(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPCustomerItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/customers/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 2 && parts[1] == "context" && r.Method == http.MethodGet {
		a.handleHTTPCustomerContext(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPCustomerGet(w, r)
	case http.MethodPatch:
		a.handleHTTPCustomerUpdate(w, r)
	case http.MethodDelete:
		a.handleHTTPCustomerDelete(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPInvoicesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPInvoicesList(w, r)
	case http.MethodPost:
		a.handleHTTPInvoiceCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPInvoiceItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/invoices/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 2 {
		switch parts[1] {
		case "finalize":
			if r.Method == http.MethodPost {
				a.handleHTTPInvoiceFinalize(w, r)
				return
			}
		case "void":
			if r.Method == http.MethodPost {
				a.handleHTTPInvoiceVoid(w, r)
				return
			}
		case "line-items":
			if r.Method == http.MethodPost {
				a.handleHTTPInvoiceAddLineItem(w, r)
				return
			}
		case "payments":
			if r.Method == http.MethodGet {
				a.handleHTTPInvoicePayments(w, r)
				return
			}
		case "print":
			if r.Method == http.MethodGet {
				a.handleHTTPInvoicePrint(w, r)
				return
			}
		case "pdf":
			if r.Method == http.MethodGet {
				a.handleHTTPInvoicePDF(w, r)
				return
			}
		}
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPInvoiceGet(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPPaymentsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPPaymentsList(w, r)
	case http.MethodPost:
		a.handleHTTPPaymentRecord(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─── MCP tools ──────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		// ── Customers ────────────────────────────────────────────────
		{
			Name:        "customers_search",
			Description: "Filtered customer search. Args: q (free text matches name+email), email (exact), limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"q":     map[string]any{"type": "string"},
				"email": map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolCustomersSearch,
		},
		{
			Name:        "customers_get",
			Description: "Fetch one customer (snapshot only). Args: id OR email.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"email": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolCustomersGet,
		},
		{
			Name:        "customers_get_context",
			Description: "Snapshot + open invoices + recent payments + lifetime totals — pre-flight read before drafting an invoice. Args: id OR email, payments_limit (default 10).",
			InputSchema: schemaObject(map[string]any{
				"id":             map[string]any{"type": "integer"},
				"email":          map[string]any{"type": "string"},
				"payments_limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolCustomersGetContext,
		},
		{
			Name:        "customers_upsert_by_email",
			Description: "Find-or-create by email. Returns {customer, was_created}. Args: email, defaults (subset of customer fields used only on create).",
			InputSchema: schemaObject(map[string]any{
				"email":    map[string]any{"type": "string"},
				"defaults": map[string]any{"type": "object"},
			}, []string{"email"}),
			Handler: a.toolCustomersUpsertByEmail,
		},
		{
			Name:        "customers_update",
			Description: "Partial-patch a customer. Args: id, patch (any subset of customer fields).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"patch": map[string]any{"type": "object"},
			}, []string{"id", "patch"}),
			Handler: a.toolCustomersUpdate,
		},
		{
			Name:        "customers_merge",
			Description: "Merge loser_id into winner_id. Reassigns invoices and payments; loser is soft-deleted. Args: loser_id, winner_id.",
			InputSchema: schemaObject(map[string]any{
				"loser_id":  map[string]any{"type": "integer"},
				"winner_id": map[string]any{"type": "integer"},
			}, []string{"loser_id", "winner_id"}),
			Handler: a.toolCustomersMerge,
		},

		// ── Invoices ─────────────────────────────────────────────────
		{
			Name: "invoices_create",
			Description: "Create a DRAFT invoice with optional initial line items. Provider arg ('local'|'stripe') falls back to install default. PROVIDER IS FROZEN: to switch, delete and recreate. Args: customer_id, currency (default install setting), provider, due_date, notes, line_items [{description, quantity, unit_price_cents, tax_rate_bps?}].",
			InputSchema: schemaObject(map[string]any{
				"customer_id": map[string]any{"type": "integer"},
				"currency":    map[string]any{"type": "string"},
				"provider":    map[string]any{"type": "string"},
				"due_date":    map[string]any{"type": "string"},
				"notes":       map[string]any{"type": "string"},
				"line_items":  map[string]any{"type": "array"},
				"metadata":    map[string]any{"type": "object"},
			}, []string{"customer_id"}),
			Handler: a.toolInvoicesCreate,
		},
		{
			Name:        "invoices_add_line_item",
			Description: "Append a line item to a DRAFT invoice. Errors on non-draft invoices. Args: invoice_id, description, quantity (default 1), unit_price_cents, tax_rate_bps (default install setting).",
			InputSchema: schemaObject(map[string]any{
				"invoice_id":       map[string]any{"type": "integer"},
				"description":      map[string]any{"type": "string"},
				"quantity":         map[string]any{"type": "number"},
				"unit_price_cents": map[string]any{"type": "integer"},
				"tax_rate_bps":     map[string]any{"type": "integer"},
				"metadata":         map[string]any{"type": "object"},
			}, []string{"invoice_id", "description", "unit_price_cents"}),
			Handler: a.toolInvoicesAddLineItem,
		},
		{
			Name:        "invoices_finalize",
			Description: "Transition draft → open. Mints the project-scoped invoice number. Idempotent — re-finalizing an already-open invoice returns the existing record. v0.1.0: provider=local only.",
			InputSchema: schemaObject(map[string]any{
				"invoice_id": map[string]any{"type": "integer"},
			}, []string{"invoice_id"}),
			Handler: a.toolInvoicesFinalize,
		},
		{
			Name:        "invoices_void",
			Description: "Void an open or uncollectible invoice. Cannot void paid invoices. Cannot void drafts (delete instead). Args: invoice_id, reason.",
			InputSchema: schemaObject(map[string]any{
				"invoice_id": map[string]any{"type": "integer"},
				"reason":     map[string]any{"type": "string"},
			}, []string{"invoice_id"}),
			Handler: a.toolInvoicesVoid,
		},
		{
			Name:        "invoices_get",
			Description: "Fetch one invoice with line items, payment history, and audit log. Args: id OR number.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"number": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolInvoicesGet,
		},
		{
			Name:        "invoices_search",
			Description: "Filter invoices. Args: customer_id, status (draft|open|paid|void|uncollectible), provider (local|stripe), currency, since (RFC3339), until (RFC3339), min_total_cents, max_total_cents, limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"customer_id":     map[string]any{"type": "integer"},
				"status":          map[string]any{"type": "string"},
				"provider":        map[string]any{"type": "string"},
				"currency":        map[string]any{"type": "string"},
				"since":           map[string]any{"type": "string"},
				"until":           map[string]any{"type": "string"},
				"min_total_cents": map[string]any{"type": "integer"},
				"max_total_cents": map[string]any{"type": "integer"},
				"limit":           map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolInvoicesSearch,
		},

		// ── Payments ─────────────────────────────────────────────────
		{
			Name:        "payments_record",
			Description: "Record a non-Stripe payment (wire / cash / check / other). Updates invoice.amount_paid_cents and transitions to 'paid' when fully covered. REJECTS method='stripe' — that path is owned by the v0.1.1 reconciler. Args: invoice_id, amount_cents (cents; negative = refund record), method, received_at (RFC3339, default now), notes.",
			InputSchema: schemaObject(map[string]any{
				"invoice_id":   map[string]any{"type": "integer"},
				"amount_cents": map[string]any{"type": "integer"},
				"method":       map[string]any{"type": "string"},
				"received_at":  map[string]any{"type": "string"},
				"notes":        map[string]any{"type": "string"},
			}, []string{"invoice_id", "amount_cents", "method"}),
			Handler: a.toolPaymentsRecord,
		},
		{
			Name:        "invoices_render_pdf",
			Description: "Render an invoice as a PDF. Default returns {pdf_base64, filename, size_bytes}. With save_to_storage=true, writes the PDF to the storage app (must be installed) and returns {file_id, url, filename, size_bytes} so the agent can attach it to chat / email. Args: invoice_id, save_to_storage (default false), folder (storage path, default '/invoices/').",
			InputSchema: schemaObject(map[string]any{
				"invoice_id":       map[string]any{"type": "integer"},
				"save_to_storage":  map[string]any{"type": "boolean"},
				"folder":           map[string]any{"type": "string"},
			}, []string{"invoice_id"}),
			Handler: a.toolInvoicesRenderPDF,
		},
		{
			Name:        "payments_list",
			Description: "List payments. Args: customer_id, invoice_id, method, since (RFC3339), until (RFC3339), limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"customer_id": map[string]any{"type": "integer"},
				"invoice_id":  map[string]any{"type": "integer"},
				"method":      map[string]any{"type": "string"},
				"since":       map[string]any{"type": "string"},
				"until":       map[string]any{"type": "string"},
				"limit":       map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolPaymentsList,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution ─────────────────────────────────────────────

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

// callerActor extracts the audit-log actor string from args. Agents
// inject their identity via _caller; the dashboard's REST surface
// passes "human:<id>" via X-Actor header. Falls back to "system".
func callerActor(args map[string]any) string {
	if v, ok := args["_caller"].(string); ok && v != "" {
		return v
	}
	return "system"
}

func actorFromRequest(r *http.Request) string {
	if v := r.Header.Get("X-Actor"); v != "" {
		return v
	}
	return "human:unknown"
}

// ─── Domain types ───────────────────────────────────────────────────

type Customer struct {
	ID             int64           `json:"id"`
	ProjectID      string          `json:"project_id,omitempty"`
	Name           string          `json:"name"`
	Email          string          `json:"email,omitempty"`
	Phone          string          `json:"phone,omitempty"`
	BillingAddress json.RawMessage `json:"billing_address,omitempty"`
	TaxIDs         json.RawMessage `json:"tax_ids,omitempty"`
	Currency       string          `json:"currency,omitempty"`
	ExternalID     string          `json:"external_id,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	CreatedAt      string          `json:"created_at,omitempty"`
	UpdatedAt      string          `json:"updated_at,omitempty"`
	DeletedAt      string          `json:"deleted_at,omitempty"`
}

type Invoice struct {
	ID              int64           `json:"id"`
	ProjectID       string          `json:"project_id,omitempty"`
	CustomerID      int64           `json:"customer_id"`
	Provider        string          `json:"provider"`
	Number          string          `json:"number,omitempty"`
	Status          string          `json:"status"`
	Currency        string          `json:"currency"`
	SubtotalCents   int64           `json:"subtotal_cents"`
	TaxCents        int64           `json:"tax_cents"`
	TotalCents      int64           `json:"total_cents"`
	AmountPaidCents int64           `json:"amount_paid_cents"`
	DueDate         string          `json:"due_date,omitempty"`
	Notes           string          `json:"notes,omitempty"`
	ExternalID      string          `json:"external_id,omitempty"`
	ExternalURL     string          `json:"external_url,omitempty"`
	LastSyncedAt    string          `json:"last_synced_at,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	FinalizedAt     string          `json:"finalized_at,omitempty"`
	PaidAt          string          `json:"paid_at,omitempty"`
	VoidedAt        string          `json:"voided_at,omitempty"`
	CreatedAt       string          `json:"created_at,omitempty"`
	UpdatedAt       string          `json:"updated_at,omitempty"`
	LineItems       []LineItem      `json:"line_items,omitempty"`
	Payments        []*Payment      `json:"payments,omitempty"`
	AuditLog        []AuditEntry    `json:"audit_log,omitempty"`
}

type LineItem struct {
	ID             int64           `json:"id,omitempty"`
	InvoiceID      int64           `json:"invoice_id,omitempty"`
	Position       int             `json:"position"`
	Description    string          `json:"description"`
	Quantity       float64         `json:"quantity"`
	UnitPriceCents int64           `json:"unit_price_cents"`
	AmountCents    int64           `json:"amount_cents"`
	TaxRateBps     int             `json:"tax_rate_bps"`
	ExternalID     string          `json:"external_id,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

type Payment struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
	InvoiceID   *int64 `json:"invoice_id,omitempty"`
	CustomerID  int64  `json:"customer_id"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
	Method      string `json:"method"`
	ExternalID  string `json:"external_id,omitempty"`
	ReceivedAt  string `json:"received_at"`
	Notes       string `json:"notes,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

type AuditEntry struct {
	ID        int64           `json:"id"`
	InvoiceID int64           `json:"invoice_id"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Details   json.RawMessage `json:"details,omitempty"`
	CreatedAt string          `json:"created_at"`
}

// ─── MCP tool handlers ──────────────────────────────────────────────

func (a *App) toolCustomersSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := dbCustomerSearch(ctx.AppDB(), pid,
		strArg(args, "q"), strArg(args, "email"), limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"customers": rows, "count": len(rows)}, nil
}

func (a *App) toolCustomersGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c, err := lookupCustomer(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return map[string]any{"customer": nil, "found": false}, nil
	}
	return map[string]any{"customer": c, "found": true}, nil
}

func (a *App) toolCustomersGetContext(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c, err := lookupCustomer(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return map[string]any{"customer": nil, "found": false}, nil
	}
	plimit := intArg(args, "payments_limit", 10)
	if plimit <= 0 || plimit > 100 {
		plimit = 10
	}
	openInvs, err := dbInvoiceSearch(ctx.AppDB(), pid, invoiceFilters{
		customerID: c.ID, status: "open", limit: 50,
	})
	if err != nil {
		return nil, err
	}
	pays, err := dbPaymentList(ctx.AppDB(), pid, paymentFilters{
		customerID: c.ID, limit: plimit,
	})
	if err != nil {
		return nil, err
	}
	totals, err := dbCustomerTotals(ctx.AppDB(), pid, c.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"customer":       c,
		"open_invoices":  openInvs,
		"recent_payments": pays,
		"lifetime":       totals,
		"found":          true,
	}, nil
}

func (a *App) toolCustomersUpsertByEmail(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	email := normaliseEmail(strArg(args, "email"))
	if email == "" {
		return nil, errors.New("email required")
	}
	defaults, _ := args["defaults"].(map[string]any)
	c, created, err := dbCustomerUpsertByEmail(ctx.AppDB(), pid, email, defaults)
	if err != nil {
		return nil, err
	}
	emitCustomer(ctx, ifThen(created, "customer.added", "customer.updated"), c)
	return map[string]any{"customer": c, "was_created": created}, nil
}

func (a *App) toolCustomersUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	patch, _ := args["patch"].(map[string]any)
	if id == 0 || patch == nil {
		return nil, errors.New("id and patch required")
	}
	c, err := dbCustomerUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		return nil, err
	}
	emitCustomer(ctx, "customer.updated", c)
	return map[string]any{"customer": c}, nil
}

func (a *App) toolCustomersMerge(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	loser := int64Arg(args, "loser_id")
	winner := int64Arg(args, "winner_id")
	if loser == 0 || winner == 0 || loser == winner {
		return nil, errors.New("loser_id and winner_id required and must differ")
	}
	if err := dbCustomerMerge(ctx.AppDB(), pid, loser, winner); err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("customer.merged", map[string]any{
			"winner_id": winner, "loser_id": loser,
		})
	}
	return map[string]any{"merged": true, "winner_id": winner, "loser_id": loser}, nil
}

func (a *App) toolInvoicesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cid := int64Arg(args, "customer_id")
	if cid == 0 {
		return nil, errors.New("customer_id required")
	}
	provider := strArg(args, "provider")
	if provider == "" {
		provider = configString(ctx, "default_provider", "local")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	// v0.1.0 gate. v0.1.1 will remove this branch.
	if provider == "stripe" {
		return nil, errors.New("provider='stripe' lands in v0.1.1 — use provider='local' for now")
	}
	if provider != "local" {
		return nil, fmt.Errorf("unknown provider %q (expected 'local' or 'stripe')", provider)
	}
	currency := strings.ToUpper(strArg(args, "currency"))
	if currency == "" {
		currency = strings.ToUpper(configString(ctx, "default_currency", "USD"))
	}
	if !looksLikeISO4217(currency) {
		return nil, fmt.Errorf("currency %q not a 3-letter ISO 4217 code", currency)
	}

	rawItems, _ := args["line_items"].([]any)
	items, err := normaliseLineItems(rawItems, configIntBps(ctx, "tax_default_rate_bps"))
	if err != nil {
		return nil, err
	}

	inv := &Invoice{
		ProjectID:  pid,
		CustomerID: cid,
		Provider:   provider,
		Status:     "draft",
		Currency:   currency,
		DueDate:    strArg(args, "due_date"),
		Notes:      strArg(args, "notes"),
		LineItems:  items,
	}
	if md, ok := args["metadata"].(map[string]any); ok {
		if raw, err := json.Marshal(md); err == nil {
			inv.Metadata = raw
		}
	}

	created, err := dbInvoiceCreate(ctx.AppDB(), inv, callerActor(args))
	if err != nil {
		return nil, err
	}
	emitInvoice(ctx, "invoice.added", created)
	return map[string]any{"invoice": created}, nil
}

func (a *App) toolInvoicesAddLineItem(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invoice_id")
	desc := strArg(args, "description")
	unit := int64Arg(args, "unit_price_cents")
	if id == 0 || desc == "" {
		return nil, errors.New("invoice_id and description required")
	}
	qty := float64Arg(args, "quantity", 1)
	if qty <= 0 {
		return nil, errors.New("quantity must be > 0")
	}
	taxBps := intArg(args, "tax_rate_bps", configIntBps(ctx, "tax_default_rate_bps"))
	if taxBps < 0 || taxBps > 100000 {
		return nil, errors.New("tax_rate_bps out of range")
	}
	li := LineItem{
		Description:    desc,
		Quantity:       qty,
		UnitPriceCents: unit,
		AmountCents:    roundCents(qty * float64(unit)),
		TaxRateBps:     taxBps,
	}
	if md, ok := args["metadata"].(map[string]any); ok {
		if raw, err := json.Marshal(md); err == nil {
			li.Metadata = raw
		}
	}
	inv, err := dbInvoiceAddLineItem(ctx.AppDB(), pid, id, li, callerActor(args))
	if err != nil {
		return nil, err
	}
	emitInvoice(ctx, "invoice.updated", inv)
	return map[string]any{"invoice": inv}, nil
}

func (a *App) toolInvoicesFinalize(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invoice_id")
	if id == 0 {
		return nil, errors.New("invoice_id required")
	}
	inv, err := dbInvoiceFinalize(ctx.AppDB(), pid, id,
		configString(ctx, "invoice_number_format", "INV-{yyyy}-{seq:04}"),
		callerActor(args))
	if err != nil {
		return nil, err
	}
	emitInvoice(ctx, "invoice.finalized", inv)
	return map[string]any{"invoice": inv}, nil
}

func (a *App) toolInvoicesVoid(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invoice_id")
	if id == 0 {
		return nil, errors.New("invoice_id required")
	}
	inv, err := dbInvoiceVoid(ctx.AppDB(), pid, id,
		strArg(args, "reason"), callerActor(args))
	if err != nil {
		return nil, err
	}
	emitInvoice(ctx, "invoice.voided", inv)
	return map[string]any{"invoice": inv}, nil
}

func (a *App) toolInvoicesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	inv, err := lookupInvoice(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return map[string]any{"invoice": nil, "found": false}, nil
	}
	if err := loadInvoiceChildren(ctx.AppDB(), pid, inv); err != nil {
		return nil, err
	}
	return map[string]any{"invoice": inv, "found": true}, nil
}

func (a *App) toolInvoicesSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out, err := dbInvoiceSearch(ctx.AppDB(), pid, invoiceFilters{
		customerID:    int64Arg(args, "customer_id"),
		status:        strArg(args, "status"),
		provider:      strArg(args, "provider"),
		currency:      strArg(args, "currency"),
		since:         strArg(args, "since"),
		until:         strArg(args, "until"),
		minTotalCents: int64Arg(args, "min_total_cents"),
		maxTotalCents: int64Arg(args, "max_total_cents"),
		limit:         limit,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"invoices": out, "count": len(out)}, nil
}

func (a *App) toolPaymentsRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invoice_id")
	amount := int64Arg(args, "amount_cents")
	method := strings.ToLower(strArg(args, "method"))
	if id == 0 || method == "" {
		return nil, errors.New("invoice_id and method required")
	}
	if amount == 0 {
		return nil, errors.New("amount_cents must be non-zero")
	}
	if method == "stripe" {
		return nil, errors.New("method='stripe' is reserved for the v0.1.1 reconciler — use 'wire', 'cash', 'check', or 'other'")
	}
	switch method {
	case "wire", "cash", "check", "other":
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
	received := strArg(args, "received_at")
	if received == "" {
		received = time.Now().UTC().Format(time.RFC3339)
	}
	pay, inv, err := dbPaymentRecord(ctx.AppDB(), pid, id, amount, method, received,
		strArg(args, "notes"), callerActor(args))
	if err != nil {
		return nil, err
	}
	emitInvoice(ctx, "invoice.paid", inv) // listeners filter on status == 'paid'
	return map[string]any{"payment": pay, "invoice": inv}, nil
}

func (a *App) toolPaymentsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out, err := dbPaymentList(ctx.AppDB(), pid, paymentFilters{
		customerID: int64Arg(args, "customer_id"),
		invoiceID:  int64Arg(args, "invoice_id"),
		method:     strArg(args, "method"),
		since:      strArg(args, "since"),
		until:      strArg(args, "until"),
		limit:      limit,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"payments": out, "count": len(out)}, nil
}

// ─── PDF rendering ──────────────────────────────────────────────────

// loadInvoiceForRender loads an invoice + its line items + the
// customer record. Used by both the HTTP /print + /pdf paths and
// the MCP tool. Returns nil/nil when the invoice doesn't exist.
func loadInvoiceForRender(db *sql.DB, pid string, id int64) (*Invoice, *Customer, error) {
	inv, err := dbInvoiceGetByID(db, pid, id)
	if err != nil || inv == nil {
		return inv, nil, err
	}
	if err := loadInvoiceChildren(db, pid, inv); err != nil {
		return inv, nil, err
	}
	cust, err := dbCustomerGetByID(db, pid, inv.CustomerID)
	// Customer may have been soft-deleted after the invoice landed —
	// fall through with nil customer; the renderer falls back to "#<id>".
	if err != nil {
		return inv, nil, err
	}
	return inv, cust, nil
}

func (a *App) toolInvoicesRenderPDF(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invoice_id")
	if id == 0 {
		return nil, errors.New("invoice_id required")
	}
	inv, cust, err := loadInvoiceForRender(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, fmt.Errorf("invoice %d not found", id)
	}
	pdfBytes, err := renderInvoicePDF(inv, cust)
	if err != nil {
		return nil, err
	}
	filename := suggestPDFFilename(inv)

	saveToStorage, _ := args["save_to_storage"].(bool)
	if !saveToStorage {
		return map[string]any{
			"pdf_base64": base64.StdEncoding.EncodeToString(pdfBytes),
			"filename":   filename,
			"size_bytes": len(pdfBytes),
			"saved":      false,
		}, nil
	}

	folder, _ := args["folder"].(string)
	if folder == "" {
		folder = "/invoices/"
	}
	// Cross-app call: hand the bytes to storage's files_upload tool.
	// Falls back to base64 + a clear error reason if storage isn't
	// installed for this project — keeps the agent's failure mode
	// recoverable ("retry without save_to_storage").
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("save_to_storage=true requires the platform API; running outside an Apteva server")
	}
	var got struct {
		ID int64 `json:"id"`
	}
	if callErr := ctx.PlatformAPI().CallAppResult("storage", "files_upload", map[string]any{
		"name":           filename,
		"folder":         folder,
		"content_base64": base64.StdEncoding.EncodeToString(pdfBytes),
		"content_type":   "application/pdf",
		"tags":           []any{"invoice", "billing", inv.Status},
		"source":         "billing",
	}, &got); callErr != nil {
		return nil, fmt.Errorf("save_to_storage: storage app call failed (%w) — install the storage app or retry with save_to_storage=false", callErr)
	}
	if got.ID == 0 {
		return nil, errors.New("save_to_storage: storage returned no file id")
	}
	storageID := got.ID
	return map[string]any{
		"file_id":    storageID,
		"url":        fmt.Sprintf("/api/apps/storage/files/%d/content?project_id=%s", storageID, pid),
		"filename":   filename,
		"size_bytes": len(pdfBytes),
		"saved":      true,
	}, nil
}

func (a *App) handleHTTPInvoicePrint(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/invoices/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	inv, cust, err := loadInvoiceForRender(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if inv == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write([]byte(renderInvoiceHTML(inv, cust)))
}

func (a *App) handleHTTPInvoicePDF(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/invoices/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	inv, cust, err := loadInvoiceForRender(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if inv == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	pdfBytes, err := renderInvoicePDF(inv, cust)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	filename := suggestPDFFilename(inv)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "private, no-store")
	// `inline` so browsers display the PDF in-tab; users save via
	// the browser's PDF viewer toolbar. The dashboard's "Download"
	// button can override with `?download=1` (handled by the panel
	// JS, not server-side — keeps this endpoint simple).
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	_, _ = w.Write(pdfBytes)
}

// ─── Event emission ─────────────────────────────────────────────────

func emitCustomer(ctx *sdk.AppCtx, topic string, c *Customer) {
	if ctx == nil || c == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"id":    c.ID,
		"name":  c.Name,
		"email": c.Email,
	})
}

func emitInvoice(ctx *sdk.AppCtx, topic string, inv *Invoice) {
	if ctx == nil || inv == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"id":          inv.ID,
		"customer_id": inv.CustomerID,
		"number":      inv.Number,
		"status":      inv.Status,
		"total_cents": inv.TotalCents,
		"currency":    inv.Currency,
	})
}

// ─── HTTP handlers ──────────────────────────────────────────────────

func (a *App) handleHTTPCustomersList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query().Get("q")
	email := r.URL.Query().Get("email")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := dbCustomerSearch(ctx.AppDB(), pid, q, email, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"customers": rows, "count": len(rows)})
}

func (a *App) handleHTTPCustomerUpsert(w http.ResponseWriter, r *http.Request) {
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
	email := normaliseEmail(strArg(body, "email"))
	if email == "" {
		httpErr(w, http.StatusBadRequest, "email required")
		return
	}
	defaults, _ := body["defaults"].(map[string]any)
	if defaults == nil {
		// Legacy shape: pass top-level fields as defaults too.
		defaults = body
	}
	c, created, err := dbCustomerUpsertByEmail(ctx.AppDB(), pid, email, defaults)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	emitCustomer(ctx, ifThen(created, "customer.added", "customer.updated"), c)
	httpJSON(w, map[string]any{"customer": c, "was_created": created})
}

func (a *App) handleHTTPCustomerGet(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/customers/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	c, err := dbCustomerGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"customer": c})
}

func (a *App) handleHTTPCustomerContext(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/customers/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	c, err := dbCustomerGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	openInvs, _ := dbInvoiceSearch(ctx.AppDB(), pid, invoiceFilters{
		customerID: id, status: "open", limit: 50,
	})
	pays, _ := dbPaymentList(ctx.AppDB(), pid, paymentFilters{customerID: id, limit: 10})
	totals, _ := dbCustomerTotals(ctx.AppDB(), pid, id)
	httpJSON(w, map[string]any{
		"customer":        c,
		"open_invoices":   openInvs,
		"recent_payments": pays,
		"lifetime":        totals,
	})
}

func (a *App) handleHTTPCustomerUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/customers/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	c, err := dbCustomerUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	emitCustomer(ctx, "customer.updated", c)
	httpJSON(w, map[string]any{"customer": c})
}

func (a *App) handleHTTPCustomerDelete(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/customers/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE customers SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ctx != nil {
		ctx.Emit("customer.deleted", map[string]any{"id": id})
	}
	httpJSON(w, map[string]any{"deleted": true})
}

func (a *App) handleHTTPInvoicesList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cid, _ := strconv.ParseInt(q.Get("customer_id"), 10, 64)
	out, err := dbInvoiceSearch(ctx.AppDB(), pid, invoiceFilters{
		customerID: cid,
		status:     q.Get("status"),
		provider:   q.Get("provider"),
		currency:   q.Get("currency"),
		since:      q.Get("since"),
		until:      q.Get("until"),
		limit:      limit,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"invoices": out, "count": len(out)})
}

func (a *App) handleHTTPInvoiceCreate(w http.ResponseWriter, r *http.Request) {
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
	cid := int64Arg(body, "customer_id")
	if cid == 0 {
		httpErr(w, http.StatusBadRequest, "customer_id required")
		return
	}
	provider := strArg(body, "provider")
	if provider == "" {
		provider = configString(ctx, "default_provider", "local")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "stripe" {
		httpErr(w, http.StatusBadRequest, "provider='stripe' lands in v0.1.1")
		return
	}
	if provider != "local" {
		httpErr(w, http.StatusBadRequest, "unknown provider")
		return
	}
	currency := strings.ToUpper(strArg(body, "currency"))
	if currency == "" {
		currency = strings.ToUpper(configString(ctx, "default_currency", "USD"))
	}
	if !looksLikeISO4217(currency) {
		httpErr(w, http.StatusBadRequest, "currency must be 3-letter ISO 4217")
		return
	}
	rawItems, _ := body["line_items"].([]any)
	items, err := normaliseLineItems(rawItems, configIntBps(ctx, "tax_default_rate_bps"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	inv := &Invoice{
		ProjectID:  pid,
		CustomerID: cid,
		Provider:   provider,
		Status:     "draft",
		Currency:   currency,
		DueDate:    strArg(body, "due_date"),
		Notes:      strArg(body, "notes"),
		LineItems:  items,
	}
	if md, ok := body["metadata"].(map[string]any); ok {
		if raw, err := json.Marshal(md); err == nil {
			inv.Metadata = raw
		}
	}
	created, err := dbInvoiceCreate(ctx.AppDB(), inv, actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	emitInvoice(ctx, "invoice.added", created)
	httpJSON(w, map[string]any{"invoice": created})
}

func (a *App) handleHTTPInvoiceGet(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/invoices/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	inv, err := dbInvoiceGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if inv == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := loadInvoiceChildren(ctx.AppDB(), pid, inv); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"invoice": inv})
}

func (a *App) handleHTTPInvoiceFinalize(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/invoices/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	inv, err := dbInvoiceFinalize(ctx.AppDB(), pid, id,
		configString(ctx, "invoice_number_format", "INV-{yyyy}-{seq:04}"),
		actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitInvoice(ctx, "invoice.finalized", inv)
	httpJSON(w, map[string]any{"invoice": inv})
}

func (a *App) handleHTTPInvoiceVoid(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/invoices/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	inv, err := dbInvoiceVoid(ctx.AppDB(), pid, id, strArg(body, "reason"), actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitInvoice(ctx, "invoice.voided", inv)
	httpJSON(w, map[string]any{"invoice": inv})
}

func (a *App) handleHTTPInvoiceAddLineItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/invoices/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	desc := strArg(body, "description")
	unit := int64Arg(body, "unit_price_cents")
	qty := float64Arg(body, "quantity", 1)
	taxBps := intArg(body, "tax_rate_bps", configIntBps(ctx, "tax_default_rate_bps"))
	if desc == "" || qty <= 0 {
		httpErr(w, http.StatusBadRequest, "description and quantity>0 required")
		return
	}
	li := LineItem{
		Description:    desc,
		Quantity:       qty,
		UnitPriceCents: unit,
		AmountCents:    roundCents(qty * float64(unit)),
		TaxRateBps:     taxBps,
	}
	inv, err := dbInvoiceAddLineItem(ctx.AppDB(), pid, id, li, actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitInvoice(ctx, "invoice.updated", inv)
	httpJSON(w, map[string]any{"invoice": inv})
}

func (a *App) handleHTTPInvoicePayments(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/invoices/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	out, err := dbPaymentList(ctx.AppDB(), pid, paymentFilters{invoiceID: id, limit: 100})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"payments": out})
}

func (a *App) handleHTTPPaymentsList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cid, _ := strconv.ParseInt(q.Get("customer_id"), 10, 64)
	iid, _ := strconv.ParseInt(q.Get("invoice_id"), 10, 64)
	out, err := dbPaymentList(ctx.AppDB(), pid, paymentFilters{
		customerID: cid, invoiceID: iid,
		method: q.Get("method"),
		since:  q.Get("since"), until: q.Get("until"),
		limit: limit,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"payments": out, "count": len(out)})
}

func (a *App) handleHTTPPaymentRecord(w http.ResponseWriter, r *http.Request) {
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
	id := int64Arg(body, "invoice_id")
	amount := int64Arg(body, "amount_cents")
	method := strings.ToLower(strArg(body, "method"))
	if id == 0 || amount == 0 || method == "" {
		httpErr(w, http.StatusBadRequest, "invoice_id, amount_cents, method required")
		return
	}
	if method == "stripe" {
		httpErr(w, http.StatusBadRequest, "method='stripe' is reserved for the v0.1.1 reconciler")
		return
	}
	switch method {
	case "wire", "cash", "check", "other":
	default:
		httpErr(w, http.StatusBadRequest, "unknown method")
		return
	}
	received := strArg(body, "received_at")
	if received == "" {
		received = time.Now().UTC().Format(time.RFC3339)
	}
	pay, inv, err := dbPaymentRecord(ctx.AppDB(), pid, id, amount, method, received,
		strArg(body, "notes"), actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitInvoice(ctx, "invoice.paid", inv)
	httpJSON(w, map[string]any{"payment": pay, "invoice": inv})
}

// ─── DB layer ───────────────────────────────────────────────────────

// ── Customers ──

func dbCustomerSearch(db *sql.DB, pid, q, email string, limit int) ([]*Customer, error) {
	var (
		where = []string{"project_id = ?", "deleted_at IS NULL"}
		args  = []any{pid}
	)
	if email != "" {
		where = append(where, "email = ?")
		args = append(args, normaliseEmail(email))
	}
	if q != "" {
		where = append(where, "(name LIKE ? OR email LIKE ?)")
		pat := "%" + q + "%"
		args = append(args, pat, pat)
	}
	args = append(args, limit)
	sqlStr := `SELECT id, project_id, name, email, phone, billing_address, tax_ids,
	             currency, external_id, metadata, created_at, updated_at
	           FROM customers
	           WHERE ` + strings.Join(where, " AND ") + `
	           ORDER BY updated_at DESC
	           LIMIT ?`
	rows, err := db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Customer
	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func dbCustomerGetByID(db *sql.DB, pid string, id int64) (*Customer, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, email, phone, billing_address, tax_ids,
		        currency, external_id, metadata, created_at, updated_at
		 FROM customers
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, id, pid)
	c, err := scanCustomer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func dbCustomerGetByEmail(db *sql.DB, pid, email string) (*Customer, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, email, phone, billing_address, tax_ids,
		        currency, external_id, metadata, created_at, updated_at
		 FROM customers
		 WHERE project_id = ? AND email = ? AND deleted_at IS NULL
		 ORDER BY id DESC
		 LIMIT 1`, pid, email)
	c, err := scanCustomer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func lookupCustomer(db *sql.DB, pid string, args map[string]any) (*Customer, error) {
	if id := int64Arg(args, "id"); id != 0 {
		return dbCustomerGetByID(db, pid, id)
	}
	if email := normaliseEmail(strArg(args, "email")); email != "" {
		return dbCustomerGetByEmail(db, pid, email)
	}
	return nil, errors.New("id or email required")
}

func dbCustomerUpsertByEmail(db *sql.DB, pid, email string, defaults map[string]any) (*Customer, bool, error) {
	existing, err := dbCustomerGetByEmail(db, pid, email)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, false, nil
	}
	name := strArg(defaults, "name")
	if name == "" {
		name = email // fall back to email so the row has a non-empty name
	}
	addr := jsonOrEmpty(defaults["billing_address"], "{}")
	taxes := jsonOrEmpty(defaults["tax_ids"], "[]")
	meta := jsonOrEmpty(defaults["metadata"], "{}")
	now := nowRFC3339()
	res, err := db.Exec(
		`INSERT INTO customers (project_id, name, email, phone, billing_address, tax_ids,
		                       currency, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, name, email, strArg(defaults, "phone"), addr, taxes,
		strArg(defaults, "currency"), meta, now, now)
	if err != nil {
		return nil, false, err
	}
	id, _ := res.LastInsertId()
	c, err := dbCustomerGetByID(db, pid, id)
	return c, true, err
}

func dbCustomerUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Customer, error) {
	if len(patch) == 0 {
		return dbCustomerGetByID(db, pid, id)
	}
	allowed := map[string]bool{
		"name": true, "email": true, "phone": true,
		"currency": true, "billing_address": true, "tax_ids": true, "metadata": true,
	}
	var (
		sets  []string
		args  []any
	)
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		switch k {
		case "email":
			s, _ := v.(string)
			args = append(args, normaliseEmail(s))
		case "billing_address", "tax_ids", "metadata":
			args = append(args, jsonOrEmpty(v, ifThen(k == "tax_ids", "[]", "{}")))
		default:
			args = append(args, v)
		}
		sets = append(sets, k+" = ?")
	}
	if len(sets) == 0 {
		return dbCustomerGetByID(db, pid, id)
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id, pid)
	if _, err := db.Exec(
		`UPDATE customers SET `+strings.Join(sets, ", ")+
			` WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, args...); err != nil {
		return nil, err
	}
	return dbCustomerGetByID(db, pid, id)
}

func dbCustomerMerge(db *sql.DB, pid string, loser, winner int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Both sides must exist + not deleted.
	for _, id := range []int64{loser, winner} {
		var n int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM customers
			 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
			id, pid).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("customer %d not found or deleted", id)
		}
	}
	if _, err := tx.Exec(
		`UPDATE invoices SET customer_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE customer_id = ? AND project_id = ?`, winner, loser, pid); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE payments SET customer_id = ?
		 WHERE customer_id = ? AND project_id = ?`, winner, loser, pid); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE customers SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`, loser, pid); err != nil {
		return err
	}
	return tx.Commit()
}

func dbCustomerTotals(db *sql.DB, pid string, cid int64) (map[string]any, error) {
	out := map[string]any{
		"invoiced_cents":  int64(0),
		"paid_cents":      int64(0),
		"outstanding_cents": int64(0),
		"invoice_count":   0,
	}
	row := db.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(total_cents), 0),
		        COALESCE(SUM(amount_paid_cents), 0)
		 FROM invoices
		 WHERE project_id = ? AND customer_id = ?
		   AND deleted_at IS NULL AND status IN ('open','paid','uncollectible')`,
		pid, cid)
	var count int
	var invoiced, paid int64
	if err := row.Scan(&count, &invoiced, &paid); err != nil {
		return out, err
	}
	out["invoice_count"] = count
	out["invoiced_cents"] = invoiced
	out["paid_cents"] = paid
	out["outstanding_cents"] = invoiced - paid
	return out, nil
}

// ── Invoices ──

type invoiceFilters struct {
	customerID                       int64
	status, provider, currency       string
	since, until                     string
	minTotalCents, maxTotalCents     int64
	limit                            int
}

func dbInvoiceSearch(db *sql.DB, pid string, f invoiceFilters) ([]*Invoice, error) {
	where := []string{"project_id = ?", "deleted_at IS NULL"}
	args := []any{pid}
	if f.customerID != 0 {
		where = append(where, "customer_id = ?")
		args = append(args, f.customerID)
	}
	if f.status != "" {
		where = append(where, "status = ?")
		args = append(args, f.status)
	}
	if f.provider != "" {
		where = append(where, "provider = ?")
		args = append(args, f.provider)
	}
	if f.currency != "" {
		where = append(where, "currency = ?")
		args = append(args, strings.ToUpper(f.currency))
	}
	if f.since != "" {
		where = append(where, "created_at >= ?")
		args = append(args, f.since)
	}
	if f.until != "" {
		where = append(where, "created_at < ?")
		args = append(args, f.until)
	}
	if f.minTotalCents != 0 {
		where = append(where, "total_cents >= ?")
		args = append(args, f.minTotalCents)
	}
	if f.maxTotalCents != 0 {
		where = append(where, "total_cents <= ?")
		args = append(args, f.maxTotalCents)
	}
	limit := f.limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, project_id, customer_id, provider, number, status, currency,
		        subtotal_cents, tax_cents, total_cents, amount_paid_cents,
		        due_date, notes, external_id, external_url, last_synced_at, metadata,
		        finalized_at, paid_at, voided_at, created_at, updated_at
		 FROM invoices
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY updated_at DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Invoice
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func dbInvoiceGetByID(db *sql.DB, pid string, id int64) (*Invoice, error) {
	row := db.QueryRow(
		`SELECT id, project_id, customer_id, provider, number, status, currency,
		        subtotal_cents, tax_cents, total_cents, amount_paid_cents,
		        due_date, notes, external_id, external_url, last_synced_at, metadata,
		        finalized_at, paid_at, voided_at, created_at, updated_at
		 FROM invoices
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, id, pid)
	inv, err := scanInvoice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return inv, err
}

func dbInvoiceGetByNumber(db *sql.DB, pid, number string) (*Invoice, error) {
	row := db.QueryRow(
		`SELECT id, project_id, customer_id, provider, number, status, currency,
		        subtotal_cents, tax_cents, total_cents, amount_paid_cents,
		        due_date, notes, external_id, external_url, last_synced_at, metadata,
		        finalized_at, paid_at, voided_at, created_at, updated_at
		 FROM invoices
		 WHERE project_id = ? AND number = ? AND deleted_at IS NULL`, pid, number)
	inv, err := scanInvoice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return inv, err
}

func lookupInvoice(db *sql.DB, pid string, args map[string]any) (*Invoice, error) {
	if id := int64Arg(args, "id"); id != 0 {
		return dbInvoiceGetByID(db, pid, id)
	}
	if num := strArg(args, "number"); num != "" {
		return dbInvoiceGetByNumber(db, pid, num)
	}
	return nil, errors.New("id or number required")
}

func dbInvoiceCreate(db *sql.DB, inv *Invoice, actor string) (*Invoice, error) {
	// Verify customer exists + not deleted.
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM customers
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		inv.CustomerID, inv.ProjectID).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("customer %d not found", inv.CustomerID)
	}
	subtotal, tax, total := computeTotals(inv.LineItems)
	inv.SubtotalCents, inv.TaxCents, inv.TotalCents = subtotal, tax, total

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowRFC3339()
	res, err := tx.Exec(
		`INSERT INTO invoices (project_id, customer_id, provider, status, currency,
		                       subtotal_cents, tax_cents, total_cents, amount_paid_cents,
		                       due_date, notes, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, 'draft', ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		inv.ProjectID, inv.CustomerID, inv.Provider, inv.Currency,
		inv.SubtotalCents, inv.TaxCents, inv.TotalCents,
		nullStr(inv.DueDate), nullStr(inv.Notes), jsonOrEmpty(inv.Metadata, "{}"),
		now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	for i, li := range inv.LineItems {
		if _, err := tx.Exec(
			`INSERT INTO invoice_line_items
			   (invoice_id, position, description, quantity, unit_price_cents,
			    amount_cents, tax_rate_bps, metadata)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i, li.Description, li.Quantity, li.UnitPriceCents,
			li.AmountCents, li.TaxRateBps, jsonOrEmpty(li.Metadata, "{}")); err != nil {
			return nil, err
		}
	}
	if err := writeAuditTx(tx, id, actor, "create", map[string]any{
		"provider":      inv.Provider,
		"currency":      inv.Currency,
		"line_count":    len(inv.LineItems),
		"total_cents":   inv.TotalCents,
		"subtotal_cents": inv.SubtotalCents,
		"tax_cents":     inv.TaxCents,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out, err := dbInvoiceGetByID(db, inv.ProjectID, id)
	if err != nil || out == nil {
		return nil, err
	}
	if err := loadInvoiceChildren(db, inv.ProjectID, out); err != nil {
		return nil, err
	}
	return out, nil
}

func dbInvoiceAddLineItem(db *sql.DB, pid string, id int64, li LineItem, actor string) (*Invoice, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(
		`SELECT status FROM invoices
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("invoice %d not found", id)
		}
		return nil, err
	}
	if status != "draft" {
		return nil, fmt.Errorf("cannot add line item: invoice %d is %s, only drafts accept line items", id, status)
	}
	var pos int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(position)+1, 0) FROM invoice_line_items WHERE invoice_id = ?`,
		id).Scan(&pos); err != nil {
		return nil, err
	}
	li.Position = pos
	if _, err := tx.Exec(
		`INSERT INTO invoice_line_items
		   (invoice_id, position, description, quantity, unit_price_cents,
		    amount_cents, tax_rate_bps, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, li.Position, li.Description, li.Quantity, li.UnitPriceCents,
		li.AmountCents, li.TaxRateBps, jsonOrEmpty(li.Metadata, "{}")); err != nil {
		return nil, err
	}
	if err := recomputeInvoiceTotalsTx(tx, id); err != nil {
		return nil, err
	}
	if err := writeAuditTx(tx, id, actor, "add_line_item", map[string]any{
		"description":      li.Description,
		"quantity":         li.Quantity,
		"unit_price_cents": li.UnitPriceCents,
		"amount_cents":     li.AmountCents,
		"tax_rate_bps":     li.TaxRateBps,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	inv, err := dbInvoiceGetByID(db, pid, id)
	if err != nil || inv == nil {
		return nil, err
	}
	if err := loadInvoiceChildren(db, pid, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

// dbInvoiceFinalize transitions draft → open. Idempotent: re-finalizing
// an already-open invoice returns the existing record.
func dbInvoiceFinalize(db *sql.DB, pid string, id int64, format, actor string) (*Invoice, error) {
	// Single-Tx finalize so the count-based number mint is consistent.
	// v0.1.0 is single-replica; v0.2 HA will switch this to an
	// IMMEDIATE Tx (or an advisory lock) so concurrent finalizes
	// serialize cleanly. The unique partial index on
	// (project_id, number) catches the rare race today either way.
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		status, currency, provider string
		number                     sql.NullString
	)
	if err := tx.QueryRow(
		`SELECT status, currency, provider, number FROM invoices
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid).Scan(&status, &currency, &provider, &number); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("invoice %d not found", id)
		}
		return nil, err
	}
	if status == "open" || status == "paid" || status == "void" || status == "uncollectible" {
		// Idempotent — return current state.
		_ = tx.Commit()
		inv, err := dbInvoiceGetByID(db, pid, id)
		if err != nil || inv == nil {
			return nil, err
		}
		if err := loadInvoiceChildren(db, pid, inv); err != nil {
			return nil, err
		}
		return inv, nil
	}
	if status != "draft" {
		return nil, fmt.Errorf("cannot finalize: invoice %d has unexpected status %s", id, status)
	}
	// v0.1.0 only knows how to finalize local invoices. v0.1.1 will
	// branch here on provider.
	if provider != "local" {
		return nil, fmt.Errorf("provider=%q finalize unsupported in v0.1.0 — use 'local'", provider)
	}

	// Fail-fast: drafts with zero line items shouldn't finalize.
	var lineCount int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM invoice_line_items WHERE invoice_id = ?`, id).Scan(&lineCount); err != nil {
		return nil, err
	}
	if lineCount == 0 {
		return nil, errors.New("cannot finalize an empty draft — add at least one line item")
	}

	// Mint number.
	num, err := mintInvoiceNumberTx(tx, pid, format)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`UPDATE invoices
		 SET status = 'open', number = ?, finalized_at = CURRENT_TIMESTAMP,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, num, id); err != nil {
		// Unique-index conflict on number → race. Caller can retry.
		return nil, fmt.Errorf("finalize: %w", err)
	}
	if err := writeAuditTx(tx, id, actor, "finalize", map[string]any{
		"provider": provider,
		"number":   num,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	inv, err := dbInvoiceGetByID(db, pid, id)
	if err != nil || inv == nil {
		return nil, err
	}
	if err := loadInvoiceChildren(db, pid, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

func dbInvoiceVoid(db *sql.DB, pid string, id int64, reason, actor string) (*Invoice, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(
		`SELECT status FROM invoices WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("invoice %d not found", id)
		}
		return nil, err
	}
	switch status {
	case "void":
		// Idempotent.
	case "paid":
		return nil, errors.New("cannot void a paid invoice — record a refund via payments_record(amount<0)")
	case "draft":
		return nil, errors.New("cannot void a draft — delete it instead (drafts have no lasting effect)")
	case "open", "uncollectible":
		if _, err := tx.Exec(
			`UPDATE invoices SET status='void', voided_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			 WHERE id = ?`, id); err != nil {
			return nil, err
		}
		if err := writeAuditTx(tx, id, actor, "void", map[string]any{"reason": reason}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invoice has unexpected status %s", status)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	inv, err := dbInvoiceGetByID(db, pid, id)
	if err != nil || inv == nil {
		return nil, err
	}
	if err := loadInvoiceChildren(db, pid, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

func loadInvoiceChildren(db *sql.DB, pid string, inv *Invoice) error {
	rows, err := db.Query(
		`SELECT id, invoice_id, position, description, quantity, unit_price_cents,
		        amount_cents, tax_rate_bps, external_id, metadata
		 FROM invoice_line_items
		 WHERE invoice_id = ?
		 ORDER BY position ASC`, inv.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var li LineItem
		var ext sql.NullString
		var meta sql.NullString
		if err := rows.Scan(&li.ID, &li.InvoiceID, &li.Position, &li.Description,
			&li.Quantity, &li.UnitPriceCents, &li.AmountCents, &li.TaxRateBps,
			&ext, &meta); err != nil {
			rows.Close()
			return err
		}
		li.ExternalID = ext.String
		if meta.Valid {
			li.Metadata = json.RawMessage(meta.String)
		}
		inv.LineItems = append(inv.LineItems, li)
	}
	rows.Close()

	pays, err := dbPaymentList(db, pid, paymentFilters{invoiceID: inv.ID, limit: 200})
	if err != nil {
		return err
	}
	inv.Payments = pays

	auditRows, err := db.Query(
		`SELECT id, invoice_id, actor, action, details, created_at
		 FROM invoice_audit_log
		 WHERE invoice_id = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT 100`, inv.ID)
	if err != nil {
		return err
	}
	defer auditRows.Close()
	for auditRows.Next() {
		var a AuditEntry
		var det sql.NullString
		if err := auditRows.Scan(&a.ID, &a.InvoiceID, &a.Actor, &a.Action, &det, &a.CreatedAt); err != nil {
			return err
		}
		if det.Valid {
			a.Details = json.RawMessage(det.String)
		}
		inv.AuditLog = append(inv.AuditLog, a)
	}
	return nil
}

func recomputeInvoiceTotalsTx(tx *sql.Tx, id int64) error {
	rows, err := tx.Query(
		`SELECT amount_cents, tax_rate_bps FROM invoice_line_items WHERE invoice_id = ?`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	var subtotal, tax int64
	for rows.Next() {
		var amount int64
		var bps int
		if err := rows.Scan(&amount, &bps); err != nil {
			return err
		}
		subtotal += amount
		tax += amount * int64(bps) / 10000
	}
	total := subtotal + tax
	_, err = tx.Exec(
		`UPDATE invoices
		 SET subtotal_cents = ?, tax_cents = ?, total_cents = ?,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, subtotal, tax, total, id)
	return err
}

// ── Payments ──

type paymentFilters struct {
	customerID, invoiceID int64
	method                string
	since, until          string
	limit                 int
}

func dbPaymentList(db *sql.DB, pid string, f paymentFilters) ([]*Payment, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.customerID != 0 {
		where = append(where, "customer_id = ?")
		args = append(args, f.customerID)
	}
	if f.invoiceID != 0 {
		where = append(where, "invoice_id = ?")
		args = append(args, f.invoiceID)
	}
	if f.method != "" {
		where = append(where, "method = ?")
		args = append(args, strings.ToLower(f.method))
	}
	if f.since != "" {
		where = append(where, "received_at >= ?")
		args = append(args, f.since)
	}
	if f.until != "" {
		where = append(where, "received_at < ?")
		args = append(args, f.until)
	}
	limit := f.limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, project_id, invoice_id, customer_id, amount_cents, currency,
		        method, external_id, received_at, notes, created_at
		 FROM payments
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY received_at DESC, id DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Payment
	for rows.Next() {
		var p Payment
		var iid sql.NullInt64
		var ext, notes sql.NullString
		if err := rows.Scan(&p.ID, &p.ProjectID, &iid, &p.CustomerID, &p.AmountCents,
			&p.Currency, &p.Method, &ext, &p.ReceivedAt, &notes, &p.CreatedAt); err != nil {
			return nil, err
		}
		if iid.Valid {
			v := iid.Int64
			p.InvoiceID = &v
		}
		p.ExternalID = ext.String
		p.Notes = notes.String
		out = append(out, &p)
	}
	return out, rows.Err()
}

// dbPaymentRecord inserts a non-Stripe payment and rolls forward the
// invoice's amount_paid_cents + status. Single Tx so the invoice
// state can never disagree with its payments.
func dbPaymentRecord(db *sql.DB, pid string, invID int64, amount int64,
	method, receivedAt, notes, actor string) (*Payment, *Invoice, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var (
		status, currency string
		cid              int64
		total, paid      int64
	)
	if err := tx.QueryRow(
		`SELECT customer_id, status, currency, total_cents, amount_paid_cents
		 FROM invoices
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		invID, pid).Scan(&cid, &status, &currency, &total, &paid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, fmt.Errorf("invoice %d not found", invID)
		}
		return nil, nil, err
	}
	if status != "open" && status != "uncollectible" {
		return nil, nil, fmt.Errorf("cannot record payment on %s invoice — only 'open' or 'uncollectible' accept payments", status)
	}
	res, err := tx.Exec(
		`INSERT INTO payments (project_id, invoice_id, customer_id, amount_cents,
		                      currency, method, received_at, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, invID, cid, amount, currency, method, receivedAt, nullStr(notes))
	if err != nil {
		return nil, nil, err
	}
	pid64, _ := res.LastInsertId()
	newPaid := paid + amount
	newStatus := status
	action := "partial_payment"
	if newPaid >= total && total > 0 {
		newStatus = "paid"
		action = "paid"
	}
	if _, err := tx.Exec(
		`UPDATE invoices
		 SET amount_paid_cents = ?,
		     status = ?,
		     paid_at = CASE WHEN ? = 'paid' AND paid_at IS NULL THEN CURRENT_TIMESTAMP ELSE paid_at END,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, newPaid, newStatus, newStatus, invID); err != nil {
		return nil, nil, err
	}
	if err := writeAuditTx(tx, invID, actor, action, map[string]any{
		"payment_id":   pid64,
		"amount_cents": amount,
		"method":       method,
		"new_paid":     newPaid,
		"total":        total,
	}); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	pays, err := dbPaymentList(db, pid, paymentFilters{invoiceID: invID, limit: 1})
	if err != nil {
		return nil, nil, err
	}
	var pay *Payment
	for _, p := range pays {
		if p.ID == pid64 {
			pay = p
			break
		}
	}
	inv, err := dbInvoiceGetByID(db, pid, invID)
	if err != nil || inv == nil {
		return pay, inv, err
	}
	if err := loadInvoiceChildren(db, pid, inv); err != nil {
		return pay, inv, err
	}
	return pay, inv, nil
}

// ─── Audit log ──────────────────────────────────────────────────────

func writeAuditTx(tx *sql.Tx, invoiceID int64, actor, action string, details map[string]any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		raw = []byte("{}")
	}
	if actor == "" {
		actor = "system"
	}
	_, err = tx.Exec(
		`INSERT INTO invoice_audit_log (invoice_id, actor, action, details)
		 VALUES (?, ?, ?, ?)`, invoiceID, actor, action, string(raw))
	return err
}

// ─── Invoice number minting ─────────────────────────────────────────

// mintInvoiceNumberTx renders the format string with project-scoped
// {seq} resolved to count-of-invoices-this-year + 1. Year tokens use
// UTC. The unique partial index on (project_id, number) catches the
// rare race; callers retry.
func mintInvoiceNumberTx(tx *sql.Tx, pid, format string) (string, error) {
	if strings.TrimSpace(format) == "" {
		format = "INV-{yyyy}-{seq:04}"
	}
	now := time.Now().UTC()
	yyyy := fmt.Sprintf("%04d", now.Year())
	yy := fmt.Sprintf("%02d", now.Year()%100)
	mm := fmt.Sprintf("%02d", int(now.Month()))
	dd := fmt.Sprintf("%02d", now.Day())

	// Project-scoped sequence: count invoices already created this year
	// for this project + 1. Includes drafts (which can be discarded
	// without burning a number — drafts have NULL `number`). Counts
	// rows that already have a number too, so re-finalizes never
	// overlap.
	yearStart := fmt.Sprintf("%04d-01-01T00:00:00Z", now.Year())
	yearEnd := fmt.Sprintf("%04d-01-01T00:00:00Z", now.Year()+1)
	var seq int64
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM invoices
		 WHERE project_id = ?
		   AND number IS NOT NULL
		   AND finalized_at >= ? AND finalized_at < ?`,
		pid, yearStart, yearEnd).Scan(&seq); err != nil {
		return "", err
	}
	seq++

	out := format
	out = strings.ReplaceAll(out, "{yyyy}", yyyy)
	out = strings.ReplaceAll(out, "{yy}", yy)
	out = strings.ReplaceAll(out, "{mm}", mm)
	out = strings.ReplaceAll(out, "{dd}", dd)

	// {seq[:NN]} → optional zero-pad width.
	out = renderSeqToken(out, seq)
	return out, nil
}

// renderSeqToken handles {seq} and {seq:NN} (zero-pad to NN chars).
func renderSeqToken(in string, seq int64) string {
	for {
		i := strings.Index(in, "{seq")
		if i < 0 {
			break
		}
		j := strings.Index(in[i:], "}")
		if j < 0 {
			break
		}
		token := in[i : i+j+1]
		body := token[4 : len(token)-1] // after "{seq", before "}"
		var rendered string
		switch {
		case body == "":
			rendered = strconv.FormatInt(seq, 10)
		case strings.HasPrefix(body, ":"):
			width, err := strconv.Atoi(body[1:])
			if err != nil || width <= 0 {
				rendered = strconv.FormatInt(seq, 10)
			} else {
				rendered = fmt.Sprintf("%0*d", width, seq)
			}
		default:
			rendered = strconv.FormatInt(seq, 10)
		}
		in = in[:i] + rendered + in[i+j+1:]
	}
	return in
}

// ─── Validation + normalisation ─────────────────────────────────────

func computeTotals(items []LineItem) (subtotal, tax, total int64) {
	for _, li := range items {
		subtotal += li.AmountCents
		// Per-line round so total stays consistent with what the panel
		// shows next to each line.
		tax += li.AmountCents * int64(li.TaxRateBps) / 10000
	}
	total = subtotal + tax
	return
}

func normaliseLineItems(raw []any, defaultBps int) ([]LineItem, error) {
	out := make([]LineItem, 0, len(raw))
	for i, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("line_items[%d] is not an object", i)
		}
		desc := strArg(m, "description")
		unit := int64Arg(m, "unit_price_cents")
		qty := float64Arg(m, "quantity", 1)
		bps := intArg(m, "tax_rate_bps", defaultBps)
		if desc == "" {
			return nil, fmt.Errorf("line_items[%d].description required", i)
		}
		if qty <= 0 {
			return nil, fmt.Errorf("line_items[%d].quantity must be > 0", i)
		}
		if bps < 0 || bps > 100000 {
			return nil, fmt.Errorf("line_items[%d].tax_rate_bps out of range", i)
		}
		li := LineItem{
			Position:       i,
			Description:    desc,
			Quantity:       qty,
			UnitPriceCents: unit,
			AmountCents:    roundCents(qty * float64(unit)),
			TaxRateBps:     bps,
		}
		if md, ok := m["metadata"].(map[string]any); ok {
			if raw, err := json.Marshal(md); err == nil {
				li.Metadata = raw
			}
		}
		out = append(out, li)
	}
	return out, nil
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

// normaliseEmail lowercases + trims. We do NOT strip +suffix here —
// alice+work@x and alice@x are different rows on purpose. The CRM app
// has a config to opt into stripping; billing doesn't need it, since
// billing emails are usually authoritative.
func normaliseEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func roundCents(f float64) int64 {
	if f >= 0 {
		return int64(f + 0.5)
	}
	return int64(f - 0.5)
}

// ─── Scan helpers ───────────────────────────────────────────────────

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCustomer(s rowScanner) (*Customer, error) {
	var c Customer
	var email, phone, currency, ext sql.NullString
	var addr, taxes, meta sql.NullString
	if err := s.Scan(
		&c.ID, &c.ProjectID, &c.Name, &email, &phone,
		&addr, &taxes, &currency, &ext, &meta,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Email = email.String
	c.Phone = phone.String
	c.Currency = currency.String
	c.ExternalID = ext.String
	if addr.Valid {
		c.BillingAddress = json.RawMessage(addr.String)
	}
	if taxes.Valid {
		c.TaxIDs = json.RawMessage(taxes.String)
	}
	if meta.Valid {
		c.Metadata = json.RawMessage(meta.String)
	}
	return &c, nil
}

func scanInvoice(s rowScanner) (*Invoice, error) {
	var inv Invoice
	var number, dueDate, notes sql.NullString
	var ext, extURL, syncedAt sql.NullString
	var meta sql.NullString
	var finalizedAt, paidAt, voidedAt sql.NullString
	if err := s.Scan(
		&inv.ID, &inv.ProjectID, &inv.CustomerID, &inv.Provider, &number,
		&inv.Status, &inv.Currency, &inv.SubtotalCents, &inv.TaxCents,
		&inv.TotalCents, &inv.AmountPaidCents,
		&dueDate, &notes, &ext, &extURL, &syncedAt, &meta,
		&finalizedAt, &paidAt, &voidedAt, &inv.CreatedAt, &inv.UpdatedAt); err != nil {
		return nil, err
	}
	inv.Number = number.String
	inv.DueDate = dueDate.String
	inv.Notes = notes.String
	inv.ExternalID = ext.String
	inv.ExternalURL = extURL.String
	inv.LastSyncedAt = syncedAt.String
	inv.FinalizedAt = finalizedAt.String
	inv.PaidAt = paidAt.String
	inv.VoidedAt = voidedAt.String
	if meta.Valid {
		inv.Metadata = json.RawMessage(meta.String)
	}
	return &inv, nil
}

// ─── Tiny utils ─────────────────────────────────────────────────────

func intArg(args map[string]any, key string, def int) int {
	if args == nil {
		return def
	}
	switch v := args[key].(type) {
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

func int64Arg(args map[string]any, key string) int64 {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

func float64Arg(args map[string]any, key string, def float64) float64 {
	if args == nil {
		return def
	}
	switch v := args[key].(type) {
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

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
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

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func ifThen[T any](cond bool, t, f T) T {
	if cond {
		return t
	}
	return f
}

// jsonOrEmpty serialises v (which can be json.RawMessage, a Go map, or
// already-encoded JSON) into a TEXT column, falling back to a sentinel
// when v is nil. Used for billing_address / tax_ids / metadata where
// the column is NOT NULL with a default.
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

// ─── Path helpers ───────────────────────────────────────────────────

// pathInt parses /<prefix>/<id> and returns id (0 on parse failure).
func pathInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	id, _ := strconv.ParseInt(rest, 10, 64)
	return id
}

// pathIntSegment parses /<prefix>/<seg0>/<seg1>/... and returns the
// nth segment as int64.
func pathIntSegment(path, prefix string, n int) int64 {
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if n >= len(parts) {
		return 0
	}
	id, _ := strconv.ParseInt(parts[n], 10, 64)
	return id
}

// ─── HTTP utilities ─────────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// getAppCtx returns the AppCtx stashed at OnMount. The SDK doesn't
// expose a request-scoped accessor; mirroring the crm pattern.
func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

var globalCtx *sdk.AppCtx

// ─── Config helpers ─────────────────────────────────────────────────

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil || ctx.Config() == nil {
		return def
	}
	if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
		return v
	}
	return def
}

func configIntBps(ctx *sdk.AppCtx, key string) int {
	if ctx == nil || ctx.Config() == nil {
		return 0
	}
	v := strings.TrimSpace(ctx.Config().Get(key))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 100000 {
		return 0
	}
	return n
}
