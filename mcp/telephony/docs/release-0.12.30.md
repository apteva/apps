# Telephony 0.12.30

## Optional managed TURN

Telephony declares an optional TURN app integration and dependency (>=0.3.0).
Binding it does not enable it: existing ICE configuration remains the default.
Project administrators can select managed credentials in Numbers → WebRTC relay /
TURN settings, set project defaults and manage adviser overrides. An explicit
project permission additionally allows advisers to edit their own policy and use
session test overrides. Identities are scoped to authenticated project, issuer,
installation, subject and organization; session overrides are not saved in
shared-browser local storage.

Transport selection supports all, UDP, TCP and TLS, including a configured TLS
port. Forced relay is available for controlled comparisons and cannot silently
fall back to PCM/WebSocket after an ICE failure. TURN's hostname, listeners and
certificates are managed in the TURN app. No infrastructure settings are changed
by installing this release.

The shared softphone, passive listening and private coaching use the same policy
and renewal implementation. Listening/coaching resolves the supervisor's own
policy. Project and user identity are never taken from credential request input.

## Renewal and audio isolation

After validating a media session, Telephony requests temporary credentials through
the app-only TURN tool with the project-scoped, cancellable SDK API. Credentials
are reauthorized on renewal, including explicit ended-call and replaced-token
checks. Managed credentials belong to browser peers; Telephony does not allocate
a second managed server-side relay.

Credential service work runs outside RTP, encoding and playback. Four process-wide
slots bound concurrent requests; refresh timers are staggered, browser requests
are debounced, and failures retry with bounded backoff. Default installations
start no managed TURN renewal workers. Normal direct-or-relay policy can still
attempt direct ICE when initial credential issuance fails; forced relay fails
closed. An extended TURN outage can still interrupt a relay-dependent call.

Controlled ICE restart preserves the peer, sender, microphone mute and processing
graph. Listening and coaching negotiations remain separate, respect initial
signaling and coaching generation, and reject stale answers. Diagnostics retain
bounded safe revision/attempt/duration/expiry events and distinguish candidate
protocol from browser-to-TURN relay protocol. Credentials, SDP and credential-bearing
URLs are excluded from these events.

## Verification

Local checks passed before release preparation:

- Full Go suite: 1,783 passing test/subtest cases and 11 skips; additional static
  transport filtering and scheduling regressions pass separately.
- All 320 frontend/UI tests, type checking, focused race checks, vet and builds.
- Real loopback WebRTC/TURN UDP and TCP media in both directions with credential
  rotation on the same attachment.
- Real Chromium running the shared softphone and microphone worklet through TURN
  UDP/TCP, preserving peer, sender and mute across credential rotation.
- Separate TURN/TLS media exchange with certificate verification: the fixture's
  trusted certificate succeeds and an untrusted certificate is rejected.
- Policy authorization, project/identity isolation, supervisor policy selection,
  expired/ended sessions, malformed credentials, transport/port filtering,
  cancellation, concurrency bounds and forced-relay fallback protection.

The Chromium managed-renewal test is included in existing Telephony CI. Both
manifests, installer source references and regenerated frontend metadata use
0.12.30. SDK remains pinned to v0.104.0, verified by published tag ancestry.

These tests do not establish public TURN reachability or public browser/TLS
interoperability. Validate the deployed TURN hostname, trusted TLS, different
public media IPs, firewall rules and selected ICE pair in staging before enabling
production relaying. Existing SIP speech/load qualification limits remain; this
release does not certify unlimited capacity or fix missing network packets.

Publishing this release does not install, restart, configure or place calls on
staging or production. TURN remains optional and disabled by default.

See [managed TURN configuration](managed-turn.md).
