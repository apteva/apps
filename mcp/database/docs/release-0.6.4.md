# Database 0.6.4

Released 2 November 2026. Source tag: `database/v0.6.4`.

On AptevaDB, several filtered `count_distinct` metrics without `groupBy` no longer fail with `resource_limit`.

## Changes

- **Pin AptevaDB v0.2.0-alpha.14: distinct counts for one oversized group.**
  - **The failure.** An aggregate without `groupBy` has one global group. When its distinct sets did not fit the work budget together, AptevaDB's spill tried to split them by group key. One key cannot be split, so it recursed until `429 resource_limit: aggregation partition depth exhausted`.
  - **Reported case.** Eight filtered distinct-caller counts over 64,084 records and 24,443 callers failed at the default 16 MiB work budget. Six fitted, and grouping by adviser or splitting the metrics into smaller batches also worked.
  - **The fix.** Alpha.14 counts such a group in disjoint slices of its values, one pass over the spilled state per slice, and adds the slice counts. Results stay exact; nothing is estimated.
  - **Merge accounting.** Alpha.14 also charges only new members when merging partial distinct sets from parallel workers.
  - **Test.** `TestGlobalFilteredDistinctCountsSpill` runs that data through `aggregate` with 6, 7 and 8 metrics on AptevaDB and SQLite, and checks every count. It fails on 0.6.3.
- Alpha.14 joins the projected decoder's audited versions (`doc.go` unchanged).

No API or storage change. AptevaDB format remains 10.
