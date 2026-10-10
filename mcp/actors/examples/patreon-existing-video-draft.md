# Optional existing Patreon video draft operations

Requires Actors 0.2.19+ and Computer 0.7.94+. This is an editable definition;
accounts, post IDs, video hosts and tier policy are runtime inputs.

`inspect_video_draft` is read-only. It verifies creator identity in the assigned
context, scans the complete Drafts view (including pagination), selects exactly
one requested `post_id`, checks its `post_edit_url`, then opens that editor.
It returns exact saved title/body, media source/player/save observations and the
Publish control. Scheduled or published records cannot satisfy the draft gate.

`attach_video_to_draft` preserves the existing copy and post ID, requires no
existing media, selects the exact requested audience/tier set with sale disabled,
and attaches the supplied `video_url`. It reloads and verifies the saved player
source, exact title/body, audience, tier combination and sale setting. It stops
without publishing. An uncertain result requires inspection, not blind reattach.

`publish_video_draft` independently repeats draft/account/ID checks and saved
copy/media/access checks. It publishes only that existing draft with a guarded
semantic Publish control and stable once key, then verifies the success message
and the same post ID in its live URL. It never clicks Create or creates a post.
If media already saved correctly, call this operation without reattaching.

All operations require explicit `context_id`, `creator_url`, `post_id` (string)
and the exact observed `post_edit_url`. Attachment/publication also require
`post_title`, exact `post_body` (paragraph breaks preserved), `video_url` (canonical
embed URL; player query parameters may be appended), `audience` (`public` or
`paid`) and exact runtime `tier_labels` ([] for Public, a nonempty array for Paid).
Publication additionally requires stable `request_id` and Actors idempotency key.
Get tier labels from live plans; creator prices/names never live in the engine.

Worker recovery: complete published/draft/scheduled duplicate inspection first.
Read this draft, reconcile it to the approved release and asset, attach only if
no media is present, then verify/publish the same ID when due and authorized.
Wrong existing media, copy, access, identity, incomplete reads or unknown write
outcomes block. Do not call video_post or a generic fallback to replace the draft.
This operation does not approve content, change a release date or authorize early
production publication. It preserves existing copy; a copy mismatch requires
separate reviewed draft editing rather than silently rewriting text.
