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
	_ "embed"

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

//go:embed apteva.yaml
var manifestYAML string

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
	if err := backfillInvoiceSnapshots(ctx.AppDB()); err != nil {
		return err
	}
	// Stash the ctx so HTTP handlers — which the SDK invokes without
	// passing AppCtx — can reach it. (Same pattern as crm.)
	globalCtx = ctx

	if bound := ctx.IntegrationFor("payment_processor"); bound != nil {
		ctx.Logger().Info("billing: payment_processor integration bound — on-demand Stripe payment links enabled")
		if err := ensureStripeWebhook(ctx, bound); err != nil {
			ctx.Logger().Warn("billing: platform-managed Stripe webhook reconciliation pending", "err", err.Error())
		}
	} else {
		ctx.Logger().Info("billing: no payment_processor bound — manual-payment mode only")
	}

	ctx.Logger().Info("billing mounted",
		"version", "0.12.5",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return billingWorkers(a) }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ────────────────────────────────────────────────────
//
// Reverse-proxied at /api/apps/billing/* by apteva-server. The
// dashboard passes ?project_id=<id> (when scope=global) and
// ?install_id=<id> on every URL.

// ─── MCP tools ──────────────────────────────────────────────────────

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
		return "agent:" + strings.TrimPrefix(v, "agent:")
	}
	return "system"
}

func actorFromRequest(r *http.Request) string {
	if v := r.Header.Get("X-Actor"); v != "" {
		return "human:" + strings.TrimPrefix(v, "human:")
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
	TaxTreatment   string `json:"tax_treatment"`
	CollectionHold bool   `json:"collection_hold"`
	CreditCents    int64  `json:"credit_cents"`
	ID             int64  `json:"id"`
	ProjectID      string `json:"project_id,omitempty"`
	CustomerID     int64  `json:"customer_id"`
	// Customer fields denormalised by the LEFT JOIN in dbInvoiceSearch /
	// dbInvoiceGetByID — the panel renders these in the sidebar list so
	// users don't have to fetch every customer separately. Empty when
	// the customer row has been soft-deleted out from under the invoice.
	CustomerName    string          `json:"customer_name,omitempty"`
	CustomerEmail   string          `json:"customer_email,omitempty"`
	Provider        string          `json:"provider"`
	Number          string          `json:"number,omitempty"`
	Status          string          `json:"status"`
	Currency        string          `json:"currency"`
	SubtotalCents   int64           `json:"subtotal_cents"`
	TaxCents        int64           `json:"tax_cents"`
	TotalCents      int64           `json:"total_cents"`
	AmountPaidCents int64           `json:"amount_paid_cents"`
	AccountingDate  string          `json:"accounting_date,omitempty"`
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
	ID             int64   `json:"id,omitempty"`
	InvoiceID      int64   `json:"invoice_id,omitempty"`
	Position       int     `json:"position"`
	Description    string  `json:"description"`
	Quantity       float64 `json:"quantity"`
	UnitPriceCents int64   `json:"unit_price_cents"`
	AmountCents    int64   `json:"amount_cents"`
	TaxRateBps     int     `json:"tax_rate_bps"`
	ExternalID     string  `json:"external_id,omitempty"`
	// Optional cross-app FKs into the catalog app — populated when the
	// caller passes price_id at create time, and snapshot fields above
	// (description, unit_price_cents) are filled from catalog. NULL on
	// free-form/ad-hoc lines (one-off custom work, refunds, manual
	// adjustments). Used for analytics ("revenue by product") not for
	// PDF rendering — the snapshot fields are what the customer sees.
	PriceID   *int64          `json:"price_id,omitempty"`
	ProductID *int64          `json:"product_id,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
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

// ─── Event emission ─────────────────────────────────────────────────

func emitCustomer(ctx *sdk.AppCtx, topic string, c *Customer) {
	if ctx == nil || c == nil {
		return
	}
	ctx.EmitWithProject(topic, c.ProjectID, map[string]any{
		"id":    c.ID,
		"name":  c.Name,
		"email": c.Email,
	})
}

func emitInvoice(ctx *sdk.AppCtx, topic string, inv *Invoice) {
	if ctx == nil || inv == nil {
		return
	}
	if topic == "invoice.finalized" || topic == "invoice.voided" || topic == "invoice.refunded" {
		_ = flushOutbox(ctx)
		return
	}
	payload := map[string]any{
		"id":          inv.ID,
		"customer_id": inv.CustomerID,
		"number":      inv.Number,
		"status":      inv.Status,
		"total_cents": inv.TotalCents,
		"currency":    inv.Currency,
	}
	// Billing stays product-agnostic: source apps stamp linkage (e.g.
	// subscription_id, cycle_id, source) into invoice metadata at create
	// time, and consumers read it off the event — this is what lets a
	// revenue listener tell a subscription-cycle invoice from a one-off
	// without a read-time join.
	if meta := string(inv.Metadata); meta != "" && meta != "{}" && meta != "null" {
		payload["metadata"] = inv.Metadata
	}
	ctx.EmitWithProject(topic, inv.ProjectID, payload)
}

func emitInvoicePaid(ctx *sdk.AppCtx, inv *Invoice, payment *Payment) {
	if ctx != nil {
		_ = flushOutbox(ctx)
	}
}

// ─── HTTP handlers ──────────────────────────────────────────────────

// ─── DB layer ───────────────────────────────────────────────────────

// ── Customers ──

func lookupCustomer(db *sql.DB, pid string, args map[string]any) (*Customer, error) {
	if id := int64Arg(args, "id"); id != 0 {
		return dbCustomerGetByID(db, pid, id)
	}
	if email := normaliseEmail(strArg(args, "email")); email != "" {
		return dbCustomerGetByEmail(db, pid, email)
	}
	return nil, errors.New("id or email required")
}

// ── Invoices ──

type invoiceFilters struct {
	q                            string
	offset                       int
	customerID                   int64
	status, provider, currency   string
	since, until                 string
	sort                         string
	minTotalCents, maxTotalCents int64
	limit                        int
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

// toString coerces common JSON-decoded types to a string. nil → "".
func toString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case fmt.Stringer:
		return s.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// ── Payments ──

type paymentFilters struct {
	offset                int
	customerID, invoiceID int64
	method                string
	since, until          string
	limit                 int
}

// ─── Audit log ──────────────────────────────────────────────────────

// ─── Invoice number minting ─────────────────────────────────────────

// mintInvoiceNumberTx renders the format string with project-scoped
// {seq} resolved to (count-of-numbered-invoices-this-year + seqStart).
// Year tokens use UTC. The unique partial index on (project_id, number)
// catches the rare race; callers retry.
//
// seqStart defaults to 1001 so the first invoice of the year looks
// like INV-2026-1001 rather than INV-2026-0001 — the "0001" pattern
// is the universal tell of "first invoice this project ever sent."
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
			if err != nil || width <= 0 || width > 12 {
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
		tax += taxForAmount(li.AmountCents, li.TaxRateBps)
	}
	total = subtotal + tax
	return
}

func taxForAmount(amount int64, bps int) int64 {
	// Round each line to the nearest minor unit, half away from zero.
	n := amount * int64(bps)
	if n >= 0 {
		return (n + 5000) / 10000
	}
	return (n - 5000) / 10000
}

func normaliseLineItems(raw []any, defaultBps int) ([]LineItem, error) {
	if defaultBps < 0 || defaultBps > 100000 {
		return nil, errors.New("default tax rate out of range")
	}
	if len(raw) > 1000 {
		return nil, errors.New("at most 1000 line items allowed")
	}
	out := make([]LineItem, 0, len(raw))
	for i, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("line_items[%d] is not an object", i)
		}
		if err := validateLineInput(m); err != nil {
			return nil, fmt.Errorf("line_items[%d]: %w", i, err)
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
		// Optional catalog FKs — populated by resolveCatalogRefs before
		// this function runs. Stored as analytics-only references; the
		// snapshot fields above (description, unit_price_cents) are
		// what the customer-facing PDF renders from.
		if v := int64Arg(m, "price_id"); v != 0 {
			li.PriceID = &v
		}
		if v := int64Arg(m, "product_id"); v != 0 {
			li.ProductID = &v
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

// resolveCatalogRefs walks the raw line items and, for any that carry
// a `price_id`, calls the catalog app to fetch the price + product so
// we can snapshot description/unit_price_cents/currency/product_id into
// the line. Mutates the input maps in place: the caller-provided
// description/unit_price_cents WIN if set, so callers can override.
//
// Catalog is an optional dep — when it isn't installed, this returns
// a clear error pointing at the install gap. Free-form line items
// (no price_id) pass through untouched, so existing flows keep
// working without catalog.
func resolveCatalogRefs(ctx *sdk.AppCtx, raw []any) (string, error) {
	if ctx == nil {
		return "", nil
	}
	currencyHint := ""
	cache := map[string]map[string]any{}
	api := ctx.PlatformAPI()
	for i, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		priceID := int64Arg(m, "price_id")
		if priceID == 0 {
			continue
		}
		if api == nil {
			return "", errors.New("price_id requires platform API (catalog app must be installed)")
		}
		var priceResp map[string]any
		if err := cachedCatalogCall(api, cache, "catalog_prices_get", priceID, &priceResp); err != nil {
			return "", fmt.Errorf("line_items[%d]: catalog price %d lookup failed (is the catalog app installed?): %w", i, priceID, err)
		}
		price := mapFromAny(priceResp["price"])
		if len(price) == 0 {
			price = priceResp
		}
		productID := int64Arg(price, "product_id")
		if strArg(price, "archived_at") != "" {
			return "", fmt.Errorf("line_items[%d]: catalog price %d is archived; pick a different price", i, priceID)
		}
		product := map[string]any{}
		if productID != 0 {
			var productResp map[string]any
			if err := cachedCatalogCall(api, cache, "catalog_products_get", productID, &productResp); err == nil {
				product = mapFromAny(productResp["product"])
				if len(product) == 0 {
					product = productResp
				}
			}
		}
		// Fill missing fields from the snapshot. Caller's explicit
		// values always win (lets you override description per invoice).
		if strArg(m, "description") == "" {
			desc := strArg(price, "nickname")
			if desc == "" {
				// Fall back to product name when the price has no nickname.
				if name := strArg(product, "name"); name != "" {
					desc = name
				} else {
					desc = fmt.Sprintf("Product #%d", productID)
				}
			}
			m["description"] = desc
		}
		if _, present := m["unit_price_cents"]; !present {
			m["unit_price_cents"] = int64Arg(price, "unit_amount_cents")
		}
		// product_id is always taken from the catalog (caller can't override).
		m["product_id"] = productID
		m["metadata"] = mergeMaps(
			mapFromAny(product["metadata"]),
			mapFromAny(price["metadata"]),
			mapFromAny(m["metadata"]),
			map[string]any{
				"catalog_product_id": productID,
				"catalog_price_id":   priceID,
			},
		)
		// Currency hint — first line's catalog currency wins as the
		// invoice's currency if the caller didn't set one explicitly.
		cur := strings.ToUpper(strArg(price, "currency"))
		if !supportedCurrency(cur) {
			return "", fmt.Errorf("catalog price %d has unsupported currency", priceID)
		}
		if currencyHint != "" && currencyHint != cur {
			return "", errors.New("catalog prices must use one currency")
		}
		currencyHint = cur
		m["_catalog_currency"] = cur
	}
	return currencyHint, nil
}

func looksLikeISO4217(s string) bool { return supportedCurrency(s) }

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

func boolFromArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

func mapFromAny(v any) map[string]any {
	out := map[string]any{}
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			out[k] = vv
		}
	case map[string]string:
		for k, vv := range x {
			out[k] = vv
		}
	case json.RawMessage:
		_ = json.Unmarshal(x, &out)
	case []byte:
		_ = json.Unmarshal(x, &out)
	case string:
		if strings.TrimSpace(x) != "" {
			_ = json.Unmarshal([]byte(x), &out)
		}
	}
	return out
}

func mergeMaps(base map[string]any, overlays ...map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for _, overlay := range overlays {
		for k, v := range overlay {
			out[k] = v
		}
	}
	return out
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

func nullInt(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func validateAccountingDate(value string) error {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return errors.New("accounting_date must be a valid date in YYYY-MM-DD format")
	}
	return nil
}

func validateReceivedAt(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("received_at is required")
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return errors.New("received_at must be a valid RFC3339 timestamp")
	}
	return nil
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
	// no-store: these are dynamic, per-request reads. Without this the
	// browser serves a cached list/detail after a mutation, so the
	// panel shows stale data until a hard reload.
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// getAppCtx returns the AppCtx stashed at OnMount. The SDK doesn't
// expose a request-scoped accessor; mirroring the crm pattern.
func getAppCtx(r *http.Request) *sdk.AppCtx {
	if globalCtx == nil {
		return nil
	}
	pid, _ := resolveProjectFromRequest(r)
	return globalCtx.WithProject(pid)
}

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

// configInt64 reads a non-negative int64 config value with a default
// fallback. Used by the invoice-number minter to read seq_start.
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

// ─── Issuer settings (singleton) ────────────────────────────────────
//
// The entity that emits invoices — your own business identity, rendered
// as the "BILL FROM" block on PDFs and the bank-transfer instructions
// in the footer. One row total per install; the CHECK(id=1) constraint
// plus the INSERT…ON CONFLICT shape below makes that physical.

type Issuer struct {
	DisplayName  string          `json:"display_name"`
	LegalName    string          `json:"legal_name,omitempty"`
	Email        string          `json:"email,omitempty"`
	Phone        string          `json:"phone,omitempty"`
	Website      string          `json:"website,omitempty"`
	BrandColor   string          `json:"brand_color,omitempty"`
	Address      json.RawMessage `json:"address,omitempty"`
	TaxIDs       json.RawMessage `json:"tax_ids,omitempty"`
	Bank         json.RawMessage `json:"bank,omitempty"`
	FooterText   string          `json:"footer_text,omitempty"`
	DefaultTerms string          `json:"default_terms,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	CreatedAt    string          `json:"created_at,omitempty"`
	UpdatedAt    string          `json:"updated_at,omitempty"`
	// Configured is true once a row exists; consumers (PDF, panel)
	// fall back to a placeholder when false.
	Configured bool `json:"configured"`
}
