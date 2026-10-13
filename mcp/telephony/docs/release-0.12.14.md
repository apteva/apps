# Telephony 0.12.14 — managed runtime support (prepared locally)

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
