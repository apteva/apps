# GraphQL v0.20.0

Access values can now keep identifiers exactly as stored. Until now they were
always trimmed, with blanks dropped, so a stored grant of `[""]` became `[]` and
could select a plan's no-grant fallback. A plan opts in per value, and plans that
don't opt in behave as before.

## Exact access values

```json
{"grantOffers": {"type": "exact_string_list", "optional": true,
                 "otherwise": {"collection": "grants", "field": "offer_ids"}}}
```

With `type: "exact_string_list"`:

- Leading and trailing spaces and empty strings are kept.
- Only exact duplicates are removed.
- Numbers still become strings.
- `[]`, `[""]` and `["   "]` stay different, so a blank grant is not an empty one.
- A blank or padded identifier still matches a record that really holds it.

This holds for reads, distinct reads, `union`, `intersect` and `expr` results. The
type is resolver configuration, and clients can't set it.

## Requested values

`requested` on an exact value must name an `exact_string_list` param, and a
default value must still name a `string_list` param. Each requested identifier
has to be in the caller's access exactly as sent. `""` asks for that identifier,
so it's refused unless the caller holds it; it doesn't mean nothing was requested.

## A new publish check

A `union`, `intersect` or `expr` that reads an exact value must be exact too, or
publishing is refused. Otherwise it would normalise the values again and could
turn `[""]` back into `[]`.

An exact value can read a default one, which stays normalised, so values the
policy deliberately trims can keep that. A default read can still filter on an
exact value.

## What doesn't change

- Plans without the type, including their trimming and blank handling.
- Identity verification and permission-claim normalisation.
- Source errors, scope limits, cursors and subscription re-authorisation.

## Known limits

- Inside an `expr`, `eq` and `in` still compare numeric-looking strings as numbers,
  so `"01"` equals `"1"` there. `intersect` and `requested` compare exactly.
- How a grant stored as JSON null, a scalar or an object behaves is unchanged, and
  isn't covered by this release.
