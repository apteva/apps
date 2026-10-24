# Telephony 0.12.39

## Twilio SIP dialog TLS identity

- Supply `sip.twilio.com` as the default certificate identity for inbound
  Twilio dialog reconnections to IP Contacts. BYE and in-dialog refresh requests
  keep the original destination and routing while TLS verifies the DNS identity.
- Preserve certificate trust/name checks, TLS 1.2 minimum, destination network
  restrictions, existing socket reuse, custom operator identities, and outbound
  trunk-specific identities. Telnyx regional handling remains unchanged.
- Add local TLS regressions covering successful BYE/refresh and connection reuse,
  untrusted/lookalike certificates, blocked destinations, custom identity
  mismatches, and outbound wildcard/wrong-trunk certificates.

The regression reproduced the original IP-SAN error on 0.12.38 and passes with
this fix. A read-only TLS handshake against the affected Twilio peer also
verified its chain and `sip.twilio.com` identity; no SIP request was sent.

Uses published app-sdk v0.107.0. Both manifests, the source tag and the generated
frontend version identify 0.12.39. Publishing does not install, restart or
configure any instance. A carrier call after installing the release remains
necessary to verify the full deployed hangup path.
