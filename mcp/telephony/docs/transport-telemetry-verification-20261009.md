# Transport telemetry verification — 9 October 2026

Only Telephony source changed. No installed instance, production/staging
configuration, deployment or live carrier call was used.

## Implemented

- Rich, allowlisted WebRTC stats: receiver, sender, remote receiver/sender,
  selected ICE pair, safe codec metadata and interval delay/rate calculations.
- PCM interval rates, receipt gaps and pre-discard delay/queue maxima.
- Timestamped periodic/incident history in the shared/headless backbone.
- Small diagnostic parts, at most one every 250 ms. WebRTC summaries use the
  same pacing slot. Busy PCM sockets skip monitoring pieces.
- Bounded asynchronous storage, idempotent partial-map merging, explicit
  completeness, expiry, project/operator access and trusted connection mapping.
- Latest published SDK v0.99.0, verified as a descendant of v0.98.0. Its release
  adds optional ingress metadata/documentation; the signed IP helper is retained.

The default transport, PCM/Opus codecs, capture/playback DSP, buffer settings,
late-audio protection, routing, answer handling and reconnect policy are unchanged.
The playback Worklet is byte-identical to the previous committed source.

## Verification

Full Go suite: **784 passes, three opt-in skips**. Targeted race tests cover
collection, reconnects, current-writer attribution, indexed retention, failed
storage and exact full-duplex PCM preservation under a contended collector.
The canonical separate frontend/audio suites passed **99 + 123 = 222 tests**.
Typecheck, packaged asset checks, panel builds and vet passed.
A combined single-process run had one pre-existing renewal timer assertion
failure (one attempt observed by its 3.7-second sleep instead of two); the normal
separate-suite command passed afterward. Renewal logic was not changed. Full details and commands are in the local logs listed below.

The initial rich-report prototype failed the 64/128 kbit/s relay gates. A repeat
still failed at 64 kbit/s. The unchanged source passed both control profiles.
Splitting history fixed 128 kbit/s, but 64 kbit/s still failed when a history
piece and a summary collided. Coordinating both on one pacing schedule fixed
the constrained tests. **No thresholds or quality checks were relaxed.** The
JSON evidence retains the failures, control results and final runs.

| Final check | Upload p95 | Download p95 | Missing upload/download markers |
| --- | ---: | ---: | --- |
| WebRTC 128 kbit/s, 20 sec | 122 ms | 184 ms | 0% / 0% |
| WebRTC 64 kbit/s, 20 sec | 256 ms | 262 ms | 2.63% / 0% |
| WebRTC 64 kbit/s, 60 sec | 252 ms | 273 ms | 0% / 0% |
| PCM 512 kbit/s, 60 sec | 152 ms | 217 ms | 0% / 0% |

The preceding matrix passed baseline, Wi-Fi jitter, PCM 512 kbit/s, intentional
mute and reconnect on both paths, and WebRTC 128 kbit/s. It failed the old
uncoordinated 64 kbit/s gate; the coordinated runs above resolve that failure.
Every new benchmark also requires persisted, complete transport history.
Intentional mute/reconnect profiles allow their expected interruption losses;
they are not evidence of uninterrupted media during a forced disconnect.

The exact-source Chromium Worker microbenchmark processed 1,100 frames in each
direction at 24/44.1/48 kHz, with zero fixture drops. Native 24 kHz averaged
0.008 ms per bidirectional packet pair; fallback resampling averaged 1.62/2.01
ms. This isolates Worker processing and excludes network, devices and server
work. It is not a complete call CPU measurement.

These are local software pipeline checks, not PSTN mouth-to-ear measurements,
perceptual MOS certification or proof that every VPN/shared network is adequate.
Native bandwidth estimates and loss reports improve diagnosis; they do not
establish the cause of a production outage. WebSocket cannot expose browser TCP
retransmission/congestion-window statistics through a standard browser API.

## Evidence

- Committed compact data: `transport-telemetry-verification-20261009.json`.
- Raw network results: `/private/tmp/telephony-rich-network-*-20261009/`.
- Final full Go log: `/private/tmp/telephony-rich-go-verified.jsonl`.
- Frontend regression log: `/private/tmp/telephony-rich-separated-final.log` (canonical command);
  `/private/tmp/telephony-rich-last-frontend.log` retains the combined-run failure.
- Targeted race logs: `/private/tmp/telephony-rich-race-final.log` and
  `/private/tmp/telephony-rich-final-storage.log`.
- Source/runtime contract: `softphone-transports.md`, timestamped history section.
