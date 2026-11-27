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
  the same tenant, and resumes it if it was suspended (so a SaaS
  `account_active` after a late payment brings it back). While it is being
  released, create fails until the release finishes. Once released, the
  reference can be used again.
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
hosting_tenant_release          on cancellation; confirm = tenant id
hosting_tenant_usage            usage source for metered billing
```

Mapping for SaaS fulfillment actions (`external_ref` = `{{account.id}}`):

| SaaS event | Hosting call |
|---|---|
| `account_active` | `hosting_tenant_create` (store the returned tenant id) |
| `account_resumed` | `hosting_tenant_resume` |
| `account_past_due`, `account_suspended` | `hosting_tenant_suspend` |
| `account_cancelled` | `hosting_tenant_release` with `confirm` = the stored tenant id |

A hold made with `hosting_reservation_create` is used by
`hosting_tenant_create` for the same profile and `external_ref` even without
`reservation_id`. A held machine that went offline, lost validation or whose
pool stopped execution is not assigned: the hold is dropped and another
machine (or the waiting line) is used.

`hosting_tenant_release` and `hosting_tenant_reinstall` need `confirm` equal to
the tenant id. Changing a tenant's profile (a plan change such as M4 to M4
Pro) is not supported in place: release the tenant and create a new one.

### Waiting line and presale

When no machine is free, a pool tenant waits (`waiting_for_machine`) and gets
the next machine that comes on sale, oldest order first. A new order never
takes a free machine while someone in the same pool is waiting or holds a
presale place. A tenant suspended while waiting keeps its place.

`presale_limit` on a machine_pool profile (`hosting_profile_upsert`) sets how
many orders it takes ahead of hardware:

- not set (null): the waiting line has no limit and availability counts free
  machines only (the behaviour before 2.3.0).
- `0`: only free machines are sold; `hosting_tenant_create` fails when none
  is free.
- `5`: up to 5 customers may wait. It counts customers waiting and open
  presale holds for that profile, so after 5 bookings it is full, and when a
  Mac arrives and the first waiting customer moves to it, one place opens
  again.

`hosting_availability` reports `available = machines + presale`: `machines`
free now and not promised to anyone waiting in the pool, `presale` places
left under the limit, plus `presale_limit` and `presale_note` (free text such
as "ready within 10 days"; Hosting does not know when hardware arrives).

`hosting_reservation_create` holds a free machine when there is one;
otherwise it makes a presale hold (`presale: true`, no machine) while the
limit has room, and fails when there is none. Presale holds expire like
other holds. `hosting_tenant_create` with a presale hold puts the tenant in
the waiting line even when the limit was lowered since. Setting the limit
back to `0` stops new orders ahead; customers already waiting keep their
place. `hosting.tenant.active` fires when the tenant's machine is ready;
read `hosting_tenant_get` for the SSH command to send the customer.

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
by hand. A machine with `expected_os` set (a version prefix such as `15` or
`15.3`, or `ubuntu 24.04` on Linux) is checked at inspect, provision and
reinstall.

A machine is online when Instances reports it `ready` and, on Instances
versions that report it, `connected`. Hosting refreshes this every minute.

## Customer endpoint

The address a customer connects to is stored on the machine
(`public_host`, `public_port`), separately from the address Instances uses
to reach it. It depends on the instance's `connect_mode`:

- `reverse_tunnel` (a Mac behind NAT, Instances 0.19+): Instances' public SSH
  (`public_ssh.host`/`port`). Turn it on with
  `instance_public_ssh_set(id, enabled=true)` in Instances, or pass
  `enable_public_ssh: true` to `hosting_machine_add`. A tunnel instance from
  Instances older than 0.19 is refused unless a `public_host` is given.
- `vpn`: the instance's public IPv4. Without one the machine has no endpoint.
- otherwise: the public IPv4, else the SSH host. A loopback address is never
  used.

A machine without an endpoint keeps an `endpoint_issue`, is never for sale
(`not_for_sale` in the machine list says why) and cannot be inspected. Its
tenants see `access.issue` instead of an SSH command. Hosting re-reads
endpoints every minute, so a changed tunnel port reaches tenants' commands
on the next refresh. Setting `public_host` with `hosting_machine_update`
fixes the endpoint by hand (`endpoint_source: manual`) and refresh leaves it
alone; setting it to an empty string hands it back to Instances. Hosting
does not subscribe to `instance.public_ssh_changed`.

**Tunnel Macs are never erased automatically**: erasing removes the tunnel
and Instances' key, so the machine could not come back. Release on a tunnel
Mac revokes the customer's access, then quarantines the machine for a
manual clean; `hosting_machine_clean`, `hosting_tenant_reinstall` and
`force_clean` are refused on it. After cleaning it by hand, run inspect to
put it back on sale.

Runs hold a 3-minute lease that a running step renews every minute, so a slow
step (creating an instance, pulling an image, erasing a Mac) is never run
twice. Starting a workload on a machine is adopted by name if an earlier
attempt started it without saving its id.

When a failure leaves a machine tenant stuck (machine quarantined, or the
tenant in suspending, resuming, releasing or failed), an operator settles it
with `hosting_tenant_resolve`: `resume`, `release`, or `force_clean` (erase a
validated macOS machine without revoking first). It needs `reviewed: true`
and `confirm` equal to the tenant id.

## Alerts

Hosting emits events that Messaging or a Function can turn into an email or
chat message. Nothing needs polling.

| Event | When |
|---|---|
| `hosting.operation.failed` | a run failed (step and error included); its machine is quarantined |
| `hosting.machine.offline` / `hosting.machine.online` | the minute refresh sees a machine drop off or come back. Not sent while the machine is being erased (state `cleaning`): that run reports its own failure |
| `hosting.tenant.stuck` | once per situation: a waiting tenant past its `due_at` (`reason: presale_due`), or a tenant still provisioning, suspending, resuming or releasing after `stuck_after_minutes` (default 180; `reason: status:<status>`) |

`presale_ready_days` on a pool profile is the promise made to customers who
wait: a tenant that has to wait gets `due_at` = order time + that many days.
The date is fixed at order time; changing the profile later does not move it.

Erase failures name the missing piece in plain words (for example "device
management re-enrollment not verified in the receipt", or "Mac unreachable
… stopped at the setup screens"). Inspect refuses a Mac with Activation
Lock on.

**Unattended erase needs device management.** After an erase the Mac has
no user, SSH key or Instances connection. It only comes back on its own
through Automated Device Enrollment with an MDM that skips the setup screens
and restores the management account and SSH access, then writes the receipt
that `verify-clean.sh` checks. Hosting does not ship that part: build and
test it on the first machine before marking machines `validated`.

## Operator

The panel has three views: tenants (create, suspend, resume, restart,
reinstall, release, run log), machines (pools, execution switch, add,
inspect, validate, maintenance, quarantine, clean, remove, reviewed retry,
resolve a stuck tenant)
and profiles (with availability).

## Upgrading to 2.4.1

2.4.1 fixes checks in the machine scripts that never failed (a check
written as `! cmd`, or as the first part of `a && b`, is ignored by
`set -e`). Run inspect on every macOS pool machine after upgrading so the
new reset scripts are installed and their hashes pinned. A test rejects
both patterns in `scripts/`.

## Upgrading from 1.x

Migration 004 turns plans into container profiles, maps `deleted` tenants to
`released` and `stopped` to `suspended`, and keeps container tenants running.
A plan whose product row is missing gets an archived placeholder product, and
a tenant whose plan row is missing gets an archived placeholder profile, so
the migration never fails on them. Released tenants give their hostname back
(migration 005 does this for databases that already ran 004), so their slugs
can be used again.

Breaking changes:

- **Nothing enforces payment on existing tenants any more.** 1.x suspended
  paid container and WordPress tenants from Subscriptions events; 2.x
  subscribes to no events. Their Auth and LLM add-ons stay live too. Migrated
  tenants have an empty `external_ref`: link each one to its SaaS account with
  `hosting_tenant_update` (`external_ref` = the account id) and add SaaS
  fulfillment actions for those plans, or review them by hand.
- **Orders fulfillments are no longer completed.** 1.x created orders for
  paid invoices and completed them through `hosting_fulfillment_provision`.
  An Orders fulfillment of type `hosting_tenant_provision` created by an old
  flow now stays open; close it in Orders.
- **Renamed and removed tools.** `hosting_tenant_delete` is now
  `hosting_tenant_release` (and needs `confirm`), `hosting_plan_list` is
  `hosting_profile_list`, and `hosting_usage_get` is `hosting_tenant_usage`.
  Customer, checkout, invoice,
  add-on, metering and fulfillment tools are removed; the old tables stay in
  the database untouched.
