# Telephony 0.14.1

Fixes direct SIP calls where neither side speaks first.

Telephony sent RTP only while its own AI or hold music had audio. When the
other end also waits silently, for example two Telephony direct-SIP legs bridged
to each other (an agent in one project calling an agent in another), no packet
was ever sent in either direction: carrier media relays had nothing to forward,
and both legs ended with `direct SIP RTP timed out` after 30 seconds.

- After 200 ms with nothing queued, the SIP pacer now streams encoded silence
  every 20 ms until the AI speaks, hold starts or the call ends. This keeps the
  far end's inactivity timer, NAT/firewall pinholes and carrier relays alive.
- Gaps shorter than 200 ms between AI audio chunks are unchanged.
- No RTP is sent while the remote side is `sendonly` or `inactive`.
- Keepalive packets are not counted as sent audio in call diagnostics or
  statistics.

## Authentication provider aliases

0.13.0 rejected every policy where two authentication providers named the same
issuer (`issuer_app` + `issuer_install_id`). That broke existing aliases such as
an adviser provider and a listening provider that both point at the same Auth
installation's `/me`: requests failed with `401 unknown authentication provider`
and such policies could not be saved.

- Providers that share an issuer are accepted as aliases when their identity
  `url` and `format` are exactly identical. A different URL (including path or
  query) or format for the same issuer is still rejected, at save time and at
  request time.
- Each alias keeps its own provider ID and action list; actions are never merged.
  User and group grants, destination bounds and supervisor requirements for
  listening and coaching are unchanged.
- Online session validation, revocation, project checks and the identity-endpoint
  network restrictions are unchanged. After identity validation the request is
  rechecked against the current policy, including for a conflicting provider.
- Existing policies are read as stored; no provider IDs, revisions or grants are
  rewritten.
