# Outbound browser → Telephony → SIP soak

This opt-in app test uses **real Chromium and real elapsed time**. It connects
microphones before carrier answer, then drives the production capture Worklet,
WebRTC/Opus transport, server decoder/release queue, authenticated human loopback,
outbound SIP session and SRTP pacer. The carrier is a local SIP/TLS/SRTP fixture;
no Telnyx, staging, production, CRM or external-number calls are made.

```sh
# From mcp/telephony; install the existing test dependencies once.
bun install --frozen-lockfile
bunx --no-install playwright install chromium

# Fast regression including both codecs and every fault profile.
bun run benchmark:outbound-sip --seconds 20 --calls 8

# Representative burst holds from the 21 October production observations.
# Run each separately; these model gap lengths, not full packet captures.
bun run benchmark:outbound-sip --seconds 20 --calls 8 --pause-ms 438 --recording true
bun run benchmark:outbound-sip --seconds 20 --calls 8 --pause-ms 441 --recording true
bun run benchmark:outbound-sip --seconds 20 --calls 8 --pause-ms 570 --recording true

# Long wall-clock run; 16 real browser calls, with local recording enabled.
bun run benchmark:outbound-sip --minutes 30 --calls 16 --recording true

# Direct Go test invocation, consistent with the other integration tests.
GOWORK=off TELEPHONY_OUTBOUND_SOAK_OUTPUT=/private/tmp/outbound-soak \
  TELEPHONY_OUTBOUND_SOAK_SECONDS=1800 TELEPHONY_OUTBOUND_SOAK_CALLS=16 \
  TELEPHONY_OUTBOUND_SOAK_RECORDING=1 \
  go test -tags integration -run '^TestOutboundSIPBrowserSoak$' \
    -count=1 -timeout 34m -v .
```

Without `TELEPHONY_OUTBOUND_SOAK_OUTPUT` the long test skips. The CLI accepts
2–64 calls and 8 seconds–4 hours; use at least eight calls to cover every
codec/profile combination. Output directories are new by default and retain
failed results. Local recording WAVs are finalized and removed during cleanup.
No Docker is used. Production runtime code contains no benchmark hooks.
The Telephony CI workflow runs the 20-second, eight-call variant with recording.
It uses the 570 ms burst hold; the CLI default remains 420 ms.
The full wall-clock soak is opt-in and uses the same Go integration test.

## Traffic and faults

Calls alternate PCMA/G.722 and cycle through four profiles:

- Healthy control with an unchanged UDP link.
- Continuous deterministic 0/8/16 ms ingress jitter.
- Repeated 180 ms delivery shifts lasting eight seconds every thirty seconds.
- Repeated ingress pauses (420 ms by default, configurable with `--pause-ms`
  or `TELEPHONY_OUTBOUND_SOAK_PAUSE_MS`), followed by bounded catch-up delivery.

Both the offer and answer ICE candidates are rewritten through the fixture.
This prevents peer-reflexive ICE from selecting a direct route around the
fault link. STUN, DTLS and RTCP retain their normal protocol handling. The
ingress and scheduler queues each hold at most 64 datagrams. Fault profiles must record their
configured incidents; a run cannot pass merely because no fault was injected.
Every call also performs an authenticated SIP session refresh before measurement;
long runs refresh every 45 seconds against a 90-second dialog interval. The
fixture retains the negotiated media endpoint and SRTP key across refreshes.

Synthetic microphone input combines natural-speech fixtures with timestamped
markers. The real capture processor and Chromium Opus encoder remain in use.
Marker IDs repeat every 128 seconds. Each marker records its own source render
time in the AudioWorklet, mapped through the browser output clock, before
encoding or transport. Source and sink observations are paired by marker ID.
This separates audio/render-clock drift from actual queue latency; source-clock
drift is not subtracted from measured downstream delays. Mapping has an
estimated 20 ms uncertainty.

## Acceptance and artifacts

`results.json` contains each call's:

- Source-to-carrier marker delay, per-minute median/p95/maximum and first-third
  versus last-third median growth.
- Longest RTP delivery interruption (packet-arrival gap minus one 20 ms frame),
  including the deliberately injected interruptions.
- Separate healthy-window interruption and unexpected silence against known
  source activity. This is a continuity proxy, not MOS/PESQ or proof that every
  spoken word is intact.
- Each injected incident and time until **fresh post-fault content** arrives.
- Capture and SIP queue/drop observations sampled once per second.
- Transport/codec errors and local recording drop counters.

The test fails on absent marker evidence, median growth over 40 ms, healthy p95
source-to-carrier delay over 300 ms, healthy interruption over 160 ms, recovery
of fresh content taking over 1.5 seconds, missed fault injections, transport or
codec errors, recording drops, or queue-bound violations. Fault windows and a
1.5-second recovery allowance are reported separately, never presented as
uninterrupted speech. Silence and speech pauses in the reference are excluded
from the unexpected-silence proxy.

`browser.jsonl` streams compact browser diagnostics, per-marker source oracles,
Chromium process CPU/RSS, host CPU/load, memory/paging counters and free memory to disk rather than retaining all snapshots in RAM.
`browser.log`, `test.log`, `test-errors.log` and `source.json` support diagnosis.
Source hashes are checked again at the end; editing executable test/app sources
during a soak invalidates its result.
`source-verification.json` retains the comparison and the underlying test exit code.
The test stops a broken run early if audio makes no progress for three seconds
or a transport/codec/clock error occurs; the failed run's evidence is retained.

This tests the local app/browser/SIP path and workstation load. It does not
reproduce Telnyx's media servers, an external carrier network, Apteva's remote
proxy, physical microphones/headsets, or a production host's capacity limit.

## Recorded local results, 21 October 2026

See `results-2026-10-21.json` for the retained summary and raw artifact locations.
The 438/441 ms eight-call short runs passed. The 570 ms short run recovered
audio but failed the median-growth gate on five calls. The eight-call run
completed 30 minutes with bounded median growth, fault recovery below 800 ms,
zero recording drops and 41 SIP refreshes per call, but failed the healthy
interruption gate. The 16-call run stopped after 1,027 seconds with interruptions
and a stalled stream. These are failed acceptance results, not a smoothness or
production-capacity certification. Healthy controls also recorded common timing
interruptions; their cause remains unresolved. The runtime recovery fix was local at measurement time, was not included in
0.12.26, and is packaged in 0.12.27.

## Resource policy and diagnostics contention

Enable the optional SDK governor/socket policy and eight diagnostic readers
while the existing recording and media acceptance gates remain unchanged:

```sh
TELEPHONY_RESOURCE_SOAK=1 TELEPHONY_RESOURCE_SOAK_RX=262144 \
  bun run benchmark:outbound-sip --seconds 60 --calls 8 --recording true
```

`TELEPHONY_RESOURCE_SOAK_RX` accepts 0 (unchanged OS buffer) through 4 MiB;
256 KiB, 512 KiB and 1 MiB are comparison candidates, not runtime defaults.
`resources.json` records successful/failed diagnostic work, governor counts,
and requested/effective socket settings. A test fails if no diagnostic work
happens or a diagnostic request fails. Only local testkit contexts are changed.

When developing the SDK simultaneously, set `TELEPHONY_TEST_GOWORK` to an
explicit temporary Go workspace that selects the modified SDK and this app.
Default runs keep `GOWORK=off` and use the pinned released SDK.
