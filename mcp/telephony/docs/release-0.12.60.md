# Telephony 0.12.60

Adds optional delay-distribution receive buffering and guarded recovery for SIP
and browser WebRTC audio. Telephony remains implemented in Go; the Rust/Tokio
comparison programs are development benchmarks and are not part of this release.

- SIP audio profiles accept `rtp_playback.delay_estimator: "distribution"`.
  Existing project/number/adviser profile scopes and per-call policy pinning apply.
  The existing `ewma` estimator remains the default.
- The installation setting `softphone_webrtc_receive_estimator: "distribution"`
  enables bounded adaptation for new browser and coaching receive sessions.
  `fixed` remains the default. Both options can be changed without redeploying;
  active calls retain their selected receive policy.
- The distribution uses a bounded rolling delay history, increases its target
  promptly and decreases it gradually. Existing capacity and age limits remain.
- WebRTC automatically recovers a persistently rejected, valid progressing stream
  after guarded qualification. It resets stale encoded/decoded queues and repair
  history together, with cooldowns and precise rejection/recovery diagnostics.
- SIP receive starvation resumes fresh speech without rebuilding the initial
  reserve. Synthetic concealment no longer consumes real-audio reserve or
  completes recovery; retained real PCM and decoder continuity are preserved.
- Pins the published Go app SDK to v0.110.0, the latest descendant release.

Existing age protection, initial priming and reference-receiver behavior remain.
Recovery cannot recreate missing speech. This release does not enable either
experimental estimator, alter routing or update an installed deployment.

See [configuration and receive recovery evidence](receive-timing-recovery.md)
and [SIP profiles](sip-audio-profiles.md). Tests cover long-call clock jumps and
delivery shifts, duplicate/reordered/wrapped RTP, malformed input, cooldowns,
real Opus decoding and SIP wire recovery, G.722/PCMA/PCMU starvation and bounded
adaptive-delay comparisons. Release validation: full standalone Go suite (422.721 s), focused race checks
(43.090 s), all 437 frontend/UI tests, vet, TypeScript/panel verification, and
macOS arm64/Linux amd64 builds passed with published SDK v0.110.0. See
[release validation](verification/release-0.12.60.json).
