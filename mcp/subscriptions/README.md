# Subscriptions

Generic recurring-commerce lifecycle for SaaS, physical subscriptions, and services.

Subscriptions owns recurrence and renewal cycles. It does not own access rights or fulfillment operations:

- Entitlements owns access.
- Orders owns physical shipments.
- Billing owns invoices and payments.

Each `subscription_cycle` can link to an invoice, order, and entitlement grant.

The lifecycle worker claims both trial endings and active renewals, creates one
pending cycle per due period, and emits `subscription.cycle_due` idempotently.
Product apps decide how that due cycle is fulfilled or collected.

## Trial end ownership

Subscriptions owns the recurring-domain transition at trial end:

- claim the due transition per project;
- create exactly one pending cycle for the first paid period;
- apply `trial_end_behavior` (`collect`, `pause`, or `end`);
- publish `subscription.cycle_due` when external collection is needed.
- persist immutable discount applications decided by Catalog or another eligibility source;
- apply `once`, `repeating`, and `forever` discounts deterministically by cycle number;
- preserve historical invoice results when a discount is cancelled.

Subscriptions does not create Billing customers, initiate charges, create
payment links, or reconcile payment-provider sessions. A commerce workflow can
consume `subscription.cycle_due`, use the existing invoice preparation tool,
call Billing, and update the cycle through `subscription_cycles_update`.

## Active renewals

The lifecycle worker also claims active subscriptions whose
`next_renewal_at` is due. It creates one pending cycle for the next period and
publishes `subscription.cycle_due` for the external commerce workflow.
Collection success advances the period through `subscriptions_update_status`;
an unpaid cycle remains idempotently pending and is not duplicated on later
worker runs. Cancellation at period end transitions directly to `ended`
without creating another cycle.

## Discount ownership

Discount eligibility remains outside Subscriptions. An orchestrator reserves a
discount in Catalog, passes Catalog's immutable `application` snapshot to
`subscriptions_create` or `subscription_discounts_create`, and then redeems the
reservation. Subscriptions applies the snapshot by cycle number and never calls
Catalog or Billing while doing so.

## Item changes

`subscription_changes_create` replaces the recurring item set either
immediately or at the next cycle. Each change is durable and idempotent, keeps
the old and new item snapshots, calculates generic proration, and versions item
cycle ranges so historical invoice retries continue to use the original
prices. Preserved discounts follow the matching replacement item and proration
uses the effective net recurring amounts.

An immediate change created with `defer_apply: true` remains
`awaiting_approval`; the due worker will not apply it. An external orchestrator
uses `subscription_changes_apply` after its payment or policy gate succeeds.
Next-cycle changes remain automatic and publish `subscription.change.applied`
when they become effective. Subscriptions never creates an invoice, payment,
entitlement, or fulfillment operation for a change.
