# Subscriptions

Generic recurring-commerce lifecycle for SaaS, physical subscriptions, and services.

Subscriptions owns recurrence and renewal cycles. It does not own access rights or fulfillment operations:

- Entitlements owns access.
- Orders owns physical shipments.
- Billing owns invoices and payments.

Each `subscription_cycle` can link to an invoice, order, and entitlement grant.

## Paid trial conversion

Expired trials are claimed and processed per project in bounded batches. For
`trial_end_behavior: collect`, Subscriptions lazily creates or links the Billing
customer, creates the first paid-period invoice and cycle, and requests a
payment link. The subscription becomes `past_due` with
`collection_status: requires_payment` until Billing emits `invoice.paid`, then
it becomes `active` and the cycle becomes paid.

Collection controls on `subscriptions_create`:

- `billing_customer_id`: Billing-owned customer reference. Legacy rows fall
  back to `customer_id`.
- `collection_method`: `invoice`, `auto_charge`, `manual`, or `none`.
- `trial_end_behavior`: `collect`, `pause`, or `end`.

Lifecycle attempts are durable and retried independently. Billing integration
failures do not stop other subscriptions in the worker batch. See
`BILLING_HANDOFF.md` for downstream idempotency work Billing must implement to
close the external-call crash window completely.
