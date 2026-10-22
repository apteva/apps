# Local verification — 22 October 2026

Candidate based on `1e073901` (Telephony 0.12.30), macOS arm64 / Apple M1 Pro.
All changes and tests were local. No production or staging calls, configuration,
deployment, release, registry publication or Docker usage.

## Completed checks

| Check | Result |
| --- | --- |
| Full uncached Go suite | 1,823 passed; 11 skipped; 373.275 seconds |
| Targeted race suite | Passed: profiles, adaptive receiving, clocks, forwarding, capture, runtime evidence, G.722 state, source restart, header age and partial tails |
| Frontend and UI unit suites | 189 + 131 passed |
| New profile browser tests | 2 passed |
| TypeScript check | Passed |
| Panel rebuild and host-import verification | Passed |
| Go vet and native build | Passed |
| Linux amd64 CGO-disabled test cross-build | Passed; not native Linux execution |

The full suite includes ordinary speech/first-word gates, PCMA/PCMU/G.722,
loss/reordering/DTX, hold/source changes, genuine local stale-audio protection,
two-hour clock scenarios and a simulated 64-call / 20-minute clock-and-jitter
comparison. That 64-call comparison reported zero discard and a 61 ms maximum
queue. Simulation is not a production host capacity measurement.

Artifacts:

- `/private/tmp/sip-adaptive-full-frozen.jsonl`
- `/private/tmp/sip-adaptive-race-frozen.txt`
- `/private/tmp/sip-adaptive-ui-frozen.txt`
- `/private/tmp/sip-adaptive-playwright-frozen.txt`
- `/private/tmp/sip-panels-freeze.txt`

## Captured severe timing replay

Same immutable RTP timing fixture and injected natural speech; G.722:

| Measurement | Frozen 0.12.28 baseline | Candidate | Test-only SpeexDSP reference |
| --- | --- | --- | --- |
| Discarded input | 6,360 ms | 1,240 ms | Reference does not expose the same counter |
| Active speech coverage | 75.11% | 90.95% | 99.73% |
| Longest damaged speech interval | 680 ms | 80 ms | 20 ms |
| Longest playback mismatch interval | 120 ms | 200 ms | 40 ms |
| Maximum modeled added delay | 159.56 ms | 379.56 ms | 319.56 ms |
| Opening preserved | No | No | Yes |

Candidate discard is bounded overflow, with zero late/local-age rejection in
this fixture. The modeled delay includes the fixture's initial 219.56 ms
relative source phase; it is not a synchronized one-way measurement. SpeexDSP
uses its own buffering policy and shares the test decoder/concealment.

**The severe trace is improved but still fails the smooth-speech criterion.**
Neither fewer discard counters nor passing bounded-fault ceilings establishes
first-word preservation here. The mature reference performs better and remains
a useful next receiver comparison. Healthy/moderate natural-speech gates were
not weakened to accept this severe trace.

## CPU/allocation microbenchmarks

Two repetitions, `-benchmem`, without other test suites running:

| Operation | Time | Allocation |
| --- | --- | --- |
| Adaptive ingress + pop | 567–678 ns | 160 B / 1 allocation |
| Previous-admission ingress + pop | 647–666 ns | 160 B / 1 allocation |
| Cached process runtime evidence | 51–52 ns | 0 |
| Healthy recovery eligibility | 31 ns | 0 |
| Shift/jitter recovery eligibility | 339–353 ns | 0 |
| Worst bounded four-packet G.722 warm-up | 52.5 µs | 2,560 B / 4 allocations |

Ingress retains the existing payload allocation count. Warm-up runs only after
bounded stateful-codec overflow, on the playback worker. No claim of a reliable
ingress speedup follows from these short runs. Detailed capture is disabled by
default; its fixed memory and process caps are separately tested.

Raw results: `/private/tmp/sip-adaptive-bench-frozen.txt`.

## Real Chromium → WebRTC → outbound SIP/TLS/SRTP

All runs used eight concurrent calls, 20 measured seconds, PCMA/G.722, recording,
healthy/jitter/180 ms phase-shift/570 ms pause profiles, a local carrier fixture,
and unchanged executable source hashes. No acceptance thresholds were changed.

| Run | Acceptance | Process CPU, percent of one core |
| --- | --- | --- |
| Candidate, first run | **Failed: 7/8 exceeded 40 ms median-growth gate** | 54.14% |
| Unchanged baseline, immediately following | Passed 8/8 | 57.44% |
| Candidate, matched repeat | Passed 8/8 | 53.83% |

The initial failure included healthy controls, with median growth of 43–83 ms.
Host CPU averaged 55.1% busy in that run, versus 46.6% baseline and 44.0% in the
candidate repeat. The machine also had memory pressure. These observations do
not prove the cause or rule out a software scheduling/recovery weakness. The
first result is retained as a failure, not invalidated or replaced by the repeat.

In the passing candidate repeat:

- Maximum healthy p95 source-to-carrier marker delay: 123.66 ms.
- Largest positive first-third → last-third median growth: 19.95 ms.
- Longest healthy speech-delivery interruption: 20 ms.
- Maximum fresh-marker recovery after an injected incident: 417.18 ms.
- No transport/codec errors or recording drops; queue bounds held.

Artifacts, including source hashes and per-call evidence:

- Failed candidate: `/private/tmp/sip-adaptive-browser-soak-20261022-final/`
- Baseline: `/private/tmp/sip-baseline-browser-soak-20261022-compare/`
- Passing candidate: `/private/tmp/sip-adaptive-browser-soak-20261022-paired/`

The baseline was extracted unchanged from commit `1e073901`; its temporary
archive is outside Git, so the runner's revision field is empty. The extraction
retains the original executable source and its per-file hashes.

## Remaining validation

This is not a production-capacity certification. The short load results varied,
and the severe receiver replay is not uniformly smooth. Before rollout, run
matched inbound G.722/PCMA speech comparisons and a longer concurrent browser/SIP
soak on an isolated staging host, including native Linux timestamp tests. Keep
the failed-run and smooth-speech gates visible. The configurable policies allow
those comparisons on new calls without redeploying or changing existing calls.
