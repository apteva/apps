# Telephony 0.11.3

Cumulative release retaining all Telephony 0.11.2 functionality.

## Changes

- Scope recording summaries to requested call IDs, with a partial covering index.
- Use compact SSE state projections and batched ownership/offer checks. Coalesce
  changes within a bounded 20 ms window while preserving adviser-scoped hints.
- Select public call-list fields instead of large diagnostics and credentials.
  Batch permission metadata and reuse identical authorized reads in a bounded
  cache. Lists expire after at most 250 ms; SSE reads after at most the 20-second
  stream lease. Offer expiry and recording-control deadlines shorten validity.
  Dependency triggers invalidate changed state; media diagnostics and heartbeats
  do not invalidate it. Authentication remains fresh, and answering, media attach,
  controls and reservations retain authoritative database permission checks.
- Start capacity cleanup from occupied slots and look up their calls by primary
  key, avoiding historical terminal-call scans.
- Add regression tests for response parity, visibility before limits, grant
  isolation/revocation, offer expiry, fresh answer permissions, concurrency,
  invalidation, covering-index use and capacity cleanup semantics.
- Update local browser/integration fixtures for current media assets, fresh attach
  sessions and asynchronous diagnostic persistence.

The public list response and full-detail diagnostics remain available. Live
listen/coaching capabilities are recomputed when rendering. Production audio
processing, worker, worklet, WebSocket handling and resampling are unchanged.

## Local measurements

Three sequential trials per revision compared the same fixture and driver with
public 0.11.2: 6,020 calls, 5,906 recordings and 10 occupied capacity slots.

| Workload | Speedup | Process CPU reduction |
| --- | ---: | ---: |
| Fresh changing call list | 2.1× | 57.1% |
| Repeated call list | 10.0× | 86.7% |
| Single-call recording summary | 457.1× | 99.8% |
| Fresh changing SSE snapshot | 2.3× | 57.2% |
| Reused SSE heartbeat | 192.8× | 99.4% |
| Capacity cleanup | 90.3× | 98.5% |

Read invalidation adds about 0.012 ms per call-state write in this in-memory
SQLite workload (33.5% more CPU for that small operation). Diagnostic writes
have no new trigger. These measurements exclude disk durability, authentication
service latency and production contention; they do not predict an identical
reduction in total production CPU.

## Verification and remaining limitation

Implementation verification passed 762 Go checks, 212 frontend/audio tests,
focused race checks, vet, TypeScript checks, Linux amd64 build, local demo browser
test and 34 compiled-app integration checks, including five Chromium calling
surfaces. Opt-in live-carrier tests were skipped.

The complete network matrix passed 27 of 28 gates. The two-second main-thread
pause profile exceeded its 5% missing-marker threshold (2 of 38 markers, 5.26%).
Three paired replays each produced one failure and two passes on both unchanged
0.11.2 and this update. Thresholds were not relaxed. The cause remains unresolved;
these results do not establish a universal absence of audio regressions.

See [query benchmarks and full verification](../benchmarks/queries/README.md)
and [raw measurements](../benchmarks/queries/results.json).

Publishing does not activate installations, change bindings or place real calls.
No staging or production instance was contacted or changed.
