# GraphQL v0.11.1

Keeps Database's error codes and pins down the write guards. Requires
Database >=0.6.5 whenever Database sources are used.

## Database error codes

Database app-call errors now reach clients as `errors[].extensions.code`
with Database's own message, for resolvers and `database_pipeline` alike:
`version_conflict`, `invalid_argument`, `not_found`, `permission_denied`,
`unique_conflict`, `schema_conflict`, `resource_limit`,
`query_too_expensive`, `unsupported`, `realtime_disabled`,
`history_expired` and `resume_invalid`. A stale `ifVersion` edit is now
`version_conflict` instead of a generic `execution_error`, so a UI can show
"this record was changed by someone else". Unknown classes and transport
failures stay `execution_error`.

## Write guards

- A resolver's configured `where` is always sent to Database and ANDed with
  any client filter; a client filter can only narrow it.
- `all`, `scope`, `database` and `collection` stay resolver configuration.
- A configured `maxAffected` can be lowered by the client but not raised.
  Key edits and deletes without one are bounded to 1.
- Database 0.6.5 applies a key and a guard together: an edit outside the
  guard affects 0 records, or returns `version_conflict` with `ifVersion`.
  Database 0.6.4 rejected that combination, hence the new minimum.

## Validation

Full Go suite, race suite and vet with `GOWORK=off`.
`TestDatabaseMutationGuardCannotBeWeakened` proves the guard, bound and
server-owned arguments survive hostile client input.
`TestRealDatabaseGuardedEdits` runs against the released `database/v0.6.5`
on AptevaDB over real HTTP: authorised edit, stale revision
(`version_conflict` in `extensions.code`), cross-centre edits (plain, OR
filter, `all` with a bound), claims, missing keys, deletes, and a two-client
race with exactly one winner.

SDK remains pinned to v0.99.0; Tables >=0.2.16 is unchanged. No running
installation or production deployment is updated by this release.
