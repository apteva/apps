# Browser audio delay investigation — 7 October 2026

## Conclusion

Local tests did not reproduce processing overload in Telephony's normal audio
path. Delaying browser delivery reproduces the reported mechanism while
carrier ingress, server queues and microphone delivery stay healthy. This does
not establish which production network component delayed Manon's audio.

There is a confirmed diagnostics defect: playback Worklet statistics erase
the Worker's earlier playback rejection events. The local change preserves
both bounded event histories. It changes diagnostics only. Worker, Worklet,
320 ms rejection policy, carrier handling and reconnection bytes/behavior
are unchanged.

All work was local. No production/staging inspection, configuration changes,
installation, activation or live calls were performed. The production version,
served Worker hash and raw incident data have not been supplied for this run.

## What was reproduced

- Exact-source controlled Worker test: a 7.23 second delivery delay and ordered
  catch-up at 0.3 ms/frame produce **351 rejected frames, exactly 7.02 seconds**,
  followed by successful fresh playback. This is a selected synthetic arrival
  schedule; matching a total does not establish the real arrival schedule.
- Actual Chromium + compiled Telephony + loopback Telnyx L16 substitute:
  pausing browser download for 7.23 seconds produces 7.229 seconds maximum
  relative delivery excess, **6.92 seconds rejected at the Worker** and
  0.18 seconds discarded at playback. Carrier ingress has zero stalls and
  zero stale discard. The server playback queue peaks at 20 ms; its recorded
  maximum write/residence times round to zero ms. Microphone markers all arrive.
  Playback recovers; received marker p95 latency is 172 ms. Missing playback
  markers are 39.5%, primarily during the imposed outage.
- Deliberately regressing the mapped source clock by 7.23 seconds, while keeping
  the send clock fresh, imitates the rejection signal. This is an invalid
  fixture, **not a discovered production clock defect**. The relative-delay
  maximum alone cannot establish network buffering as the cause.
- Prompt delivery with carrier timestamp/epoch restarts, repeated valid clock
  probes, microphone mute and wall-clock changes has no Worker playback loss.
  Server source mapping restart/invalid timestamp/concurrent-call tests pass.

## Diagnostics defect and local fix

The Worker already emits timestamped `transport.drop` events. Releases 0.10.1
and later also update worst-case transit/source age before rejecting frames,
when a fresh clock estimate exists. The claim that this return bypasses those
statistics does not match these releases. Worker bytes are identical between
0.10.2, 0.10.3 and 0.10.4:

`38ff3c201aa506b38cbd1b44254539b9654a88bc44c4de74bc7e8a54a47c5133`

However, `SoftphoneSession.installWorkletDiagnostics()` previously replaced
every caller-direction event with the Worklet's own rolling snapshot. Worker
rejection timestamps could disappear before the next diagnostics upload.
Totals survived, explaining how loss could be visible without its events.

The regression failed before the local fix. The session now stores Worker and
Worklet event histories separately, merges them in timestamp order, and caps
the published list at 100 events. Repeated snapshots do not duplicate events;
an empty Worklet snapshot cannot erase Worker events. Panel and headless
bundles were rebuilt from this same backbone.

Clock-based transit maxima remain estimates with uncertainty and sample-age
limits. An absent usable clock estimate must not be interpreted as zero delay.

## Real-browser network and load results

20 second measurements/profile, seed 20261007, Chromium 153.0.8010.12,
Apple M1 Pro, Darwin arm64. Latencies are p95 of identifiable synthetic markers,
approximately ±20 ms. Missing markers are not percentages of speech samples.

| Scenario | Adviser → carrier p95 / missing | Carrier → adviser p95 / missing | Result |
|---|---:|---:|---|
| Baseline | 223 ms / 2.6% | 200 ms / 2.6% | Within usable gates |
| Broadband | 78 ms / 0% | 111 ms / 0% | Pass |
| Wi-Fi jitter | 124 ms / 0% | 133 ms / 0% | Pass |
| Mobile latency | 183 ms / 0% | 202 ms / 0% | Pass |
| 512 kbit/s each direction | 108 ms / 0% | 139 ms / 0% | Pass |
| 256 kbit/s download | 76 ms / 0% | No measurable playback / 100% | Expected degradation |
| Two-second outage | 170 ms / 10.5% | 202 ms / 10.5% | Recovery gates pass |
| Seven-second browser catch-up | 58 ms / 0% | 172 ms / 39.5% | Recovery and directional attribution pass |
| Two-second UI thread block | 41 ms / 0% | 99 ms / 0% | Pass, runtime pause observed |
| Forced 48 kHz fallback | 57 ms / 0% | 101 ms / 0% | Pass, no playback drops |
| Intentional microphone mute | 165 ms / 10.5% | 255 ms / 0% | Pass; mute does not trigger carrier stall |
| Browser reconnect | 43 ms / 2.6% | 91 ms / 2.6% | Reconnect and recovery pass |

The first UI-block gate incorrectly compared the full busy interval with the
observer's excess delay beyond a one-second tick. Audio had zero loss. The
harness comparison was corrected, tested and rerun; the table uses the passing
rerun. The other rows retain their original recorded results.

The profiles use ordered TCP byte shaping. They do not emulate kernel TCP
congestion control, VPN software, Wi-Fi radio, a production proxy or the PSTN.

## Processing cost and concurrency

A separate Chromium Worker microbenchmark executes the exact production
Worker, including clock probes and timing calculations, on 1,100 pairs of
20 ms packets per sample rate (100 warmup, 1,000 measured):

| AudioContext rate | Mean processing/pair | p95 | Approx. one-core cost at 50 pairs/s |
|---|---:|---:|---:|
| 24 kHz | <0.01 ms | Below timer resolution | <0.1% |
| 44.1 kHz fallback | 1.56 ms | 1.70 ms | 7.8% |
| 48 kHz fallback | 1.92 ms | 2.00 ms | 9.6% |

All packet pairs are transmitted/accepted without drops. This microbenchmark
bypasses real socket and Worklet IPC and is not a whole-browser CPU estimate.
The full 48 kHz Chromium profile separately exercises production capture,
playback, resampling and sockets. Do not substitute Bun's VM performance for
browser Worker performance; that VM's fallback math overhead differs greatly.

Server human-path input processing and duplex resampling measured 23–29 µs per
20 ms frame in three focused runs. This excludes network, database, routing and
scheduling. The existing 24-call concurrent bidirectional server bridge test
passes, including under the race detector; source timing isolation passes too.
This is not a full many-browser production capacity certification or a test on
older adviser devices.

## What still needs measuring or improving

1. Establish the deployed client/Worker version and hash. Match raw incident
   timestamps, clock uncertainty/sample freshness and directional totals.
2. Add bounded aggregated drop-burst samples carrying first/last timestamps,
   frame count/duration, send and mapped source clocks, source epoch/sequence,
   receive clock, excess delay and clock confidence. Timestamp preservation
   is now fixed locally; these richer burst fields are still a proposal.
3. Use the already implemented, unreleased connection/network telemetry to
   associate intervals with the authenticated adviser, connection ID and
   trusted client IP. An office exit match identifies a known exit; an
   unmatched IP does not prove there is no VPN.
4. Measure TCP delivery/ACK/retransmission and proxy/VPN behavior alongside
   server frame send and browser receipt. A completed server write means the
   kernel accepted bytes, not that the browser received them.
5. Account for our bandwidth choice: mono 24 kHz PCM16 requires **384 kbit/s
   each direction before framing**, roughly 0.4 Mbit/s with our headers, plus
   WebSocket/TCP/TLS overhead. Shared VPN links need capacity for every active
   adviser. Benchmark compressed transport separately before selecting it;
   do not disable stale-audio protection or enlarge buffers to hide congestion.

## Verification and evidence

- 158 frontend/audio tests pass (87 frontend, 71 audio), including exact loss
  accounting, timestamp retention, 30-minute simulated conversation, clock
  probes/restarts, mute, reconnect and bounded buffers.
- Focused server bridge/source tests, browser-network tests and network-model
  tests pass with the race detector. Benchmark gate unit tests pass.
- Frontend and benchmark typechecks, Telephony-only panel/headless builds,
  panel import checks and `git diff --check` pass.
- Network results: `/private/tmp/telephony-microcuts-network-20261007/`.
- Browser/load results: `/private/tmp/telephony-microcuts-load-20261007/`.
- Corrected UI-block rerun: `/private/tmp/telephony-microcuts-mainthread-rerun-20261007/`.
- Chromium CPU evidence: `/private/tmp/telephony-microcuts-worker-chromium-20261007.json`.
- Test log: `/private/tmp/telephony-microcuts-frontend-tests.log`.

Reproduction commands are documented in `../benchmarks/softphone/README.md`.
The new scenarios and regression tests remain in the Telephony app only.
