# SaaS

Shared SaaS access control for Apteva apps.

SaaS generalizes the access, lifecycle, and live-usage parts that worked
well in Hosting without assuming every sold product is a container. Apps
such as CRM and Storage stay installed once; SaaS sells access to them
through Auth organizations, Entitlement grants, plan limits, and live
usage gauges.

## Boundaries

- Catalog owns product and price records.
- Billing owns invoices, customers, and payment links.
- Subscriptions owns recurring lifecycle state.
- Auth owns organizations, users, clients, and sessions.
- Entitlements owns grants and limits.
- SaaS owns plan composition, account state, and live usage gauges.
- Target apps own their own domain data and expose ordinary tools.

SaaS does not install apps, run containers, or expose domains. Hosting can
become a target later through its existing tools, but SaaS does not modify
or depend on Hosting.

SaaS declares dynamic app calls so operators can meter whichever installed
apps they configure in usage sources. Target apps are still ordinary apps;
they do not need SaaS-specific hooks.

## Checkout

`saas_checkout_create` is the paid-sale entrypoint. For a paid plan it:

- creates or links the Billing customer;
- resolves the Catalog price;
- creates an idempotent Subscription and durable checkout record;
- creates the first subscription cycle and prepares its lines through
  Subscriptions before asking Billing to create the invoice;
- returns a payment link and leaves the account `past_due`, or returns a
  setup link for a card-required trial without creating an invoice;
- grants Entitlements during provisioning, while access checks only allow
  `active` accounts.

This keeps the sales flow inside SaaS without moving invoices, payments,
subscriptions, auth, or product data out of their owner apps.

Paid plans with `trial_days > 0` and
`trial_requires_payment_method: false` in plan or Catalog price metadata
start as no-card trials. SaaS creates a `trialing` Subscription, activates
the account, runs fulfillment immediately, and does not call Billing until
payment is required at trial end.

When Subscriptions publishes `subscription.cycle_due`, SaaS prepares the
period lines through Subscriptions, creates and finalizes the Billing invoice,
links the invoice to the subscription cycle, and creates its payment link. The
operation is checkpointed by subscription cycle so delivery retries resume at
the last completed step. When Billing publishes `invoice.paid`, SaaS marks the
cycle paid and asks Subscriptions to transition to `active`; the resulting
`subscription.active` event activates and fulfills the SaaS account.

`saas_checkout_get` returns the durable status and current payment/setup URL.
Administrative manual payments use the separately permission-gated
`saas_checkout_mark_paid` tool. Public checkout cannot override plan pricing,
trial policy, provider, billing/auth IDs, lifecycle dates, or payment state.
When Billing publishes `invoice.paid` during an administrative payment, the
tool and event handler share the same idempotent operation: the loser waits for
the winner and returns the completed checkout instead of a false duplicate
payment error.

SaaS exposes `saas.read`, `saas.checkout`, and `saas.admin` permissions. Plan,
fulfillment, account-management, usage-write, and manual-payment tools require
`saas.admin`; customer checkout requires `saas.checkout`.

## Fulfillment Actions

Plans can define generic lifecycle actions. SaaS calls the configured
app/tool, expands `{{...}}` templates from the account/customer/plan
context, and stores selected output fields in account metadata. Actions
default to `once_per_transition`; set `execution_policy` to `always` only
for operations that are explicitly safe to repeat. SaaS reserves each run
before calling the target and passes a deterministic `idempotency_key`.

```json
{
  "plan_key": "container-pro",
  "event": "account_active",
  "app_name": "containers",
  "tool_name": "containers_create",
  "args": {
    "name": "saas-{{account.slug}}",
    "image": "nginx:alpine",
    "env": {
      "SAAS_ACCOUNT_ID": "{{account.id}}"
    }
  },
  "store": {
    "metadata.workload_id": "workload.id"
  }
}
```

Later lifecycle actions can use stored metadata:

```json
{
  "plan_key": "container-pro",
  "event": "account_past_due",
  "app_name": "containers",
  "tool_name": "containers_stop",
  "args": {
    "workload_id": "{{account.metadata.workload_id}}"
  }
}
```

Supported lifecycle events are `account_active`, `account_past_due`,
`account_suspended`, `account_resumed`, and `account_cancelled`.

## Live Usage

Plan usage sources point at app tools and tell SaaS where to read the
quantity. For CRM contact count, SaaS can call the existing
`contacts_search` tool and read `total`:

```json
{
  "plan_key": "crm-pro",
  "app_name": "crm",
  "tool_name": "contacts_search",
  "feature_key": "crm:contacts",
  "read_path": "total",
  "call_args": { "limit": 1, "offset": 0 }
}
```

For apps that already expose gauges, the compatibility format is still:

```json
{
  "usage": [
    { "feature_key": "crm:contacts", "quantity": 812 },
    { "feature_key": "crm:seats", "quantity": 4 }
  ]
}
```

`saas_usage_sync` stores those values as current snapshots. It does not
write them to Entitlements' additive `usage_record` stream, because live
gauges such as contact count or storage size can go down.

Each source is synchronized independently with bounded concurrency and a
timeout. Successful complete responses replace the source's prior
generation; failed responses preserve the last good gauges and update the
source failure state. Access checks return `usage_unknown` when a configured
source has never succeeded or is older than its freshness threshold.

SaaS persists quota state per account and feature. The default warning
threshold is 80%; set `metadata.warning_threshold_percent` on a plan limit
to use another value from 1 through 99. Successful pulled or manually
recorded usage updates emit transition-only events:

- `saas.quota.approaching` when usage enters the warning range.
- `saas.quota.reached` when usage equals the limit.
- `saas.quota.exceeded` when usage is above the limit.
- `saas.quota.recovered` when usage moves down to a less severe state.

Repeated usage updates in the same state do not emit duplicate events. Event
payloads include the account, customer, plan, feature, quantity, limit,
percentage, threshold, and previous/current state. Quotas remain scoped to a
SaaS account (tenant); `auth_user_id` identifies its owner but does not make
the quota user-specific.

## Account and Billing Reporting

`saas_account_get` and `saas_account_list` return the account's nested SaaS
customer and a generic Billing summary. The account and customer retain their
own `created_at` dates. Billing summaries include first and last positive
payment dates, payment and paid-invoice counts, net totals grouped by currency,
the latest linked invoice, and projection freshness/completeness.

Account listing supports `customer_email`, `plan_key`, `status`,
`created_before`, `created_after`, `has_paid`, `paid_since`, `paid_until`,
`last_paid_before`, and `last_paid_after`, plus `limit` and `offset`. Date
filters are RFC3339. For example, callers identify accounts older than two
calendar months by calculating that boundary and passing it as
`created_before`.

Billing remains authoritative. SaaS already consumes Billing invoice lifecycle
events; on a linked invoice event it calls `billing.invoices_get` and replaces
only its local query projection for that invoice and its payments. Listing is
therefore a bounded local query rather than an N+1 set of Billing calls.
`saas_billing_sync` repairs or backfills linked invoices, and the checkout
recovery worker incrementally backfills projections missing after an upgrade.
`billing_sync_pending` and each summary's `data_complete` flag expose unfinished
backfill explicitly.

An `invoice.paid` event activates access only after Billing confirms that the
invoice status is actually `paid`. Partial payments update the reporting
projection but leave the subscription and account payment state unchanged.
