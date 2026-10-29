# Database 0.5.3

Prepared 29 October 2026. Source tag: `database/v0.5.3` (not yet created).

Calling apps can now use project-owned databases that a project operator explicitly grants them. AptevaDB moves to alpha.10 for its aggregation speedups and two durability fixes, and three adapter paths that were slower than they needed to be are fixed.

## Changes

- **Project database access for calling apps.** App-to-app calls always carry the bridge's bound-caller identity, so Database served them from the caller's private namespace: a GraphQL source asking for `prod` got "database prod does not exist" while the project's `prod` was never reached. Calling apps now pass `scope: "project"` to address a project database, and Database serves it only if a project operator granted that app `read` or `write` access with the new `db_access_grant` tool (or `POST /operations/access_grant`). `db_access_list` and `db_access_revoke` manage grants. Apps can never grant themselves access, manage schema or realtime capture, or reach ungranted databases; omitting `scope` keeps the private namespace, so existing callers are unchanged. Grants are stored in the app registry, keyed by project, database and the server-asserted caller app name, and are removed when the database is dropped.
- Pin AptevaDB v0.2.0-alpha.10 and app-sdk v0.111.0, the latest published SDK tag by commit topology during preparation. The `v0.2.0-alpha.10` tag points at release commit `420f38e`, whose CI passed after one rerun of an intermittent Ubuntu `TestChecksumDetectsCorruption` failure.
- Alpha.9/alpha.10 fixes a checkpoint panic when the free list sits just above a multiple of 4,092 ids. Since alpha.7 that panic deadlocked the checkpointer, hanging the database; alpha.8, shipped in 0.5.2, is affected. Creating a log generation now fsyncs the database directory, so a power loss right after rotation cannot lose acknowledged records in the new file.
- Re-enable the projected scan decoder. Its codec guard accepted only alpha.5, so every native scan since alpha.6 silently fell back to the full public decoder. `doc.go` is byte-identical from alpha.5 through alpha.10; the audited versions are now listed explicitly, and a test fails when the pinned version is not audited, instead of degrading silently.
- Unsorted native `find` pages decode only retained fields through the projected decoder, instead of decoding a complete map and cloning it.
- Filtered native aggregates pass their filter to the parallel executor as a leading `$match`. The native planner shards an index range or the full scan across the configured aggregate workers; previously only unfiltered aggregates were parallel and filtered ones ran one serial query. The streamed/spill retry already used the same `$match` translation.

## Validation and performance

`BenchmarkAptevaPaths` (new, `engine/aptevadb_upgrade_benchmark_test.go`) times the adapter paths an AptevaDB upgrade can move on a 200,000-record fixture with a tenant index: point reads, indexed and unindexed-sorted finds, unindexed counts, 4/1,000-group, indexed-filter, unindexed-filter and high-cardinality spilled aggregates, and indexed filtered updates. Builds ran interleaved in fresh processes; raw output and benchstat summaries are in `benchmarks/results/aptevadb-alpha10-2026-10-29/`. The workstation was heavily loaded (load average ~30 on 10 cores); only statistically significant changes (p < 0.05) are listed.

| Path | 0.5.2 (alpha.8) | 0.5.3 | Change |
|---|---:|---:|---:|
| High-cardinality aggregate (disk spill) | 556 ms | 213–250 ms | −55–62%, 36% fewer bytes |
| Unindexed sorted find, 50 rows | 184 ms | 106 ms | −42%, 60% fewer bytes |
| Unindexed filtered aggregate, 1,000 groups | ~39 ms (alpha.10 serial) | 9.6 ms | −74% |
| Unindexed count | 34.5 ms | 28.0 ms | −19% |
| 4-group aggregate | 10.8 ms | 8.6 ms | −20% (alpha.10) |
| Indexed-filter aggregate | 163 µs (alpha.10) | 145 µs | −12% |
| Indexed find page | 258 KiB/op | 172 KiB/op | −33% bytes, −18% allocations |

Point reads, 1,000-group aggregates and durable filtered updates are unchanged within noise. Small aggregates allocate about 64 KiB more per query inside alpha.10's executor without a latency change.

Go tests, race tests and vet pass with `GOWORK=off`. New tests cover the access model directly and through real bound-caller bridge headers: private default, explicit project scope, denial without a grant, read-versus-write, schema/realtime/grant management denied to apps, other apps and other projects isolated, revocation, and no grant revival after drop and re-create. Existing aggregate parity tests against SQLite cover the filtered parallel path, including indexed filters, exact integers, nullable counts and the spill retry.

## Upgrade

Native format remains 10. Database 0.5.2 native data needs no migration. SQLite remains the default backend and AptevaDB remains an experimental alpha dependency. No running app installation is replaced by publishing this release.
