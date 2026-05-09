# Bills (v0.1.3)

Vendors, bills, and bill payments for Apteva agents and human teams.
The accounts-payable mirror of the `billing` app — money OUT instead
of money in.

## What's in v0.1.3

Adds **`ocr_provider="llm"` mode** on top of v0.1.2 — a built-in
vision-LLM path that runs inline (no separate sidecar app required).

- New manifest binding: `requires.integrations.[{role: vision_llm,
  compatible_slugs: [opencode-go], ...}]`. Bind OpenCode Go in the
  dashboard and pick a vision-capable model (Kimi K2.6 is the
  default).
- New file `ocr_llm.go` with the embedded path: storage fetch via
  signed URL → PDF render via PDFium-WASM → OpenAI-shape chat
  completion → JSON parse → same `ExtractedInvoice` shape that
  drives v0.1.2's auto-fill.
- PDFium-WASM via `klippa-app/go-pdfium` (Wazero runtime, BSD
  license). No CGO, no external system deps; ~10 MB binary delta.
  Lazy-init pool so the OnMount path is unchanged.
- New permissions in the manifest: `net.egress` (signed-URL fetch),
  `platform.connections.execute` (bound integration call),
  `platform.apps.call` (storage cross-app calls).
- Bills' own surface didn't change: 20 MCP tools, 5 tables, same
  `bills_create_from_file` contract.

The OpenCode Go subscription provides flat-rate access to Kimi K2.6
+ other vision models — typically cheaper than per-page Mindee
billing for moderate AP volume.

## What's in v0.1.2

Adds **inline OCR auto-fill** on top of v0.1.1:
- New `ocr_provider` config field. Set to a slug ("mindee",
  "veryfi", …) of an installed integration that exposes the
  standard `extract_invoice(file_id)` tool. Empty (default) =
  OCR disabled.
- When set, `bills_create_from_file` and `POST /bills/from-file`
  automatically extract vendor + line items + dates + totals before
  creating the bill row. **No new MCP tools, no new DB tables.**
- Caller-supplied args always win. Extraction only fills gaps.
- Vendor auto-match: by extracted email (upsert) → unique name match
  → auto-create. Ambiguous name match errors with candidate ids.
- Audit log entry `extracted` records the provider, filled field
  list, vendor resolution path, and provider-reported cost.
- Panel: small "Auto-filled by OCR" banner on bill detail when the
  bill carries an `extracted` audit entry.
- Failures are non-fatal — OCR errors fall back to manual fields.

The capability shape: any integration that exposes
`extract_invoice(file_id)` returning the documented `ExtractedInvoice`
JSON shape (see `ocr.go`) works as an OCR provider. Catalog-side
adapters map per-provider responses (Mindee's nested invoice fields,
Veryfi's flat shape, Document AI's entities, etc.) into the
normalised form bills consumes.

## What's in v0.1.1

Adds **PDF attachments** on top of v0.1.0:
- 3 new MCP tools: `bills_attach_file`, `bills_detach_file`,
  `bills_create_from_file` (one-shot upload + create).
- 4 new REST endpoints: `POST /bills/from-file` (multipart or JSON),
  `POST /bills/{id}/attach` (multipart upload + link),
  `POST /bills/{id}/attach/link` (JSON link existing), `DELETE
  /bills/{id}/attach` (unlink).
- Cross-app integration: bills calls `storage.files_get` for
  validation and `storage.files_upload` for the bytes path.
- Dashboard panel: drop-zone overlay on the Bills tab + vendor-pick
  modal; "Original document" section on bill detail with drop zone,
  Open / Replace / Remove buttons.

## What's in v0.1.0

- **Vendors** with billing address, tax IDs, default payment method
  + terms, W-9 received marker, soft-delete + merge.
- **Bills** with line items and an explicit lifecycle:
  `received → approved → scheduled → paid`, plus `disputed`
  (vendor needs to issue a corrected invoice) and `void` (we entered
  by mistake). Per-bill `provider` field (`local` only in v0.1.0;
  bank-rail providers land in v0.2) frozen at create.
- **Bill payments** for outbound money — wire, check, cash, ach,
  card.
- **Append-only audit log** per bill for status transitions + payments.
- **20 MCP tools** covering vendor CRUD + bill lifecycle +
  payment recording + voucher PDF rendering + PDF attachment
  (3 added in v0.1.1).
- **REST surface** at `/api/apps/bills/*` for the dashboard panel.
- **Bills panel** at `slot: project.page` plus inline `bill-card`
  and `vendor-card` chat components.
- **PDF voucher** at `GET /bills/{id}/pdf` — OUR copy for filing,
  not for sending to the vendor. Includes the vendor's own invoice
  number + our internal reference + payment trail.

## What's deferred

- **OCR / LLM extraction** of received PDFs — `bills_attach_file` lands
  in v0.1.1 with auto-fill of vendor / total / due / line items.
- **Inbound email integration** (forward to `bills@your-domain` →
  draft bill) lands in v0.1.2 once the messaging app gates this.
- **Bank-rail providers** (Mercury / Wise / Bill.com) — actually move
  money via API. v0.2.
- **Multi-step approvals** (manager OK + finance OK). v0.2 adds a
  `bill_approvals` table; v0.1.0 uses single-step approval recorded
  in the bill row + audit log.
- **Recurring bills** (rent, subscriptions). v0.2.
- **1099 generation / vendor tax forms.** v0.2.
- **Currency conversion** on payment when bill currency ≠ payment
  currency. v0.2 adds `fx_rate` on the payment row.

## Local development

```bash
cd mcp/bills
go build .
APTEVA_PROJECT_ID=test ./bills     # smoke run; binds to :8080
curl http://localhost:8080/health
```

See `migrations/001_init.sql` for the full schema and `main.go`'s
`MCPTools()` for the tool surface.

## Tests

Same three-tier convention as the other apps.

```bash
go test ./...                       # tier 1, ~500ms — pure + DB ops, in-process
go test -tags integration ./...     # tier 2, ~2s — real binary, real HTTP
apteva test ./scenarios/            # tier 3, ~3min — live agent + LLM
```

Counts today: 71 tier 1 tests · 10 tier 2 tests · 7 tier 3 scenarios.

## Relationship to `billing`

The two apps share patterns deliberately (lifecycle as state machine,
provider frozen at create, audit log per row, integer cents + basis
points for tax) but stay fully separate — different DBs, different
panels, different tool surfaces. See the bills v0.1 design doc for
the rationale on splitting AR + AP into two apps.
