# Direct SIP transport

Telephony can receive inbound calls without a provider Voice API media stream:

```text
carrier DID -> SIP signaling -> Telephony -> RTP/SRTP -> Core realtime bridge
```

The transport is selected per inbound route. Existing and newly created routes
default to `programmable_websocket`; direct SIP is opt-in.

## Current carrier support

| Carrier | Direct SIP setup | Media |
|---|---|---|
| Twilio | Telephony creates an Elastic SIP Trunk, origination URI, and number association | G.722 or G.711 PCMU/PCMA over RTP or SDES SRTP |
| Telnyx | Telephony creates an FQDN connection and assigns the number | G.722 or G.711 PCMU/PCMA over RTP or SDES SRTP |
| DIDWW | Telephony creates an inbound voice trunk and assigns the DID, retaining the previous trunk for restoration | G.711 provisioning; gateway also accepts offered G.722 over RTP/SRTP |

Twilio and Telnyx outbound calls default to their programmable voice APIs;
operators can select direct SIP for an account or a specific owned number.
DIDWW outbound calls use its configured SIP trunk.
Bandwidth uses the programmable inbound callback adapter with a manually
assigned Voice Application and dedicated Location; it does not use this direct
SIP gateway.

## Network requirements

The Telephony sidecar must receive carrier traffic directly. On a public host:

- Configure Apteva's normal public URL with a hostname pointing to the host.
- Expose TCP `5061` for SIP over TLS.
- Expose UDP `20000-20199` for RTP.
- Give the sidecar a stable public IPv4 address or correct one-to-one NAT.
- Use an Apteva-managed ingress certificate, or make the host's certificate
  available in `/var/lib/apteva/certs/<host>/` or the standard Let's Encrypt
  directory (`/etc/letsencrypt/live/<host>/`).
- Restrict the host firewall to the current signaling and media ranges
  published by the selected carrier.

A temporary HTTP tunnel is not suitable because SIP signaling and UDP media do
not travel through it. The ordinary Apteva public URL remains necessary for the
panel and for routes using programmable WebSockets.

The app runtime does not currently own the host firewall or container port
publication. Those are one-time host deployment settings; Telephony validates
its listener and reports preflight failures but cannot change host security
policy.

## Managed defaults

Direct SIP is activated lazily when a route selects it. No SIP-specific app
settings are required for a standard public Apteva host:

- Hostname comes from the platform `public_url`.
- Public IPv4 comes from that hostname's DNS A record.
- TLS certificates are discovered from Apteva's native ingress certificate
  cache, the shared Certs app directory, Let's Encrypt, and `/etc/apteva/certs`.
- Signaling defaults to TLS on TCP `5061`.
- Media defaults to SDES SRTP when the carrier supports it.
- RTP binds on UDP `20000-20199`.
- The capacity default is 100 concurrent sessions.
- Carrier source validation uses maintained Twilio and Telnyx signaling/media
  network presets.

App settings remain available as advanced overrides for unusual NAT, custom
certificate layouts, nonstandard ports, and temporarily overriding a carrier
CIDR list. Process environment variables with the `TELEPHONY_` prefix remain
available as a deployment fallback.

UDP or TCP signaling is rejected unless
`TELEPHONY_SIP_ALLOW_INSECURE_SIGNALING=true` is explicitly set.

## Route setup

In the Numbers panel, select **Direct SIP** for a routed Twilio, Telnyx, or DIDWW
number and apply it. Telephony runs the host/certificate/network preflight,
starts the listener if necessary, and configures the provider. The
project-scoped endpoint is:

```http
POST /api/apps/telephony/numbers/transport?project_id=<project>
Content-Type: application/json

{
  "route_id": "route-...",
  "inbound_transport": "sip_direct",
  "configure": true
}
```

Agents can perform the same operation with `telephony_routes_set_transport`.
Carrier configuration is applied by default. `configure: false` is an advanced
local-only change and does not update the carrier; it is blocked during active
calls to avoid a mismatch between local admission and carrier assignment.

Applied transport switches affect new ingress. Existing calls retain their
transport, routing snapshot and media settings. Switching back to
`programmable_websocket` restores the Telnyx programmable connection or detaches
the Twilio number association. SIP resources are retained in a durable retirement
journal and removed only after existing dialogs and calls finish. Failed saves
restore the previous carrier assignment; uncertain rollback errors require
reconciliation. Carrier commands cannot provide a fully atomic database/carrier
transaction.

Select the carrier account when creating a route with `carrier_connection_id`.
Only accounts bound to the installation are accepted. Different numbers can use
different supported carriers and inbound transports concurrently. Outbound
caller-ID permissions and runtime enablement remain independent of inbound
transport; outbound transport is configured separately for the selected account
or number. Provider configuration is never assumed healthy merely because
another account is healthy.

## Operational behavior

- Unknown numbers receive `404 No Route`.
- Ambiguous enabled routes are rejected instead of being routed to the wrong
  project or agent.
- Carrier signaling and SDP media addresses must match the configured CIDRs.
- Calls receive SIP `100 Trying` and `180 Ringing` before agent or immediate
  realtime answer.
- Audio uses a 60 ms startup jitter buffer, 20 ms RTP pacing, packet-level
  playback acknowledgements, adaptive caller-noise processing, and local
  barge-in as a fallback to provider turn detection.
- Direct SIP routes keep the INVITE ringing through the shared bounded adviser
  decision loop. A selected browser adviser or AI fallback answers the same SIP
  dialog, with one answer winner and actual carrier ACK before live media.
- Announcements, keypad menus and voicemail use the shared local media engine.
  It renders before answering, waits for ACK, sends paced RTP, then gathers
  digits or follows the next routing action. Caller cancellation stops pending
  synthesis; late completion cannot answer a canceled call.
- External PSTN/SIP destinations use the existing ring-leg adapters. Twilio and
  Telnyx external ring-leg adapters use their programmable outbound APIs; DIDWW
  uses its outbound SIP trunk.
  A switched route retains the provider context captured for the existing call.
- Recording is local because the provider Voice API is bypassed. The existing
  project recording policy selects mono/dual WAV, private Storage import,
  retention and deletion. Disk writes use a bounded nonblocking queue; omitted
  audio is counted. Pause/resume operates locally. Empty or failed voicemail
  recordings never mark the call as handled.
- Local hold music and RFC4733 DTMF use the existing paced RTP sender. Incoming
  RFC4733 digits and authenticated in-dialog SIP INFO are supported.

## Dialog and media recovery

Established calls accept re-INVITE and UPDATE refreshes with unchanged media.
Requests must match the dialog tags and advance its remote CSeq. Session timers
support both carrier-initiated and Telephony-initiated refreshes. An unsupported
media change receives `488` while the existing call remains active.
Remote hold/resume accepts sendonly, recvonly and inactive directions and
returns a complementary SDP answer. Endpoint, codec and key changes and native
REFER transfer remain unsupported; they are rejected without ending the existing
call. App-level external routing uses ring legs rather than native REFER.

Initial answers and re-INVITE answers have a fixed ACK deadline. Missing ACKs,
expired session timers, and failures connecting an answered call's audio bridge
terminate the SIP leg. A hangup interrupts an outstanding local refresh rather
than waiting for its response timeout. Shutdown drains signaling and media work
before releasing the app context, and call admission reserves capacity atomically.

SRTP answers echo the selected SDES crypto tag and enable a 128-packet replay
window, including sequence-number rollover. Codec name, rate, and channel count
are validated exactly. Unsupported SDES lifetime, MKI, and session parameters are
rejected instead of being silently ignored.

The receive jitter buffer starts after its 60 ms target or three playback ticks,
so shorter speech bursts drain too. It retains recent audio under congestion and
caps queued audio age at 120 ms. Caller audio uses the bounded WebSocket audio
queue. RTP timestamps follow the monotonic media clock across silence, and the
first packet of a new talkspurt is marked. Inbound loss, dropped audio, queue age,
and jitter estimates are included in the selected call's carrier diagnostics.

TLS handshakes load renewed certificate files without restarting the listener.
Invalid or partially written renewals retain the last valid certificate, but an
expired certificate is not served. The direct-SIP number status includes the
certificate expiry, readiness, and renewal error. RTP port allocation rotates
through the configured range to avoid repeatedly trying the same occupied ports.

## Rollback

Telephony stores only provider resource IDs and previous routing IDs, not
carrier credentials. Provider setup is rolled back when a later setup or
database step fails. Disabling a route or switching its transport restores the
previous Telnyx connection or removes the Twilio trunk association.

## Local media configuration

The Numbers panel has **SIP media** controls for each route and project defaults.
Route configuration overrides the whole project media configuration. **Use
project defaults** removes that override. Changes apply only to new calls.

The generic tool is `telephony_local_media_settings`; the project-scoped HTTP
endpoint is `POST /numbers/local-media`. Send no `config` to read the effective
settings, or provide:

```json
{
  "route_id": "route-...",
  "config": {
    "codecs": ["G722", "PCMU", "PCMA"],
    "speech_tool": "generate_speech",
    "speech_input": {"model": "your-provider-model"},
    "sample_rate": 24000,
    "prompts": {"Welcome": 123},
    "gather_timeout_seconds": 6,
    "voicemail_max_seconds": 120
  }
}
```

Omit `route_id` for project defaults. Use `{"route_id":"route-...",
"inherit":true}` to remove a route override. Agents can save only owned routes.
Unknown fields, invalid codecs/sample rates, foreign Storage assets, and
out-of-range limits are rejected. Keypad timeout is at most 60 seconds and
voicemail at most 600 seconds; zero means the default.

Bind an optional **speech** integration and select its tool, or map exact prompt
text to private project Storage WAV/MP3 file IDs. Tool input merges configured
fields with `text` and optional `voice`; the result must be the SDK binary
envelope (`_binary`, `base64`, `mimeType`). WAV/MP3 are decoded locally; raw
little-endian mono PCM16 requires `sample_rate`. Speech generation has a
30-second cancellable deadline and four shared slots. Total prompt audio is
bounded to 120 seconds. Text-bearing SIP flows fail validation without a renderer
or prerecorded mapping rather than silently skipping announcements.

G.722 runs at 16 kHz with the mandated 8 kHz RTP clock. Codec preferences are
snapshotted at ingress. DIDWW provisioning currently requests its documented
G.711 codec IDs; no undocumented G.722 ID is sent. Telnyx FQDN API documentation
forbids explicitly setting `encrypted_media` with TLS, so provisioning omits it
for TLS. Required SRTP mode still rejects plaintext SDP; carrier TLS/SRTP policy
must be verified before activation. This change does not prove real carrier
interoperability or change host firewall/port publication.

## Verification scope

Local UDP signaling/RTP tests cover prompt delivery before hangup, menus,
duplicate digits, caller cancellation, voicemail, recording, codec negotiation,
G.722 loss, hold/resume, outgoing DTMF, resource retirement, failed-save rollback,
and account isolation. Existing dialog, browser and provider regression suites
remain required. Mocked provisioning tests make no carrier requests. Real carrier
signaling, TLS/SRTP provisioning and network reachability still require a
controlled deployment verification; no staging or production instance is changed
by these local tests. Browser PCM/WebSocket and WebRTC transports are independent
of the number's inbound SIP/programmable selection.

## Outbound Telnyx and Twilio SIP

The Numbers panel exposes **Outbound transport** for each account and number.
Settings affect new human and AI calls, including the headless softphone API.
The browser can still use either PCM/WebSocket or WebRTC independently.
No carrier configuration is changed by saving these settings.

Prepare an existing **Telnyx SIP connection** (credential, IP or FQDN) with an
enabled outbound voice profile, or a **Twilio Elastic SIP Trunk** with termination
and credentials/IP ACL configured. A Twilio Programmable Voice application is
not an Elastic SIP Trunk. Telnyx Call Control application IDs are likewise not
SIP connection IDs. Ensure destination country permissions, spending/concurrency
limits, caller-ID permissions, firewall/NAT and media settings permit the call.

TLS signaling on port 5061 is required. SRTP/SDES is the default; explicit plain
RTP must also be permitted by the local gateway. Telnyx supports G.722, PCMU and
PCMA here; Twilio termination uses PCMU/PCMA. Setup verifies the authorized
carrier's existing resource through read-only API requests. New call admission
rechecks it and still requires number ownership, user permissions and runtime
outbound enablement. Provider failure blocks that selection without falling back
to an unrelated account. Saved verification time is not proof of a successful
real call or current reachability.

Operator endpoint: `POST /numbers/outbound-transport?project_id=<project>`.
The equivalent operator MCP tool is `telephony_outbound_transport`:

```json
{
  "carrier_connection_id": 10,
  "phone_number": "+33123456780",
  "transport": "sip_direct",
  "config": {
    "host": "sip.telnyx.com",
    "resource_id": "<existing-SIP-connection-ID>",
    "resource_type": "credential_connection",
    "auth_mode": "digest",
    "media_encryption": "srtp_sdes",
    "codecs": ["G722", "PCMU", "PCMA"]
  },
  "username": "<SIP-username>",
  "password": "<SIP-password>"
}
```

For Twilio use the trunk's `TK...` resource ID and
`<trunk>.pstn.twilio.com` (or the regional termination hostname), with
`codecs: ["PCMU", "PCMA"]`. Use `auth_mode: "ip"` without credentials for a
provider-configured IP ACL. Omit `phone_number` for the account default; a number
override takes priority. Select `programmable_websocket` to retain Voice API use
for one number. Send `inherit: true` to delete an override. Omit `transport` to
read safe configuration; SIP passwords/usernames are never returned.

Credentials are encrypted with an install-local key in
`<app data directory>/outbound-sip.key` (0600). Back up this key with the app's
persistent data. Losing it blocks digest admission until restored or credentials
are supplied again. Existing live dialogs keep their in-memory context and saved
transport; no setting change reconnects, stops or migrates them.

Native SIP recording uses Telephony's existing local recording policy and
Storage import. Disable Twilio trunk recording to avoid a second carrier
recording charge. Provider AMD is unsupported on direct SIP and rejected
explicitly; use `machine_detection: "off"` or retain the programmable transport.
TLS negotiation, authenticated re-INVITE refreshes, encrypted two-way audio,
recording pause/resume, codec negotiation, cancellation and BYE are covered by
local mock-carrier tests for both adapters. Real carrier/account interoperability
and actual pricing require controlled verification before switching a live number.

### TLS identity for dialog reconnection

If the original TLS socket is unavailable and an established dialog targets an
IP address, Telephony preserves that destination and verifies the carrier's DNS
certificate identity. Inbound defaults are `sip.twilio.com` for Twilio and
`sip.telnyx.com` for Telnyx (including the existing validated Telnyx regional
alternative). Outbound calls retain their configured termination hostname.
The `sip_tls_peer_names` map can override inbound identities. Destination network
restrictions, certificate trust/name verification and TLS 1.2 minimum remain
enabled. This also applies to in-dialog refresh requests, not only hangup.
