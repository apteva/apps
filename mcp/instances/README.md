# Instances (v0.1)

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
Other ids are remote VPS rows provisioned via the bound integration.

## Tools

| Tool | Purpose |
|---|---|
| `instance_create` | Provision a new VPS (v0.1: Hetzner Cloud only) |
| `instance_get` | Fetch one instance row |
| `instance_list` | List all instances; optional `provider` / `status` filters |
| `instance_destroy` | Terminate upstream + remove row (refused for local id 0) |
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

## Remote instances (Hetzner v0.1)

`instance_create` with `provider=hetzner`:

1. Generates a per-instance Ed25519 SSH keypair.
2. Persists the row at `status='provisioning'`.
3. Calls `hetzner.server_create` via the bound integration with cloud-init
   that seeds `authorized_keys` with the public key.
4. Records `provider_id` + public IPv4 from the response.
5. Background goroutine probes SSH readiness; flips to `status='ready'`
   when the box accepts the key (typically 30-60s).

`instance_destroy` calls `hetzner.server_delete` and removes the row.
404 from upstream is treated as success (already gone).

## Metrics

Local: `gopsutil` for CPU / memory / disk / network / load / uptime.

Remote: SSH-execute a small bash script that parses `/proc/loadavg`,
`/proc/meminfo`, `/proc/stat`, `df -P`, `/proc/net/dev`, and prints
JSON. Tolerant of preamble noise on first SSH connect (picks the last
line that parses as JSON).

Cached 5s per-instance to avoid duplicate SSH sessions on rapid panel
refreshes.

## Naming

"Instance" here = compute machine (AWS/Vultr/EC2-style). Apteva-core
has its own internal "instance" concept (a thinking-loop running per
project) — same word, different scope, no code overlap. A future
apteva-server release renames core's concept to "agent" and removes
the linguistic collision.

## v0.1 limitations

- Hetzner is the only remote provider. DigitalOcean / Vultr / AWS EC2
  in v0.2 once the catalog API mappings are validated.
- SSH key pinning on first connect is `InsecureIgnoreHostKey`. v0.2
  pins the host key seen at provisioning.
- Private SSH keys are stored plaintext in the DB. v0.2 encrypts at
  rest with the platform secret.
- No metrics streaming (SSE) yet. Pull-only via `instance_metrics`,
  cached 5s.
- No host-key-based audit trail for who-ran-what. v0.3 addition if
  multi-user installs need it.

## Tests

```bash
go test ./...          # tier 1: schema, idempotency, local exec
```

Tier 2 (real Hetzner provisioning + real SSH round-trip) requires API
credentials and is run manually before each release.
