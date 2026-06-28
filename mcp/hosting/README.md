# Hosting

V1 control plane for managed Apteva hosting.

Hosting intentionally stays above the generic `containers` runtime:

- Hosting owns customers, plans, tenants, default hostnames, lifecycle
  policy, and usage.
- Containers owns Docker workloads, ports, logs, health, resources, and
  volumes.
- Billing, Subscriptions, and Entitlements are optional in v1. Free
  plans work without any billing objects.

## v1 scope

- Create/find hosting customers.
- Seed and list free/dev/pro plans.
- Provision one Apteva container per hosted tenant through
  `containers_run`.
- Expose the tenant through server-native ingress using a generated
  default hostname.
- Suspend, resume, restart, delete, fetch logs, and probe health.
- Record and query basic usage events.
- Ship an operator panel for provisioning and lifecycle controls.

## Deferred

- Custom domains.
- Tenant bootstrap API integration.
- Backup/restore.
- Image upgrade/rollback.
- Multi-container templates such as WordPress.
