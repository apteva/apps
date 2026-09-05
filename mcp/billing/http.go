package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) HTTPRoutes() []sdk.Route {
	routes := []sdk.Route{
		{Pattern: "/customers", Handler: a.handleHTTPCustomersCollection},
		{Pattern: "/customers/", Handler: a.handleHTTPCustomerItem},
		{Pattern: "/invoices", Handler: a.handleHTTPInvoicesCollection},
		{Pattern: "/invoices/", Handler: a.handleHTTPInvoiceItem},
		{Pattern: "/payments", Handler: a.handleHTTPPaymentsCollection},
		{Pattern: "/payment-methods", Handler: a.handleHTTPPaymentMethodsCollection},
		{Pattern: "/payment-methods/", Handler: a.handleHTTPPaymentMethodItem},
		{Pattern: "/setup-sessions", Handler: a.handleHTTPSetupSessionsCollection},
		{Pattern: "/issuer", Handler: a.handleHTTPIssuer},
		{Pattern: "/defaults", Handler: func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				httpErr(w, 405, "method not allowed")
				return
			}
			ctx := getAppCtx(r)
			httpJSON(w, map[string]any{"currency": configString(ctx, "default_currency", "USD"), "tax_rate_bps": configInt64(ctx, "tax_default_rate_bps", 0)})
		}},
		// Stripe webhook receiver. The platform automatically registers
		// this URL and verifies events through the bound connection.
		{Pattern: "/webhooks/stripe", Handler: a.handleStripeWebhook},
	}
	for i := range routes {
		routes[i].Handler = validatedHTTP(routes[i].Handler)
	}
	return routes
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
		case "history":
			if r.Method == http.MethodGet {
				a.handleHTTPInvoiceHistory(w, r)
				return
			}
		case "cancel-payment", "resume-collection":
			if r.Method == http.MethodPost {
				a.handleHTTPLifecycle(w, r, parts[1])
				return
			}
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
	case http.MethodPatch:
		a.handleHTTPInvoiceUpdate(w, r)
	case http.MethodDelete:
		a.handleHTTPLifecycle(w, r, "delete")
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
	inv, cust, issuer, err := loadIssuedInvoiceForRender(ctx.AppReadDB(), pid, id)
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
	_, _ = w.Write([]byte(renderInvoiceHTML(inv, cust, issuer)))
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
	inv, cust, issuer, err := loadIssuedInvoiceForRender(ctx.AppReadDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if inv == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	pdfBytes, err := renderInvoicePDF(inv, cust, issuer)
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
	rows, err := dbCustomerSearch(ctx.AppReadDB(), pid, q, email, limit+1, int(atoi64(r.URL.Query().Get("offset"))))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	httpJSON(w, map[string]any{"customers": rows, "count": len(rows), "has_more": more})
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
	c, err := dbCustomerGetByID(ctx.AppReadDB(), pid, id)
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
	c, err := dbCustomerGetByID(ctx.AppReadDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	openInvs, err := dbInvoiceSearch(ctx.AppReadDB(), pid, invoiceFilters{
		customerID: id, status: "open", limit: 50,
	})
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	pays, err := dbPaymentList(ctx.AppReadDB(), pid, paymentFilters{customerID: id, limit: 10})
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	totals, err := dbCustomerTotals(ctx.AppReadDB(), pid, id)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
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
		ctx.EmitWithProject("customer.deleted", pid, map[string]any{"id": id})
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
	out, err := dbInvoiceSearch(ctx.AppReadDB(), pid, invoiceFilters{q: q.Get("q"), offset: int(atoi64(q.Get("offset"))),
		customerID: cid,
		status:     q.Get("status"),
		provider:   q.Get("provider"),
		currency:   q.Get("currency"),
		since:      q.Get("since"),
		until:      q.Get("until"),
		sort:       q.Get("sort"),
		limit:      limit + 1,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	httpJSON(w, map[string]any{"invoices": out, "count": len(out), "has_more": more})
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
	rawItems, _ := body["line_items"].([]any)
	catalogCurrency, err := resolveCatalogRefs(ctx, rawItems)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	currency, err := invoiceCurrency(ctx, pid, cid, strArg(body, "currency"), catalogCurrency)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if currency == "" && catalogCurrency != "" {
		currency = strings.ToUpper(catalogCurrency)
	}
	if currency == "" {
		currency = strings.ToUpper(configString(ctx, "default_currency", "USD"))
	}
	if !looksLikeISO4217(currency) {
		httpErr(w, http.StatusBadRequest, "currency must be a supported 2-decimal ISO 4217 code")
		return
	}
	items, err := normaliseLineItems(rawItems, configIntBps(ctx, "tax_default_rate_bps"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	accountingDate := strings.TrimSpace(strArg(body, "accounting_date"))
	if err := validateAccountingDate(accountingDate); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	inv := &Invoice{
		ProjectID:      pid,
		CustomerID:     cid,
		Provider:       provider,
		Status:         "draft",
		Currency:       currency,
		AccountingDate: accountingDate,
		DueDate:        strArg(body, "due_date"),
		Notes:          strArg(body, "notes"),
		LineItems:      items,
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
	inv, err := dbInvoiceGetByID(ctx.AppReadDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if inv == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := loadInvoiceChildren(ctx.AppReadDB(), pid, inv); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"invoice": inv})
}

func (a *App) handleHTTPInvoiceUpdate(w http.ResponseWriter, r *http.Request) {
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
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if rawItems, ok := patch["line_items"].([]any); ok {
		if _, err := resolveCatalogRefs(ctx, rawItems); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	inv, err := dbInvoiceUpdate(ctx.AppDB(), pid, id, patch, actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitInvoice(ctx, "invoice.updated", inv)
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
		configInt64(ctx, "invoice_seq_start", 1001),
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
	if _, err := resolveCatalogRefs(ctx, []any{body}); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := normaliseLineItems([]any{body}, configIntBps(ctx, "tax_default_rate_bps"))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	li := items[0]
	if err := validateCatalogCurrency(ctx.AppDB(), pid, id, []any{body}); err != nil {
		httpErr(w, 400, err.Error())
		return
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
	out, err := dbPaymentList(ctx.AppReadDB(), pid, paymentFilters{offset: int(atoi64(r.URL.Query().Get("offset"))), invoiceID: id, limit: 100})
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
	out, err := dbPaymentList(ctx.AppReadDB(), pid, paymentFilters{offset: int(atoi64(r.URL.Query().Get("offset"))),
		customerID: cid, invoiceID: iid,
		method: q.Get("method"),
		since:  q.Get("since"), until: q.Get("until"),
		limit: limit + 1,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	httpJSON(w, map[string]any{"payments": out, "count": len(out), "has_more": more})
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
	switch method {
	case "wire", "cash", "check", "other", "stripe":
	default:
		httpErr(w, http.StatusBadRequest, "unknown method")
		return
	}
	received := strArg(body, "received_at")
	if received == "" {
		received = time.Now().UTC().Format(time.RFC3339)
	}
	if err := validateReceivedAt(received); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	pay, inv, err := dbPaymentRecord(ctx.AppDB(), pid, id, amount, method,
		strArg(body, "external_id"), received,
		strArg(body, "notes"), actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitInvoicePaid(ctx, inv, pay)
	httpJSON(w, map[string]any{"payment": pay, "invoice": inv})
}
