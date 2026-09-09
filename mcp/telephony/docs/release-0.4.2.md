# Telephony 0.4.2

Fix a Twilio audio WebSocket handshake race introduced in Telephony 0.2.0. A
`stream-started` callback arriving before the socket previously acquired the
media claim indirectly, causing the first real socket to receive HTTP 409
`media bridge already active`. Twilio reports a declined handshake as error 31920.

- Store authenticated Twilio stream notifications separately from bridge status
  in `telephony_twilio_streams`, keyed by call and provider stream ID. These are
  provider observations, not proof of connected audio.
- Notifications cannot acquire/release socket ownership, mark media connected,
  or alter call deadlines. Duplicate and out-of-order notifications cannot revive
  a stopped/failed provider stream; notifications after terminal calls are ignored.
- Keep the exclusive media claim until the owning handler releases it, including
  during error and close cleanup. Shared media-status reporting no longer changes
  ownership; this also protects the other carrier bridge handlers.
- Log Twilio pre-upgrade rejection reasons and HTTP status codes separately from
  provider notifications and the existing actual `bridge up` event. New logs omit
  callback URLs, credentials, and provider error text.

The additive database migration runs automatically on upgrade. Existing generic
application-user softphones and the v0.77.1 Go SDK authentication fix are retained.
No server, Web SDK, or CRM UI change is required.

Validation: regressions first reproduced the old callback-first claim failure and
late-callback state corruption. The Go short suite and vet pass. Signed callback
checks cover both arrival orders, concurrent socket claims and callbacks,
duplicates, reconnect after release, and every terminal call status. Repeated
race-detector checks include a real local WebSocket bridge carrying bidirectional
audio after the callback, with duplicate handshakes correctly rejected.

This release addresses audio transport ownership. It does not claim to resolve
answer-preparation timing, low caller speech levels, or background-model fallback.
Production recovery still requires upgrading the installation and observing live
calls; the historical failed handshake responses were not captured.
