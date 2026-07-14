# Catalog (v0.2.0)

Products, prices, and discounts — the source of truth for what your business sells.

Lower-layer commerce app. Modelled after Stripe's `Product` + `Price` API split: one product, many prices (different currencies, intervals, tiers). Other apps reference catalog by ID and snapshot fields they need into their own data; catalog itself calls nothing.

## What's in v0.2.0

- **Products** — typed shell (`one_time` | `recurring` | `service`) with name, slug, description, category, tax_category, image, color, soft-delete.
- **Prices** — `unit_amount_cents` + ISO 4217 currency, optional recurrence (`interval`, `interval_count`, `trial_days`), `active` flag, `tax_inclusive` flag.
- **Discounts** — percentage, fixed-amount, and final-price overrides scoped to all Catalog items or selected products/prices.
- **Discount duration** — `once`, `repeating` for a fixed number of caller-owned cycles, or `forever`.
- **Eligibility** — validity windows, minimum subtotal, global limits, per-customer limits, and code-specific limits.
- **Codes** — project-unique, case-insensitive codes with independent activation and limits.
- **Safe checkout lifecycle** — quote, atomic/idempotent reservation, redemption, and release. Expired reservations stop consuming capacity.
- **23 MCP tools** — products, prices, discount definitions, codes, and discount application lifecycle.
- **REST surface** at `/api/apps/catalog/*` for the dashboard panel.
- **Catalog panel** — two-column products list + detail with embedded prices section.
- **Chat-attachable** `product-card` and `price-card` components.
- **Forward-compat Stripe columns** (`external_id` on both tables) for the future mirror.

## Design rules

- **Prices are effectively immutable** for financial fields after create. `unit_amount_cents`, `currency`, `interval` cannot change — to change a price, create a new one and archive the old. Same rule Stripe enforces. Keeps historical invoice snapshots sound.
- **Soft-delete only** — never hard-delete a product or price. Existing invoice/subscription references must keep resolving forever.
- **Per-project partition** — same as billing; works for both project and global install scopes.
- **Immutable discount applications** — quote/reserve returns a complete application snapshot. Changing a campaign later does not alter an existing purchase or subscription.
- **Opaque consumers** — `customer_ref` and `context_ref` are caller-owned strings. Catalog does not depend on SaaS, Commerce, Checkout, Subscriptions, or Billing.

## Discount flow

```text
catalog_discounts_quote
  -> catalog_discounts_reserve(idempotency_key)
  -> caller creates purchase or subscription from reservation.application
  -> catalog_discounts_redeem
```

Call `catalog_discounts_release` when the purchase or subscription is abandoned. `price_override` is a per-unit final price; percentage and amount discounts apply to the quoted subtotal. A repeating discount expresses a number of billing cycles, not a calendar duration. The caller owns cycle application.

## Cross-app integration

Catalog declares **no app deps** — it's self-contained. Downstream apps call it:

```go
ctx.PlatformAPI().CallAppResult("catalog", "catalog_prices_get",
    map[string]any{"id": priceID}, &price)
```

Billing wires this when an `invoices_create` request includes a `price_id` — billing snapshots `unit_amount_cents`, `currency`, and `nickname` into the line item. Free-form line items (no `price_id`) keep working unchanged — catalog is an optional dep.

## Local development

```bash
cd mcp/catalog
go build .
APTEVA_PROJECT_ID=test ./catalog     # smoke run; binds to :8080
curl http://localhost:8080/health
```

See `migrations/001_init.sql` for the full schema and `main.go`'s `MCPTools()` for the tool surface.

## Tests

```bash
go test ./...                       # tier 1 — pure + DB ops, in-process
```
