# Telephony 0.11.1

Cumulative release retaining Telephony 0.11.0 routing, provider controls,
WebRTC/Opus, browser recovery, listening/coaching and Audio health functionality.

## Changes

- Record bounded, timestamped PCM playback underrun intervals with missing-sample
  duration, sequence boundaries and recovery/call-ending reasons. Exclude startup,
  supplied silence, intentional suspension and disconnected observation periods.
  Preserve authoritative connection attribution and reconnect history on the
  server; expose underrun filtering and duration in Audio health and call details.
- Improve the shared/headless PCM playback buffer for every carrier. Start at
  60 ms and build a real reserve during playback using tightly bounded,
  waveform-matched expansion. The adaptive ceiling is configurable up to 280 ms;
  the 320 ms hard source-age/backlog protection remains. Poor matches are skipped,
  adjustment work is bounded and healthy streams remain sample-exact.
- Add optional `playbackAdaptive:false` and configurable ceiling controls for
  hosts. WebRTC continues to use its native jitter buffer. Microphone processing,
  permissions, routing and carrier call control are unchanged.
- Add actual-worklet micro-cut comparisons and three Chromium network profiles
  comparing 160/220/280 ms ceilings with repeated 280 ms delivery gaps. Retain
  both first-pass results and isolated repeats in checked-in verification reports.
- Rebuild shared headless/panel assets and pin app SDK v0.97.0, confirmed newer
  than v0.96.0 by commit ancestry; its optional directory appearance metadata
  does not change Telephony's audio path.

## Verification and limits

744 Go checks/subtests and 195 frontend/audio tests passed; two optional
live-carrier tests were skipped. These suites, focused race checks, vet, Linux
release build and both typechecks passed with the final SDK pin and rebuilt
assets. Packaged client/worklet and benchmark source hashes are verified against
the release sources.

The 52 worklet comparisons include 24/44.1/48 kHz, voiced harmonics, tone,
noise, silence, missing media, bandwidth limits and a ten-minute simulation.
For progressive 40/80/120/200/280 ms voiced gaps, measured missing playback fell
from 240 ms to 74.6 ms, with zero stale/overflow discard and approximately 289 ms
p95 original source-to-render age. These controlled results are not a guarantee
against every network outage or a subjective natural-speech quality assessment.

Chromium network verification passed 27/28 first-pass profile gates. The
ten-second missing-carrier case missed 2/38 adviser markers during overlapping
local workloads; the cause was not established. That profile and two additional
profiles passed isolated repeats with unchanged thresholds. Two existing Go
wall-clock pacer tests failed during concurrent load and passed in the complete
isolated suite. Both results are retained; inadequate bandwidth profiles report
degradation instead of concealing it.

See [adaptive playback and verification](adaptive-playback.md) and
[underrun telemetry](audio-health-telemetry.md). Missing speech, long outages and
links below the PCM payload budget still cause gaps. No staging/production
installation, carrier account, binding or live call is changed by publication.
