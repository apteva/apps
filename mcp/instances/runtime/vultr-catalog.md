# Vultr compute catalog — Instances 0.9.0

Instances reads every page of Vultr `/plans` without a product-family filter,
and every page of `/plans-metal`. New plan IDs and virtual/GPU families flow
through the same catalog model. The source endpoint identifies virtual versus
physical hardware; persisted resource metadata selects the correct create/get/
delete API during provisioning, network readiness, diagnostics and recovery.
Existing virtual-instance records continue to use their original API.

Catalog rows preserve provider family, CPU/memory/local disk capacity, reported
accelerators and per-device VRAM, region availability, explicit known pricing
(including a genuine zero quote), and regional price overrides. Bare-metal disk
capacity is total physical disk capacity; provider partitioning/RAID determines
usable space. ARM Grace/Neoverse CPU metadata is distinguished from x86. GPU
values are taken from reported fields; missing accelerator details are not
invented from a per-SKU lookup table. No GPU drivers are automatically installed.

All plans remain visible. Plans without regions, an on-demand offering, or a
known quote are unavailable for creation. Preemptible-only offers are explicitly
marked unavailable for this on-demand provisioning flow; the adapter does not
guess a preemptible purchase protocol or switch purchase modes. Creation also
checks the provider's live regional availability before making the paid request.
The OS must be a supported catalog OS, with matching architecture. Custom ISO,
snapshot, backup, marketplace pseudo-OS entries and Windows are not offered by
the existing Linux automation flow.

## Boot storage

Fixed local-disk plans use their included storage. Plans reporting block-storage
boot support expose boot constraints; this covers VX1 local NVMe and diskless
variants, including API placeholder disks that are not genuine local storage.
Diskless creation requires explicit `storage.boot` rather than silently buying
an extra volume. Bootable block storage uses `high_perf`/performance, with a
positive requested size, and is billed separately by Vultr. The provider validates
its current volume limits. Local capacity cannot be resized or retained through
a boot override. Physical plans use their included disks; Vultr virtual-machine
block volumes cannot be purchased/attached for a physical target through this
adapter.

Inline new boot blocks get a unique ownership label recorded before compute
creation. Readiness verifies the block's account, label, ID, attachment, region,
bootable flag, performance type and size, then persists it using the shared boot
volume inventory. Restarted provisioning reuses the recorded compute resource.
An unconfirmed create result keeps its inventory for reconciliation.

On deletion, compute absence must be confirmed before boot cleanup. Only the
recorded app-managed boot ID with matching ownership metadata can be deleted,
and only once it is detached. `retain` leaves it in volume inventory; default
`with_instance` removes it using the existing recoverable volume-operation
journal. Unknown outcomes or changed ownership keep the inventory and stop
cleanup. Other hosts, attached volumes and unrelated disks are not selected for
deletion by inference.

## Updating an existing Server

The matching Vultr integration definition is published in `apteva/integrations`
`main`. It adds `list_metal_plans`, bare-metal lifecycle tools, regional
availability and boot-device payload fields. Existing servers need a catalog
refresh so these tools are loaded. This requires no Server binary rebuild:

```text
POST /api/integrations/catalog/download
Authorization: Bearer <existing server API key>
```

This downloads/reloads the integration catalog; it does not provision machines.
Then update Instances to 0.9.0. An old definition produces an explicit refresh
error instead of silently hiding bare metal. Release publication does not refresh
or update production installations automatically.

## Validation

Read-only public API checks on 2026-10-21 preserved all 139 virtual and 23 physical
plans returned by Vultr, including unavailable entries. Mock provider lifecycle
and local SSH tests cover bare-metal creation/readiness, correct endpoint routing,
full pagination/account isolation, new families, regional pricing and availability,
boot payloads, ownership checks, deletion/retention retries and rejection of foreign
resources. UI tests cover physical labels, unavailable options and required boot
choices. The production React JSX bundle is rebuilt and verified.

No paid Vultr resources were created for validation; production was untouched.
A real hardware purchase/provisioning trial has not been performed for this release.

Protocol references: [Vultr plan SDK](https://github.com/vultr/govultr/blob/master/plan.go),
[bare-metal SDK](https://github.com/vultr/govultr/blob/master/bare_metal_server.go),
[VX1 storage](https://docs.vultr.com/vultr-vx1-cloud-compute).
