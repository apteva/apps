# GraphQL v0.11.2

Fixes two configuration bugs in Database sources and lets a pipeline return
a list directly. Database >=0.6.5 is unchanged.

## Fixes

- **Lookups and buckets on plain Database resolvers.** `find`, `list`,
  `search` and `aggregate` resolvers dropped a configured `lookups` (and
  `aggregate` dropped `buckets`), so the joined record came back missing even
  though Database returned it directly. Both are now forwarded from resolver
  config, validated on publish (at most 4, known keys only, bucket units
  hour/day/week/month/year), and never taken from client arguments. A
  subscription on such a resolver also watches the lookup targets.
- **Strict `database_pipeline` validation.** Unknown keys and mistyped values
  were silently ignored, so an access read with `orderBy` and `limit` (for
  example "the latest grant") was accepted and read every matching record
  instead. Every level now rejects unknown keys: params, access, access
  values and reads, steps, metrics, lookups, buckets, combine and totals.
  Booleans must be Booleans, integers must be integral, metric ops must be
  Database's. Releases that relied on ignored keys now fail to publish with
  the offending key named; fix or remove it.

## Root result

`result` can be a single reference instead of an object:

```json
"result": "$steps.rows"
```

The field then returns that value itself, so an existing list field such as
`commercials: [PartnerCommercial!]!` keeps its shape. `$steps.<id>`,
`$steps.<id>.first`, `$combine.<id>` and `$scope.<name>` all work; the
object form is unchanged.

## Validation

Full Go suite, race suite and vet with `GOWORK=off`. New unit tests cover
every rejected option, root results of each kind, and that configured
lookups/buckets are forwarded while client-supplied ones are not (the test
fails on 0.11.1). `TestRealDatabaseQueryOptions` runs against the released
`database/v0.6.5` on AptevaDB: a plain `find` returns the looked-up agent, an
`aggregate` buckets calls by Paris day, a pipeline returns a list directly,
and publishing an access read with `orderBy` fails naming the key.
`TestRealDatabaseGuardedEdits` still passes.

SDK remains pinned to v0.99.0. No running installation or production
deployment is updated by this release.
