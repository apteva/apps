# How to use the Checkout app

The Checkout app is the **cart + buy flow** that sits between a storefront and your billing ledger. It owns short-lived shopping state and turns it into a real billing invoice when the customer commits.

## When to use this app vs. billing directly

- **Use checkout** when there's a customer-facing flow — a storefront page, a quote you want them to accept, an order they're assembling themselves.
- **Use billing directly** for one-off invoices you create manually for a known customer (consulting, retainers, custom B2B). No cart, no session, just `invoices_create`.

If you're confused: the cart is for the *buyer*. The invoice is for the *seller's books*. Checkout produces the invoice when the buyer commits.

## Mental model

```
Cart (open) ─► Cart (checkout / locked) ─► Cart (converted)
                                                │
                                                └─► Billing invoice
```

A cart goes through three states the agent should know:

- **open**: items can be added/changed; this is the shopping phase.
- **checkout**: locked because a checkout_session is awaiting payment. Mutations to items are rejected.
- **converted**: terminal. The cart is linked to a billing invoice. The agent should reference the invoice from here on.

## Carts: guest vs. logged-in

- **Guest** carts use a `session_token` (server-issued opaque string). The storefront stores it in a cookie. Multiple anonymous shoppers = multiple carts.
- **Logged-in** carts use a `customer_id`. Persistent across devices. v0.1.0 doesn't ship a login flow — you'd integrate `auth` for that — but the schema is ready.

## Price snapshots

When `cart_add_item` is called with a `price_id`, checkout fetches the catalog price and **snapshots** the description, amount, and currency onto `cart_items`. If the catalog price changes 10 minutes later, the buyer's cart total doesn't shift.

The snapshot becomes the line item on the resulting billing invoice. The catalog FK is preserved for analytics.

## The pay flow (v0.1.0)

```
checkout_pay(session_id)
   ▼
   1. Calls billing.customers_upsert_by_email(email, defaults)
   2. Calls billing.invoices_create(customer_id, line_items)
         line_items are snapshots from cart_items
   3. Calls billing.invoices_finalize(invoice_id)  ← mints number
   4. session.status = 'awaiting_payment'
   5. cart.status   = 'converted'
   6. Returns { invoice_id, invoice_number }
```

The buyer now owes the amount on a real billing invoice. **Payment is recorded manually** in v0.1.0 — bank transfer arrives → user goes to billing's invoice detail → "Record payment". When `amount_paid_cents >= total_cents`, billing transitions the invoice to `paid` automatically.

## v0.2.0 will add Stripe

Schema already supports it (`provider`, `provider_session_id`, `processed_event_ids`). The pay flow will branch:

- `provider='manual'`: same as v0.1.0
- `provider='stripe'`: creates Stripe Checkout Session, returns redirect URL, the webhook auto-records payment

No data migration needed when v0.2.0 lands.

## Triggering this skill

This skill loads when the agent touches: tools matching `cart_*` or `checkout_*`, the Checkout panel, or chat mentions of "cart", "checkout", "order", "buy", "convert (a cart)".
