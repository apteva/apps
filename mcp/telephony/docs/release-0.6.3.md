# Telephony 0.6.3

This release makes a late softphone Answer request report the actual terminal
call state without weakening destination access controls.

- Answer returns HTTP 410 with `call has ended` when the carrier ended an
  inbound call immediately before the request acquired the call lock.
- A settled offer is visible only to users who are currently authorized for
  its historically offered browser destination, or to the call's owner.
- Because ring offers are destination-scoped, all currently authorized members
  of the offered destination receive the same terminal result. Users authorized
  only for another destination continue to receive HTTP 403.
- Active-call claiming, supervisor takeover, routing, and carrier behavior are
  unchanged.
