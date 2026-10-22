# Native snapshot limits and progress

Instances publishes Server-compatible `state_transfer` policy plus detailed `snapshot_limits` in the protected native backend
`GET /describe` response. Sizes are integer bytes:

```json
{
  "state_transfer": {
    "format": "tar.gz",
    "max_archive_bytes": 8589934592,
    "max_expanded_bytes": 8589934592,
    "max_entries": 100000,
    "timeout_seconds": 7200
  },
  "snapshot_limits": {
    "max_archive_bytes": 8589934592,
    "max_expanded_bytes": 8589934592,
    "max_file_bytes": 8589934592,
    "max_entries": 100000,
    "tar_overhead_bytes": 134217728,
    "hard_max_bytes": 17179869184,
    "disk_reserve_bytes": 268435456,
    "timeout_seconds": 7200
  },
  "snapshot_progress": {
    "endpoint": "/_apteva/runtime/backends/native/state-progress",
    "poll_interval_ms": 1000
  }
}
```

`max_expanded_bytes` limits cumulative regular-file content and every individual
file. The uncompressed tar stream (including headers, padding, paths and trailer)
is separately bounded to that value plus `tar_overhead_bytes`. Existing entry,
path length, 16 MiB path metadata and safe file-type restrictions remain in force.
Executable/package limits are separate and unchanged.

## Configuration

Use the existing app installation settings; values apply when Instances restarts:

| Install configuration | Environment override | Default | Hard range |
| --- | --- | --- | --- |
| `native_snapshot_max_archive_bytes` | `APTEVA_NATIVE_SNAPSHOT_MAX_ARCHIVE_BYTES` | 8 GiB | 1 byte–16 GiB |
| `native_snapshot_max_state_bytes` | `APTEVA_NATIVE_SNAPSHOT_MAX_STATE_BYTES` | 8 GiB | 1 byte–16 GiB |
| `native_snapshot_timeout_seconds` | `APTEVA_NATIVE_SNAPSHOT_TIMEOUT_SECONDS` | 7200 | 60–21600 seconds |

An explicit environment value takes precedence over install configuration. Empty environment overrides,
non-integer, zero, negative and out-of-range configured values are rejected at backend
initialization. The capability response reports the effective configuration.

## Server interoperability

This implementation matches the local Server `state_transfer` contract in
`docs/app-runtime-backends.md` and `apps_runtime_transfer_policy.go` (checked
2026-10-22). It uses the same field names and `tar.gz` format, enforcing the
requested lower archive/expanded/entry/deadline policy from
`X-Apteva-State-Transfer-Policy` JSON on both snapshot and restore. Invalid,
incomplete or unsupported policies fail before staging or mutation. Server may
also send `state_transfer` on lifecycle requests; Instances validates it before
handling the request. Policies are persisted with snapshot/restore receipts so
conflicting retries cannot silently change them. Recovery of an already committed
remote receipt remains possible without rewriting state.

Absent transfer headers retain the legacy 512 MiB / 100,000 entries / 1200 second
behavior (further bounded by any smaller configured backend capacity). An absent
capability must likewise retain Server's legacy fallback. Old Servers therefore
remain supported but do not gain large migration support merely by updating this
app. Server's negotiated policy support must be running and the backend's
capabilities re-registered/refreshed for large transfers. Instances caps entries
at 100,000 even when Server supports a larger count. Executable/package limits
remain independent and unchanged.

Execution `inspect` reports `state_bytes`, `state_entries` and
`state_available_bytes`. `inspect-target` reports `state_available_bytes` for the
existing state filesystem or nearest existing ancestor without provisioning any
resource. Unknown/unsafe/unreadable metadata stays null, never invented zero.
Running-state size estimates are cached for at most one minute by deployment,
data directory and target identity; ordinary health polling does not repeatedly
walk the data tree or open another SSH connection. Negotiated inspection and
stopped/prepared-state checks request fresh counts, within the existing combined
SSH operation. Free space is read from the state filesystem on each inspection. Metadata walks
have a two-second remote budget (`timeout`); an unavailable/slow scan reports
unknown size/count instead of failing ordinary service health. GNU coreutils and
GNU find are required for complete snapshot metadata.
Server owns pre-drain negotiation, controller cache budgeting, rollback provenance,
routing and process sequencing. No Server source is changed in this app release.

## Transfer and disk accounting

All snapshot/restore requests continue to require a deployment ID, existing backend
authentication, and `Idempotency-Key`; restores require `X-Apteva-Snapshot-SHA256`.

Snapshots also return `X-Apteva-Snapshot-Expanded-Bytes`: cumulative file content,
excluding the internal restore marker. A restoring controller may forward that
header as an exact expanded-size declaration. Instances checks it against the
validated archive before transfer. A missing declaration causes conservative disk
preflight using the negotiated expanded maximum. The current local Server sends
the policy header rather than this optional size declaration; it still works
without the declaration, with conservative headroom requirements. It is never trusted as a size
limit or used to bypass actual file/cumulative/decompression checks.

Before accepting a restore body, Instances checks remote ownership, stopped
writers and target free space, then checks archive staging space on its own
filesystem. A known HTTP length reserves that archive size; a chunked/unknown
length reserves the negotiated archive maximum. Both kinds of upload have the
same actual streamed byte limit. Snapshot creation reserves a conservative output
bound from measured source size and the archive limit. The previous local cache
is reclaimed under the existing cross-process lock before reserving new space.

Remote free-space checks use space available to the process, leave a 256 MiB
reserve and allow one target-filesystem allocation block of overhead per entry (the maximum entry count
when metadata is not yet known). The allocation size is read again under the
remote lock immediately before extraction. Existing state remains on disk throughout
extraction and already reduces reported free space: it must not be counted twice.
The remote host receives a streamed canonical tar, not an additional compressed
archive. Only the controller stores that archive. Budget independently for any
Server-side cache or other filesystem.

Target space is checked again after full local validation and under the remote
lock immediately before extraction. An owned interrupted staging directory can
be reclaimed on retry; its intent/ownership are verified first and actual free
space is rechecked after removal. Unrelated staging data is not used as a source
of available space. Filesystem races, quotas or changing allocation can still
cause ENOSPC; these errors retain the old state and recovery journal.

Canonical tar re-encoding, SHA-256 hashing, compression, decompression and transfer
use bounded streams/buffers. There is one serialized archive cache and one latest
progress receipt, with no chunk history. Cache reuse validates both the digest and
the currently selected expanded-state and entry limits, including after a configuration reduction.

## Progress and errors

Poll `GET /state-progress?deployment_id=...` under the same backend prefix and
credentials, with the transfer's `Idempotency-Key`. It does not acquire the state
operation lock. Progress is deployment/key scoped; another operation returns 404.
The latest transfer replaces the single progress receipt. Bytes/total apply to
that phase; total may be absent for streams with unknown length. Phases include
preflight, upload/create_snapshot, verify_checksum, verify_archive/verify_cache,
restore, verify_restore and download.

The receipt is atomically replaced at phase boundaries and at most once per
second during streaming. It reports timestamps, phase byte counts, status and
stable failure codes. It omits keys, credentials and remote command output.
Polling an unfinished receipt after a controller restart reports `unknown` and
`requires_inspection: true`, never invented completion. Durable execution/restore
receipts remain authoritative for recovery.

| HTTP status | `error.code` | Meaning |
| --- | --- | --- |
| 413 | `archive_too_large` | Compressed/upload limit exceeded |
| 413 | `expanded_state_too_large` | File/content/tar expansion budget exceeded |
| 400 | `incomplete_transfer` | Truncated upload/stream or cancellation |
| 400 | `checksum_mismatch` | SHA-256 or gzip CRC mismatch |
| 400 | `invalid_archive` | Unsafe paths/types, corrupt archive or incorrect expanded-size declaration |
| 507 | `insufficient_disk_space` | Preflight headroom, ENOSPC or quota failure |
| 503 | `state_transfer_failed` | Other transport/verification failure; inspect before retry |

Existing auth, ownership, busy, stopped-writer and conflicting-retry HTTP errors
remain compatible. A failure after a restore may have run includes
`error.outcome: "unknown"` and `error.requires_inspection: true`. Do not infer that
an SSH timeout means failure or retry with another digest. Inspect/retry the
existing durable operation. Successful restoration still echoes the verified
`X-Apteva-Snapshot-SHA256` and publishes `snapshot_sha256` through inspection.

No lifecycle, directory locks, stopped-process/cgroup checks, SQLite WAL/SHM/journal
preservation, digest receipts, atomic replacement or rename-boundary recovery is
removed. This feature does not start/stop unrelated services or change Server.
