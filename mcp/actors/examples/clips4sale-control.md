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
The original publishing operations still reject real-person footage. Performer
registration, document storage and clip-specific consent requests are separate operations;
a contact record or uploaded file must never be treated as approved identity
or signed consent. Never use the no-real-person declaration to bypass these
requirements.

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

## Performer records and documents

Supply the same account/context inputs for every operation. Names, email
addresses, IDs and genuine documents are caller inputs, never bundled presets.

| Operation | Inputs and result |
| --- | --- |
| `list_performers` | Paginates All Performers and reports explicit coverage. Returns numeric `performer_id`, exact `stage_name`, store ID, native legal-name/DOB labels, clip count and verified edit URL. This covers registered records, not document approval. |
| `get_performer` | `performer_id`, `expected_stage_name`. Selects the unique collected record and rereads its identity, email, legal-name/DOB fields and document count. Blank identity details remain blank. |
| `get_performer_requirements` | Reads the native Action Required page and its document-action labels. This page is account-wide; it is not a proven mapping of each warning to a performer ID or a complete paginated document inventory. |
| `register_performer` | `stage_name`, lowercase `email`, stable `request_id`. Checks complete performer coverage, rejects an existing exact stage name, saves a hidden contact record with no documents, then independently rereads its saved stage name/email. Returns the assigned ID. Registration itself does not send an invitation or approve the performer. |
| `upload_performer_document` / `upload_performer_document_base64` | `performer_id`, `expected_stage_name`, `document_type` (`id` or `consent`), corresponding `document_label` (`Performer ID` or `Consent Form`), stable `request_id`, safe `filename`, `genuine_document: true`, and `source_url` or `document_base64`. `document_mime_type` defaults to `application/pdf`; use the appropriate image MIME type for images. Checks the selected type and pending filename before Save Documents, then reloads and verifies the persisted filename and increased document count. |

Digital consent is a **clip-editor** flow. Select the saved performer, choose
Request Digital Consent, enter the email, then use Send Consent Request.
Saving an email in Performer Management alone does not send anything. Never
manufacture identification or signatures; Clips4Sale must still accept the
genuine records.

- `preflight_clip_consent` checks the store, saved performer email and exact
  owned draft ID/title/body/filename, selects the numeric performer ID through
  its verified native menu, then verifies the exact confirmation dialog.
  It stops before the final Request Consent button. Supply
  `real_person_appears: true` only for footage that actually represents that person.
  The unsaved composer changes mean this is not marked strictly read-only.
- `request_clip_consent` requires that same explicit person declaration. It
  rejects an existing matching association/request, checks the email and dialog,
  confirms Request Consent once, then requires the exact native performer row
  to show Consent Requested. It verifies metadata, saves the association through
  Save and Exit, reloads and checks saved person mode and the same pending row.
  Supply `performer_id`, `expected_stage_name`, lowercase `email`, `clip_id`,
  `title`, `description`, `expected_saved_filename`, exact `audience` and
  `category`, plus account/context inputs. It never clicks Publish.
- `request_clip_consent_test` is an **explicit email-only test** for a no-person
  draft. It requires `allow_unsaved_test_request: true` and
  `real_person_appears: false`, checks saved mode `2`, and verifies the immediate
  native request result. It sends a real invitation but does not save a false
  person classification or claim a persisted clip association. Its recipient
  and all identities remain caller inputs. Use this only for an authorized test.
- `get_clip_performers` independently reads the draft and returns the native
  performer evidence without changing controls. With saved `performer_mode: "2"`,
  Clips4Sale hides the section; an empty visible view does not prove that no
  pending invitations exist.
- `verify_clip_consent` is strictly read-only. It verifies the store, performer,
  email, exact draft, saved person mode `0` and the exact Consent Requested row.
  A hidden, missing or ambiguous row fails; it never reveals, sends or saves.

The opener, final send and draft save use separate effect keys. The opener is
acknowledged as an email action because of its native Send label; opening the
confirmation dialog is never treated as success. Never resend after an
uncertain send or save. Reconcile the original run, performer documents and
saved clip instead. Similar stage-name matches stop safely.

Sending a request does not prove email delivery, a signature, identity approval
or publication readiness. A failed or ambiguous request must be reconciled
against its original run and the clip; do not use a new actor or request key to
send again. These operations do not click Publish.

Document upload reserves separate opener and final-save keys for the logical
request. A successful or ambiguous save cannot be replayed. Reconcile through
`get_performer` and the original run before issuing any new request identity.
A saved filename and document count prove storage, not approval or legal
validity. Read the native document evidence before deciding publication readiness.

Live checks exercised registration with the owner's supplied email, exact
performer reads and one accepted digital consent request. The no-person test
draft retained its original saved classification and was not published. Its
pending row was transient until Save; no saved real-person association was
claimed. Email delivery and signing are not inferred. Genuine ID/consent files
and real-person test footage were not supplied: document uploads and the final
association-save/reload workflow have fixture coverage, not live success coverage.
