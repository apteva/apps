# Database 0.5.1

27 October 2026. Source tag: `database/v0.5.1`.

Filtered row counts on AptevaDB now use its native `CountDocuments` API. Scalar equality prefixes covered by an index can count matching index entries without fetching documents. A dependency upgrade alone did not enable this path in Database 0.5.0.

## Changes

- Pin AptevaDB v0.2.0-alpha.7 and app-sdk v0.109.0, the latest published SDK tag by commit topology at release preparation.
- Route ungrouped row-count summaries and `count` operations through native counting when there are no staged writes. Preserve the transaction-aware fallback for staged writes and retain snapshot visibility, cancellation, work budgets and `requireIndex` checks.
- Omit the redundant non-null guard for equality to a non-null value, allowing the native planner to prove index coverage. Other operators keep portable null handling.
- Preserve field-count non-null semantics and existing grouped and numeric aggregate paths.

## Validation

Regression tests cover compound indexes with descending components, exact large integers, null and missing fields, false and empty-string values, residual/range/OR filters, filtered and unfiltered summaries, index requirements, cancellation, snapshot visibility and rollback of staged writes. Tests exercise both the default cache and a bounded cache with buffered writes, before and after checkpointing.

The opt-in `engine/query_diagnostic_test.go` harness compares the in-process adapter with direct-driver references on three fresh 100,000-row fixtures, including exactly 5,000 indexed matches. Set `DB_QUERY_DIAGNOSTIC_OUTPUT` and run with `-tags aggregatebench`. Raw results and release verification evidence accompany the GitHub release.

## Upgrade and remaining limits

The native format remains 10; upgrading from Database 0.5.0 requires no new format migration. Opening older native formats still permanently upgrades them to format 10, so retain a backup made with the original binary. SQLite remains the default backend and AptevaDB remains an experimental alpha dependency.

Index-only counting currently covers supported scalar equality prefixes. Ranges, OR and residual predicates may still fetch documents. Field counts and grouped aggregates have their existing behavior and work limits. Pebble's filtered count path is unchanged. Native realtime feeds remain available; SQLite/Pebble change journals remain future work.
