# Telephony 0.12.31

## TURN renewal without interrupting the current media path

Healthy selected direct connections update credentials without an ICE restart.
Relayed connections keep their nominated media path available while the new path
connects. The release includes a pinned app-local Pion ICE compatibility patch;
one previous candidate generation is retained, then drained and closed after the
replacement is selected. No second encoder or DTLS/SRTP pipeline is created.

Browser and server distinguish signaling from completed recovery. Completion
requires a new selected pair and RTP progress in the previously active directions.
Bounded diagnostics retain the outcome; credentials and SDP are excluded. The
shared implementation also covers passive listening and private coaching.
TURN remains optional and disabled by default.

## SIP audio controls and live occupancy

Adaptive SIP admission uses local packet residence to protect against genuinely
stale audio, while retaining relative RTP clock evidence. Recovery preserves
decoder state, includes bounded G.722 decoder warming after overflow, and avoids
destroying queued audio solely because the estimated source phase changes.
Kernel clock-step guards keep arrival timing consistent.

Scoped audio profiles support project, direction, route, outbound account and
number settings, with temporary test overrides. Profiles are pinned for each
call; new-call comparisons do not require an app redeploy. Optional bounded packet
timing capture and runtime evidence assist investigation. See
[SIP audio profiles](sip-audio-profiles.md).

The read-only `telephony_live_occupancy` tool returns a consistent, complete,
project-scoped snapshot of active human ownership using one indexed query. It
does not load recordings, carrier inventory or full call diagnostics. Full
identity tuples and existing authorization rules are preserved. Routing must
retain reservations and atomic capacity checks.

## Verification and limits

- Full uncached Go suite: 1,824 passing test/subtest cases, 11 skips.
- Frontend/UI: 323 tests passed; type checking, builds, vet and focused races passed.
- Complete bundled ICE suite passed, including socket cleanup and forged-consent
  rejection; CI now runs that nested module and renewal race tests explicitly.
- Three real Chromium runs, 100 seconds each: direct, TURN/UDP and TURN/TCP.
  Each included two credential renewals; both relay cases delayed the answer by
  400 ms. All reported zero additional packet loss at both receiving ends. The
  matching old-code regression lost 51 packets in each direction. These local
  tests do not prove public network performance or zero audible impairment.
- Healthy ICE transport microbenchmarks retain zero allocations per operation.
  Progress polling runs only during renewal, with bounded time and event history.
- SIP tests cover codecs, speech/first words, stale protection, jitter, hold,
  recovery, long clocks and concurrent simulations. The severe captured G.722
  replay improves discard from 6.36 seconds to 1.24 seconds but still fails the
  smooth-speech criterion and does not preserve the opening. One short concurrent
  browser/SIP soak failed delay-growth gates; its matched repeat passed. Both
  results remain documented. This release does not certify production capacity
  or establish that every reported audio defect is resolved.

Detailed evidence: [TURN renewal verification](turn-renewal-verification-2026-10-22.md)
and [SIP verification](sip-audio-profiles-verification.md).

Both manifests, installer source references and generated frontend metadata use
0.12.31. SDK stays pinned to v0.104.0 (verified by published tag ancestry);
minimum Server remains 0.87.4. Publishing does not install or change staging or
production, carrier settings, TURN settings or calls.
