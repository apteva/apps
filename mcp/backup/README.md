# Backup

Backup captures restorable Apteva database snapshots and stores them on local
disk, AWS S3, or Cloudflare R2. It also supports per-tenant snapshots when a
Fleet app is bound.

## Coverage

On current servers, the platform scope contains the server database, managed
app data (including stopped apps), agent data, and managed custom MCP sources.
Legacy format-v1 snapshots contain the platform database and running app
sidecar databases only. External storage, unmanaged repositories, and host
configuration remain outside this archive.

Local backup destinations carry a `.apteva-backup-destination` marker. These markers are for a separate Server snapshot-exclusion update, which is
not included in Backup v0.3.5. Until that Server update is deployed, use cloud
storage or a local destination outside the server configuration directory to
avoid recursively including previous backup archives.

A Fleet tenant scope contains the selected local tenant's managed config
directory. Hosted Fleet tenants are reported as unsupported until remote
snapshot transport is implemented.

## Features

- Immediate and cron-scheduled backups through the Jobs app
- Full gzip/tar validation before an object is accepted as restorable
- Policy- and scope-isolated object keys and retention
- Prefix-scoped local, AWS S3, and Cloudflare R2 retention scans
- Optional age encryption using an install-configured passphrase
- SHA-256 verification before every restore
- Per-tenant Fleet backup and restore for local tenants
- Soft-deleted destinations, preserving historical restore metadata
- Durable run progress with interrupted-run recovery and bounded failed history
- Destination health checks and partial-restore reporting
- App-authorized streaming with separate snapshot and restore permissions

## Scheduling

Backup requires the Jobs app. Each policy creates a Jobs `app_tool` target that
calls `backup.backup_now` with the policy ID. Policy creation is atomic from the
operator's perspective: if Jobs registration fails, the incomplete policy is
removed. Scheduled calls commit a durable queued run before acknowledging Jobs. A
single dispatcher processes queued policies when the backup/restore lock is
available; pending requests survive restarts. Repeated ticks for an already
queued policy share that pending run.

Backup and restore operations are globally serialized. Scheduled backups wait
in the durable queue; conflicting interactive operations are rejected so platform snapshots cannot overlap a destructive
restore or run several full-database VACUUM operations at once.

Retention applies independently to each policy and scope. New policies use
random storage identities so deleting and recreating a policy cannot reuse an
old archive namespace. The replacement backup is committed before pruning;
failed result writes preserve both the uploaded object and its predecessors. A value of `0` keeps
all backups. Ad-hoc runs are stored under their own namespace and are not pruned
by scheduled policies.

Failed and interrupted run rows are retained for 90 days by default. Set
`failed_history_retention_days` to `0` to retain them indefinitely. Successful
history remains tied to the stored object and scheduled retention policy.

## Cloud Storage

Cloud destinations use the install's optional `cloud_storage` binding and read
credentials through the SDK's restricted credential API. Supported connection
types are:

- AWS S3
- Cloudflare R2

One cloud account can be bound to a Backup install. Multiple destinations can
use different buckets or key prefixes within that account. Credentials are not
stored in Backup's database.

Local destinations remain on the Apteva host and are not disaster recovery on
their own. Keep at least one off-host destination or a separate host snapshot.

## Encryption

Set `encryption_passphrase` in the app configuration to encrypt new objects
with age before upload. The stored object's SHA-256 digest is recorded and
verified before decryption and restore.

Encryption streams directly into the destination after the plaintext archive
has been validated, avoiding a second full encrypted temporary file.

For portable platform recovery, use a passphrase of at least 12 characters
without line breaks. Backup sends it through authenticated headers on the existing app callback
routes so format-v2 servers wrap the original server encryption key inside the
archive. An app-local streaming adapter supports the published SDK v0.77.0.
Backup rejects new encrypted platform snapshots if the server did not wrap the
key. This requires a server with format-v2 recovery support; no SDK release is needed. Unencrypted
snapshots still require the original server key on a fresh host. Older archives
are not retroactively updated: age encryption alone did not preserve that key.

Keep the passphrase outside the Apteva host. Losing it makes encrypted backups
unrecoverable; changing it does not re-encrypt old backups, so restores require
the passphrase used when each backup was created.

## Restore Semantics

Current format-v2 recovery stages all captured data and activates it on the
next `apteva-server` restart. Legacy format-v1 restores replace app databases
live and stage the platform database for restart.
Fleet restores validate the archive's provider, tenant ID, and tenant slug
before replacing the selected tenant directory.

Access to Backup REST, MCP, and inter-app routes is enforced by Apteva Server.
The separate Server changes restricting these routes to administrators and
administrator-owned Jobs calls are not included in Backup v0.3.5.

The global Backup installation uses its own install token through dedicated
platform callback routes. It never receives an administrator API key and it
cannot access the management snapshot routes. Snapshot and restore are separate
approved permissions, and every restore requires explicit operator confirmation.
Older servers without the app-authorized streaming capability are rejected with
a clear upgrade error; Backup does not fall back to an administrator credential.

The restore report is inspected entry by entry. If the platform applies some
databases but rejects others, Backup reports a partial restore and lists the
failed entries instead of presenting the operation as fully successful.

History uses indexed cursor pagination, with no 500-run navigation ceiling.
UTC timestamps include an explicit timezone; the panel also handles legacy
SQLite timestamps as UTC.

Deleting a destination hides it from new runs but preserves its configuration
for historical restores. A destination referenced by a policy cannot be
deleted until the policy is removed.

## Development

```bash
cd mcp/backup
go test ./...
go build .
```

The panel source is `ui/BackupPanel.tsx`; rebuild panel artifacts from the apps
repository root with:

```bash
bun run scripts/build-panels.ts
```


## Optional Instances source

Bind `instances_provider` to the Instances app. It is optional: Backup's
platform and Fleet flows work without it, and Instances has no dependency on
Backup. This is a global administrator workflow through the Server's protected
Backup surface. Booking-scoped customer access is not implemented here.

Select an instance, enter 1–32 non-overlapping absolute folder paths, and use
**Check host and folders**. Supported hosts are registered, ready remote SSH
instances with command, upload and tunnel capabilities, running macOS or Linux
on amd64 or arm64. No language runtime or compiler is required on the host. Provider names do not establish backup support. Source symlinks,
special files, root/virtual filesystems and changing files are rejected.

The method preserves regular-file contents, directories, ordinary POSIX modes
and modification times. It does not preserve ownership, ACLs, extended
attributes, resource forks, symlinks, or full-machine bootability. Use a quiet
source directory; this is not a filesystem snapshot or a database-consistency
mechanism. Restore ownership belongs to the SSH user.

Backup downloads the matching native Go worker from this version’s GitHub
release assets and checks its embedded SHA-256 checksum. It sends that executable
through `instance_upload_file`, verifies it again on the host, and starts it
through `instance_run_command`. The helper is built with `CGO_ENABLED=0`.
The worker binds only the remote loopback address; Backup transfers archive
bytes through `instance_open_tunnel`, using an operation-specific bearer token.
No archive bytes go through MCP command output or base64 file responses, and
Backup never obtains SSH keys. As with existing Fleet tunnel consumers, Backup
and Instances must share a network namespace where Instances' loopback tunnel
is reachable. Separate network namespaces are reported as unavailable.

The worker materializes a gzip archive in a private `/tmp/apteva-backup-<uid>`
operation directory. Backup streams it into its existing verification,
encryption and local/S3/R2 destination pipeline. Allow disk space for a source
archive on the host and a temporary archive on Backup's host. Temporary worker
files are removed on successful completion, or when the two-hour worker limit
expires. A powered-off host cannot run expiry cleanup until it starts again.

Both scheduled and immediate instance backups enter the durable SQL queue.
Restart reconciliation reuses the run and remote operation IDs; a completed
remote archive is reused after transfer interruption. **Retry same recovery
point** requeues a failed instance run without generating another recovery
point. If temporary remote data has expired, capture starts again under that
same pending run. Completed recovery points are never retried or overwritten
by this action. Jobs scheduling, destinations, install-configured encryption
and policy retention remain owned by Backup.

Object prefixes include the administrator namespace, pinned Instances binding
and host identity, instance ID, and random policy identity. Host deletion does
not delete recovery points. Restore can select another compatible registered
host without querying the original host. Every restore requires confirmation
and a **new** absolute target directory with an existing parent; existing data
is never overwritten. Archive integrity, member paths and types are checked
before staged files are activated with an atomic no-replace rename. Restored
folders are numbered `0`, `1`, etc.; `.apteva-recovery.json` maps them to original
source paths and records the restore operation for retry reconciliation.

Policy scope example:

```json
{
  "kind": "instance",
  "id": "41",
  "source_app": "instances",
  "config": {"method": "folders", "paths": ["/Users/operator/Documents"]}
}
```

`backup_now` / `backup_schedule` use `scope_kind`, `scope_id`, `source_app`,
and `source_config` for the same fields. `backup_restore` adds `target_path`
and optional `target_instance_id` alongside `run_id` and `confirm: true`.
`backup_now` with `retry_run_id` retries a failed instance run.

Worker regression tests are included in `go test ./...`. To build all four
helper release assets, run `go run ./cmd/build-helpers -version 0.3.6 -out /tmp/backup-helper-assets`.
Commit the resulting `helper-assets.json` and attach those exact binaries to
the matching Backup release before publishing its registry entry.
Go tests exercise the real worker and HTTP streams through a local test adapter
for Instances, including encrypted archives larger than the generic file-tool
limit, replacement restore, metadata, restart reconciliation, and retry reuse.
They do not contact a registered production host.
