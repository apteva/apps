# WebRTC FEC CPU check — 10 October 2026

## Measurement scope

Local only, on a ten-core Apple M1 Pro, with Go limited to two logical CPUs
(`GOMAXPROCS=2`). No instance configuration, carrier calls or deployment changed.
Production codec settings and audio quality settings were not changed.

Each workload has its own encoder, decoder and production 24→48 kHz resampler,
processing the same local French speech at 50 frames/second. Four variants
compare the previous portable codec, native codec without FEC, native codec
with FEC, and native FEC with a modeled 5% ingress loss. The latter attempts
recovery of single missing packets using the next packet. Measurements include
process user + system CPU time from `getrusage`, Go allocations, per-frame wall
time and missed scheduling slots. Initialization is outside the steady-state
measurement. Each variant runs for three seconds per concurrency level; two
complete runs were recorded. These are short codec load probes, not sustained
full-call/server capacity tests: sockets, SRTP, carrier handling, routing,
diagnostics and database work are excluded.

## Results

Percentages below mean percentage **of one CPU core**; 100% equals one core.
The second run produced:

| Concurrent codec workloads | Previous portable | Native, FEC off | Native, FEC on | FEC on, 5% loss |
| --- | ---: | ---: | ---: | ---: |
| 10 | 14.8% | 18.1% | 20.0% | 21.1% |
| 25 | 47.5% | 55.1% | 62.0% | 66.8% |
| 50 | 79.2% | 118.5% | 140.1% | 138.5% |

The ten-workload portable measurement missed 30/1,500 slots and should not be
treated as a complete-throughput result. At 25 and 50 workloads, every variant
completed all frames in this second run. FEC's p99 combined per-frame work at
50 workloads was 0.80 ms without loss and 0.68 ms with loss, within the 20 ms
frame interval. The first run measured 97.8%/102.4% of a core for the two FEC
variants at 50 workloads, also without skipped slots. Timing at other points
was inconsistent, including misses in a one-workload loss test and a
25-workload loss test. Failures remain recorded; they are not averaged away.
Host load was high and changing, reaching load averages above 80 on ten CPUs.
The measurements do not prove that every scheduling miss was external.

The second run's 50-workload comparison assigns approximately 0.22 cores to
FEC above the same native codec with FEC disabled. The complete change from
portable to native FEC costs approximately 0.61 additional cores in that run.
Separate sequential runs are susceptible to host/frequency variation, so
these differences are local estimates, not fixed per-call guarantees.

Fresh repeated encoder-only microbenchmarks measured:

- Portable: 88–95 microseconds/frame; zero Go allocations.
- Native without FEC: 182–192 microseconds/frame; 200 bytes/seven allocations.
- Native with FEC: 187–260 microseconds/frame; 200 bytes/seven allocations.

Much of the encoder increase therefore comes from the native implementation,
not FEC alone. No additional per-frame Go allocation was measured from
enabling FEC on that implementation. The complete native pipeline allocated
approximately 2.7 kB/frame, versus 17–20 kB for the portable pipeline; native
C allocations are not included in Go `MemStats`. Encoder/decoder state is
created per call and freed on exit, and the library is loaded once. Codec
controls are bounded to at most once every two seconds. Repair is limited to
three missing packets; no unbounded retry or catch-up encoding loop is added.

## Operational conclusion

No runaway CPU behavior was observed. This is still a meaningful cost increase:
allow roughly 2–3% of one core per active WebRTC codec workload on this host,
and approximately 1.5 cores for 50, before budgeting other server work. On a
two-core machine, that leaves limited headroom for the rest of Telephony.
Do not use this as the production admission limit without measuring the target
machine and complete bridge under its expected concurrent load. Codec CPU
does not cover the browser's WebRTC implementation.

Only WebRTC uses these Opus codecs. The default PCM/WebSocket audio engine
has not acquired this encoding cost. No quality setting was reduced to make
the benchmarks look faster.

## Reproduce

```sh
GOWORK=off GOMAXPROCS=2 \
TELEPHONY_SPEECH_INPUT=/absolute/path/24khz-mono-pcm.wav \
TELEPHONY_CPU_OUTPUT=/absolute/path/cpu-results.json \
go test -run '^TestRTCCodecCPUStress$' -count=1 -v .

GOWORK=off GOMAXPROCS=2 go test -run '^$' \
-bench '^BenchmarkRTCVoiceEncoding$' -benchtime=500ms -count=3 -benchmem .
```

Artifacts: `/Users/marcoschwartz/.codex/artifacts/telephony-fec-cpu-20261010/`.
The original raw files are preserved. Separate `*-analysis.json` files derive
CPU per completed frame and full-rate projections so skipped slots cannot
make an overloaded workload appear cheaper.
