# Telephony 0.12.57

Adds an opt-in adviser-only warning tone to automatic pickup in the shared
softphone and built-in panel. Eligible offers play a short beep on the selected
output, confirm rendering and wait a one-second default warning window before
answering. The window is configurable from 750–3000 ms through existing scoped
pickup policies. It overlaps the configured pickup delay.

Playback/output failures preserve manual Answer and emit structured SDK guidance.
Offer expiry/withdrawal, absence, cancellation and another call suppress pending
pickup, including late asynchronous completions. Duplicate offers trigger one
cue/answer. The tone graph connects only to local playback; no cue is injected
into microphone or carrier media. Older clients cannot silently bypass a
required warning. Existing policies default to warning off; manual/outbound
calling and active media/mute behavior are preserved.

Validation includes full Go and frontend regressions, focused race tests, real
Chromium pickup/cancellation/playback failures, silent caller PCM isolation,
selected output/window unit checks, typecheck, panel regeneration, release-ref
consistency and Linux amd64 build. See automatic-pickup-warning.md for the
contract and example French guidance using structured codes.

This release does not change deployed installations or enable a warning policy.
