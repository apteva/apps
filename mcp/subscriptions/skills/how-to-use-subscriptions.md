# Subscriptions

Use Subscriptions for recurring lifecycle state: active, trialing, past_due, paused, cancelled, and renewal periods.

Kinds:

- `saas`: successful cycles should grant or extend entitlements.
- `physical`: successful cycles should create Orders records.
- `service`: successful cycles may only create invoices or tasks.

Do not store feature access rules here. Use the Entitlements app for access checks.

For discounts, an external source such as Catalog must first decide eligibility.
Pass its immutable application snapshot through `subscriptions_create.discounts`
or `subscription_discounts_create`. Subscriptions owns cycle application only:
`once` applies to the first eligible cycle, `repeating` to the configured number
of cycles, and `forever` until cancellation. Pass `cycle_id` when preparing an
existing cycle so invoice retries resolve the same application deterministically.
