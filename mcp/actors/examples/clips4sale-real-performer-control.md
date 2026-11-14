# Clips4Sale real-performer workflow

This Actor definition adds a separate real-performer path to the original
no-real-person example. It does not change or relabel existing footage. Supply
an authorized saved creator context and exact expected store identity. Runtime
availability never supplies authority to upload, email, sign or publish.

The initial supported layout is one performer and one reviewed release file,
with one or two reviewed ID files. Use the `_two_id_documents` operation when
both sides are separate files. Additional performers or attached documents fail
closed; do not omit a person or substitute the no-real-person classification.

## Authorization records

Use `register_performer` only for a genuinely authorized performer. Upload only
real supplied documents using `upload_performer_document` or its base64 variant;
retain their provenance and the platform's actual IDs. `get_performer_documents`
reads native document IDs, filenames, types and review statuses without opening
or downloading identity document contents.

`verify_performer_authorization` requires the selected ID and release document
records to have native status `valid` (the UI label is Reviewed). Under Review,
Rejected, missing or ambiguous records do not qualify. It also verifies the
performer's actual ID, stage name and store using complete native collections.
Old rejected documents which are not selected do not invalidate reviewed ones.

Supply `recording_date` and `authorization_evidence_ref` from the actual
production/release record. The caller must review that the signed release covers
that recording and its distribution. These references preserve business evidence;
the browser Actor does not interpret a legal document or validate an external
Gigs reference by itself. Do not use a generic Boolean or requested email as
proof that a performer signed a release.

## Digital consent

`request_clip_consent` retains its original exact performer/email/clip guards and
once keys. Queue it only with actual authorization to contact that performer.
The performer completes identity verification and signs the consent themselves.
Do not impersonate them, submit invented documents or sign for them.

`verify_clip_consent_request` verifies only the pending request receipt.
`get_clip_consent_status` returns the observed status without treating it as
approval. The revised `verify_clip_consent` runs the full real-performer
preflight, including reviewed documents and their exact assignment to the clip.
A Consent Requested badge is not consent. A Reviewed clip-row badge alone is
also insufficient: the selected documents must be reviewed in the performer
record and the exact filenames must be attached under that performer's native
popover IDs on the exact clip.

## Draft and publication steps

`upload_performer_clip_draft` and its base64 variant upload privately under the
original durable preparation key, select the real-performer mode and stop before
performer assignment, consent messaging or Publish. They return the observed
native draft URL/title/filename; do not claim that final copy or authorization
has been saved. Preserve that draft and logical request after any uncertainty.

Complete the performer assignment through the native digital-consent workflow
or select the reviewed ID and release files. Then use
`prepare_existing_clip_with_performer` to prepare that same authorized draft.
Its `current_title` and `current_description` freeze the existing draft; `title`
and `description` are the desired approved copy. It never selects No Performer.

`preflight_clip_with_performer` checks exact store, performer, attached documents,
copy, classification, filename, preview/thumbnail/store IDs, price, currency and
immediate schedule without publishing or sending. Its only input actions open
and dismiss document metadata popovers. `publish_clip_with_performer` repeats
those gates and retains the original context-scoped
`c4s:<store>:clip:<clip>:publish` key, shared with the original publishing path.
Changing operation names never authorizes a second Publish.

Submission can enter moderation rather than become publicly available. Use
`verify_clip_submission` and native review issues to reconcile the result. Never
replay an ambiguous upload, consent request or publication under a fresh key.
The store-owner expired-ID payout warning remains separate and visible.

## Validation

Mock browser tests cover reviewed documents, pending/rejected/missing releases,
wrong performers and popover IDs, extra people/documents, wrong media/price,
missing evidence, one/two ID documents and duplicate-publication prevention.
Only read-only inspection is used against the live account until a real model,
actual documents and an exact authorized clip are supplied.

### Verified replacement and document saves (Actors 0.3.4 / Computer 0.7.98)

`preflight_replace_clip_video` stages and SHA-256 verifies a replacement MP4 in
its existing Computer session without clicking Replace File. `replace_clip_video`
performs its own preflight in the same run, confirms Replace File once, selects
the staged file, waits for the editor, clicks Save and Exit once, and reloads the
same clip to verify its native filename, exact copy, store and performer cards.
A standalone preflight does not transfer its temporary file to a later session.
No replacement operation publishes or sends performer messages. Use the existing
explicitly authorized submission flow only after inspecting the saved replacement.

Required replacement inputs include the source URL and SHA-256, a safe
`filename_stem` and matching `filename` (`<stem>.mp4`), the exact prior filename,
clip/store identity and copy, and the exact sets `expected_performer_names` and
`expected_performer_cards` observed before replacement. Choose a new basename;
the old filename cannot match the expected replacement pattern. The commit also
requires `replacement_authorized:true` and a unique `request_id`. Reuse the same
request identity when investigating failures; never mint a new key to retry an
uncertain Replace File or Save click.

The default Browserbase path retains Computer's 100 MiB transport ceiling.
Files over that limit fail before changing the existing clip. The `_local`
variants require an explicit `local_context_id`, use only that local backend,
and stream up to 5 GiB to private disk before native file selection. The browser
and Computer must share the host filesystem. Configure and log into that context
separately; these operations never copy cloud login cookies or choose a different
backend automatically. A local Mac test does not prove a production host has a
working local-browser installation or an authenticated Clips4Sale context.

Staged files expire after 15 minutes and are removed after use, replacement or
session closure. An upload with `require_prepared:true` cannot silently fetch a
new source after destructive confirmation. A failed or missing preflight, wrong
backend, expired preparation or SHA mismatch blocks replacement.

Document uploads stage the source, reveal the whole Save Documents control,
click it once, require the filename to leave the staged list, and prove a native
saved document ID/type/filename before and after reload. Use the read-only
`verify_performer_document_saved` to reconcile an uncertain save, without
uploading another copy or clicking Save again. Native `tbd`/Under Review is a
saved receipt awaiting platform review, not approval.

## Draft previews and thumbnails

Use `set_clip_thumbnail` for a JPG or PNG and `set_clip_preview_video` for an
MP4 preview. Pass `visibility: sfw` or `visibility: nsfw` explicitly. Each run
sets one slot on an existing draft, so separate SFW and NSFW assets can be
assigned independently. If the SFW slot currently copies NSFW, set the separate
SFW asset first.

Supply the saved browser context, exact store ID and name, clip ID, title, and
saved main filename. Also supply the asset filename, HTTPS source URL and
SHA-256, `preview_upload_authorized: true`, and a unique `request_id` for the
intended change. Clips4Sale expects 16:9 assets, with a 5 MB limit for thumbnails
and a 100 MB and 60-second limit for previews. The native uploader validates the
file.

The operation stages the supplied bytes before opening the upload control. It
checks the other slot, saves and exits, then reopens the draft to verify the
saved asset. For thumbnails it compares the uploaded filename with the exact
SFW or NSFW field in the saved clip record because the editor may display a
resized or aliased image URL. If an upload or save reports an uncertain outcome,
inspect the draft before issuing another request.

## Previews and thumbnails on active clips

The Clips4Sale Creator actor contains the draft and active-clip operations
for all store contexts. Deploy the complete definition with Actors v0.3.5 or
later, which supports definitions up to 2 MiB.

Use `preflight_live_clip_thumbnail` or `preflight_live_clip_preview_video`
to check the exact published clip and selected SFW or NSFW slot. The
preflight requires an active listing that is up to date, so edits already in
progress or awaiting review are left for the operator to resolve.

Run `set_live_clip_thumbnail` or `set_live_clip_preview_video` with the
same identity and asset inputs as for a draft, plus
`live_edit_authorized: true`. The actor uses Save and Exit, then confirms
Publish changes if the versioning dialog appears. It reads the resulting
publication and review states and verifies that the same clip remains active
with the original main filename, price and publication timestamp. The
receipt includes the staged asset evidence and the approved and pending
asset fields returned by Clips4Sale.

## Daily traffic

Use `get_daily_traffic` with an explicit `date` in `YYYY-MM-DD` format and
the saved context, expected store ID, and expected store name. Dates follow
Clips4Sale’s New York reporting timezone. Run once per store and date. The
audience report provides unique visitors. The traffic source report provides
impressions by source, purchases, and creator income.

## Creator Support tickets

`prepare_creator_support_ticket` opens the public Creator Support form in a
fresh browser and checks the email, store numbers, payable name, subject,
and description after filling them. Provide one store number or a list
separated by commas. Review each separate request before sending.

`submit_creator_support_ticket` requires explicit
`ticket_send_authorized: true` and a stable `request_id` for each approved
request. It submits once and waits for the portal acknowledgment. If the
portal does not confirm submission, check for the confirmation email
before retrying.

## Payouts

Use `get_payout_verification_status` to check the email verification prompt
and payout tabs before onboarding. Use `send_account_verification_email` with
explicit send authorization and a unique request ID. Match the incoming
verification email to the recipient shown in the actor receipt.

Use `get_payout_settings` for the Creator Workspace setup and disabled tabs,
`get_payout_method` for the method shown after onboarding, and
`get_payout_history` for payment and adjustment rows. The method report reads
only the method and destination type fields. Payout history follows the
workspace's pagination and preserves displayed signs and currency.

Use `set_payout_account_type` for initial EU registered-business onboarding.
Supply the legal and payable names, context-bound secret references for the
VAT ID and registration number, and a temporary HTTPS proof-of-address
source with its filename and SHA-256. The tax classification selected is
EU business. Use `set_payout_sole_trader_account_type` for the separate EU
sole trader classification. It takes the names, VAT ID reference and proof
of address. Both Account Type writes verify the expected profile country
before opening the tax form. Use `set_payout_method` separately after Account
Type is saved.
It supports Spanish SEPA and takes the bank account type (`C` for checking
or `S` for savings) and context-bound secret references for the bank name,
IBAN, and BIC.

Both writes require `payout_setup_authorized: true`, a unique `request_id`,
and the exact store identity. Each clicks Save at most once per request.
If an OTP prompt appears, the run records a confirmation-code marker and
stops. Check the store's payout status before retrying any uncertain save.

Use `preflight_payout_minimum` to verify the editable USD threshold.
`set_payout_minimum` requires the exact expected current amount, an explicit
new amount, `payout_minimum_change_authorized: true`, and a unique
`request_id`. The actor verifies the saved value after a reload. Complete
account verification and banking onboarding in Creator Workspace. For
changes to banking details on file, use Clips4Sale's secure change form.


### Delete a clip and reconcile deletion

The shared actor provides `preflight_delete_clip`, `delete_clip`, and
`verify_deleted_clip`. Runtime inputs bind the context, store ID, store name,
clip ID and exact title, with no production IDs embedded in the operations.
The write also requires `expected_status`, a native `library_tab` (`Drafts` or
`Review` for the bulk control; `All` for the row control), and boolean
`delete_authorized: true`.

Preflight checks the signed-in store and the native clip ID, title and status,
selects exactly one clip, opens the native confirmation, and stops. Only
`delete_clip` confirms it. It checks the exact selected ID before opening the
confirmation, then checks that the modal confirms exactly one reversible
deletion. The modal hides the background table from accessibility extraction.
The write reserves `c4s:<store>:clip:<clip>:delete` in the context scope and requires
positive evidence of the same clip in Deleted. This uses the normal reversible
delete, not permanent trash purging.

After an ambiguous delete attempt, run `verify_deleted_clip` using the same
context/store/clip/title. Do not replay the write. Verification never selects
clips or clicks Delete, Restore or Force Delete. A completed preflight is not a
deletion receipt.

`preflight_delete_clip_from_row` and `delete_clip_from_row` use the per-row menu
for inventories where the bulk menu omits Delete, including the All tab. They
share the same store/clip guards, deletion reservation and verification steps.

Verification reads every page of Deleted and checks the native `trash` status,
clip ID and title. It does not depend on a search box or an ephemeral success
notification. `get_store_launch_status` reads owner verification, payout setup,
store and performer pages without changing settings.
