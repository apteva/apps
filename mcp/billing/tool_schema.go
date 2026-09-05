package main

import (
	sdk "github.com/apteva/app-sdk"
)

func (a *App) MCPTools() []sdk.Tool {
	tools := []sdk.Tool{
		{
			Name:        "payment_processor_public_config",
			Description: "Return browser-safe payment processor configuration. For Stripe this exposes only the publishable key; no credentials or secrets are returned.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolPaymentProcessorPublicConfig,
		},
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
			Name:        "invoices_create",
			Description: "Create a DRAFT invoice with optional initial line items. Provider arg ('local'|'stripe') falls back to install default. PROVIDER IS FROZEN. Line items can be either free-form ({description, quantity, unit_price_cents, tax_rate_bps?}) OR catalog references ({price_id, quantity}) — when price_id is set, billing calls the catalog app to snapshot description + unit_price_cents + currency into the line. Free-form and catalog-ref lines can mix on the same invoice. Args: customer_id, currency (catalog price wins; falls back to install default), provider, accounting_date (YYYY-MM-DD), due_date, notes, line_items, metadata.",
			InputSchema: schemaObject(map[string]any{
				"customer_id":     map[string]any{"type": "integer"},
				"currency":        map[string]any{"type": "string"},
				"provider":        map[string]any{"type": "string"},
				"accounting_date": map[string]any{"type": "string"},
				"due_date":        map[string]any{"type": "string"},
				"notes":           map[string]any{"type": "string"},
				"line_items":      map[string]any{"type": "array"},
				"metadata":        map[string]any{"type": "object"},
			}, []string{"customer_id"}),
			Handler: a.toolInvoicesCreate,
		},
		{
			Name:        "invoices_create_from_prepared_lines",
			Description: "Create a Billing invoice from generic prepared line items produced by another app, optionally finalizing it. Billing stays product-agnostic; source details live in invoice/line metadata.",
			InputSchema: schemaObject(map[string]any{
				"customer_id":     map[string]any{"type": "integer"},
				"currency":        map[string]any{"type": "string"},
				"provider":        map[string]any{"type": "string"},
				"accounting_date": map[string]any{"type": "string"},
				"due_date":        map[string]any{"type": "string"},
				"notes":           map[string]any{"type": "string"},
				"line_items":      map[string]any{"type": "array"},
				"metadata":        map[string]any{"type": "object"},
				"finalize":        map[string]any{"type": "boolean"},
			}, []string{"customer_id", "line_items"}),
			Handler: a.toolInvoicesCreateFromPreparedLines,
		},
		{
			Name:        "invoices_add_line_item",
			Description: "Append a line item to a DRAFT invoice. Errors on non-draft invoices. Free-form OR catalog-ref: pass price_id to snapshot from the catalog app, or pass description+unit_price_cents for an ad-hoc line. Args: invoice_id, price_id, description, quantity (default 1), unit_price_cents, tax_rate_bps (default install setting).",
			InputSchema: schemaObject(map[string]any{
				"invoice_id":       map[string]any{"type": "integer"},
				"price_id":         map[string]any{"type": "integer"},
				"description":      map[string]any{"type": "string"},
				"quantity":         map[string]any{"type": "number"},
				"unit_price_cents": map[string]any{"type": "integer"},
				"tax_rate_bps":     map[string]any{"type": "integer"},
				"metadata":         map[string]any{"type": "object"},
			}, []string{"invoice_id"}),
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
			Name:        "invoices_update",
			Description: "Patch an invoice. Args: id, patch (object). Always-allowed fields: notes, accounting_date (YYYY-MM-DD), due_date, metadata. Draft-only fields: customer_id, currency, line_items (full replacement). Rejects voided invoices. Recomputes totals when line_items change; writes an audit entry.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"patch": map[string]any{"type": "object"},
			}, []string{"id", "patch"}),
			Handler: a.toolInvoicesUpdate,
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
			Description: "Filter invoices. Args: customer_id, status (draft|open|paid|void|uncollectible), provider (local|stripe), currency, since (RFC3339), until (RFC3339), min_total_cents, max_total_cents, sort (due_date), limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"customer_id":     map[string]any{"type": "integer"},
				"status":          map[string]any{"type": "string"},
				"provider":        map[string]any{"type": "string"},
				"currency":        map[string]any{"type": "string"},
				"since":           map[string]any{"type": "string"},
				"until":           map[string]any{"type": "string"},
				"min_total_cents": map[string]any{"type": "integer"},
				"max_total_cents": map[string]any{"type": "integer"},
				"sort":            map[string]any{"type": "string"},
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
			Name:        "invoices_refund",
			Description: "Request a partial or full Stripe refund for a paid invoice. Billing selects and validates the original Stripe payment, records an idempotent request, calls the bound processor, and waits for the verified refund webhook to update invoice balances. Args: invoice_id, amount_cents?, reason?, idempotency_key.",
			InputSchema: schemaObject(map[string]any{
				"invoice_id":      map[string]any{"type": "integer"},
				"amount_cents":    map[string]any{"type": "integer"},
				"reason":          map[string]any{"type": "string", "enum": []string{"duplicate", "fraudulent", "requested_by_customer"}},
				"idempotency_key": map[string]any{"type": "string"},
			}, []string{"invoice_id", "idempotency_key"}),
			Handler: a.toolInvoicesRefund,
		},
		{
			Name:        "invoices_render_pdf",
			Description: "Render an invoice as a PDF. Default returns {pdf_base64, filename, size_bytes}. With save_to_storage=true, writes the PDF to the storage app (must be installed) and returns {file_id, url, filename, size_bytes} so the agent can attach it to chat / email. Args: invoice_id, save_to_storage (default false), folder (storage path, default '/invoices/').",
			InputSchema: schemaObject(map[string]any{
				"invoice_id":      map[string]any{"type": "integer"},
				"save_to_storage": map[string]any{"type": "boolean"},
				"folder":          map[string]any{"type": "string"},
			}, []string{"invoice_id"}),
			Handler: a.toolInvoicesRenderPDF,
		},
		{
			Name:        "invoices_create_payment_session",
			Description: "Create or recover a Stripe payment session for an open invoice. Product-app API: presentation='elements' returns publishable_key + client_secret; presentation='hosted' returns url. Requires a stable idempotency_key for retry-safe use. Billing owns amount/currency validation and webhook reconciliation. Args: invoice_id, presentation, idempotency_key, return_url, success_url, cancel_url, expires_at, payment_method_types, save_payment_method, set_default_payment_method.",
			InputSchema: schemaObject(map[string]any{
				"invoice_id":                 map[string]any{"type": "integer"},
				"presentation":               map[string]any{"type": "string", "enum": []string{"elements", "hosted"}},
				"idempotency_key":            map[string]any{"type": "string"},
				"return_url":                 map[string]any{"type": "string"},
				"success_url":                map[string]any{"type": "string"},
				"cancel_url":                 map[string]any{"type": "string"},
				"expires_at":                 map[string]any{"type": "integer"},
				"payment_method_types":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"save_payment_method":        map[string]any{"type": "boolean"},
				"set_default_payment_method": map[string]any{"type": "boolean"},
			}, []string{"invoice_id", "presentation", "idempotency_key"}),
			Handler: a.toolInvoicesCreatePaymentSession,
		},
		{
			Name:        "invoices_create_payment_intent",
			Description: "Create or recover a Stripe PaymentIntent for an identified customer's open invoice. Returns publishable_key + client_secret for confirmation with an already-rendered deferred Payment Element. Args: invoice_id, idempotency_key, save_payment_method, set_default_payment_method.",
			InputSchema: schemaObject(map[string]any{
				"invoice_id":                 map[string]any{"type": "integer"},
				"idempotency_key":            map[string]any{"type": "string"},
				"save_payment_method":        map[string]any{"type": "boolean"},
				"set_default_payment_method": map[string]any{"type": "boolean"},
			}, []string{"invoice_id", "idempotency_key"}),
			Handler: a.toolInvoicesCreatePaymentIntent,
		},
		{
			Name:        "invoices_send_payment_link",
			Description: "ON DEMAND ONLY: Create a Stripe-hosted Checkout payment URL for an existing open or uncollectible invoice, and only when the user explicitly asks for a Stripe or payment link. Never call this tool automatically because Stripe is connected, because an invoice was created or finalized, or because the customer has an email address. This tool does NOT send email or deliver the URL; it only returns {url, stripe_session_id, expires_at}. Sharing the returned URL requires a separate, explicitly requested channel or email action. Uses only the bound payment_processor integration; Billing never receives Stripe credentials. The URL is valid for 24h. On payment success, the platform-verified webhook records the payment and transitions the invoice to 'paid' automatically. Product apps may set save_payment_method=true to collect consent during the first checkout for later recurring charges. Args: invoice_id (required), success_url, cancel_url, save_payment_method, set_default_payment_method.",
			InputSchema: schemaObject(map[string]any{
				"invoice_id":                 map[string]any{"type": "integer"},
				"success_url":                map[string]any{"type": "string"},
				"cancel_url":                 map[string]any{"type": "string"},
				"save_payment_method":        map[string]any{"type": "boolean"},
				"set_default_payment_method": map[string]any{"type": "boolean"},
			}, []string{"invoice_id"}),
			Handler: a.toolInvoicesSendPaymentLink,
		},
		{
			Name:        "invoices_collect",
			Description: "Collect an open invoice automatically with an active reusable saved payment method. Billing owns the off-session processor call, durable attempt state, webhook reconciliation, and invoice payment record. Product apps must supply a stable idempotency_key. Args: invoice_id, idempotency_key, optional payment_method_id.",
			InputSchema: schemaObject(map[string]any{
				"invoice_id":        map[string]any{"type": "integer"},
				"idempotency_key":   map[string]any{"type": "string"},
				"payment_method_id": map[string]any{"type": "integer"},
			}, []string{"invoice_id", "idempotency_key"}),
			Handler: a.toolInvoicesCollect,
		},
		// ── Payment methods ──────────────────────────────────────────
		{
			Name:        "payment_method_setup_create",
			Description: "Create a provider-hosted setup session so a customer can save a reusable payment method. Returns {setup_session, url}. Args: customer_id, payment_method_types (default ['card']; e.g. ['card','sepa_debit']), success_url, cancel_url, set_default (default true), metadata.",
			InputSchema: schemaObject(map[string]any{
				"customer_id":          map[string]any{"type": "integer"},
				"payment_method_types": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"success_url":          map[string]any{"type": "string"},
				"cancel_url":           map[string]any{"type": "string"},
				"set_default":          map[string]any{"type": "boolean"},
				"metadata":             map[string]any{"type": "object"},
			}, []string{"customer_id"}),
			Handler: a.toolPaymentMethodSetupCreate,
		},
		{
			Name:        "payment_methods_list",
			Description: "List stored customer payment methods. Args: customer_id, status (active|detached), type (card|sepa_debit|...), limit.",
			InputSchema: schemaObject(map[string]any{
				"customer_id": map[string]any{"type": "integer"},
				"status":      map[string]any{"type": "string"},
				"type":        map[string]any{"type": "string"},
				"limit":       map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolPaymentMethodsList,
		},
		{
			Name:        "payment_method_default_set",
			Description: "Set an active stored payment method as the customer's default. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolPaymentMethodDefaultSet,
		},
		{
			Name:        "payment_method_detach",
			Description: "Detach a stored payment method. For Stripe methods, detaches it through the bound payment_processor integration before updating Billing locally. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolPaymentMethodDetach,
		},
		// ── Issuer ───────────────────────────────────────────────────
		{
			Name:        "issuer_get",
			Description: "Fetch this install's billing identity (display/legal name, address, tax IDs, bank coordinates). Returns {issuer: {..., configured: bool}}. Singleton across the install.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolIssuerGet,
		},
		{
			Name:        "issuer_set",
			Description: "Upsert this install's billing identity. Singleton — calling this overwrites the existing settings. Args: display_name (required), legal_name, email, phone, website, brand_color, address {line1,line2,postal_code,city,state,country}, tax_ids [{type,value}], bank {iban,bic,bank_name,bank_code,beneficiary}, footer_text, default_terms.",
			InputSchema: schemaObject(map[string]any{
				"display_name":  map[string]any{"type": "string"},
				"legal_name":    map[string]any{"type": "string"},
				"email":         map[string]any{"type": "string"},
				"phone":         map[string]any{"type": "string"},
				"website":       map[string]any{"type": "string"},
				"brand_color":   map[string]any{"type": "string"},
				"address":       map[string]any{"type": "object"},
				"tax_ids":       map[string]any{"type": "array"},
				"bank":          map[string]any{"type": "object"},
				"footer_text":   map[string]any{"type": "string"},
				"default_terms": map[string]any{"type": "string"},
			}, []string{"display_name"}),
			Handler: a.toolIssuerSet,
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
	for _, entry := range []struct {
		name, desc string
		handler    func(*sdk.AppCtx, map[string]any) (any, error)
	}{
		{"billing_reconciliation_status", "Read pending or uncertain provider operations and pending event deliveries.", a.toolReconciliationStatus},
		{"invoices_history", "Page through the full payment and audit history with limit and offset.", a.toolInvoicesHistory},
		{"invoices_delete", "Delete an unissued draft invoice.", a.toolInvoicesDelete},
		{"invoices_cancel_payment", "Cancel an active provider payment after verifying its state.", a.toolInvoicesCancelPayment},
		{"invoices_resume_collection", "Explicitly resume collection after a refund hold.", a.toolInvoicesResumeCollection},
	} {
		tools = append(tools, sdk.Tool{Name: entry.name, Description: entry.desc, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"invoice_id": map[string]any{"type": "integer"}}, "required": []string{"invoice_id"}}, Handler: entry.handler})
	}

	for i := range tools {
		p, _ := tools[i].InputSchema["properties"].(map[string]any)
		if p == nil {
			continue
		}
		switch tools[i].Name {
		case "billing_reconciliation_status":
			delete(tools[i].InputSchema, "required")
			delete(p, "invoice_id")
		case "invoices_search", "customers_search", "payments_list", "invoices_history":
			p["offset"] = map[string]any{"type": "integer", "minimum": 0}
			p["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 200}
			if tools[i].Name == "invoices_search" {
				p["q"] = map[string]any{"type": "string"}
			}
		}
	}

	for i := range tools {
		h := tools[i].Handler
		tools[i].Handler = func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			if err := validateInput(args); err != nil {
				return nil, err
			}
			return h(ctx, args)
		}
	}
	return tools
}
