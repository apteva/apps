# DIDWW outbound SIP

Telephony places DIDWW outbound calls through a SIP dialog, not the DIDWW REST
`create_outbound_trunk` tool. The connection's API token continues to manage
numbers and trunks; the outbound SIP trunk has separate credentials.

Configure the DIDWW connection with:

- `sip_host`: a DIDWW outbound signaling hostname for the desired region (for
  example `fra.eu.out.didww.com`). It must end in `.out.didww.com`.
- `sip_username` and `sip_password`: credentials of an active DIDWW outbound
  trunk that permits the selected caller ID and source IP.
- `sip_transport`: `tls` (the only transport accepted by this adapter).
- `sip_port`: optional; defaults to 5061.
- `sip_media_encryption`: `srtp_sdes` by default; `disabled` is an explicit
  opt-out and is rejected if the Telephony gateway requires SRTP.

Enable the Telephony SIP gateway with a public TLS certificate, reachable SIP
listener, and reachable UDP RTP port range. Its `sip_transport` must be `tls`.
Set `sip_srtp` to `required` or `preferred` when using the default
`srtp_sdes` setting. The default SIP carrier allowlist includes DIDWW's
published signaling/RTP ranges (`46.19.208.0/21` and
`185.238.172.0/22`); a custom `sip_allowed_cidrs` override must include the
current DIDWW ranges as well. Keep both the provider trunk's allowed source IP
and your firewall rules aligned with the gateway's public signaling/RTP IPs.

Outbound calls use one G.711 PCMU audio leg with TLS signaling and, by
default, SDES-SRTP. A provider answer that changes the offered codec or
encryption is rejected. SIP Digest challenges use the trunk credentials. The
call is canceled on ring timeout, sent BYE on local hangup, and finalized on
carrier BYE. Hold and provider-cloud recording are not advertised for DIDWW.

The local tests use a mocked SIP peer, not a DIDWW account. Before production
use, make a staging call with an authorized French caller ID and verify two-way
audio, caller ID, busy/no-answer behavior, local and remote hangup, and a
cancel/answer race against the real trunk.
