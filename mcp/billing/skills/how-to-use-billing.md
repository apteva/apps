---
name: how-to-use-billing
description: |
  Billing's mental model + conventions. Load when working with
  customers, invoices, or payments — drafting an invoice, marking
  one paid, voiding, recording a wire payment, looking up balances.
  Covers invoice lifecycle (draft → open → paid / void / uncollectible),
  currency conventions, cents-not-dollars, Stripe payment links and
  reusable methods, refunds, and void confirmation rules. Triggers on:
  "invoice", "bill", "charge", "customer balance",
  "outstanding", "wire payment", "mark paid", "void invoice", or any
  billing tool call.
command: /billing
metadata:
  category: finance
  icon: 💰
---

# How to use Billing

Billing is the app where customer invoices live: drafted by agents
or humans, finalized into a numbered open invoice, marked paid as
money arrives. Before calling any billing tool, hold these
conventions.

## Mental model

- **SQLite is the source of truth.** Every read goes against the local DB.
  Verified Stripe webhooks append payment/refund rows and update invoice state.
- **An invoice is a state machine.** `draft → open → paid | void |
  uncollectible`. Status transitions are explicit (`finalize`,
  `void`, payment events) — you don't write `status` directly.
- **Money is integer cents.** `unit_price_cents`, `total_cents`,
  `amount_cents`. `1500` means $15.00. Never pass dollars or floats
  for money. Tax is **basis points** (`1000` = 10.00%).
- **Invoice provider is frozen at create.** Current releases issue local
  invoices; Stripe is a bound payment processor used for hosted Checkout
  links and reusable payment methods.

## Stripe payment processing

`invoices_send_payment_link` creates a Stripe-hosted Checkout Session for
the invoice's exact outstanding balance. It requires a bound
`payment_processor` integration. Billing never receives Stripe API or webhook
signing secrets; the platform registers and verifies the webhook through the
connection. The verified webhook records the payment idempotently.

**Stripe availability is a capability, never the default invoice workflow.**
Only call `invoices_send_payment_link` when the user's current request
explicitly asks for a Stripe link or payment link. Do not infer permission from
Stripe being connected, an invoice being created or finalized, the invoice
being open or due, or the customer having an email address. If the user merely
asks to create, finalize, or send an invoice, do not create a Stripe Session.

The tool creates and returns a URL; it does **not** email or otherwise deliver
it. Share or email the returned URL only through a separate channel or email
tool, and only when the user explicitly requested that delivery.

- Use `payment_method_setup_create` to create a hosted setup link.
- Use `payment_methods_list` before an off-session charge workflow.
- Never collect or store raw card/bank credentials in Billing metadata.
- Reissuing a payment link creates a new tracked Session; previous links can
  remain valid until Stripe expires them.


## Lifecycle

| Status            | Reachable from           | Reachable by                                          |
|-------------------|--------------------------|-------------------------------------------------------|
| `draft`           | (initial)                | `invoices_create`                                     |
| `open`            | `draft`                  | `invoices_finalize` (mints number)                    |
| `paid`            | `open`, `uncollectible`  | `payments_record` covers `total_cents`                |
| `void`            | `open`, `uncollectible`  | `invoices_void` (refuses on `paid`)                   |
| `uncollectible`   | `open`                   | reserved; v0.1.0 has no tool that sets this           |

**You don't write `status` directly.** Always call the transition
tool — it takes care of timestamps and audit log entries.

## What stays editable when

| In status     | Edit line items | Add payment | Void | Re-finalize | Delete |
|---------------|-----------------|-------------|------|-------------|--------|
| `draft`       | yes             | no          | no   | finalize    | yes    |
| `open`        | no              | yes         | yes  | idempotent  | no     |
| `paid`        | no              | yes (refund only)* | no | n/a    | no     |
| `void`        | no              | no          | (idemp.) | n/a    | no     |

*A negative payment on a paid invoice records a refund and reopens it when
the remaining paid amount falls below the total. A refund cannot exceed the
invoice's recorded payments.

## Voiding requires confirmation

Before calling `invoices_void`, quote the customer + number + total
back to the user, like:

> "About to void INV-2026-0042 for Acme Corp ($1,200.00 USD). Confirm?"

Voids are reversible only by recreating from scratch — you lose the
invoice number permanently. Never void without explicit confirmation
unless the user said "void", "void it", "cancel that invoice", or
"discard" with a specific invoice in scope.

## Currency

- **One currency per invoice.** Locked at `invoices_create`. To bill
  the same customer in two currencies, create two invoices.
- Use supported **two-decimal ISO 4217** codes (`USD`, `EUR`, `GBP`, `CAD`…).
  Zero/three/four-decimal currencies are rejected because the API contract is
  explicitly integer cents.
- The customer record has a `currency` preference — use it as the
  default on new invoices for that customer when the user doesn't
  specify.

## Tax

- Per-line `tax_rate_bps`. Basis points: `0` = no tax, `1000` =
  10.00%, `2000` = 20.00%, `725` = 7.25%.
- `tax_default_rate_bps` in the install config fills the gap when
  `invoices_create` / `invoices_add_line_item` don't set one. Many
  installs leave it at `0` and require explicit per-line tax.
- Totals roll up per line: `amount_cents = round(quantity *
  unit_price_cents)` and tax is rounded to the nearest cent, half away from
  zero.

## Customer first, then invoice

Always look up or create the customer **before** drafting an
invoice. The right primitive is `customers_upsert_by_email` —
returns `{customer, was_created}`. Don't loop `customers_get` then
`customers_create` yourself; that's racy and the upsert is the
"right by default" path.

`customers_get_context` is the pre-flight read for billing work:
returns the customer, their open invoices, recent payments, and
lifetime totals. Run it before drafting if the customer has
existing relationships — it'll tell you whether they have an open
balance you should consolidate.

Use `accounting_date` (`YYYY-MM-DD`) when the invoice belongs to a specific
accounting period that differs from when the record was created or finalized.
It is optional and can be corrected later with `invoices_update`.

## Sending the invoice — PDF + print view

Once an invoice is **open** (finalized, has a number), the agent has
three ways to share it:

| Surface | When to use | Returns |
|---|---|---|
| `invoices_render_pdf(invoice_id=…)` | Default. Get the PDF bytes back as base64 — useful when the agent is sending the file via another tool (email, messaging). | `{pdf_base64, filename, size_bytes}` |
| `invoices_render_pdf(invoice_id=…, save_to_storage=true)` | Storage app is installed and you want a re-shareable URL or to attach a `file-card` to chat. | `{file_id, url, filename, size_bytes}` |
| `GET /api/apps/billing/invoices/{id}/print` | A human is in the loop and wants to print or save-as-PDF themselves. | HTML page with browser-driven Print/Save action |
| `GET /api/apps/billing/invoices/{id}/pdf` | The dashboard's "Download PDF" button — same bytes as `invoices_render_pdf` but streamed direct. | `application/pdf` |

**Don't render PDFs for drafts.** Drafts have no number, no
finalized-at date, and no commitment behind them. Finalize first;
then render. The tool will technically work on a draft (the renderer
shows "Draft #N") but the customer sees a confusing artifact.

When `save_to_storage=true` and the storage app isn't installed,
the tool errors with a clear "install storage or retry without
save_to_storage" message. Default to `save_to_storage=false` and
attach the bytes inline unless you specifically need the file to
live in storage (e.g. to compose `respond(components=[file-card])`).

## Recording manual payments

Use `payments_record` for **non-Stripe** money — wire, cash, check,
other. Required args: `invoice_id`, `amount_cents`, `method`.
Default `received_at` is now (UTC); set it explicitly if the money
landed earlier (e.g. a wire that cleared yesterday). When that payment covers
the invoice, its `received_at` becomes the invoice's `paid_at`; the later time
when the payment was entered remains available as the payment's `created_at`.

If the payment fully covers the invoice, status flips to `paid`
automatically. Partial payments stay `open` and accumulate —
`amount_paid_cents` adds up across rows.

Stripe webhooks normally own `method="stripe"` rows. Manual Stripe recording
is supported for reconciliation, but always include the provider's unique
`external_id` so retries remain idempotent.

## Don'ts

- Don't pass `provider="stripe"` to `invoices_create`; Stripe processes
  payment for locally issued invoices rather than issuing the invoice itself.
- Don't render a PDF for a draft — finalize first so the file shows the real number.
- Don't try to set `status` directly via `customers_update`-style
  patches — the lifecycle tools own status.
- Don't void a paid invoice expecting a refund. Use `invoices_refund` for Stripe
  refunds; a negative manual ledger entry only records money already returned.
- Don't include sensitive data (card numbers, SSNs) in `notes` or
  `metadata`. Notes show in the dashboard panel; metadata is JSON
  the agent can read back.
- Don't use `confirm` rules as a soft gate — they're the gate. Ask before
  voiding. Never issue a Stripe payment link by default; create one only when
  the user explicitly asks for it.

## When in doubt

Run `invoices_get(id=…)` before mutating an invoice. The `status`,
`provider`, `total_cents`, `amount_paid_cents`, and `audit_log`
fields are usually enough to make the right decision without
asking the user.


## Retry, refund, and history rules

Reuse the same `idempotency_key` and original arguments when a provider call times
out. Never create a new key just to escape a pending operation. Check
`billing_reconciliation_status`; uncertain operations are recovered in the
background and older ambiguous requests require review.

`invoices_refund` sends a real provider refund and records its lifecycle. A refund
puts collection on hold by default. Set `reopen_invoice=true` only when the debt
remains due; otherwise use a separate accounting correction as appropriate.
`invoices_resume_collection` is an explicit decision to collect again.
`invoices_cancel_payment` retires an active provider payment after checking its
current state. Drafts can be removed with `invoices_delete`.

Use server search with `limit`/`offset` and inspect `has_more`. For complete invoice
history use `invoices_history`; its payments, audit entries, and refund requests
have independent continuation flags. Customer totals are grouped by currency in
`by_currency`; never add those values without an explicit conversion.

Finalized PDF/print identities are immutable snapshots. Set tax treatment
explicitly before finalization; exempt/reverse-charge invoices require zero tax.
Pass the intended invoice currency and retain catalog IDs and metadata when
updating line items. Negative-total documents cannot be finalized as invoices.
