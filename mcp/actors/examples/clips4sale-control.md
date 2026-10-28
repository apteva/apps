# Clips4Sale Creator example

This is a reusable actor definition, not a built-in site integration. Supply a
saved Computer Browserbase `context_id`, `expected_store_id` and
`expected_store_name` for every account. No account, category, audience or price
is fixed in the engine. Computer 0.7.95 or later is required.

## Operations

- `get_account`: verified store identity, dashboard counters, payout label and
  any expired-ID payment warning. Currency is returned as a verified symbol;
  it is not inferred as an ISO code.
- `get_stats`: current-month revenue and audience plus separately named lifetime
  follower count. `sales_widget_purchases` is a separate widget counter whose
  period is not inferred from the revenue selector. Only `period: "this_month"` is supported by this example.
- `list_clips`: All, Drafts, Review and Deleted with clip deduplication by clip ID,
  pagination/count checks and explicit coverage. `record_type: "clip"` is the
  price-bearing record. `record_type: "review_issue"` is a separate linked
  observation with `clip_id`, `issue_id`, native status, reason and comment;
  Review is an attention-required table, not a list of successful publications.
  Coverage applies to the new
  creator library, not a guarantee that legacy clips are represented.
- `prepare_clip` / `prepare_clip_base64`: upload and save a draft, without Publish.
  The former uses `source_url`; the latter uses `media_base64`.
- `prepare_existing_clip`: complete metadata and price on an existing draft.
- `preflight_clip`: independently reread the saved draft and preview, stopping
  before Publish. It is a read-only operation.
- `verify_clip_submission`: reconcile a prior submit attempt using full read-only
  catalog coverage and exact ID, title, filename, price and non-draft status.
  A matching Review issue blocks success; inspect its reason/comment.
- `publish_clip`: verify the existing draft then attempt submission once and
  verify its catalog status and absence of a matching attention-required issue.
  Clips4Sale submission may enter validation rather than
  immediately become publicly available.

## Preparation inputs

In addition to account/context inputs, supply `title`, `description`, `audience`
(the exact visible label), `category` (the exact visible label), `price` as a
string such as `"6.99"`, and `expected_currency_symbol` such as `"$"`.

For new uploads, also supply `filename` ending in `.mp4`, a stable unique
`request_id`, `mime_type` (defaults to `video/mp4`) and exactly one media source
through the appropriate operation. Preserve the returned clip ID and saved
filename, which may differ from the uploaded filename.

This first example requires an explicit `asset_class: "no_real_person"`.
Real-person footage is rejected: selecting verified registered performers and
associated records is a different workflow and is not implemented here. Never
use this declaration to bypass performer registration.

New uploads first save the metadata, inspect the catalog, select the unique
new draft and reopen its verified edit URL. They then wait up to 90 seconds for
the generated preview source to appear,
then require matching preview/thumbnail/store IDs. The wait targets the visible
video player; source URLs are extracted from the raw saved DOM. Missing previews stop before
Save or Publish; reconcile the uploaded draft rather than uploading again.

For existing preparation, preflight or publication also supply `clip_id`.
Preflight and publication additionally require `expected_saved_filename`.
Exact copy, audience, category, person classification, filename, media IDs,
media store IDs, price, currency symbol and immediate schedule are checked.

## Recovery and bounds

Use the same request identity across retries. Preparation reserves a context
key before upload; an ambiguous upload must be reconciled through `list_clips`
and `prepare_existing_clip` rather than uploading again with a new key.
Publication reserves `c4s:<store_id>:clip:<clip_id>:publish`. A failed response
can still mean the site received the submission: inspect the catalog and the
original run before deciding its outcome. Do not issue another Publish.

Custom thumbnail names that do not expose a verified clip ID fail closed. A
missing view, total mismatch, exhausted budget or pagination problem must not
be interpreted as an empty catalog. The multi-page traversal has fixture
coverage; a small local account cannot establish live
multi-page compatibility. Dashboard clip totals may differ from this library.

The `validation` and `under_review` statuses prove neither public availability nor successful
moderation. A clip flagged for duplication/compliance is reported as blocked,
with its issue evidence preserved. Never resubmit that same clip automatically.

Each inspection deduplicates clip records across its views. Mutation runs also
retain earlier audit snapshots in their dataset; use the latest verified
collection rather than combining old and new statuses. Moderation may arrive
after submission, so run `verify_clip_submission` again before treating a
validation/under-review/pending clip as ready for customers. Public availability requires an
actual active/published state.
