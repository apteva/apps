# Database 0.8.0

Released 4 November 2026. Source tag: `database/v0.8.0`.

Filters can test related records with `exists` and `not_exists`, and `not_in` joins the operators.

## Changes

- **Correlated `exists` and `not_exists`.** A record can now be filtered by records of another collection in the same database, without returning them to the caller. Lookups could not express this: they follow one single-field primary key and cannot state an exclusion across several related records.

  Customers in one region with a paid order and no refunded order:

  ```json
  {"and": [
    {"field": "region", "op": "eq", "value": "emea"},
    {"exists": {"collection": "orders",
      "on": [{"localField": "id", "foreignField": "customer_id"}],
      "where": {"field": "status", "op": "eq", "value": "paid"}}},
    {"not_exists": {"collection": "orders",
      "on": [{"localField": "id", "foreignField": "customer_id"}],
      "where": {"field": "status", "op": "eq", "value": "refunded"}}}
  ]}
  ```

  A record that can relate in several ways uses `anyOf` in place of `on`:

  ```json
  {"exists": {"collection": "orders",
    "anyOf": [{"on": [{"localField": "id", "foreignField": "customer_id"}]},
              {"on": [{"localField": "tenant_id", "foreignField": "tenant_id"},
                      {"localField": "email", "foreignField": "billing_email"}]}]}}
  ```

  | Rule | Behaviour |
  |---|---|
  | `on` | Every pair must match (AND, as in SQL's `ON`), 1–8 pairs, so composite keys work |
  | `anyOf` | Replaces `on`: 1–16 alternatives (OR), each with its own `on` list. A clause takes exactly one of the two |
  | Nulls | A null on either side never matches; empty strings match, as in SQL |
  | Comparison | Fields of one type by default; `"text"` compares text and integer fields by exact decimal text, with no trimming or other coercion |
  | `where` | Applies to the related record that matched |
  | Index | Each `on` list needs one `foreignField` with an index starting with it, or the primary key; otherwise `query_too_expensive` names the fields |
  | Limits | 8 nodes per filter; nodes cannot nest |
  | Where allowed | `where` of `find`, `count`, `aggregate`, `explain` and read batches, before sorting, pagination and aggregation. Metric `where`, `update` and `delete` reject them |
  | Failures | Any evaluation failure fails the request; no partial result is returned |

  Each record counts once, however many related records match. Related records are read through the operation's own transaction. SQLite compiles a correlated subquery. Pebble and AptevaDB probe the related indexes and, on wide scans, switch to one pass over the related collection that builds key sets. That pass is bounded by a row allowance tied to the probes already spent and by the 16 MiB work budget; past the budget they keep probing.

- **`not_in`.** Takes the same 1–100 non-null values as `in`. A null value does not satisfy it, as in SQL, and a one-value list is treated as `ne`.

`TestCorrelatedExists` checks `exists` and `not_exists` over six alternatives, their combination and an `or` against a brute-force reference on all three adapters, through count, paged find, grouped aggregate and read batch. `TestExistsCompositeKeys` does the same for composite keys, including a null in one component and a mix of composite and single-field alternatives. Pebble and AptevaDB also run with probes only, key sets at once and key sets over budget.

## Performance

`BenchmarkExistsAndNotExists`, AptevaDB, 125,000 records and 35,000 related records, one `exists` and one `not_exists` with six alternatives each, Apple M-series. Timings vary by about a third between runs:

| Request | One scope value, no exists | One scope value (about 4,200 records) | Two scope values | All records |
|---|---:|---:|---:|---:|
| `count` | under 0.1 ms | 280 ms | 310 ms | 410 ms |
| `find`, 50 rows | 0.2 ms | 160 ms | 320 ms | 510 ms |
| `aggregate` by scope | 1.2 ms | 280 ms | 310 ms | 420 ms |

These filters read the related collection once per operation, so they cost far more than an indexed scope alone, and the cost grows with the size of the related collection and the number of alternatives. With probes alone the whole table took about 15 s. Key sets are rebuilt per operation. An `in` scope of several values does not use the outer index on AptevaDB; an equality or one-value `in` does. A composite key seeks through its first indexed pair, so index the most selective field.

No storage change. AptevaDB stays on v0.2.0-alpha.15, format 10. Requests valid on 0.7.0 behave as before.
