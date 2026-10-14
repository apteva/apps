# AptevaDB backend for the Database app

Historical design/review notes. See [the current implementation](REALTIME.md) and [release notes](docs/release-0.5.0.md).


Current verified status: [alpha.3 is green for adapter implementation](APTEVADB_ALPHA3_REVIEW.md). The original bugs and alpha.2 cursor/index-limit findings are resolved. The alpha.1 assessment below is retained as the original design baseline; use alpha.3 as the integration dependency.

Draft, 13 October 2026. Target: `github.com/apteva/db@v0.2.0-alpha.1`, commit `f97b8944dbf5119e72c89bf92e330816da8a48d4`.

Add `aptevadb` as a third, optional embedded adapter beside SQLite and Pebble. Each named database should own an AptevaDB directory and expose the existing typed Database app API. Keep SQLite as the default. Expose the native adapter as experimental only after the correctness issues below are fixed and its supported capabilities are explicit; require full contract parity before treating it as interchangeable with the existing adapters.

The native engine already supplies concurrent collection handles, durable writes, cross-collection transactions/batches, snapshots, primary cursors, single-field unique indexes, aggregation, online backup and verification. Those are enough to justify integration work. Its document API is not a direct substitute for the Database app contract.

## Required engine work

| Priority | Requirement | Current behavior and integration consequence |
|---|---|---|
| P1 | Exact integer query operands | `Collection.Query` decodes filters through `json.Unmarshal`, which converts numbers to float64. An equality filter for `9007199254740993` matched both adjacent integers around 2^53. Pipeline parsing uses `UseNumber` and matched correctly. Share exact parsing across Query, FindOne and aggregation, with boundary tests. |
| P1 | Safe document size handling | A document containing two 40,000-byte strings and a small third field encoded and persisted successfully, then failed to decode. The uint16 field offsets wrap once the layout grows beyond their range. Reject unrepresentable documents before writes immediately; extend/version the document layout to fit every record allowed by the app's 1 MiB user-record cap, including service metadata and encoding overhead, before full parity. String, nested-document and array lengths also have 16-bit limits. |
| P1 for parity | Compound indexes and ordered index iteration | `CreateIndex` accepts one field/path. The app supports up to eight fields with mixed ascending/descending order. Add atomic compound index definitions, seek/range iteration on snapshot indexes, reverse traversal and deterministic primary-key tie-breakers. The existing Cursor iterates primary keys; it does not expose ordered secondary-index traversal. |
| P1 for parity | Larger physical key and index budgets | Native user keys and complete index-entry keys are limited to 1,024 bytes. The app accepts 2,048-byte logical primary keys and 4,096-byte index tuples. Size the physical engine limits for encoded tuples, primary-key suffixes and engine prefixes, not just the user-visible limit. Validate boundaries without silently narrowing the other adapters. |
| P1 for ordered queries | Exact integer index ordering | Current numeric index values encode float64, and exact predicates/uniqueness recheck original records. Adjacent large integers can share an access-path value. That can support candidate filtering, but an ordered index cursor must preserve exact int64 order rather than order collisions by primary key. |
| P2 | Logical predicates and literal prefixes | Native match parsing supports scalar comparisons and `in`; it does not implement the app's arbitrary nested `and`/`or` or literal `starts_with`. An `or` filter returned no rows without an error in the probe. Add the operators, or have the adapter explicitly evaluate residual predicates within the app's work/deadline bounds. Unsupported query operators should not appear to succeed. |
| P2 | Nullable uniqueness policy | Native unique indexes allow only one null owner; both current app adapters allow multiple nulls. Add an index option equivalent to nulls-distinct, or use an adapter-owned uniqueness representation. Do not replace explicit null with a missing field in the public record. |
| P2 | Query plan and work accounting | Expose the chosen index, scan/sort behavior, residual predicates and work counters. The adapter must implement `explain`, enforce `requireIndex`, and retain the app's 16 MiB retained-work budget, deadlines and 4 MiB response budget. Row/group limits alone do not bound retained bytes. |

The first two rows are reproduced correctness defects. The remaining rows describe missing capabilities or contract differences, rather than missing transactions or durability.

## Work owned by the adapter

Use the embedded root package, not the standalone HTTP server or remote driver. Store each database at `ctx.DataDir()/database/databases/<database-id>/aptevadb/` and open it once with `db.DurableOptions()`. The existing scope/name resolver supplies the database ID; users never select filesystem paths. Cache collection handles and close all readers/snapshots/transactions before checking the database close error during shutdown. Preserve one-process directory ownership.

Implement `engine/backend` in `engine/aptevadb.go`, with transaction, snapshot and point-read implementations. Reuse the common schema validation, exact wire types, service metadata, authorization, optimistic version checks, query AST and authenticated cursor handling. The earlier database/schema caches and point-read optimizations should apply to this adapter too.

Keep the manager registry and SDK app database on SQLite initially. Adding a managed record backend does not require replacing either internal database.

Map portable integers and `_version` from decimal strings to native int64 for storage, then back to decimal strings on the wire. Preserve `number` as float64, normalized datetime strings and nullable fields. Encode declared `json` fields as lossless canonical JSON payloads, because the common API does not query/index their nested contents and the native document codec has additional field-count/name/depth limits. Decode those payloads with exact number handling on return; do not impose native nested-document limits on otherwise valid app JSON fields. Prefer a schema-aware codec and `DecodeDocument` over `FindDocument` into an untyped map: the convenience decode path marshals and unmarshals JSON again, which adds allocations and can turn untyped integers into float64. Retain the typed distinctions through query compilation and aggregation as well.

Treat native `UpsertDocument` as whole-document replacement. App `upsert` patches supplied fields. Read the existing record in a transaction, merge and validate the patch, preserve `_created_at`, increment `_version`, and replace the merged document. Implement atomic `ifVersion`, unique-conflict upsert and multi-collection `db_batch` in the same transaction. Keep callbacks free of external side effects. The current app serializes writes per database, so start without an additional adapter write queue; native group commit can be evaluated separately under concurrent load.

Transactions can read staged writes, but their public collection API opens only existing collections and DDL is outside transactions. Keep schema operations separate from record transactions. Store typed schema/index/version metadata in a private catalog. Publishing that catalog and native DDL together needs either upstream transactional DDL/catalog metadata support or an explicit durable intent/recovery protocol. An ordinary two-call sequence is insufficient for crash consistency. Prefer upstream atomic collection metadata hooks; test create/drop/index recovery before activation.

Map native errors to existing app errors: duplicate primary/unique keys to `unique_conflict`; missing collections to `not_found`; size/staging exhaustion to `resource_limit`; corrupted data or failed durable writes to `storage_error`. An optimistic transaction conflict must not become a client `ifVersion` conflict automatically. Failed I/O commits have uncertain outcomes: stop using the handle and propagate the failure; do not transparently retry an acknowledged-or-unknown write.

Retain the app's signed keyset cursors. Each page obtains a fresh snapshot, uses the full sort tuple and primary-key tie-breaker, and closes the snapshot before returning. Do not keep native Cursor objects alive between HTTP requests. Until ordered index seek exists, a bounded sort is an explicit fallback and cannot satisfy `requireIndex` for the corresponding access path.

## Integration changes

| Location | Proposed change |
|---|---|
| `go.mod` | Pin an exact native DB tag after the required fixes; alpha.1 is the inspected baseline, not the production integration target. |
| `engine/database.go` | Recognize `adapter: "aptevadb"`, open its directory, preserve the selected adapter on reopen, and add explicit capability checks. |
| `engine/aptevadb.go` and codec/query helpers | Implement lifecycle, transactions, point reads, records, indexes, queries, aggregation and explain. |
| `main.go` and `apteva.yaml` | Add the adapter to tool schemas and descriptions; retain existing permissions and routes. |
| `ui/DatabasePanel.tsx` | Add “AptevaDB (experimental)” after activation gates pass; show concrete limitations when selection affects a request. |
| `engine/*_test.go` and benchmarks | Include the new adapter in parity, crash/reopen and native-driver comparison matrices. |
| README and usage skill | Document selection, persistent storage, supported operations, limits and backup/restore. |

A database is created with:

```json
{"database":"events","adapter":"aptevadb"}
```

The adapter choice remains fixed for that database. Moving existing SQLite/Pebble data requires a separate export/import into a fresh named database, verification and an explicit client binding change. No existing data is rewritten by enabling the option.

## Rollout and acceptance

1. **Engine fixes and contract agreement.** Fix exact filter decoding and document offset overflow. Agree on large-document/key limits, compound ordering and nullable uniqueness. Add reproductions to native DB tests, including reopen/recovery for accepted document boundaries.
2. **Internal adapter prototype.** Implement lifecycle, typed codec, transactions and point reads against disposable data. Keep it outside the normal selector while unsupported capabilities remain. Exercise large integers, escaped/composite keys, input immutability, scope separation and dropped/recreated handles.
3. **Contract parity.** Run the existing API/adapter suite across all three engines. Include duplicate keys, patch upserts, version checks, rollback across collections, unique swaps/nulls, mixed-direction compound indexes, schema/index cursor invalidation, pagination during writes, deadline/byte limits and truthful explain plans. Add process-kill recovery, disk-full/flush-failure injection, atomic catalog recovery, backup verification and restore.
4. **Durable performance comparison.** Run direct native DB methods, the new adapter, optimized SQLite and Pebble sequentially in fresh processes on identical typed fixtures. Compare 10k/100k/1m rows, secondary indexes, point reads, sorted 50-row pages, counts/aggregations, 1/1,000-row writes and concurrent readers/writers. Include identical result serialization and create-only/patch semantics. Record p50/p95, errors, allocation/RSS, disk, startup/recovery and source/build hashes. Match flush strength explicitly: on macOS native `DurableOptions` uses full flushing, while SQLite's current WAL/FULL profile does not enable `fullfsync`. Historical asynchronous KV benchmarks cannot establish a durable adapter advantage.
5. **Optional experimental release.** Expose the selector with an explicit capability/limit contract after crash and correctness gates pass. Keep SQLite as the default. Promote to full parity only when the common contract suite passes without backend-specific omissions.

Computed fields, computed collections, joins and the native remote server are useful future capabilities, but they are outside this adapter's first scope. Native DB already implements much of that functionality; the initial integration should focus on delivering the existing Database app contract reliably.

## Evidence

Source inspected: [AptevaDB alpha.1](https://github.com/apteva/db/tree/v0.2.0-alpha.1), commit `f97b8944`.

The local compatibility probe uses an isolated temporary directory and the exact tagged source. It creates no app installation and opens no user data:

- Probe source (local historical artifact: `aptevadb-backend-proposal-2026-10-13/checks/main.go`)
- Observed results (local historical artifact: `aptevadb-backend-proposal-2026-10-13/compatibility.log`)
- Native document encoding (local historical artifact: `aptevadb-backend-proposal-2026-10-13/source/doc.go`)
- Native query implementation (local historical artifact: `aptevadb-backend-proposal-2026-10-13/source/documents.go`)
- Native index implementation (local historical artifact: `aptevadb-backend-proposal-2026-10-13/source/index.go`)
- Native transaction contract (local historical artifact: `aptevadb-backend-proposal-2026-10-13/source/USAGE.md`)
