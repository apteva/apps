# Subscriptions

Generic recurring-commerce lifecycle for SaaS, physical subscriptions, and services.

Subscriptions owns recurrence and renewal cycles. It does not own access rights or fulfillment operations:

- Entitlements owns access.
- Orders owns physical shipments.
- Billing owns invoices and payments.

Each `subscription_cycle` can link to an invoice, order, and entitlement grant.

## Trial end ownership

Subscriptions owns the recurring-domain transition at trial end:

- claim the due transition per project;
- create exactly one pending cycle for the first paid period;
- apply `trial_end_behavior` (`collect`, `pause`, or `end`);
- publish `subscription.cycle_due` when external collection is needed.

Subscriptions does not create Billing customers, initiate charges, create
payment links, or reconcile payment-provider sessions. A commerce workflow can
consume `subscription.cycle_due`, use the existing invoice preparation tool,
call Billing, and update the cycle through `subscription_cycles_update`.
