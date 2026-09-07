# Immutable source exports

Code 0.10.0 provides a generic source handoff for any downstream builder. This
release changes Code only. Existing Deploy versions still require inline ZIPs;
they must adopt this contract to automatically transfer large source snapshots.

## Capture once, read the exact bytes

Call `repos_export` with `slug` and optional `metadata_only: true`. Code captures
source under its repository mutation lock, streams a deterministic ZIP to disk,
and atomically stores it by SHA-256. Binary assets and executable modes survive;
Code's existing generated/vendor exclusions apply. Symlink files are rejected.

The response contains:

- `snapshot_id`: the archive's lowercase SHA-256.
- `source_revision`: `sha256:<digest>`, identifying working-tree source including
  uncommitted edits; this is not a Git commit ID.
- `sha256`, compressed `size`, `format: "zip-v1"`, and `expires_at`.
- `download_url`: a relative authenticated gateway URL containing the snapshot ID.
- `inline` and, below `CODE_EXPORT_INLINE_BYTES`, `zip_b64` unless metadata-only.

Consumers should persist the receipt before transferring or building. Read large
archives through the existing authenticated app binding using
`repos_snapshot_read(slug, snapshot_id, offset, limit)`. `limit` is at most 1 MiB.
Each response contains `snapshot_id`, `sha256`, `size`, `offset`, `next_offset`,
`eof` and `data_b64`. Verify identity, offsets, final size and SHA-256 before
extracting. Never silently replace a pinned source snapshot with a new export.

The download URL serves the same stored bytes, requires normal gateway auth,
and supports HTTP Range and ETag. It is not a public signed link. The ordinary
HTTP export without `snapshot_id` remains a live export for compatibility.

`repos_export` with `snapshot_id` reopens that archive without recapturing source.
Edits after capture do not affect it. Snapshots persist across process restarts.
Missing or expired IDs fail explicitly; create a new snapshot intentionally.

## Multi-project repositories

Capture only one project by supplying `subdir`, for example:

```json
{"slug":"games","subdir":"games/client","metadata_only":true}
```

The archive root becomes that directory: `games/client/index.html` is exported
as `index.html`, and sibling projects are omitted. The same interface works for
server projects and native clients. Omit `subdir` or use `.` for the whole repo.
Relative real directories are required; traversal, absolute paths and symlink
directories are rejected. `subdir` cannot be combined with `snapshot_id` when
reopening: the ID already identifies the selected archive. Chunk reads need
only the repo slug and snapshot ID. Consumers should retain their original
subdirectory selection alongside the receipt if they need its provenance.

If a project requires shared parent files, export the repository root instead
and configure the downstream build command appropriately.

## Bounds and retention

| Setting | Default |
| --- | --- |
| `CODE_EXPORT_INLINE_BYTES` | 8 MiB compressed |
| `CODE_SNAPSHOT_MAX_BYTES` | 1 GiB compressed and expanded |
| `CODE_SNAPSHOT_MAX_FILES` | 100,000 files |
| `CODE_SNAPSHOT_CACHE_BYTES` | 8 GiB retained archives |

Snapshots are retained for 24 hours from capture. Capturing identical bytes
renews retention; reopening/reading does not. Expired snapshots are pruned when
new source is captured. A full cache rejects new captures without evicting live
snapshots. Capture uses one additional bounded temporary archive; failed
captures remove their temporary files. Captures serialize to bound scratch disk
pressure. Large assets use the snapshot limit independently of the editor limit.

Code mutations are locked during capture. Detected external source changes
cause capture to fail; this is not an OS-level filesystem transaction against
arbitrary external writers. Once stored, the snapshot is fixed. Hashes check
integrity within the authenticated app binding, not publisher identity.

## Validation

Unit tests cover deterministic bytes, source edits, binary assets larger than
the editor limit, modes, subdirectory roots, scope, traversal/symlink rejection,
expiry, restart persistence, cache budgets, cancellation and HTTP ranges.
`TestSidecarSourceSnapshotTransport` boots real Code, transfers a multi-chunk
snapshot, validates size/hash/content, checks auth and ranges, and proves later
edits and sibling projects do not enter the captured archive.
