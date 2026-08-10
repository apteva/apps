# Billing (v0.10.0)

Customers, invoices, and payments for Apteva agents and human teams.

## Current capabilities

- **Customers** with billing address, tax IDs, soft-delete + merge.
- **Invoices** with line items and an explicit lifecycle
  (draft → open → paid / void / uncollectible). The invoice issuer remains
  local; Stripe can process its outstanding balance through Checkout.
- **Payments and refunds** for Stripe, wire, cash, check, and other methods,
  with provider-ID idempotency and paid-invoice reopening after refunds.
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

See `migrations/001_init.sql` for the full schema and `main.go`'s
`MCPTools()` for the tool surface.

## Tests

Three tiers, like the other apps. Tier 1 runs on every commit; tier 2
on pre-merge CI; tier 3 nightly + before a release.

```bash
go test ./...                       # tier 1, ~330ms — pure + DB ops, in-process
go test -tags integration ./...     # tier 2, ~2s — real binary, real HTTP
apteva test ./scenarios/            # tier 3, ~3min — live agent + LLM
```

The suite includes unit, real-sidecar integration, and live-agent scenarios.
