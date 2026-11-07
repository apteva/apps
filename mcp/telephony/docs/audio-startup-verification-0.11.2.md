# Telephony 0.11.2 verification

Local checks on 9 October 2026. No staging or production installation was changed, and no real carrier calls were placed.

## Final code

749 Go checks/subtests pass, two opt-in real-carrier checks skip. All 212 frontend/audio tests pass in an isolated run. Focused race checks, vet, both typechecks, Linux amd64 release build and panel import checks pass. All five real Chromium surfaces pass: headless, bandwidth-constrained coaching, jitter-constrained coaching, panel navigation and application-user calling.

The 15 startup comparisons retain exact supplied-sample accounting, with no discard. Delayed startup improves against released 0.11.1. The 52 adaptive playback scenarios retain bounded latency, waveform matching, source accounting and voiced/tone/noise/silence coverage. Shared/headless tests verify backpressure telemetry and intentional close handshakes.

## Network performance

The final full matrix passes 25/28 original profile gates. Profiles explicitly designed below usable bandwidth report degradation; a passed gate does not mean an inadequate link has good audio. Browser reconnect recovered but was degraded in that run, then passed its isolated repeat.

Final isolated repeats pass 2/4: broadband and browser reconnect pass; baseline
and seven-second browser catch-up remain failed timing checks. Both failed cases
had zero missing outbound markers. The original full-matrix failures recorded
261–414 ms of independent synthetic source-clock lag. **Network performance is
not consistently all green.** These measurements do not prove that computer
activity explains every failure or certify production audio quality. Public
publication is not installation or permission to activate production.

| Profile | Outcome | Adviser → carrier p95 | Missing markers | Carrier → adviser p95 | Missing markers |
|---|---|---:|---:|---:|---:|
| browser-seven-second-catchup | failed | 467 ms | 0.0% | 462 ms | 39.5% |
| main-thread-two-second-pause | pass | 319 ms | 0.0% | 287 ms | 0.0% |
| browser-48k-fallback | pass | 310 ms | 0.0% | 268 ms | 0.0% |
| local-baseline | failed | 438 ms | 0.0% | 353 ms | 0.0% |
| broadband | failed | 446 ms | 0.0% | 319 ms | 0.0% |
| wifi-jitter | pass | 313 ms | 0.0% | 315 ms | 0.0% |
| mobile-latency | pass | 231 ms | 0.0% | 395 ms | 0.0% |
| 512k-symmetric | pass | 348 ms | 0.0% | 293 ms | 0.0% |
| tcp-recovery-1pct | pass | 448 ms | 7.9% | 481 ms | 0.0% |
| upload-256k | degraded | 0 ms | 100.0% | 488 ms | 7.9% |
| download-256k | degraded | 1180 ms | 2.6% | 0 ms | 100.0% |
| two-second-outage | pass | 220 ms | 13.2% | 358 ms | 10.5% |
| carrier-ten-second-catchup | pass | 60 ms | 0.0% | 331 ms | 52.6% |
| carrier-ten-second-missing | pass | 53 ms | 0.0% | 326 ms | 52.6% |
| intentional-microphone-mute | pass | 49 ms | 13.2% | 134 ms | 0.0% |
| browser-reconnect | degraded | 890 ms | 2.6% | 218 ms | 5.3% |
| webrtc-baseline | pass | 130 ms | 0.0% | 344 ms | 0.0% |
| webrtc-microphone-mute | pass | 143 ms | 13.2% | 387 ms | 2.6% |
| webrtc-reconnect | pass | 122 ms | 2.6% | 305 ms | 2.6% |
| webrtc-udp-128k | pass | 129 ms | 0.0% | 364 ms | 0.0% |
| webrtc-udp-96k | pass | 165 ms | 0.0% | 333 ms | 0.0% |
| webrtc-udp-64k | pass | 234 ms | 0.0% | 457 ms | 0.0% |
| webrtc-udp-48k | degraded | 249 ms | 57.9% | 617 ms | 63.2% |
| webrtc-udp-32k | degraded | 0 ms | 100.0% | 534 ms | 94.7% |
| websocket-64k | degraded | 0 ms | 100.0% | 0 ms | 100.0% |
| adaptive-160-280ms-jitter | pass | 375 ms | 0.0% | 481 ms | 0.0% |
| adaptive-220-280ms-jitter | pass | 255 ms | 0.0% | 479 ms | 0.0% |
| adaptive-280-280ms-jitter | pass | 285 ms | 0.0% | 482 ms | 0.0% |

## Earlier runs retained

Before the rounding fix, the first full matrix passed 22/28 gates; six isolated repeats passed 5/6. A fresh full matrix passed 24/28; repeating its four failures passed 2/4. A subsequent mobile/512k pair passed 1/2. Unchanged 0.11.1 failed its baseline control but passed later mobile/512k controls. This variability does not exonerate the app or prove host activity caused a failure.

Investigation identified near-target resampler startup residue waiting for another whole packet. The final implementation tolerates at most one render block after waiting for the target, with an absolute 40 ms floor; a single 20 ms packet still cannot start continuous playback. New 24/44.1/48 kHz regression checks cover that boundary. After that change, the focused mobile/512k pair passed 2/2 with no missing markers. No benchmark threshold or simulation timeout was relaxed.

One complete frontend run during a release build had three simulation runtime timeouts; the complete isolated rerun passed with the original five-second limit. Host snapshots also showed high load and almost full memory, with macOS storage processes active. Both observations are retained rather than presenting earlier failures as successes.

## Processing cost and limits

Chromium Worker measurements use the exact packaged source with clock probes and telemetry active: zero drops in 1,100 full-duplex frames per rate. Mean callback cost is about 0.01 ms at native 24 kHz, 1.66 ms at 44.1 kHz and 2.11 ms at 48 kHz. Resampling costs approximately 8–11% of one core in this fixture; this excludes hardware, network, Worklet IPC and server work.

Synthetic markers measure loss and delay, not a subjective speech quality score. These checks cannot reconstruct Long's missing packet history or guarantee uninterrupted calls on every computer/network. Hard stale-audio caps, microphone DSP, carrier call control and WebRTC native media remain intact.

Detailed bounded results and source fingerprints: [verification JSON](../benchmarks/softphone/startup-backpressure-verification-2026-10-09.json).
