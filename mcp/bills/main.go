// Bills v0.1.0 — local-only path.
//
// Vendors, bills (with line items), and outbound payments. The
// AP mirror of the `billing` app. Per-bill `provider` field is
// frozen at create — v0.2's bank-rail integrations land alongside
// existing local rows with no migration.
//
// State machine:
//
//   bills_create     → received
//   bills_update     : received only (line items, notes, …)
//   bills_approve    : received → approved
//   bills_reject     : received | approved → disputed
//   bills_schedule_  : approved → scheduled
//   bill_payments_   : scheduled | approved → paid (when covered)
//   bills_void       : any → void
//
// Differences worth flagging vs billing/main.go:
//   - We don't mint our own number — bills carry the VENDOR's invoice
//     number. Duplicate-entry guard is the (project, vendor, vendor
//     invoice number) unique partial index.
//   - Payment is two-step in v0.1.0: schedule + record. v0.2's bank
//     integrations collapse this when they actually move the money.
//   - INPUT tax (VAT input / sales tax we paid) lives in tax_cents,
//     mirroring billing's column shape but meaning the opposite.
package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: bills
display_name: Bills
version: 0.1.12
description: |
  Vendors, bills, and outbound payments. The AP mirror of billing.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.apps.call
  apps:
    - name: storage
      version: ">=0.1.0"
      reason: holds vendor PDF attachments, rendered vouchers, and source bytes for OCR
  integrations:
    - role: vision_llm
      kind: integration
      compatible_slugs: [anthropic-api, opencode-go]
      capabilities: [chat.complete, vision.describe]
      tools:
        chat.complete: chat_completion
        vision.describe: chat_completion
      required: false
      label: "Vision LLM provider"
      hint: "Anthropic API (Haiku 4.5, ~3s/page) or OpenCode Go (Qwen3.6 Plus, ~100s/page)."
provides:
  http_routes:
    - prefix: /
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/bills
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/bills.db
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
		return errors.New("bills requires a db block")
	}
	globalCtx = ctx

	if cfg := ctx.Config(); cfg != nil {
		if v := strings.ToLower(strings.TrimSpace(cfg.Get("default_provider"))); v != "" && v != "local" {
			ctx.Logger().Warn("bills v0.1.0: default_provider=" + v +
				" ignored — non-local providers land in v0.2; running local-only")
		}
	}

	ctx.Logger().Info("bills mounted",
		"version", "0.1.12",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"),
		"ocr_provider", configString(ctx, "ocr_provider", "(disabled)"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/vendors", Handler: a.handleHTTPVendorsCollection},
		{Pattern: "/vendors/", Handler: a.handleHTTPVendorItem},
		{Pattern: "/bills", Handler: a.handleHTTPBillsCollection},
		{Pattern: "/bills/", Handler: a.handleHTTPBillItem},
		{Pattern: "/payments", Handler: a.handleHTTPPaymentsCollection},
	}
}

func (a *App) handleHTTPVendorsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPVendorsList(w, r)
	case http.MethodPost:
		a.handleHTTPVendorUpsert(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPVendorItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/vendors/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 2 && parts[1] == "context" && r.Method == http.MethodGet {
		a.handleHTTPVendorContext(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPVendorGet(w, r)
	case http.MethodPatch:
		a.handleHTTPVendorUpdate(w, r)
	case http.MethodDelete:
		a.handleHTTPVendorDelete(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPBillsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPBillsList(w, r)
	case http.MethodPost:
		a.handleHTTPBillCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPBillItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/bills/")
	// /bills/from-file is a sibling of /bills/{id}/* — handle it
	// before parsing as a per-bill action.
	if rest == "from-file" {
		if r.Method == http.MethodPost {
			a.handleHTTPBillsCreateFromFile(w, r)
			return
		}
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) >= 2 {
		switch parts[1] {
		case "attach":
			// /bills/{id}/attach — multipart upload + link
			// /bills/{id}/attach/link — JSON {file_id} link existing
			if len(parts) == 3 && parts[2] == "link" {
				if r.Method == http.MethodPost {
					a.handleHTTPBillAttachLink(w, r)
					return
				}
				httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			switch r.Method {
			case http.MethodPost:
				a.handleHTTPBillAttachUpload(w, r)
				return
			case http.MethodDelete:
				a.handleHTTPBillDetach(w, r)
				return
			default:
				httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
		case "approve":
			if r.Method == http.MethodPost {
				a.handleHTTPBillApprove(w, r)
				return
			}
		case "reject":
			if r.Method == http.MethodPost {
				a.handleHTTPBillReject(w, r)
				return
			}
		case "schedule":
			if r.Method == http.MethodPost {
				a.handleHTTPBillSchedule(w, r)
				return
			}
		case "void":
			if r.Method == http.MethodPost {
				a.handleHTTPBillVoid(w, r)
				return
			}
		case "payments":
			if r.Method == http.MethodGet {
				a.handleHTTPBillPayments(w, r)
				return
			}
		case "pdf":
			if r.Method == http.MethodGet {
				a.handleHTTPBillPDF(w, r)
				return
			}
		case "print":
			if r.Method == http.MethodGet {
				a.handleHTTPBillPrint(w, r)
				return
			}
		}
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPBillGet(w, r)
	case http.MethodPatch:
		a.handleHTTPBillUpdate(w, r)
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

// ─── MCP tools (17) ─────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		// ── Vendors ────────────────────────────────────────────────
		{
			Name:        "vendors_search",
			Description: "Filtered vendor search. Args: q (free text matches name+email), email (exact), limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"q":     map[string]any{"type": "string"},
				"email": map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolVendorsSearch,
		},
		{
			Name:        "vendors_get",
			Description: "Snapshot only. Args: id OR email.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"email": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolVendorsGet,
		},
		{
			Name:        "vendors_get_context",
			Description: "Snapshot + open bills + recent payments + lifetime spend — pre-flight read before logging a bill. Args: id OR email, payments_limit (default 10).",
			InputSchema: schemaObject(map[string]any{
				"id":             map[string]any{"type": "integer"},
				"email":          map[string]any{"type": "string"},
				"payments_limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolVendorsGetContext,
		},
		{
			Name:        "vendors_upsert_by_email",
			Description: "Find-or-create vendor by email. Returns {vendor, was_created}. Args: email, defaults (name, phone, currency, default_payment_method, default_payment_terms_days, billing_address, tax_ids).",
			InputSchema: schemaObject(map[string]any{
				"email":    map[string]any{"type": "string"},
				"defaults": map[string]any{"type": "object"},
			}, []string{"email"}),
			Handler: a.toolVendorsUpsertByEmail,
		},
		{
			Name:        "vendors_update",
			Description: "Partial-patch a vendor. Args: id, patch.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"patch": map[string]any{"type": "object"},
			}, []string{"id", "patch"}),
			Handler: a.toolVendorsUpdate,
		},
		{
			Name:        "vendors_merge",
			Description: "Merge loser_id into winner_id. Reassigns bills + payments; loser is soft-deleted. Args: loser_id, winner_id.",
			InputSchema: schemaObject(map[string]any{
				"loser_id":  map[string]any{"type": "integer"},
				"winner_id": map[string]any{"type": "integer"},
			}, []string{"loser_id", "winner_id"}),
			Handler: a.toolVendorsMerge,
		},

		// ── Bills ──────────────────────────────────────────────────
		{
			Name: "bills_create",
			Description: "Log a bill received from a vendor. Status starts at 'received'. Provider arg ('local' | 'mercury' | 'wise' | 'bill_dot_com') falls back to install default. PROVIDER IS FROZEN: to switch, void and recreate. v0.1.0 only honours 'local'. Args: vendor_id, vendor_invoice_number, vendor_invoice_date, currency, provider, due_date, line_items [{description, quantity, unit_price_cents, tax_rate_bps?}], subtotal_cents, tax_cents, total_cents (when supplied, override the line-items computation — use these when the OCR-extracted invoice header total is the source of truth and line items are only a partial breakdown), notes, category, gl_account, attached_file_id, paid (optional {amount_cents?, method, paid_at?, reference?} — when present and amount covers the total, the bill skips received→approved→scheduled and lands directly in 'paid' with a matching payment row; use this for bills you've already paid outside the system, e.g. on a credit card).",
			InputSchema: schemaObject(map[string]any{
				"vendor_id":             map[string]any{"type": "integer"},
				"vendor_invoice_number": map[string]any{"type": "string"},
				"vendor_invoice_date":   map[string]any{"type": "string"},
				"currency":              map[string]any{"type": "string"},
				"provider":              map[string]any{"type": "string"},
				"due_date":              map[string]any{"type": "string"},
				"line_items":            map[string]any{"type": "array"},
				"subtotal_cents":        map[string]any{"type": "integer"},
				"tax_cents":             map[string]any{"type": "integer"},
				"total_cents":           map[string]any{"type": "integer"},
				"notes":                 map[string]any{"type": "string"},
				"category":              map[string]any{"type": "string"},
				"gl_account":            map[string]any{"type": "string"},
				"attached_file_id":      map[string]any{"type": "integer"},
				"metadata":              map[string]any{"type": "object"},
				"paid": map[string]any{
					"type":        "object",
					"description": "Optional already-paid block. When present and amount covers the bill total, status goes directly to 'paid'. Fields: amount_cents (defaults to total_cents), method (wire|check|cash|ach|card|other), paid_at (RFC3339 or YYYY-MM-DD; defaults to now), reference (optional txn id / check #).",
				},
			}, []string{"vendor_id"}),
			Handler: a.toolBillsCreate,
		},
		{
			Name:        "bills_update",
			Description: "Partial-patch a bill while in 'received' state. Errors on approved+ bills (the audit trail must reflect post-approval immutability). Args: id, patch.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"patch": map[string]any{"type": "object"},
			}, []string{"id", "patch"}),
			Handler: a.toolBillsUpdate,
		},
		{
			Name:        "bills_approve",
			Description: "Transition received → approved. Records the approver in the audit log. Required before scheduling payment. Args: bill_id, notes.",
			InputSchema: schemaObject(map[string]any{
				"bill_id": map[string]any{"type": "integer"},
				"notes":   map[string]any{"type": "string"},
			}, []string{"bill_id"}),
			Handler: a.toolBillsApprove,
		},
		{
			Name:        "bills_reject",
			Description: "Transition received|approved → disputed. Records the rejection reason. Use when the bill is wrong (overcharge, duplicate, work not done). Differs from void (which is for 'we entered this by mistake'). Args: bill_id, reason.",
			InputSchema: schemaObject(map[string]any{
				"bill_id": map[string]any{"type": "integer"},
				"reason":  map[string]any{"type": "string"},
			}, []string{"bill_id", "reason"}),
			Handler: a.toolBillsReject,
		},
		{
			Name:        "bills_schedule_payment",
			Description: "Transition approved → scheduled. Sets scheduled_for + payment method hint. Doesn't move money — bill_payments_record logs the actual outflow. Args: bill_id, scheduled_for (RFC3339, default now), method (wire|check|ach|card, default vendor's default_payment_method).",
			InputSchema: schemaObject(map[string]any{
				"bill_id":       map[string]any{"type": "integer"},
				"scheduled_for": map[string]any{"type": "string"},
				"method":        map[string]any{"type": "string"},
			}, []string{"bill_id"}),
			Handler: a.toolBillsSchedulePayment,
		},
		{
			Name:        "bills_void",
			Description: "Void a bill. Use for 'we entered this by mistake' (different from bills_reject which is 'vendor needs to fix'). Cannot void paid bills — record an offsetting refund instead. Args: bill_id, reason.",
			InputSchema: schemaObject(map[string]any{
				"bill_id": map[string]any{"type": "integer"},
				"reason":  map[string]any{"type": "string"},
			}, []string{"bill_id"}),
			Handler: a.toolBillsVoid,
		},
		{
			Name:        "bills_get",
			Description: "Fetch one bill with line items, payment history, and audit log. Args: id OR (vendor_id + vendor_invoice_number).",
			InputSchema: schemaObject(map[string]any{
				"id":                    map[string]any{"type": "integer"},
				"vendor_id":             map[string]any{"type": "integer"},
				"vendor_invoice_number": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolBillsGet,
		},
		{
			Name:        "bills_search",
			Description: "Filter bills. Args: vendor_id, status (received|approved|scheduled|paid|disputed|void), provider, currency, category, since (RFC3339), until (RFC3339), due_before, min_total_cents, max_total_cents, limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"vendor_id":       map[string]any{"type": "integer"},
				"status":          map[string]any{"type": "string"},
				"provider":        map[string]any{"type": "string"},
				"currency":        map[string]any{"type": "string"},
				"category":        map[string]any{"type": "string"},
				"since":           map[string]any{"type": "string"},
				"until":           map[string]any{"type": "string"},
				"due_before":      map[string]any{"type": "string"},
				"min_total_cents": map[string]any{"type": "integer"},
				"max_total_cents": map[string]any{"type": "integer"},
				"limit":           map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolBillsSearch,
		},
		{
			Name:        "bills_render_pdf",
			Description: "Render OUR voucher copy of the bill as a PDF — vendor info + their invoice number + line items + approval trail + payment history. Used for filing / audit, NOT for sending to the vendor. Default returns {pdf_base64, filename, size_bytes}. With save_to_storage=true, writes to the storage app and returns {file_id, url, ...}. Args: bill_id, save_to_storage, folder.",
			InputSchema: schemaObject(map[string]any{
				"bill_id":         map[string]any{"type": "integer"},
				"save_to_storage": map[string]any{"type": "boolean"},
				"folder":          map[string]any{"type": "string"},
			}, []string{"bill_id"}),
			Handler: a.toolBillsRenderPDF,
		},

		// ── Attachments (v0.1.1) ──────────────────────────────────
		{
			Name: "bills_attach_file",
			Description: "Link an existing storage app file to a bill. Use after the file is already in storage. Validates the file exists in storage before linking. Allowed on any status except void. Replaces an existing attachment if there is one — the previous file is NOT auto-deleted from storage. Args: bill_id, file_id.",
			InputSchema: schemaObject(map[string]any{
				"bill_id": map[string]any{"type": "integer"},
				"file_id": map[string]any{"type": "integer"},
			}, []string{"bill_id", "file_id"}),
			Handler: a.toolBillsAttachFile,
		},
		{
			Name:        "bills_detach_file",
			Description: "Unlink the attached storage file from a bill. Does NOT delete the file from storage — clean up via the storage panel if needed. Idempotent: detaching when nothing's attached is a no-op. Args: bill_id.",
			InputSchema: schemaObject(map[string]any{
				"bill_id": map[string]any{"type": "integer"},
			}, []string{"bill_id"}),
			Handler: a.toolBillsDetachFile,
		},
		{
			Name: "bills_create_from_file",
			Description: "Upload a PDF/image to the storage app AND create a bill row in one call. Saves the agent the storage.files_upload → bills_create two-step. Use when you have raw bytes; if you already have a storage file_id, use plain bills_create with attached_file_id instead. OCR auto-fills vendor + line items + totals when a vision_llm integration is bound. Args: name, content_base64, content_type (default 'application/pdf'), folder (default '/.bills/attachments/'), plus all bills_create args (vendor_id, vendor_invoice_number, line_items, totals, paid block to record an already-paid bill, etc.).",
			InputSchema: schemaObject(map[string]any{
				"name":           map[string]any{"type": "string"},
				"content_base64": map[string]any{"type": "string"},
				"content_type":   map[string]any{"type": "string"},
				"folder":         map[string]any{"type": "string"},

				"vendor_id":             map[string]any{"type": "integer"},
				"vendor_invoice_number": map[string]any{"type": "string"},
				"vendor_invoice_date":   map[string]any{"type": "string"},
				"currency":              map[string]any{"type": "string"},
				"provider":              map[string]any{"type": "string"},
				"due_date":              map[string]any{"type": "string"},
				"line_items":            map[string]any{"type": "array"},
				"subtotal_cents":        map[string]any{"type": "integer"},
				"tax_cents":             map[string]any{"type": "integer"},
				"total_cents":           map[string]any{"type": "integer"},
				"notes":                 map[string]any{"type": "string"},
				"category":              map[string]any{"type": "string"},
				"gl_account":            map[string]any{"type": "string"},
				"metadata":              map[string]any{"type": "object"},
				"paid": map[string]any{
					"type":        "object",
					"description": "Optional already-paid block — see bills_create.paid for the schema. Use this when uploading a receipt for a bill you've already paid (credit card charge, manual transfer, cash).",
				},
			}, []string{"name", "content_base64", "vendor_id"}),
			Handler: a.toolBillsCreateFromFile,
		},

		// ── Payments OUT ──────────────────────────────────────────
		{
			Name:        "bill_payments_record",
			Description: "Log an outbound payment to a vendor (wire / check / cash / ach / card / other). Updates bill.amount_paid_cents and transitions to 'paid' when fully covered. Bills must be in 'scheduled' or 'approved' state. method='external_rail' is reserved for v0.2 bank integrations. Args: bill_id, amount_cents (positive), method, sent_at (RFC3339, default now), notes.",
			InputSchema: schemaObject(map[string]any{
				"bill_id":      map[string]any{"type": "integer"},
				"amount_cents": map[string]any{"type": "integer"},
				"method":       map[string]any{"type": "string"},
				"sent_at":      map[string]any{"type": "string"},
				"notes":        map[string]any{"type": "string"},
			}, []string{"bill_id", "amount_cents", "method"}),
			Handler: a.toolBillPaymentsRecord,
		},
		{
			Name:        "bill_payments_list",
			Description: "List outbound payments. Args: vendor_id, bill_id, method, since (RFC3339), until (RFC3339), limit.",
			InputSchema: schemaObject(map[string]any{
				"vendor_id": map[string]any{"type": "integer"},
				"bill_id":   map[string]any{"type": "integer"},
				"method":    map[string]any{"type": "string"},
				"since":     map[string]any{"type": "string"},
				"until":     map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolBillPaymentsList,
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

type Vendor struct {
	ID                       int64           `json:"id"`
	ProjectID                string          `json:"project_id,omitempty"`
	Name                     string          `json:"name"`
	Email                    string          `json:"email,omitempty"`
	Phone                    string          `json:"phone,omitempty"`
	BillingAddress           json.RawMessage `json:"billing_address,omitempty"`
	TaxIDs                   json.RawMessage `json:"tax_ids,omitempty"`
	Currency                 string          `json:"currency,omitempty"`
	DefaultPaymentMethod     string          `json:"default_payment_method,omitempty"`
	DefaultPaymentTermsDays  *int            `json:"default_payment_terms_days,omitempty"`
	W9ReceivedAt             string          `json:"w9_received_at,omitempty"`
	ExternalID               string          `json:"external_id,omitempty"`
	Metadata                 json.RawMessage `json:"metadata,omitempty"`
	CreatedAt                string          `json:"created_at,omitempty"`
	UpdatedAt                string          `json:"updated_at,omitempty"`
	DeletedAt                string          `json:"deleted_at,omitempty"`
}

type Bill struct {
	ID                  int64           `json:"id"`
	ProjectID           string          `json:"project_id,omitempty"`
	VendorID            int64           `json:"vendor_id"`
	Provider            string          `json:"provider"`
	VendorInvoiceNumber string          `json:"vendor_invoice_number,omitempty"`
	VendorInvoiceDate   string          `json:"vendor_invoice_date,omitempty"`
	Status              string          `json:"status"`
	Currency            string          `json:"currency"`
	SubtotalCents       int64           `json:"subtotal_cents"`
	TaxCents            int64           `json:"tax_cents"`
	TotalCents          int64           `json:"total_cents"`
	AmountPaidCents     int64           `json:"amount_paid_cents"`
	DueDate             string          `json:"due_date,omitempty"`
	Notes               string          `json:"notes,omitempty"`
	Category            string          `json:"category,omitempty"`
	GLAccount           string          `json:"gl_account,omitempty"`
	AttachedFileID      *int64          `json:"attached_file_id,omitempty"`
	ApprovedAt          string          `json:"approved_at,omitempty"`
	ApprovedBy          string          `json:"approved_by,omitempty"`
	ScheduledFor        string          `json:"scheduled_for,omitempty"`
	ScheduledMethod     string          `json:"scheduled_method,omitempty"`
	ExternalID          string          `json:"external_id,omitempty"`
	ExternalURL         string          `json:"external_url,omitempty"`
	LastSyncedAt        string          `json:"last_synced_at,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
	PaidAt              string          `json:"paid_at,omitempty"`
	VoidedAt            string          `json:"voided_at,omitempty"`
	DisputedAt          string          `json:"disputed_at,omitempty"`
	CreatedAt           string          `json:"created_at,omitempty"`
	UpdatedAt           string          `json:"updated_at,omitempty"`
	LineItems           []BillLineItem  `json:"line_items,omitempty"`
	Payments            []*BillPayment  `json:"payments,omitempty"`
	AuditLog            []BillAudit     `json:"audit_log,omitempty"`
	// VendorName is populated by list/get queries (LEFT JOIN vendors).
	// Not stored on the bills table — purely a denormalised display
	// label so the UI can show "Acme Corp" instead of "Vendor #2"
	// without an N+1 round-trip.
	VendorName string `json:"vendor_name,omitempty"`
}

type BillLineItem struct {
	ID             int64           `json:"id,omitempty"`
	BillID         int64           `json:"bill_id,omitempty"`
	Position       int             `json:"position"`
	Description    string          `json:"description"`
	Quantity       float64         `json:"quantity"`
	UnitPriceCents int64           `json:"unit_price_cents"`
	AmountCents    int64           `json:"amount_cents"`
	TaxRateBps     int             `json:"tax_rate_bps"`
	ExternalID     string          `json:"external_id,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

type BillPayment struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
	BillID      *int64 `json:"bill_id,omitempty"`
	VendorID    int64  `json:"vendor_id"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
	Method      string `json:"method"`
	ExternalID  string `json:"external_id,omitempty"`
	SentAt      string `json:"sent_at"`
	Notes       string `json:"notes,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

type BillAudit struct {
	ID        int64           `json:"id"`
	BillID    int64           `json:"bill_id"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Details   json.RawMessage `json:"details,omitempty"`
	CreatedAt string          `json:"created_at"`
}

// ─── Vendor tool handlers ───────────────────────────────────────────

func (a *App) toolVendorsSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := dbVendorSearch(ctx.AppDB(), pid,
		strArg(args, "q"), strArg(args, "email"), limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"vendors": rows, "count": len(rows)}, nil
}

func (a *App) toolVendorsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	v, err := lookupVendor(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return map[string]any{"vendor": nil, "found": false}, nil
	}
	return map[string]any{"vendor": v, "found": true}, nil
}

func (a *App) toolVendorsGetContext(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	v, err := lookupVendor(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return map[string]any{"vendor": nil, "found": false}, nil
	}
	plimit := intArg(args, "payments_limit", 10)
	if plimit <= 0 || plimit > 100 {
		plimit = 10
	}
	openBills, err := dbBillSearch(ctx.AppDB(), pid, billFilters{
		vendorID: v.ID, statusIn: []string{"received", "approved", "scheduled"}, limit: 50,
	})
	if err != nil {
		return nil, err
	}
	pays, err := dbBillPaymentList(ctx.AppDB(), pid, paymentFilters{
		vendorID: v.ID, limit: plimit,
	})
	if err != nil {
		return nil, err
	}
	totals, err := dbVendorTotals(ctx.AppDB(), pid, v.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"vendor":          v,
		"open_bills":      openBills,
		"recent_payments": pays,
		"lifetime":        totals,
		"found":           true,
	}, nil
}

func (a *App) toolVendorsUpsertByEmail(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	email := normaliseEmail(strArg(args, "email"))
	if email == "" {
		return nil, errors.New("email required")
	}
	defaults, _ := args["defaults"].(map[string]any)
	v, created, err := dbVendorUpsertByEmail(ctx.AppDB(), pid, email, defaults)
	if err != nil {
		return nil, err
	}
	emitVendor(ctx, ifThen(created, "vendor.added", "vendor.updated"), v)
	return map[string]any{"vendor": v, "was_created": created}, nil
}

func (a *App) toolVendorsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	patch, _ := args["patch"].(map[string]any)
	if id == 0 || patch == nil {
		return nil, errors.New("id and patch required")
	}
	v, err := dbVendorUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		return nil, err
	}
	emitVendor(ctx, "vendor.updated", v)
	return map[string]any{"vendor": v}, nil
}

func (a *App) toolVendorsMerge(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	loser := int64Arg(args, "loser_id")
	winner := int64Arg(args, "winner_id")
	if loser == 0 || winner == 0 || loser == winner {
		return nil, errors.New("loser_id and winner_id required and must differ")
	}
	if err := dbVendorMerge(ctx.AppDB(), pid, loser, winner); err != nil {
		return nil, err
	}
	if ctx != nil {
		ctx.Emit("vendor.merged", map[string]any{
			"winner_id": winner, "loser_id": loser,
		})
	}
	return map[string]any{"merged": true, "winner_id": winner, "loser_id": loser}, nil
}

// ─── Bill tool handlers ─────────────────────────────────────────────

func (a *App) toolBillsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	vid := int64Arg(args, "vendor_id")
	if vid == 0 {
		return nil, errors.New("vendor_id required")
	}
	provider := strings.ToLower(strings.TrimSpace(strArg(args, "provider")))
	if provider == "" {
		provider = strings.ToLower(configString(ctx, "default_provider", "local"))
	}
	if provider != "local" {
		return nil, fmt.Errorf("provider=%q lands in v0.2 — use 'local' for now", provider)
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

	dueDate := strArg(args, "due_date")
	if dueDate == "" {
		// Compute from terms: vendor's default first, then install default,
		// then fall back to no due date.
		vendorTerms, _ := dbVendorPaymentTerms(ctx.AppDB(), pid, vid)
		if vendorTerms == 0 {
			if n, err := strconv.Atoi(configString(ctx, "default_payment_terms_days", "")); err == nil {
				vendorTerms = n
			}
		}
		if vendorTerms > 0 {
			dueDate = time.Now().UTC().Add(time.Duration(vendorTerms) * 24 * time.Hour).Format(time.RFC3339)
		}
	}

	bill := &Bill{
		ProjectID:           pid,
		VendorID:            vid,
		Provider:            provider,
		VendorInvoiceNumber: strArg(args, "vendor_invoice_number"),
		VendorInvoiceDate:   strArg(args, "vendor_invoice_date"),
		Status:              "received",
		Currency:            currency,
		DueDate:             dueDate,
		Notes:               strArg(args, "notes"),
		Category:            strArg(args, "category"),
		GLAccount:           strArg(args, "gl_account"),
		LineItems:           items,
		// Header totals — when supplied (typically by OCR pulling them
		// from the invoice header), they override dbBillCreate's
		// line-items-sum recompute. See dbBillCreate for the precedence
		// rules. Caller can supply any subset.
		SubtotalCents: int64Arg(args, "subtotal_cents"),
		TaxCents:      int64Arg(args, "tax_cents"),
		TotalCents:    int64Arg(args, "total_cents"),
	}
	if fid := int64Arg(args, "attached_file_id"); fid != 0 {
		bill.AttachedFileID = &fid
	}
	if md, ok := args["metadata"].(map[string]any); ok {
		if raw, err := json.Marshal(md); err == nil {
			bill.Metadata = raw
		}
	}

	paid, err := parsePaidOnCreate(args)
	if err != nil {
		return nil, err
	}
	created, err := dbBillCreate(ctx.AppDB(), bill, callerActor(args))
	if err != nil {
		return nil, err
	}
	if paid != nil {
		updated, err := dbBillMarkPaidOnCreate(ctx.AppDB(), pid, created.ID, *paid, callerActor(args))
		if err != nil {
			return nil, err
		}
		created = updated
		emitBill(ctx, "bill.paid", created)
	} else {
		emitBill(ctx, "bill.added", created)
	}
	return map[string]any{"bill": created}, nil
}

func (a *App) toolBillsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	patch, _ := args["patch"].(map[string]any)
	if id == 0 || patch == nil {
		return nil, errors.New("id and patch required")
	}
	bill, err := dbBillUpdate(ctx.AppDB(), pid, id, patch, callerActor(args),
		configIntBps(ctx, "tax_default_rate_bps"))
	if err != nil {
		return nil, err
	}
	emitBill(ctx, "bill.updated", bill)
	return map[string]any{"bill": bill}, nil
}

func (a *App) toolBillsApprove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "bill_id")
	if id == 0 {
		return nil, errors.New("bill_id required")
	}
	bill, err := dbBillApprove(ctx.AppDB(), pid, id, callerActor(args), strArg(args, "notes"))
	if err != nil {
		return nil, err
	}
	emitBill(ctx, "bill.approved", bill)
	return map[string]any{"bill": bill}, nil
}

func (a *App) toolBillsReject(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "bill_id")
	reason := strArg(args, "reason")
	if id == 0 {
		return nil, errors.New("bill_id required")
	}
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("reason required — bills_reject must record why for the audit trail")
	}
	bill, err := dbBillReject(ctx.AppDB(), pid, id, callerActor(args), reason)
	if err != nil {
		return nil, err
	}
	emitBill(ctx, "bill.disputed", bill)
	return map[string]any{"bill": bill}, nil
}

func (a *App) toolBillsSchedulePayment(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "bill_id")
	if id == 0 {
		return nil, errors.New("bill_id required")
	}
	scheduledFor := strArg(args, "scheduled_for")
	if scheduledFor == "" {
		scheduledFor = time.Now().UTC().Format(time.RFC3339)
	}
	method := strings.ToLower(strArg(args, "method"))
	if method != "" {
		if !validScheduleMethod(method) {
			return nil, fmt.Errorf("method %q not supported (wire | check | ach | card | other)", method)
		}
	}
	bill, err := dbBillSchedulePayment(ctx.AppDB(), pid, id, scheduledFor, method, callerActor(args))
	if err != nil {
		return nil, err
	}
	emitBill(ctx, "bill.scheduled", bill)
	return map[string]any{"bill": bill}, nil
}

func (a *App) toolBillsVoid(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "bill_id")
	if id == 0 {
		return nil, errors.New("bill_id required")
	}
	bill, err := dbBillVoid(ctx.AppDB(), pid, id, strArg(args, "reason"), callerActor(args))
	if err != nil {
		return nil, err
	}
	emitBill(ctx, "bill.voided", bill)
	return map[string]any{"bill": bill}, nil
}

func (a *App) toolBillsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	bill, err := lookupBill(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if bill == nil {
		return map[string]any{"bill": nil, "found": false}, nil
	}
	if err := loadBillChildren(ctx.AppDB(), pid, bill); err != nil {
		return nil, err
	}
	return map[string]any{"bill": bill, "found": true}, nil
}

func (a *App) toolBillsSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out, err := dbBillSearch(ctx.AppDB(), pid, billFilters{
		vendorID:      int64Arg(args, "vendor_id"),
		status:        strArg(args, "status"),
		provider:      strArg(args, "provider"),
		currency:      strArg(args, "currency"),
		category:      strArg(args, "category"),
		since:         strArg(args, "since"),
		until:         strArg(args, "until"),
		dueBefore:     strArg(args, "due_before"),
		minTotalCents: int64Arg(args, "min_total_cents"),
		maxTotalCents: int64Arg(args, "max_total_cents"),
		limit:         limit,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"bills": out, "count": len(out)}, nil
}

// ─── Payment tool handlers ──────────────────────────────────────────

func (a *App) toolBillPaymentsRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "bill_id")
	amount := int64Arg(args, "amount_cents")
	method := strings.ToLower(strArg(args, "method"))
	if id == 0 || method == "" {
		return nil, errors.New("bill_id and method required")
	}
	if amount <= 0 {
		return nil, errors.New("amount_cents must be > 0 (refunds-from-vendor are a v0.2 concept)")
	}
	if method == "external_rail" {
		return nil, errors.New("method='external_rail' is reserved for the v0.2 bank integrations — use 'wire', 'check', 'cash', 'ach', 'card', or 'other'")
	}
	switch method {
	case "wire", "check", "cash", "ach", "card", "other":
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
	sentAt := strArg(args, "sent_at")
	if sentAt == "" {
		sentAt = time.Now().UTC().Format(time.RFC3339)
	}
	requireW9 := strings.EqualFold(strings.TrimSpace(configString(ctx, "require_w9_before_payment", "false")), "true")
	pay, bill, err := dbBillPaymentRecord(ctx.AppDB(), pid, id, amount, method, sentAt,
		strArg(args, "notes"), callerActor(args), requireW9)
	if err != nil {
		return nil, err
	}
	emitBill(ctx, "bill.paid", bill) // listeners filter on status == 'paid'
	return map[string]any{"payment": pay, "bill": bill}, nil
}

func (a *App) toolBillPaymentsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out, err := dbBillPaymentList(ctx.AppDB(), pid, paymentFilters{
		vendorID: int64Arg(args, "vendor_id"),
		billID:   int64Arg(args, "bill_id"),
		method:   strArg(args, "method"),
		since:    strArg(args, "since"),
		until:    strArg(args, "until"),
		limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"payments": out, "count": len(out)}, nil
}

// ─── Attachment tool handlers (v0.1.1) ──────────────────────────────

func (a *App) toolBillsAttachFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	billID := int64Arg(args, "bill_id")
	fileID := int64Arg(args, "file_id")
	if billID == 0 || fileID == 0 {
		return nil, errors.New("bill_id and file_id required")
	}
	if err := storageFileExists(ctx, fileID); err != nil {
		return nil, err
	}
	bill, prevID, err := dbBillAttachFile(ctx.AppDB(), pid, billID, fileID, callerActor(args))
	if err != nil {
		return nil, err
	}
	emitBill(ctx, "bill.updated", bill)
	out := map[string]any{
		"bill":    bill,
		"file_id": fileID,
	}
	if prevID != 0 {
		out["replaced_file_id"] = prevID
	}
	return out, nil
}

func (a *App) toolBillsDetachFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	billID := int64Arg(args, "bill_id")
	if billID == 0 {
		return nil, errors.New("bill_id required")
	}
	bill, prevID, err := dbBillDetachFile(ctx.AppDB(), pid, billID, callerActor(args))
	if err != nil {
		return nil, err
	}
	emitBill(ctx, "bill.updated", bill)
	out := map[string]any{
		"bill":     bill,
		"detached": prevID != 0,
	}
	if prevID != 0 {
		out["file_id"] = prevID
	}
	return out, nil
}

func (a *App) toolBillsCreateFromFile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	// Resolve early so we don't upload bytes if project_id is missing.
	// toolBillsCreate re-resolves from the same args downstream.
	if _, err := resolveProjectFromArgs(args); err != nil {
		return nil, err
	}
	// vendor_id is OPTIONAL when an OCR provider is configured —
	// resolveVendorFromExtraction will fill it from the extracted
	// vendor email/name. When OCR is disabled, vendor_id is required
	// and we error early before uploading bytes.
	ocrEnabled := strings.TrimSpace(configString(ctx, "ocr_provider", "")) != ""
	if int64Arg(args, "vendor_id") == 0 && !ocrEnabled {
		return nil, errors.New("vendor_id required (OCR is disabled — set ocr_provider config to 'llm' or pass vendor_id)")
	}
	name := strArg(args, "name")
	b64 := strArg(args, "content_base64")
	if name == "" || b64 == "" {
		return nil, errors.New("name and content_base64 required")
	}
	contentType := strArg(args, "content_type")
	if contentType == "" {
		contentType = "application/pdf"
	}
	folder := strArg(args, "folder")
	if folder == "" {
		folder = "/.bills/attachments/"
	}

	// 1. Upload bytes to storage. Hard fail with a clear error when
	//    storage isn't installed — agent can retry without this tool.
	fileID, err := storageUploadBase64(ctx, name, folder, contentType, b64)
	if err != nil {
		return nil, err
	}

	// 2. Build the bill payload from the remaining args + the new
	//    file_id, then run the regular create. Errors here orphan
	//    the file in storage; we surface the file_id in the error
	//    so the caller can clean up.
	createArgs := map[string]any{}
	for _, k := range []string{
		"_project_id", "_caller",
		"vendor_id", "vendor_invoice_number", "vendor_invoice_date",
		"currency", "provider", "due_date",
		"line_items", "subtotal_cents", "tax_cents", "total_cents",
		"notes", "category", "gl_account", "metadata",
		"paid", // pass through the already-paid block to toolBillsCreate
	} {
		if v, ok := args[k]; ok {
			createArgs[k] = v
		}
	}
	createArgs["attached_file_id"] = fileID

	// 3. v0.1.2 — OCR auto-fill. When ocr_provider is configured and a
	//    capable integration is installed, extract structured fields
	//    from the file BEFORE creating the bill. Caller args win on
	//    every conflict; extraction only fills gaps. Failures are
	//    non-fatal — bill still gets created from caller args.
	pid, _ := resolveProjectFromArgs(args)
	extracted, ocrProvider, ocrErr := callOCR(ctx, fileID)
	var fieldsFilled []string
	var vendorVia string
	if ocrErr != nil {
		ctx.Logger().Warn("ocr extraction failed, falling back to manual fields",
			"provider", ocrProvider, "file_id", fileID, "err", ocrErr)
	} else if extracted != nil {
		fieldsFilled = mergeExtractedIntoArgs(createArgs, extracted)
		via, vErr := resolveVendorFromExtraction(ctx.AppDB(), pid, extracted, createArgs)
		if vErr != nil {
			// Vendor resolution couldn't pick a unique candidate.
			// Surface the orphan file id so the caller can clean up.
			return nil, fmt.Errorf("bills_create_from_file: %w (file uploaded as storage id %d, orphaned)", vErr, fileID)
		}
		vendorVia = via
	}

	out, err := a.toolBillsCreate(ctx, createArgs)
	if err != nil {
		return nil, fmt.Errorf("bills_create_from_file: bill create failed (%w) — file uploaded as storage id %d but no bill row exists; delete the orphan via storage if you don't intend to retry",
			err, fileID)
	}
	res := out.(map[string]any)
	res["file_id"] = fileID

	// Best-effort post-create audit so the user can see exactly what
	// extraction filled. Failure here doesn't unwind the bill.
	if extracted != nil && len(fieldsFilled) > 0 {
		bill := res["bill"].(*Bill)
		if err := writeExtractedAudit(ctx.AppDB(), bill.ID, ocrProvider, fieldsFilled, vendorVia, extracted.CostCents); err != nil {
			ctx.Logger().Warn("audit write failed for ocr extraction", "err", err)
		}
		// Also reload the bill so the response carries the extracted audit entry.
		if reloaded, _ := dbBillGetByID(ctx.AppDB(), pid, bill.ID); reloaded != nil {
			_ = loadBillChildren(ctx.AppDB(), pid, reloaded)
			res["bill"] = reloaded
		}
		res["ocr_extracted"] = true
		res["ocr_provider"] = ocrProvider
		res["fields_filled"] = fieldsFilled
		if vendorVia != "" {
			res["vendor_resolved_via"] = vendorVia
		}
	}
	return res, nil
}

// ─── Storage cross-app helpers ──────────────────────────────────────

// storageFileExists validates a storage file_id is reachable for the
// current project. Returns a clear error when storage isn't installed.
func storageFileExists(ctx *sdk.AppCtx, fileID int64) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("attach: storage app not installed for this project — install it to attach files to bills")
	}
	var got struct {
		ID int64 `json:"id"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get", map[string]any{
		"id": fileID,
	}, &got); err != nil {
		msg := err.Error()
		if strings.Contains(strings.ToLower(msg), "not installed") ||
			strings.Contains(strings.ToLower(msg), "no such app") {
			return errors.New("attach: storage app not installed for this project — install it to attach files to bills")
		}
		return fmt.Errorf("attach: storage file %d not found (%w)", fileID, err)
	}
	if got.ID == 0 {
		return fmt.Errorf("attach: storage file %d not found", fileID)
	}
	return nil
}

// storageUploadBase64 calls storage.files_upload with the bytes and
// returns the new file_id. Adds standard tags so the storage panel
// can filter "what came from bills."
func storageUploadBase64(ctx *sdk.AppCtx, name, folder, contentType, b64 string) (int64, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return 0, errors.New("upload: storage app not installed for this project — install it to attach files to bills")
	}
	var got struct {
		ID int64 `json:"id"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", map[string]any{
		"name":           name,
		"folder":         folder,
		"content_base64": b64,
		"content_type":   contentType,
		"tags":           []any{"bill", "attachment"},
		"source":         "bills",
	}, &got); err != nil {
		msg := err.Error()
		if strings.Contains(strings.ToLower(msg), "not installed") ||
			strings.Contains(strings.ToLower(msg), "no such app") {
			return 0, errors.New("upload: storage app not installed for this project — install it to attach files to bills")
		}
		return 0, fmt.Errorf("upload: storage call failed (%w)", err)
	}
	if got.ID == 0 {
		return 0, errors.New("upload: storage returned no file id")
	}
	return got.ID, nil
}

// ─── PDF rendering tool ─────────────────────────────────────────────

func (a *App) toolBillsRenderPDF(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "bill_id")
	if id == 0 {
		return nil, errors.New("bill_id required")
	}
	bill, vendor, err := loadBillForRender(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if bill == nil {
		return nil, fmt.Errorf("bill %d not found", id)
	}
	pdfBytes, err := renderBillPDF(bill, vendor)
	if err != nil {
		return nil, err
	}
	filename := suggestPDFFilename(bill, vendor)

	if !boolArg(args, "save_to_storage") {
		return map[string]any{
			"pdf_base64": base64.StdEncoding.EncodeToString(pdfBytes),
			"filename":   filename,
			"size_bytes": len(pdfBytes),
			"saved":      false,
		}, nil
	}
	folder := strArg(args, "folder")
	if folder == "" {
		folder = "/.bills/vouchers/"
	}
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
		"tags":           []any{"bill", "voucher", bill.Status},
		"source":         "bills",
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

// loadBillForRender wraps the get-with-children + customer fetch the
// /pdf and /print endpoints both need.
func loadBillForRender(db *sql.DB, pid string, id int64) (*Bill, *Vendor, error) {
	bill, err := dbBillGetByID(db, pid, id)
	if err != nil || bill == nil {
		return bill, nil, err
	}
	if err := loadBillChildren(db, pid, bill); err != nil {
		return bill, nil, err
	}
	vendor, err := dbVendorGetByID(db, pid, bill.VendorID)
	if err != nil {
		return bill, nil, err
	}
	return bill, vendor, nil
}

// ─── Event emission ─────────────────────────────────────────────────

func emitVendor(ctx *sdk.AppCtx, topic string, v *Vendor) {
	if ctx == nil || v == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"id":    v.ID,
		"name":  v.Name,
		"email": v.Email,
	})
}

func emitBill(ctx *sdk.AppCtx, topic string, b *Bill) {
	if ctx == nil || b == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"id":                    b.ID,
		"vendor_id":             b.VendorID,
		"vendor_invoice_number": b.VendorInvoiceNumber,
		"status":                b.Status,
		"total_cents":           b.TotalCents,
		"currency":              b.Currency,
	})
}

// ─── HTTP handlers ──────────────────────────────────────────────────

func (a *App) handleHTTPVendorsList(w http.ResponseWriter, r *http.Request) {
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
	rows, err := dbVendorSearch(ctx.AppDB(), pid, q, email, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"vendors": rows, "count": len(rows)})
}

func (a *App) handleHTTPVendorUpsert(w http.ResponseWriter, r *http.Request) {
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
		defaults = body
	}
	v, created, err := dbVendorUpsertByEmail(ctx.AppDB(), pid, email, defaults)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	emitVendor(ctx, ifThen(created, "vendor.added", "vendor.updated"), v)
	httpJSON(w, map[string]any{"vendor": v, "was_created": created})
}

func (a *App) handleHTTPVendorGet(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/vendors/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	v, err := dbVendorGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if v == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"vendor": v})
}

func (a *App) handleHTTPVendorContext(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/vendors/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	v, err := dbVendorGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if v == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	openBills, _ := dbBillSearch(ctx.AppDB(), pid, billFilters{
		vendorID: id, statusIn: []string{"received", "approved", "scheduled"}, limit: 50,
	})
	pays, _ := dbBillPaymentList(ctx.AppDB(), pid, paymentFilters{vendorID: id, limit: 10})
	totals, _ := dbVendorTotals(ctx.AppDB(), pid, id)
	httpJSON(w, map[string]any{
		"vendor":          v,
		"open_bills":      openBills,
		"recent_payments": pays,
		"lifetime":        totals,
	})
}

func (a *App) handleHTTPVendorUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/vendors/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	v, err := dbVendorUpdate(ctx.AppDB(), pid, id, patch)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	emitVendor(ctx, "vendor.updated", v)
	httpJSON(w, map[string]any{"vendor": v})
}

func (a *App) handleHTTPVendorDelete(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/vendors/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE vendors SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ctx != nil {
		ctx.Emit("vendor.deleted", map[string]any{"id": id})
	}
	httpJSON(w, map[string]any{"deleted": true})
}

func (a *App) handleHTTPBillsList(w http.ResponseWriter, r *http.Request) {
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
	vid, _ := strconv.ParseInt(q.Get("vendor_id"), 10, 64)
	out, err := dbBillSearch(ctx.AppDB(), pid, billFilters{
		vendorID:  vid,
		status:    q.Get("status"),
		provider:  q.Get("provider"),
		currency:  q.Get("currency"),
		category:  q.Get("category"),
		since:     q.Get("since"),
		until:     q.Get("until"),
		dueBefore: q.Get("due_before"),
		limit:     limit,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"bills": out, "count": len(out)})
}

func (a *App) handleHTTPBillCreate(w http.ResponseWriter, r *http.Request) {
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
	vid := int64Arg(body, "vendor_id")
	if vid == 0 {
		httpErr(w, http.StatusBadRequest, "vendor_id required")
		return
	}
	provider := strings.ToLower(strArg(body, "provider"))
	if provider == "" {
		provider = strings.ToLower(configString(ctx, "default_provider", "local"))
	}
	if provider != "local" {
		httpErr(w, http.StatusBadRequest, "non-local providers land in v0.2")
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
	bill := &Bill{
		ProjectID:           pid,
		VendorID:            vid,
		Provider:            provider,
		VendorInvoiceNumber: strArg(body, "vendor_invoice_number"),
		VendorInvoiceDate:   strArg(body, "vendor_invoice_date"),
		Status:              "received",
		Currency:            currency,
		DueDate:             strArg(body, "due_date"),
		Notes:               strArg(body, "notes"),
		Category:            strArg(body, "category"),
		GLAccount:           strArg(body, "gl_account"),
		LineItems:           items,
		SubtotalCents:       int64Arg(body, "subtotal_cents"),
		TaxCents:            int64Arg(body, "tax_cents"),
		TotalCents:          int64Arg(body, "total_cents"),
	}
	if fid := int64Arg(body, "attached_file_id"); fid != 0 {
		bill.AttachedFileID = &fid
	}
	if md, ok := body["metadata"].(map[string]any); ok {
		if raw, err := json.Marshal(md); err == nil {
			bill.Metadata = raw
		}
	}
	paid, err := parsePaidOnCreate(body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := dbBillCreate(ctx.AppDB(), bill, actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if paid != nil {
		updated, err := dbBillMarkPaidOnCreate(ctx.AppDB(), pid, created.ID, *paid, actorFromRequest(r))
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		created = updated
		emitBill(ctx, "bill.paid", created)
	} else {
		emitBill(ctx, "bill.added", created)
	}
	httpJSON(w, map[string]any{"bill": created})
}

func (a *App) handleHTTPBillGet(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/bills/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	bill, err := dbBillGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bill == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := loadBillChildren(ctx.AppDB(), pid, bill); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"bill": bill})
}

func (a *App) handleHTTPBillUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/bills/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	bill, err := dbBillUpdate(ctx.AppDB(), pid, id, patch, actorFromRequest(r),
		configIntBps(ctx, "tax_default_rate_bps"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitBill(ctx, "bill.updated", bill)
	httpJSON(w, map[string]any{"bill": bill})
}

func (a *App) handleHTTPBillApprove(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/bills/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	bill, err := dbBillApprove(ctx.AppDB(), pid, id, actorFromRequest(r), strArg(body, "notes"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitBill(ctx, "bill.approved", bill)
	httpJSON(w, map[string]any{"bill": bill})
}

func (a *App) handleHTTPBillReject(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/bills/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reason := strArg(body, "reason")
	if strings.TrimSpace(reason) == "" {
		httpErr(w, http.StatusBadRequest, "reason required")
		return
	}
	bill, err := dbBillReject(ctx.AppDB(), pid, id, actorFromRequest(r), reason)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitBill(ctx, "bill.disputed", bill)
	httpJSON(w, map[string]any{"bill": bill})
}

func (a *App) handleHTTPBillSchedule(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/bills/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	scheduledFor := strArg(body, "scheduled_for")
	if scheduledFor == "" {
		scheduledFor = time.Now().UTC().Format(time.RFC3339)
	}
	method := strings.ToLower(strArg(body, "method"))
	if method != "" && !validScheduleMethod(method) {
		httpErr(w, http.StatusBadRequest, "method not supported")
		return
	}
	bill, err := dbBillSchedulePayment(ctx.AppDB(), pid, id, scheduledFor, method, actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitBill(ctx, "bill.scheduled", bill)
	httpJSON(w, map[string]any{"bill": bill})
}

func (a *App) handleHTTPBillVoid(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/bills/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	bill, err := dbBillVoid(ctx.AppDB(), pid, id, strArg(body, "reason"), actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitBill(ctx, "bill.voided", bill)
	httpJSON(w, map[string]any{"bill": bill})
}

// ─── Attachment HTTP handlers (v0.1.1) ─────────────────────────────

// handleHTTPBillAttachLink — POST /bills/{id}/attach/link with
// JSON body {file_id: N}. Pure linking — no upload.
func (a *App) handleHTTPBillAttachLink(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/bills/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	fileID := int64Arg(body, "file_id")
	if fileID == 0 {
		httpErr(w, http.StatusBadRequest, "file_id required")
		return
	}
	if err := storageFileExists(ctx, fileID); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	bill, prevID, err := dbBillAttachFile(ctx.AppDB(), pid, id, fileID, actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitBill(ctx, "bill.updated", bill)
	out := map[string]any{"bill": bill, "file_id": fileID}
	if prevID != 0 {
		out["replaced_file_id"] = prevID
	}
	httpJSON(w, out)
}

// handleHTTPBillAttachUpload — POST /bills/{id}/attach (multipart).
// Reads the uploaded bytes, calls storage.files_upload, then links.
func (a *App) handleHTTPBillAttachUpload(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/bills/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	name, b64, contentType, err := readMultipartFile(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "/.bills/attachments/"
	}
	fileID, err := storageUploadBase64(ctx, name, folder, contentType, b64)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	bill, prevID, err := dbBillAttachFile(ctx.AppDB(), pid, id, fileID, actorFromRequest(r))
	if err != nil {
		// Bill state guard caught it (e.g. void). The upload landed in
		// storage; surface the orphan id so the dashboard can clean up
		// without an investigation.
		httpErr(w, http.StatusBadRequest,
			fmt.Sprintf("%s — uploaded as storage id %d (orphan; delete via storage panel)", err.Error(), fileID))
		return
	}
	emitBill(ctx, "bill.updated", bill)
	out := map[string]any{
		"bill":    bill,
		"file_id": fileID,
		"name":    name,
	}
	if prevID != 0 {
		out["replaced_file_id"] = prevID
	}
	httpJSON(w, out)
}

// handleHTTPBillDetach — DELETE /bills/{id}/attach.
func (a *App) handleHTTPBillDetach(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/bills/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	bill, prevID, err := dbBillDetachFile(ctx.AppDB(), pid, id, actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitBill(ctx, "bill.updated", bill)
	out := map[string]any{"bill": bill, "detached": prevID != 0}
	if prevID != 0 {
		out["file_id"] = prevID
	}
	httpJSON(w, out)
}

// handleHTTPBillsCreateFromFile — POST /bills/from-file.
// Multipart: file part + bill_json field with the rest of the bill.
// JSON: {file_id, bill: {...}} for already-uploaded files.
// Bytes path uploads to storage internally.
func (a *App) handleHTTPBillsCreateFromFile(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpStart := time.Now()
	ctx.Logger().Info("from-file: request received",
		"content_type", r.Header.Get("Content-Type"),
		"content_length", r.ContentLength)
	defer func() {
		ctx.Logger().Info("from-file: request done",
			"elapsed_ms", time.Since(httpStart).Milliseconds())
	}()

	contentType := r.Header.Get("Content-Type")
	var (
		fileID   int64
		filename string
		billBody map[string]any
	)

	if strings.HasPrefix(contentType, "multipart/") {
		name, b64, ct, err := readMultipartFile(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		filename = name
		// Optional bill_json field — when absent, create a stub bill
		// with just attached_file_id (status 'received', vendor_id
		// must come from the JSON; without one we error out — see
		// below).
		raw := r.FormValue("bill_json")
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &billBody); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid bill_json: "+err.Error())
				return
			}
		} else {
			billBody = map[string]any{}
		}
		folder := r.URL.Query().Get("folder")
		if folder == "" {
			folder = "/.bills/attachments/"
		}
		uploadStart := time.Now()
		fileID, err = storageUploadBase64(ctx, name, folder, ct, b64)
		if err != nil {
			ctx.Logger().Error("from-file: upload failed",
				"name", name, "folder", folder, "err", err)
			httpErr(w, http.StatusBadGateway, err.Error())
			return
		}
		ctx.Logger().Info("from-file: uploaded to storage",
			"file_id", fileID, "name", name, "folder", folder,
			"size_bytes", len(b64)*3/4,
			"upload_ms", time.Since(uploadStart).Milliseconds())
	} else {
		// JSON path.
		var body struct {
			FileID int64          `json:"file_id"`
			Bill   map[string]any `json:"bill"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if body.FileID == 0 {
			httpErr(w, http.StatusBadRequest, "file_id required")
			return
		}
		if err := storageFileExists(ctx, body.FileID); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		fileID = body.FileID
		billBody = body.Bill
		if billBody == nil {
			billBody = map[string]any{}
		}
	}

	// v0.1.2 — OCR auto-fill. Runs after the file is in storage but
	// before we validate vendor_id (extraction may resolve it). Same
	// "caller-args win" rule as the MCP path. Failures are non-fatal.
	ocrStart := time.Now()
	extracted, ocrProvider, ocrErr := callOCR(ctx, fileID)
	ocrElapsed := time.Since(ocrStart).Milliseconds()
	var fieldsFilled []string
	var vendorVia string
	if ocrErr != nil {
		ctx.Logger().Warn("from-file: ocr extraction failed, falling back to manual fields",
			"provider", ocrProvider, "file_id", fileID, "err", ocrErr,
			"ocr_ms", ocrElapsed)
	} else if extracted != nil {
		fieldsFilled = mergeExtractedIntoArgs(billBody, extracted)
		via, vErr := resolveVendorFromExtraction(ctx.AppDB(), pid, extracted, billBody)
		if vErr != nil {
			httpErr(w, http.StatusBadRequest,
				fmt.Sprintf("%s (file orphaned as storage id %d)", vErr.Error(), fileID))
			return
		}
		vendorVia = via
	}

	if int64Arg(billBody, "vendor_id") == 0 {
		hint := "pass vendor_id explicitly"
		if extracted == nil {
			// Either OCR is disabled or it errored out. Tell the user
			// which so they can fix the right thing.
			if strings.TrimSpace(configString(ctx, "ocr_provider", "")) == "" {
				hint = "set ocr_provider config to 'llm' (and bind the vision_llm integration), or pass vendor_id explicitly"
			} else if ocrErr != nil {
				hint = fmt.Sprintf("OCR failed (%s) — pass vendor_id explicitly or fix the OCR provider", truncate(ocrErr.Error(), 120))
			} else {
				hint = "OCR ran but couldn't identify a vendor — pass vendor_id explicitly"
			}
		}
		httpErr(w, http.StatusBadRequest,
			fmt.Sprintf("vendor_id required: %s (file uploaded as storage id %d — delete via storage panel if you don't intend to retry)", hint, fileID))
		return
	}

	provider := strings.ToLower(strArg(billBody, "provider"))
	if provider == "" {
		provider = strings.ToLower(configString(ctx, "default_provider", "local"))
	}
	if provider != "local" {
		httpErr(w, http.StatusBadRequest,
			fmt.Sprintf("non-local providers land in v0.2 (file orphaned as storage id %d)", fileID))
		return
	}
	currency := strings.ToUpper(strArg(billBody, "currency"))
	if currency == "" {
		currency = strings.ToUpper(configString(ctx, "default_currency", "USD"))
	}
	if !looksLikeISO4217(currency) {
		httpErr(w, http.StatusBadRequest,
			fmt.Sprintf("currency must be 3-letter ISO 4217 (file orphaned as storage id %d)", fileID))
		return
	}
	rawItems, _ := billBody["line_items"].([]any)
	items, err := normaliseLineItems(rawItems, configIntBps(ctx, "tax_default_rate_bps"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	bill := &Bill{
		ProjectID:           pid,
		VendorID:            int64Arg(billBody, "vendor_id"),
		Provider:            provider,
		VendorInvoiceNumber: strArg(billBody, "vendor_invoice_number"),
		VendorInvoiceDate:   strArg(billBody, "vendor_invoice_date"),
		Status:              "received",
		Currency:            currency,
		DueDate:             strArg(billBody, "due_date"),
		Notes:               strArg(billBody, "notes"),
		Category:            strArg(billBody, "category"),
		GLAccount:           strArg(billBody, "gl_account"),
		LineItems:           items,
		AttachedFileID:      &fileID,
		SubtotalCents:       int64Arg(billBody, "subtotal_cents"),
		TaxCents:            int64Arg(billBody, "tax_cents"),
		TotalCents:          int64Arg(billBody, "total_cents"),
	}
	if md, ok := billBody["metadata"].(map[string]any); ok {
		if rb, err := json.Marshal(md); err == nil {
			bill.Metadata = rb
		}
	}
	paid, err := parsePaidOnCreate(billBody)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := dbBillCreate(ctx.AppDB(), bill, actorFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusBadRequest,
			fmt.Sprintf("%s — file orphaned as storage id %d", err.Error(), fileID))
		return
	}
	if paid != nil {
		updated, err := dbBillMarkPaidOnCreate(ctx.AppDB(), pid, created.ID, *paid, actorFromRequest(r))
		if err != nil {
			httpErr(w, http.StatusBadRequest,
				fmt.Sprintf("bill created but mark-paid failed: %s (bill_id=%d)", err.Error(), created.ID))
			return
		}
		created = updated
		emitBill(ctx, "bill.paid", created)
	} else {
		emitBill(ctx, "bill.added", created)
	}
	if extracted != nil && len(fieldsFilled) > 0 {
		if err := writeExtractedAudit(ctx.AppDB(), created.ID, ocrProvider, fieldsFilled, vendorVia, extracted.CostCents); err != nil {
			ctx.Logger().Warn("audit write failed for ocr extraction", "err", err)
		}
		// Reload so the response carries the extraction audit entry.
		if reloaded, _ := dbBillGetByID(ctx.AppDB(), pid, created.ID); reloaded != nil {
			_ = loadBillChildren(ctx.AppDB(), pid, reloaded)
			created = reloaded
		}
	}
	out := map[string]any{
		"bill":    created,
		"file_id": fileID,
	}
	if filename != "" {
		out["filename"] = filename
	}
	if extracted != nil && len(fieldsFilled) > 0 {
		out["ocr_extracted"] = true
		out["ocr_provider"] = ocrProvider
		out["fields_filled"] = fieldsFilled
		if vendorVia != "" {
			out["vendor_resolved_via"] = vendorVia
		}
	}
	httpJSON(w, out)
}

// readMultipartFile parses a multipart request, finds the first
// uploaded file part (under any field name), and returns its base64,
// detected content-type, and filename.
func readMultipartFile(r *http.Request) (name, b64, contentType string, err error) {
	// 25MB ceiling — typical bill PDFs are <2MB. Keeps a runaway
	// upload from buffering forever.
	if err = r.ParseMultipartForm(25 << 20); err != nil {
		return "", "", "", fmt.Errorf("parse multipart: %w", err)
	}
	if r.MultipartForm == nil || len(r.MultipartForm.File) == 0 {
		return "", "", "", errors.New("no file in multipart body")
	}
	for _, fhs := range r.MultipartForm.File {
		if len(fhs) == 0 {
			continue
		}
		fh := fhs[0]
		f, err := fh.Open()
		if err != nil {
			return "", "", "", fmt.Errorf("open multipart file: %w", err)
		}
		buf := make([]byte, fh.Size)
		n, err := io.ReadFull(f, buf)
		_ = f.Close()
		if err != nil {
			return "", "", "", fmt.Errorf("read multipart file: %w", err)
		}
		ct := fh.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		return fh.Filename, base64.StdEncoding.EncodeToString(buf[:n]), ct, nil
	}
	return "", "", "", errors.New("no file in multipart body")
}

func (a *App) handleHTTPBillPayments(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/bills/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	out, err := dbBillPaymentList(ctx.AppDB(), pid, paymentFilters{billID: id, limit: 100})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"payments": out})
}

func (a *App) handleHTTPBillPDF(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/bills/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	bill, vendor, err := loadBillForRender(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bill == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	pdfBytes, err := renderBillPDF(bill, vendor)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	filename := suggestPDFFilename(bill, vendor)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	_, _ = w.Write(pdfBytes)
}

func (a *App) handleHTTPBillPrint(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathIntSegment(r.URL.Path, "/bills/", 0)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	bill, vendor, err := loadBillForRender(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bill == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write([]byte(renderBillHTML(bill, vendor)))
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
	vid, _ := strconv.ParseInt(q.Get("vendor_id"), 10, 64)
	bid, _ := strconv.ParseInt(q.Get("bill_id"), 10, 64)
	out, err := dbBillPaymentList(ctx.AppDB(), pid, paymentFilters{
		vendorID: vid, billID: bid,
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
	id := int64Arg(body, "bill_id")
	amount := int64Arg(body, "amount_cents")
	method := strings.ToLower(strArg(body, "method"))
	if id == 0 || amount <= 0 || method == "" {
		httpErr(w, http.StatusBadRequest, "bill_id, amount_cents (>0), method required")
		return
	}
	if method == "external_rail" {
		httpErr(w, http.StatusBadRequest, "method='external_rail' is reserved for v0.2")
		return
	}
	switch method {
	case "wire", "check", "cash", "ach", "card", "other":
	default:
		httpErr(w, http.StatusBadRequest, "unknown method")
		return
	}
	sentAt := strArg(body, "sent_at")
	if sentAt == "" {
		sentAt = time.Now().UTC().Format(time.RFC3339)
	}
	requireW9 := strings.EqualFold(strings.TrimSpace(configString(ctx, "require_w9_before_payment", "false")), "true")
	pay, bill, err := dbBillPaymentRecord(ctx.AppDB(), pid, id, amount, method, sentAt,
		strArg(body, "notes"), actorFromRequest(r), requireW9)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitBill(ctx, "bill.paid", bill)
	httpJSON(w, map[string]any{"payment": pay, "bill": bill})
}

// ─── DB layer ───────────────────────────────────────────────────────

// ── Vendors ──

func dbVendorSearch(db *sql.DB, pid, q, email string, limit int) ([]*Vendor, error) {
	where := []string{"project_id = ?", "deleted_at IS NULL"}
	args := []any{pid}
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
	rows, err := db.Query(
		`SELECT id, project_id, name, email, phone, billing_address, tax_ids,
		        currency, default_payment_method, default_payment_terms_days,
		        w9_received_at, external_id, metadata, created_at, updated_at
		 FROM vendors
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY updated_at DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Vendor
	for rows.Next() {
		v, err := scanVendor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func dbVendorGetByID(db *sql.DB, pid string, id int64) (*Vendor, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, email, phone, billing_address, tax_ids,
		        currency, default_payment_method, default_payment_terms_days,
		        w9_received_at, external_id, metadata, created_at, updated_at
		 FROM vendors
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, id, pid)
	v, err := scanVendor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

func dbVendorGetByEmail(db *sql.DB, pid, email string) (*Vendor, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, email, phone, billing_address, tax_ids,
		        currency, default_payment_method, default_payment_terms_days,
		        w9_received_at, external_id, metadata, created_at, updated_at
		 FROM vendors
		 WHERE project_id = ? AND email = ? AND deleted_at IS NULL
		 ORDER BY id DESC
		 LIMIT 1`, pid, email)
	v, err := scanVendor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

func lookupVendor(db *sql.DB, pid string, args map[string]any) (*Vendor, error) {
	if id := int64Arg(args, "id"); id != 0 {
		return dbVendorGetByID(db, pid, id)
	}
	if email := normaliseEmail(strArg(args, "email")); email != "" {
		return dbVendorGetByEmail(db, pid, email)
	}
	return nil, errors.New("id or email required")
}

func dbVendorUpsertByEmail(db *sql.DB, pid, email string, defaults map[string]any) (*Vendor, bool, error) {
	existing, err := dbVendorGetByEmail(db, pid, email)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, false, nil
	}
	name := strArg(defaults, "name")
	if name == "" {
		name = email
	}
	addr := jsonOrEmpty(defaults["billing_address"], "{}")
	taxes := jsonOrEmpty(defaults["tax_ids"], "[]")
	meta := jsonOrEmpty(defaults["metadata"], "{}")
	terms := int64Arg(defaults, "default_payment_terms_days")
	now := nowRFC3339()
	res, err := db.Exec(
		`INSERT INTO vendors (project_id, name, email, phone, billing_address, tax_ids,
		                     currency, default_payment_method, default_payment_terms_days,
		                     metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, name, email, strArg(defaults, "phone"), addr, taxes,
		strArg(defaults, "currency"), strArg(defaults, "default_payment_method"),
		nullInt64(terms), meta, now, now)
	if err != nil {
		return nil, false, err
	}
	id, _ := res.LastInsertId()
	v, err := dbVendorGetByID(db, pid, id)
	return v, true, err
}

func dbVendorUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Vendor, error) {
	if len(patch) == 0 {
		return dbVendorGetByID(db, pid, id)
	}
	allowed := map[string]bool{
		"name": true, "email": true, "phone": true,
		"currency": true, "billing_address": true, "tax_ids": true, "metadata": true,
		"default_payment_method": true, "default_payment_terms_days": true,
		"w9_received_at": true,
	}
	var sets []string
	var args []any
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
		return dbVendorGetByID(db, pid, id)
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id, pid)
	if _, err := db.Exec(
		`UPDATE vendors SET `+strings.Join(sets, ", ")+
			` WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, args...); err != nil {
		return nil, err
	}
	return dbVendorGetByID(db, pid, id)
}

func dbVendorMerge(db *sql.DB, pid string, loser, winner int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range []int64{loser, winner} {
		var n int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM vendors
			 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
			id, pid).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("vendor %d not found or deleted", id)
		}
	}
	if _, err := tx.Exec(
		`UPDATE bills SET vendor_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE vendor_id = ? AND project_id = ?`, winner, loser, pid); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE bill_payments SET vendor_id = ?
		 WHERE vendor_id = ? AND project_id = ?`, winner, loser, pid); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE vendors SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`, loser, pid); err != nil {
		return err
	}
	return tx.Commit()
}

func dbVendorTotals(db *sql.DB, pid string, vid int64) (map[string]any, error) {
	out := map[string]any{
		"billed_cents":      int64(0),
		"paid_cents":        int64(0),
		"outstanding_cents": int64(0),
		"bill_count":        0,
	}
	row := db.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(total_cents), 0),
		        COALESCE(SUM(amount_paid_cents), 0)
		 FROM bills
		 WHERE project_id = ? AND vendor_id = ?
		   AND deleted_at IS NULL
		   AND status IN ('received','approved','scheduled','paid')`,
		pid, vid)
	var count int
	var billed, paid int64
	if err := row.Scan(&count, &billed, &paid); err != nil {
		return out, err
	}
	out["bill_count"] = count
	out["billed_cents"] = billed
	out["paid_cents"] = paid
	out["outstanding_cents"] = billed - paid
	return out, nil
}

// dbVendorPaymentTerms returns the vendor's default_payment_terms_days,
// or 0 when null/missing. Used by bills_create to compute due_date when
// not specified.
func dbVendorPaymentTerms(db *sql.DB, pid string, vid int64) (int, error) {
	var n sql.NullInt64
	err := db.QueryRow(
		`SELECT default_payment_terms_days FROM vendors
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		vid, pid).Scan(&n)
	if err != nil || !n.Valid {
		return 0, err
	}
	return int(n.Int64), nil
}

// ── Bills ──

type billFilters struct {
	vendorID                     int64
	status                       string
	statusIn                     []string
	provider, currency, category string
	since, until, dueBefore      string
	minTotalCents, maxTotalCents int64
	limit                        int
}

func dbBillSearch(db *sql.DB, pid string, f billFilters) ([]*Bill, error) {
	where := []string{"project_id = ?", "deleted_at IS NULL"}
	args := []any{pid}
	if f.vendorID != 0 {
		where = append(where, "vendor_id = ?")
		args = append(args, f.vendorID)
	}
	if f.status != "" {
		where = append(where, "status = ?")
		args = append(args, f.status)
	}
	if len(f.statusIn) > 0 {
		placeholders := make([]string, len(f.statusIn))
		for i, s := range f.statusIn {
			placeholders[i] = "?"
			args = append(args, s)
		}
		where = append(where, "status IN ("+strings.Join(placeholders, ",")+")")
	}
	if f.provider != "" {
		where = append(where, "provider = ?")
		args = append(args, f.provider)
	}
	if f.currency != "" {
		where = append(where, "currency = ?")
		args = append(args, strings.ToUpper(f.currency))
	}
	if f.category != "" {
		where = append(where, "category = ?")
		args = append(args, f.category)
	}
	if f.since != "" {
		where = append(where, "created_at >= ?")
		args = append(args, f.since)
	}
	if f.until != "" {
		where = append(where, "created_at < ?")
		args = append(args, f.until)
	}
	if f.dueBefore != "" {
		where = append(where, "due_date < ?")
		args = append(args, f.dueBefore)
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
	// Scope `where` clauses to the bills table to disambiguate from
	// the vendors join.
	scopedWhere := make([]string, len(where))
	for i, w := range where {
		// All current `where` entries reference unqualified columns
		// that exist on bills only — prefix with `bills.` so the JOIN
		// doesn't make them ambiguous.
		if strings.Contains(w, ".") {
			scopedWhere[i] = w
		} else {
			scopedWhere[i] = "bills." + w
		}
	}
	rows, err := db.Query(
		`SELECT bills.id, bills.project_id, bills.vendor_id, bills.provider,
		        bills.vendor_invoice_number, bills.vendor_invoice_date,
		        bills.status, bills.currency, bills.subtotal_cents, bills.tax_cents,
		        bills.total_cents, bills.amount_paid_cents,
		        bills.due_date, bills.notes, bills.category, bills.gl_account,
		        bills.attached_file_id,
		        bills.approved_at, bills.approved_by, bills.scheduled_for, bills.scheduled_method,
		        bills.external_id, bills.external_url, bills.last_synced_at, bills.metadata,
		        bills.paid_at, bills.voided_at, bills.disputed_at,
		        bills.created_at, bills.updated_at,
		        vendors.name
		 FROM bills
		 LEFT JOIN vendors ON vendors.id = bills.vendor_id
		 WHERE `+strings.Join(scopedWhere, " AND ")+`
		 ORDER BY bills.updated_at DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Bill
	for rows.Next() {
		b, vname, err := scanBillWithVendor(rows)
		if err != nil {
			return nil, err
		}
		b.VendorName = vname
		out = append(out, b)
	}
	return out, rows.Err()
}

// scanBillWithVendor scans the same columns as scanBill plus a
// trailing nullable vendors.name (NULL when the LEFT JOIN finds no
// row, e.g. the vendor was hard-deleted out from under a bill).
func scanBillWithVendor(s rowScanner) (*Bill, string, error) {
	var b Bill
	var (
		invNum, invDate                                 sql.NullString
		dueDate, notes, category, gl                    sql.NullString
		fileID                                          sql.NullInt64
		approvedAt, approvedBy, scheduledFor, schedMeth sql.NullString
		ext, extURL, syncedAt                           sql.NullString
		meta                                            sql.NullString
		paidAt, voidedAt, disputedAt                    sql.NullString
		vendorName                                      sql.NullString
	)
	if err := s.Scan(
		&b.ID, &b.ProjectID, &b.VendorID, &b.Provider, &invNum, &invDate,
		&b.Status, &b.Currency, &b.SubtotalCents, &b.TaxCents, &b.TotalCents,
		&b.AmountPaidCents,
		&dueDate, &notes, &category, &gl, &fileID,
		&approvedAt, &approvedBy, &scheduledFor, &schedMeth,
		&ext, &extURL, &syncedAt, &meta,
		&paidAt, &voidedAt, &disputedAt, &b.CreatedAt, &b.UpdatedAt,
		&vendorName); err != nil {
		return nil, "", err
	}
	b.VendorInvoiceNumber = invNum.String
	b.VendorInvoiceDate = invDate.String
	b.DueDate = dueDate.String
	b.Notes = notes.String
	b.Category = category.String
	b.GLAccount = gl.String
	if fileID.Valid {
		v := fileID.Int64
		b.AttachedFileID = &v
	}
	b.ApprovedAt = approvedAt.String
	b.ApprovedBy = approvedBy.String
	b.ScheduledFor = scheduledFor.String
	b.ScheduledMethod = schedMeth.String
	b.ExternalID = ext.String
	b.ExternalURL = extURL.String
	b.LastSyncedAt = syncedAt.String
	b.PaidAt = paidAt.String
	b.VoidedAt = voidedAt.String
	b.DisputedAt = disputedAt.String
	if meta.Valid {
		b.Metadata = json.RawMessage(meta.String)
	}
	return &b, vendorName.String, nil
}

func dbBillGetByID(db *sql.DB, pid string, id int64) (*Bill, error) {
	row := db.QueryRow(
		`SELECT bills.id, bills.project_id, bills.vendor_id, bills.provider,
		        bills.vendor_invoice_number, bills.vendor_invoice_date,
		        bills.status, bills.currency, bills.subtotal_cents, bills.tax_cents,
		        bills.total_cents, bills.amount_paid_cents,
		        bills.due_date, bills.notes, bills.category, bills.gl_account,
		        bills.attached_file_id,
		        bills.approved_at, bills.approved_by, bills.scheduled_for, bills.scheduled_method,
		        bills.external_id, bills.external_url, bills.last_synced_at, bills.metadata,
		        bills.paid_at, bills.voided_at, bills.disputed_at,
		        bills.created_at, bills.updated_at,
		        vendors.name
		 FROM bills
		 LEFT JOIN vendors ON vendors.id = bills.vendor_id
		 WHERE bills.id = ? AND bills.project_id = ? AND bills.deleted_at IS NULL`, id, pid)
	b, vname, err := scanBillWithVendor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err == nil {
		b.VendorName = vname
	}
	return b, err
}

func lookupBill(db *sql.DB, pid string, args map[string]any) (*Bill, error) {
	if id := int64Arg(args, "id"); id != 0 {
		return dbBillGetByID(db, pid, id)
	}
	if vid := int64Arg(args, "vendor_id"); vid != 0 {
		if num := strArg(args, "vendor_invoice_number"); num != "" {
			row := db.QueryRow(
				`SELECT id, project_id, vendor_id, provider, vendor_invoice_number, vendor_invoice_date,
				        status, currency, subtotal_cents, tax_cents, total_cents, amount_paid_cents,
				        due_date, notes, category, gl_account, attached_file_id,
				        approved_at, approved_by, scheduled_for, scheduled_method,
				        external_id, external_url, last_synced_at, metadata,
				        paid_at, voided_at, disputed_at, created_at, updated_at
				 FROM bills
				 WHERE project_id = ? AND vendor_id = ? AND vendor_invoice_number = ? AND deleted_at IS NULL`,
				pid, vid, num)
			b, err := scanBill(row)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return b, err
		}
	}
	return nil, errors.New("id, or (vendor_id + vendor_invoice_number), required")
}

func dbBillCreate(db *sql.DB, bill *Bill, actor string) (*Bill, error) {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM vendors
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		bill.VendorID, bill.ProjectID).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("vendor %d not found", bill.VendorID)
	}
	// Totals: caller-supplied wins (OCR fills these from the invoice
	// header, which is more reliable than summing extracted line items
	// — line items are often a per-service summary that doesn't add up
	// to the header total). Only compute from line items when caller
	// didn't supply ANY of subtotal/tax/total. Partial supply (e.g.
	// total + tax but no subtotal) is filled in: subtotal = total - tax.
	if bill.SubtotalCents == 0 && bill.TaxCents == 0 && bill.TotalCents == 0 {
		bill.SubtotalCents, bill.TaxCents, bill.TotalCents = computeTotals(bill.LineItems)
	} else {
		// Backfill the one missing field if the other two are present.
		if bill.SubtotalCents == 0 && bill.TotalCents > 0 {
			bill.SubtotalCents = bill.TotalCents - bill.TaxCents
		}
		if bill.TotalCents == 0 {
			bill.TotalCents = bill.SubtotalCents + bill.TaxCents
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowRFC3339()
	res, err := tx.Exec(
		`INSERT INTO bills (project_id, vendor_id, provider, vendor_invoice_number,
		                    vendor_invoice_date, status, currency,
		                    subtotal_cents, tax_cents, total_cents, amount_paid_cents,
		                    due_date, notes, category, gl_account, attached_file_id,
		                    metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'received', ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
		bill.ProjectID, bill.VendorID, bill.Provider,
		nullStr(bill.VendorInvoiceNumber), nullStr(bill.VendorInvoiceDate),
		bill.Currency,
		bill.SubtotalCents, bill.TaxCents, bill.TotalCents,
		nullStr(bill.DueDate), nullStr(bill.Notes), nullStr(bill.Category),
		nullStr(bill.GLAccount),
		nullPtrInt64(bill.AttachedFileID),
		jsonOrEmpty(bill.Metadata, "{}"),
		now, now)
	if err != nil {
		// Sniff for the duplicate-entry unique constraint and surface
		// a helpful message; SQLite gives "UNIQUE constraint failed:
		// bills.project_id, bills.vendor_id, bills.vendor_invoice_number".
		if strings.Contains(err.Error(), "UNIQUE constraint failed: bills") {
			return nil, fmt.Errorf("bill from vendor %d with invoice number %q already exists — use bills_get to fetch it",
				bill.VendorID, bill.VendorInvoiceNumber)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	for i, li := range bill.LineItems {
		if _, err := tx.Exec(
			`INSERT INTO bill_line_items
			   (bill_id, position, description, quantity, unit_price_cents,
			    amount_cents, tax_rate_bps, metadata)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i, li.Description, li.Quantity, li.UnitPriceCents,
			li.AmountCents, li.TaxRateBps, jsonOrEmpty(li.Metadata, "{}")); err != nil {
			return nil, err
		}
	}
	if err := writeAuditTx(tx, id, actor, "create", map[string]any{
		"provider":              bill.Provider,
		"vendor_invoice_number": bill.VendorInvoiceNumber,
		"currency":              bill.Currency,
		"total_cents":           bill.TotalCents,
		"line_count":            len(bill.LineItems),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out, err := dbBillGetByID(db, bill.ProjectID, id)
	if err != nil || out == nil {
		return nil, err
	}
	if err := loadBillChildren(db, bill.ProjectID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// PaidOnCreate captures the "this bill was already paid by us
// outside the system" case (credit card charge, manual bank transfer,
// cash, etc). When supplied to a create call, the new bill skips the
// received → approved → scheduled lifecycle and lands directly in
// 'paid' state with a matching payment row attached.
type PaidOnCreate struct {
	// AmountCents defaults to bill.TotalCents when zero. Caller can
	// pass a smaller value for a partial pre-payment (status stays
	// 'received' in that case — partial pay-on-create isn't supported
	// to keep the lifecycle simple; either you've fully paid it or
	// you record the rest later through the normal flow).
	AmountCents int64  `json:"amount_cents,omitempty"`
	Method      string `json:"method"`
	PaidAt      string `json:"paid_at,omitempty"` // RFC3339; defaults to now
	Reference   string `json:"reference,omitempty"`
}

// parsePaidOnCreate extracts the optional "paid" block from a tool /
// HTTP body. Returns nil when the caller didn't supply one. Callers
// pass the unwrapped body map (whatever they normally read fields
// out of). Validation of method happens here so every entry point
// gets the same error message.
func parsePaidOnCreate(body map[string]any) (*PaidOnCreate, error) {
	if body == nil {
		return nil, nil
	}
	raw, ok := body["paid"]
	if !ok || raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New(`"paid" must be an object {amount_cents, method, paid_at, reference}`)
	}
	method := strings.ToLower(strArg(m, "method"))
	if method == "" {
		return nil, errors.New(`paid.method required (wire | check | cash | ach | card | other)`)
	}
	switch method {
	case "wire", "check", "cash", "ach", "card", "other":
	default:
		return nil, fmt.Errorf("paid.method=%q is not a valid method", method)
	}
	return &PaidOnCreate{
		AmountCents: int64Arg(m, "amount_cents"),
		Method:      method,
		PaidAt:      strArg(m, "paid_at"),
		Reference:   strArg(m, "reference"),
	}, nil
}

// dbBillMarkPaidOnCreate runs after dbBillCreate to record the
// already-paid payment + flip the bill straight to 'paid', bypassing
// the approve + schedule hops. Mirrors what the recordPayment +
// approve transitions would do, but in a single tx and without the
// status-gate (which forbids paying a 'received' bill).
//
// Caller is expected to have already supplied a fully-validated
// PaidOnCreate (parsePaidOnCreate did the method check). Returns the
// reloaded bill (now in status='paid' with the payment + audit log
// attached).
func dbBillMarkPaidOnCreate(db *sql.DB, pid string, billID int64, paid PaidOnCreate, actor string) (*Bill, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		vid      int64
		currency string
		total    int64
		status   string
	)
	if err := tx.QueryRow(
		`SELECT vendor_id, currency, total_cents, status
		 FROM bills WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		billID, pid).Scan(&vid, &currency, &total, &status); err != nil {
		return nil, err
	}
	if status != "received" {
		return nil, fmt.Errorf("dbBillMarkPaidOnCreate: bill %d is %s, expected freshly-created 'received'", billID, status)
	}
	amount := paid.AmountCents
	if amount == 0 {
		amount = total
	}
	if amount <= 0 {
		return nil, fmt.Errorf("paid.amount_cents must be positive (or omit to default to total %d)", total)
	}
	paidAt := paid.PaidAt
	if paidAt == "" {
		paidAt = nowRFC3339()
	}
	notes := ""
	if paid.Reference != "" {
		notes = "ref: " + paid.Reference
	}
	res, err := tx.Exec(
		`INSERT INTO bill_payments (project_id, bill_id, vendor_id, amount_cents,
		                            currency, method, sent_at, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, billID, vid, amount, currency, paid.Method, paidAt, nullStr(notes))
	if err != nil {
		return nil, err
	}
	payID, _ := res.LastInsertId()
	// Full payment → 'paid'; partial → keep 'received' (caller can
	// continue through the normal flow). We still attribute the
	// approval to "auto-paid" so the audit trail says how this bill
	// short-circuited the approval gate.
	finalStatus := "received"
	if amount >= total && total > 0 {
		finalStatus = "paid"
	}
	if _, err := tx.Exec(
		`UPDATE bills
		 SET amount_paid_cents = ?,
		     status = ?,
		     approved_at = ?,
		     approved_by = ?,
		     paid_at = CASE WHEN ? = 'paid' THEN ? ELSE paid_at END,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		amount, finalStatus, paidAt, "auto-paid", finalStatus, paidAt, billID); err != nil {
		return nil, err
	}
	auditAction := "created_paid"
	if finalStatus != "paid" {
		auditAction = "created_partial_paid"
	}
	if err := writeAuditTx(tx, billID, actor, auditAction, map[string]any{
		"payment_id":   payID,
		"amount_cents": amount,
		"method":       paid.Method,
		"paid_at":      paidAt,
		"reference":    paid.Reference,
		"final_status": finalStatus,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out, err := dbBillGetByID(db, pid, billID)
	if err != nil || out == nil {
		return nil, err
	}
	if err := loadBillChildren(db, pid, out); err != nil {
		return nil, err
	}
	return out, nil
}

func dbBillUpdate(db *sql.DB, pid string, id int64, patch map[string]any, actor string, defaultBps int) (*Bill, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var status string
	if err := tx.QueryRow(
		`SELECT status FROM bills WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("bill %d not found", id)
		}
		return nil, err
	}
	if status != "received" {
		return nil, fmt.Errorf("cannot update: bill is %s, only 'received' bills are editable. Reject + recreate if you need to change post-approval", status)
	}

	allowed := map[string]bool{
		"vendor_invoice_number": true, "vendor_invoice_date": true,
		"due_date": true, "notes": true, "category": true, "gl_account": true,
		"attached_file_id": true, "metadata": true,
	}
	var sets []string
	var args []any
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		if k == "metadata" {
			args = append(args, jsonOrEmpty(v, "{}"))
		} else {
			args = append(args, v)
		}
		sets = append(sets, k+" = ?")
	}

	// line_items replacement is opt-in via patch["line_items"] — fully
	// replaces the existing list and recomputes totals.
	var changedLineItems bool
	if rawItems, ok := patch["line_items"].([]any); ok {
		items, err := normaliseLineItems(rawItems, defaultBps)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM bill_line_items WHERE bill_id = ?`, id); err != nil {
			return nil, err
		}
		for i, li := range items {
			if _, err := tx.Exec(
				`INSERT INTO bill_line_items
				   (bill_id, position, description, quantity, unit_price_cents,
				    amount_cents, tax_rate_bps, metadata)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				id, i, li.Description, li.Quantity, li.UnitPriceCents,
				li.AmountCents, li.TaxRateBps, jsonOrEmpty(li.Metadata, "{}")); err != nil {
				return nil, err
			}
		}
		changedLineItems = true
	}

	if len(sets) > 0 {
		sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
		args = append(args, id, pid)
		if _, err := tx.Exec(
			`UPDATE bills SET `+strings.Join(sets, ", ")+
				` WHERE id = ? AND project_id = ?`, args...); err != nil {
			return nil, err
		}
	}
	if changedLineItems {
		if err := recomputeBillTotalsTx(tx, id); err != nil {
			return nil, err
		}
	}
	if err := writeAuditTx(tx, id, actor, "update", map[string]any{
		"fields_changed":      keysOf(patch),
		"line_items_replaced": changedLineItems,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	bill, err := dbBillGetByID(db, pid, id)
	if err != nil || bill == nil {
		return nil, err
	}
	if err := loadBillChildren(db, pid, bill); err != nil {
		return nil, err
	}
	return bill, nil
}

func dbBillApprove(db *sql.DB, pid string, id int64, actor, notes string) (*Bill, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	var lineCount int
	if err := tx.QueryRow(
		`SELECT b.status, (SELECT COUNT(*) FROM bill_line_items WHERE bill_id = b.id)
		 FROM bills b
		 WHERE b.id = ? AND b.project_id = ? AND b.deleted_at IS NULL`,
		id, pid).Scan(&status, &lineCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("bill %d not found", id)
		}
		return nil, err
	}
	if status == "approved" || status == "scheduled" || status == "paid" {
		// Idempotent — return current state.
		_ = tx.Commit()
		bill, err := dbBillGetByID(db, pid, id)
		if err != nil || bill == nil {
			return nil, err
		}
		if err := loadBillChildren(db, pid, bill); err != nil {
			return nil, err
		}
		return bill, nil
	}
	if status != "received" {
		return nil, fmt.Errorf("cannot approve: bill is %s, only 'received' bills can be approved", status)
	}
	if lineCount == 0 {
		return nil, errors.New("cannot approve a bill with no line items — add at least one or void instead")
	}
	if _, err := tx.Exec(
		`UPDATE bills
		 SET status='approved', approved_at = CURRENT_TIMESTAMP, approved_by = ?,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, actor, id); err != nil {
		return nil, err
	}
	if err := writeAuditTx(tx, id, actor, "approve", map[string]any{"notes": notes}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	bill, err := dbBillGetByID(db, pid, id)
	if err != nil || bill == nil {
		return nil, err
	}
	if err := loadBillChildren(db, pid, bill); err != nil {
		return nil, err
	}
	return bill, nil
}

func dbBillReject(db *sql.DB, pid string, id int64, actor, reason string) (*Bill, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(
		`SELECT status FROM bills WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("bill %d not found", id)
		}
		return nil, err
	}
	switch status {
	case "received", "approved":
		// ok
	case "disputed":
		// Idempotent.
		_ = tx.Commit()
		bill, err := dbBillGetByID(db, pid, id)
		if err != nil || bill == nil {
			return nil, err
		}
		if err := loadBillChildren(db, pid, bill); err != nil {
			return nil, err
		}
		return bill, nil
	default:
		return nil, fmt.Errorf("cannot reject: bill is %s — use bills_void instead for paid/scheduled bills", status)
	}
	if _, err := tx.Exec(
		`UPDATE bills
		 SET status='disputed', disputed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, id); err != nil {
		return nil, err
	}
	if err := writeAuditTx(tx, id, actor, "reject", map[string]any{"reason": reason}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	bill, err := dbBillGetByID(db, pid, id)
	if err != nil || bill == nil {
		return nil, err
	}
	if err := loadBillChildren(db, pid, bill); err != nil {
		return nil, err
	}
	return bill, nil
}

func dbBillSchedulePayment(db *sql.DB, pid string, id int64, scheduledFor, method, actor string) (*Bill, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status, vendorMethod string
	if err := tx.QueryRow(
		`SELECT b.status, COALESCE(v.default_payment_method, '')
		 FROM bills b JOIN vendors v ON v.id = b.vendor_id
		 WHERE b.id = ? AND b.project_id = ? AND b.deleted_at IS NULL`,
		id, pid).Scan(&status, &vendorMethod); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("bill %d not found", id)
		}
		return nil, err
	}
	if status != "approved" && status != "scheduled" {
		return nil, fmt.Errorf("cannot schedule: bill is %s, only 'approved' bills can be scheduled (re-scheduling 'scheduled' bills is supported)", status)
	}
	if method == "" {
		method = vendorMethod // fall back to vendor's default
	}
	if _, err := tx.Exec(
		`UPDATE bills
		 SET status='scheduled', scheduled_for = ?, scheduled_method = ?,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, scheduledFor, nullStr(method), id); err != nil {
		return nil, err
	}
	if err := writeAuditTx(tx, id, actor, "schedule", map[string]any{
		"scheduled_for": scheduledFor,
		"method":        method,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	bill, err := dbBillGetByID(db, pid, id)
	if err != nil || bill == nil {
		return nil, err
	}
	if err := loadBillChildren(db, pid, bill); err != nil {
		return nil, err
	}
	return bill, nil
}

func dbBillVoid(db *sql.DB, pid string, id int64, reason, actor string) (*Bill, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(
		`SELECT status FROM bills WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("bill %d not found", id)
		}
		return nil, err
	}
	switch status {
	case "void":
		// Idempotent.
	case "paid":
		return nil, errors.New("cannot void a paid bill — record an offsetting refund/credit instead (or wait for v0.2's reverse-payment tool)")
	case "received", "approved", "scheduled", "disputed":
		if _, err := tx.Exec(
			`UPDATE bills SET status='void', voided_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			 WHERE id = ?`, id); err != nil {
			return nil, err
		}
		if err := writeAuditTx(tx, id, actor, "void", map[string]any{"reason": reason}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("bill has unexpected status %s", status)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	bill, err := dbBillGetByID(db, pid, id)
	if err != nil || bill == nil {
		return nil, err
	}
	if err := loadBillChildren(db, pid, bill); err != nil {
		return nil, err
	}
	return bill, nil
}

// dbBillAttachFile sets attached_file_id on a bill, replacing any
// previous attachment. Returns the bill + the previous file_id (0
// if none was set). Refuses on void status.
func dbBillAttachFile(db *sql.DB, pid string, billID, fileID int64, actor string) (*Bill, int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var (
		status string
		prev   sql.NullInt64
	)
	if err := tx.QueryRow(
		`SELECT status, attached_file_id FROM bills
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		billID, pid).Scan(&status, &prev); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, fmt.Errorf("bill %d not found", billID)
		}
		return nil, 0, err
	}
	if status == "void" {
		return nil, 0, errors.New("cannot attach to a voided bill")
	}
	if _, err := tx.Exec(
		`UPDATE bills SET attached_file_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, fileID, billID); err != nil {
		return nil, 0, err
	}
	action := "attach"
	details := map[string]any{"file_id": fileID}
	if prev.Valid && prev.Int64 != 0 && prev.Int64 != fileID {
		action = "replace"
		details["replaced_file_id"] = prev.Int64
	}
	if err := writeAuditTx(tx, billID, actor, action, details); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	bill, err := dbBillGetByID(db, pid, billID)
	if err != nil || bill == nil {
		return nil, 0, err
	}
	if err := loadBillChildren(db, pid, bill); err != nil {
		return nil, 0, err
	}
	var prevID int64
	if prev.Valid && prev.Int64 != fileID {
		prevID = prev.Int64
	}
	return bill, prevID, nil
}

// dbBillDetachFile clears attached_file_id. Returns the bill + the
// previously-attached file_id (0 if nothing was attached).
// Idempotent: detaching nothing is a no-op (returns 0, no audit).
func dbBillDetachFile(db *sql.DB, pid string, billID int64, actor string) (*Bill, int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var (
		status string
		prev   sql.NullInt64
	)
	if err := tx.QueryRow(
		`SELECT status, attached_file_id FROM bills
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		billID, pid).Scan(&status, &prev); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, fmt.Errorf("bill %d not found", billID)
		}
		return nil, 0, err
	}
	if status == "void" {
		return nil, 0, errors.New("cannot detach from a voided bill")
	}
	if !prev.Valid || prev.Int64 == 0 {
		// Idempotent no-op.
		_ = tx.Commit()
		bill, err := dbBillGetByID(db, pid, billID)
		if err != nil || bill == nil {
			return nil, 0, err
		}
		if err := loadBillChildren(db, pid, bill); err != nil {
			return nil, 0, err
		}
		return bill, 0, nil
	}
	if _, err := tx.Exec(
		`UPDATE bills SET attached_file_id = NULL, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, billID); err != nil {
		return nil, 0, err
	}
	if err := writeAuditTx(tx, billID, actor, "detach", map[string]any{
		"file_id": prev.Int64,
	}); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	bill, err := dbBillGetByID(db, pid, billID)
	if err != nil || bill == nil {
		return nil, 0, err
	}
	if err := loadBillChildren(db, pid, bill); err != nil {
		return nil, 0, err
	}
	return bill, prev.Int64, nil
}

func loadBillChildren(db *sql.DB, pid string, b *Bill) error {
	rows, err := db.Query(
		`SELECT id, bill_id, position, description, quantity, unit_price_cents,
		        amount_cents, tax_rate_bps, external_id, metadata
		 FROM bill_line_items
		 WHERE bill_id = ?
		 ORDER BY position ASC`, b.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var li BillLineItem
		var ext, meta sql.NullString
		if err := rows.Scan(&li.ID, &li.BillID, &li.Position, &li.Description,
			&li.Quantity, &li.UnitPriceCents, &li.AmountCents, &li.TaxRateBps,
			&ext, &meta); err != nil {
			rows.Close()
			return err
		}
		li.ExternalID = ext.String
		if meta.Valid {
			li.Metadata = json.RawMessage(meta.String)
		}
		b.LineItems = append(b.LineItems, li)
	}
	rows.Close()

	pays, err := dbBillPaymentList(db, pid, paymentFilters{billID: b.ID, limit: 200})
	if err != nil {
		return err
	}
	b.Payments = pays

	auditRows, err := db.Query(
		`SELECT id, bill_id, actor, action, details, created_at
		 FROM bill_audit_log
		 WHERE bill_id = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT 100`, b.ID)
	if err != nil {
		return err
	}
	defer auditRows.Close()
	for auditRows.Next() {
		var a BillAudit
		var det sql.NullString
		if err := auditRows.Scan(&a.ID, &a.BillID, &a.Actor, &a.Action, &det, &a.CreatedAt); err != nil {
			return err
		}
		if det.Valid {
			a.Details = json.RawMessage(det.String)
		}
		b.AuditLog = append(b.AuditLog, a)
	}
	return nil
}

func recomputeBillTotalsTx(tx *sql.Tx, id int64) error {
	rows, err := tx.Query(
		`SELECT amount_cents, tax_rate_bps FROM bill_line_items WHERE bill_id = ?`, id)
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
		`UPDATE bills
		 SET subtotal_cents = ?, tax_cents = ?, total_cents = ?,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, subtotal, tax, total, id)
	return err
}

// ── Payments ──

type paymentFilters struct {
	vendorID, billID int64
	method           string
	since, until     string
	limit            int
}

func dbBillPaymentList(db *sql.DB, pid string, f paymentFilters) ([]*BillPayment, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.vendorID != 0 {
		where = append(where, "vendor_id = ?")
		args = append(args, f.vendorID)
	}
	if f.billID != 0 {
		where = append(where, "bill_id = ?")
		args = append(args, f.billID)
	}
	if f.method != "" {
		where = append(where, "method = ?")
		args = append(args, strings.ToLower(f.method))
	}
	if f.since != "" {
		where = append(where, "sent_at >= ?")
		args = append(args, f.since)
	}
	if f.until != "" {
		where = append(where, "sent_at < ?")
		args = append(args, f.until)
	}
	limit := f.limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, project_id, bill_id, vendor_id, amount_cents, currency,
		        method, external_id, sent_at, notes, created_at
		 FROM bill_payments
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY sent_at DESC, id DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*BillPayment
	for rows.Next() {
		var p BillPayment
		var bid sql.NullInt64
		var ext, notes sql.NullString
		if err := rows.Scan(&p.ID, &p.ProjectID, &bid, &p.VendorID, &p.AmountCents,
			&p.Currency, &p.Method, &ext, &p.SentAt, &notes, &p.CreatedAt); err != nil {
			return nil, err
		}
		if bid.Valid {
			v := bid.Int64
			p.BillID = &v
		}
		p.ExternalID = ext.String
		p.Notes = notes.String
		out = append(out, &p)
	}
	return out, rows.Err()
}

func dbBillPaymentRecord(db *sql.DB, pid string, billID int64, amount int64,
	method, sentAt, notes, actor string, requireW9 bool) (*BillPayment, *Bill, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var (
		status, currency string
		vid              int64
		total, paid      int64
		w9               sql.NullString
	)
	if err := tx.QueryRow(
		`SELECT b.vendor_id, b.status, b.currency, b.total_cents, b.amount_paid_cents,
		        v.w9_received_at
		 FROM bills b JOIN vendors v ON v.id = b.vendor_id
		 WHERE b.id = ? AND b.project_id = ? AND b.deleted_at IS NULL`,
		billID, pid).Scan(&vid, &status, &currency, &total, &paid, &w9); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, fmt.Errorf("bill %d not found", billID)
		}
		return nil, nil, err
	}
	if status != "scheduled" && status != "approved" {
		return nil, nil, fmt.Errorf("cannot record payment on %s bill — only 'approved' or 'scheduled' bills accept payments", status)
	}
	if requireW9 && !w9.Valid {
		return nil, nil, errors.New("vendor has no W-9 on file (require_w9_before_payment=true) — set vendors.update with w9_received_at first or flip the install config off")
	}

	res, err := tx.Exec(
		`INSERT INTO bill_payments (project_id, bill_id, vendor_id, amount_cents,
		                            currency, method, sent_at, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, billID, vid, amount, currency, method, sentAt, nullStr(notes))
	if err != nil {
		return nil, nil, err
	}
	payID, _ := res.LastInsertId()
	newPaid := paid + amount
	newStatus := status
	action := "partial_payment"
	if newPaid >= total && total > 0 {
		newStatus = "paid"
		action = "paid"
	}
	if _, err := tx.Exec(
		`UPDATE bills
		 SET amount_paid_cents = ?,
		     status = ?,
		     paid_at = CASE WHEN ? = 'paid' AND paid_at IS NULL THEN CURRENT_TIMESTAMP ELSE paid_at END,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, newPaid, newStatus, newStatus, billID); err != nil {
		return nil, nil, err
	}
	if err := writeAuditTx(tx, billID, actor, action, map[string]any{
		"payment_id":   payID,
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
	pays, err := dbBillPaymentList(db, pid, paymentFilters{billID: billID, limit: 1})
	if err != nil {
		return nil, nil, err
	}
	var pay *BillPayment
	for _, p := range pays {
		if p.ID == payID {
			pay = p
			break
		}
	}
	bill, err := dbBillGetByID(db, pid, billID)
	if err != nil || bill == nil {
		return pay, bill, err
	}
	if err := loadBillChildren(db, pid, bill); err != nil {
		return pay, bill, err
	}
	return pay, bill, nil
}

// ─── Audit log ──────────────────────────────────────────────────────

func writeAuditTx(tx *sql.Tx, billID int64, actor, action string, details map[string]any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		raw = []byte("{}")
	}
	if actor == "" {
		actor = "system"
	}
	_, err = tx.Exec(
		`INSERT INTO bill_audit_log (bill_id, actor, action, details)
		 VALUES (?, ?, ?, ?)`, billID, actor, action, string(raw))
	return err
}

// ─── Validation + normalisation ─────────────────────────────────────

func computeTotals(items []BillLineItem) (subtotal, tax, total int64) {
	for _, li := range items {
		subtotal += li.AmountCents
		tax += li.AmountCents * int64(li.TaxRateBps) / 10000
	}
	total = subtotal + tax
	return
}

func normaliseLineItems(raw []any, defaultBps int) ([]BillLineItem, error) {
	out := make([]BillLineItem, 0, len(raw))
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
		li := BillLineItem{
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

func normaliseEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func roundCents(f float64) int64 {
	if f >= 0 {
		return int64(f + 0.5)
	}
	return int64(f - 0.5)
}

func validScheduleMethod(m string) bool {
	switch strings.ToLower(m) {
	case "wire", "check", "ach", "card", "other":
		return true
	}
	return false
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ─── Scan helpers ───────────────────────────────────────────────────

type rowScanner interface {
	Scan(dest ...any) error
}

func scanVendor(s rowScanner) (*Vendor, error) {
	var v Vendor
	var email, phone, currency, ext sql.NullString
	var addr, taxes, meta sql.NullString
	var dpm sql.NullString
	var dpt sql.NullInt64
	var w9 sql.NullString
	if err := s.Scan(
		&v.ID, &v.ProjectID, &v.Name, &email, &phone,
		&addr, &taxes, &currency, &dpm, &dpt, &w9,
		&ext, &meta, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	v.Email = email.String
	v.Phone = phone.String
	v.Currency = currency.String
	v.DefaultPaymentMethod = dpm.String
	if dpt.Valid {
		n := int(dpt.Int64)
		v.DefaultPaymentTermsDays = &n
	}
	v.W9ReceivedAt = w9.String
	v.ExternalID = ext.String
	if addr.Valid {
		v.BillingAddress = json.RawMessage(addr.String)
	}
	if taxes.Valid {
		v.TaxIDs = json.RawMessage(taxes.String)
	}
	if meta.Valid {
		v.Metadata = json.RawMessage(meta.String)
	}
	return &v, nil
}

func scanBill(s rowScanner) (*Bill, error) {
	var b Bill
	var (
		invNum, invDate                                 sql.NullString
		dueDate, notes, category, gl                    sql.NullString
		fileID                                          sql.NullInt64
		approvedAt, approvedBy, scheduledFor, schedMeth sql.NullString
		ext, extURL, syncedAt                           sql.NullString
		meta                                            sql.NullString
		paidAt, voidedAt, disputedAt                    sql.NullString
	)
	if err := s.Scan(
		&b.ID, &b.ProjectID, &b.VendorID, &b.Provider, &invNum, &invDate,
		&b.Status, &b.Currency, &b.SubtotalCents, &b.TaxCents, &b.TotalCents,
		&b.AmountPaidCents,
		&dueDate, &notes, &category, &gl, &fileID,
		&approvedAt, &approvedBy, &scheduledFor, &schedMeth,
		&ext, &extURL, &syncedAt, &meta,
		&paidAt, &voidedAt, &disputedAt, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, err
	}
	b.VendorInvoiceNumber = invNum.String
	b.VendorInvoiceDate = invDate.String
	b.DueDate = dueDate.String
	b.Notes = notes.String
	b.Category = category.String
	b.GLAccount = gl.String
	if fileID.Valid {
		v := fileID.Int64
		b.AttachedFileID = &v
	}
	b.ApprovedAt = approvedAt.String
	b.ApprovedBy = approvedBy.String
	b.ScheduledFor = scheduledFor.String
	b.ScheduledMethod = schedMeth.String
	b.ExternalID = ext.String
	b.ExternalURL = extURL.String
	b.LastSyncedAt = syncedAt.String
	b.PaidAt = paidAt.String
	b.VoidedAt = voidedAt.String
	b.DisputedAt = disputedAt.String
	if meta.Valid {
		b.Metadata = json.RawMessage(meta.String)
	}
	return &b, nil
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

func boolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
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

func nullInt64(n int64) sql.NullInt64 {
	return sql.NullInt64{Int64: n, Valid: n != 0}
}

func nullPtrInt64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
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

func pathInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	id, _ := strconv.ParseInt(rest, 10, 64)
	return id
}

func pathIntSegment(path, prefix string, n int) int64 {
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if n >= len(parts) {
		return 0
	}
	id, _ := strconv.ParseInt(parts[n], 10, 64)
	return id
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

var globalCtx *sdk.AppCtx

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
