# Database 0.8.0

Released 4 November 2026. Source tag: `database/v0.8.0`.

Filters can test related records with `exists` and `not_exists`, and `not_in` joins the operators.

## Changes

- **Correlated `exists` and `not_exists`.** A record can now be filtered by records of another collection in the same database, without returning them to the caller. Lookups could not express this: they follow one single-field primary key and cannot state an exclusion across several related records.

  ```json
  {"and": [
    {"field": "centre_id", "op": "eq", "value": "centre-a"},
    {"exists": {"collection": "ventes",
      "on": [{"localField": "vente_id", "foreignField": "id", "comparison": "text"},
             {"localField": "local_id", "foreignField": "appel_id"}],
      "where": {"field": "offre_id", "op": "in", "value": ["offer-a"]}}},
    {"not_exists": {"collection": "ventes",
      "on": [{"localField": "vente_id", "foreignField": "id", "comparison": "text"},
             {"localField": "local_id", "foreignField": "appel_id"}],
      "where": {"field": "offre_id", "op": "not_in", "value": ["offer-a"]}}}
  ]}
  ```

  | Rule | Behaviour |
  |---|---|
  | `on` | Alternatives (OR), 1–16 pairs; composite keys are not supported |
  | Nulls | A null on either side never matches; empty strings match, as in SQL |
  | Comparison | Fields of one type by default; `"text"` compares text and integer fields by exact decimal text, with no trimming or other coercion |
  | `where` | Applies to the related record that matched an `on` pair |
  | Index | Each `foreignField` needs an index starting with it, or the primary key; otherwise `query_too_expensive` names the field |
  | Limits | 8 nodes per filter; nodes cannot nest |
  | Where allowed | `where` of `find`, `count`, `aggregate`, `explain` and read batches, before sorting, pagination and aggregation. Metric `where`, `update` and `delete` reject them |
  | Failures | Any evaluation failure fails the request; no partial result is returned |

  Each record counts once, however many related records match. Related records are read through the operation's own transaction. SQLite compiles a correlated subquery. Pebble and AptevaDB probe the related indexes and, on wide scans, switch to one pass over the related collection that builds key sets. That pass is bounded by a row allowance tied to the probes already spent and by the 16 MiB work budget; past the budget they keep probing.

- **`not_in`.** Takes the same 1–100 non-null values as `in`. A null value does not satisfy it, as in SQL, and a one-value list is treated as `ne`.

`TestCorrelatedExists` checks an offer-restricted policy, its parts and an `or` against a brute-force reference on all three adapters, through count, paged find, grouped aggregate and read batch. Pebble and AptevaDB also run with probes only, key sets at once and key sets over budget.

## Performance

`BenchmarkExistsPolicy`, AptevaDB, 125,000 calls and 35,000 sales, a policy of one `exists` and one `not_exists` over six relationships, Apple M-series. Timings vary by about a third between runs:

| Request | One centre, no policy | One centre (about 4,200 calls) | Two centres | All calls |
|---|---:|---:|---:|---:|
| `count` | under 0.1 ms | 450 ms | 360 ms | 390 ms |
| `find`, 50 rows | 0.2 ms | 300 ms | 410 ms | 550 ms |
| `aggregate` by centre | 1.3 ms | 280 ms | 380 ms | 390 ms |

With probes alone the whole table took about 15 s. Key sets are rebuilt per operation. An `in` scope of several values does not use the outer index on AptevaDB; an equality or one-value `in` does.

No storage change. AptevaDB stays on v0.2.0-alpha.15, format 10. Requests valid on 0.7.0 behave as before.
