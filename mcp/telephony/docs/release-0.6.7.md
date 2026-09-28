# Telephony 0.6.7

This release makes browser call offers explicit for application users.

- `/user/calls` and its `call_id` read include an advisory `answerable` boolean for each visible call. It uses the same current browser-offer, destination, and identity check as Answer. A supervisor can still see a call without an answerable offer. Direct browser destinations remain answerable without a ring-group offer.
- A user who still has access to a browser destination that was previously offered the call receives HTTP 409 with `{"code":"offer_expired","error":"call offer expired"}` after that offer expires or moves. A user who was never offered the destination still receives the existing denial. A caller hang-up remains HTTP 410 for an authorized historical offeree.
- The click-time check and atomic claim remain authoritative. The list field can become stale between refresh and click; it does not reserve a call or authorize a later answer.
- The Web SDK call type exposes `answerable`, and its incoming-call helper excludes visible calls marked `answerable: false`. `TelephonyClient.answer()` converts the HTTP response into `TelephonyOfferExpiredError` with a stable `code` and `status`. Older servers without the field retain the previous behavior.

Tests cover the current offeree, offer reassignment, stale list data, supervisor visibility, never-offered denial, carrier hang-up, and a concurrent answer/reassignment race. Production should upgrade from 0.6.5 through this release to retain the 0.6.6 public-IVR burst-guard correction.
