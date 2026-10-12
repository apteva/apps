# Production follow-up verification (0.12.12)

All traffic in this validation is local or CI fixture traffic. Production was
not accessed or modified while implementing or publishing these corrections.

## Regression coverage

- Real TLS socket: numeric SIP Contact and DNS-only certificate; correct identity,
  wrong identity, missing identity, dialog refresh and existing-socket reuse.
  Certificate name/trust checks remain active.
- Outbound early RTP: 3,000 packets representing 60 seconds of media, PCMU/PCMA/
  G.722/unknown payloads, cancellation, eight-packet fresh reserve and expiry.
- Real Telnyx/Twilio-shaped SIP peer: digest challenge, provisional PCMA with a
  different SRTP key/sequence timeline, final G.722/SRTP, bidirectional audio,
  recording, refresh and hangup. No live carrier involved.
- Rejected BYE is returned as an error with retained final evidence. Gateway
  shutdown sends BYE before closing the transport. Three repeated focused runs
  and race-instrumented runs cover these cases.
- Notification HTTP 503/retry keeps the same event identity; canceled persistence
  retains pending evidence. Audio collection skips contention without waiting.
  Timed-out telemetry transactions retry a smaller batch without losing records.
- Browser WebRTC ingress: bounded history, lock contention, padding, DTX, loss,
  actual concealment and processing delay. No inference of spoken content loss.
- Authenticated/unauthenticated browser handshakes, proxy identity, Core bridge
  correlation and exclusion of media tokens.
- Browser processing metric: known 12 ms per-sample delay, variable packetization,
  absent counters, zero denominator, resets and SSRC replacement.

## Browser evidence delivery comparison

Deterministic 120-second production-sized replay, unchanged 640 B/s control
budget. Times are collection age, not synchronized network latency.

| Input | Released sender max age | New sender max age | Complete samples after |
|---|---:|---:|---:|
| Periodic every 5 s | 52.75 s | 8.00 s | 24 |
| Incident + preceding context every 5 s | 53.50 s | 2.25 s | 24 incidents |

The second case intentionally omits redundant preceding context on the wire
(25 samples counted skipped), preserving context in local history. Detailed
native counters are sent less often. Blocked reports expire after 12 seconds;
reconnect starts with fresh evidence. Diagnostics never wait for shutdown.

## Packet-loop cost

Apple M1 Pro, local Go benchmark, three runs of healthy WebRTC ingress evidence:
38.87–41.70 ns/packet, **0 B and 0 allocations/packet**. This measures the added
observer, not an entire call. Persistence/serialization runs outside packet loops.

## Validation commands

From `mcp/telephony`, with `GOWORK=off`:

```sh
go test -json ./...
go vet ./...
bun test --timeout 15000 frontend/tests/*.test.ts ui/*.test.ts
bun run typecheck
bunx --no-install playwright test -c frontend/outbound-setup.playwright.config.ts
bunx --no-install playwright test -c frontend/recording-playback.playwright.config.ts
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build .
go test -run '^$' -bench '^BenchmarkRTCIngressEvidenceHealthy$' -benchmem -count=3 .
TELEPHONY_SIP_MIXED_LOAD=1 TELEPHONY_SIP_LOAD_RESULTS=/tmp/telephony-load.json GOMAXPROCS=4 go test -run '^TestSIPPlaybackClockPacedLoad$' -count=1 -timeout 5m -v .
```

CI additionally runs focused races, long SIP recording import/playback, and
native Opus on Linux/macOS amd64/arm64 (including Linux musl).

## Failed checks retained

The first full run exposed a manifest synchronization omission and a preexisting
softphone fixture sending before attachment. After correction, a subsequent full
run passed 1,295 tests/subtests but found the same missing `ready` synchronization
in the browser reconnect fixture. That fixture now waits for the actual protocol
event; 20 focused repetitions pass. An initial race run timed out persisting a
large telemetry transaction; the smaller-batch retry is covered explicitly.

An initial mixed-load run generated one notification **per call every 100 ms**.
32/native and 64/portable had zero discard, but 32/portable discarded 760 ms in
aggregate and 64/native experienced a 6.4 s scheduling pause with 426,722.875 ms
aggregate discard across calls. The machine had other active processes; their
causal contribution is unproven. This failure is retained, not treated as a pass.
The first artifact's scope label incorrectly excluded recording/database even
though its `mixed_recording_database_notifications` flag was true; the label is
corrected in the harness.

The revised representative mixed workload uses one notification per call per
second, plus call-list queries and outbox flushing every 100 ms. It records
process GC pause totals and a Go scheduling heartbeat. It is a different workload,
not a like-for-like improvement claim. Media remains bounded when host scheduling
fails; no finite queue can make several-second starvation inaudible.
