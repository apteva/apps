# Telephony 0.12.27

## Microphone backlog recovery

The WebRTC capture-release queue now catches up after a delayed batch instead
of retaining a nearly expired queue phase indefinitely. When multiple frames
are buffered and the oldest source age reaches 180 ms, it discards a bounded
prefix toward 80 ms source age, retaining at least the newest frame. Each frame
keeps its original source timestamp and the existing 250 ms stale-audio limit.
At most one frame is released per media cadence; there is no catch-up RTP burst.

Recovery skips are reported as `webrtc_capture_release_recovery` and included
in microphone discard totals. A bounded 5 ms crossfade reduces the discontinuity
after a skip. Healthy frames retain their exact samples and timestamps.
The shared capture path applies to human inbound/outbound calls using WebRTC,
independently of the carrier. SIP sender, recording and admission settings retain
their current behavior. No TURN configuration is changed by this release.

## Repeatable outbound browser-to-SIP tests

`bun run benchmark:outbound-sip --minutes 30 --calls 8 --recording true`
starts real Chromium through the production capture Worklet, WebRTC/Opus,
Telephony decoder/release queue, authenticated loopback, outbound SIP engine
and a local TLS/SRTP carrier. PCMA and G.722 calls exercise healthy control,
jitter, delivery shifts and burst pauses, with microphone capture before answer.
`--pause-ms 438`, `441` and `570` model the reported production gap range.
These are representative faults, not exact production packet replays.

Results include source-marker delay by minute, median growth, healthy and
injected interruptions separately, fresh-content recovery, queue/drop counters,
recording drops, SIP refreshes and host-load evidence. Source hashes and exit
status are retained, including failed runs. CI includes a short eight-call
smoke; the full wall-clock soak remains opt-in. No Docker is required.

## Validation and known limits

- Full local Go suite, 306 frontend/UI tests, type checks, vet and focused race
  checks passed. Release manifest/source-ref and generated asset checks apply.
- Deterministic regression checks cover 420/438/441/570 ms delayed batches,
  downstream source-budget headroom, unchanged healthy speech and bounded queues.
- Healthy capture release measured 52–63 ns/frame, zero allocations, on the
  local Apple M1 Pro. This measures the queue, not the entire call pipeline.
- Eight-call 438/441 ms short runs passed locally. The 570 ms run recovered
  audio but failed the 40 ms median-growth gate on five calls.
- Eight calls completed 30 minutes with maximum positive median growth of
  28.4 ms, fresh-content recovery below 800 ms, no recording drops and 41 SIP
  refreshes per call. The run nevertheless failed the 160 ms healthy-interruption
  gate. The 16-call run stopped at 1,027 seconds with interruptions and a stalled
  stream. Healthy controls also had common interruptions, coinciding with local
  host CPU spikes; this does not establish production CPU overload.

The retained summary is `benchmarks/outbound-sip/results-2026-10-21.json`.
This release fixes a reproduced backlog mechanism; it does not certify that all
production microphone interruptions are resolved or prove production capacity.
It cannot recover speech that arrives genuinely too late. Distinguishing
persistently delayed delivery from stale RTP-to-source mapping remains relevant.

Both manifests, source refs and frontend metadata use 0.12.27. SDK remains
v0.102.0, the latest published descendant of v0.101.0 verified before release.
Publishing does not install, restart or change production/staging.
