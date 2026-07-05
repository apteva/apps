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
- creates the Subscription and first invoice;
- either records a manual payment and activates the SaaS account, or
  returns a payment/setup link and leaves the account `past_due`;
- grants Entitlements during provisioning, while access checks only allow
  `active` accounts.

This keeps the sales flow inside SaaS without moving invoices, payments,
subscriptions, auth, or product data out of their owner apps.

Paid plans with `trial_days > 0` and
`trial_requires_payment_method: false` in plan or Catalog price metadata
start as no-card trials. SaaS creates a `trialing` Subscription, activates
the account, runs fulfillment immediately, and does not call Billing until
payment is required at trial end.

## Fulfillment Actions

Plans can define generic lifecycle actions. SaaS calls the configured
app/tool, expands `{{...}}` templates from the account/customer/plan
context, and stores selected output fields in account metadata.

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
