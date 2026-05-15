# Billing (v0.4.3)

Customers, invoices, and payments for Apteva agents and human teams.

## What's in v0.1.0

- **Customers** with billing address, tax IDs, soft-delete + merge.
- **Invoices** with line items and an explicit lifecycle
  (draft → open → paid / void / uncollectible). Per-invoice
  `provider` field (`local` only in v0.1.0; `stripe` lands in v0.1.1)
  set at create time and frozen for the rest of the row's life.
- **Payments** for non-Stripe methods — wire, cash, check, other.
- **Append-only audit log** per invoice for status transitions.
- **15 MCP tools** covering customer CRUD + invoice lifecycle +
  payment recording + PDF/print rendering.
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

## What's deferred to v0.1.1+

- **Stripe provider** — `requires.integrations.stripe` wiring,
  push-at-finalize, periodic reconciler. `invoices_get_payment_link`
  + `customers_get_payment_methods` tools.
- **Webhooks** — strictly additive on the v0.1 schema (one new
  table, one new manifest field). v0.1.0 already has the unique
  index that will make webhook + reconciler writes idempotent.

## What's deferred further (v0.2+)

- Subscriptions / recurring billing.
- Stripe Tax / Avalara.
- Multi-currency on a single invoice.
- Refunds beyond `payments_record(amount<0)`.
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

Counts today: 48 tier 1 tests · 9 tier 2 tests · 5 tier 3 scenarios.
