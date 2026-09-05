package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

func (a *App) toolCustomersSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := dbCustomerSearch(ctx.AppReadDB(), pid,
		strArg(args, "q"), strArg(args, "email"), limit+1, intArg(args, "offset", 0))
	if err != nil {
		return nil, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	return map[string]any{"customers": rows, "count": len(rows), "has_more": more}, nil
}

func (a *App) toolCustomersGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c, err := lookupCustomer(ctx.AppReadDB(), pid, args)
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
	c, err := lookupCustomer(ctx.AppReadDB(), pid, args)
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
	openInvs, err := dbInvoiceSearch(ctx.AppReadDB(), pid, invoiceFilters{
		customerID: c.ID, status: "open", limit: 50,
	})
	if err != nil {
		return nil, err
	}
	pays, err := dbPaymentList(ctx.AppReadDB(), pid, paymentFilters{
		customerID: c.ID, limit: plimit,
	})
	if err != nil {
		return nil, err
	}
	totals, err := dbCustomerTotals(ctx.AppReadDB(), pid, c.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"customer":        c,
		"open_invoices":   openInvs,
		"recent_payments": pays,
		"lifetime":        totals,
		"found":           true,
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
		ctx.EmitWithProject("customer.merged", pid, map[string]any{
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
	rawItems, _ := args["line_items"].([]any)
	// Resolve any catalog price_id references first — this lookup
	// fills in description / unit_price_cents on those lines and
	// surfaces the catalog's currency if the caller didn't pin one.
	catalogCurrency, err := resolveCatalogRefs(ctx, rawItems)
	if err != nil {
		return nil, err
	}
	currency, err := invoiceCurrency(ctx, pid, cid, strArg(args, "currency"), catalogCurrency)
	if err != nil {
		return nil, err
	}
	if currency == "" && catalogCurrency != "" {
		currency = strings.ToUpper(catalogCurrency)
	}
	if currency == "" {
		currency = strings.ToUpper(configString(ctx, "default_currency", "USD"))
	}
	if !looksLikeISO4217(currency) {
		return nil, fmt.Errorf("currency %q must be a supported 2-decimal ISO 4217 code", currency)
	}

	items, err := normaliseLineItems(rawItems, configIntBps(ctx, "tax_default_rate_bps"))
	if err != nil {
		return nil, err
	}

	accountingDate := strings.TrimSpace(strArg(args, "accounting_date"))
	if err := validateAccountingDate(accountingDate); err != nil {
		return nil, err
	}
	inv := &Invoice{
		ProjectID:      pid,
		CustomerID:     cid,
		Provider:       provider,
		Status:         "draft",
		Currency:       currency,
		AccountingDate: accountingDate,
		DueDate:        strArg(args, "due_date"),
		Notes:          strArg(args, "notes"),
		LineItems:      items,
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

func (a *App) toolInvoicesCreateFromPreparedLines(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rawItems, _ := args["line_items"].([]any)
	if len(rawItems) == 0 {
		return nil, errors.New("line_items required")
	}
	createArgs := map[string]any{
		"_project_id":     strArg(args, "_project_id"),
		"_caller":         callerActor(args),
		"customer_id":     int64Arg(args, "customer_id"),
		"currency":        strArg(args, "currency"),
		"provider":        strArg(args, "provider"),
		"accounting_date": strArg(args, "accounting_date"),
		"due_date":        strArg(args, "due_date"),
		"notes":           strArg(args, "notes"),
		"line_items":      rawItems,
		"metadata":        args["metadata"],
	}
	out, err := a.toolInvoicesCreate(ctx, createArgs)
	if err != nil {
		return nil, err
	}
	inv := out.(map[string]any)["invoice"].(*Invoice)
	if boolFromArg(args, "finalize") {
		finalOut, err := a.toolInvoicesFinalize(ctx, map[string]any{"_project_id": strArg(args, "_project_id"), "_caller": callerActor(args), "invoice_id": inv.ID})
		if err != nil {
			return nil, err
		}
		return finalOut, nil
	}
	return out, nil
}

func (a *App) toolInvoicesAddLineItem(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invoice_id")
	if id == 0 {
		return nil, errors.New("invoice_id required")
	}
	// If price_id is set, snapshot description/unit_price_cents from
	// catalog (caller-provided values still win). resolveCatalogRefs
	// mutates the map in place — maps are reference types, so args
	// reflects the snapshot after this returns.
	if _, err := resolveCatalogRefs(ctx, []any{args}); err != nil {
		return nil, err
	}
	items, err := normaliseLineItems([]any{args}, configIntBps(ctx, "tax_default_rate_bps"))
	if err != nil {
		return nil, err
	}
	li := items[0]
	if err := validateCatalogCurrency(ctx.AppDB(), pid, id, []any{args}); err != nil {
		return nil, err
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
		configInt64(ctx, "invoice_seq_start", 1001),
		callerActor(args))
	if err != nil {
		return nil, err
	}
	emitInvoice(ctx, "invoice.finalized", inv)
	return map[string]any{"invoice": inv}, nil
}

func (a *App) toolInvoicesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
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
	// If patch.line_items references catalog prices, resolve before the
	// DB call so dbInvoiceUpdate stays catalog-agnostic.
	if rawItems, ok := patch["line_items"].([]any); ok {
		if _, err := resolveCatalogRefs(ctx, rawItems); err != nil {
			return nil, err
		}
	}
	inv, err := dbInvoiceUpdate(ctx.AppDB(), pid, id, patch, callerActor(args))
	if err != nil {
		return nil, err
	}
	emitInvoice(ctx, "invoice.updated", inv)
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
	inv, err := lookupInvoice(ctx.AppReadDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return map[string]any{"invoice": nil, "found": false}, nil
	}
	if err := loadInvoiceChildren(ctx.AppReadDB(), pid, inv); err != nil {
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
	out, err := dbInvoiceSearch(ctx.AppReadDB(), pid, invoiceFilters{q: strArg(args, "q"), offset: intArg(args, "offset", 0),
		customerID:    int64Arg(args, "customer_id"),
		status:        strArg(args, "status"),
		provider:      strArg(args, "provider"),
		currency:      strArg(args, "currency"),
		since:         strArg(args, "since"),
		until:         strArg(args, "until"),
		minTotalCents: int64Arg(args, "min_total_cents"),
		maxTotalCents: int64Arg(args, "max_total_cents"),
		sort:          strArg(args, "sort"),
		limit:         limit + 1,
	})
	if err != nil {
		return nil, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return map[string]any{"invoices": out, "count": len(out), "has_more": more}, nil
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
	switch method {
	case "wire", "cash", "check", "other":
	case "stripe":
		// v0.8.0+: Stripe payments accepted. Webhook handler is the
		// primary writer (idempotent via (method, external_id) unique
		// index); explicit MCP calls are allowed for manual recording
		// of off-platform Stripe activity. external_id is recommended
		// for any stripe payment so re-deliveries dedupe cleanly.
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
	received := strArg(args, "received_at")
	if received == "" {
		received = time.Now().UTC().Format(time.RFC3339)
	}
	if err := validateReceivedAt(received); err != nil {
		return nil, err
	}
	pay, inv, err := dbPaymentRecord(ctx.AppDB(), pid, id, amount, method,
		strArg(args, "external_id"), received,
		strArg(args, "notes"), callerActor(args))
	if err != nil {
		return nil, err
	}
	emitInvoicePaid(ctx, inv, pay) // listeners filter on status == 'paid'
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
	out, err := dbPaymentList(ctx.AppReadDB(), pid, paymentFilters{offset: intArg(args, "offset", 0),
		customerID: int64Arg(args, "customer_id"),
		invoiceID:  int64Arg(args, "invoice_id"),
		method:     strArg(args, "method"),
		since:      strArg(args, "since"),
		until:      strArg(args, "until"),
		limit:      limit + 1,
	})
	if err != nil {
		return nil, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return map[string]any{"payments": out, "count": len(out), "has_more": more}, nil
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
	inv, cust, issuer, err := loadIssuedInvoiceForRender(ctx.AppReadDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, fmt.Errorf("invoice %d not found", id)
	}
	pdfBytes, err := renderInvoicePDF(inv, cust, issuer)
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
		// App-internal default per storage's dotted-folder convention —
		// these are voucher PDFs the agent attaches to chat via
		// invoice-card; users don't browse them through the storage
		// panel directly. Caller can pass any non-dot folder to make
		// them user-visible.
		folder = "/.billing/invoices/"
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
