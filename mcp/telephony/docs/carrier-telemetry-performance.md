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
| RTP | 1 | 321.5 | 372.0 | 50.5 |
| RTP | 10 | 318.6 | 397.3 | 78.7 |
| RTP | 50 | 312.6 | 400.6 | 88.0 |
| RTP | 100 | 301.6 | 395.2 | 93.6 |
| SRTP | 1 | 1800.0 | 1285.0 | -515.0 |
| SRTP | 10 | 1206.0 | 1365.0 | 159.0 |
| SRTP | 50 | 1324.0 | 1378.0 | 54.0 |
| SRTP | 100 | 1334.0 | 1380.0 | 46.0 |

Negative differences reflect local benchmark variability, not a telemetry
speedup. The raw repetitions are retained for inspection.

The packet pipeline retains **3 allocations for RTP / 7 for SRTP** in both
variants: telemetry adds no per-packet allocation. The standalone send-counter
and duration bookkeeping takes about **113 ns/frame**, with zero allocations.
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
| 1 | 0.194 ms | 0.019% |
| 10 | 1.323 ms | 0.132% |
| 50 | 4.515 ms | 0.451% |
| 100 | 9.262 ms | 0.926% |

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
