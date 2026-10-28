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
