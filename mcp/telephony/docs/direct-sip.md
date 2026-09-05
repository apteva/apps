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
| Twilio | Telephony creates an Elastic SIP Trunk, origination URI, and number association | G.711 PCMU/PCMA over RTP or SDES SRTP |
| Telnyx | Telephony creates an FQDN connection and assigns the number | G.711 PCMU/PCMA over RTP or SDES SRTP |

Outbound calls continue to use the provider's programmable voice API. Direct
SIP currently targets the high-volume inbound cost path.

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

In the Numbers panel, select **Direct SIP** for a routed Twilio or Telnyx
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

Agents can perform the same operation with
`telephony_routes_set_transport`, followed by
`telephony_routes_configure_carrier`.

Switching back to `programmable_websocket` restores or removes the direct SIP
provider resources before configuring the normal provider application.
Transport changes are blocked while the route has an active call.

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
- Direct SIP routes do not currently record calls. Telephony forces recording
  off for this transport because provider-cloud call control is bypassed.

## Dialog and media recovery

Established calls accept re-INVITE and UPDATE refreshes with unchanged media.
Requests must match the dialog tags and advance its remote CSeq. Session timers
support both carrier-initiated and Telephony-initiated refreshes. An unsupported
media change receives `488` while the existing call remains active. Native
hold, transfer, codec changes, and changing media endpoints are not implemented.

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
