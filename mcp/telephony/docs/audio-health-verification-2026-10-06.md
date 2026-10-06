# Audio health telemetry — local verification, 6 October 2026

Local, unreleased changes based on Telephony 0.10.1. Only Telephony files were
edited. No production/staging installation, carrier setting, number, route or
live call was changed. Standalone dependency tests use `GOWORK=off`; app SDK
v0.95.0 remains the latest tag by verified commit topology.

## Regression gates

- **655 Go cases/subtests passed**, zero failures; two live Twilio cases skipped.
- **143 frontend/audio tests passed**, zero failures, including sample-exact
  steady PCM at 24/44.1/48 kHz and the existing 30-minute bounded playback test.
- **34 focused race cases passed**, no race reports.
- **All five packaged Chromium surfaces passed**: headless, coaching at 512 kbit/s,
  coaching with jitter, Calls panel and authenticated application user. Actual
  AudioContext suspension/resumption and a 2.2-second UI long task are recorded
  without replacing the carrier/media connection. An actual proxy socket close
  recovers with fresh attach authorization, same call and mute retained.
- The final notice-only guard has a functional regression: inactive/suspended
  playback cannot announce restored speech. Final packaged headless and Calls
  panel checks passed again after rebuilding the client.
- Typechecks, app/asset builds, Go vet and asset SHA256 integrity checks passed.

Backend regressions prove the same carrier socket survives browser disconnect
and fresh reattachment, PCM remains unchanged, and `audio_degraded` does not
change call/media lifecycle. An actual database-write failure leaves microphone
frames flowing and diagnostics queued for retry. Tests also cover trusted proxy
chains and process-scoped hashes, reconnect counter deduplication/restoration,
rolling distinct-call correlation, scopes, cooldown/recovery and hold/silence
exclusion.

## Network matrix

**11 profiles passed; two undersized 256 kbit/s links showed expected degradation.**
No threshold was relaxed. Source hashes and the exact network report are retained
in [machine-readable evidence](../benchmarks/softphone/audio-health-verification-2026-10-06.json).
The benchmark compiled before the final notice-only guard; backend, Worker/DSP
and latency/drop policy are identical. Final guard/unit and packaged browser
checks cover the later UI notice change.

# Softphone network benchmark

Local run: 2026-10-06T12:51:25+02:00. Source revision: `9c46928cb64b9e98c88f1482a90445eb675e43f8` plus local changes. Seed: 20261006. Measurement: 20s/profile, plus warmup/drain.

Actual Chromium, production audio pipeline, compiled Telephony and a local Telnyx L16 substitute. Browser TCP links and carrier links are impaired as specified per profile. Carrier catch-up, missing media, intentional microphone mute and browser reconnect scenarios are included. No production/staging traffic or real calls. Timing uncertainty is approximately ±20ms.

| Profile | Outcome | Adviser → carrier p95 | Missing markers | Carrier → adviser p95 | Missing markers |
|---|---|---:|---:|---:|---:|
| local-baseline | pass | 152 ms | 0.0% | 226 ms | 0.0% |
| broadband | pass | 81 ms | 0.0% | 111 ms | 0.0% |
| wifi-jitter | pass | 207 ms | 2.6% | 301 ms | 2.6% |
| mobile-latency | pass | 186 ms | 0.0% | 203 ms | 0.0% |
| 512k-symmetric | pass | 139 ms | 0.0% | 210 ms | 0.0% |
| tcp-recovery-1pct | pass | 251 ms | 7.9% | 362 ms | 2.6% |
| upload-256k | degraded | 0 ms | 100.0% | 110 ms | 0.0% |
| download-256k | degraded | 67 ms | 0.0% | 0 ms | 100.0% |
| two-second-outage | pass | 150 ms | 10.5% | 193 ms | 10.5% |
| carrier-ten-second-catchup | pass | 51 ms | 0.0% | 178 ms | 52.6% |
| carrier-ten-second-missing | pass | 153 ms | 0.0% | 172 ms | 52.6% |
| intentional-microphone-mute | pass | 53 ms | 13.2% | 96 ms | 0.0% |
| browser-reconnect | pass | 51 ms | 2.6% | 94 ms | 2.6% |

Missing markers measure corruption/loss of identifiable tone sequences, not a percentage of speech samples or a MOS score. Zero latency with zero received markers means no measurement. TCP recovery profiles delay ordered bytes; they do not emulate kernel packet loss/congestion control. Degradation is expected below the raw PCM payload requirement of 384 kbit/s per direction (framing adds overhead). A `usable` profile failing its thresholds fails the command. Adverse profiles report degradation honestly; a `recovery` profile must deliver markers again in the final two seconds.


These local gates are not a zero-loss guarantee, a multi-browser capacity
certification, a production root-cause diagnosis or live carrier verification.
See [behavior/API/configuration](audio-health-telemetry.md).
