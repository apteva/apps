# Shared softphone logic audit — 2026-09-08

Audited the app-owned controller, panel adapter, browser audio engine,
worker/worklet transport, and server claim/media handoff. Fixes apply to both
the Calls panel and the headless extension. Included in Telephony 0.3.10.

## Fixed

| Failure | Change and verification |
| --- | --- |
| Hangup rejected while microphone permission or audio startup was pending | Device startup is cancellable. Hangup stops local audio immediately and completes independently of a permission prompt. An outstanding placement write retains the busy lock until its outcome can be cleaned up. Deferred-operation regression tests cover both paths. |
| Old startup results could overwrite terminal state or touch a replacement call | Generation checks guard completion, error reporting, device cleanup and busy state. Tests end a call during reconnect, start another session and deliver the old result afterward. |
| A media adapter could report an error during startup yet answer resolve successfully | Startup now verifies it still owns a usable audio connection. Failed outbound setup attempts hangup; failed inbound setup attempts token-bound release. |
| Factory/cleanup failures could leave a carrier call without monitoring | Retained sessions restart status reconciliation even when the audio factory throws. Tests verify eventual terminal status clears controls. |
| Carrier answer failure rolled the server back to pending but left the controller stuck with an invalid session | Audio failure triggers a fresh call read. A verified server rollback clears the local claim so Answer works again. Old host list snapshots are not used to infer rollback. |
| Callback exceptions could interrupt media cleanup | State, meter, diagnostics and notice callbacks are isolated; adapter cleanup cannot prevent controller cleanup. Worker startup cancellation settles its promise and clears its timeout. |
| A responsive socket with no carrier peer was closed after ten seconds | Worker health uses received traffic and heartbeats, independent of the callee answering. A simulated 60-second ringing test keeps the socket open and the microphone gate closed. |
| Silent TCP stalls and stalled WebSocket handshakes could hang indefinitely | Worker heartbeat/watchdog detaches stalled transports, clears audio history and retries with a bounded budget. Tests cover half-open sockets, stalled handshakes, stale frames and malformed PCM. |
| A microphone could disappear before its disconnect handler was installed | Validate track state and install device handlers immediately after capture is acquired. A regression verifies an already-ended track fails before media transport opens. Audio-context recovery also restores live state after browser suspension. |
| A second stale Answer click could obtain an active non-group call's media credentials | Existing calls require explicit rejoin, including ordinary single-destination calls. Backend tests cover accidental takeover and permitted rejoin. |
| Answer release could race carrier activation or report success without changing the claim | Claim creation, media activation and release serialize per call. Release checks the session token in the conditional database update and requires an affected row. A test blocks carrier answer, issues release, then verifies the successful carrier answer cannot be reset. Active-media and stale-token regressions also pass. |

## Validation

- 44 frontend/audio tests: 24 controller/client tests and 20 audio/worker/worklet tests.
- Full Go short test suite and `go vet`.
- Go race checks covering softphone, inbound/outbound browser handling, SIP/RTP and browser ring claims.
- Compiled-sidecar browser integration for both the SDK-loaded headless client
  and actual Calls panel under React StrictMode. Generated microphone and
  speaker audio cross the real Telephony bridge; tests cover mute, acknowledged
  DTMF, reconnect, hangup and keeping a call alive across panel navigation.
- Existing human-browser and ring-group integration cases.
- Standalone example interaction and mobile layout tests.
- TypeScript checking, rebuilt panel and hashed headless bundle, and panel
  imports checked against the host React surface.

Media still runs through the shared worker/worklet pipeline. There is one status
request at a time; panel hosts disable the controller's regular poll because they
already watch calls. The error-recovery read runs only after an audio failure.
Heartbeat overhead is one small message every five seconds in the worker. The
headless bundle remains about 44 KB minified and includes no React runtime.

## Boundaries

These tests use Chromium, generated audio and simulated carrier responses. They
do not establish real-network or physical-headset quality, Safari/Firefox support,
or mobile background-call reliability. Real carrier calls, Bluetooth route changes,
network switching and long-duration calls remain useful acceptance checks before
claiming broad device support.

This remains a single-active-call controller for trusted Telephony operators.
Hold, transfer, conferencing and untrusted application-user authorization are not
added by this audit. Multi-destination incoming ringing remains a server feature.
Disposing a controller releases devices but deliberately leaves an established
carrier call up; hosts intending to end it must await hangup. For dial recovery
across page reloads, hosts must persist the same idempotency key and request.
