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
Other ids are provider-managed compute provisioned through one of the bound
cloud integrations.

## Tools

| Tool | Purpose |
|---|---|
| `instance_list_providers` | List bound provider connections and the configured default. |
| `instance_create` | Provision compute through the bound provider, including VPS, GPU Pods, and Scaleway Apple silicon. |
| `instance_get` | Fetch one instance row |
| `instance_list` | List all instances; optional `provider` / `status` filters |
| `instance_destroy` | Terminate the provider-managed resource and remove its row where supported (refused for local id 0) |
| `instance_run_command` | Shell command. Local: in-process exec. Remote: SSH. |
| `instance_upload_file` | Write a file. Local: filesystem (path-allowlisted to `<dataDir>/local-files/`). Remote: SCP-equivalent over SSH. |
| `instance_wait_ready` | Poll until SSH reachable. |
| `instance_metrics` | CPU / mem / disk / network / load / uptime. 5s cache. |

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

- Provider selection is by provider slug. Multiple different providers can be
  bound simultaneously; selecting between multiple accounts of the same
  provider is not exposed yet.
- In-place resizing is currently available only for Hetzner.
- Metrics are pull-only through `instance_metrics`, cached for 5 seconds.

## Tests

```bash
go test ./...
```

Real provider provisioning is opt-in and requires separately scoped test
credentials; unit tests never create billable resources.
