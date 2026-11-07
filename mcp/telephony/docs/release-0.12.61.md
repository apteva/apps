# Telephony 0.12.61

Fixes unnecessary padding in server-to-browser WebRTC playback. When a complete
20 ms frame is already selected for the current tick, retain the partial PCM
remainder for the next chunk instead of padding it and adding another frame to
the queue. This prevents small arrival/chunk variations from manufacturing audio
that fills the queue and displaces received speech.

- Flush final partial speech only on a tick without a complete frame to send.
- Continue expiring stale partials even when a complete frame is available.
- Preserve the six-frame/120 ms queue bound, original sample deadlines, pacing,
  codec/resampler configuration and media-direction diagnostics.
- Add regression tests to the media race-check workflow.

Five deterministic eight-second traces with ±1 sample chunk variation and ±1 ms
arrival variation reduced overflow accounting from 200–400 ms to zero. Every real
PCM sample was preserved in order. Tests cover natural speech through 24/48 kHz
encoder paths, resampling, Opus/RTP, final-word tails, and expired audio after a
scheduling pause. These reproduce the defect, not the exact production incident.

See [replay evidence](verification/webrtc-outgoing-partial-20261028.json) and
[release validation](verification/release-0.12.61.json). No buffer settings or
experimental receive-estimator defaults change. Publishing does not upgrade
installed deployments.

Release checks passed: full standalone Go suite (441.424 s), focused race checks
(7.255 s), all 437 frontend tests, vet, TypeScript/panel verification, and macOS
arm64/Linux amd64 builds using published app SDK v0.110.0.
