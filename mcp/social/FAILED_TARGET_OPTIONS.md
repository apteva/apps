# Correcting one failed destination

`post_target_options_update` corrects publication options on one failed target
of a failed or partially published post. It does not publish, create a Social
record, edit the parent post, or change successful destinations. The account,
source media, audience, and destination are fixed. Reviewed publications remain
blocked: this tool cannot bypass approval.

1. Read `post_get`. Take `revision` from the post and `options_revision` from
   the selected target. Inspect the delivery if an operation is already present.
2. Call `post_target_options_update` with the exact IDs, both expected revisions,
   an `options` patch, and a reason. For example:

   ```json
   {
     "post_id": 100,
     "target_id": 200,
     "expected_revision": 1,
     "expected_options_revision": 1,
     "options": {"privacy_level": "SELF_ONLY"},
     "reason": "Publisher explicitly selected private delivery"
   }
   ```

   Social validates TikTok privacy against that creator's current options;
   there is no Public default. Other platform options use their declared field
   types and choices. Null removes an optional key, but cannot remove privacy.
   A stale revision, invalid choice, no-op patch, or failed permission lookup
   does not authorize retry.

   TikTok corrections require a direct connection: provider adapters that cannot
   expose current creator privacy choices are blocked rather than guessing.
3. Inspect the result. Corrections remain failed until a separate explicit
   retry. Use the returned `retry` arguments with `post_retry`, including
   `target_id` and `expected_options_revision`. Bulk retries exclude corrected
   targets. No successful or other failed sibling is selected by this retry.

Existing TikTok operations are read back first. A completed publication is
reconciled without retry; processing or uncertain outcomes block correction.
Only a confirmed FAILED operation can be retired, with its ID/reason preserved
in attempt history. Existing YouTube upload sessions must be reconciled/resumed
with their submitted metadata; they cannot be replaced by this tool. Unresolved
upload receipts, duplicate conflicts, and unverified legacy failures also block
correction. Proven local preflight failures can be corrected without starting
an operation.

`post_delivery_inspect` returns `options_corrections` (latest 50), including old
and new options, parent and target revisions, source attempt, previous failure,
actor, reason, and timestamp. Saving invalidates prepared recovery grants bound
to the old options. Target revision and attempt are rechecked after provider
validation, and the option update and audit entry commit together.

The equivalent HTTP endpoint is
`POST /posts/{post_id}/delivery/{target_id}/options`. Its body contains
`expected_revision`, `expected_options_revision`, `options`, and `reason`.
