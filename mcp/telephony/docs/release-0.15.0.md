# Telephony 0.15.0

Smoother AI voice on direct SIP when the realtime provider streams at, or
slightly below, real time.

Live traces on 2026-10-29 showed OpenAI `gpt-live-1` delivering 100 ms deltas
with no lead, 0.965–0.994× real time over a reply and single stalls up to
236 ms. Core and the Core-to-Telephony link added at most 17 ms. The 60 ms
reserve then ran dry 13–22 times per minute, and the existing time-stretch never
engaged on live speech because it searched only within one 20 ms frame.

- New direct-SIP drift compensation, on by default: when the AI's queue is
  nearly empty, explicit silence is lengthened first (inaudible), then speech is
  lengthened WSOLA-style by repeating one pitch period with a raised-cosine
  crossfade, using up to 30 ms of look-back. Splices require correlation ≥ 0.90
  on voiced audio, never create a sharper step than the audio already has, never
  repeat louder audio into a quieter frame, and keep every frame's edges exact.
- Healthy delivery is never modified. The first near-dry moment in a call arms a
  160 ms buffer target for the rest of that call; playback still starts after the
  normal 60 ms reserve.
- Settings: `playout.drift` (`"off"` disables), `playout.drift_percent`
  (default 6), `playout.drift_lead_ms` (default 160). See agent-playout.md.
- Offline replay of the captured pacing (six 18 s replies): underrun gaps 31
  (2.2 s) → 5 (0.3 s), no click edges or loud restarts, no pitch change on
  voiced splices; a 2% slow source over two minutes: 2.4 s of gaps → 0.
- Fixes two underrun fades: speech no longer ends on a hard cut when new audio
  arrived just before it but was not yet ready, or when the remainder after the
  last full packet was shorter than the 3 ms fade.
- Carrier media streams (Twilio, Telnyx and others) are unchanged.
