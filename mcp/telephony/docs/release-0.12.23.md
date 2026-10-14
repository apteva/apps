# Telephony 0.12.23

## Controlled SIP playout recovery

Adds optional `rtp_playback.resynchronize` to the existing project/route-scoped
`telephony_local_media_settings` configuration. Default **false**: trial it in
staging before enabling it on production routes. Settings are copied for each
new SIP bridge, inbound and outbound, independently of the carrier.

After one second of stable contiguous media with a persistently shifted RTP
phase, the enabled policy flushes raw/decoded/PLC reserve, advances the playout
epoch and primes from newly received audio. A ten-second cooldown prevents
repeated rebasing. Bursts, reordering, local processing stalls, hold and source
changes cannot use this path to renew stale frames. Existing capacity and local
age bounds remain. Keep the offset and discard evidence: RTP timing is relative,
so a recovered stream may still have increased upstream latency.

A 60-second deterministic codec replay with a stable 240 ms shift after ten
seconds reduced late rejection from 50 seconds to one second with recovery
configured. G722, PCMA and PCMU recovered; active playback after recovery had
no further discard. Repeated 360 ms bursts remained rejected by the age policy.
Those are synthetic comparisons, not exact reconstructions of production calls.

## Browser recovery and attachment safety

- After fast transport recovery expires, the shared softphone retries at a
  bounded low rate while the same call remains active. An online event wakes
  pending recovery. PCM/WebSocket and WebRTC retain their transport settings;
  `auto` can use its existing setup fallback when recreating audio.
- Bound authorization/status requests and abort cancelled operations. Coalesce
  uncertain attachment retries; supported sessions use an atomic generation
  precondition and a bounded process-memory result cache. A lost response can
  reuse its current session; retries cannot displace a newer attachment or extend
  a live lease. An expired session requires fresh HTTP authorization and a
  conditional successor. Revocation, ownership changes and terminal calls stop
  recovery. Explicit manual recovery can conditionally replace the current
  authorized generation after a conflict.
- Preserve mute, devices and call identity. Recovery does not dial another leg.
  Passive listening continues with slower retries after thirty seconds; private
  coaching retains explicit recovery and push-to-talk safeguards. Late cancelled
  listening credentials are cleaned up without starting playback.

## Accurate bounded diagnostics

Use recent WebRTC jitter-buffer intervals for current health. Retain the lifetime
average separately. Sample age includes telemetry queue residence; server-owned
observation time survives persistence without refreshing old measurements.

SIP buffer rejection degrades processing health independently of transport
health. Add playout-epoch counters, recovery offsets and post-lock ingress timing.
A best-effort fixed ring correlates kernel arrival, socket read, handler time and
RTP identity. Persistence carries at most eight recent observations. Collection
never waits for diagnostic locks and performs no database/network I/O on audio
paths. Genuine carrier loss and browser loss remain separate.

## Release gates

Full Go/Bun tests, generation/lease and outage regressions, race checks, native
Opus on Linux/macOS amd64/arm64 and Linux musl, compiled-sidecar Chromium,
30/60/120-minute recording playback/import, and 32/64-stream mixed UDP/SRTP load.
A local TURN/TCP Chromium test forces relay candidates, carries two-way audio,
blocks HTTP for forty seconds, then recovers the same call with mute preserved.
No production TURN service is configured by this release. The Chromium carrier
fixture continuously consumes outgoing media, matching the live transport; the
Linux regression gate verifies PCM listening and coaching as well as WebRTC.

Both manifests and the bundled frontend identify 0.12.23; the source reference
is `telephony/v0.12.23`. Publishing does not install or restart an environment.
The release preserves 0.12.22 audio and read optimizations and its default 48 kHz
Opus input. External network loss and delayed carrier delivery remain possible.
