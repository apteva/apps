# Private coaching verification — 2 October 2026

## Scope

Telephony 0.10.0, based on released 0.9.1 and current apps main. All implementation, test fixtures and assets are confined to Telephony. SDK remains pinned to v0.91.0, confirmed as the latest fetched release by commit topology. Standalone builds/tests use `GOWORK=off` to exclude unrelated local SDK edits.

No staging/production installation, carrier setting, number or route was changed. No live calls were placed.

## Regression gates

- Full Go suite: **640 cases/subtests passed**, zero failed; two explicitly disabled live Twilio tests skipped.
- Frontend/audio suite: **124 tests passed** (63 frontend + 61 audio), zero failed.
- Focused race suite: **31 cases/subtests passed**, including coaching/listening and concurrent audio isolation.
- Go build/vet, frontend and benchmark typechecks, asset integrity/version checks and two output-clock regression tests passed.
- Existing steady playback stays bit-exact at 24/44.1/48 kHz. The 30-minute local simulation preserves bounded latency; 24 concurrent calls retain independent primary audio.
- New checks cover separate scope/grants, resource and credential isolation, adviser-only audio, unchanged primary PCM, no injection into the carrier/tap/other listeners, deadman expiry, hold/end/revocation/session takeover, late capture/start cancellation, talk audits, flood limits and caller queue priority.

## Real Chromium and compiled sidecar

The actual packaged, integrity-verified headless client joins coaching, captures a synthetic supervisor microphone, transmits through the real server and plays the adviser overlay. The checks require audible playback counters, bounded coaching queue, no microphone at join, immediate track cleanup on release/blur, and cancellation of late microphone permission. The main adviser connection remains live. The separate bundled-host listener path also passes with strict CSP blocking blob scripts/worklets. The SDK module loader itself requires `script-src blob:`, as documented by web-sdk; no SDK changes were made.

Successful coaching checks used:

- Unshaped local adviser transport.
- **512 kbit/s** each direction, 30 ms modeled latency and 10 ms jitter.
- **2 Mbit/s** each direction, 35 ms modeled latency and 40 ms jitter.

Calls panel and genuine online application-user browser regressions also passed. All carrier traffic was loopback test data.

## Directional network matrix

Real Chromium and production softphone worker/worklet plus compiled Telephony, local Telnyx L16 substitute. 20 seconds/profile plus warmup/drain, seed 20261002. The matrix evaluates normal primary audio; the separate coaching checks above exercise active coaching.

| Profile | Outcome | Adviser → carrier p95 / missing markers | Carrier → adviser p95 / missing markers |
|---|---|---:|---:|
| local-baseline | pass | 101 ms / 0.0% | 99 ms / 0.0% |
| broadband | pass | 77 ms / 0.0% | 111 ms / 0.0% |
| wifi-jitter | pass | 130 ms / 0.0% | 138 ms / 0.0% |
| mobile-latency | pass | 364 ms / 2.6% | 349 ms / 2.6% |
| 512k-symmetric | pass | 111 ms / 0.0% | 141 ms / 0.0% |
| tcp-recovery-1pct | pass | 295 ms / 10.5% | 302 ms / 2.6% |
| upload-256k | degraded | 0 ms / 100.0% | 291 ms / 2.6% |
| download-256k | degraded | 204 ms / 0.0% | 0 ms / 100.0% |
| two-second-outage | pass | 153 ms / 10.5% | 351 ms / 10.5% |
| carrier-ten-second-catchup | pass | 155 ms / 0.0% | 182 ms / 52.6% |
| carrier-ten-second-missing | pass | 47 ms / 0.0% | 171 ms / 52.6% |
| intentional-microphone-mute | pass | 169 ms / 15.8% | 216 ms / 0.0% |
| browser-reconnect | pass | 56 ms / 2.6% | 94 ms / 2.6% |

**All matrix gates passed:** eleven profiles passed and two deliberately undersized 256 kbit/s profiles reported degradation. Raw PCM needs 384 kbit/s per direction before framing; zero latency with zero markers means unmeasurable, not instant delivery. Ten-second carrier interruptions retain their expected missing-marker interval and must recover in the final two seconds.

An earlier complete run failed the local-baseline clock invariant: its one-shot browser wall/render mapping produced a negative playback latency while tests ran concurrently. That failed run is retained locally. The benchmark now maps render time through `AudioContext.getOutputTimestamp()` and uses a fresh monotonic fallback when unavailable. Two regression tests check the mapping. No runtime buffer or acceptance gate was relaxed. The corrected complete matrix passed.

Synthetic tone marker timing/loss is not a perceptual quality score or a guarantee for real carrier/network conditions. Coaching is deliberately band-limited to telephone speech bandwidth. Browser matrix calls run sequentially; concurrency coverage is a separate server regression. Network budgets cannot identify unknown fixed first-packet latency. No provider origin or production incident is attributed by these tests.

Use headsets: digital coaching isolation cannot prevent loudspeaker sound being picked up acoustically by an adviser microphone.

Durable compact measurements and runtime hashes: [JSON](../benchmarks/softphone/private-coaching-verification-2026-10-02.json). Full local browser/network logs are retained under the ignored benchmark results directories.
