# Telephony 0.11.4

Cumulative release retaining all Telephony 0.11.3 functionality and all local
Telephony work committed since that release.

## Changes

- Add destination/project-scoped realtime turn detection in existing AI/agent
  destination configuration. Validate profile and positive silence duration on
  save; carry overrides through frozen routing/offer snapshots to new realtime
  spawn requests, including AI fallback and bounded startup retries. Absence
  preserves the telephony profile (currently 750 ms in Core), with its existing
  sensitivity, prefix padding and interruption defaults. Active sessions are
  not updated, reconnected or terminated by a destination edit. Republish the
  intended flow for new calls to receive an edited setting.
- Consume the SDK's signed, validated browser IP at media handshake; retain the
  socket peer and safe fallback diagnostics when assertions are absent/invalid.
  Classification remains known configured VPN exit or unknown, never an inferred
  absence of VPN. Uses published app-sdk v0.99.0. The server's matching forwarding
  implementation must be installed separately to expose IPs behind its proxy.
- Add bounded timestamped transport history shared by panel and headless
  softphones. WebRTC reports safe RTP/remote-report, ICE bandwidth/RTT and codec
  statistics. PCM reports directional interval throughput, capture age, arrival
  gaps, queue and transit/excess maxima measured before frame rejection.
- Pace small telemetry parts through one shared slot, at most every 250 ms,
  and skip PCM monitoring parts when media backpressure is present. Store reports
  asynchronously with indexed retention, idempotent partial-map merging,
  completeness flags, project/operator access and trusted connection attribution.
  Per-call history is available through audio-health detail; broad lists and SSE
  do not scan it. Raw credentials, media tokens and token-bearing URLs are excluded.
- Recover a completely unsent PCM frame after a writer deadline by dropping it
  and continuing with current audio. Partial writes and control-frame failures
  remain terminal; no stale bytes are replayed.
- Improve network stress evidence with independent browser/Worklet clock
  observations and host CPU samples; retain failure/control/replay evidence and
  bound simulation execution time instead of treating a timeout as a pass.

Default transport selection, PCM/Opus codecs, capture/playback DSP, buffer
targets, latency caps and stale-audio rejection are retained. The playback
Worklet is unchanged. Existing routing, authorization, human answer, listening,
coaching, number controls and all earlier features remain included.

## Verification

- Final complete Go suite: 818 passing tests/subtests, three opt-in skips,
  using the published SDK with `GOWORK=off`.
- Focused race checks cover turn detection, AI preparation/recovery,
  concurrent claims, transport storage and PCM writer recovery; vet and app
  builds pass.
- Canonical separated frontend/audio suites: 222 passing tests. Type checks,
  packaged assets and panel/headless builds pass.
- Local carrier-neutral network checks include jitter, mute/reconnect, WebRTC
  at 64/128 kbit/s and PCM at 512 kbit/s. Final 60-second constrained runs recorded
  zero missing speech markers in both directions, with WebRTC p95 latency
  252/273 ms at 64 kbit/s and PCM p95 152/217 ms at 512 kbit/s.

The retained benchmark evidence includes initial failures and subsequent fixes;
these checks do not establish universal call quality, PSTN mouth-to-ear latency,
a production VPN cause, or a live provider's conversational turn latency.
Controlled conversational comparison of 500 vs 750 ms remains pending.

See [turn detection](realtime-turn-detection.md),
[transport verification](transport-telemetry-verification-20261009.md) and
[audio stress/client-IP verification](audio-stress-and-client-ip-verification.md).

## Publication and installation

Publication does not activate installations or modify carrier/configuration
bindings. No staging or production installation or real call was contacted.
An app upgrade replaces the install's sidecar process; do not assume active
media survives it. Drain live calls before a separately authorized installation.
