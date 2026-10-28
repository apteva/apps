# Database 0.5.2

28 October 2026. Source tag: `database/v0.5.2`.

Large AptevaDB aggregates can now complete within a bounded working-memory budget by retrying through native streaming and disk spill. The adapter preserves the fast parallel executor for aggregates that fit, and keeps only the requested result window from streamed groups.

## Changes

- Pin AptevaDB v0.2.0-alpha.8 and app-sdk v0.110.0, the latest published SDK tag by commit topology during preparation.
- Retry native working-limit failures on the same transaction snapshot. Stream all completed groups through checked scalar conversion and a bounded top-K heap. Reserve three quarters of the query work budget for native execution and one quarter for caller-retained results. Preserve exact integer wire values, nullable counts, filters, ordering, truncation and index requirements.
- Validate groups discarded by pagination. Return no partial result on overflow, cancellation, scratch failure or cleanup errors. Staged writes and integer sums needing scan-order checks retain the checked portable fallback.
- Default aggregate scratch to a 1 GiB quota per query inside the owned database directory. Zero disables disk spill. Completion and cancellation clean execution files; native Open removes abandoned execution leases after a crash. The native group guard defaults to 1,048,576 and is configurable up to 16,777,216 without increasing other limits.
- Enable cold mapped-translation reclamation with a 64 MiB per-database soft target. Expose an optional managed-buffer target, disabled by default. Report on-demand memory diagnostics through `database_describe`; add the effective targets and aggregate policy to `explain` and startup logs. These targets do not cap process RSS.
- Include the previously merged, unreleased app controls: 64 MiB logical cache targets, an automatic total cache-target budget of at least 256 MiB, idle handle eviction/reopen, two expensive native reads at a time and at most 64 waiting requests. Active queries and watches pin their handles. Write buffering is independently configurable and disabled by default.
- Preserve the complete-primary-key COUNT rewrite and count-only native result-window optimization. SQLite and Pebble behavior is unchanged.

## Validation and performance

Regression tests cover spill/SQLite parity, exact integers above 2^53, nullable values, compound groups, indexed filters, result ordering, snapshot visibility, watch initial aggregates, omitted-group overflow, quota/group limits, deadlines, scratch cleanup and memory diagnostics. Release validation includes Go tests, race checks, vet, module verification, UI tests/build, fresh packaged execution and existing-data upgrade checks.

The opt-in `TestHugeAggregateBenchmark` harness measures adapter and direct-driver calls on fresh million-row fixtures. Its direct AptevaDB reference uses bounded native streaming when materializing execution exceeds the work budget, with the same memory split and top-K response work. SQLite uses its own native query planner and GROUP BY implementation. Both successful results and resource errors are checked. Raw timing trials, memory diagnostics, provenance and release checks accompany the GitHub release as benchmark evidence.

Spill enables workloads that previously failed; it does not guarantee SQLite parity for high-cardinality grouping. Native partitioning, allocation and spill I/O still cost time. Cold-page reclamation lowers idle/current RSS and can increase subsequent read latency; lifetime peak RSS may remain high. Uncertain integer reductions and staged-write aggregates retain the portable accumulator's limits.

## Upgrade

Native format remains 10. Database 0.5.1 native data needs no new format migration. Opening older native formats still permanently upgrades them to format 10; retain a backup created with the original binary. SQLite remains the default backend and AptevaDB remains an experimental alpha dependency. No running app installation is replaced by publishing this release.
