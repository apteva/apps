# Telephony 0.6.2

This release adds project-scoped ownership metadata to explicit call
reconciliation without changing call placement, routing, or lifecycle events.

- `telephony_call_get` now returns `peer_kind` and `routing_flow_id`.
- When a human softphone call has an owner, `telephony_call_get` also returns
  its verified `owner_identity`. This is the current owner and can change after
  a supervisor takeover; it is not an immutable dial-originator field.
- Ownership remains project-scoped and is not added to lifecycle broadcasts or
  bulk call listings. Missing or malformed optional ownership metadata is
  omitted so existing call lookups retain their previous behavior.
