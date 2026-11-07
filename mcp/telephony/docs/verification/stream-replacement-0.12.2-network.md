# Softphone network benchmark

Local run: 2026-10-11T13:05:28+02:00. Source revision: `3e3a7027f778bf6bbd0883f1d80faa58bc2dc552` plus local changes. Seed: 20260929. Measurement: 20s/profile, plus warmup/drain.

Actual Chromium, production audio pipeline, compiled Telephony and a local Telnyx L16 substitute. Browser TCP links and carrier links are impaired as specified per profile. WebRTC profiles with `rtc_udp: true` force the selected browser candidate through a local TURN relay and shape its actual encrypted UDP traffic, including SRTP, RTCP, TURN encapsulation/control and 28 bytes IPv4/UDP overhead per datagram. Signaling TCP shares the same per-direction capacity with a conservative 52-byte header allowance per proxy chunk; kernel TCP ACK/retransmission and Ethernet overhead are excluded. Setup before the measurement epoch is unrestricted. Other WebRTC profiles shape signaling TCP only. The 200ms network queue drops excess datagrams instead of storing seconds of speech. Rates are separate per direction, per call; unrelated application traffic is excluded. Carrier catch-up, missing media, intentional microphone mute and browser reconnect scenarios are included. No production/staging traffic or real calls. Timing uncertainty is approximately ±20ms.

| Profile | Outcome | Adviser → carrier p95 | Missing markers | Carrier → adviser p95 | Missing markers |
|---|---|---:|---:|---:|---:|
| local-baseline | pass | 56 ms | 0.0% | 94 ms | 0.0% |
| wifi-jitter | pass | 129 ms | 0.0% | 183 ms | 0.0% |
| carrier-ten-second-catchup | pass | 45 ms | 0.0% | 322 ms | 52.6% |
| carrier-ten-second-missing | pass | 51 ms | 0.0% | 323 ms | 52.6% |
| intentional-microphone-mute | pass | 42 ms | 13.2% | 94 ms | 0.0% |
| browser-reconnect | pass | 44 ms | 2.6% | 92 ms | 2.6% |
| webrtc-baseline | pass | 126 ms | 0.0% | 163 ms | 0.0% |
| webrtc-udp-96k-jitter-loss | pass | 158 ms | 0.0% | 275 ms | 0.0% |

Missing markers measure corruption/loss of identifiable tone sequences, not a percentage of speech samples or a MOS score. Zero latency with zero received markers means no measurement. TCP recovery profiles delay ordered bytes; they do not emulate kernel packet loss/congestion control. Degradation is expected below the raw PCM payload requirement of 384 kbit/s per direction (framing adds overhead). A `usable` profile failing its thresholds fails the command. Adverse profiles report degradation honestly; a `recovery` profile must deliver markers again in the final two seconds.
