# Telephony 0.10.1

Cumulative release retaining all Telephony 0.10.0 functionality, including
routing, inbound protection, provider/compliance support, configurable call
duration, passive listening and private supervisor coaching.

- Retry temporary media lease renewal failures within the last verified lease,
  with bounded requests and independent expiry handling for delayed tab timers.
  Confirmed revocation or expiry still stops audio.
- Recover interrupted adviser audio with fresh authorization from
  `/softphone/attach`, preserving the existing call and microphone mute state.
  Late or duplicate operations cannot revive an ended call.
- Treat temporary authorization/database failures as HTTP 503. Established
  sockets retain only their last verified lease; coaching pauses talk while
  permission checks are uncertain and never automatically retargets.
- Refresh browser audio clock mapping after suspension and stale clock samples.
  Keep the existing bounded audio latency budgets and PCM processing.
- Add bounded renewal, WebSocket close, audio error and timestamped directional
  drop diagnostics, including measurements of rejected frames.
- Ship the fixes in the shared headless softphone/listener APIs and bundled
  Calls panel. Pin the standalone app SDK dependency to v0.95.0.

## Verification

648 Go cases/subtests, 137 frontend/audio tests, 42 focused race cases and all
five packaged Chromium scenarios passed. Typechecks and builds passed. The
final 13-profile network matrix met its gates: eleven passed and two undersized
256 kbit/s links showed expected degradation. Thirty-minute simulated playback
had zero dropped samples; steady PCM checks were sample-exact at 24/44.1/48 kHz.

An earlier Wi-Fi jitter run failed and is retained in the evidence; its precise
cause remains unproven. The isolated retry and final full matrix passed without
relaxing thresholds. These are local regression tests, not a guarantee of zero
loss on every network or proof of the production incident's root cause. Live
carrier verification remains unperformed; two live Twilio tests were skipped.

See [behavior](media-session-resilience.md) and
[verification evidence](media-session-verification-2026-10-05.md).

Publishing does not activate any installation. Production, staging, carrier
configuration, routes and numbers are untouched. No live calls were placed.
