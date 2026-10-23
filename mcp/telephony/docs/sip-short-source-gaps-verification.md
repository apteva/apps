# SIP short source-time gaps — local verification, 23 October 2026

Candidate: Telephony 0.12.32, based on released commit
`21d285e81620edfb758ad275cc4111d515dac5e2` (0.12.31).
All candidate validation was local, without Docker. No staging or production
installation, configuration, calls, uploads or restarts were performed.

## Constructed reproduction and before/after

Call `0874dda6…` did not retain per-packet timings. The test uses its initial
sequence/timestamp, G.722 20 ms packets with the 8 kHz RTP clock, declared
40 + 20 ms source-time jumps, and a constructed arrival pattern. It reproduces
the old rebuffer at 1,635.039 ms and second start at 1,688 ms; it does not prove
the original packet-by-packet trigger. PCMA runs the same construction.

| Measurement, both codecs | Released 0.12.31 | Candidate |
| --- | --- | --- |
| Playback starts | 108 and 1,688 ms | 108 ms only |
| Unnecessary re-priming wait | 52.961 ms | None |
| Maximum actual PCM output interval | 60 ms | 20 ms |
| Packet-loss concealment | 40 ms | 0 |
| Separately filled source-time gap | Not available | 60 ms |
| Speech discarded / sequence loss | 0 / 0 | 0 / 0 |
| Measured active-speech coverage | 100% | 100% |
| Opening preserved | Yes | Yes |

Short source gaps are rendered as zeros without consuming the next speech
packet or altering G.722 decoder state. A source timestamp jump is not proof of
intentional DTX. The 77.5 ms browser startup concealment predates this SIP
symptom and is outside this correction.

## Repeatable acceptance bench

`TestSIPShortSourceGapContinuity` runs 26 natural-speech scenarios (13 per codec):
normal output, report-shaped timing, report + jitter, sequence/timestamp rollover,
40/20/10 ms jumps, repeated short source silence, modest jitter, a 60 ms packet
arrival gap, real loss, a 500 ms delivery stall, and a 2-second timestamp-only
discontinuity. It measures every missing PCM output tick rather than omitting
holes, and compares healthy decoded speech samples with the clean codec output.

Short transitions require one initial playback start, no real-speech discard,
no sequence loss or clock/decoder reset, first-word preservation and at least
99.5% speech coverage. Timing and speech damage bounds are explicit. The normal
case preserves every decoded PCM sample. Real loss still increments the loss
counter; the long stall still discards stale/overflow audio and recovers within
the fault budget. The 2-second timestamp discontinuity is skipped instead of
adding unbounded delay. No default queue or age limit is increased.

`TestSIPSourceGapNeverExpiresFreshPacketToFillTimeline` additionally verifies
that 190 ms-old speech skips a pending 40 ms gap rather than waiting past the
existing 200 ms age limit. Telemetry tests distinguish source gaps from missing,
concealed and discarded audio. The shared inbound/outbound SIP receiver is fixed;
no carrier-specific signaling or browser settings change.

Run from `mcp/telephony`:

```sh
GOWORK=off TELEPHONY_SHORT_GAP_RESULTS=/tmp/short-gaps.json \
  go test -count=1 -run '^Test(SIPShortSourceGap|SIPSourceGap|SIPFreshPacketAfterConcealment|SIPJitterResumesShortDTX)' -v .
GOWORK=off go test -race -count=1 -run '^Test(SIPShortSourceGap|SIPSourceGap|SIPFreshPacketAfterConcealment|SIPJitterResumesShortDTX)' .
```

CI runs these scenarios in the full suite, retains the JSON evidence and runs
the focused regressions under the race detector. Frozen before/after results and
candidate source hashes: [measurements](verification/sip-short-source-gaps-0.12.32.json).

## Release checks

- Full uncached Go suite: **1,855 passing test/subtest cases, 11 skips**, 385.235 seconds.
- New continuity, source-gap telemetry, fresh-resumption and DTX race checks: passed.
- Frontend/UI: **326 passing tests**; type checking and generated panel import checks passed.
- Go vet, native build and Linux amd64 CGO-disabled test cross-build: passed. Cross-build is not native Linux execution.
- Manifest version/source-ref and frontend asset hash checks passed in the full suite.

## CPU and allocations

Three repetitions per benchmark, one second each, run sequentially without
competing task-owned test suites; macOS arm64 / Apple M1 Pro. Median time:

| Operation | 0.12.31 | 0.12.32 | Allocation, unchanged |
| --- | --- | --- | --- |
| Adaptive ingress + pop | 558 ns | 565 ns | 160 B / 1 allocation |
| Healthy PCMA PCM playback | 1,181 ns | 1,138 ns | 800 B / 3 allocations |
| Healthy G.722 PCM playback | 15,623 ns | 15,661 ns | 1,440 B / 3 allocations |

These measurements show no material steady-state overhead, not a guaranteed
speedup or a production capacity limit. No workers, timers, I/O or database
writes are added to the audio path. New counters use the existing diagnostic
snapshot path; gap metadata is only handled when a gap/missing interval occurs.

Raw [baseline](verification/sip-short-source-gaps-0.12.32-bench-before.txt) and
[candidate](verification/sip-short-source-gaps-0.12.32-bench-after.txt) results.

## Local browser/SIP smoke

Eight concurrent real Chromium → WebRTC → outbound SIP/TLS/SRTP calls passed
all existing smoke gates, with PCMA/G.722, recording, SIP refresh, jitter,
180 ms phase changes and injected 570 ms pauses. Measurement window: 20 seconds;
total test runtime: 33.352 seconds. Local carrier/platform fixtures exclude the
Telnyx network and remote Apteva proxy.

- All eight calls accepted, no transport/codec errors or recording drops.
- Worst healthy p95 marker delay: 105.16 ms.
- Largest positive first-third → last-third median delay growth: 0.072 ms.
- Maximum healthy speech-delivery interruption: 24.24 ms.
- Injected pause scenarios still interrupt delivery by about 500 ms; passing
  recovery budgets does not make that fault inaudible.
- Process CPU: 53.97% of one core in this short eight-call local workload.

[Smoke summary](verification/sip-short-source-gaps-0.12.32-outbound-smoke.json).
This short smoke complements the deterministic long-clock suite; it does not
replace a long real-network capacity test.

The report-shaped improvement does not certify
production host capacity, eliminate genuine network loss, recover missing
upstream speech or establish that every historical audio issue is resolved.
