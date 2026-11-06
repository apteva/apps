# Database 0.12.2

Makes `joined_page` faster on AptevaDB. Requests, responses and answers are unchanged.

## The problem

On a local collection of 125,865 wide records (109 fields, about 2.8 KB each), a page over 73,048 of them took about 1.5 s, and a page for one centre of 8,380 rows took about 0.9 s. Two things cost the most:

- The sort looked up each field by name on every comparison, and each lookup walked the collection's field list.
- The first pass read root records in batches of 1,000, which adds a keyset filter to every row. When the filter's index can't return primary key order, every batch also reads and sorts all the matches again, so the time grows with rows times batches.

## The change

- The sort resolves its fields once, on every adapter.
- On AptevaDB, the first pass reads the matching root records once, in whichever order costs least. `pick`, groups, legs and the page order all end in a complete tie-break, so the order rows arrive in can't change the answer.
- `distinct` keeps the first row it sees, so it still reads in primary key order, in batches.
- SQLite and Pebble keep their batches. So do `joined_aggregate` and `related_read`.

The first pass also no longer needs each batch of 1,000 root records to fit `aptevadb_query_memory_mib`, except with `distinct`.

## Measured

Warm reads on a clone of that collection, one sort field, no joins, alternating between the two builds at machine load 22 to 25. Times are the median of three rounds, in milliseconds.

| Page | Rows | 0.12.1 | 0.12.2 | Faster by |
|---|---|---:|---:|---:|
| All centres | 73,048 | 1,512 | 845 | 1.8× |
| All centres, offset 25 | 73,048 | 1,459 | 860 | 1.7× |
| All centres, `pick` | 73,048 | 1,116 | 706 | 1.6× |
| All centres, `distinct` | 73,048 | 1,609 | 1,206 | 1.3× |
| Top 3 centres, `in` | 22,897 | 711 | 244 | 2.9× |
| Largest centre | 8,380 | 900 | 81 | 11× |
| Third largest centre | 6,571 | 526 | 64 | 8.2× |
| Fifth largest centre | 2,912 | 100 | 27 | 3.7× |
| Seventeenth largest centre | 1,518 | 38 | 15 | 2.5× |

An index on the date alone used to make the all-centres page worse, because each of the 74 batches read all the matches again. On 0.12.1 that page hits the 30-second limit. On 0.12.2 it takes 0.8 to 1 s and returns the same answer.

## What hasn't changed

- `distinct` only gains the sort fix. With an index that can't return primary key order and a large match, it still reaches the 30-second limit, as it does on 0.12.1. In the test above, `distinct` over 73,048 rows behind the date index failed on both builds.
- The first read of a freshly opened database still has to bring the records into memory. It took 2.6 s in the check against Tables below, and between 2.3 and 8.1 s in earlier runs, on a machine short of free memory. This release doesn't try to change that.
- `joined_page` still reads every matching record on every page to keep its total exact, so bound it with `where`. Going well below 0.8 s for all centres would need a structural change, such as a narrower collection or a cached first pass. Neither has been tested.
- The 128 MiB read budget, the mapped cache setting, cursors and limits are as they were in 0.12.1.

## Verification

- **Against the previous releases.** 3,572 request shapes crossing joins, `distinct`, `pick`, grouping, order and filters, each read whole, by offset and by cursor, match the record from `database/v0.12.0` on all three adapters. A further 1,029 answers on AptevaDB match 0.12.1 exactly, on a fixture built so rows arrive in reverse order. `JOINED_PAGE_FULL=1` runs the 3,572.
- **The real collection.** All 14 page answers matched 0.12.1, including by cursor. With the date index added, the all-centres page and its cursor page gave the same answers as without it. Five cases, at one centre and at all centres, by offset and by cursor, matched Tables.
- **The packaged binary, on default settings.** 73,048 synthetic interactions on SQLite, Pebble and AptevaDB: offset, cursor, `pick` and a `distinct` across records all return the expected pages. On AptevaDB the first page took 0.30 s, one centre 0.01 s and a page behind a date index 0.33 s.
- **Upgrade from 0.12.1.** Data written by 0.12.1 reads correctly on all three adapters, and a cursor it issued resumes on 0.12.2.
- **Safety of reading during the scan.** Joins run inside the scan, alongside a concurrent writer, without a deadlock. Cancelling stops the read, and pages inside a read `batch` stay on one snapshot. The suite passes under the race detector.
- **The production path.** A test proves the single read runs through the wrapper the app really uses, not only the bare adapter.
- **Mutation testing.** 25 deliberate breaks to the change, each caught by a test.
- **GraphQL.** Its real-Database tests pass against this code on all three adapters, except one that needs realtime capture and so only runs on AptevaDB.

## Not measured

- A real Activity page with its joins. The collection above has one row per interaction and no joins, so several legs per interaction, `distinct` and `pick` over joins are proven on fixtures and not on these rows.
- Hosts other than this Mac, staging and production.
