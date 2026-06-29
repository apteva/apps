# Subscriptions

Use Subscriptions for recurring lifecycle state: active, trialing, past_due, paused, cancelled, and renewal periods.

Kinds:

- `saas`: successful cycles should grant or extend entitlements.
- `physical`: successful cycles should create Orders records.
- `service`: successful cycles may only create invoices or tasks.

Do not store feature access rules here. Use the Entitlements app for access checks.
