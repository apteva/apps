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

Twilio and Telnyx outbound calls continue to use their programmable voice
APIs. DIDWW outbound calls use its configured SIP trunk. Direct SIP inbound
transport targets the high-volume inbound cost path.
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
transport; Telnyx outbound uses its programmable application, not the FQDN
connection. Provider configuration is never assumed healthy merely because
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
  Telnyx use their programmable outbound APIs; DIDWW uses its outbound SIP trunk.
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
