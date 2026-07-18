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
- Policy- and scope-isolated object keys and retention
- Streaming local, AWS S3, and Cloudflare R2 uploads and restores
- Optional age encryption using an install-configured passphrase
- SHA-256 verification before every restore
- Per-tenant Fleet backup and restore for local tenants
- Soft-deleted destinations, preserving historical restore metadata
- Run history with scope, encryption, size, status, and failure details

## Scheduling

Backup requires the Jobs app. Each policy creates a Jobs `app_tool` target that
calls `backup.backup_now` with the policy ID. Policy creation is atomic from the
operator's perspective: if Jobs registration fails, the incomplete policy is
removed.

Retention applies independently to each policy and scope. A value of `0` keeps
all backups. Ad-hoc runs are stored under their own namespace and are not pruned
by scheduled policies.

## Cloud Storage

Cloud destinations use the install's optional `cloud_storage` binding and read
credentials through the SDK's restricted credential API. Supported connection
types are:

- AWS S3
- Cloudflare R2

One cloud account can be bound to a Backup install. Multiple destinations can
use different buckets or key prefixes within that account. Credentials are not
stored in Backup's database.

## Encryption

Set `encryption_passphrase` in the app configuration to encrypt new objects
with age before upload. The stored object's SHA-256 digest is recorded and
verified before decryption and restore.

Keep the passphrase outside the Apteva host. Losing it makes encrypted backups
unrecoverable; changing it does not re-encrypt old backups, so restores require
the passphrase used when each backup was created.

## Restore Semantics

Platform app databases are swapped by the platform restore endpoint. The
platform database is staged and activated on the next `apteva-server` restart.
Fleet restores validate the archive's provider, tenant ID, and tenant slug
before replacing the selected tenant directory.

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
