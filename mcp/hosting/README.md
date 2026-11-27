# Hosting

Runs tenants. A tenant is one thing running for one buyer: a container, a
machine from a pool, a machine created on demand, or a phone from a phone
pool. Hosting does not sell
anything. Prices, checkout, invoices and subscriptions belong to the SaaS app
(or any other caller), which calls Hosting's tools.

Dependencies, all optional:

- `containers` for container tenants and workloads on machines.
- `instances` for machines (pool hosts and on-demand servers).
- `mobile` for phone pools (Android phones on ADB).

Hosting subscribes to no events and calls no billing app.

## Model

- **Product**: what runs (Apteva, a Docker image, WordPress, SSH access).
- **Profile**: where it runs. `backend` is `container`, `machine_pool`
  (needs `pool_key`) or `machine_on_demand` (needs `settings.provider` and
  `settings.server_type`).
- **Pool**: a set of Instances hosts of one platform (`macos` or `linux`),
  or, with `managed_by: mobile`, a set of Android phones that the Mobile app
  drives (see [Phone pools](#phone-pools)). New pools start with execution
  switched off.
- **Machine**: one Instances host (or one phone) in a pool. States: `quarantined`,
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

## Phone pools

A pool with `managed_by: mobile` rents Android phones through the Mobile
app. Hosting uses Mobile's tools as they are (`mobile_devices`,
`mobile_session`, `mobile_device`) and keeps no phone logic of its own. Mac
and Linux pools work as before; both kinds can exist side by side.

```
hosting_pool_upsert         key=phones, managed_by=mobile   (platform android)
hosting_machine_add         pool_key=phones, device_ref=<id from mobile_devices>
hosting_machine_update      validated=true, confirm=<machine id>
hosting_machine_inspect     records the baseline the first time, verifies it
hosting_profile_upsert      backend machine_pool, pool_key=phones, product ssh-access
```

- **Lock**: renting a phone opens an exclusive Mobile session (caller_ref
  `hosting-tenant:<tenant id>`, one hour) that Hosting renews every minute.
  Nobody else can open a session on that phone while it is rented.
- **Customer access**: `hosting_tenant_get` returns `device`: `managed_by`,
  `device_ref`, `session_id`, `online`. It is the only place the session id
  appears: tenant lists, machine views, events and run logs show
  `session_held` instead, and a session Hosting opened to inspect or clean
  a phone is never shown to a tenant. The customer drives the phone with
  Mobile's tools and that session id. If Mobile ends the session (the phone
  was away past Mobile's offline grace), Hosting opens a new one and emits
  `hosting.tenant.access_updated` with `reason: session_reopened`: read the
  tenant again for the new id.
- **Suspend** stops the session; **resume** opens a new one. Restart,
  reinstall and SSH keys do not apply to phones.
- **Release** (and `hosting_machine_clean`): stop the session, open a new
  one, run Mobile's `clean` with `clear_data` = every third-party app in the
  baseline (in batches of 50, Mobile's limit), then `verify_clean`.
  `verify_clean` cannot see app data, so the release fails, and the phone is
  quarantined, if Mobile does not report every baseline app's data cleared. A phone that still differs from its baseline
  is quarantined with the differences; otherwise it goes back on sale.
- **Inspect** records the baseline only when the phone has none. Pass
  `capture_baseline: true` to record it again, and only on a phone set up
  exactly as it should be sold: the baseline is what "clean" means.
- **Health**: every minute Hosting reads `mobile_devices`. A phone Mobile does
  not list as ready, configured or busy is offline and not sold. When Mobile
  cannot be reached, phones go off sale without alerts.
- **Degraded**: a rented phone offline longer than `degraded_after_minutes`
  (default 5), or taken by another session while Hosting's had ended, puts
  the tenant in `degraded` (`hosting.tenant.degraded`). It returns to
  `active` by itself (`hosting.tenant.recovered`). The minute sweep never
  changes a tenant or opens a session once a run (a release, suspend, swap)
  has been requested for it. `hosting_tenant_swap` (confirm = tenant id)
  moves the tenant to a free phone in the same pool and quarantines the old
  one; a `device_stop` run ends the old session, retried while Mobile cannot
  be reached. Clean the old phone and run inspect to sell it again.

Limits:

- **The session id is a bearer.** Whoever has it drives the phone, and
  `mobile_session list` shows every session of the project to anyone with
  Mobile tools there. Renters must not get Mobile tool access in the project
  Hosting runs in; give them the session id through your own front end.
- **Clean is not a factory reset.** It removes apps added since the baseline,
  clears the data of baseline apps, the clipboard, and new downloads and
  screenshots. Accounts added to the phone show up as differences and stop
  the phone from being sold, but nothing removes them automatically. Rent
  phones for app testing, not to strangers who will sign in to personal
  accounts.
- Android phones on Mobile's ADB driver only (clean and verify_clean need
  ADB).

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
| `hosting.machine.quarantined` | a run failed on a machine, or a phone was swapped away from its tenant |
| `hosting.tenant.degraded` / `hosting.tenant.recovered` | a rented phone is offline longer than `degraded_after_minutes`, or held by someone else; and when that is over |
| `hosting.tenant.swapped` | `hosting_tenant_swap` moved a tenant to another phone (`from_device_ref`, `device_ref`) |
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
reinstall, swap phone, release, run log), machines (pools, phone pools,
execution switch, add, inspect, validate, maintenance, quarantine, clean,
remove, reviewed retry, resolve a stuck tenant)
and profiles (with availability).

## Upgrading to 2.5.1

2.5.1 fixes phone pools from 2.5.0:

- Release cleared the data of only the first 50 baseline apps. All are now
  cleared in batches, and a release fails if any is not.
- The minute sweep could bring back a tenant being released, open a session
  in the middle of a release, or show Hosting's cleaning session to the
  tenant. Its writes now hold only while no run is requested.
- Swap could race a release, and left the old phone's session alive for up
  to an hour when stopping it failed. Swap and every tenant run now start
  only if the tenant is unchanged, and a run stops the old session.
- The session id appeared in machine views and tenant lists. It now
  appears only in `hosting_tenant_get`.

Migration 010 adds `device_session_owner` to machines and records it for
phones rented under 2.5.0, so their sessions keep being renewed and shown
to the tenant.

## Upgrading to 2.5.0

Migration 009 rebuilds `hosting_pools` (adds `managed_by`, allows platform
`android`) and `hosting_machines` (`instance_id` may be empty for phones;
adds `device_ref` and `device_session`). Existing pools, machines and
tenants are kept as they are. Tenant and machine events from phone pools
carry `managed_by` and `device_ref`. Nothing changes for Mac and Linux
pools.

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
