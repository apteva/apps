# Two-original Patreon gallery

This optional user-definition example requires Actors 0.2.10 and Computer
0.7.93 or later. It is not installed or seeded by the app.

`gallery_post` supports exactly two original images, in the supplied order,
with Public (`audience: "public"`) or all paid tiers (`audience: "paid"`).
Other sizes and tier subsets must remain blocked instead of losing assets or
changing access. Both sources are uploaded independently; the actor does not
resize, merge or substitute the pictures.

Required runtime input:

- `context_id`, `creator_url`: verified saved creator configuration.
- `image_1_source_url`, `image_2_source_url`: ready HTTPS URLs for the exact
  approved original Storage files; use sufficiently long-lived signed URLs.
- `image_1_filename`, `image_2_filename`, `image_1_mime_type`,
  `image_2_mime_type`: original file metadata.
- `post_title`, `post_body`: exact approved plain text, including paragraphs.
- `audience`: `public` or `paid`.
- `gallery_paid_tiers_text`: complete verified tier-menu text in display order,
  including `Select all tiers`. Example only: `Select all tiers Basic $3/month
  Premium $8/month`. Obtain the real labels for the selected creator; do not
  infer them from this example or another creator.
- `request_id`: a stable, persisted per-release key. Pass the same value as
  Actors `idempotency_key` and retain the accepted run ID.

The actor selects and verifies every paid tier, establishes Sell this post off
before switching audience, and accepts Public's absent sale control only with
that prior evidence and a verified Public radio. It captures Patreon asset IDs
after each successful upload, reloads the saved editor, requires exactly two
attachments, and compares their persisted IDs in order. Required exact title
and paragraph-preserving body patterns gate Publish. The commit uses a durable
`once_key`, then requires Patreon's live-post confirmation.

The publishing process must independently verify the exact live post, creator,
copy, access and original-source lineage, then reconcile Editorial records.
Any failed guard or unknown result stops writes. Reconcile through read-only
operations; never switch to generic `run`, change the request key to bypass a
guard, or publish again to repair bookkeeping.
