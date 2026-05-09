---
name: how-to-use-bills
description: |
  Bills' mental model + conventions. Load when working with vendors,
  bills, or outbound payments. Covers the AP lifecycle (received →
  approved → scheduled → paid), the difference between rejection and
  void, schedule-then-record discipline, currency / tax conventions,
  and the v0.1.0 scope. Triggers on: "bill", "vendor", "AP", "pay
  vendor", "approve invoice from", "log a bill", "wire to", or any
  bills tool call. Distinct from /billing — this app handles money
  going OUT, /billing handles money coming IN.
command: /bills
metadata:
  category: finance
  icon: 📥
---

# How to use Bills

Bills is the AP app — money going OUT to vendors. The mirror of the
`billing` app (which handles AR — money coming in from customers).
If you're drafting an invoice TO send a customer, that's `billing`.
If you're logging a bill a vendor sent us, that's here.

## Mental model

- **Vendors send us bills; we don't mint them.** The `vendor_invoice_number`
  field is THEIRS, not ours. We carry it alongside our internal
  bill id. Together with the vendor, it's the duplicate-entry guard:
  re-keying the same `(vendor, vendor_invoice_number)` pair errors
  with "this bill already exists."
- **Lifecycle is explicit.** A bill walks through 4 happy-path
  statuses, each via its own tool:

      bills_create        → received
      bills_approve       → approved
      bills_schedule_     → scheduled
      bill_payments_      → paid (when fully covered)

  Off-path: `bills_reject` → `disputed` (vendor needs to fix);
  `bills_void` → `void` (we entered by mistake).

- **Money is integer cents.** `unit_price_cents`, `total_cents`,
  `amount_cents`. `1500` means $15.00. Tax is **basis points**
  (`1000` = 10.00%). Same shape as `billing`.

- **Tax in bills is INPUT tax** (VAT we paid, sales tax on purchases)
  — different meaning from `billing`'s output tax even though the
  column is identical.

## v0.1.0 reality check

This install is on **v0.1.0**:

- `bills_create(provider="mercury"|"wise"|...)` → returns an error.
  Only `local` is supported. Bank-rail integrations land in v0.2.
- No OCR — the agent or human enters bill data manually. v0.1.1 adds
  `bills_attach_file` with auto-extraction.
- No inbound email ingest — v0.1.2.
- No multi-step approval — v0.1.0 has single-step approval recorded
  in the bill row + audit log.

## Lifecycle table

| Status      | How you got there            | What's editable                         |
|-------------|------------------------------|-----------------------------------------|
| `received`  | `bills_create`               | line items, due, notes, category, GL via `bills_update` |
| `approved`  | `bills_approve` from received | nothing — locked once approved          |
| `scheduled` | `bills_schedule_payment` from approved | re-schedule via the same tool   |
| `paid`      | `bill_payments_record` covers `total_cents` | nothing — terminal       |
| `disputed`  | `bills_reject` from received\|approved | nothing — terminal until vendor sends a corrected bill (which is a NEW bill) |
| `void`      | `bills_void` from any non-paid state | nothing — terminal                |

**Key rule**: edits stop at approval. If the bill is wrong post-approval,
`bills_reject` it (creates an audit trail saying we pushed back) and
ask the vendor to send a corrected invoice.

## Reject vs void

These look similar but mean different things and the vendor sees
different consequences.

| | `bills_reject` | `bills_void` |
|---|---|---|
| Use when | Bill is **wrong** — overcharge, duplicate the vendor sent, work not done | We **entered it by mistake** (wrong vendor, typo in amount, etc.) |
| Vendor expectation | Vendor needs to issue a corrected invoice | Vendor sees nothing — they don't know we entered anything |
| Final status | `disputed` | `void` |
| Required arg | `reason` (mandatory — the audit trail must explain why) | `reason` (optional, recommended) |

If unsure: ask. "About to reject INV-2026-0042 from Acme — that
tells them they need to re-send. Confirm, or did you mean to void
(which is silent)?"

## Schedule, then record

v0.1.0 separates "the agent or AP team intends to pay" from "the
money has actually left the account." Two tools:

1. `bills_schedule_payment(bill_id, scheduled_for, method)` —
   transitions approved → scheduled. Doesn't move money. The
   `scheduled_for` date + `scheduled_method` are hints for whoever
   actually executes the transfer.
2. `bill_payments_record(bill_id, amount_cents, method, sent_at)`
   — the money has left; this logs it. Updates `amount_paid_cents`
   and transitions to `paid` when fully covered.

You can skip step 1 and call `bill_payments_record` directly on an
`approved` bill (e.g. an immediate ad-hoc wire). The `scheduled`
step exists so an AP team can queue payments mid-week and a finance
person batches them on payday.

## Voiding requires confirmation

Before calling `bills_void`, quote vendor + invoice number + total
back to the user:

> "About to void Acme Corp's INV-2026-0042 ($1,200.00 USD) — this
> is silent (vendor won't know). Confirm, or did you mean reject?"

Voids are reversible only by recreating from scratch — but recreating
means burning a new internal id. Better to ask once.

## Currency

- **One currency per bill.** Locked at `bills_create`. To pay the
  same vendor in two currencies, log two bills.
- Use **ISO 4217** codes (`USD`, `EUR`, `GBP`, `JPY`, `CAD`, …).
- The vendor record has a `currency` preference — use it as the
  default on new bills for that vendor when the user doesn't
  specify.
- v0.1.0 doesn't handle FX: if the bill is in EUR but you pay from
  a USD account, record the payment in the same currency as the
  bill (EUR). v0.2 adds an `fx_rate` field on the payment row.

## Categories + GL accounts

Both are free-form strings in v0.1.0; the panel surfaces them but
the app doesn't validate against a fixed chart-of-accounts. Common
categories: `cogs`, `opex`, `capex`, `tax`, `software`, `travel`,
`utilities`. GL accounts mirror your accounting tool's codes.

Always set `category` on bills_create when context tells you what
it is — it makes the dashboard usable for "show me Q3 software
spend."

## Vendor first, then bill

Always look up or create the vendor **before** logging a bill. The
right primitive is `vendors_upsert_by_email` — returns
`{vendor, was_created}`. Don't loop `vendors_get` then
`vendors_create`; that's racy and the upsert is the right-by-default
path.

`vendors_get_context` is the pre-flight read for AP work: returns
the vendor, their open bills, recent payments, and lifetime spend.
Run it before logging if the vendor exists already — tells you
whether the bill might be a duplicate.

## Attaching the original PDF (v0.1.1+)

Bills can carry the vendor's original PDF/email — useful for audit
and for the dashboard's "Open original" button. The file lives in
the **storage** app; bills holds the link.

### When you have raw bytes (a downloaded PDF, an email attachment)

Use `bills_create_from_file`:
```
bills_create_from_file(
  name="aws-2026-04.pdf",
  content_base64="JVBERi0xLjQK…",
  vendor_id=…, vendor_invoice_number="…",
  line_items=[…],
)
```
One round-trip: bills uploads to storage internally, gets back a
file_id, creates the bill with the attachment linked.

### When the file is already in storage (e.g. user uploaded earlier)

Two-step composition is the cleanest path:
```
storage.files_get(id=…)         ← confirm it exists
bills_create(
  vendor_id=…,
  attached_file_id=…,           ← link by id
  line_items=[…],
)
```
or, if the bill already exists:
```
bills_attach_file(bill_id=…, file_id=…)
```

### Removing / replacing

- `bills_detach_file(bill_id)` — unlink. The file STAYS in storage
  (deliberate safety guard); the user can delete it from the storage
  panel if they want.
- `bills_attach_file(bill_id, file_id=NEW)` on a bill that already
  has an attachment — replaces the link. The previous file is NOT
  auto-deleted; it stays in storage.

### Don'ts

- Don't paste a storage URL into the bill's `notes` field. Use the
  attachment column. The dashboard renders it natively, the audit
  log tracks it, and v0.1.2's OCR will work off it.
- Don't attach internal-only commentary as a "PDF" — those go in
  `notes`. Attachments are the canonical original document the
  vendor sent us.
- Don't attach to a voided bill. The state machine refuses.
- Don't expect the attached file to "follow" if you delete it from
  storage. Bills carries an opaque file_id; if storage's row is
  deleted, the bill keeps the dangling id and the panel link 404s.
  Detach if you intend the file to be gone.

## OCR auto-fill (v0.1.2+)

When the install's `ocr_provider` config is set, `bills_create_from_file`
and the multipart `POST /bills/from-file` endpoint **automatically
extract fields from the uploaded file** before creating the bill.
No new tools, no opt-in flag — it just happens.

### Modes (v0.1.5+)

| `ocr_provider` | Backend | When |
|---|---|---|
| `""` (default) | **AUTO**: LLM if `vision_llm` is bound, else off. | Bind OpenCode Go in the dashboard's Bindings tab — that's all. No separate config flip required. |
| `"llm"` | Bound `vision_llm` integration (Kimi K2.6 by default). Errors if not bound. | Use when you want a hard guarantee OCR is on (instead of silently falling back to manual). |
| `"off"` | Force off, even if `vision_llm` is bound. | Operators who bound the integration for another purpose and don't want bills consuming it. |
| `"<slug>"` | Slug of an Apteva sidecar app exposing `extract_invoice(file_id)` (custom Mindee/Veryfi/etc. wrapper). | Forward-compat hook; no such app ships today. |

The agent doesn't pick the mode — it's an install setting. From the
agent's POV, `bills_create_from_file` either auto-fills (any non-empty
mode) or doesn't (empty mode).

What gets auto-filled when the caller didn't pass it:

- `vendor_id` — resolved by extracted email (upsert), or by unique
  name match, or by auto-creating a new vendor with extracted name
- `vendor_invoice_number`, `vendor_invoice_date`, `due_date`
- `currency`
- `line_items` (full list — descriptions, quantities, unit prices,
  per-line tax rates)

What doesn't auto-fill: `notes`, `category`, `gl_account`, `metadata`.
Those are intent fields you set explicitly.

### How to call it (when OCR is on)

Minimal call — let extraction do the work:
```
bills_create_from_file(
  name="aws-2026-04.pdf",
  content_base64="JVBERi0xLjQK…"
)
```
Extraction fills vendor + everything else. The response includes
`ocr_extracted: true`, the provider slug, the list of `fields_filled`,
and `vendor_resolved_via: "email" | "name_unique" | "auto_created"`.

Always-explicit call — caller wins on every field:
```
bills_create_from_file(
  name="aws-2026-04.pdf", content_base64=…,
  vendor_id=7,                    # skips vendor resolution
  category="opex", gl_account="6500"
)
```
Extraction still runs (in case fields like line_items are unset),
but anything you passed stays.

### When OCR runs and when it doesn't

- `bills_create_from_file` ✓
- `POST /bills/from-file` ✓
- `bills_attach_file` (existing bill) ✗ — by design; the bill is
  already shaped, overwriting would be hostile
- `bills_create(attached_file_id=…)` ✗ — explicit path, respected
- `POST /bills/{id}/attach` (replace existing attachment) ✗

### When OCR fails

Network error, garbage extraction, ambiguous vendor name — bills
**falls back to manual** rather than failing the call. The bill
gets created from your args alone; a warning lands in the bills
sidecar log; the response doesn't carry the `ocr_extracted` flag.
Worth checking the audit log on a bill you expected to have
extraction — no `extracted` entry means extraction was skipped.

The one case that DOES fail the create: extracted vendor name
matches multiple existing vendors and the caller didn't pass
`vendor_id`. The error lists candidate ids; retry with one
selected. The uploaded file is left orphaned in storage; the error
message includes the orphan id for cleanup.

### Cost

Each extraction call is billed by the provider. Vision-LLM mode
(`ocr_provider="llm"` via OpenCode Go) typically lands at flat-rate
subscription cost ($10/mo for the Go plan, no per-page billing).
Sidecar-mode wrappers around external APIs (Mindee is ~$0.01-0.03
per page, varies). v0.1.2 doesn't cache — uploading the same file
twice triggers two extractions. Mitigate by only calling
`bills_create_from_file` once per file, and using
`bills_create(attached_file_id=…)` for any subsequent re-creates.

## Recording payments

Methods: `wire`, `check`, `cash`, `ach`, `card`, `other`. Pick the
one that matches the actual transfer.

- Don't record `method='external_rail'` — reserved for v0.2 bank
  integrations; the tool errors.
- Don't record `method='stripe'` — Stripe is an INBOUND rail (it
  belongs in `billing`, not bills).
- Set `sent_at` to the actual date the money moved, not "now," when
  you're recording a payment that already cleared. Default is now
  (UTC) which is right when the agent is logging in real time.

## Don'ts

- Don't call `bills_create(provider="mercury"|...)` on v0.1.0.
- Don't void a paid bill — record an offsetting refund/credit
  (v0.2) or log it as a new bill with a negative amount when
  refund-flow lands.
- Don't try to set `status` directly via `bills_update` — the
  lifecycle tools own status.
- Don't record `method='external_rail'` or `method='stripe'`.
- Don't render the voucher PDF and think it's something you send
  the vendor. It's OUR copy for filing/audit. The vendor already
  has their own invoice.
- Don't enter a bill twice. The unique `(vendor, vendor_invoice_number)`
  index will error, but check first via `bills_search` so you can
  show the user "we already have this one."

## When in doubt

Run `bills_get(id=…)` (or `vendors_get_context`) before mutating.
The `status`, `amount_paid_cents`, `audit_log` fields are usually
enough to make the right decision without asking the user.
