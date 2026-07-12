# Trial conversion orchestration handoff

Subscriptions v0.3.1 deliberately stops at the subscription boundary. At the end of a collectible trial it creates a pending subscription cycle, moves the subscription to `past_due`, and publishes `subscription.cycle_due`.

An external commerce workflow—SaaS, Checkout, or another product-level orchestrator—must own the commercial conversion:

1. Consume `subscription.cycle_due`.
2. Resolve or create the Billing customer using the product customer's identity.
3. Call `subscriptions_invoice_prepare` for the cycle period.
4. Create the Billing invoice and store its ID with `subscription_cycles_update`.
5. Attempt collection or create the appropriate payment/setup flow.
6. On payment, mark the cycle `paid` and call `subscriptions_update_status` with `active`.
7. On collection failure, leave the cycle pending or mark it failed and keep the subscription `past_due` according to product policy.

The orchestrator must use durable, deterministic idempotency for customer, invoice, charge, and payment-link operations. Billing still needs to honor idempotency keys on object-creating and provider-calling tools; that requirement is independent of Subscriptions.

## `subscription.cycle_due` payload

The event includes:

- `subscription_id`
- `cycle_id`
- generic `customer_id` and `customer_email`
- `kind`, `source`, and `source_ref`
- `currency` and `total_cents`
- `period_start` and `period_end`
- subscription metadata

The event intentionally contains no Billing customer, invoice, payment, or provider-session state.
