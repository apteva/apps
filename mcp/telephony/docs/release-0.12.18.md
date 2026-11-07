# Telephony 0.12.18

This release reduces repeated browser audio preparation work on both PCM/WebSocket
and WebRTC, including listening/coaching and the existing direct connection path.

- Verify and share immutable scripts per client handle and project/installation.
- Prepare scripts and check microphone permission before dialing or answering.
- Prepare scripts before acquiring reconnect credentials, keeping usable current
  audio running if preparation fails.
- Load only the worklet for WebRTC; fetch the PCM worker when fallback needs it.
- Bound asset verification to eight seconds, evict failures, and retain exact
  byte integrity checks and isolated blob ownership for external hosts.
- Record bounded UTC setup events and monotonic elapsed durations through the
  existing background telemetry sender, without per-frame instrumentation.

The controlled loader comparison against 0.12.17 reduces verification requests
from two to zero for warm preparation, from two to one for cold WebRTC, and from
128 to two for 64 simultaneous preparations on one handle. This is simulated
HTTP, not a 64-call load benchmark or a live-call CPU measurement. Codec quality,
DSP, media admission, stale-frame protection and queue bounds are unchanged.
Replacement still requires device/context initialization and a media handshake.

Local validation: full Go suite, frontend/UI regressions, frontend and benchmark
TypeScript checks, client/panel builds and all seven Chromium integration
scenarios pass. Preparation evidence is retained in
`benchmarks/softphone/asset-preparation-verification-2026-10-13.json`.

Includes all 0.12.15–0.12.17 managed package, external direct-browser, declared
ingress, browser fetch receiver and fenced-drain fixes, using SDK v0.102.0.
Publishing this release does not install or restart staging or production.
