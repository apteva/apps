# Actors 0.2.25

Read-only pagination waits for verified AJAX advancement without clicking twice and compares sorted record IDs, so DOM reordering cannot masquerade as progress. Semantic pagination reveal overlaps short viewports and can begin at either document boundary.

Definitions may opt into conservative partial results after verified pagination stalls. This requires `allow_partial` and is restricted to read-only operations. The returned coverage remains incomplete with more results possible and the explicit stop reason. Identity changes, unsafe controls, changed records and truncated HTML still fail. Existing strict definitions, including Patreon, retain their behavior.

The editable AdultFolio example uses a tested 400-pixel viewport, stable record metadata and explicit partial-history policies. A live read on the previous runtime retrieved 179 unique messages across 30 pages, beyond the earlier 149-message reveal limit. This is bounded coverage, not proof of exhaustive history. No message is sent by the example or tests.

Validation: full Go tests, build and pagination regression fixtures, including delayed responses, terminal evidence, reordered stalls, account changes, bounded prefixes and overlapping viewport reveal.
