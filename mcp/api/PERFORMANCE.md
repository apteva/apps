# API Gateway performance — 0.5.0 → 0.6.0 candidate

Measured locally on 2026-09-05, Apple M1 Pro, Go 1.26.6 / Darwin ARM64,
`GOMAXPROCS=2`, `GOWORK=off`. Baseline: exact release `api/v0.5.0`
(`04a090f3122b6b2916854a3432b93fbd1e3695f7`). These are loopback results
on a shared, busy machine, not production capacity estimates.

## Complete HTTP requests

The same harness sends a real HTTP GET through a local gateway server to a
local HTTP origin and validates the body. The request matches the final seeded
literal route among 10, 100 or 1,000 routes. One first request is recorded
separately, then 100 requests are timed sequentially. Logs are drained before
calculating overall requests/second. Five runs per version and route count
provide 500 measured requests per cell. Version order alternates across adjacent
pairs; compilation precedes measurement. Entries are medians of five runs'
p50/p95/throughput statistics, rather than pooled percentiles.

| Routes | p50 before | p50 after | Speedup | p95 before | p95 after | Requests/s before → after |
|---:|---:|---:|---:|---:|---:|---:|
| 10 | 0.555 ms | 0.448 ms | 1.24× | 2.235 ms | 3.008 ms | 1228 → 1134 |
| 100 | 1.368 ms | 0.439 ms | 3.12× | 4.872 ms | 1.768 ms | 541 → 1463 |
| 1,000 | 6.983 ms | 0.458 ms | 15.25× | 18.055 ms | 2.964 ms | 121 → 1082 |

**The small-route tail did not improve:** at 10 routes, p95 increased by
0.773 ms (2.235 → 3.008 ms) and measured throughput fell about 8%, despite a
lower median. Results do not support claiming a universal speedup. Bounds,
policy checks and asynchronous log coordination have costs; unrelated load
also affects these short tests. At 100 and 1,000 routes, both median and tail
latency improved in this comparison.

First-request medians, including route-table compilation:

| Routes | Before | After |
|---:|---:|---:|
| 10 | 2.500 ms | 1.211 ms |
| 100 | 1.859 ms | 1.683 ms |
| 1,000 | 8.836 ms | 6.241 ms |

Raw results: [final runs](validation-results/http-final.txt),
[all per-run statistics](validation-results/http-summary.json). Earlier runs
are retained alongside them. They exposed large timing variation and
small-route overhead, prompting removal of management diagnostic subqueries
from public dispatch, a short log-batching window, pooled response buffers,
and fewer header-only writes for ordinary responses.

## Route lookup microbenchmark

Worst-case miss across enabled routes, with one untimed warm-up lookup for
both versions. Five one-second repetitions; medians below. The old version
loads and decodes every route from SQLite on each lookup. The candidate reads
an immutable compiled snapshot, allocating parameters only for a matching
route. Public dispatch still checks authoritative API policy/status and key
validity separately.

| Routes | Before | After | Speedup | Bytes before → after | Allocations before → after |
|---:|---:|---:|---:|---:|---:|
| 10 | 148.081 µs | 1.287 µs | 115.1× | 10,632 → 16 | 435 → 1 |
| 100 | 1,597.155 µs | 1.707 µs | 935.7× | 95,352 → 16 | 4,038 → 1 |
| 1,000 | 10,483.339 µs | 50.625 µs | 207.1× | 944,682 → 16 | 40,786 → 1 |

At 1,000 routes this removes **99.998% of lookup allocation bytes**
(944,682 → 16) and reduces steady-state lookup time by approximately 207×.
This microbenchmark speedup is not an end-to-end throughput multiplier.
Allocation counts are more stable than elapsed time under unrelated load.

Raw results: [before](validation-results/route-before.txt),
[after](validation-results/route-after.txt).

## Reproduce

Run from `mcp/api` with the same toolchain/machine for both versions. Copy the
same `performance_test.go` and `BenchmarkAuditMatchRoute` function into an
isolated original release snapshot; retain its production code, manifest and
SDK pin. Timing tests are opt-in and impose no brittle CI threshold.

```sh
env GOWORK=off GOMAXPROCS=2 go test -run '^$' -bench '^BenchmarkAuditMatchRoute$' -benchtime=1s -count=5 ./...
env GOWORK=off GOMAXPROCS=2 API_PERF=1 go test -run '^TestGatewayHTTPPerformance$' -count=5 -v ./...
```

For less scheduling bias, compile test binaries first and alternate versions
using `-test.run=^TestGatewayHTTPPerformance$ -test.v`, from each version's own
directory. The final comparison used this approach. Avoid simultaneous builds
or tests. The checks cover routing and local serial HTTP traffic, not production
concurrency ceilings, external network transit, realistic upstream work, or
long-running fan-out capacity. See VALIDATION.md for correctness-test scope.

## Release dependency update

The original candidate results above used SDK v0.73.0. The released app pins
v0.74.1. Release checks rerun the HTTP harness; see
[release HTTP results](validation-results/http-release.txt).
