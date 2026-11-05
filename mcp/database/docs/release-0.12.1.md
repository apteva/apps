# Database 0.12.1

Fixes `joined_page` running out of its memory budget on small pages. Requests and responses are unchanged.

## The problem

`joined_page` kept every matching row of every item until it had sorted them, then applied the offset and limit and projected the selected fields. A page of 25 items selecting one field still held the whole match. On a local dataset of 73,048 interactions it failed with `resource_limit: relational read retained more than 128 MiB` at every offset.

## The change

- The first pass now keeps each item's order values, its tie-break and which rows belong to it, never the rows. That is enough for the exact total, the order, the offset and the cursor.
- A second pass on the same snapshot reads only the page's root records, runs them through the same joins, computed values and filters, and keeps the rows the first pass chose. `distinct` and `pick` don't run again: they decided their winners over the whole input, including items outside the page.
- `pick` keeps each winner's order values and identity instead of its whole row.
- Item overhead now counts toward the budget, and is released once the page is chosen, along with `distinct` keys and `pick` winners. The second pass spends the work budget too.

Totals, group order, leg order, offset and cursor behave as they did. Cursors issued by 0.12.0 stay valid.

## What the limit is now

- An item costs about 500 bytes. A page with no joins and rows of this width worked at 260,000 interactions and was refused at 280,000.
- Each join can also cache up to 16 MiB, a `distinct` remembers each key it has seen, and `pick` keeps each winner until the page is chosen. So a real page with joins reaches the limit sooner.
- A page's own rows count, so one group with too many rows still fails, even when the page holds only that group.
- The budget is an accounting estimate, not a cap on process memory. `joined_page` still reads the whole joined input on every page so its total stays exact, so bound it with `where`.

## Verification

- **The failing request, on a clone of the local dataset.** All centres at offset 0 and 25, and by cursor, return 25 interactions and the total of 73,048. Interactions, row IDs, order and total match Tables. Accounted memory is between 32 and 36 MiB. Heap growth during the read fell from about 410 MiB at the point of failure to about 60 MiB. Each page took about 1.5 s on a machine at load 10 to 20, and 3.7 s on the first read of a freshly opened clone.
- **The same size on all adapters.** 73,048 synthetic interactions at the real limit, 0.45 to 0.69 s per page. Run it with `DATABASE_PAGE_SCALE=1`.
- **Against this release's predecessor.** 3,572 request shapes crossing joins, `distinct`, `pick`, grouping, order and filters, each read whole, by offset and by cursor. Every answer matches what the `database/v0.12.0` tag gave, on SQLite, Pebble and AptevaDB. The record is in `engine/testdata`. A normal test run takes one case in twenty; `JOINED_PAGE_FULL=1` runs them all.
- **One snapshot.** Pages stay consistent while a writer replaces rows on every adapter.
- **Mutation testing.** 29 deliberate breaks to the change, each caught by a test.
- **GraphQL.** Its real-Database tests pass against this code on all three adapters, except one that needs realtime capture and so only runs on AptevaDB.

## Not measured

- A real Activity page with its joins. The dataset above has one row per interaction and no joins, so several legs per interaction, `distinct` and `pick` are proven on fixtures and not on these rows.
- Staging and production.
