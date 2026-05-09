# Bills (v0.1.9)

Vendors, bills, and bill payments for Apteva agents and human teams.
The accounts-payable mirror of the `billing` app — money OUT instead
of money in.

## What's in v0.1.9

LLM-OCR is now **dual-rail** — the `vision_llm` binding accepts either
`anthropic-api` (recommended, ~3s/page with Claude Haiku 4.5) or
`opencode-go` (Qwen3.6 Plus default, ~100s/page on the flat-rate plan).
Bills branches on `bound.AppSlug` to build the right request shape:

- **anthropic-api** → Anthropic Messages API: `system` as top-level
  field, single user message with image content blocks before text,
  response in `content[0].text` (parser handles ```json fences)
- **opencode-go** → OpenAI chat-completion: `messages` array with
  system message + user message with image_url parts, response in
  `choices[0].message.content` with reasoning_content fallback for
  reasoning-shaped models

Provider-aware defaults via the new `ocr_llm_model` config field
(empty = pick per provider): `claude-haiku-4-5-20251001` for
Anthropic, `qwen3.6-plus` for OpenCode Go (was `kimi-k2.6` —
Qwen tested cleaner shape and similar speed).

Plus pipeline observability — INFO logs at every stage of
bills_create_from_file's OCR flow (request received → upload →
ocr dispatch → storage fetch → render → vision_llm call → parse →
bill create) — and parser fix for reasoning-model `reasoning_content`
fallback.

## What's in v0.1.8

Pipeline observability for the LLM-OCR path — INFO logs at every
stage of bills_create_from_file's OCR flow so we can see exactly
where it stops when something goes wrong.

## What's in v0.1.7

OCR fetch via storage's new files_get_content tool (storage v0.9.5+)
fixes the multi-storage-install routing bug. Default storage folders
now dot-prefixed per storage's skill convention. Empty-state drop-zone
icon swapped from emoji to monochrome SVG.

## What's in v0.1.6

UX patch: the bills tab's empty state was just "No bills." text —
no visible affordance that you could drop a PDF to draft one.
Users couldn't discover the upload flow without already knowing
about it. Replaced with a real drop-zone card: 📄 icon, "Drop a
PDF here or click to upload", with a hidden file input wired to
the same OCR-first submit-then-fallback flow as drag-drop. The
"+ New" button still works for starting a bill blank.

The full-pane drag overlay still triggers on dragenter (unchanged).

## What's in v0.1.5

UX patch: **binding the integration is now the on switch**. Previously
operators had to bind OpenCode Go in the dashboard *and* set
`ocr_provider="llm"` in the install Settings tab — two-step config
that nobody discovered. Now `ocr_provider=""` (the default) auto-
detects: if `vision_llm` is bound, the LLM path runs; if not, OCR
is off.

New `ocr_provider` modes:
- `""` (default) — auto: LLM if bound, else off
- `"llm"` — force LLM (errors if not bound; for installs that want
  the binding loss to surface as a hard error rather than silent
  manual fallback)
- `"off"` — force off, even if bound (escape hatch)
- `"<slug>"` — sidecar app, unchanged

No code outside `callOCR` changed. Single-line behavior addition
plus updated config_schema description + skill doc.

## What's in v0.1.4

UX patch on the v0.1.3 LLM-OCR path — the dashboard's drop-PDF flow
no longer prompts for a vendor when OCR can resolve one. Two changes:

- **Backend**: `bills_create_from_file` and `POST /bills/from-file`
  now treat `vendor_id` as optional when an OCR provider is
  configured (let `resolveVendorFromExtraction` fill it). The "still
  missing post-OCR" error message now points at the actual gap —
  OCR disabled, OCR errored, or OCR ran but found no vendor.
- **Panel**: dropping a PDF on the bills tab uploads immediately
  instead of opening the vendor-pick modal. Modal still appears as a
  fallback when the backend tags the response with `vendor_id
  required` (i.e. OCR couldn't resolve). The "+ New" button (no file
  context) still goes through the modal directly.

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
