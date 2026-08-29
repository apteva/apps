# Instances

Compute-host inventory for Apteva. Manages the local machine + remote VPS
instances under one MCP/REST surface.

## Why

Several apps need to "run a workload somewhere": Live Link's self-vps
tunnel needs a public-IP machine for the tunnel server; Deploy's planned
SSHRuntime ships releases to a VPS; Backup wants an off-host target;
future Containers/Database/MQTT/game-server apps all want a Linux box.

Instead of each app binding cloud-provider integrations directly and
duplicating provisioning + SSH plumbing, they bind **Instances** as a
`kind: app` integration and call:

```
instance_run_command(host_id, cmd)
instance_upload_file(host_id, path, content_b64)
instance_metrics(host_id)
```

`host_id=0` is the **local Apteva machine**, auto-seeded at app mount.
Other ids are remote hosts provisioned via one of the bound cloud integrations or
registered as externally managed SSH machines.

## Tools

| Tool | Purpose |
|---|---|
| `instance_list_providers` | List bound provider connections and the configured default. |
| `instance_create` | Provision compute through the bound provider, including VPS, GPU Pods, Scaleway Dedibox, and Apple silicon. |
| `instance_register` | Register an existing SSH host, including a Mac, and generate its dedicated SSH key. |
| `instance_get` | Fetch one instance row |
| `instance_list` | List all instances; optional `provider` / `status` filters |
| `instance_destroy` | Terminate managed upstream + remove row, or only forget an external host (refused for local id 0) |
| `instance_run_command` | Shell command. Local: in-process exec. Remote: SSH. |
| `instance_upload_file` | Write a file. Local: filesystem (path-allowlisted to `<dataDir>/local-files/`). Remote: SCP-equivalent over SSH. |
| `instance_wait_ready` | Poll until SSH reachable. |
| `instance_metrics` | CPU / mem / disk / network / load / uptime. 5s cache. |
| `instance_storage_capabilities` | Describe boot/data support, storage classes, tiers, and lifecycle operations for a provider. |
| `instance_list_storage_types` | List generic storage tiers and their provider-native mappings. |
| `instance_volume_create` | Create a managed data volume, optionally attaching and preparing it inside the guest. |
| `instance_volume_list` / `instance_volume_get` | Inspect volumes tracked by Instances. |
| `instance_volume_attach` / `instance_volume_detach` | Attach or retain data storage independently of compute; detach safely unmounts prepared filesystems first. |
| `instance_volume_prepare` | Safely format-if-blank, persist by UUID, mount, and verify an attached Linux data block volume over SSH. |
| `instance_volume_resize` | Grow a provider volume; shrinking is refused. |
| `instance_volume_delete` | Delete a detached app-managed data volume with explicit confirmation. |
| `object_storage_list_providers`, `object_storage_list_plans` | Discover bound object-storage providers, locations, and tiers. |
| `object_storage_create` | Provision object storage and return its S3 credentials once. |
| `object_storage_get`, `object_storage_list` | Inspect managed object-storage resources without exposing secrets. |
| `object_storage_rotate_credentials` | Rotate credentials and return the new secret once. |
| `object_storage_destroy` | Delete provider storage and revoke its managed credentials with explicit confirmation. |

## Local instance (id=0)

Auto-seeded at `OnMount`. Always `provider='local'`, `status='ready'`,
`public_ipv4='127.0.0.1'`. Cannot be created or destroyed via the public
API — only `ensureLocalInstance` touches it.

`instance_run_command` on local routes to `exec.Command("sh", "-c", ...)`
with a 30s default timeout. `instance_upload_file` writes under
`<dataDir>/local-files/` with path-allowlist + traversal guards.

## Managed remote instances

An install can bind multiple cloud providers at once and choose one per
catalog or provisioning request; the configured default is used when no
provider is specified. The app has provider adapters for Hetzner, DigitalOcean, Contabo, Vultr,
AWS EC2, Scaleway, Huawei Cloud, Linode, OVHcloud, and RunPod. Catalog,
provisioning, SSH readiness, recovery, and deletion are normalized into the
same instance contract.

## Storage model

Storage uses independent `role` and `storage_class` dimensions. `boot` versus
`data` describes what the disk does; `local`, `block`, `network`, or
`ephemeral` describes how the provider implements it. A Scaleway SBS boot disk
is therefore `role=boot` and `storage_class=block`.

Omitting `storage` from `instance_create` preserves the provider/image default.
Providers that support configurable boot storage accept
`storage.boot.size_gb`, `storage_class`, `tier`, and `delete_policy`.
`provider_type` remains an advanced provider-native override. Provider/type
catalog rows expose boot-storage constraints so unsupported combinations are
rejected before provisioning. Data volumes default to
`delete_policy=retain`; app-owned volumes can instead use `with_instance`.
Destroy first detaches retained data volumes and only deletes managed volumes
whose policy is `with_instance`. Existing/external volumes are never formatted
or deleted implicitly.

For Scaleway, `storage_class=local` maps to `l_ssd` and is offered only on
server types with non-zero local-storage capacity, such as DEV1-L. Block-only
types such as POP2 accept `storage_class=block`, which maps to `sbs_volume`.
The chosen image must use a compatible local or SBS root snapshot. Readiness
persists the provider-reported boot volume and verifies both its native type and
size, then checks that the guest root filesystem expanded to use the requested
space.

Guest activation is provider-neutral. `instance_volume_prepare` discovers the
attached device through stable `/dev/disk/by-id` identifiers and a strict size
fallback. It refuses ambiguous devices, partitions, unsupported filesystems,
and unknown signatures. With `format_if_blank=true`, a genuinely blank device
can be formatted as ext4 or XFS, mounted at a dedicated path, recorded in
`/etc/fstab` by filesystem UUID, assigned an owner/mode, and verified. The same
`prepare` object can be passed to `instance_volume_create` or
`instance_volume_attach`; create defaults `format_if_blank` to true because the
volume was just created, while attach defaults it to false for retained data.

For example, a media host can create usable storage in one call:

```json
{
  "instance_id": 42,
  "name": "media-data",
  "size_gb": 80,
  "delete_policy": "retain",
  "prepare": {
    "filesystem": "ext4",
    "mount_path": "/srv/media",
    "owner": "1000:1000"
  }
}
```

The returned volume reports `guest_ready=true` only after the mount is
verified. Higher-level apps can use the persisted `mount_path` as their data
directory or container bind-mount source.

A normal VPS provision:

1. Generates a per-instance Ed25519 SSH keypair.
2. Persists the row at `status='provisioning'`.
3. Calls the bound provider's create tool and installs the public key through
   cloud-init, user data, or the provider's native SSH-key mechanism.
4. Records `provider_id` + public IPv4 from the response.
5. Background goroutine probes SSH readiness; flips to `status='ready'`
   when the box accepts the key (typically 30-60s).

`instance_destroy` calls the matching provider delete tool and removes the
row. A 404/410 from upstream is treated as success (already gone). Contabo
does not expose immediate deletion, so Destroy is not advertised there.

Destroy is strictly ID-bound: the app only calls `server_delete` with
the `provider_id` captured from the original `server_create` response.
If a sidecar restart interrupts provisioning before that ID is
persisted, the row is marked error and the operator must inspect
the provider manually; Instances will not infer or recover a server by name.

### Scaleway Apple silicon

Scaleway Mac minis are normalized as `bare_metal` instances with
`platform=macos` (or Linux for Asahi types). Catalog rows use namespaced type
and image IDs so they cannot be sent accidentally to Scaleway's virtual
Instance API.

For each Mac, Instances creates one project-scoped IAM SSH key, records only
the returned key ID privately, and deletes exactly that key after the matching
Mac is deleted. Provisioning uses a non-renewing 24-hour commitment. The
mandatory minimum allocation is exposed as `deletable_at`; Destroy remains
disabled until that timestamp. No account password is stored.

### Scaleway Dedibox

Dedibox offers are exposed as `dedibox/<offer-id>` server types and normalized
as `bare_metal` Linux hosts. They reuse the bound Scaleway connection and its
default project. Provisioning creates one project-scoped IAM SSH key, orders the
physical server, follows the returned service until hardware delivery, selects
the requested Linux release from the server-compatible OS catalog, installs it,
and waits for SSH. The service and SSH-key IDs are retained privately so a
restart can resume provisioning and Destroy terminates only the matching
subscription and owned key.

## Existing SSH hosts and Macs

`instance_register(name, ssh_host, ssh_user, ssh_port?)` adds an existing
machine without provisioning or otherwise changing it. Instances returns a
new Ed25519 public key. Add that key to the selected user's
`~/.ssh/authorized_keys`, then call `instance_wait_ready(id)` to verify access
and mark the row ready.

These hosts use the same command, file-transfer, and loopback tunnel tools as
managed instances. `instance_destroy` only forgets an external row; it never
shuts down, deletes, or reconfigures the machine. The current remote metrics
collector requires a declared platform, so metrics are not advertised for
external hosts by default.

## Object storage

Object storage is a provider resource, not a filesystem volume. Instances can
provision it through a bound Scaleway or Vultr connection and return the S3
endpoint, region, bucket where applicable, access key, and secret key. It does
not create another Apteva Connection and does not mount or consume the storage.

Secrets are deliberately never written to the Instances database. They are
shown only in the create or rotate response and in the UI's one-time credential
dialog. Scaleway resources use a dedicated project-scoped IAM application and
policy; Vultr credentials are owned by its Object Storage subscription. Destroy
deletes the provider storage and revokes the managed credentials. A partial IAM
cleanup keeps the local record in an error state so the operation can be safely
retried.

## Metrics

Local: `gopsutil` for CPU / memory / disk / network / load / uptime.

Remote Linux: SSH-execute a shell collector that parses `/proc` and `df`.
Remote macOS: use `top`, `vm_stat`, `sysctl`, `df`, and `netstat`. Both emit
the same JSON shape and tolerate SSH preamble noise.

Cached 5s per-instance to avoid duplicate SSH sessions on rapid panel
refreshes.

## Naming

"Instance" here = compute machine (AWS/Vultr/EC2-style). Apteva-core
has its own internal "instance" concept (a thinking-loop running per
project) — same word, different scope, no code overlap. A future
apteva-server release renames core's concept to "agent" and removes
the linguistic collision.

## Current limitations

- Multiple different providers and multiple accounts of the same provider can
  be selected by connection ID for provisioning requests.
- In-place resizing is currently available only for Hetzner.
- Metrics are pull-only through `instance_metrics`, cached for 5 seconds.

## Tests

```bash
go test ./...
```

Real provider provisioning is opt-in and requires separately scoped test
credentials; unit tests never create billable resources.
