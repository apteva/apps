# SEO v0.7.0 audit

Reviewed release `seo/v0.7.0` (`b95fd52c`) on branch `fix/seo-v070-audit`.
The audited fixes are included in the SEO v0.7.1 patch release.
Validation used local fixtures; no deployment or paid provider requests were made.

| Finding | Trigger and impact | Fix |
| --- | --- | --- |
| P1: Paid queue costs could disappear from the budget | Submission discarded the reported charge, and timed-out paid tasks were excluded from committed spending. Collection could also replace a known charge with an estimate. | Persist submission charges, retain paid failed tasks in budget totals, and preserve known costs through collection. |
| P2: Empty SERPs revived stale rankings | The latest snapshot contained no results, so cached reads fell back to older domain rankings. | Current fallback checks for matching snapshots, including empty snapshots. |
| P2: Combined provider reads lost results | Latest-snapshot selection partitioned only by keyword. One provider's snapshot displaced the other provider's data. | Select the latest snapshot per keyword, provider, and locale. |
| P2: Failed bulk preflight left jobs pending indefinitely | Provider mismatch, missing binding, or insufficient credit rejected the HTTP request after creating jobs, before starting their workers. | Complete preflight for every group before creating jobs; check each provider connection once per request. |
| P2: Panel responses could overwrite a newer selection | Slow domain, keyword, entity, or page reads resolved after switching selection/provider/engine. | Cancel effect-owned reads, reject responses after cancellation, and clear stale detail state. |
| P2: Domain refresh could use the wrong locale | The configured locale was outside the first 500 loaded locations, belonged to another provider, or the user had selected YouTube. | Preserve the domain's configured locale ID and use backend provider mapping; fallback locales must be Google locales. |
| P2: Page drill-down failed with over 200 keywords | The panel could pass 500 keyword IDs to a tool that accepts at most 200. | Split reads into batches of at most 200 IDs. |
| Performance: Initial loading fetched hidden details | Overview loaded selected domain metrics, backlinks, keyword metrics, rank history, and entity rankings. Provider/engine changes also reloaded unrelated configuration. | Load details only in the relevant workspace and separate scope configuration from filtered data. |
| Performance: Backlink summary decoded every link into Go | Summary allocations and date formatting grew with the complete backlink collection. | Aggregate UTC-day buckets through a covering SQLite index. |

Local benchmark on Apple M1 Pro, 100,000 backlinks, 90-day summary:

| Metric | v0.7.0 | Updated |
| --- | ---: | ---: |
| Time per summary | 67.7 ms | 22.4 ms |
| Allocated bytes per summary | 8,009,372 | 20,540 |
| Allocations per summary | 650,115 | 1,381 |

This fixture shows approximately 3x lower latency and 99.7% fewer allocated bytes.
Production performance depends on data distribution and disk/cache conditions.
The new index consumes additional storage and adds work to backlink writes.

Validation completed:

- Existing baseline Go tests passed; four new ranking/budget regressions failed against the original code before their fixes.
- `GOWORK=off go test -race ./...` passed, including regression coverage for budget charges, failed jobs, empty snapshots, provider selection, preflight failures, and aggregation across missing timestamps and date boundaries.
- `GOWORK=off go vet ./...` passed.
- `GOWORK=off go build -o /tmp/apteva-seo-v070-audit .` passed.
- `cd ui && bun install && bun test`: five DOM-based panel tests passed.
- `bun run scripts/build-panels.ts --app seo` passed; generated bundles were rebuilt and host React imports verified.
- `git diff --check` passed.

The workspace requires the latest app-sdk tag by commit topology. Updated the
pin from v0.55.0 to v0.76.0 after fetching tags and verifying that v0.76.0 descends
from all 100 available version tags. Go validation used `GOWORK=off` to test the
pinned dependency rather than the older local workspace overlay.

Reproduce the benchmark from `mcp/seo`:

```sh
GOWORK=off go test -run '^$' -bench BenchmarkAuditBacklinkMovement -benchtime=1s
```

The audit used local SQLite fixtures and mocked provider responses. It did not
exercise live DataForSEO/YepAPI responses or the deployed dashboard in a browser.
