# Tables 0.1.15 implementation and validation

Implemented from `tables/v0.1.14` (`c04ba353`) in `codex/tables-hardening`.
Release preparation uses isolated worktrees; no production database, running
deployment or original checkout was changed.

## Companion releases

- Tables: `/Users/marcoschwartz/Documents/code/apps-tables-hardening/mcp/tables`.
- Dashboard: `/Users/marcoschwartz/Documents/code/dashboard-tables-hardening`,
  branch `codex/tables-deep-links`. Project-aware links and event-stream reconnect notification.
- SDK: `/Users/marcoschwartz/Documents/code/app-sdk-tables-hardening`,
  branch `codex/tables-lossless-mcp`. Opt-in exact numeric decoding, leaving other apps' float64 behavior unchanged.

App SDK **v0.74.0** is published and pinned in Tables' `go.mod`/`go.sum`.
Dashboard **v0.34.2** includes the companion navigation and reconnect fixes.
Release validation uses the published SDK directly with `GOWORK=off`, without
a local module replacement. Follow the database upgrade notes in
[CHANGELOG.md](CHANGELOG.md). The additional app-call permission and optional
Storage binding are handled by the platform when upgrading an installation.

## Validation

- Tables: **105 top-level Go tests passed**, including 41 added backend regression
  tests; **77.0% statement coverage**. Existing tests remain present.
- Tables UI: **9 tests passed**, including stale request isolation, cursor use,
  exact edit tokens, dirty patches, input/default handling, duplicate submissions,
  lossless JSON, and dialog keyboard behavior. All UI source type-checks.
- Dashboard: **258 tests passed**, including 3 added regressions; production build passed.
- SDK: **139 top-level tests passed** across its packages, including 2 new numeric
  decoding tests; race suite and `go vet` passed.
- Tables race suite passed with the companion SDK; `go vet` passed. The published
  v0.74.0 pinned-SDK suite also passed. All four Tables bundles and source maps rebuilt.
- Real sidecar smoke test passed over localhost HTTP/MCP: exact large JSON inputs
  and defaults, cursor pages, projected optimistic updates, graceful restart,
  legacy timestamp/schema migration and the first write after upgrade.
- The shared panel verifier passed all four Tables bundles. Its workspace-wide
  run still reports **two pre-existing failures** in DeployPanel.mjs and
  GigsPanel.mjs (development JSX runtime imports). Both were verified unchanged
  in the v0.1.14 baseline; those unrelated apps were not modified.

Commands used:

```sh
# From mcp/tables:
env GOWORK=off go test -count=1 -coverprofile=/tmp/tables-coverage.out ./...
env GOWORK=off go vet ./...
# Release checks use the published SDK pin, without a local replacement:
env GOWORK=off go test -race -count=1 ./...
env GOWORK=off go build -o /tmp/tables-smoke .
python3 scripts/smoke.py /tmp/tables-smoke
# From mcp/tables/ui:
bun run typecheck
bun test
bun run build
# From the dashboard worktree:
bun test
bun run build
# From the SDK worktree:
env GOWORK=off go test ./...
env GOWORK=off go test -race ./...
env GOWORK=off go vet ./...
```

## Performance

Apple M1 Pro, Darwin ARM64; in-memory SQLite, warm handler benchmarks,
`-benchtime=500ms -count=3`. Medians below. Write comparisons use a freshly
measured 0.1.14 baseline and the completed hardening implementation. Cursor and
projection comparisons use two alternatives within the hardened app. These
are workload-specific results, not production latency promises or additive gains.
The host showed substantial timing variation; raw runs are retained.

| Workload | Before / alternative | Hardened / optimized | Speedup |
|---|---:|---:|---:|
| Insert 1,000 rows, 8 columns | 9.897 ms | 6.537 ms | 1.51× |
| Upsert 500 existing rows among 20,000 | 8.379 ms | 5.048 ms | 1.66× |
| Fetch 50 rows after 190,000: offset vs opaque cursor | 3.926 ms | 0.169 ms | 23.26× |
| Read 20 wide columns vs 2-column projection | 5.820 ms | 0.823 ms | 7.07× |

Projection cuts allocated bytes by approximately
89.3%.
Insert allocated bytes rise from 1.40 MB to
1.57 MB per batch; upsert bytes rise from
0.72 MB to 1.10 MB.
Cancellation support, validation, stable identities and cached statements have
costs even where latency improves. These final figures supersede the earlier
experimental statement-reuse prototype numbers.

Raw data: [baseline](validation/benchmark-baseline.txt),
[hardened](validation/benchmark-hardened.txt).

## Coverage of the audit

All 22 concrete findings are addressed: project SQL isolation; stale UI identity;
patch/null/revision semantics; legacy counts; renamed managed indexes; literal
contains; canonical timestamps and migrations; index reconciliation; atomic
returned updates; cancellation and table-specific locks; index budgets/release;
cursor and oversize behavior; typed editors/defaults; precision at app/SDK/UI
boundaries; exact IDs/argument validation; SQL literals/comments/labels; optional
Storage integration and hydration status; empty schemas; non-finite/error JSON;
non-reusing identities; coalesced/reconnected refresh; scoped links and valid help.

Additional safeguards cover expanded default write budgets, table replacement
preconditions, bounded metadata pages, reused SQL shapes, immutable migrations,
column positions, and one embedded manifest source. Search defaults to projected
visible columns in the UI; editors change only visible edited fields and return
a small projection after writes.

The audit's conditional architecture suggestions remain explicitly separate:
events are best-effort invalidations, not durable workflow delivery; an outbox
would require a workflow delivery contract and consumer deduplication. Project-wide
disk quotas remain a platform capacity policy. Migration behavior was tested on
historical-schema fixtures and a restarted local sidecar, not production data.
