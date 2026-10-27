# Expired TikTok upload recovery

Social 0.20.4 adds an explicit, target-scoped recovery decision for expired
native TikTok FILE_UPLOAD operations. Ordinary retry and background status
polling do not automatically retire a PROCESSING_UPLOAD operation.

TikTok documents that the upload URL is valid for one hour after issuance:
https://developers.tiktok.com/docs/en/content-posting-api-reference-direct-post
This is an upload deadline, not a provider FAILED status or a cancellation API.
Social requires all of the following before offering replacement:

- An application-owned initialization receipt bound to project, target, native
  operation and source attempt. Use the later receipt time plus a five-minute
  margin. Arbitrary supplied dates, a 404, or absence from public history do not
  establish eligibility.
- A durable stopped-uploader receipt, written by the exiting uploader, with an
  explicit zero acknowledged-byte counter and an HTTP rejection. A crashed or
  still-active uploader stays blocked. Accepted/ambiguous upload responses stay
  blocked. The stop receipt also retains in-memory acknowledgements if saving
  the ordinary diagnostic snapshot failed.
- Two distinct, successful, fresh status observations after expiry and uploader
  termination, both PROCESSING_UPLOAD with explicit uploaded_bytes=0 and no
  publication identity. Missing values stay unknown; positive downloaded bytes
  or any previous positive uploaded/downloaded high-water value block recovery.
  A missing downloaded_bytes field is retained as unknown: FILE_UPLOAD is not a
  provider-initiated download.
- Current source bytes/checksum, exact approved revision, active account and
  connection, and current TikTok creator/privacy options are revalidated. Status
  is refreshed again after source validation. The replacement's source is pinned
  to the checksum authorized by that decision.

## Existing tools and UI

1. `post_delivery_inspect(post_id, target_id)` shows diagnostics, per-attempt
   receipts and the local disposition without starting a publication.
2. `post_delivery_revalidate(post_id, target_id, review_expired_upload=true)`
   explicitly reviews stored evidence and prepares a ten-minute recovery grant.
   The target UI exposes **Review expired upload** when acknowledgement is
   explicitly zero. Review never publishes or retires an operation.
3. `post_delivery_recover(post_id, target_id, recovery_id, confirm=true)` refreshes
   the evidence and consumes one authorization per target/revision. Grant
   consumption, old-attempt retirement and target transition are atomic. The
   existing selected-target publisher initializes exactly one replacement. Grant
   replay cannot dispatch again; a lost/ambiguous replacement is never silently
   reinitialized.

The old operation is preserved as `locally_abandoned`, with its original ID,
provider PROCESSING_UPLOAD status, response evidence, source attempt, reviewer
(reported workflow actor), timestamps and recovery ID. Social does not claim
TikTok has marked it FAILED. Results captured for the old attempt cannot write
diagnostics, publication identity or completion state to its replacement.
Facebook, YouTube and other sibling targets are not retried.

## Legacy evidence

Explicit review may adopt a complete application-owned diagnostic receipt from
before the uploader table existed: versioned progress, successful initialization,
exact operation/URL-hash binding, first-chunk HTTP 404 rejection with known
response timestamps, and explicit acknowledged zero. The historical uploader
returns immediately on that rejection. Adoption records `reviewed_stored_receipt`
and still requires the same fresh provider checks and confirmed recovery.

A prose report, last_attempt_at, old unverified transfer estimates, or an external
receipt reference is insufficient by itself. Missing initialization,
acknowledgement or uploader evidence remains unknown and blocked. Legacy cases
such as Monika require evidence investigation; this change does not invent zeros
or allow an agent to override them with a timestamp/boolean assertion.

## Production workflow

This app release does not deploy production or execute recovery. After deployment
and evidence review, an operator can recover the specified Social target and
verify the resulting native ID, URL and provider publication time before updating
the same existing Editorial release and Catalog sharing association. Those
workflow writes are separate; Social does not create replacement Catalog assets
or use its local observation timestamp as evidence of native publication time.

## Regression coverage

Tests cover a fresh rejection/stop receipt, explicit review and one-time recovery,
preserved successful Facebook/YouTube targets, unknown versus zero, historical
positive progress, failed or conflicting status reads, source/account/approval/
privacy changes, expired or replayed grants, and late polling/upload/init results
(including an operation ID reused with a different attempt). Existing YouTube
same-session retry and all other Social regression tests remain release gates.
