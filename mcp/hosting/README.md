# Hosting

Runs tenants. A tenant is one thing running for one buyer: a container, a
machine from a pool, or a machine created on demand. Hosting does not sell
anything. Prices, checkout, invoices and subscriptions belong to the SaaS app
(or any other caller), which calls Hosting's tools.

Dependencies, both optional:

- `containers` for container tenants and workloads on machines.
- `instances` for machines (pool hosts and on-demand servers).

Hosting subscribes to no events and calls no billing app.

## Model

- **Product**: what runs (Apteva, a Docker image, WordPress, SSH access).
- **Profile**: where it runs. `backend` is `container`, `machine_pool`
  (needs `pool_key`) or `machine_on_demand` (needs `settings.provider` and
  `settings.server_type`).
- **Pool**: a set of Instances hosts of one platform (`macos` or `linux`).
  New pools start with execution switched off.
- **Machine**: one Instances host in a pool. States: `quarantined`,
  `preparing`, `available`, `reserved`, `assigned`, `suspended`, `cleaning`,
  `maintenance`. A machine is for sale only when it is available, online,
  validated and its pool allows execution.
- **Tenant**: keyed by the caller's `external_ref`. Creating it twice returns
  the same tenant; once released, the reference can be used again.
- **Run** (operation): the ordered steps of a machine change (provision,
  suspend, resume, release, reinstall, inspect, clean). Runs are leased,
  ordered per tenant and machine, and a failed step quarantines the machine.
  A failed run is retried only with `reviewed: true`.

## How a caller uses it

```
hosting_availability            profile → for_sale, free machines
hosting_reservation_create      hold a pool machine during checkout
hosting_tenant_create           external_ref, profile, owner_email, ssh_keys?
hosting_tenant_suspend/resume   on non-payment and recovery
hosting_tenant_release          on cancellation
hosting_tenant_usage            usage source for metered billing
```

Container tenants are ready when `hosting_tenant_create` returns. Machine
tenants return `provisioning` (or `waiting_for_machine` when the pool is
empty) and become `active` in the background; read `hosting_tenant_get` for
the SSH command.

## Machine scripts

`scripts/<platform>/customer-access.sh` creates the customer account
(`hst_<20 hex>`), installs keys in `/etc/ssh/apteva-hosting-keys`, and
revokes or verifies access. On macOS, `wipe.sh`, `reinstall.sh` and
`verify-clean.sh` are installed to `/usr/local/libexec/apteva-hosting/` and
their hashes are pinned at inspect. Release of a macOS pool machine runs
revoke → wipe → verify clean (polled) → reinstall Hosting's SSH profile and
helpers → check clean → back on sale. Linux pools have no automatic reset yet:
a released Linux machine is quarantined for manual cleaning.

Erase is refused on machines not marked `validated` (needs `confirm` equal to
the machine id). Validate a machine only after testing its erase and recovery
by hand.

## Operator

The panel has three views: tenants (create, suspend, resume, restart,
reinstall, release, run log), machines (pools, execution switch, add,
inspect, validate, maintenance, quarantine, clean, remove, reviewed retry)
and profiles (with availability).

## Upgrading from 1.x

Migration 004 turns plans into container profiles, maps `deleted` tenants to
`released` and `stopped` to `suspended`, and keeps container tenants running.
Customers, checkout, invoices, add-ons and metering tools are removed; the
old tables stay in the database untouched.
