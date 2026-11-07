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
It also writes `host-load.json`: timestamped aggregate CPU activity, load average
and physical free memory during the entire run, including compilation. Physical
free memory is not a measurement of memory pressure or reclaimable memory.
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
| browser-seven-second-catchup | 10000 / 10000 | 0 ms | 0 ms | Hold browser download bytes for 7.23 seconds; carrier ingress and microphone continue |
| main-thread-two-second-pause | 10000 / 10000 | 0 ms | 0 ms | Block UI JavaScript for two seconds; media Worker and Worklets continue |
| browser-48k-fallback | 10000 / 10000 | 0 ms | 0 ms | Force 48 kHz AudioContext so production fallback resampling runs in both directions |

The browser catch-up scenario needs at least four seconds of recovery after
its outage; use `--seconds 20`. It verifies that carrier ingress has no stall,
the browser measures/discards received stale audio, microphone delivery stays
healthy and playback recovers. UI blocking must be observed by runtime
telemetry without violating the usable audio gates. The runtime observer
measures scheduling delay beyond its one-second interval, rather than the
full duration of a busy interval.

The benchmark also observes the synthetic microphone and playback render clocks
through direct Worklet ports in a separate Worker. `independent_render_clocks`
contains bounded timestamped progress samples, probe RTT/uncertainty and the
observer's own timer gaps. It keeps measuring while the UI is blocked and starts
a separate baseline for replacement playback contexts. Clock replies taking more
than 50 ms are excluded as uncertain; observations do not subtract delays from
the scored audio timings or convert failures into passes. Correlate these clocks,
server stage/drop timestamps and host CPU samples before attributing a failure to
the softphone or to a busy computer. A host can delay the browser, local carrier
fixture, proxy or Telephony server; simultaneous CPU saturation establishes
contention, not which process caused every gap.

For a separate processing-cost microbenchmark in a real Chromium Worker:

```sh
bun benchmarks/softphone/worker-processing.ts /absolute/path/worker-processing.json
```

This imports the exact production Worker source with fixture clocks and media
ports. It measures bidirectional 20 ms packet processing at 24, 44.1 and 48 kHz,
including clock probes and telemetry calculations. It excludes real Worklet
IPC, audio devices, network/kernel buffers and server work. Timer resolution
can quantize very short measurements to zero; these are not zero CPU cost.
Use the end-to-end Chromium profiles for delivery quality and recovery.

The default WebSocket browser connection carries raw 24 kHz, 16-bit mono PCM: **384 kbit/s per
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

## WebRTC transport checks

The `webrtc-baseline`, `webrtc-microphone-mute` and `webrtc-reconnect` profiles
use the production optional WebRTC connection, native Chromium Opus, and the
same compiled Telephony media hub/carrier substitute. Reconnect retaps the new
AudioContext; it does not keep measuring a closed playback graph.

```sh
bun run benchmark:softphone --profiles webrtc-baseline,webrtc-microphone-mute,webrtc-reconnect --seconds 20
GOWORK=off go test -run '^TestRTCUDPNetworkProfiles$' -count=1 -v .
```

**The browser TCP proxy only shapes signaling for WebRTC.** RTP flows separately
through UDP. These three browser profiles verify end-to-end codec interoperability,
normal delivery, mute accounting and reconnection; they do not measure RTP under
the proxy's advertised bandwidth/latency. The separate Go profile shapes actual
bidirectional SRTP packets using Pion's virtual UDP network: 256 kbit/s normal,
64 kbit/s with up to 15 ms added jitter and one dropped media packet in 30, and
24 kbit/s deliberately constrained. Budgets include SRTP plus IP/UDP overhead.
ICE/DTLS control traffic is excluded from that media budget. The constrained
profile must show packet loss rather than conceal an insufficient connection.
The packet fixture decodes each received Opus frame, but does not score speech
or certify Chromium's native jitter buffer under those network conditions.

Native browser concealment and packet loss remain distinct from PCM discard
counters. Intentional RTC mute can send silent RTP rather than omit capture
frames; mute/unmute events and zero false capture gaps are checked instead.
See [softphone transports](../../docs/softphone-transports.md) for configuration,
security, buffering limits and deployment requirements.

## Full-browser bandwidth tests for WebRTC

```sh
bun run benchmark:softphone --profiles webrtc-udp-128k,webrtc-udp-96k,webrtc-udp-64k,webrtc-udp-48k,webrtc-udp-32k,websocket-64k --seconds 20
bun run benchmark:softphone --profiles webrtc-udp-64k --seconds 60 --seed 20261007
```

The `webrtc-udp-*` profiles add `rtc_udp: true`. The harness overrides only
its browser's peer-connection configuration to force a loopback TURN/UDP relay.
The production `WebRTCAudioConnection`, DSP, native Chromium Opus/jitter buffer,
server decoder/encoder, media hub and carrier substitute still execute. The
selected candidate must actually be UDP relay; each direction must report
substantial delivered relay traffic. This prevents direct host candidates from
bypassing the impairment.

**Audio and signaling share one bandwidth budget per direction.** Relay datagrams
include encrypted SRTP, RTCP, DTLS/ICE maintenance and TURN framing, plus 28 bytes
IPv4/UDP overhead. Signaling consumes the same serializer, with a conservative
52-byte IPv4/TCP header allowance per proxy read chunk. The report records
`udp_network` packet/byte/drop/queue counters and `connection_budget` aggregate
admitted bytes, elapsed window and maximum backlog; the aggregate rate is also
checked. Setup before the measurement epoch remains unrestricted.

The modeled network drops UDP whose scheduled delivery would exceed 200 ms.
The real application continues to enforce its own stale-audio limits. This
fixture cannot certify a universal native browser playout cap. Kernel TCP ACKs,
actual packetization/retransmission, Ethernet/VPN overhead and unrelated host
traffic are excluded. TCP chunk header accounting is an estimate. The available
rate is for **one call in each direction**, not shared across many advisers.

Profiles use 20 ms one-way latency and up to 5 ms added jitter. The 128, 96 and
64 kbit/s profiles enforce existing gates: p95 marker delay at most 500 ms,
missing markers at most 5%, no duplicate/reordered markers, plus RTC source
level within ±2.5 dB. The 48/32 kbit/s and PCM WebSocket 64 kbit/s comparisons
are deliberately inadequate and must report actual degradation honestly.
A passing command with `degraded` rows is not an all-quality-passed result.

Synthetic acoustic markers and signal levels are reproducible objective probes;
they do not prove speech intelligibility, a MOS score, or lossless Opus encoding.
Use shared-network planning headroom above the lowest passing laboratory point.
No staging/production route, carrier account, operating-system traffic shaper or
real call is used or modified by this benchmark.

## Adaptive reserve comparison

`bun benchmarks/softphone/adaptive-playback.ts /tmp/adaptive.json` runs the actual
PCM worklet with source timestamps and compares 160/220/280 ms ceilings against
ordered catch-up, missing frames, progressive gaps and constrained bandwidth.
`adaptive-160-280ms-jitter`, `adaptive-220-280ms-jitter` and
`adaptive-280-280ms-jitter` exercise these ceilings in real Chromium with 280 ms
TCP recovery. The same audio gates apply; this is not a perceptual MOS test.
