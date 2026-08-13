# Backup

Backup captures restorable Apteva database snapshots and stores them on local
disk, AWS S3, or Cloudflare R2. It also supports per-tenant snapshots when a
Fleet app is bound.

## Coverage

The platform scope contains:

- the `apteva-server` SQLite database
- `app.db` from running sidecar apps included by the platform snapshot endpoint

It does not contain arbitrary files from app data directories, stopped
sidecars, external object storage, source repositories, container volumes, OS
configuration, or secrets stored outside the captured databases. Use a host or
volume snapshot in addition to this app when full-system disaster recovery is
required.

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
removed. Scheduled calls queue the durable run asynchronously so a large
snapshot is not constrained by Jobs' app-to-app response deadline.

Backup and restore operations are globally serialized. A second operation is
rejected while one is active so platform snapshots cannot overlap a destructive
restore or run several full-database VACUUM operations at once.

Retention applies independently to each policy and scope. A value of `0` keeps
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

Keep the passphrase outside the Apteva host. Losing it makes encrypted backups
unrecoverable; changing it does not re-encrypt old backups, so restores require
the passphrase used when each backup was created.

## Restore Semantics

Platform app databases are swapped by the platform restore endpoint. The
platform database is staged and activated on the next `apteva-server` restart.
Fleet restores validate the archive's provider, tenant ID, and tenant slug
before replacing the selected tenant directory.

The global Backup installation uses its own install token through dedicated
platform callback routes. It never receives an administrator API key and it
cannot access the management snapshot routes. Snapshot and restore are separate
approved permissions, and every restore requires explicit operator confirmation.
Older servers without the app-authorized streaming capability are rejected with
a clear upgrade error; Backup does not fall back to an administrator credential.

The restore report is inspected entry by entry. If the platform applies some
databases but rejects others, Backup reports a partial restore and lists the
failed entries instead of presenting the operation as fully successful.

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
