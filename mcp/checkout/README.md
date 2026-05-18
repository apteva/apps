# Checkout (v0.1.0)

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
