# WebRTC transport local verification — 7 October 2026

Implementation branch: `feat/telephony-webrtc-transport`, based on the complete
Telephony 0.10.5 release (`3025b102`). All app edits are inside `mcp/telephony`.
No staging/production instance, carrier account, integration or call was changed.
The version remains unchanged; this feature is not published or activated.

## Regression checks

- Full `GOWORK=off go test -short -json ./...`: passed (including carrier/SIP, routing, ownership, audio and
  lifecycle regressions; three skips). Additional RTP jitter-expiry and wire-cadence
  regressions passed in the focused RTC suite. The skipped UDP fixture was run separately without `-short`.
- Frontend/audio suite: 170 passed, zero failed. Modern RTCStats epoch timestamps
  were subsequently checked in the seven-test RTC suite to prevent zero bitrate
  reporting from a clamped clock.
- Frontend and benchmark TypeScript checks: passed.
- `go vet ./...`, Telephony-only panel/headless rebuild, packaged asset integrity
  and `CGO_ENABLED=0 go build`: passed.
- Broad audio/coaching/mute/RTC race suite: passed. Final RTC/asset race suite also
  passed after queue-expiry, jitter diagnostics and recovery changes.
- Benchmark measurement/gating integration tests: passed.

During verification, an existing CPU-heavy frontend test exceeded Bun's default
five-second timeout when run alongside Go checks; it passed with the complete
frontend suite run separately. An early Go run found stale generated audio assets;
the Telephony-only rebuild corrected them. Neither failure was hidden by changing
test assertions or quality gates.

## Real Chromium measurements

20-second profiles, seed `20260929`, local sidecar, shared capture DSP, synthetic
microphone/caller signals. All values are software marker p95 latency, not physical
mouth-to-ear latency or speech quality scores.

| Scenario | Adviser → carrier | Carrier → adviser | Outcome |
| --- | ---: | ---: | --- |
| WebRTC normal, final mono/clock implementation | 117 ms | 131 ms | Passed, zero missing markers |
| WebRTC intentional two-second microphone mute | 121 ms | 131 ms | Passed; 10.5% adviser markers intentionally absent, zero caller markers missing |
| WebRTC browser reconnect | 121 ms | 142 ms | Passed; 2.6% markers missing each direction during recovery |
| WebSocket normal, successful full matrix | 68 ms | 93 ms | Passed, zero missing markers |
| WebSocket released reference, direct comparison | 92 ms | 99 ms | Passed, zero missing markers |
| WebSocket new code, direct comparison | 134 ms | 90 ms | Passed, zero missing markers |

The queue-expiry/recovery matrix also passed all three WebRTC scenarios. A later
native check exceeded the gate (255 ms adviser, 698 ms caller, 7.9% caller markers
missing) despite zero RTP packet loss. Inspection found an RTP-clock defect in
the new sender: missed timer ticks advanced media time by only one frame. The
sender was corrected to use the monotonic live clock, with explicit pacing-gap
accounting and a regression that simulates a slow encoder and a five-second pause.
This failed run is retained as pre-fix evidence. All three browser profiles passed
after the live-clock correction. Signal-level comparison then exposed an additional
six-decibel microphone attenuation from stereo downmixing; explicit mono capture
and destination settings corrected it. The benchmark now enforces a ±2.5 dB level
budget against its known synthetic source in both RTC directions.
WebSocket measurements varied across runs: one exceeded the 250 ms p95 gate
(328 ms); another exceeded the 5% missing-marker gate (5.3% adviser, 2.6% caller).
The latter recorded server queue overflow and a 50 ms GC pause p99. These observations
do not establish the cause or prove a new transport regression. The direct comparison
used an unchanged archive of the released source and the new source, run sequentially;
both passed. The final microphone median was -15.10 dBFS and caller median -15.79 dBFS,
within the explicit source-level budget. RTP padding probes are counted as
transport padding and excluded from audio-error/drop counters; the final local
probe recorded five padding packets, zero rejected audio packets and no missing
markers. The final mute check passed at 119 ms adviser and 143 ms caller p95,
with the expected 10.5% intentionally muted adviser markers and zero caller loss.

This is representative local evidence, not a claim that all runs or
all real networks have zero loss. Gates were retained and failed runs remain in
the local artifacts.

WebSocket Wi-Fi jitter, intentional mute and reconnect profiles also passed in
the seven-profile run. The WebRTC reconnect probe was corrected to follow its
new AudioContext; measuring a closed context had falsely reported missing playback.

## Actual encrypted UDP impairment fixture

Pion peers encode/decode actual Opus and carry SRTP through a virtual network.
Each direction sends 160 frames; rates include media, SRTP, IP and UDP overhead.

| Profile | Frames decoded in each direction | Outcome |
| --- | ---: | --- |
| 256 kbit/s | 160/160 | Passed |
| 64 kbit/s, up to 15 ms added jitter, one media packet lost in 30 | 155/160 | Passed within configured loss gates |
| 24 kbit/s constrained | 85/160 | Expected degradation detected |

The Chromium TCP proxy shapes WebRTC signaling only. The UDP fixture supplies
separate transport impairment evidence; it does not score browser concealment or
certify subjective speech quality. The browser codec path also successfully
interoperated with native Chromium and an independent ffmpeg/libopus decoder.

## Local artifacts

- `/private/tmp/telephony-webrtc-final-20261007/`: successful four-profile matrix.
- `/private/tmp/telephony-webrtc-final-age-20261007/`: final queue-expiry matrix,
  including the narrow WebSocket failure and passing WebRTC profiles.
- `/private/tmp/telephony-webrtc-reference-baseline-20261007-retry/`: released-source reference.
- `/private/tmp/telephony-webrtc-current-comparison-20261007/`: new-source comparison.
- `/private/tmp/telephony-webrtc-ready-native-20261007/`: pre-pacing-fix native codec/telemetry check (failed quality gate; correct bitrate telemetry).
- `/private/tmp/telephony-webrtc-final-mono-20261007/`: final clock, mono level and recovery matrix.
- `/private/tmp/telephony-webrtc-final-padding-20261007/`: padding probes ignored, zero audio rejections.
- `/private/tmp/telephony-webrtc-final-mute-20261007/`: final mute/carrier-queue interruption parity check.
- `/private/tmp/telephony-webrtc-focus.log`: encrypted UDP profile results.
- `/private/tmp/telephony-webrtc-live-clock-full-go.jsonl`: full Go regression run.
- `/private/tmp/telephony-webrtc-complete-frontend.log`: complete frontend/audio run.
- `/private/tmp/telephony-webrtc-final-padding-race.log`: final RTC/asset/wire-clock/padding race run.

Before a live rollout, configure reachable ICE/UDP or restricted TURN, then
validate real calls and shared-network capacity under separate authorization.
Native browser jitter targets remain hints; server queue/age bounds do not certify
a universal browser playout latency cap. No such live checks were authorized here.
