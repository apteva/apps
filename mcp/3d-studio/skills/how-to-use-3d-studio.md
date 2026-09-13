# 3D Studio

Use this app for editable low-poly game meshes. The engine and server are Go.
Call `studio_capabilities` first for current operations and limits. Unsupported
operations (bevel, loop cut, UV unwrap, rigging) must not be presented as available.

1. Create a model with `assets_create` (template `empty`, `box`, or `car`).
2. Inspect node summaries with `mesh_inspect`. `include_mesh: true` reads up to
   500 elements; narrow larger meshes using `mesh_select` first.
3. Select `face` or `vertex` elements with `mesh_select`. Face bounds filter face
   centroids, not whole-face containment. `normal` is a direction filter.
4. Call `mesh_edit` with the current `expected_revision_id`, a list of typed
   commands, and `selections: {alias: saved_selection_id}`. Commands use that
   alias. Extrude/inset can set `result_selection`; subsequent commands in the
   same batch can use the new cap selection. All operations succeed together.
5. Prefer `mode: preview` for uncertain shape changes. Inspect the candidate with
   `assets_render` and `mesh_inspect` using its `candidate_id` and `asset_id`.
6. Commit with `mesh_edit_commit`. Use a unique `request_key` for each logical
   mutation; reuse exactly the same input/key on retries. Discard unwanted
   candidates with `mesh_edit_discard`. Candidates expire after one hour.
7. Render saved revisions from several views. `assets_render` returns a PNG
   artifact URL in JSON; a vision-capable client must fetch that authenticated
   URL to see the image. The SDK does not deliver a native MCP image block.
8. Export `glb` for games or `json` for editable source with `assets_export`.

Coordinates: meters, right-handed, Y-up. Rotations are Euler degrees applied
X, then Y, then Z. Selection transforms use centroid pivot unless specified.
A positive `radius` enables proportional editing; use `connected_only: true`
to prevent nearby disconnected geometry moving. `inset.amount` is a centroid
fraction, not a constant physical inset distance. `mesh.mirror` makes a baked
reflected copy across a world plane; it is not a persistent mirror modifier.

Selections are bound to a specific revision. Never reuse stale handles after
saving or restoring. Successful edits return new handles for surviving and
named output selections. If topology removes selected faces, their old handles
are invalidated. On a revision conflict, fetch the latest revision and reselect.

The first release exports solid-color PBR materials and flat normals with
separate centered object pivots. It does not provide UVs, textures, collision,
LODs, animation, or a guarantee that a visually plausible mesh avoids every
self-intersection. Open boundaries are reported as warnings. Inspect geometry
and renders before declaring a game asset ready.
