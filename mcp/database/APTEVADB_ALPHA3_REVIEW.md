# AptevaDB alpha 3 integration review

Historical design/review notes. See [the current implementation](REALTIME.md) and [release notes](docs/release-0.5.0.md).


13 October 2026. Verified published tag: `v0.2.0-alpha.3`, commit `26220edae0452a03a5cb5375016ca9eaf1e95209`.

**Integration review: green. The optional embedded `aptevadb` backend is now implemented on the development branch; see [implementation and measured results](benchmarks/APTEVADB-INTEGRATION.md).** Both remaining alpha.2 findings pass against the exact published alpha.3 source. The app's separate primary-key and index-tuple budgets fit the new native index-entry allowance. No remaining engine blocker from the earlier review was found in these checks. The sections below record the original integration gate. Completed adapter validation and local measurements are in the implementation report.

## Verified fixes

Reusing an index cursor after traversal and seeking an earlier saved position now returns the expected row. Both `SeekAfter` and `SeekBefore` pass the independent regression. The implementation clears the cached leaf for an unrelated exclusive seek while retaining it for sequential movement.

The 1,500-byte primary key plus 3,000-byte indexed text example now creates and reads its index successfully. More demanding checks also pass together:

- A canonical JSON primary key of exactly **2,048 bytes**.
- An ASCII scalar index tuple of exactly **4,096 encoded bytes**.
- The same maximum tuple size with NUL escaping: 2,046 NUL bytes plus one ordinary byte, the scalar tag and terminator.

Native user keys remain limited to 4,032 bytes; complete index entries have an independent 8,160-byte allowance. The native entry includes tuple framing and an escaped primary-key suffix, so the distinct limits matter. With the proposed canonical JSON primary keys, the app's maximum tuple/key pair has room for native framing and numeric-encoding differences. Keep explicit physical-size checks in the adapter and test eight-field mixed tuples too.

## Local validation

All of these passed on the published tag:

| Check | Result |
|---|---|
| Native `go test ./... -short` | PASS |
| Independent integration probes under `go test -race -v .` | PASS |
| Focused native race tests for reused cursors, wide index entries, mixed tree splits, WAL recovery, snapshot maintenance and format-7 fixture upgrade | PASS |
| Original alpha.1 integer-filter and 80 KB document probes | PASS |
| Decoding the alpha.1 document with wrapped root offsets | PASS |
| Compound exact numeric order, nested logical/literal-prefix filters, plans, budgets, nullable uniqueness and 1 MiB documents | PASS |

The full native race/fuzz/CI/downloaded-server results are reported by the upstream release. The independent local race runs here cover the integration probes and changed cursor/index/recovery paths; a full native race rerun was not needed to resolve these findings.

The default unique-index null behavior still constrains null. Configure `NullsDistinct: true` for the app's nullable-unique contract; this is a supported policy choice, not a remaining defect.

## Implementation scope

Pin `github.com/apteva/db@v0.2.0-alpha.3` and implement a third adapter behind the existing Database app contract. Keep SQLite as the default and the SDK/control registry on SQLite. Open each selected native database once with `DurableOptions`, share its handles, and close owned cursors/snapshots/readers during shutdown.

Preserve the existing common validation, integer wire strings, timestamps/versions, authorization and signed keyset pagination. Use native snapshots and ordered index cursors for each page request. Translate the app's patch-style upsert inside a transaction rather than calling whole-document replacement directly. Schema catalog publication still needs an atomic metadata mechanism or a documented durable intent/recovery protocol because native DDL is outside record transactions.

After implementation, run the existing app tests across SQLite, Pebble and AptevaDB, including crash/reopen, schema recovery, unique/version conflicts, cross-collection rollback and cursor invalidation. Then benchmark the complete adapter against direct native calls and the optimized existing adapters with matched durability and identical result serialization.

Published alpha.3 page benchmarks use asynchronous loading and distinct cache models. Their disclosed improvements and regressions support evaluating the engine; they do not establish the durable app adapter's performance before integration.

Format 8 permanently upgrades older native directories. This review opened only disposable test data and historical test fixtures copied by the native suite. No installed app database was migrated.

## Evidence

- [Published alpha.3 release](https://github.com/apteva/db/releases/tag/v0.2.0-alpha.3)
- [Upstream benchmark and validation report](https://github.com/apteva/db/blob/main/bench/results/alpha3/VALIDATION.md)
- Independent probe source (local historical artifact: `aptevadb-backend-proposal-2026-10-13/checks-alpha3/feature_test.go`)
- Independent race probe results (local historical artifact: `aptevadb-backend-proposal-2026-10-13/alpha3-feature-checks.log`)
- Original bug probe results (local historical artifact: `aptevadb-backend-proposal-2026-10-13/alpha3-compatibility.log`)
- Tagged-source test results (local historical artifact: `aptevadb-backend-proposal-2026-10-13/alpha3-tests.log`)
- Targeted native race results (local historical artifact: `aptevadb-backend-proposal-2026-10-13/alpha3-targeted-race.log`)

Reproduce the integration probes from their directory:

```sh
GOWORK=off GOTOOLCHAIN=local go test -race -v .
```
