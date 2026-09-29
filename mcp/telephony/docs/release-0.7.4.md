# Telephony 0.7.4

Audio reliability, directional diagnostics and a repeatable real-browser network
benchmark. Includes all Telephony 0.7.3 changes. This is a source release; no
staging or production installation is updated.

## Changes

- Replace aggressive playback soft-limit trimming with adaptive bounded
  buffering: 60–160 ms target, 320 ms hard queue limit, and stale-frame expiry.
- Bound live server audio queues and write deadlines, keep network writes outside
  call ownership locks, and expire aged human audio in JSON, Twilio and SIP pacers.
  Preserve the existing buffered AI output policy.
- Add negotiated APT2 frame timing with legacy compatibility, capture clock
  mapping and stale capture/playback protection. Retain mute, hold, ownership
  and reconnect gates.
- Record directional stage timings, gaps, queue/drop counters and process
  context. Accept fractional browser drop-event milliseconds without rejecting
  the whole diagnostics snapshot; retain the integer public diagnostics format.
- Preserve a committed AI terminal/menu fallback when a late admission callback
  arrives, preventing a fallback journal race found in integration testing.
- Add a local Chromium + compiled-sidecar benchmark using a loopback carrier
  substitute. Cover nine bandwidth, latency, jitter, retransmission-stall and
  outage profiles, retain source fingerprints and measurements, and enforce
  explicit delivery, timing, ordering, recovery and telemetry protocol gates.

## Verification

Full Go unit suite; frontend tests and typechecks; frontend/standalone builds;
targeted race tests; local sidecar/browser integration scenarios; deterministic
jitter/residence, exact PCM, cancellation and 24-call concurrent bridge tests.
The benchmark was run for 12 and 30 seconds per profile, with additional real
Chromium checks for diagnostics decoding and outage recovery.

In the 30-second matrix, all five usable profiles had zero missing markers and
p95 software audio latency of approximately 48–189 ms. The two-second outage
recovered in both directions with expected loss. Retransmission stalls also lost
some markers. See `benchmarks/softphone/RESULTS.md` and `AUDIO-QUALITY.md`.

## Limits

256 kbit/s per direction is insufficient for the current PCM transport: the
benchmark exposed severe loss and, at constrained download speed, seconds of
network-delayed audio. Application queue limits are not a universal end-to-end
latency guarantee. Marker results are not a perceptual speech-quality score or
proof of PSTN/physical-device behavior. No live carrier call was placed and the
original production delay has not been independently reproduced on the carrier.

No Telnyx integration JSON or SDK/server upgrade is required. Existing unrelated
local work is preserved; raw benchmark run artifacts stay local and are not
published as release assets.
