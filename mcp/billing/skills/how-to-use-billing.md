---
name: how-to-use-billing
description: |
  Billing's mental model + conventions. Load when working with
  customers, invoices, or payments — drafting an invoice, marking
  one paid, voiding, recording a wire payment, looking up balances.
  Covers invoice lifecycle (draft → open → paid / void / uncollectible),
  picking provider AT CREATE (frozen after), currency conventions,
  cents-not-dollars, void confirmation rules, and the v0.1.0 ⇄ stripe
  gap. Triggers on: "invoice", "bill", "charge", "customer balance",
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

- **SQLite is the source of truth.** Every read goes against the
  local DB. Don't try to call Stripe directly to get fresh state —
  v0.1.1's reconciler does that on a schedule.
- **An invoice is a state machine.** `draft → open → paid | void |
  uncollectible`. Status transitions are explicit (`finalize`,
  `void`, payment events) — you don't write `status` directly.
- **Money is integer cents.** `unit_price_cents`, `total_cents`,
  `amount_cents`. `1500` means $15.00. Never pass dollars or floats
  for money. Tax is **basis points** (`1000` = 10.00%).
- **Provider is frozen at create.** Pick `local` or `stripe` when
  you call `invoices_create`. To change after, delete the draft and
  recreate. The agent's job is to get it right the first time.

## v0.1.0 reality check

This install is on **v0.1.0**, which ships local-only. Provider must
be `local`. The Stripe provider lands in **v0.1.1**:

- `invoices_create(provider="stripe")` → returns an error.
- `invoices_get_payment_link` → not in the tool list yet.
- `customers_get_payment_methods` → not in the tool list yet.
- `payments_record(method="stripe")` → returns an error.

If the user asks for a card-payable hosted invoice, say "v0.1.0
ships local-only — the Stripe path lands in v0.1.1; for now I can
draft a local invoice you'd send manually."

## Picking a provider (when v0.1.1 lands)

Every invoice is either `local` (record-only, no payment processing)
or `stripe` (hosted invoice URL, card-payable, status updates from
Stripe). Once `invoices_create` commits, the choice is locked.

| Use **local** when…                       | Use **stripe** when…                       |
|-------------------------------------------|--------------------------------------------|
| Internal cost tracking                    | Customer needs to pay by card              |
| Paid by wire / cash / check               | You want a hosted invoice URL to share     |
| Free / zero-total                         | You want auto-updated payment status       |
| Customer refuses card payment             | Customer is already in Stripe              |
| Speculative draft you might void          | Recurring relationship                     |

When v0.1.1 ships: before defaulting to `stripe`, run
`customers_get_payment_methods(customer_id=…)`. If they have stored
methods, default `stripe`. If they don't, ask the user.

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

*v0.1.0 doesn't actually accept payments on `paid` invoices — for
refunds, record a payment with `amount_cents < 0` while the invoice
is still `open` or `uncollectible`. Refund-after-paid lands in v0.2.

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
- Use **ISO 4217** codes (`USD`, `EUR`, `GBP`, `JPY`, `CAD`…).
- The customer record has a `currency` preference — use it as the
  default on new invoices for that customer when the user doesn't
  specify.

## Tax

- Per-line `tax_rate_bps`. Basis points: `0` = no tax, `1000` =
  10.00%, `2000` = 20.00%, `725` = 7.25%.
- `tax_default_rate_bps` in the install config fills the gap when
  `invoices_create` / `invoices_add_line_item` don't set one. Many
  installs leave it at `0` and require explicit per-line tax.
- Totals roll up: each line's `amount_cents = round(quantity *
  unit_price_cents)`, line tax = `amount_cents * tax_rate_bps /
  10000` (integer division per line — keeps the displayed
  per-line subtotal consistent with the invoice total).

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
landed earlier (e.g. a wire that cleared yesterday).

If the payment fully covers the invoice, status flips to `paid`
automatically. Partial payments stay `open` and accumulate —
`amount_paid_cents` adds up across rows.

**Do not** pass `method="stripe"` to `payments_record`. v0.1.1's
reconciler owns those rows; calling this tool with `method=stripe`
is rejected.

## Don'ts

- Don't call `invoices_create(provider="stripe")` on v0.1.0.
- Don't call `payments_record(method="stripe")` ever.
- Don't render a PDF for a draft — finalize first so the file shows the real number.
- Don't try to set `status` directly via `customers_update`-style
  patches — the lifecycle tools own status.
- Don't void a paid invoice expecting a refund — record a payment
  with `amount_cents < 0` instead.
- Don't loop `invoices_create` for batches before subscriptions
  ship (v0.2). For repeating bills today, generate them one at a
  time and tell the user it's a manual cycle.
- Don't include sensitive data (card numbers, SSNs) in `notes` or
  `metadata`. Notes show in the dashboard panel; metadata is JSON
  the agent can read back.
- Don't use `confirm` rules as a soft gate — they're the gate.
  Ask before voiding. Ask before issuing a payment link in chat
  channels (v0.1.1+).

## When in doubt

Run `invoices_get(id=…)` before mutating an invoice. The `status`,
`provider`, `total_cents`, `amount_paid_cents`, and `audit_log`
fields are usually enough to make the right decision without
asking the user.
