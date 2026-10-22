# Optional managed TURN for the softphone

Telephony can bind the TURN app (version 0.3.0 or newer) through its optional
**TURN relay** integration. It remains optional: the default policy uses the
existing `softphone_webrtc_ice_servers` configuration, including an empty
configuration with direct host candidates. Binding TURN alone does not enable it.
There are no TURN credential requests or renewal workers in the default mode.

This affects browser WebRTC media for advisers, passive listening and private
coaching. SIP signaling, carrier RTP and carrier WebSockets keep their current
paths. Select WebRTC or Auto in the softphone to use TURN. A saved forced-relay
policy rejects PCM/WebSocket attachments so a comparison cannot silently bypass
TURN through the fallback transport.

## Configuration and precedence

1. Bind the TURN app in Telephony's integrations.
2. Open Numbers → WebRTC relay / TURN settings and choose **Optional TURN app**
   as the project's credential source.
3. Keep the default **Direct preferred; relay available** policy for normal
   operation. Forced relay is available for controlled comparisons.
4. Optionally choose an enabled adviser in the administrator panel and save an
   override. **Use project defaults** removes it.

The effective policy is project default → adviser override → authorized session
test options. Identities include issuer application, installation, subject type,
subject ID and organization, and are scoped to the project. A supervisor's own
listener identity determines relay settings, rather than the call owner's
identity. Project administrators can manage adviser overrides. The project's
`allow_user_overrides` switch additionally permits advisers to edit their own
policy and use per-session test options. Disabling that switch prevents editing;
it does not delete previously saved overrides.

Settings are frozen for each attachment. Changes apply to new attachments, or
an explicit softphone reconnect; they do not change the carrier call. These
settings are also exposed to headless consumers:

```ts
await client.turnPolicy();                 // authenticated adviser's view
await client.turnPolicy({ policy: {
  source: "app", transport: "tls", tls_port: 8443,
  ice_transport_policy: "relay", ttl_seconds: 3600,
} });
// Operator-only counterpart supports project defaults or an adviser identity:
await client.configureTURNPolicy({ policy: {
  source: "app", transport: "all", ice_transport_policy: "all",
  ttl_seconds: 3600, allow_user_overrides: false,
} });
```

Backend HTTP routes are `/softphone/turn-policy` (GET/POST, own authorized user)
and `/numbers/turn-policy` (POST, administrator). Project and user identity come
from authenticated Apteva context. A browser cannot submit another user's
identity to the own-policy endpoint.

For one attachment, shared softphone/listener options accept:

```ts
{ mediaTransport: "webrtc", turnTransport: "tls",
  turnTLSPort: 8443, iceTransportPolicy: "relay" }
```

Omit these options to inherit the backend policy. Session overrides are excluded
from shared-browser local storage. Transport choices are `all`, `udp`, `tcp`
and `tls`; a TLS port can select an advertised TLS listener only. Unavailable
requested transports fail validation. Credentials never come from these options.

## Domain, ports and networking

The TURN app owns the hostname, listener ports, certificate and credential
secret. Telephony uses its advertised endpoints; it does not provision DNS or
let a browser redirect backend requests to an arbitrary TURN domain.
The existing static ICE configuration remains available for external providers.

TCP/TLS selection controls browser → TURN transport. TURN → Telephony still
requires working UDP to Telephony's public WebRTC media address and configured
UDP range. The infrastructure handover requires TURN and Telephony on different
public IPs. Confirm firewall rules and the actual selected ICE pair in staging.
For the October 2026 test deployment, configured listeners are UDP/TCP 3478 and
TLS 5349/8443; public 443 is unavailable. Those deployment details are not
hardcoded in Telephony.

## Authorization and renewal

After validating the exact call/media or listener session, Telephony invokes the
app-only `turn_credentials` tool using the project-scoped SDK
`CallAppResultContext`. It generates its session ID server-side and requests a
bounded TTL (default 3600 seconds, allowed 60–86400). Every renewal rechecks the
call, token, access and optional binding. Ended calls cannot obtain credentials.

Managed credentials are delivered only to the browser peers. The server retains
its existing STUN/public host candidates, avoiding a second TURN relay on the
server side. Responses are validated for TTL, expiry, endpoint count, URL scheme
and selected transport/port before use.

An expiry timer refreshes credentials early, with staggered scheduling. All
initial/renewal requests share four process-wide credential-service slots; tool
RPCs have a five-second context. There is no provider/database work in the
packet, encoding or playback loop. Renewal failures retry with a bounded backoff
(up to 30 seconds); duplicate browser refresh requests are debounced. Existing
media stays active while credential service requests retry.

A successful renewal updates `RTCPeerConnection` configuration and performs a
controlled ICE restart. The peer, media tracks, microphone mute state and audio
processing graph survive. Listening and coaching have separate peers;
negotiation waits for initial signaling to finish and pins coaching generation.
Canceled or replaced peers cannot apply late answers. A failed negotiation is
rolled back where supported and browser retries are bounded.

With policy `all`, an initial credential-service outage still permits direct
ICE setup. Forced relay fails closed with a safe, explicit error. This does not
guarantee that a working direct route exists, nor that an expired relay survives
an extended TURN outage.

## Diagnostics and verification

Selected candidate type, candidate protocol and **relayProtocol** are separate
fields: an RTP relay candidate can be UDP while browser → TURN uses TLS.
Existing packet-loss, jitter and RTT measurements remain available. Safe ICE
renewal events include revision, attempt ID, start/result, elapsed time and
credential time remaining. Adviser events use the bounded background diagnostic
queue; listeners expose events in diagnostics, and server renewal events are
retained in their bounded audit snapshot. No credentials, SDP or credential-bearing
URLs are included in these events.

Repeatable local verification:

```sh
GOWORK=off go test -run '^TestTURN' -count=1 -timeout 3m .
bun test frontend/tests/rtc-ice.test.ts
bun run typecheck
# Requires local Playwright Chromium; no Docker or external services:
GOWORK=off go test -tags integration -run '^TestTURNBrowserMediaCredentialRenewal$' -count=1 -timeout 3m .
```

Tests cover optional defaults, binding/policy permissions, identity/project
isolation, ended-call rejection, concurrent issuance limits, cancellation,
transport filtering, renewal, forced-relay failures, redaction and listening/
coaching generation handling. Real loopback TURN/UDP and TURN/TCP WebRTC sessions
exchange audio both ways and rotate credentials while preserving the attachment.
Real Chromium also runs the shared softphone and microphone worklet through
TURN/UDP and TURN/TCP and verifies credential rotation without replacing the
peer/sender or losing mute. This check runs in the existing browser CI job.
A separate TURN/TLS test requires a trusted fixture certificate, rejects an
untrusted certificate, and transfers media both ways. TLS is not bypassed.

These tests do not replace a staging browser-to-deployed-TURN check: trusted
public TLS, public address separation, firewall reachability and real client
networks must be verified before production enablement. No production or staging
configuration is changed by these tests.
