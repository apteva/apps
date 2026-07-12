# Subscriptions

Generic recurring-commerce lifecycle for SaaS, physical subscriptions, and services.

Subscriptions owns recurrence and renewal cycles. It does not own access rights or fulfillment operations:

- Entitlements owns access.
- Orders owns physical shipments.
- Billing owns invoices and payments.

Each `subscription_cycle` can link to an invoice, order, and entitlement grant.
