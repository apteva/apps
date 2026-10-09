# Audio stress recovery and validated browser network diagnostics

## Changes

- Recover an expired live PCM write only when the WebSocket writer confirms zero
  bytes of the frame (including its header) were written. Drop the old frame,
  count `write_timeout_drops` and stale audio, then give the next frame a fresh
  deadline. Partially written frames, control errors and connection failures
  remain fatal because abandoning a partial frame corrupts framing. This does
  not add a queue or extend the 120 ms queue / 250 ms residence limits.
- Use published app-sdk v0.98.0 `ClientIPFromRequest` at the authenticated browser
  handshake, before upgrading. Retain the resolved client and actual socket-peer
  IP for the connection. Invalid assertions log fixed reason strings and fall
  back to the socket peer, without denying or ending media. Reconnects resolve
  again and keep the existing carrier leg.
- Use the same resolved address for process-scoped peer hashes and configured
  known-VPN classification. Unmatched addresses remain `unknown`. Raw addresses
  remain operator-only and are not added to ordinary call diagnostics, SSE or
  browser messages. PCM/WebSocket and optional WebRTC share the handshake; for
  WebRTC the address describes signaling, not necessarily its UDP media route.
- Add benchmark-only, independent Worklet-clock observations from a Worker and
  timestamped host CPU/load observations. No production Worker/Worklet audio DSP,
  codec, sample rate, speech matching or queue target has been changed.

## Why the benchmark was inconsistent

The local benchmark runs browser, proxy, Telephony and a carrier substitute on
one Mac with 10 logical CPUs and 16 GiB RAM. During previous failures its load
average reached 29–44 and sampled CPU reached 99.5–100% for several seconds.
The same source passed other replays, including a 60-second UI-pause and baseline
pair with no missing markers or underruns. In one failed replay, markers were
lost at 18–19 seconds, after the intentionally blocked UI at 6–8 seconds.

Another replay exposed an actual fault: an audio write deadline could close the
internal PCM WebSocket without sending any bytes, cascading into the carrier
bridge. The regression tests fail with fresh-audio EOF on the unchanged 0.11.2
control, and pass after recovery is added. Server-side and client-side framing
are both covered, with exact fresh PCM bytes and accounting assertions.

CPU saturation can still prevent real-time processing. This fix cannot recover
speech already delayed or lost, make partial TCP writes safe to abandon, or
prove the origin of any production incident. Carrier JSON/control writes retain
their existing error handling. Benchmark scores are not adjusted or relaxed
based on host load; uncertain clock probes are excluded from observations only.

## Verification

All checks use isolated local fixtures and GOWORK=off; no installation, staging,
production or live carrier is used. Telephony pins the public SDK module rather
than the dirty workspace overlay.

- SDK: full suite, 264 checks passed; client-IP race checks and vet passed.
- Telephony: full Go suite, 779 checks passed and 3 opt-in checks skipped.
- Frontend: 96 headless/client checks and 116 UI/audio checks passed. An initial
  simulation hit Bun's five-second execution timeout; the full replay passed
  with a 30-second execution timeout and identical audio assertions. The repeated
  jitter simulations now declare this timeout explicitly, like the existing
  long-call simulation, without changing virtual timing or quality limits.
- New network and write-recovery tests passed under the race detector against
  published v0.98.0, including changed IPs on real WebSocket reconnects, forged
  metadata fallback, exact full-duplex PCM and uninterrupted carrier attachment.
- Local Tier 2 / browser-network checks passed (38 checks), plus Twilio
  full-duplex continuity and benchmark gate tests. Live-carrier tests remain
  opt-in and were not run.
- Benchmark clock observer: 7 checks passed; frontend and benchmark typechecks
  and Go vet passed.
- Server forwarding: real HTTP and WebSocket proxy tests, production app/host
  routing, IPv4/IPv6, trusted-chain traversal, unsigned/forged assertion scrubbing,
  owner checks, bearer preservation and reconnects passed under race detection.
  Full Server suite passed 2,428 checks (63 optional skips), and build and vet
  passed against the released SDK. Local full-suite fixture
  dependencies (integrations and Core) use clean published checkouts. CI now
  checks out Core for its existing managed MCP startup test.

Original SDK and Server tracked diffs and untracked-file hashes were verified
unchanged after implementation. All prior Telephony changes are retained.

## Fresh browser/network results after recovery fix

The 13-profile network run passed its command gate: 11 scenarios passed and two
insufficient-bandwidth PCM profiles correctly reported degradation. Broadband,
Wi-Fi jitter, 512 kbit/s PCM and forced-relay Opus at 128/64 kbit/s had zero missing
tone markers in either direction. Outage/catch-up and reconnect scenarios passed
recovery checks; lost speech during the artificial outage is still counted.
Intentional microphone mute did not cause incoming playback loss.

| Scenario | Adviser → carrier p95 | Carrier → adviser p95 | Result |
|---|---:|---:|---|
| Broadband | 86 ms | 114 ms | Pass, zero missing markers |
| Wi-Fi jitter | 124 ms | 163 ms | Pass, zero missing markers |
| 512 kbit/s PCM each way | 121 ms | 142 ms | Pass, zero missing markers |
| Opus, forced-relay UDP, 128 kbit/s each way | 117 ms | 171 ms | Pass, zero missing markers |
| Opus, forced-relay UDP, 64 kbit/s each way | 224 ms | 270 ms | Pass, zero missing markers |
| Final 60-second baseline | 101 ms | 179 ms | Pass, zero missing markers, drops or underruns |

An earlier 60-second pair retained a failure: UI-pause passed, but baseline
playback p95 was 293 ms against its 250 ms target. Its 55 ms playback discard at
11:12:07 UTC coincided with 100% host CPU at 11:12:06–07 UTC. Independent render
clocks continued advancing, server writes stayed bounded, and neither internal
PCM writer recorded a write error. In the final baseline, sampled CPU peaked at
74%, with no drops or underruns. This establishes environment sensitivity, not
proof of which process caused the saturation, a guarantee against production
cuts, or a reason to discard the failed result.

The measurements have approximately ±20 ms clock uncertainty. Markers are
synthetic tones, not a speech/MOS assessment. Bit-exact steady PCM and unchanged
production DSP are covered by the functional tests. Full raw runs remain in
`/private/tmp/telephony-audio-stress-20261009`; the tracked
[audio stress results](audio-stress-results-20261009.json) retain outcomes,
including failures, directional scores, clock observations, queue counters,
host saturation timestamps and raw-result hashes.

## Deployment dependency

SDK v0.98.0 is published. Server must include signed forwarding and its trusted
proxy configuration before Telephony can record the browser's public exit behind
that Server. Updating Telephony alone on an old Server still works but records
its socket peer. Configured known exits enable `known_vpn_exit`; IP metadata by
itself does not detect arbitrary VPNs or establish bandwidth saturation.
