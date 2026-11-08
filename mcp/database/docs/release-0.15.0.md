# Database 0.15.0

Adds bounded related-parent aggregation to the existing `aggregate` operation.
Use the same `relation` and `match` definitions as `find` so search results,
exact totals and summary metrics agree, within one read snapshot.

## Behaviour

- Parent scope and filters apply first. Related-row eligibility, deduplication
  and ordering run before the shared context limit, in source order. `match`
  tests only those retained entries; several matching entries still contribute
  the parent's score and count once.
- Parent-level `computed`, grouping, conditional metrics, exact distinct counts,
  final result ordering/limits and derived expressions reuse the existing engines.
  With no match, every scoped parent participates, including empty contexts.
  Empty/all-null sums and averages remain null.
- Parent paging and incompatible top-level input forms fail explicitly. Related
  transformations stay inside `relation.sources`; no new data operation is added.
- Parent reads project required fields and use bounded batches or streaming.
  Related probe buffers are checked while decoding, including their memo storage.
  Resource accounting now includes decoded related buffers, so requests exceeding
  existing budgets fail explicitly. Cancellation returns no partial success.
- The caller continues to derive permissions and supply all scope/eligibility
  filters. A read-only batch can pair find and aggregate on the same snapshot.

See [READS.md](../READS.md) and the executable
[related-aggregate fixture](../testdata/related-aggregate.json).

## Validation and upgrades

SQLite, Pebble and AptevaDB pass the independent fixture: matching two parents
produces 60 points and a 50% average; blocked/out-of-cap entries produce zero
parents, an unscored parent retains a null average, and omitted match includes
all three authorised parents. Tests cover literal percent/underscore searches,
filter/dedup before the shared cap, request immutability, large projected rows,
resource limits, cancellation, concurrent writes and same-snapshot batches.

Full module and race suites, vet and standalone builds pass locally. Release
CI checks Linux and macOS. Existing native, pipeline and relational reads remain
covered. The manifest omits an optional read_only annotation unsupported by older local
server parsers; read-only permission checks are unchanged. No storage migration
or native data-format change is required; SDK
remains v0.115.0 and AptevaDB v0.2.0-alpha.18.
