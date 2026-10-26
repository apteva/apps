# Telephony 0.12.56

Adds generic softphone delivery evidence and accurate call-watcher error
classification. HTTP/network/timeout errors, invalid responses and exceptions
from an embedding app's callback are reported separately, including async
rejections. Callback failures retain the original cause without inheriting its
HTTP-like properties. Polling, receipt acknowledgements and audio continue.

Safe request codes/IDs, browser tab/session correlation, stream reconnects,
heartbeats and successful list reads support diagnosis. Explicit UI display,
ringtone and answer stages remain separate from receipt ACKs. The native panel
instruments its own display and WebAudio rendering; embedding apps can supply
the same generic events. Browser rendering does not prove physical audibility.
The call inspector adds a Pickup view for retained evidence.

Authenticated, bounded, idempotent reports use seven-day event retention and
24-hour watch snapshots, indexed ingestion cleanup, coalescing and no new
periodic worker. Raw error messages, stacks and credentials are not retained.
No Partenaires-specific logic or new permission is required.

Validation: full Go suite, focused race tests, go vet, 420 frontend/UI tests,
six real Chromium inspector tests, real-browser zero/300 ms auto-answer and
managed direct media renewal, typecheck and regenerated panels/frontend.
Release/source-ref consistency and Linux amd64 build are checked for this tag.
See softphone-delivery-telemetry.md and the verification JSON for the contract
and detailed test evidence.
