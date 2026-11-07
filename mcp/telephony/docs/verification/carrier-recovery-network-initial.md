# Softphone network benchmark

Local run: 2026-10-09T16:20:48+02:00. Source revision: `902c99d80d9d1b137917b823c47917809659ceae` plus local changes. Seed: 20260929. Measurement: 20s/profile, plus warmup/drain.

Actual Chromium, production audio pipeline, compiled Telephony and a local Telnyx L16 substitute. Browser TCP links and carrier links are impaired as specified per profile. WebRTC profiles with `rtc_udp: true` force the selected browser candidate through a local TURN relay and shape its actual encrypted UDP traffic, including SRTP, RTCP, TURN encapsulation/control and 28 bytes IPv4/UDP overhead per datagram. Signaling TCP shares the same per-direction capacity with a conservative 52-byte header allowance per proxy chunk; kernel TCP ACK/retransmission and Ethernet overhead are excluded. Setup before the measurement epoch is unrestricted. Other WebRTC profiles shape signaling TCP only. The 200ms network queue drops excess datagrams instead of storing seconds of speech. Rates are separate per direction, per call; unrelated application traffic is excluded. Carrier catch-up, missing media, intentional microphone mute and browser reconnect scenarios are included. No production/staging traffic or real calls. Timing uncertainty is approximately ±20ms.

| Profile | Outcome | Adviser → carrier p95 | Missing markers | Carrier → adviser p95 | Missing markers |
|---|---|---:|---:|---:|---:|
| local-baseline | failed | 786 ms | 15.8% | 421 ms | 10.5% |
| broadband | failed | 377 ms | 0.0% | 259 ms | 0.0% |
| wifi-jitter | pass | 261 ms | 0.0% | 312 ms | 0.0% |
| 512k-symmetric | pass | 351 ms | 0.0% | 396 ms | 0.0% |
| two-second-outage | pass | 397 ms | 10.5% | 489 ms | 10.5% |
| carrier-ten-second-catchup | failed | 503 ms | 21.1% | 465 ms | 55.3% |
| carrier-ten-second-missing | pass | 307 ms | 0.0% | 468 ms | 52.6% |
| intentional-microphone-mute | pass | 288 ms | 10.5% | 279 ms | 0.0% |
| browser-reconnect | pass | 315 ms | 2.6% | 234 ms | 2.6% |
| webrtc-baseline | failed | 203 ms | 26.3% | 374 ms | 23.7% |
| webrtc-microphone-mute | pass | 213 ms | 15.8% | 384 ms | 15.8% |
| webrtc-reconnect | pass | 120 ms | 2.6% | 304 ms | 2.6% |

Missing markers measure corruption/loss of identifiable tone sequences, not a percentage of speech samples or a MOS score. Zero latency with zero received markers means no measurement. TCP recovery profiles delay ordered bytes; they do not emulate kernel packet loss/congestion control. Degradation is expected below the raw PCM payload requirement of 384 kbit/s per direction (framing adds overhead). A `usable` profile failing its thresholds fails the command. Adverse profiles report degradation honestly; a `recovery` profile must deliver markers again in the final two seconds.
