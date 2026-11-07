# Telephony 0.11.8

Cumulative release retaining Telephony 0.11.7 and both subsequent local commits:
immediate confirmed carrier-stream recovery and isolated media echo diagnostics.
Publication does not install, restart or configure staging or production.

## Immediate first carrier-stream recovery

- A confirmed carrier socket/provider failure or unexpected handler completion
  immediately schedules the first existing bounded recovery attempt. Mounted
  project context enables direct background dispatch without waiting for a
  two-second delay and another worker tick.
- Events and the fallback worker share per-call admission, preventing duplicate
  restart commands. Later attempts retain backoff, a three-attempt ceiling and
  the configured recovery deadline. Failed replacements inherit that budget.
- Healthy streams, stale generations, ended calls and process shutdown cannot
  dispatch a new recovery. Carrier network requests remain outside ownership
  locks, so a replacement socket or caller hangup can proceed during an API call.
- This changes dispatch after a confirmed failure. Silent sockets still use the
  existing 20-second protocol liveness budget checked every five seconds. The
  one-second local dispatch assertion is not a restored-audio latency guarantee.
- Existing five-second command timeout remains; no TeXML switch, carrier
  integration JSON edit, API hedging or new command-replay policy was introduced.

## Installation-scoped diagnostic media echo

- `/media/diagnostic/echo` reuses Telephony's carrier WebSocket upgrader, reader,
  writer pump and bounded close handling. It echoes Telnyx-shaped continuous
  JSON media and supports probes matching L16 and PCMU payloads.
- Operator-only `/media-diagnostics` issues a single-use token with a 60-second
  connection window and a 1–300 second stream lifetime. Tokens travel in a
  dedicated handshake header; they never appear in probe URLs or reports.
- Operator-only project-scoped readback exposes bounded UTC receive/send events,
  source sequence/timestamps, gap/write maxima, socket error classes, actual
  peer close codes, local close reasons and writer cleanup observations.
- Two active probes and sixteen reports per process bound diagnostic work.
  Reports are temporary and in memory. Mount/shutdown invalidates prior admission
  and prevents late upgrades from becoming orphaned streams. No call, routing
  offer, media claim, AI session, CRM record or carrier command is created.
- A Bun runner sends 20 ms synthetic frames concurrently through Telephony and
  an independent unchanged-text echo. It records RTT, timer delays, receive
  gaps, backpressure and closures without sending the token to the control.
- This probes Telephony's transport/process path. It does not reproduce Telnyx's
  own network, codec conversion, carrier pacing or complete live media pipeline.

## Verification and deployment limits

Focused recovery and diagnostic race checks, actual local socket replacement
and duplex-audio checks, token/authorization/capacity tests, and paired local
Bun probes passed before release preparation. Six-second L16 and PCMU probes
crossed the server ping interval and returned every transmitted payload unchanged.
Tests distinguish peer closure, EOF, policy rejection, local shutdown and a
forced socket-write timeout, and verify probes create no call or carrier claims.

The release gate uses `GOWORK=off` and the latest published SDK v0.99.0, preserving
all earlier source and SDK fixes. See `media-diagnostic-echo.md` and
`carrier-media-recovery.md` for operation, evidence and limitations.

No staging/production installation or live carrier call was performed. Real
staging ingress/header forwarding and live Telnyx restoration timing remain
unverified. Installation replaces the app sidecar; rollout is a separate action
and requires active calls to drain. PCM/WebSocket, WebRTC codec/FEC choices and
audio algorithms are unchanged in this release.

### Final release gate

After bumping both manifests and frontend metadata, the complete Go suite passed
**1,046 tests/subtests (five opt-in skips)** and frontend/audio suites passed
**241 tests**, with zero failures. Go vet, client/benchmark/probe TypeScript checks,
Telephony-only headless/panel builds, native build, and Linux amd64/arm64 builds
with `CGO_ENABLED=0` passed. The headless client hash and panel artifacts match
0.11.7; only frontend version metadata changed. Final diagnostic tests passed
three repetitions under the race detector before release preparation.

Release evidence: `/private/tmp/telephony-0118-release-go.jsonl` and
`/private/tmp/telephony-0118-release-frontend.log`. Local paired-probe and
recovery verification details remain in the linked operation documents. Opt-in
live-carrier tests were not run, and no production performance claim is made.
