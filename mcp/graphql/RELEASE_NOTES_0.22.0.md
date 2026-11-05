# GraphQL v0.22.0

Resolvers can now use Database's relational reads, so a report that joins linked
records, returns a bounded context per row, or counts interactions across call legs
can run as a published `database_pipeline`. Existing plans keep working, apart from the
changes listed under "Behaviour changes for existing plans". The README's "Joined
reads" section has the details.

## New step types

`related_read`, `joined_page` and `joined_aggregate` work inside a read batch. They
need Database 0.12.0; older versions refuse them at publish.

GraphQL keeps control of what a request can do. Collections, joins, output fields,
order and the context cap are fixed in the plan. Client arguments reach only the
filters that narrow a result, and a filter that decides eligibility can read identity
and access values but no client values. An `or` that mixes client values with access
values or fixed alternatives is refused. Publishing sends each step to Database with a
filter that matches nothing, so an unknown field, incompatible join keys, a missing
index, a fixed value of the wrong type or an unbounded relation fails the publish, and
the previous release keeps serving.

A joined read also refuses a plan that could hand out more than it means to:

- A root `where` with only conditional restrictions, since a viewer none apply to would
  read every record.
- A condition on a normal list, where a blank grant would read as no grant. Use
  `exact_string_list`.
- A client value before a `pick`, `rollup` or `distinct` by field, where it could change
  which row wins. A plan that means it, such as a report's date window, lists the param
  in the step's `stage_params`.

## Existing response shapes

Steps can now keep a field's existing contract:

- Offsets, including a limit of 0 and a negative offset, behave like SQL.
- A total can sit on every row.
- Values can be returned as JSON text, and decimal text as numbers.
- A grouped page flattens its legs while its total keeps counting interactions.
- A result can build a JSON text payload, and choose an empty one for viewers without
  access.
- A JSON list in a `String` argument, such as `centreIds`, can feed a list param.

## Cursors

A joined page's cursor is encrypted and bound to the project, resolver definition,
viewer, effective scope, filters and order. A client can't read order values or record
keys from it. Reusing it under anything else fails with `invalid_cursor`, and changed
grants end a held cursor. Cursors on `find` steps are unchanged.

## One view of grants and rows

A plan with a joined read re-reads its grants in the batch that reads its rows. If they
changed since its scope was resolved, it resolves the scope again and retries, then
fails with `access_changing` rather than return rows it can't confirm.

## Live updates

These apply to every Database subscription, not just the new step types:

- A commit replaces a running refresh, so a result read under old access is never
  delivered after one that reflects the change.
- To keep constant writes from starving delivery, that stops after 3 replacements in a
  row, or 16 for a grant or profile source. The refresh then completes and the next one
  starts at once.
- A subscriber's first read gives way to a newer refresh, including after a reconnect.
- A refresh only shares a read that started after the last change it covers.

## Behaviour changes for existing plans

- A value a client can narrow with `requested`, or one derived from it, is refused in
  an `access_when`, a `$if`, a relationship's `where`, `not_in`, `ne`, a `combine` input
  and an `expr` that decides on it. A shorter list could return more rows there. Plain
  positive filters are unchanged.
- `watch_collections` on a `database_pipeline` now only adds to the collections the
  plan reads. A list can no longer leave one out, including a grant source.
- A subscription watches what its active release reads, rather than the draft.
- Publishing refuses a subscription that depends on more than 16 collections.

## Limits

- Publish can't catch a read that only exceeds Database's work budget on real data, or
  a param whose type doesn't suit its field. Both fail at run time with no partial rows.
- An offset above 100000 and a negative limit fail the request.
- A computed `if` can't read client values, so a computed value that depends on a
  client's `now` needs another form.
- Token permissions are still checked at connection and token expiry only. Saved grants
  are re-read on every refresh.
- A subscriber connected across a publish keeps watching what the old release read until
  it reconnects.
- A grant row with a null value, or no row at all, counts as no grant. A rule written
  as "no grant means unrestricted" lifts when the last row goes.

This release adds one table, `graphql_cursor_keys`, for each project's cursor key.
