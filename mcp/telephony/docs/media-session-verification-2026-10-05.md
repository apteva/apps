# Media session verification — 5 October 2026

Local changes based on Telephony 0.10.0. All prior app changes remain included.
Only Telephony was edited; production, staging and carrier configuration were
untouched. No live calls or installation activations occurred. No release was
published. Dependency tests use standalone app SDK v0.95.0 (`GOWORK=off`).

## Final gates

- Full Go suite: **648 cases/subtests passed**, zero failed; two live Twilio
  tests intentionally skipped.
- Frontend/controller/audio: **137 passed**, zero failed. Includes sample-exact
  steady playback at 24/44.1/48 kHz and a simulated 30-minute conversation with
  zero dropped samples, two underruns and bounded latency.
- Race checks on affected media, authorization, listening and coaching paths:
  **42 cases/subtests passed**, zero failures or race reports. An earlier broader
  run also passed 51 cases, including the busy-project call-query regression.
- Actual packaged client + compiled Telephony + Chromium: **all five surfaces
  passed** (headless, coaching at 512 kbit/s, coaching with jitter, Calls panel,
  authenticated application user). A real HTTP 503 is injected during renewal;
  the existing audio socket stays open. A real proxy disconnect forces fresh
  attach authorization and recovery of the same call with mute retained.
- TypeScript checks, benchmark typecheck, generated client/Calls panel builds
  and `git diff --check` passed.

The regressions also cover expiry during hung requests, delayed/background
callbacks, late authorization after hangup, duplicate credential replies,
AudioContext pause/resume and stale clock samples, HTTP lookup/write outages,
known revocation, last-verified lease expiry, private talk interruption during
uncertain permission checks, and accurate rejected-frame diagnostics.

## Final connection matrix

Seed 20261005; 20 seconds/profile plus warmup and drain. Actual Chromium, the
production worker/worklet/DSP, compiled Telephony and a local Telnyx L16 carrier
substitute. Both audio directions are measured. Reconnect obtains fresh
credentials through the real local attach endpoint. Latency/loss gates are
unchanged.

| Profile | Outcome | Adviser → carrier p95 | Carrier → adviser p95 | Missing markers up/down |
|---|---|---:|---:|---:|
| local-baseline | pass | 80 ms | 106 ms | 0.0% / 0.0% |
| broadband | pass | 84 ms | 112 ms | 0.0% / 0.0% |
| wifi-jitter | pass | 246 ms | 293 ms | 0.0% / 0.0% |
| mobile-latency | pass | 227 ms | 271 ms | 0.0% / 0.0% |
| 512k-symmetric | pass | 106 ms | 139 ms | 0.0% / 0.0% |
| tcp-recovery-1pct | pass | 256 ms | 281 ms | 10.5% / 2.6% |
| upload-256k | degraded | 0 ms | 182 ms | 100.0% / 0.0% |
| download-256k | degraded | 73 ms | 0 ms | 0.0% / 100.0% |
| two-second-outage | pass | 220 ms | 201 ms | 10.5% / 10.5% |
| carrier-ten-second-catchup | pass | 50 ms | 182 ms | 0.0% / 52.6% |
| carrier-ten-second-missing | pass | 46 ms | 175 ms | 0.0% / 52.6% |
| intentional-microphone-mute | pass | 46 ms | 99 ms | 10.5% / 0.0% |
| browser-reconnect | pass | 48 ms | 91 ms | 2.6% / 2.6% |

**Result: 11 profiles passed; two 256 kbit/s profiles reported expected
degradation.** Raw PCM needs approximately 384 kbit/s per direction plus framing.
Outage/mute profiles intentionally lose markers; passing means their latency and
recovery gates were met, not that missing speech was restored.

## Retained failed run

An earlier full run failed Wi-Fi jitter: upstream p95 4633 ms, 7.9% missing
markers; downstream p95 221 ms, 21.1% missing. Browser playback discarded about
3567 ms at the hard limit. Browser/source render scheduling was suspected, but
that run did not record the new clock-progress diagnostic, so its exact cause
is not established.

The isolated retry with the same seed passed (129/151 ms upstream/downstream
p95, zero missing markers, maximum source/playback clock lag approximately
1.3/1.5 ms). The final full matrix also passed Wi-Fi jitter. The failed result
is retained in the JSON evidence; thresholds were not relaxed.

## Limits

These tests verify the fixes and local regression gates. They do not establish
today's production root cause, guarantee all network/device/carrier conditions,
or provide a MOS/intelligibility score. Timing uncertainty is approximately
±20 ms; missing markers are identifiable tone loss, not a speech-sample loss
percentage. TCP recovery delays ordered bytes and does not emulate full kernel
packet-loss/congestion behavior. Real carrier verification remains unperformed.

Machine-readable evidence and hashes of tested source/assets:
[verification JSON](../benchmarks/softphone/media-session-verification-2026-10-05.json).
Design and behavior: [media session resilience](media-session-resilience.md).
