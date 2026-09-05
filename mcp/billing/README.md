# Billing (v0.13.0)

Customers, invoices, and payments for Apteva agents and human teams.

## Current capabilities

- **Customers** with billing address, tax IDs, soft-delete + merge.
- **Invoices** with line items and an explicit lifecycle
  (draft → open → paid / void / uncollectible). The invoice issuer remains
  local; Stripe can process its outstanding balance through Checkout. An
  optional `accounting_date` records the invoice's posting date independently
  from its creation, finalization, due, and payment timestamps.
- **Payments and refunds** for Stripe, wire, cash, check, and other methods,
  with provider-ID idempotency, historical `received_at` support, authoritative
  invoice `paid_at`, overpayment credits, and explicit collection holds after refunds.
- **Automatic invoice collection** through a reusable saved payment method,
  with caller-stable idempotency, durable attempts, and webhook reconciliation.
- **Append-only audit log** per invoice for status transitions.
- **Stripe Checkout payment links** reconciled against persisted expected
  amount/currency/session records before invoice state changes. All Stripe
  calls use the bound integration; the platform owns webhook registration,
  encrypted signing-secret storage, and signature verification.
- **Reusable payment methods** through provider-hosted setup sessions.
- **PDF + print view** at `GET /invoices/{id}/pdf` (server-rendered
  via gofpdf — Helvetica, A4, no font embedding) and
  `GET /invoices/{id}/print` (self-contained HTML for browser-driven
  Save-as-PDF). Agents call `invoices_render_pdf` to get bytes back
  as base64, or with `save_to_storage=true` to push the file into
  the storage app via cross-app SDK call.
- **REST surface** at `/api/apps/billing/*` for the dashboard panel.
- **Billing panel** (React + Tailwind) at `slot: project.page`,
  plus inline `invoice-card` and `customer-card` components for
  chat attachments.
- **Two install scopes**:
  - `project` — one install per Apteva project, physical isolation.
  - `global` — one install across all projects, isolation by
    `project_id` partition column.

## Deferred

- Subscriptions / recurring billing.
- Stripe Tax / Avalara.
- Multi-currency on a single invoice.
- Quotes.
- Reporting (MRR / ARR / aging).

## Local development

```bash
cd mcp/billing
go build .
APTEVA_PROJECT_ID=test ./billing     # smoke run; binds to :8080
curl http://localhost:8080/health
```

See `migrations/001_init.sql` for the full schema and `tool_schema.go` for the tool surface.

## Tests

Three tiers, like the other apps. Tier 1 runs on every commit; tier 2
on pre-merge CI; tier 3 nightly + before a release.

```bash
go test ./...                       # tier 1, ~330ms — pure + DB ops, in-process
go test -tags integration ./...     # tier 2, ~2s — real binary, real HTTP
apteva test ./scenarios/            # tier 3, ~3min — live agent + LLM
```

The suite includes unit, real-sidecar integration, and live-agent scenarios.

## Integrity and recovery

Stripe create requests are persisted before dispatch with an immutable request and a
namespaced provider idempotency key. Only one provider payment operation can hold an
invoice at a time. A timeout remains unresolved until reconciliation; it does not
mean the charge failed. A worker retries unsent claims, known provider operations,
verified webhook inbox entries, and event deliveries every 30 seconds. Unknown
operations older than 23 hours require review instead of a fresh charge. Inspect
`billing_reconciliation_status` for pending operations and delivery counts.

Use the same caller key and parameters when retrying collection, payment sessions,
or refunds. Refund requests reserve the source payment's remaining refundable
balance. Refunds hold collection by default; use `reopen_invoice=true` only when
the refunded amount should remain owed. A later commercial refund keeps the hold,
even if an older reopen event is replayed. `invoices_resume_collection` explicitly
releases a hold; `invoices_cancel_payment` verifies and cancels an active provider
object; `invoices_delete` deletes an unissued draft.

Invoice finalization captures issuer, customer, and document fields for PDF/print.
Zero-total documents settle immediately; negative-total documents require a separate
credit adjustment. Tax treatment is explicit (`standard`, `exempt`, or
`reverse_charge`); nonstandard treatments require zero tax at finalization. Currency
precedence is explicit input, consistent catalog currency, customer preference,
then installation default. Catalog conflicts are rejected. Totals use `by_currency`;
legacy scalar totals are only present for a single currency.

Customer/invoice/payment searches accept `limit` and `offset`, with `has_more`.
Invoice search runs on the server. `invoices_history` and `GET /invoices/:id/history`
page through payments, audit entries, and refund requests, each with its own
continuation flag. Offset pages are stable for an unchanged result set; concurrent
inserts can shift later pages.

Ledger events are written to a transactional outbox with stable `event_id` values.
`payment.recorded` and `invoice.refunded` describe money movements; `invoice.paid`
is a settlement transition. Delivery is acknowledged and at least once: downstream
consumers must deduplicate `event_id`. Customer notifications remain ordinary SDK
notifications. Independent reads use the SDK read pool; catalog lookups are
memoized per request, UI catalog requests expire after 30 seconds, and event-driven
refreshes coalesce over 150 ms.

## Upgrade and verification notes

Migration 011 flags duplicate normalized emails using `email_conflict` and
`metadata.billing_email_conflict`. It preserves customer IDs and financial history;
resolve these identities deliberately. Pending pre-upgrade payment paths acquire
legacy locks until reconciliation. Existing issued invoices receive snapshots
marked `migration-current-identity`: prior names/addresses already overwritten
cannot be reconstructed from the current database.

Billing v0.13.0 pins app-sdk v0.75.0, which makes schema changes and their
receipts atomic, including legacy transaction-wrapped migrations. It requires
Apteva server v0.27.4 or newer for the Stripe refund/cancellation/recovery catalog
contracts. Release notes describe the schema and event-consumer compatibility
changes.

Additional checks:

```sh
go test -race ./...
go vet ./...
go test -run '^$' -bench BenchmarkInvoiceSearch10000 -benchmem
cd tests/browser
bun install --frozen-lockfile
bun test panel.test.tsx
```

Component tests use reversed network responses and mocked API requests. The local
sidecar and Stripe executor contract tests do not create live Stripe transactions.
PDF pagination covers 80 line items; built-in Helvetica remains limited to CP-1252.
Use the HTML print surface for names requiring other scripts. Catalog price-picker
coverage remains bounded by the Catalog app's existing list/detail API limits.
