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
space. Image-derived root-volume requests never send empty-volume fields. A
full-capacity local request, such as DEV1-L with 80 GB, omits the volume map so
Scaleway creates the marketplace image's maximum local root disk. Smaller local
and explicit block roots send only `size` and `volume_type`.

Scaleway server deletion snapshots the attached root-volume identity first,
deletes the server, waits for the root volume to detach, and then deletes an
app-managed boot volume whose policy is `with_instance`. Local `l_ssd` roots use
the Instance API while SBS roots use the Block API. A retained boot volume is
detached from the instance inventory instead of being silently discarded.

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

### Scaleway Elastic Metal

Elastic Metal offers are exposed as `elastic-metal/<offer-id>` server types,
separately from virtual Instances, Dedibox, and Apple silicon. Catalog discovery
includes live stock, dedicated CPU/RAM, included local disks, hourly pricing,
compatible cloud-init operating systems, and supported zones. Provisioning uses
the hourly offer, validates Scaleway's default partitioning schema, optionally
applies RAID, installs the selected OS, and waits for both SSH and cloud-init.

Destroy uses provider APIs even when the guest is unreachable. Managed Flexible
IPs are deleted by default and can be explicitly retained. Scaleway continues
billing a powered-off Elastic Metal server until it is fully deleted.

## Existing servers and automatic setup

Use **Add your server** in the Instances panel or the existing MCP tool:

```json
{
  "name": "Home server",
  "ssh_host": "192.168.1.20",
  "ssh_user": "apteva",
  "setup": { "baseline": true, "docker": true, "runtimes": true }
}
```

Pass this to `instance_register`. The response contains the instance and an
`authorization.command` to run locally on the server. On Ubuntu/Debian this
command installs/enables SSH, creates the account if missing, and authorizes
its dedicated SSH key. When software setup is selected, the command grants
that account passwordless sudo; Docker access also gives control of the host.
Existing SSH accounts can instead authorize `authorization.public_key` manually.
Use `instance_register({"id": 123})` to recover the same command and identity.
Do not repeat a new registration when the ID already exists.

`instance_wait_ready({"id": 123})` starts/verifies connectivity and waits for
setup to finish. The sidecar also resumes pending external registrations on
startup and every 30 seconds. `instance_get` and `instance_list` expose
`setup.requested`, `setup.status`, `setup.stage`, `setup.completed`, detected OS
and architecture, verified capabilities, and the last setup error. No new MCP
tools are required.

The same optional `setup` object is accepted by `instance_create`. Cloud hosts
complete their provider-specific readiness checks before the shared setup runs.
Omitting setup on cloud creation preserves existing behavior. Omitting setup on
external registration performs discovery and SSH/file/metrics verification
without installing packages. Discovery supports Linux and macOS; automatic
package installation is initially limited to Ubuntu/Debian on AMD64 and ARM64.

- `baseline`: install missing common utilities and prepare an Apteva data directory.
- `docker`: Instances installs Docker if missing, enables its service, authorizes
  the SSH user, and verifies daemon access and a disposable container over a fresh
  SSH session. The Containers app is not called or modified.
- `runtimes`: Instances installs distribution-provided Node.js, npm, and Go if
  missing, verifies their commands, and records versions. Existing installations
  are reused. This prepares language tools; it does not install an Apteva tenant,
  promise app-specific runtime versions, or call Fleet.

All software setup runs directly through Instances' existing SSH implementation.
Neither Containers nor Fleet bindings or updates are required. The optional VPN
connection flow below uses the VPN app's existing tools without changing that app.

Setup does not advertise Docker/language-tool readiness until all selected steps pass.
`instance_wait_ready({"id":123,"retry":true,"async":true})` retries a failed
setup or rechecks an already-ready host. Supply `setup` to change desired
capabilities. Setup is additive: unchecking an option does not uninstall software.
Scripts reuse existing installations, can run again after interruption, and
never format a disk. Progress is durable; an interrupted step reruns rather than
trusting stale success. Retry does not reprovision a cloud resource. Provider
creation failures must be resolved separately.

### Home servers behind a router

Bind the existing **VPN** app to Instances and install a reachable WireGuard
server using `vpn_install`. Instances must have network access to that VPN
subnet (run it on the VPN server host or configure routing to the subnet).
Then call `instance_register` with `vpn:true` instead of `ssh_host`.

Instances reuses `vpn_status`, `vpn_peer_list`, `vpn_peer_add`, and
`vpn_peer_config`. The returned local enrollment command installs the generated
WireGuard client configuration, enables it on boot, and configures SSH. It
contains a VPN credential: keep it private. Instances stores only the peer
reference and SSH address; VPN retains ownership of its keys. Resume reuses the
same peer. Enrollment leaves the home machine's default internet route and DNS
unchanged, and refuses configurations with executable WireGuard hooks.

The VPN endpoint must be reachable from home. There is no NAT relay or automatic
route installation on the Instances host. A firewall blocking SSH on the VPN
interface must be adjusted by the host administrator. SSH readiness remains
pending until the path works; it never falsely reports a disconnected host ready.

HTTP routes mirror the MCP operations: `POST /api/instances-register`,
`POST /api/instances` with setup, and `POST /api/instances/<id>/wait-ready` with
`retry`, `setup`, or `async`. The UI uses these same handlers.

External hosts support commands, file transfer, SSH tunnels, and metrics after
platform discovery. `instance_destroy` forgets their row without deleting or
shutting down the machine. If enrollment created a VPN peer it revokes that peer
first. Software, the SSH account, and its authorized key remain on the server;
remove them locally when decommissioning access. Instances never uninstalls an
existing workload as a side effect of forgetting a host.

## Object storage

`object_storage_create` provisions storage through a bound provider and runs the
same S3 setup pipeline for every provider. Currently subscription provisioning
supports Scaleway and Vultr. The generic `s3-compatible` integration supplies
bucket and object operations; the platform stores credentials encrypted in an
app-owned, non-exportable managed connection. No secret is saved in Instances.
The server must support managed credentials and have the updated integration
catalog containing `s3-compatible` (refresh the catalog after upgrading).

```json
{
  "request_key": "media-production-storage",
  "name": "Media",
  "provider": "vultr",
  "region": "2",
  "bucket": "my-private-media-bucket",
  "setup": {
    "private": true,
    "cors_origins": ["https://app.example.com"],
    "create_connection": true
  }
}
```

Use a region/cluster and plan from `object_storage_list_plans`. Keep the same
`request_key` when retrying a creation request. To resume after an interrupted
setup, pass `{"id": 123}` to `object_storage_create`; this never purchases another
subscription. To configure an older subscription, pass `{"id":123,"bucket":
"my-new-bucket","setup":{}}`. An existing Scaleway bucket created by Instances
is reused; unrelated existing buckets are never adopted or changed.

Setup creates the bucket, sets an owner-only ACL, removes its bucket policy,
configures and reads back CORS, and verifies an upload/download/delete roundtrip.
Only then does the resource become `ready`. Empty origins disable CORS; origins
must be exact HTTP(S) origins without wildcards or paths. Allowed methods are
GET, HEAD, PUT, POST, DELETE; headers are unrestricted and ETag is exposed. CORS
does not make a bucket public or grant unauthenticated object access. Public
buckets are unsupported. Provider-specific unsupported operations remain visible
as setup failures, rather than being silently skipped.

The result contains `object_storage`, `setup` (stage, error, verified capabilities),
and `connection_id`. The connection supports S3 integration tools; binding it to a
consuming app is separate and that app must accept `s3-compatible` connections.
Credential rotation updates this same managed connection; destroy revokes it
only after deleting the provider resource. Scaleway credentials are project-wide;
use a dedicated provider project for isolation. Vultr credentials belong to the
storage subscription. Unknown provider-create outcomes require reconciliation
before retrying, rather than risking another paid resource.

`setup.create_connection=false` uses a temporary vault connection during setup,
revokes it afterward, and returns credentials once. `provision_only=true` keeps
the legacy subscription-only flow, returning credentials without S3 setup.
Provider secrets remain absent from get/list responses. Retrying setup can use
`setup` to change CORS origins; connection retention cannot change afterward.

## Diagnostics and reconciliation

Provisioning records explicit ProviderCreate, Boot, Network, SSH, CloudInit,
Storage, Rollback, and Delete stages while preserving the primary failure and
any later cleanup failure separately. Failed resources are retained for
diagnosis by default.

`instance_compare_provider` compares a tracked server with its live provider
state. `instance_provider_inventory` lists Scaleway virtual Instances, Elastic
Metal servers, volumes, Flexible IPs, and Object Storage buckets and reports
untracked server, volume, and bucket IDs. `instance_storage_benchmark` performs
a bounded 256 MiB write benchmark, removes its temporary file, and saves the
result.

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
