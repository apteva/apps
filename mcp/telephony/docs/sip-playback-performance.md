# SIP playback verification — 0.12.5

## Reproduce

Run in `mcp/telephony` with the released SDK (no workspace override):

```sh
GOWORK=off TELEPHONY_SIP_REPLAY_RESULTS=/tmp/sip-playback.json go test -run '^TestSIPPlayback' -count=1 .
GOWORK=off go test -run '^$' -bench '^BenchmarkSIPPlayback' -benchtime=300ms -count=3 .
```

An optional locally generated mono speech WAV can be supplied with
`TELEPHONY_SIP_SPEECH_WAV`; the test skips this input when absent. The local
verification used synthesized French speech, not a customer recording.

## Deterministic network replay

[Full results](verification/sip-playback-0.12.5.json) compare a frozen 0.12.4
buffer with the new implementation: 146 cases, PCMU/PCMA/G.722, initial targets
60/80/100 ms, capacities 160/200 ms. Network scenarios run for 12 seconds
(15 seconds for catch-up); the two clock cases run for 120 seconds.

| Profile | 0.12.4 discarded / replacement | New default discarded / replacement | Optional 200 ms capacity discarded / replacement |
| --- | --- | --- | --- |
| Normal, ordinary 0–40 ms jitter, 80 ms batches | 0 / 0 ms | 0 / 0 ms | 0 / 0 ms |
| Repeated 180 ms batches, each second | 960 / 880 ms | 700 / 660 ms | 220 / 260 ms |

The burst figures match across all three codecs and initial targets. They show
27% less discarded audio with the default capacity, 77% with the optional
larger capacity. These are summed audio durations, not outage lengths or a
claim of equivalent audible word loss. Replacement includes bounded
concealment followed by silence. Increasing the initial target did not improve
these burst results, so the default remains 60 ms.

Both +/-300 ppm clock replays had zero discarded/replacement audio and
estimated approximately +/-299.8 ppm. The ten-second catch-up case deliberately
discarded about 9.8 seconds of stale speech; queue bounds held in every replay.
Relative source-age checks are not synchronized end-to-end latency measurements.
The recorded `max_residence_ms` field includes relative source age as well as
local queue residence.

Unit checks cover bit-identical healthy decoded PCM, concealment fade/recovery,
partial-sample age, source changes, intentional hold, DTX/DTMF, startup reserve,
packet wrap, scoped settings and concurrent collection. Replays cannot prove
production has no cuts or identify the upstream cause of missing RTP.

## CPU and allocation

[Raw benchmark results](verification/sip-playback-0.12.5-bench.txt), Apple M1 Pro,
darwin/arm64; three isolated runs, median values per 20 ms frame:

| Work | 0.12.4 | 0.12.5 |
| --- | --- | --- |
| Healthy PCMU decode/framing | 524 ns, 1120 B, 4 allocations | 761 ns, 800 B, 3 allocations |
| Healthy PCMA decode/framing | 618 ns, 1120 B, 4 allocations | 884 ns, 800 B, 3 allocations |
| Healthy G.722 decode/framing | 14.58 µs, 2080 B, 4 allocations | 14.72 µs, 1440 B, 3 allocations |
| Queue operations only | 196 ns | 416 ns |

Bookkeeping increases CPU for the cheaper codecs; allocation falls. G.722
framing time was within about 1% in this run. A 50-frame/s G.722 stream consumes
about 0.074% of one core for this measured component; this excludes encryption,
network I/O, encoding, resampling, recording, database work and scheduling.

Worst-case 64-event history snapshot plus JSON serialization measured about
51 µs per sample (132 allocations), outside the reader lock. At one sample/s
and 100 calls that isolated work is about 5.1 ms/s, or 0.51% of one core.
Persistence and transport overhead are excluded. The media path performs no
telemetry network/database I/O, and event history/background collection remain
bounded. These measurements are local component benchmarks, not a production
capacity guarantee.
