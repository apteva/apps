# How to use the Catalog app

The Catalog app is the source of truth for **what your business sells**: SaaS plans, ecommerce products, services. Other apps (billing, future subscriptions/checkout) reference catalog entries and snapshot fields into their own data.

## Mental model

Two entities:

- **Product** — the *thing* (an Apteva SaaS plan, a hosting service, a one-off consulting engagement). One product.
- **Price** — *how much it costs, in what currency, on what cadence*. A product can have many prices: a monthly EUR price, a yearly EUR price, a monthly USD price, etc.

This is the same shape Stripe uses. The split exists because the same product is often sold at multiple price points (currency, interval, tier), and tying price to product 1:1 would force duplicates.

## When to create a Product vs. just an ad-hoc invoice line

- **Recurring or repeated sales** → create a Product. SaaS subscriptions, hosting plans, "Consulting — 1 hour" — anything you'll bill more than once should be a Product with a Price.
- **One-off custom work, refunds, manual adjustments** → don't create a Product. Use a free-form invoice line item (`description` + `unit_price_cents`, no `price_id`). Catalog is opt-in.

## Price immutability

Once a Price is created, its **financial fields are locked**: `unit_amount_cents`, `currency`, `interval`. To change a price, create a *new* Price under the same Product and archive the old one. Existing invoices that already snapshot the old price keep their values forever (that's the customer's record). This matches Stripe's rule.

What you *can* change on a Price: nickname, `active` flag, `tax_inclusive`, metadata.

## Archive vs delete

Always archive, never hard-delete. An archived Product / Price stays in the DB so invoices / subscriptions referencing it keep resolving. The picker hides archived entries from new sales.

## Slugs

Optional URL-safe handle (`apteva-saas`, `pro-tier`). Unique per project, on non-archived rows. If you re-create a product with the same slug after archiving the old one, that's fine — the partial unique index allows it.

## Cross-currency pricing

To sell the same product in EUR and USD, create **two prices** under the same product. The billing flow snapshots whichever price ID the invoice references — totals stay clean per currency.

## Tax categories

`tax_category` on a Product is a *label*, not a resolved rate. The actual rate resolution happens at checkout/invoice time when the tax engine (Stripe Tax or our own) is wired. Today the label is informational.

## Triggering this skill

This skill loads when the agent touches anything catalog-related: tools matching `catalog_*`, panel work in the Catalog tab, or chat mentions of "product", "price", "plan", "subscription tier", "SKU".
