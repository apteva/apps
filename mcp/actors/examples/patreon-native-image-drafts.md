# Native image posts and existing-draft recovery

This user-configured example repairs native image verification without depending
on image-layout classes, child indices, blurred CSS or CDN URLs. It uses the
assigned authenticated browser to read the saved post's JSON document, resolves
its ordered image relationships by **type and ID**, and checks file names,
readiness, media type and exact IDs across editor reloads. Effective tier display
labels are read from the verified editor menu, including legacy tiers with null
API titles. `inspect_control` can only open a fresh, enabled SOM button classified
as `open_configuration`; it cannot select or modify a control.
Site-specific URLs, JSON paths and access rules live in the example definition.
Actors itself contains no Patreon account, plan, price or attachment IDs.

Creation operations: `image_post` (base64), `image_url_post` (original Storage URL),
and `gallery_post` (two original URLs in approved order). Each has a named
`*_preflight` that stops before Publish. Supply creator/context, exact title/body,
audience, the actual creator's tier names, media sources/file names and a stable
`request_id`. Keep Catalog/Storage lineage checks and process live-predecessor
checks; these operations do not replace either.

Read-only `inspect_image_draft` / `inspect_gallery_draft` accept `post_id`, the
exact observed `post_edit_url`, the original publishing inputs and
`expected_attachment_filenames` in approved order. They re-open the same editor,
read saved attachments, reload and verify exact copy, creator, audience,
sale-off and saved tier names. They do not create, edit, schedule or publish.
A Public post has no selected tier names; a Paid post must have the exact
requested set. The corresponding `publish_*_draft_preflight` also checks the
reviewed attachment IDs/sizes and the final editor controls before Publish.

To recover a failed creation run, first run both `fetch_posts` and the configured
`inspect_*_draft` at the reviewed replacement revision. Then call:

```json
{
  "id": "original failed run ID",
  "revision": "reviewed replacement revision",
  "reviewed": true,
  "duplicate_check_run_id": "fresh complete fetch_posts run ID",
  "resource_check_run_id": "fresh completed draft inspection run ID"
}
```

Use integer IDs with `actors_run_recover`. The policy explicitly maps resource, upload, duplicate and state fields; no
Patreon field conventions are embedded in the engine. The engine derives the saved draft
from the failed run URL, verifies no commit was attempted, binds the fresh
inspection to the original upload names/sizes/order and creator/request inputs,
and supplies the original media IDs to the dedicated resume operation. It
preserves the original operation's durable once-key namespace and original
request identity. The existing matching **draft** is the sole permitted match;
a published/scheduled match, another duplicate, missing view or incomplete
pagination blocks. Coverage is repeated immediately before recovery execution.
Unknown/successful outcomes cannot recover. Ordinary retry still uses its
original immutable snapshot and should not be used for this repair.

`publish_image_draft` / `publish_gallery_draft` are `recovery_only`. They cannot be
queued as normal runs or scheduled tasks. They have no creation/upload/copy/access
editing steps. Only explicitly reviewed inspection controls may open; Publish
must remain bound to the saved draft ID, URL and ordered media IDs. Production
recovery is never automatically replayed when a new revision is saved.

On failure, `document_evidence` retains the structured observation, source editor
URL, post ID, attachment IDs/names/sizes/states in saved order and array counts.
`failed_condition` records the rejected check, and `execution_progress` reports
whether the guarded commit was attempted. A partial observation or failed check
never grants publishing readiness.


A specifically approved body amendment can be saved separately with
`prepare_image_draft_copy`: supply the original `expected_current_body`, approved
new `post_body`, unchanged title/creator/access and verified attachment IDs, names
and sizes. It verifies the original unscheduled draft, changes only its body, then
reopens it and checks saved copy and unchanged media/access. It never publishes.
Then run the canonical `inspect_image_draft` with the approved new body and pass
`reviewed_copy: {"post_body": "approved exact new body"}` to guarded recovery.
Only creation operations explicitly declaring that copy field accept amendments.
The original failed input remains immutable and recovery provenance records both
values. Any unexpected body, media, access or persistence change blocks.
