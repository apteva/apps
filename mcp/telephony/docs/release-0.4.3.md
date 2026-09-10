# Telephony 0.4.3

Reduce browser softphone buffering and remove the default microphone attenuation,
with controls for application developers to compare settings. The shared engine
serves both headless extensions and the built-in Telephony panel.

- Default microphone gain changes from -6 to 0 dB; echo cancellation, high-pass
  filtering and the -3 dBFS lookahead limiter remain. Explicit saved/host gain
  overrides are preserved.
- Start playback at 60 ms instead of 80 ms. Consuming apps can set
  `playbackTargetMs`, `playbackMinMs`, and `playbackMaxMs` through the existing
  `createSoftphone({audio: ...})` and `reconnect({...})` APIs. Bounds are validated
  before dialing or replacing a working audio connection. Adaptive buffering and
  backlog limits remain enabled. The built-in panel includes a playback selector.
- Keep human/external PCM passthrough without AI gating. Count received packets,
  samples, audio duration and local arrival gaps independently of AI analysis.
  Human diagnostics no longer mistake bypassed analysis for zero received frames
  or silence. Stored diagnostics include the selected carrier send-ahead window.
- Keep carrier send-ahead at 40 ms by default. Operators can select 20/40/60/80 ms
  using Telephony's `human_audio_send_ahead_ms` installation setting. Changes apply
  to new bridges; stale-audio limits remain unchanged. Invalid values use 40 ms.

Controlled worklet measurements: 60 ms starts approximately 21 ms earlier than
80 ms. A 40 ms delivery stall produced an underrun at the 40 ms target but none
at 60/80 ms; a 100 ms stall exercised adaptive recovery for all three. Quiet input
at 0 dB measured 6 dB above the -6 dB profile; full-scale input stayed under the
limiter ceiling. These are local tests, not measured production mouth-to-ear delay.

Validation: Go short suite and vet; focused race regressions; TypeScript and
frontend/worklet tests; rebuilt integrity-checked client and import-verified
panel; compiled Chromium headless and panel calls with controlled carrier peers,
including microphone/playback changes during reconnect. No paid calls were placed.

No server, Web SDK, or Flexylead-specific change is required for the new defaults.
Existing generic authorization and Twilio ownership fixes are retained. See
[configuration examples and measurements](human-audio-latency.md) for A/B testing.
