# Telephony 0.9.0

Adds provider-neutral passive live listening at the server media bridge, with
no extra carrier stream or call leg. Human and AI audio is copied through
independent bounded listener queues; listen sessions never claim calls, replace
operator media credentials, reserve adviser capacity, or send microphone audio.

Application-user listening requires supervisor/resource access, an explicit
`listen: true` policy grant, and `call.listen` in the gateway/provider scope.
Credentials are hashed, leased, renewable, revocable and independently audited.
Call responses expose `listen_supported`, `listenable`, and an unavailable reason.

The frontend SDK adds `createCallListener()`, receive-only synchronized playback,
volume controls, diagnostics and bounded reconnect. The bundled Calls panel adds
Listen and Stop listening. Verified same-origin AudioWorklet assets work with
strict Content Security Policy; microphone access is never requested.

This cumulative release retains all Telephony changes through 0.8.4, including
DIDWW compliance/number management, SIP certificate renewal, multi-carrier
outbound selection and the combined Bun worklet test fix.

See [live-listening.md](live-listening.md) for coverage, permissions, APIs and
limits. Calls whose media bypasses Telephony cannot be monitored by this tap.

No staging, production, carrier configuration, number assignment or live call
was changed by the release process.

## Verification

- Full standalone Go suite, Go vet and build.
- Combined frontend/UI suite: 111 tests, zero failures; TypeScript checks and
  rebuilt frontend/Calls panel assets.
- Race detector on listening and real carrier bridge continuity tests.
- Existing Twilio/Telnyx/SignalWire/Plivo quality and pacing assertions run with
  both a healthy listener and a stalled listener. SIP pacing also verifies the
  observer receives the successfully transmitted packet.
- Local Chromium against a compiled Telephony sidecar: both directions play
  under strict CSP, microphone requests throw, stopping the listener preserves
  the original adviser connection. Panel and genuine Auth-user flows also pass.
- Local compiled-sidecar routing, ring-group, human and AI-handoff regressions.
- Microbenchmarks: no-listener tap has zero allocations; frames are copied once
  regardless of listener count. These are local measurements, not a guarantee
  for live carrier/network quality.
