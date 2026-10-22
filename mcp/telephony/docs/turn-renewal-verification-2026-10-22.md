# TURN renewal continuity — local verification, 22 October 2026

## Confirmed defect and correction

The original controller restarted every peer whenever TURN credentials refreshed,
including healthy direct connections. Pion ICE v4.4.4 closed the nominated media
sockets immediately on restart. Both browser and server also reported completion
after signaling, without checking resumed RTP.

Telephony now updates credentials without restarting a selected healthy direct
path under policy `all`. Relay renewal retains the existing nominated path until
the new path is selected. A pinned, app-local ICE compatibility patch is required;
the inspected upstream v4.4.8 still has the destructive restart behavior. See
`third_party/pion-ice/TELEPHONY-PATCH.md` for exact files and maintenance bounds.

Browser and server now distinguish `restart_signaled` from `restart_completed`.
Completion needs a new selected pair and progress in the previously active RTP
directions. Existing packet counters provide server evidence, without another
per-packet stats interceptor. Main-call events persist in
`server.webrtc.ice_events`; listener/coaching peers retain their generation rules.

## Before versus after

The same local regression harness deliberately delays browser application of each
renewed answer by **400 ms**. The old implementation was extracted unchanged from
commit `2eba0145`, with only the new test fixture overlaid (the new server-event
assertion was omitted because that telemetry field did not exist). No acceptance
threshold was weakened.

| Local forced-relay test, two renewals | Browser receive loss increase | Remote receiver loss increase | Largest receive-observation gap |
| --- | --- | --- | --- |
| Original controller / original ICE | **51 packets** | **51 packets** | **703.5 ms** |
| Corrected implementation, short regression | **0** | **0** | about 108 ms |

The old regression failed its continuity assertion; the corrected regression
passed, including independent server-side evidence for both completed renewals.
These are local reproductions, not new measurements from staging/production.

### Extended real Chromium runs

Chromium 153.0.8010.12, macOS arm64; each case runs 100 seconds of continuous
bidirectional media, renews credentials twice, preserves microphone mute/sender
identity, and requires remote RTCP loss evidence. Relay cases each apply two
400 ms answer delays. The selected path is checked so TURN cannot be bypassed.

| Path | Browser loss increase | Remote loss increase | Largest receive-observation gap | ICE answer switches |
| --- | --- | --- | --- | --- |
| Direct, TURN configured | 0 | 0 | 109.7 ms | **0** |
| TURN/UDP | 0 | 0 | 111.7 ms | 2 |
| TURN/TCP | 0 | 0 | 109.7 ms | 2 |

All three cases passed, including server confirmation of resumed RTP for relay
renewals. Total test wall time was 305.933 seconds. Packet loss is reported in
packets, not converted into missing speech durations. The receive-observation
gap is sampled through browser stats at 50 ms; it is not an exact packet-capture
inter-arrival measurement. Browser concealment totaled approximately 17.8–17.9 ms
in each long case despite zero reported loss, so this result is not a claim of
zero browser replacement audio or perfect end-to-end listening quality.

Raw evidence:

- Original regression: `/private/tmp/turn-browser-original-reproduction.txt`
- Corrected short runs: `/private/tmp/turn-browser-continuity-fixed.txt`
- Server RTP evidence: `/private/tmp/turn-browser-server-evidence-fixed.txt`
- Extended runs: `/private/tmp/turn-browser-continuity-100s.txt`

## Performance and bounds

- No second peer, DTLS/SRTP session, encoder or audio queue is created.
- Healthy connected sends retain their normal branch and original hot-field
  layout. Only the missing-selected-pair path loads the fallback pointer.
- One previous candidate generation is retained. New nomination switches sends
  immediately, then old receive sockets drain for 250 ms before cleanup.
- Negotiation failure/close and superseded cleanup are bounded; existing ICE
  failure/setup deadlines still apply. A genuinely failed network is not hidden.
- Media progress polling runs only during renewal, at 100 ms for at most ten
  seconds. Credential issuance remains outside audio goroutines with its existing
  concurrency, timeout and debounce bounds. Detailed events remain capped at 50.

The upstream synchronous UDP write/read microbenchmark measured:

| Code | Time/op | Allocation |
| --- | --- | --- |
| Original v4.4.4 | 21.3–22.1 µs | 0 B, 0 allocations |
| Final compatibility patch | 21.9–23.2 µs | 0 B, 0 allocations |

The initial prototype shifted hot fields and had a second healthy-path branch;
that was removed before final validation. The small remaining timing difference
is not a production CPU/capacity certification. The long-run Go fixture consumed
8.39% of one core for direct and 10.34% for UDP, but those figures include the
local carrier/TURN fixtures and exclude Chromium, so they must not be treated
as isolated Telephony per-call cost.

Benchmark artifacts: `/private/tmp/turn-ice-original-bench.txt` and
`/private/tmp/turn-ice-patched-layout-bench.txt`.

## Regression checks

- Telephony full uncached suite: **1,824 passed, 11 skipped**, 374.529 seconds.
- Frontend suites: **192 + 131 passed**; TypeScript check passed.
- Complete local ICE suite and subpackages: passed (main package 132.665 seconds).
- New ICE race tests: passed. They preserve bidirectional packets while
  replacement signaling is withheld, repeat a pending restart, verify cleanup,
  and reject forged consent and unknown source addresses.
- Telephony TURN race suite: passed (51.702 seconds).
- Native build, vet, panel import verification and Linux amd64 CGO-disabled
  cross-build: passed. Native Linux execution was not performed here.
- Refreshed embedded headless client and panel assets are included. The version
  remains 0.12.30 until a release is explicitly prepared.

The protocol suite ran before a final layout-only hot-path adjustment; the new
race tests, transport benchmark and 100-second Chromium tests ran against that
final layout. The final targeted app/asset checks and cross-build are retained in
`/private/tmp/turn-final-gates.txt` and the tool transcript.

## Reproduce

From `mcp/telephony`:

```sh
GOWORK=off go test ./...
GOWORK=off go test -race -run '^TestTURN' -count=1 .
bun run test:frontend
bun run typecheck
GOWORK=off TELEPHONY_TURN_CONTINUITY_SECONDS=100 go test -tags integration -run '^TestTURNBrowserMediaCredentialRenewal$' -count=1 -timeout 6m -v .
```

Tests use local fixtures only. Production and staging configuration, processes,
webhooks and calls were not accessed or changed. No release was published. The
tests do not reproduce an external carrier/TURN network or establish a production
host's concurrent-call capacity. Existing SIP quality limits remain separate.
