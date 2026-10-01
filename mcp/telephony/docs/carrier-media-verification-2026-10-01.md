# Carrier media delivery verification — 1 October 2026

## Scope

Local changes on `fix/telephony-carrier-delivery`, based on Telephony 0.9.0 / `dc5a2082`. No staging or production instance, carrier account, routes or numbers were changed. No version was published by this task. All code and regenerated frontend assets are included in this branch. SDK dependency is pinned to v0.90.0, the latest fetched SDK tag by commit topology.

The fix retains carrier source timestamps and sequences through the human audio pipeline, discards stale delivery within a 320 ms excess-age budget, diagnoses caller-direction stalls independently of adviser audio, surfaces interruption/recovery notices, and avoids blocking carrier reception on WebSocket pong writes. Routing, answer operations and the buffered AI policy are unchanged. See [design](carrier-media-delivery.md).

## Regression results

- Standalone `GOWORK=off go test -json ./...`: **625 passed**, zero failed, two explicitly disabled live Twilio tests skipped.
- Frontend/audio `bun run test:frontend`: **115 passed** (61 + 54), zero failed. Includes steady bit-exact playback, multiple sample rates and the 30-minute simulation.
- Focused application race check: all **10** new carrier timing, notice, source framing, concurrent isolation, operator attribution, Twilio bridge, expiry and pong tests passed. Harness race check: **5** passed.
- Go build, `go vet`, frontend typecheck, benchmark typecheck and benchmark gate/metadata tests passed. Packaged client manifest and worker/worklet source hashes match.
- Real Twilio JSON/G.711 → production loopback bridge → browser WebSocket test preserves metadata, discards simulated ten-second-old speech, keeps fresh audio and retains adviser transmission.
- Concurrent regression covers **24 independent calls**. The browser matrix runs one call at a time; it is not a multi-browser capacity certification.

## Durable network matrix

Real Chromium 153.0.8010.12, production SoftphoneSession/worker/worklets and compiled Telephony with a local Telnyx L16 substitute. Both browser directions and selected carrier links are independently shaped. 20 seconds per profile plus warmup/drain, seed 20261001. Reported software timing uncertainty approximately ±20 ms; per-run browser clock uncertainty is retained in the JSON evidence.

**This complete short matrix exited 1:** ten profiles passed, two deliberately undersized links were degraded, and Wi-Fi exceeded its marker-loss gate. The failure is retained, not reclassified or hidden.

| Profile | Outcome | Adviser → carrier p95 / missing markers | Carrier → adviser p95 / missing markers |
|---|---|---:|---:|
| local-baseline | pass | 106 ms / 0.0% | 20 ms / 0.0% |
| broadband | pass | 84 ms / 0.0% | 81 ms / 0.0% |
| wifi-jitter | failed | 202 ms / 10.5% | 270 ms / 7.9% |
| mobile-latency | pass | 191 ms / 0.0% | 175 ms / 0.0% |
| 512k-symmetric | pass | 105 ms / 0.0% | 110 ms / 0.0% |
| tcp-recovery-1pct | pass | 253 ms / 13.2% | 301 ms / 2.6% |
| upload-256k | degraded | 0 ms / 100.0% | 80 ms / 0.0% |
| download-256k | degraded | 67 ms / 0.0% | 0 ms / 100.0% |
| two-second-outage | pass | 232 ms / 13.2% | 171 ms / 10.5% |
| carrier-ten-second-catchup | pass | 51 ms / 0.0% | 161 ms / 52.6% |
| carrier-ten-second-missing | pass | 51 ms / 0.0% | 151 ms / 52.6% |
| intentional-microphone-mute | pass | 53 ms / 10.5% | 61 ms / 0.0% |
| browser-reconnect | pass | 61 ms / 2.6% | 66 ms / 2.6% |

Zero p95 with zero received markers means timing was unmeasurable. Missing markers count damaged or missing identifiable synthetic tone sequences, not a percentage of lost speech samples or a perceptual quality score.

### Wi-Fi follow-up

The short run lost four consecutive adviser markers out of 38 (10.53%, gate 10%). It shows approximately 190–250 ms coincident carrier ingress and microphone receipt gaps, while the modeled browser links scheduled at most 97/139 ms of delay. Browser microphone age peaked at 185 ms; bounded capture/server queues dropped old or excess audio. This is consistent with a shared scheduling disturbance during that interval, but the logs do not prove the exact source. No threshold or runtime buffer was relaxed.

Two longer **60-second** Wi-Fi runs used seeds 20261001 and 20261002. Both passed, with **118/118 markers delivered in each direction**, no duplicate or out-of-order markers.

| Seed | Adviser → carrier p95 | Carrier → adviser p95 | Missing markers each way |
|---|---:|---:|---:|
| 20261001 | 175 ms | 226 ms | 0% |
| 20261002 | 181 ms | 245 ms | 0% |

These longer results support recovery/steady quality under the modeled Wi-Fi conditions. They do not erase the short-run failure or certify acceptable loss under arbitrary host pauses and network impairment.

### Carrier interruption findings

- Ten-second catch-up: **9,680 ms of actually received stale PCM discarded**, one diagnosed stall and recovery, both browser notices delivered. Adviser direction delivered every marker. Fresh caller audio resumed without replaying ten seconds of old speech.
- Ten-second missing frames: **zero received-stale PCM discarded**, one stall and recovery, both notices delivered. The missing interval is not added to discard accounting. Adviser direction delivered every marker.
- Intentional microphone mute did not produce a caller transport stall. Browser reconnection recovered and retained directional audio.
- The 256 kbit/s links were correctly reported as degraded: raw 24 kHz/16-bit mono audio needs **384 kbit/s each way before framing**. This task does not add an adaptive compressed codec.

## Reproduction and evidence

The [compact JSON](../benchmarks/softphone/carrier-delivery-verification-2026-10-01.json) retains exact commands, source manifest checksums, selected source file hashes, benchmark outcomes, latency/loss, source discard/incident counters, notices, browser timing and raw report checksums. Raw results/browser traces and test logs are retained locally under the ignored results directory.

- [2026-10-01-carrier-delivery-verified report](../benchmarks/softphone/results/2026-10-01-carrier-delivery-verified/REPORT.md)
- [2026-10-01-wifi-60s report](../benchmarks/softphone/results/2026-10-01-wifi-60s/REPORT.md)
- [2026-10-01-wifi-60s-seed2 report](../benchmarks/softphone/results/2026-10-01-wifi-60s-seed2/REPORT.md)

Runtime code and asset fingerprints match across all three benchmark runs. Reports identify the base Git revision plus local changes; the subsequent local commit captures the changes.

## Remaining evidence boundary

The local scenarios verify bounded buffering, loss accounting, notices, backwards compatibility and regression behavior. They cannot restore speech delivered ten seconds late, guarantee zero perceptual quality loss on all connections, or determine whether Twilio, a proxy/network, or server scheduling caused the production interruption. Correlating authorized carrier traces with the new directional/source diagnostics remains necessary. No live or staging call was made.
