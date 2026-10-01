# Local softphone network benchmark

This runs **real Chromium, the production softphone session, worker and audio
worklets, a compiled Telephony sidecar, and a loopback Telnyx L16 carrier
substitute**. It measures audio in both directions through that pipeline.
It requires Bun, Go, and the project's Playwright Chromium installation.
It does not use credentials, staging, production, or live calls.

From the Telephony directory:

```sh
bun run benchmark:softphone
bun run benchmark:softphone --seconds 30 --seed 20260929
bun run benchmark:softphone --profiles broadband,wifi-jitter --seconds 60
```

The default measurement is 20 seconds per profile. The ten-second carrier
profiles require at least 17 seconds, including four seconds for recovery.

Each run writes `REPORT.md`, machine-readable `results.json`, and browser
measurements under the ignored `benchmarks/softphone/results/<timestamp>/`.
Use `--output /absolute/path` to select a different directory. Keep reports
with release evidence; they are not bundled into the frontend. A failing usable
profile, infrastructure error, or failure to recover makes the command fail.
Profiles marked `degradation` are deliberately adverse and may report degraded
results without making the command fail. A successful command therefore does
**not** mean every profile has acceptable quality: read the outcome column.

## Scenarios

`profiles.json` is the source of truth for the network model and gates.
Bandwidth is independently applied to each direction, in decimal kbit/s.
Latency is one-way; random positive jitter is added up to the stated maximum.

| Profile | Download / upload kbit/s | One-way latency | Added jitter | Purpose |
|---|---:|---:|---:|---|
| local-baseline | 10000 / 10000 | 0 ms | 0 ms | Detect pipeline regressions |
| broadband | 5000 / 1000 | 20 ms | 5 ms | Normal connection |
| wifi-jitter | 2000 / 1000 | 35 ms | 40 ms | Variable arrival times |
| mobile-latency | 2000 / 1000 | 100 ms | 30 ms | Higher latency |
| 512k-symmetric | 512 / 512 | 30 ms | 10 ms | Limited bandwidth |
| tcp-recovery-1pct | 2000 / 1000 | 30 ms | 15 ms | 1% of proxy chunks stall ordered bytes by 180 ms |
| upload-256k | 2000 / 256 | 20 ms | 0 ms | Insufficient microphone bandwidth |
| download-256k | 256 / 2000 | 20 ms | 0 ms | Insufficient playback bandwidth |
| two-second-outage | 2000 / 1000 | 20 ms | 0 ms | Blackout at 4–6 seconds, then recovery |
| carrier-ten-second-catchup | 10000 / 10000 | 0 ms | 0 ms | Carrier input delivery pauses at 3–13 seconds, then queued bytes arrive rapidly |
| carrier-ten-second-missing | 10000 / 10000 | 0 ms | 0 ms | Carrier skips source frames at 3–13 seconds, without catch-up |
| intentional-microphone-mute | 10000 / 10000 | 0 ms | 0 ms | Mute at 4–6 seconds; caller delivery must remain healthy |
| browser-reconnect | 10000 / 10000 | 0 ms | 0 ms | Close browser fixture sockets at 4 seconds; worker reconnects |

The browser connection carries raw 24 kHz, 16-bit mono PCM: **384 kbit/s per
direction before framing**. A 256 kbit/s link cannot sustain it. The benchmark
must expose that limitation; queue bounds cannot manufacture missing bandwidth.
512 kbit/s is a test point with limited headroom, not a deployment guarantee.
Reserve at least 1 Mbit/s each way per active adviser as an initial planning
budget, then validate representative shared Wi-Fi, VPN and WAN conditions.

## Measurements and gates

Distinct tone sequences are generated every 500 ms in both directions. The
source replaces only the physical microphone; production capture DSP still
runs. A tap on the production playback node measures rendered PCM. The carrier
substitute decodes microphone markers after the actual server/carrier codec
path. Reports contain p50/p95/p99/max marker latency, received and missing
markers, duplication/order checks, signal level, and final recovery counts.
The first/last markers are excluded to avoid startup/drain artifacts.

Both endpoints share the host clock. Browser audio-clock mapping adds an
estimated ±20 ms uncertainty. These are **software pipeline timings**, not
physical mouth-to-ear PSTN measurements. Zero reported latency with zero
received markers means no timing measurement, never instantaneous delivery.

Usable profiles enforce the explicit p95 and missing-marker thresholds in
`profiles.json`, plus nonzero delivery, ordering, and no duplicate markers.
The outage/recovery profiles must deliver at least two valid markers each way in the
final measurement window. Carrier-interruption gates additionally require stall/recovery counters and
notices, no received-stale discard for missing frames, at least nine seconds of
stale discard for catch-up, no disruption of the adviser direction, and bounded
replayed-marker delay. Intentional mute must not raise a caller-delivery incident.
The reconnect profile must actually enter reconnecting and recover.

Missing markers reflect damaged/missing tone
sequences, not a percentage of missing speech samples. Signal level is recorded
for comparison; there is no perceptual MOS, PESQ or POLQA quality claim.

## Model boundaries and reproducibility

- This is a real application benchmark with **modeled browser and optional
  carrier links**. Carrier input has its own independently shaped TCP proxy.
  It does not identify the origin of production carrier ingress gaps.
- Source timestamps follow absolute deadlines. Scheduling pauses produce a
  catch-up batch rather than silently losing ticker ticks and drifting tone
  timestamps. Silent PCM continues during the drain period, so the harness
  does not manufacture a false delivery incident when markers stop.
- WebSockets use TCP. The proxy preserves ordered bytes and models stalls
  during retransmission. Its 1% value is per proxy read chunk (up to 1460
  bytes), **not IP packet loss**. It does not simulate kernel congestion
  control, radio interference, TLS, packet reordering or a carrier jitter buffer.
- Buffers in the network model are bounded and backpressure the real sender.
  Byte counters include handshake/control/drain traffic; they are not exact
  codec bitrate measurements.
- The random seed fixes model randomness. OS scheduling, socket read chunking,
  Chromium timing and machine load still vary. Run longer measurements and
  multiple seeds for release comparisons on the same hardware/browser.
- No physical microphone, speaker, acoustic echo, noise suppression or human
  listening score is tested. Synthetic input bypasses hardware capture.
- Profiles run one call at a time. Existing concurrent server-hub regressions
  cover 24 calls; this benchmark is not a multi-browser capacity certification.

For real carrier validation, a separately authorized controlled call must
measure both endpoints and correlate carrier traces. Local success cannot
prove zero quality loss on every real connection.

## Harness validation

```sh
GOWORK=off go test -race ./benchmarks/softphone
GOWORK=off go test -tags integration -run '^TestBenchmark' .
bun run typecheck:benchmark
```

Harness tests check bandwidth serialization, noncompounding propagation delay,
ordered recovery, byte preservation, marker decoding and rejection of damaged
markers. Application audio regression evidence is in `../../AUDIO-QUALITY.md`.

The first measured matrix and identified limits are recorded in
[RESULTS.md](RESULTS.md). Recent outbound diagnostics messages are also retained
and checked against the Go decoder; malformed telemetry fails the benchmark.
