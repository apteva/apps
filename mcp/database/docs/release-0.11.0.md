# Database 0.11.0

Released 5 November 2026. Source tag: `database/v0.11.0`.

Adds `contains_ascii`, a literal substring filter on one text field, so a search for `100%` finds a real percent sign.

## Changes

- **`contains_ascii`.** `{"field":"label","op":"contains_ascii","value":"100%"}` matches `instr(lower(coalesce(label,'')), lower(value)) > 0`. `%`, `_` and `\` are ordinary characters, ASCII `A–Z` fold case (other letters compare exactly), and null reads as an empty string. An empty value matches every record, including nulls.
- **Where it works.** `find`, `count`, filtered aggregates, a metric's `where`, `and`/`or`, and `exists` and `not_exists` clauses. It applies before paging and counting.
- **Validation.** The field must be `text` and the value a string of at most 1024 bytes.
- **AptevaDB v0.2.0-alpha.18.** Adds the literal form of `$like` that this compiles to.

`like_contains_ascii` is unchanged, and callers that want wildcards keep using it.

`contains_ascii` never selects an index, so keep an indexed scope beside it on large collections.

No storage change: AptevaDB format 10. Requests valid on 0.10.1 behave as before.
