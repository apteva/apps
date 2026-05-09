package main

// OCR extraction (v0.1.2). Inline auto-fill for `bills_create_from_file`
// + the multipart `POST /bills/from-file` endpoint when an OCR
// integration is installed.
//
// Strategy:
//   - Operator sets `ocr_provider` config to the slug of an installed
//     integration that exposes the standard `extract_invoice(file_id)`
//     tool. Empty (default) = OCR disabled, behavior identical to v0.1.1.
//   - When set + non-empty, callOCR runs after the file lands in
//     storage but BEFORE the bill row is inserted. Failures are
//     non-fatal — bill still gets created from caller args.
//   - mergeExtractedIntoArgs fills every bill arg the caller didn't
//     provide. Caller wins on every conflict.
//   - resolveVendorFromExtraction: email upsert → unique name match
//     → auto-create → ambiguity error. Sets args["vendor_id"] in place.
//   - After successful create, an "extracted" audit entry records
//     what fields were filled, the provider, the vendor resolution
//     path, and the provider's reported cost (if any).
//
// We deliberately don't cache extraction results — without a new
// table, there's no place to. Mindee bills per call; the operator
// is responsible for not thrashing. v0.1.3 may add a sha256-keyed
// cache table.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ExtractedInvoice is the normalised shape every `ocr_invoice`
// integration's `extract_invoice` tool returns. Catalog adapters
// (mindee, veryfi, etc.) map their native response into this.
type ExtractedInvoice struct {
	Vendor struct {
		Name    string                 `json:"name,omitempty"`
		Email   string                 `json:"email,omitempty"`
		Phone   string                 `json:"phone,omitempty"`
		Address map[string]any         `json:"address,omitempty"`
		TaxID   string                 `json:"tax_id,omitempty"`
		Extra   map[string]any         `json:"extra,omitempty"`
	} `json:"vendor"`
	InvoiceNumber  string  `json:"invoice_number,omitempty"`
	IssueDate      string  `json:"issue_date,omitempty"`
	DueDate        string  `json:"due_date,omitempty"`
	Currency       string  `json:"currency,omitempty"`
	SubtotalCents  int64   `json:"subtotal_cents,omitempty"`
	TaxCents       int64   `json:"tax_cents,omitempty"`
	TotalCents     int64   `json:"total_cents,omitempty"`
	PaymentTermsDays int   `json:"payment_terms_days,omitempty"`
	LineItems []struct {
		Description    string  `json:"description"`
		Quantity       float64 `json:"quantity,omitempty"`
		UnitPriceCents int64   `json:"unit_price_cents,omitempty"`
		AmountCents    int64   `json:"amount_cents,omitempty"`
		TaxRateBps     int     `json:"tax_rate_bps,omitempty"`
		Confidence     float64 `json:"confidence,omitempty"`
	} `json:"line_items,omitempty"`

	Confidences        map[string]float64 `json:"confidences,omitempty"`
	Provider           string             `json:"provider,omitempty"`
	ProviderRequestID  string             `json:"provider_request_id,omitempty"`
	CostCents          int64              `json:"cost_cents,omitempty"`
}

// callOCR invokes whichever OCR backend the install is configured for.
// Modes, dispatched on the `ocr_provider` config value:
//
//	""       AUTO (default, v0.1.5+). If the vision_llm integration is
//	         bound, behave as "llm". If not, OCR is off — returns
//	         (nil, "", nil) and the caller falls through to manual.
//	         The intent is: binding the integration IS the on switch;
//	         no separate config flip required.
//	"llm"    Force the LLM path (errors if vision_llm isn't bound).
//	         Useful when you want a clear failure if the binding ever
//	         disappears, instead of silently falling back to manual.
//	"off"    Force OFF, even if vision_llm is bound. Escape hatch for
//	         operators who bound the integration for another purpose
//	         and don't want bills to use it.
//	"<slug>" Treated as the slug of another Apteva sidecar app exposing
//	         an `extract_invoice(file_id)` MCP tool — forward-compat
//	         hook for custom providers. Calls via CallAppResult.
//
// Real failures (network, malformed response) return an error; the
// caller logs and continues with manual fields.
func callOCR(ctx *sdk.AppCtx, fileID int64) (*ExtractedInvoice, string, error) {
	provider := strings.TrimSpace(configString(ctx, "ocr_provider", ""))

	// Auto-detect (v0.1.5+): empty config means "use the binding if
	// present, otherwise off." Binding the vision_llm integration is
	// the user's intent to enable OCR — we don't want them to also
	// flip a separate config switch.
	autoDetected := false
	if provider == "" {
		if ctx != nil && ctx.IntegrationFor("vision_llm") != nil {
			provider = "llm"
			autoDetected = true
		} else {
			ctx.Logger().Info("ocr: skipped — no provider configured + no vision_llm binding",
				"file_id", fileID)
			return nil, "", nil // genuinely off
		}
	}
	if provider == "off" {
		ctx.Logger().Info("ocr: skipped — provider=off", "file_id", fileID)
		return nil, "", nil
	}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, provider, errors.New("ocr_provider set but platform API unavailable")
	}
	ctx.Logger().Info("ocr: dispatching",
		"provider", provider, "auto_detected", autoDetected, "file_id", fileID)

	// LLM path — uses the bound vision_llm integration (no separate
	// sidecar). Lives in ocr_llm.go.
	if provider == "llm" {
		parsed, providerLabel, err := callOCRViaLLM(ctx, fileID)
		if err != nil {
			return nil, providerLabel, err
		}
		return parsed, providerLabel, nil
	}

	// Sidecar-app path — provider is the slug of an installed app that
	// implements extract_invoice. No app of this kind ships in v0.1.3,
	// but the contract is here for anyone who wants to wrap a different
	// OCR API as a sidecar (Mindee, Veryfi, custom internal service).
	var parsed ExtractedInvoice
	if err := ctx.PlatformAPI().CallAppResult(provider, "extract_invoice", map[string]any{
		"file_id": fileID,
	}, &parsed); err != nil {
		return nil, provider, fmt.Errorf("ocr provider %q: %w", provider, err)
	}
	if parsed.Vendor.Name == "" && parsed.Vendor.Email == "" &&
		len(parsed.LineItems) == 0 && parsed.InvoiceNumber == "" && parsed.TotalCents == 0 {
		return nil, provider, fmt.Errorf("ocr provider %q: no parseable extraction in response", provider)
	}
	if parsed.Provider == "" {
		parsed.Provider = provider
	}
	return &parsed, provider, nil
}

// ─── Field merge ────────────────────────────────────────────────────

// mergeExtractedIntoArgs fills bill-create args with extracted values
// where the caller didn't provide them. Mutates `args` in place;
// returns the list of fields that were actually filled (for the
// audit log). Caller-supplied values are NEVER overwritten — even
// empty strings are treated as intentional.
//
// We don't fill: vendor_id (handled separately by
// resolveVendorFromExtraction), notes, category, gl_account
// (per the proposal — "don't guess these").
func mergeExtractedIntoArgs(args map[string]any, e *ExtractedInvoice) []string {
	if e == nil {
		return nil
	}
	var filled []string

	fillStr := func(key, val string) {
		if val == "" {
			return
		}
		if _, has := args[key]; has {
			return
		}
		args[key] = val
		filled = append(filled, key)
	}
	fillStr("vendor_invoice_number", e.InvoiceNumber)
	fillStr("vendor_invoice_date", e.IssueDate)
	fillStr("due_date", e.DueDate)
	fillStr("currency", strings.ToUpper(e.Currency))

	// Header totals — these come from the invoice's printed totals
	// row, which is more authoritative than summing extracted line
	// items (the model often pulls only a subset of lines, especially
	// on multi-page invoices or when many line items are $0). When
	// supplied here, dbBillCreate uses them directly instead of
	// recomputing from line_items.
	fillInt := func(key string, val int64) {
		if val == 0 {
			return
		}
		if _, has := args[key]; has {
			return
		}
		args[key] = val
		filled = append(filled, key)
	}
	fillInt("subtotal_cents", e.SubtotalCents)
	fillInt("tax_cents", e.TaxCents)
	fillInt("total_cents", e.TotalCents)

	if _, has := args["line_items"]; !has {
		if items := convertExtractedLineItems(e); len(items) > 0 {
			args["line_items"] = items
			filled = append(filled, "line_items")
		}
	}
	return filled
}

// convertExtractedLineItems maps the extraction's line items into the
// shape `bills_create` expects (an `[]any` of map[string]any). When
// quantity / unit price are missing but amount is present, we fall
// back to "1 × amount" so the line still lands rather than being
// dropped. Better an approximate row that the user can adjust than
// a missing entry.
func convertExtractedLineItems(e *ExtractedInvoice) []any {
	if len(e.LineItems) == 0 {
		return nil
	}
	out := make([]any, 0, len(e.LineItems))
	for _, li := range e.LineItems {
		desc := strings.TrimSpace(li.Description)
		if desc == "" {
			desc = "(no description)"
		}
		qty := li.Quantity
		unit := li.UnitPriceCents
		if qty <= 0 {
			qty = 1
		}
		if unit <= 0 {
			if li.AmountCents > 0 {
				unit = li.AmountCents
			} else {
				continue // can't reconstruct; skip silently
			}
		}
		out = append(out, map[string]any{
			"description":      desc,
			"quantity":         qty,
			"unit_price_cents": unit,
			"tax_rate_bps":     li.TaxRateBps,
		})
	}
	return out
}

// ─── Vendor resolution ──────────────────────────────────────────────

// resolveVendorFromExtraction picks (or creates) a vendor based on
// extracted data, mutating args["vendor_id"] in place. Returns a
// label describing how the resolution happened — "email" |
// "name_unique" | "auto_created" — for the audit log. Returns "" when
// caller already supplied vendor_id (no resolution needed).
//
// Errors out — rather than guessing — when the extraction returned
// nothing useful, or when the name fuzzy-matches multiple vendors.
// The agent retries with explicit vendor_id in those cases.
func resolveVendorFromExtraction(db *sql.DB, pid string, e *ExtractedInvoice, args map[string]any) (string, error) {
	if e == nil {
		return "", nil
	}
	if int64Arg(args, "vendor_id") != 0 {
		return "", nil // caller-supplied
	}

	email := normaliseEmail(e.Vendor.Email)
	if email != "" {
		defaults := map[string]any{}
		if e.Vendor.Name != "" {
			defaults["name"] = e.Vendor.Name
		}
		if e.Vendor.Phone != "" {
			defaults["phone"] = e.Vendor.Phone
		}
		if e.Vendor.Address != nil {
			defaults["billing_address"] = e.Vendor.Address
		}
		if e.PaymentTermsDays > 0 {
			defaults["default_payment_terms_days"] = e.PaymentTermsDays
		}
		if e.Vendor.TaxID != "" {
			defaults["tax_ids"] = []map[string]any{{"value": e.Vendor.TaxID}}
		}
		v, _, err := dbVendorUpsertByEmail(db, pid, email, defaults)
		if err != nil {
			return "", fmt.Errorf("vendor upsert (email %q): %w", email, err)
		}
		args["vendor_id"] = v.ID
		return "email", nil
	}

	name := strings.TrimSpace(e.Vendor.Name)
	if name == "" {
		return "", errors.New("OCR couldn't identify a vendor (no email or name) — call again with explicit vendor_id")
	}
	rows, err := dbVendorSearch(db, pid, name, "", 5)
	if err != nil {
		return "", err
	}
	if len(rows) == 1 {
		args["vendor_id"] = rows[0].ID
		return "name_unique", nil
	}
	if len(rows) > 1 {
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return "", fmt.Errorf("OCR matched name %q to %d existing vendors %v — call again with explicit vendor_id to disambiguate",
			name, len(rows), ids)
	}

	// No matches → create new vendor with NULL email (allowed).
	res, err := db.Exec(
		`INSERT INTO vendors (project_id, name, billing_address, tax_ids, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, '{}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		pid, name, jsonOrEmpty(e.Vendor.Address, "{}"), "[]")
	if err != nil {
		return "", fmt.Errorf("auto-create vendor %q: %w", name, err)
	}
	id, _ := res.LastInsertId()
	args["vendor_id"] = id
	return "auto_created", nil
}

// ─── Audit ──────────────────────────────────────────────────────────

// writeExtractedAudit records that OCR filled fields on a bill. Runs
// AFTER the create — so we know the bill id. Best-effort: if the audit
// write fails, we log but don't unwind the bill.
func writeExtractedAudit(db *sql.DB, billID int64, provider string, fieldsFilled []string,
	vendorResolvedVia string, costCents int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	details := map[string]any{
		"provider":            provider,
		"fields_filled":       fieldsFilled,
		"vendor_resolved_via": vendorResolvedVia,
	}
	if costCents > 0 {
		details["extraction_cost_cents"] = costCents
	}
	if err := writeAuditTx(tx, billID, "system:ocr", "extracted", details); err != nil {
		return err
	}
	return tx.Commit()
}
