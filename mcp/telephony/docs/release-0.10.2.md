# Telephony 0.10.2

Cumulative release retaining all Telephony 0.10.1 functionality: routing,
inbound protection, provider/compliance support, multi-carrier outbound calls,
AI handoffs, configurable call duration, listening and private coaching.

- Record per-call peer-address hashes, browser ping RTT, socket write wait,
  buffered bytes, reconnects, close codes/reasons, queue delay, dropped audio,
  sequence gaps and Worker scheduling pauses. Raw peer addresses are not stored.
- Keep separate carrier → Telephony, Telephony → browser and browser → Telephony
  health stages. Emit recoverable `audio_degraded` diagnostics without changing
  call status or ending the carrier call.
- Add timestamped AudioContext state/suspension and main-thread pause telemetry
  to the shared headless softphone backbone and bundled Calls panel.
- Preserve fresh authorized browser reattachment to the existing call, including
  microphone mute state. Keep stale-audio dropping and existing latency caps.
- Correlate distinct impaired calls in a 30-second window by project, provider
  and stage; publish degradation/recovery and correlated alert/recovery events.
  Threshold and cooldown are configurable; correlation uses bounded memory.
- Move diagnostic persistence off the media receive path, coalesce reports and
  retry failed writes without interrupting microphone audio.

## Verification

655 Go cases/subtests, 143 frontend/audio tests, 34 focused race cases and all
five packaged Chromium surfaces passed. Build, typechecks, Go vet and asset
integrity checks passed. Two live Twilio tests were skipped.

The 13-profile local network matrix met its gates: eleven passed and two
undersized 256 kbit/s links showed expected degradation. The matrix preceded
one final notice-only guard; backend, Worker/DSP and latency/drop policies are
identical. Functional tests and packaged headless/Calls panel checks passed
again after that guard. Source hashes and this distinction are retained in
[verification evidence](audio-health-verification-2026-10-06.md).

Steady PCM tests remain sample-exact at 24/44.1/48 kHz and the existing
30-minute bounded-playback regression passes. Local simulations do not prove
zero loss on every network or identify the production incident's root cause.
No live carrier or physical mouth-to-ear verification was performed.

See [behavior, API and configuration](audio-health-telemetry.md).

Publishing does not activate installations. Production and staging, carrier
configuration, routes and numbers are untouched. No live calls were placed.
