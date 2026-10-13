# Telephony 0.12.14 — managed runtime and SIP recovery (prepared locally)

- Implement protected describe/drain/activate hooks and passive startup for
  Apteva's managed execution controller, retaining exclusive activation and
  single-execution SQLite ownership.
- Renew/fence verified ownership in the background. Activation is armed before
  Server commits ownership; business admission opens afterward. Track slow
  initialization and async SIP, routing, AI preparation and recording work.
- Let existing calls, media and carrier callbacks finish during drain. Gate
  shared inbound routes at new-call creation so Telnyx hangup events still work.
- Consume backend browser/media/signaling/carrier endpoint bases; preserve
  legacy installation-scoped proxy URLs when no endpoint is advertised.
- Add scoped direct browser authorization, bounded grant storage and streaming
  revalidation, with no process credentials in the browser. Preserve business
  authorization, recording access, listening/coaching, call conflict errors and
  reconnecting bounded SSE. Delegated customer-auth clients retain the proxy.
- Store managed recording originals with relative references. Validate restored
  originals before readiness; rewrite recognized old paths only after ownership.
- Retain SDK v0.100.0, existing SIP/WebRTC/audio quality and startup fixes, and
  normal local execution. Add no infrastructure app dependencies.
- Fix Telnyx in-dialog TLS fallback when an IP Contact presents the regional
  `sip.telnyx.eu` certificate instead of `sip.telnyx.com`. Retry once only after
  a hostname mismatch matches the other explicitly allowed Telnyx identity;
  standard certificate trust, validity and hostname verification remain enabled.
  Preserve dialog routing and source-network checks, reuse verified regional
  sockets, and keep custom and other-provider identities exact. Covers inbound
  and outbound BYE/refresh requests; no audio framing or pacing changes.
- Keep valid SIP source-rate correction active when a large timing offset must
  be withheld. Recover a stable rate from a bounded lower envelope/recent
  window after delivery changes, without rebasing genuine stale audio.
- Give G.722 and G.711 the same playback reserve correction bound in audio
  duration, instead of applying the same decoded sample count at different
  rates. Retain the 160 ms capacity and 200 ms age limit.
- Add bounded timestamped clock-fit evidence, separate relative delivery age
  from local queue age, and measure consumer intervals. On Linux, consume
  kernel packet-arrival timestamps when supported and expose unread socket
  residence; other platforms retain normal read timestamps. Preserve early
  media arrival times through handoff. Diagnostic snapshots allocate their
  histories after releasing the audio lock.
- Rebuffer fresh, in-order speech after concealment instead of treating the
  synthetic playback cursor as proof the packet was already played. Preserve
  duplicate, reordered and stale-packet rejection and the carrier session.
- Fit startup/rebuffer reserve to the remaining age budget, avoiding an
  unattainable target that expires every new packet. Normally retain the
  minimum startup reserve; near the age limit, report explicitly when a single
  fresh frame must start without additional waiting. Advance decoded-partial
  source deadlines with consumed samples while retaining physical arrival age.
- Limit live reserve adaptation to the remaining relative source-age budget,
  leaving one output frame of headroom. Expose `effective_target_ms` separately
  from the nominal adaptive target; expiry and capacity limits remain unchanged.

See [managed-runtime.md](managed-runtime.md) for endpoint/configuration and
Server prerequisites. The current native snapshot limit is 512 MiB. Concrete
remote infrastructure and legacy rollback boot authorization still require
Server/backend work and staging validation. This version is prepared locally;
no instance is installed, moved or restarted by this work.

## Verification

The managed-runtime verification run passed **1,337 cases (including subtests)** with nine
explicit optional/live-test skips and no failures. All **269 frontend/UI tests**,
the 20 managed-runtime regression functions under the race detector, broader
SIP/recording/hub race checks, TypeScript checks, Go vet, the Linux amd64 build
and the real Chromium example passed. Headless and panel assets were rebuilt and
verified against host React imports.

The example simulator was missing the production call-status/readiness events
introduced in 0.12.13; it now emits those events. The live audio readiness policy
was retained. An earlier competing-build run failed an existing timing-sensitive
20 ms startup-pacer test; three isolated repetitions and later full runs passed.
No stale-audio limit or pacer algorithm was loosened.

Admission bookkeeping measured 33.07 ns/op for legacy execution and 80.44 ns/op
for managed execution on this Apple M1 Pro, with 16 B/op and one allocation. This
is an admission microbenchmark, not a whole-call CPU/capacity guarantee.

[Machine-readable verification](verification/managed-runtime-0.12.14.json)
records commands, skips, the earlier failure and integration limits. Real remote
placement and controlled carrier calls remain staging validation work.

### Regional Telnyx TLS correction

The pre-fix implementation reproduces the reported BYE failure against a local
TLS peer with a `sip.telnyx.eu` certificate. With the correction, the peer
confirms BYE with SIP `200 OK` and subsequent refreshes reuse the
verified connection. Eleven regression scenarios cover inbound/outbound,
global/regional identities, untrusted and unrelated certificates, custom
identities, other providers and destination network restrictions.

The follow-up full Go suite passed **1,349 cases (including subtests)**, with
the same nine optional/live skips and zero failures. Focused TLS, outbound
hangup, shutdown and remote-hangup tests passed under the race detector. Go vet
and the Linux amd64 build with CGO disabled also passed.
These are local tests; no carrier call or production modification was made.

### SIP clock and playback recovery

The final full Go suite passes **1,378 cases including subtests**, with nine
optional/live skips and zero failures. The existing 269 frontend/UI tests and
TypeScript check also passed; no frontend code changed in this follow-up.
Focused timing/concurrency and carrier lifecycle tests pass under the race
detector. Go vet, the Linux amd64 build and Linux arm64 test compilation pass.

The frozen 0.12.13 ingress/clock and corrected ingress were compared using the
same current decoder/framer. In a controlled twenty-minute G.722 replay with
250 ppm drift, jitter and an 80 ms delivery step, late rejection fell from
775.86 seconds to zero total discarded audio. Rate changes and alternating
delivery windows also pass for PCMU, PCMA and G.722. This is a synthetic timing
comparison, not a replay of the production call's unavailable packet trace.

In a separate 147-second simulation with a 160 ms timing step and one 420 ms
delivery hold, G.722 discard fell from 34.99975 seconds to 401.25 ms, with zero
continued tail rejection. The held backlog still expires. A deliberately
tighter 180 ms step plus jitter leaves insufficient headroom for some 20 ms
playout ticks: those packets still expire above 200 ms, even with a recovered
clock. No stale-age limit was raised to make that case appear lossless.

The final paced mixed-load run retains its strict zero-drop gate. Native
Opus/FEC with 32 calls passed with zero audio or recording drops, 0.729 CPU
cores and 0.639 ms work p99. Portable 32/64 and native 64 cases failed; measured
scheduling pauses reached 777 ms. Discard totals in that report aggregate all
streams. These results do not establish a clean 64-call capacity guarantee or
prove the cause of production incidents. Earlier failures are also retained.

Linux kernel timestamp runtime verification remains pending on native Linux/CI;
cross-compilation does not validate kernel behavior. No further Docker use,
production/staging change, carrier call or deployment was performed.

See [SIP verification evidence](verification/sip-recovery-0.12.14.json) for
the final suite results, commands, source hashes and retained limitations.
