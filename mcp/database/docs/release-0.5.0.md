# Database 0.5.0

14 October 2026. Source tag: `database/v0.5.0`.

This release adds an optional embedded AptevaDB backend and opt-in native realtime feeds while preserving the shared SQLite/Pebble record API. SQLite remains the default. Existing databases keep their selected backend.

## Changes

- AptevaDB v0.2.0-alpha.5: typed records, exact integer fields, compound indexes, snapshot reads, atomic cross-collection record writes, recoverable schema publication and native aggregate fast paths.
- Native collection watches through authenticated SSE `POST /realtime/watch` and bounded `db_changes` MCP polling, with an atomic initial snapshot, signed resume cursors, commit invalidations, expiry recovery, scope/grant checks and watcher quotas.
- `db_realtime_configure` and `db_realtime_describe`; the panel can enable realtime and refresh the selected collection and last submitted query/aggregate without changing unsaved editor text. Invalidation resets record pagination to its first page.
- Adapter performance improvements: cached database/schema lookup, prepared SQLite point access, direct Pebble point reads, projected streaming decoders, bounded top-page sorting and native aggregation with portable integer-overflow safeguards.
- Configurable synchronous AptevaDB flush modes (`full`, `os`, `barrier`), aggregate workers, cache target and query work budget. Full flush remains the default. On macOS, OS/barrier provide weaker power-loss protection than full drive-cache flushes.
- Public app-sdk v0.102.0, selected by tag commit topology. Builds do not require a local workspace overlay.

## Upgrade and limits

**Back up native data with its original binary before upgrading.** AptevaDB alpha.5 permanently upgrades older native directories to format 10, even if change capture is disabled. An older binary cannot open the upgraded directory; reverting requires the original backup. Previously published Database 0.4.4 supports SQLite/Pebble only, so this warning mainly affects users of the unreleased native development builds.

Capture is per-database and opt-in. Its default logical retention is 128 MiB or one hour; physical WAL storage can exceed that target. Disabling capture ends resume history. SQLite/Pebble change journals, individual row-image feeds and immediate in-flight platform grant-revocation cancellation remain future work. HTTP watches reconnect through authentication at least every five minutes; MCP rechecks grants on each bounded poll.

The existing app-level 1 MiB record limit, result limits, retained-work limits and synchronous index-build limits remain. Backend-native maximum sizes do not raise the portable app contract. High-cardinality aggregates can hit the retained-work budget even when their output limit is small.

## Validation and performance

The Go suite and race checks, `go vet`, module verification, Bun client tests and panel build passed. Real SDK sidecar tests cover bearer authentication, initial snapshot/ready/invalidate delivery and cleanup on disconnect. Snapshot/write races, replay, rollback, restart, expiry, scope isolation, forged cursors, drop/recreation and quotas are covered.

A genuine backed-up alpha.4 format-9 fixture upgraded to format 10, preserved an exact MaxInt64 document and emitted a native watch event. The old binary refused the upgraded files without mutating them, while the original backup remained readable.

Pre-release local macOS measurements include three fresh 100,000-row fixtures per capture/watch configuration and two fresh million-row processes per backend. At 32 watchers, ordinary-fsync single inserts increased from about 72 to 93 microseconds; batch timings showed no consistent slowdown. Full-flush batch medians were about 8.82 ms capture-off and 8.81 ms with 32 watchers. A million-row, four-group numeric aggregate was 42.85 ms capture-off versus 42.95 ms with capture and ten idle watchers. These measurements used SDK v0.82.0; release compatibility checks were rerun on v0.102.0. No browser-paint or production-network latency is claimed.

See [realtime API and limits](../REALTIME.md), [full benchmark comparisons and ranges](../benchmarks/REALTIME.md), [matched flush profiles](../benchmarks/FLUSH-PROFILES.md), and the checked-in [raw measurement evidence](../benchmarks/results/database-realtime-2026-10-14).
