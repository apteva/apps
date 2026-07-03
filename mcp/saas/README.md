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
