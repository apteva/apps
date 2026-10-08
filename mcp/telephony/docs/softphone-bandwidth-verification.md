# Softphone bandwidth verification — 7 October 2026

Local Telephony branch `feat/telephony-webrtc-transport`, based on the complete
0.10.5 release. The optional WebRTC/Opus implementation is saved in `c42a6eca`;
this follow-up adds benchmark infrastructure and evidence only. No production,
staging, carrier account, live call, integration or operating-system networking
configuration was used or changed. No version was published or activated.

## What was actually constrained

Real headless Chromium executes Telephony's shared/headless WebRTC audio client,
its capture DSP and native Opus/jitter buffer. A compiled local Telephony sidecar
executes the production Opus/hub/carrier path with a local Telnyx L16 substitute.
The physical microphone is replaced by a repeatable synthetic acoustic signal;
caller playback is measured at the real browser audio graph.

The browser is forced to use a local TURN/UDP relay. The selected UDP relay
candidate and nonzero bidirectional traffic are checked, so direct candidates
cannot bypass the limit. **Audio and signaling share one serializer per direction.**
Each limit applies to one call in each direction; it is not a shared whole-office
capacity figure. Profiles add 20 ms one-way network latency and up to 5 ms jitter.

UDP accounting includes encrypted SRTP, RTCP, TURN encapsulation/control and
28 bytes IPv4/UDP headers per datagram. Signaling TCP consumes the same budget
with a conservative 52-byte IPv4/TCP header allowance per proxy read chunk.
Setup before the measurement epoch is unrestricted. Ethernet/VPN overhead,
kernel TCP ACKs/retransmission/packetization and unrelated application traffic
are excluded. This is userspace network modeling, not an operating-system
traffic shaper or a reproduction of a specific office VPN.

Datagrams scheduled more than 200 ms after receipt are dropped by the network
fixture rather than stored indefinitely. The real application retains its own
stale-audio protection. Reports record relay loss, byte counters, aggregate
budget/window/backlog, source levels, native browser telemetry and marker timing.
The model does not override the browser's own native jitter buffer.

## Results

20-second measurements, seed `20260929`, plus warmup/drain:

| Transport / per-direction limit | Adviser → carrier p95 | Carrier → adviser p95 | Missing adviser / caller markers | Quality gate |
| --- | ---: | ---: | ---: | --- |
| WebRTC 128 kbit/s | 128 ms | 202 ms | 0% / 0% | Passed |
| WebRTC 96 kbit/s | 168 ms | 214 ms | 0% / 0% | Passed |
| WebRTC 64 kbit/s | 227 ms | 292 ms | 0% / 0% | Passed |
| WebRTC 48 kbit/s | 246 ms | 492 ms | 65.8% / 36.8% | Degraded; insufficient bandwidth |
| WebRTC 32 kbit/s | Unmeasurable | Unmeasurable | 100% / 100% | Degraded; insufficient bandwidth |
| Existing WebSocket PCM at 64 kbit/s | Unmeasurable | Unmeasurable | 100% / 100% | Degraded; insufficient bandwidth |

A second **60-second** WebRTC run at **64 kbit/s**, with seed `20261007`, also
passed: **226 ms** adviser → carrier p95, **264 ms** carrier → adviser p95,
**118/118 markers in each direction**, no duplicates or reordering. Source-level
medians were −15.74/−15.71 dBFS against the approximately −15.05 dBFS source,
within the unchanged ±2.5 dB level gate.

Both 64 kbit/s runs discarded one encrypted upstream datagram in the fixture;
zero missing markers therefore does **not** mean zero packet loss or prove every
speech sample survived. Datagram drop and marker-loss counters remain separate.

Usable profiles keep their explicit gates: p95 at most 500 ms, at most 5% missing
markers, nonzero delivery, no duplicates/reordering, and source level within
±2.5 dB. Deliberately inadequate profiles report degradation rather than hiding
it; their subtest passing means the harness executed, not acceptable audio.
Clock mapping uncertainty is approximately ±20 ms. All failed/degraded evidence
is retained alongside successful results.

## Meaning and limits

The optional WebRTC/Opus path demonstrably works under a much smaller available
per-call budget than the existing 384 kbit/s PCM payload requirement. **64 kbit/s
is a tested laboratory point, not a deployment guarantee.** Reserve headroom:
128 kbit/s per direction per active call is a reasonable initial planning point,
then measure representative shared WAN/Wi-Fi/VPN traffic and real devices.

These are software marker latency/loss and signal-level tests, not physical
mouth-to-ear timing, subjective speech quality, MOS/PESQ/POLQA or lossless codec
proof. Opus is lossy compression. The synthetic source, short single-call tests,
limited modeled jitter and local carrier substitute do not certify every carrier,
network, browser/device or a production TURN deployment. The existing WebSocket
path remains the default; no live installation was switched to WebRTC.

## Reproduce locally

```sh
bun run benchmark:softphone --profiles webrtc-udp-128k,webrtc-udp-96k,webrtc-udp-64k,webrtc-udp-48k,webrtc-udp-32k,websocket-64k --seconds 20
bun run benchmark:softphone --profiles webrtc-udp-64k --seconds 60 --seed 20261007
GOWORK=off go test -race ./benchmarks/softphone
GOWORK=off go test -tags integration -run '^TestBenchmark' -count=1 .
bun run typecheck:benchmark
```

Local artifacts:

- `/private/tmp/telephony-webrtc-bandwidth-probe-20261007/`: initial 128 kbit/s actual-relay probe (signaling separate).
- `/private/tmp/telephony-webrtc-bandwidth-matrix-20261007/`: initial actual-media matrix (signaling separate).
- `/private/tmp/telephony-webrtc-shared-bandwidth-20261007/`: final shared media/signaling matrix.
- `/private/tmp/telephony-webrtc-shared-64k-long-20261007/`: 60-second repeat, independent jitter seed.

Each directory contains machine-readable measurements, a report and browser
results. Source manifests record file hashes and local changes as well as Git
revision; the earlier separate-signaling experiments are clearly distinguished
from the final shared-budget evidence.

## Regression checks after the benchmark additions

- Full `GOWORK=off go test -short -json ./...`: 703 test passes, three skips, zero failures.
- Complete frontend/audio tests: 170 passed, zero failures.
- Harness race suite, integration measurement/gating tests, benchmark TypeScript
  check and harness `go vet`: passed.
- Existing production audio code and generated assets were unchanged by this
  follow-up. The existing Pion TURN dependency was promoted from indirect to
  direct for the test relay; its version and the SDK pin were unchanged.

Logs are retained in `/private/tmp/telephony-bandwidth-full-go-20261007.jsonl`,
`/private/tmp/telephony-bandwidth-frontend-20261007.log` and
`/private/tmp/telephony-bandwidth-harness-race-20261007.log`.
