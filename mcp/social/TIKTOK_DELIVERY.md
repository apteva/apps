# TikTok video delivery diagnostics

Source verification, attempted upload payload and acknowledged upload are separate.
`verified_source_bytes` is populated only after complete source/container/checksum
validation. `attempted_upload_bytes` counts payload bytes submitted to HTTP
requests, not proof of network transfer. `acknowledged_upload_bytes` advances only
after 206 for intermediate chunks or 201 for the final chunk, with any returned
Content-Range/Range checked against the submitted bytes and total. Supported
inclusive endpoints and exclusive end offsets are normalized, including TikTok's
observed final `bytes 0-N/N` response. Under-acknowledgements, incompatible totals,
conflicting headers and unexpected HTTP statuses remain errors. The legacy
`transferred_bytes` alias now means acknowledged bytes for new TikTok attempts.
Legacy unverified transfer counters are exposed separately; acknowledgement is
unknown rather than fabricated during upgrade. Saved final HTTP 201 responses
previously classified as invalid acknowledgements can be reconciled from their
exact stored ranges, verified source size and attempted/acknowledged prefix.
`upload_acknowledgement_resolution` explains the interpretation while original
first/latest error evidence is retained. This does not infer publication or resend
bytes: the existing operation is inspected/polled through the normal guarded path.

Each attempt has a durable `video_delivery_attempts` snapshot independent of the
32 most recent events. Initialization and the first/latest upload errors retain
HTTP status, a redacted response excerpt, provider request ID, chunk/range and
start/completion times. Exact initialization and request URL SHA-256 values allow
binding checks without persisting signed URLs. Redirects are not followed. A
stale poll cannot overwrite newer upload outcomes. Source revalidation creates
a separate snapshot and does not rewrite the original source/upload evidence.

Provider observations contain nullable `uploaded_bytes` and `downloaded_bytes`,
observation time, query HTTP status, success and provider request ID. Explicit
zero survives JSON serialization; absent counters remain null. Successful status
retrieval does not acknowledge an upload or establish publication. Provider
status remains unchanged by local diagnostics.

After an upload error and five minutes with no confirmed progress, Social shows
`stalled_upload` while retaining TikTok's PROCESSING_UPLOAD status. Automatic
reconciliation backs off from 5 minutes to 15, 30, 60 and then 360 minutes, pauses
after 12 checks without progress, and remains available for manual inspection.
New provider progress restarts the budget. Foreground checks are bounded to five
minutes and use backoff. Neither stalling, elapsed time, zero bytes nor HTTP 404
authorizes replacement. Existing provider-confirmed failure/revalidation guards
and single-target recovery remain mandatory.

Authenticated routes:
- GET `/posts/{post}/delivery/{target}/inspect`
- POST `/posts/{post}/delivery/{target}/revalidate`
- POST `/posts/{post}/delivery/{target}/recover` with recovery_id and confirm=true

Post details expose source/attempted/acknowledged/provider bytes, timestamps,
durable errors, status inspection and a separate recovery action. The recovery
button is offered only after a valid retry/resume decision; pending/unknown
operations have no resubmit action. Successful siblings are never recovered.

TikTok defines upload HTTP 404 as no valid task for that upload URL, and HTTP 403
as an expired URL. The original post 908 incident did not retain enough upload
response/binding evidence to establish the underlying 404 cause. These changes
preserve evidence for future incidents without guessing a historical cause.
