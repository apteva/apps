# Carrier telemetry performance — Telephony 0.12.1

Local Apple M1 Pro, macOS/arm64, Go 1.25 toolchain. Packet figures are the median
of three fresh 300ms runs. They measure a test-only pre-observation RTP/SRTP
packet decoder against the production decoder with telemetry, including PCM
conversion. SRTP includes fixture encryption on both sides of the comparison.
A concurrent sampler runs every millisecond (more often than production's one
second). Audio codecs, packet validation/security and queue limits are retained.

## Receive packet pipeline

| Transport | Simulated calls | Baseline ns/frame | Telemetry ns/frame | Added ns/frame |
| --- | ---: | ---: | ---: | ---: |
| RTP | 1 | 294.3 | 382.8 | 88.5 |
| RTP | 10 | 300.1 | 385.9 | 85.8 |
| RTP | 50 | 304.1 | 375.9 | 71.8 |
| RTP | 100 | 306.5 | 396.4 | 89.9 |
| SRTP | 1 | 1202.0 | 1347.0 | 145.0 |
| SRTP | 10 | 1264.0 | 1338.0 | 74.0 |
| SRTP | 50 | 1264.0 | 1336.0 | 72.0 |
| SRTP | 100 | 1245.0 | 1348.0 | 103.0 |

Negative differences reflect local benchmark variability, not a telemetry
speedup. The raw repetitions are retained for inspection.

The packet pipeline retains **3 allocations for RTP / 7 for SRTP** in both
variants: telemetry adds no per-packet allocation. The standalone send-counter
and duration bookkeeping takes about **112 ns/frame**, with zero allocations.
With the sequence sampler's mutex deliberately held, receiving counters take
about 57 ns/frame and never wait; skipped timing observations are counted. A
completely full diagnostic channel returns in about 13 ns, with zero allocations.

At 100 calls × 50 frames/s per direction, these nanosecond costs are small
fractions of one CPU core. This is additional observation cost, not zero overhead
or the complete SIP/media/codec CPU requirement.

## Complete background batch

Includes registered live SIP snapshots with populated jitter, pacer and forward
statistics, SQLite history/latest writes, bounded retention and dashboard summary
refresh. Live readings advance each second; healthy disk history is sparse at
five seconds. Fixture SQLite and elapsed wall-time budget, one run per size:

| Active calls | Full batch per one-second tick | Fraction of one-second wall-time budget |
| ---: | ---: | ---: |
| 1 | 0.186 ms | 0.019% |
| 10 | 1.422 ms | 0.142% |
| 50 | 4.495 ms | 0.450% |
| 100 | 9.362 ms | 0.936% |

The background work is measurable and includes allocations for serialized
observations (about 1.87 MB per 100-call batch). These wall-time ratios are **not a
measurement of production process CPU**, disk throughput or a capacity guarantee.
Production encryption, codec choices, disk and other workload can differ.

## Blocking/behavior verification

Regression tests deliberately hold sequence, jitter, pacer, forward-stat and
collector/database locks while processing packets and sampling. Sampling skips
busy audio-stat locks, retains previous cumulative counters and exposes
availability. The producer never waits for persistence. Cancellation/database
failure leaves collection bounded; overflow loses diagnostics rather than audio.
Successful media payloads and routing/termination behavior remain unchanged.

## Reproduce

```sh
GOWORK=off go test . -run '^$' -bench 'Benchmark(SIPTelemetry|CarrierTelemetry)' -benchtime=300ms -count=3
TELEPHONY_TELEMETRY_BENCH=1 GOWORK=off go test . -run '^TestSIPTelemetryCollectionCost$' -v -count=1
GOWORK=off go test -race . -run 'Test(CarrierEvidence|SIPTelemetry|CarrierRecoveryNetworkAttempt)' -count=1
```

Raw results: [packet/send/overload benchmarks](verification/carrier-telemetry-packets.txt)
and [sampling/persistence/dashboard benchmark](verification/carrier-telemetry-persistence.txt).
These tests do not validate a live carrier/proxy connection. No installed instance
was changed and no calls were placed.
