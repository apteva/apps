# Two-original-image Patreon post example

This optional actor definition is user configuration, not a built-in workflow or seeded creator. Import it and supply your saved Computer context and Patreon creator URL.

`gallery_post` is a named publishing operation alongside text, single-image and video operations. It currently supports exactly two distinct approved originals in their provided order.

Inputs: `context_id`, `creator_url`, `post_title`, `post_body`, `audience` (`public` or `paid`), `tier_labels` (array of exact creator menu names; empty for Public and nonempty for Paid), `image_1_source_url`, `image_2_source_url`, each `image_N_filename` and `image_N_mime_type`, and a stable `request_id`. Supply the identical stable Actors idempotency key. Source URLs must resolve to the approved original files and remain valid for the whole run.

Read the creator's current plans through `list_tiers` and, where needed, `get_tier` before choosing access. Names and prices are caller/creator configuration. Do not infer an audience from the number of tiers or use another creator's labels. Reconcile a live price/name mismatch before writing. The posting action clears prior choices, selects exact runtime labels through fresh SOM targets, and verifies the complete selected set after saving and reloading. Legacy/unrequested tiers fail verification. Free/Public uses an empty array; Paid without a verified selection fails closed.

The actor verifies Sell this post off before switching audience and again after reload, accepting the hidden sale control on Public posts only when Public is verified. It uploads each original separately, captures Patreon attachment IDs, reloads, and verifies exactly two images, original ID order, image access, exact title/body including paragraph breaks, and audience. Only then does it click Publish with a durable once-key guard and require Patreon’s live-post confirmation.

A failed/uncertain run must be reconciled read-only before another attempt. Never fall back to generic run, change the key, drop attachments or loosen tier/copy checks to bypass failure. Original hashes/lineage and editorial approval/due checks belong in the caller process before invoking this action. This operation does not add scheduling or choose creator-specific pricing policy.

Requires Actors 0.2.14 or later and a current Computer version with semantic uploads/forms and preserved newlines. Site markup can change; failures must remain blocked until reviewed.

Exact SOM names allow only the configured numeric member-count suffix. Similar tier names remain distinct; saved selection verification uses canonical labels from the DOM.
