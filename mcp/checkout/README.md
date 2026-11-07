# Checkout v0.4.0

## Order bumps

Configure **Checkout → Order bumps** to offer optional, unchecked add-ons on
any selling app's checkout. Rules match the original Catalog product or price,
optionally restricted by `source_app` and `context_ref` on the server-created
cart. Each rule references an active, fixed, one-time Catalog price and names
the app and reference responsible for delivery. Use a separate Catalog price
for a special bump price. The amount is never supplied by the buyer.

For Community, set `source_app=community`, `context_ref=<community ID>`,
`fulfillment_app=community`, and `fulfillment_ref=<course space ID>`. The add-on
price must reference the same Catalog product as an active paid course in
that community; it may differ from the normal course price. Other apps consume
`checkout.paid` and `checkout.refunded`, fetch `checkout_receipt`, and fulfill or
revoke only their own items. The receipt includes the verified buyer email and
selling-app identity metadata. Deduplicate delivery by session ID and item ID.

The buyer adapter calls `checkout_bumps_get` and `checkout_bump_set`, then
`checkout_prepare` for updated deferred Stripe configuration. Selection is
idempotent, quantity is one, products already in the cart are excluded, and
currencies must match. Rules can be enabled, ordered and scheduled. Recovery
retains selections. A started session can change selections after sign-in;
`checkout_pay` freezes the invoice. Its optional `expected_total_cents` and
`expected_price_ids` reject changes from another browser tab before payment.
Invoiced checkouts retain their frozen offer even if an operator changes rules.

The shared `ui/OrderBumps.tsx` component renders buyer offers; selling apps keep
their own authenticated/scoped API adapters. Generic Checkout tools and admin
HTTP routes are for operators and trusted apps, not delegated buyer access.

Checkout converts all selected items into one Billing invoice, carrying item,
offer, Catalog and fulfillment references on each line. Main-product checkout
discounts are allocated only to original cart items, not add-ons. One-time
bumps can accompany an initial membership purchase; the selling app must create
the recurring subscription from its primary price alone.

Use **Sessions → Purchased items → Refund this item** or
`checkout_item_refund` to refund a specific line. Partial amounts and durable
idempotency keys are supported. Access is revoked only after an authoritative
refund succeeds; other items retain their paid balance. A full invoice refund
refunds every item. Partial refunds initiated externally have no reliable line
identity: receipts flag `unallocated_refund_cents`, retain existing access and
block additional item refunds until accounting is reviewed. Do not guess which
product an external partial refund belongs to.

Order-bump reporting counts unique eligible checkout sessions, sessions that
selected an offer, paid conversions and gross paid revenue per offer. Changes
in cart selection do not count as sales. Recurring bump subscriptions are not
supported in this version.

Validation: `GOWORK=off go test -race ./...`, `GOWORK=off go vet ./...`, and
`GOWORK=off go build .`. SDK dependency: v0.110.0.

Cart + checkout flow. Sits between the storefront (rendered by `content`) and the order ledger (`billing`).

## What's in v0.1.0

- **Carts**: long-lived basket state; one per `session_token` (guest) or `customer_id` (logged in).
- **Cart items**: catalog price references with snapshot fields so totals stay stable across price changes.
- **Checkout sessions**: one attempt to pay for a cart. Captures email, name, addresses.
- **Conversion**: pay flow calls `billing.invoices_create` + `billing.invoices_finalize` to mint a real invoice. v0.1.0 is the *manual* path — the customer/agent records the payment in billing separately.
- **8 MCP tools**: `cart_*` and `checkout_*`.
- **REST surface** at `/api/apps/checkout/*` for storefront + dashboard panel.
- **Admin panel** with Carts and Sessions tabs.

## What's NOT in v0.1.0

- **Stripe Checkout Session** — schema is forward-compat (`provider`, `provider_session_id`, `processed_event_ids`) but the actual Stripe call lands in checkout v0.2.0 alongside billing v0.8.0's Stripe integration.
- **Public storefront blocks** — separate work in the `content` app (add-to-cart, cart-summary, checkout-page).
- **Discounts / coupons** — deferred.
- **Tax computation** — deferred (carts compute subtotal; tax stays 0 until the tax engine lands).
- **Abandoned-cart emails** — deferred (events fire; consumer not built).

## Architecture

```
                           ┌─────────────────┐
                           │ content (NEW)   │
                           │ add-to-cart     │
                           │ cart-summary    │
                           │ checkout-page   │
                           └────────┬────────┘
                                    │ public REST
                                    ▼
        ┌───────────────────────────────────────────┐
        │              checkout (this app)          │
        │ carts · cart_items · checkout_sessions    │
        └─────────┬─────────────────────┬───────────┘
                  │                     │
       reads      │                     │     writes invoice + payment
       prices ────┘                     └────►
                  ▼                              ▼
            ┌──────────┐                  ┌──────────┐
            │ catalog  │                  │ billing  │
            └──────────┘                  └──────────┘
```

## Lifecycle

```
Cart:    open ─► checkout (locked) ─► converted (terminal)
                                  ╲
                                   ─► open (if session cancelled / expired)
              ╲
               ─► abandoned (TTL hit; terminal)

Session: started ─► awaiting_payment ─► paid (terminal)
                                    ╲
                                     ─► cancelled | expired
```

## Local development

```bash
cd mcp/checkout
go build .
APTEVA_PROJECT_ID=test ./checkout
curl http://localhost:8080/health
```

## Tests

```bash
go test ./...
```
