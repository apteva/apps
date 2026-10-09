# Telephony query performance

Local comparison with public Telephony 0.11.2 (`75f4e031c32d6f11c1a65ba9f03b33320b77ccb3`).
See `results.json` for every measurement and the verification record.

## Changes

- Recording summaries filter requested call IDs and use a partial covering index.
- Lists select public fields, without copying diagnostics, signaling or credentials.
- Ownership, current offers, ring-run presence and destination identity checks are batched by candidate page. Visibility is applied before the result limit.
- SSE reads a compact state projection and batches offer lookups. A fixed 20 ms window coalesces notifications without indefinitely postponing delivery.
- Identical authorized read scopes reuse bounded results: lists for 250 ms; SSE for at most its 20 second lease. Offer expiry and recording-control timeout shorten validity.
- Database triggers invalidate reads when call state, offers, ownership, destinations, grants, recordings or hold configuration changes. Diagnostic and heartbeat writes do not increment the read epoch. Live listen/coaching capabilities and durations are recomputed when rendering.
- Capacity cleanup starts from occupied capacity slots and looks up calls by primary key.

Authentication still runs on every request and SSE wake. Answer, attach, controls and reservations retain fresh database checks. The cache is never authoritative for answering a call. The list JSON contract is unchanged; full details remain available through the single-call endpoint.

## Measurement

The identical `query_efficiency_benchmark_test.go` driver runs against both revisions, using Go 1.25.1 and the SDK's SQLite test database. The fixture contains 6,020 calls, 5,906 recordings, 100 owned historical calls, 10 active offers and 10 occupied slots. Each historical call includes three generated diagnostic/signaling payloads of about 4 KiB each.

Three trials per revision use 300 operations per read/cleanup case and 3,000 per write case. Figures are medians. CPU is process user + system CPU from `getrusage`, across all threads, including Go/SQLite/GC work. Wall time, allocation bytes and allocation counts are also retained. CPU profiles were captured for one trial of each revision.

Changing-state cases update a call before every read, forcing invalidation; they demonstrate gains without relying on cache hits. Repeated-list and heartbeat cases measure reuse separately. The HTTP handler case includes serialization and uses an already authenticated principal. Authentication-service latency, disk contention, proxy/network cost and production concurrency are outside this comparison. The test database is in memory; installation migrations and file-backed durability costs are not timed.

The Mac was busy with other work. Absolute wall times vary; the raw trials and process CPU figures expose that variation. These results do not predict an identical reduction in total production CPU or guarantee zero audio interruptions.

## Reproduce

Run from the Telephony directory:

```sh
env GOWORK=off TELEPHONY_QUERY_BENCHMARK=1 \
  TELEPHONY_QUERY_ITERATIONS=300 \
  TELEPHONY_QUERY_OUTPUT=/private/tmp/telephony-query.json \
  TELEPHONY_QUERY_CPU_PROFILE=/private/tmp/telephony-query.cpu \
  go test -run '^TestQueryEfficiencyPerformance$' -count=1 -timeout 3m -v .
```

For a baseline comparison, use a separate 0.11.2 checkout and copy only the identical benchmark test into it. Do not run workloads concurrently. Run three times per revision and compare medians. The benchmark is opt-in and never invokes a carrier API.

Raw verification logs and CPU profiles from this run are retained locally under `/private/tmp/telephony-query-performance-20261009/`.

## Measured results

| Workload | 0.11.2 ms/op | Updated ms/op | Speedup | CPU reduction |
| --- | ---: | ---: | ---: | ---: |
| call_list_changing | 17.548 | 8.305 | 2.1× | 57.1% |
| call_list_repeated | 18.446 | 1.842 | 10.0× | 86.7% |
| single_call_recording_summary | 6.994 | 0.015 | 457.1× | 99.8% |
| sse_changing | 10.975 | 4.678 | 2.3× | 57.2% |
| sse_heartbeat | 9.495 | 0.049 | 192.8× | 99.4% |
| capacity_cleanup | 4.289 | 0.047 | 90.3× | 98.5% |
| call_state_write | 0.033 | 0.045 | 0.7× | -33.5% |
| audio_diagnostics_write | 0.033 | 0.035 | 0.9× | -2.1% |

Read-epoch maintenance adds about 0.012 ms per call-state write in this in-memory workload. Audio diagnostic writes have no new trigger; their 2.1% CPU difference (about 0.001 ms) is within the variation visible across the trials. The production cost of durable writes still needs a file-backed deployment profile.

## Verification

- Full Go suite: 762 passing checks; the two opt-in live-carrier checks were intentionally skipped. The query benchmark's normal opt-in skip was exercised separately with six measured runs.
- Frontend/audio suite: 212 tests passed.
- Focused race suite: cache concurrency, SSE, permission isolation, expiry and capacity passed.
- Frontend and benchmark TypeScript checks, `go vet`, and a Linux amd64 build with CGO disabled passed.
- Final compiled-app integration run: 34 checks passed, including five real Chromium calling surfaces: headless, panel, delegated application user, coaching at 512 kbps and coaching with jitter.
- Compiled sidecar tests cover human audio in both directions, carrier reconnect, ring groups, routing decisions, AI startup and carrier activation. An existing human-call assertion read telemetry before its coalesced persistence completed; unchanged 0.11.2 failed the same assertion. The test now polls the authorized full-detail endpoint for the expected durable data, keeping its original audio assertions. Three focused reruns passed.
- The local demo browser test passed after its simulator was brought up to date with hashed worklet/worker assets and fresh attach sessions. The test waits for reconnect completion before sending DTMF.

### Audio stress result: not completely green

The complete 28-profile network matrix passed 27 gates. Expected degraded outcomes below available transport bandwidth count as successful gates, not as usable audio.

The `main-thread-two-second-pause` case failed its 5% missing-marker gate: 2 of 38 adviser-to-carrier markers were missing (5.26%); p95 delay was 188 ms. Missing markers are a probe measurement, not a percentage of all speech samples.

Three paired, sequential replays using the same profile, seed and duration produced:

| Replay | Unchanged 0.11.2 | Updated Telephony |
| --- | --- | --- |
| 1 | Failed: outbound p95 662 ms | Failed: outbound p95 577 ms, 5.26% missing markers |
| 2 | Passed: outbound p95 48 ms, no missing markers | Passed: outbound p95 55 ms, no missing markers |
| 3 | Passed: outbound p95 52 ms, no missing markers | Passed: outbound p95 47 ms, no missing markers |

This stress failure exists on both versions. Its initial failure is retained; thresholds were not relaxed. These runs do not identify its precise cause or prove that the update has no effect on every network condition. The audio engine, worker, worklet, WebSocket handler, diagnostics implementation and resampler are byte-identical to the baseline; hashes and replay results are in `results.json`.

No staging/production instance was contacted or changed, and no real carrier calls were placed. No new public release was made for this task.
