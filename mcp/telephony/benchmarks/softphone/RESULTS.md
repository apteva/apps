# Measured local results — 29 September 2026

Actual Chromium/production audio pipeline with the local carrier substitute.
30 seconds per profile, seed 20260929; approximately ±20 ms timing uncertainty.
Source was revision `4568ec395712c6b515c984a8e77056133022046b` with the local audio
reliability and benchmark changes. Exact file hashes and browser version are
recorded in the raw report under
`results/network-matrix-30s-20260929/results.json` (ignored local artifact).

| Network profile | Adviser → carrier p95 | Missing markers | Carrier → adviser p95 | Missing markers |
|---|---:|---:|---:|---:|
| Clean local baseline | 48 ms | 0% | 61 ms | 0% |
| Broadband | 80 ms | 0% | 80 ms | 0% |
| Wi-Fi jitter | 141 ms | 0% | 111 ms | 0% |
| Mobile latency | 189 ms | 0% | 176 ms | 0% |
| 512 kbit/s each way | 99 ms | 0% | 110 ms | 0% |
| Modeled TCP recovery | 260 ms | 5.2% | 241 ms | 0% |
| 256 kbit/s upload | No complete markers | 100% | 80 ms | 0% |
| 256 kbit/s download | 70 ms | 0% | **11,320 ms** | **81%** |
| Two-second outage | 154 ms | 6.9% | 330 ms | 6.9% |

All five usable profiles passed their declared gates. The outage recovered in
both directions; its interruption still lost audio. Recovery-profile thresholds
are tolerances for a deliberately damaged connection, not a loss-free claim.
A shorter 12-second run also passed all usable profiles and outage recovery.

**256 kbit/s is unsupported for this PCM transport.** The longer download test
also exposed seconds of network-delayed audio among the few surviving markers.
Application queue limits do not bound all bytes already in a TCP/network queue,
and RTT-based clock estimates become uncertain on severely asymmetric links.
Do not describe the current system as enforcing a universal end-to-end latency
cap. A future low-bandwidth transport/codec or an explicit degraded-connection
policy needs separate design and regression testing.

These markers measure delivery and software timing, not voice intelligibility,
physical devices, a perceptual quality score or the actual carrier network.
No staging, production or real calls were used. See [README.md](README.md) for
reproduction and scope. The raw files retain browser/server diagnostics; consult
their observation timestamps rather than assuming they are simultaneous.

## Additional defect found by the benchmark

Captured wire messages showed fractional `queue_before_ms` in worker drop events.
The Go integer decoder rejected the whole diagnostics message, leaving persisted
counters stale. The decoder now accepts fractional event times and rounds/clamps
them to the existing integer public format. No audio processing changed for this
fix. The harness retains recent wire diagnostics and checks they decode against
the real Go protocol, so the same mismatch fails future benchmark runs.

Follow-up Chromium runs after the decoder fix passed broadband and outage
profiles, including the new wire-protocol checks. Persisted capture counters
advanced to 750 frames in each run; playback counters recorded approximately
11,999 ms for broadband and 10,158 ms for the outage, including 161 ms of worklet
drops. These are stage counters, separate from missing-marker percentages.
Reports: `results/diagnostics-verified-20260929/`.

Verification also passed the full Go short suite, 100 frontend tests, frontend
and benchmark TypeScript checks, and race-enabled network-model and benchmark
gate tests. The new telemetry decoder has focused regression coverage.
