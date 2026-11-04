# Database 0.10.0

Released 4 November 2026. Source tag: `database/v0.10.0`.

`exists` and `not_exists` can nest, so a filter can follow a chain of relationships without passing lists of IDs between calls.

## Changes

- **Nested `exists` and `not_exists`.** A clause's `where` can hold further clauses. Customers with an order that has a paid payment and no refunded payment:

  ```json
  {"exists": {"collection": "orders",
    "on": [{"localField": "id", "foreignField": "customer_id"}],
    "where": {"and": [
      {"exists": {"collection": "payments",
        "on": [{"localField": "id", "foreignField": "order_id"}],
        "where": {"field": "state", "op": "eq", "value": "paid"}}},
      {"not_exists": {"collection": "payments",
        "on": [{"localField": "id", "foreignField": "order_id"}],
        "where": {"field": "state", "op": "eq", "value": "refunded"}}}
    ]}}}
  ```

  | Rule | Behaviour |
  |---|---|
  | Local fields | Read from the immediate parent: above, `id` in the `payments` clauses is the order's |
  | Limits | 4 levels and 8 nodes across the whole filter |
  | Index | Every level needs one, or `query_too_expensive` names the fields |
  | Records | Each counts once, however many related records match at any level |
  | Paging | The whole filter applies before sorting, pagination and counting |
  | Snapshot | The whole tree reads the operation's one snapshot |
  | Memory | One 16 MiB key set budget across the tree; past it, that node keeps probing |
  | Work | Nested filters also stop at about 30 s of reads with `query_too_expensive`. Single-level filters keep only their deadline |
  | Failures | Any failure at any level fails the operation, including under `not_exists`, where a failed evaluation would otherwise read as a match. No partial result is returned, and a key set read past a failure is discarded |

  SQLite compiles nested correlated subqueries, each with its own alias. Pebble and AptevaDB evaluate the same predicate through probes or key sets, applying nested clauses in both.

`TestNestedExists` checks 12 filter shapes against a brute-force reference on all three adapters, through count, paged find, grouped aggregate and read batch: every combination of `exists` and `not_exists`, three levels, `or` inside a nested `where`, `anyOf` with text comparison, a field name shared across levels, and a cycle back to the first collection. Pebble and AptevaDB also run with probes only, key sets at once and key sets over budget. `TestNestedExistsFailureFailsClosed` covers failures.

## Performance

`BenchmarkNestedExists`, AptevaDB, 5,000 records, 125,000 related and 35,000 restricted, Apple M-series, 3 runs each:

| Request | `exists` of `exists` | `not_exists` of `exists` |
|---|---:|---:|
| `count`, one scope (about 156 records) | 62 ms | 67 ms |
| `find` 50 rows, one scope | 21 ms | 64 ms |
| `count`, all records | 285 ms | 278 ms |

Key sets are rebuilt per operation, so a page and a count in one batch each pay this. Single-level filters take the same time as before.

No storage change. AptevaDB stays on v0.2.0-alpha.17, format 10. Requests valid on 0.9.1 behave as before.
