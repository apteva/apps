# Instances native runtime backend

Instances implements `apteva.runtime-backend/v1` at the protected prefix
`/_apteva/runtime/backends/native`. It requires a Server build implementing that
controller (minimum app manifest version requirement: Apteva 0.87.4).

This first implementation runs prebuilt Linux amd64/arm64 source/service apps on
registered **remote hosts with root SSH access and systemd**. Packages must expose
SDK-compatible authenticated HTTP control, `/health` (or their declared health
path) and `/manifest`. It uses existing SSH
connections and private TCP forwarding; it does not provision hosts, install
Docker, build applications, change Telephony dependencies, or migrate an existing
local installation. Existing inventory, provisioning, storage and monitoring
surfaces are preserved. Snapshot, replica, fencing and resource-limit capabilities
are false. Container images, port remapping and extra storage mounts are rejected
rather than silently ignored. Required tools on the target are `systemctl`, `ss`, `flock`, `tar`,
`sha256sum`, `stat` and `curl`. Missing tools or unavailable resources reject validation;
this backend does not silently install prerequisites.

## Prepared artifact handoff

Prepare a directory containing the Linux ELF executable and its runtime files.
For Telephony 0.12.14, include the release's `migrations/` and built `ui/` directory.
Include any required runtime libraries; merely changing GOOS does not establish
ABI/library compatibility. The executable is checked against the declared ELF
architecture and all files are SHA-256 verified before sealing and on the host.

Protected staging endpoints:

- `POST /artifacts`: register `{id, app_name, app_version, platform, executable,
  files: [{path, sha256, size}], app_capabilities}`.
- `PUT /artifacts/:id/files/:path`: stream raw file bytes (256 MiB per file).
- `POST /artifacts/:id/seal`: verify every file and make the package immutable.
- `DELETE /artifacts/:id`: discard staging only after its deployments are deleted.

Staging has a shared 512 MiB reservation budget and at most 128 artifacts. Paths
must be relative, regular files; traversal and reserved internal filenames are
rejected. This dedicated streaming path does not raise the existing 16 MiB MCP
file-transfer limit. The release digest is the executable SHA-256; each additional file has its own
digest. The advertised delivery format is `binary`. SDK `runtime.artifacts` entries
are checksummed complete archives, whose digest is different; those manifests are
rejected until a verified archive handoff is implemented. The first Telephony
source-manifest trial has no such archive entry.

A helper hashes and uploads an already-built package, without building it:

```sh
# APTEVA_APP_TOKEN is the Instances installation's control credential, supplied
# by the operator/Server at runtime. Never put it in an artifact or tracked file.
bun run runtime/stage-artifact.ts /tmp/telephony-package \
  http://127.0.0.1:<instances-sidecar-port> \
  telephony 0.12.14 linux-amd64 telephony /tmp/telephony-capabilities.json
```

Use the private sidecar/control connection, not the public app proxy: Server
intentionally blocks `/_apteva/runtime/` on ordinary app proxies. A Server artifact
publisher can call these protected endpoints using its existing backend credential.
The helper emits `artifact_id` and `release_digest`, never a token. A retry reuses
an already sealed matching package.

Telephony 0.12.14's exact-release app capabilities are:

```json
{"passive_start":true,"drain":true,"replica_safe":false,"worker_coordination":false,"routing":"key"}
```

Capabilities are part of the sealed metadata. `inspect` verifies advertised app
lifecycle capabilities against the authenticated running app before reporting
readiness. Server remains responsible for activating and draining the app. This
backend never switches a standby app into active mode independently.

## Register and place a fresh installation

Use the local Server's admin API:

```http
POST /api/apps/execution-backends
Content-Type: application/json

{"app_install_id":40,"id":"native","endpoint":"/_apteva/runtime/backends/native"}
```

Example placement (use actual installation/instance IDs):

```json
{
  "type":"backend",
  "backend":{"app_install_id":40,"backend_id":"native"},
  "target":{"app_install_id":40,"resource_type":"instances.instance","resource_id":"1"},
  "gateway_url":"https://trial-server.example.com",
  "options":{
    "artifact_id":"the-staged-artifact-id",
    "gateway_url":"https://trial-server.example.com",
    "control_port":53001,
    "min_memory_mb":128,
    "min_disk_mb":256
  }
}
```

The current Server does not put `placement.gateway_url` in `validate-target`'s
request. Therefore repeat it in `options.gateway_url`. Start rejects an environment
whose gateway differs from the validated option. A future explicit request
`gateway_url` is also accepted. The gateway must reach **the trial Server that owns
this deployment**. A remote host's `localhost` is not the operator's computer.
For a local-only trial, an operator-managed reverse SSH tunnel can expose the local
Server gateway on a remote loopback port; use that loopback URL consistently in
placement and options. Instances does not create that callback tunnel in this scope.

Instances and Server must share the network namespace of the returned loopback
control listener. Independently isolated sidecar containers need a separate secure
connectivity adapter; the current backend must not be advertised as container-ready.

## Lifecycle and persistence

- `GET /describe`: exact capabilities and target/options schemas.
- `POST /validate-target`: installation-scoped target, SSH/root/systemd,
  architecture, free memory/disk and reachable gateway health.
- `POST /prepare`: durable record and private port allocation, then background
  checksum-verified transfer. Returns `state: preparing`; `inspect` observes
  completion without exceeding the controller's 30-second HTTP budget.
- `POST /start`: journaled intent, encrypted environment storage and one owned
  systemd service. Environment travels through SSH stdin, not command arguments.
- `POST /inspect`: owned unit/executable identity and metadata, required TCP/UDP listeners, authenticated
  HTTP health and advertised lifecycle capabilities.
- `POST /stop`: owned service only; verifies inactive service, empty remaining
  cgroup and released listeners before setting `stopped_confirmed`.
- `POST /delete`: confirmed stop, remove owned unit and release files; preserve
  installation data and small ownership tombstones.
- `POST /logs`: at most 100 bounded lines with cursor, redacting process environment
  values and nested configuration strings. Missing decryption context fails closed.

Requests carry the Server contract, operation/idempotency key, deployment ID,
installation ID, generation, project, target, options, manifest and previous
execution. Only `start` accepts environment/credentials. Deployment identity,
artifact, manifest and options are immutable; conflicting retries fail. The
Server-side request header must match the body when present.

Executable checksums are verified during preparation/start. Recurring inspection
checks the verified executable inode, size and high-resolution timestamps instead
of hashing a large executable on every controller poll.

The persistent host identity combines machine ID and canonical SSH host public
key, independent of inventory aliases. All SSH mutations recheck that identity
and immutable ownership markers. Physical TCP/UDP ports cannot overlap another
retained native deployment. SSH failures leave execution **unknown**; they do not
confirm termination or authorize a second process.

SQLite keeps deployment metadata and tunnel allocation. Environments use AES-GCM
with a separate owner-only key/file in the Instances data directory. On an
Instances restart, active executions regain the same private local tunnel port;
recovery never calls `start`. A conflicting local port prevents readiness rather
than silently returning a different endpoint. Start responses lost after the
remote effect are recovered through `inspect`.

Remote releases are under `/var/lib/apteva-native/<backend-install>/inst-<app-install>/releases/`;
persistent data is the sibling `data/` directory. Units are named
`apteva-native-<backend-install>-<deployment-hash>.service`, use `KillMode=control-group`,
and do not restart automatically. No pre-existing service is adopted or stopped.
A host cannot be destroyed or forgotten while it has retained native deployments.

## Trial boundary

The separate Telephony 0.12.14 installation on DigitalOcean `test-render` is the
intended first integration trial. It needs a staged exact-release package and a
reachable gateway to the local trial Server. Test lifecycle, duplicate requests,
lost responses, restart/tunnel recovery, retained data and unrelated services.
SIP/RTP/public WebRTC connectivity and existing-installation migration are outside
this increment. No production deployment, registry release or dashboard change is
part of this implementation.
