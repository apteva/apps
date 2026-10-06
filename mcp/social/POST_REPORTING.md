# Stored post reporting

`profile_list.post_count` and `profile_get.post_count` count stored post records
in that project and profile across all statuses. `target_count` counts their
per-account delivery entries across all statuses. A post sent to three accounts
counts as one post and three targets. A draft without targets counts as one
post and zero targets. These are not counts of successful publications.

## Retrieve a complete local profile history

Use the ID or slug returned by `profile_list`:

```json
{"profile": "hgv", "limit": 200, "offset": 0}
```

`post_list` retains its `posts` array and adds:

| Field | Meaning |
| --- | --- |
| `total` | All matching stored post records, before pagination |
| `total_targets` | Delivery entries for all matching posts, before pagination |
| `returned_targets` | Delivery entries in this page's `posts[].targets` |
| `limit` | Effective page size; default 50, MCP maximum 200, HTTP maximum 1000 |
| `offset` | Number of matching posts skipped; default 0 |
| `has_more` | Whether there is another page |
| `next_offset` | Offset for the next page, or `null` when finished |

While `has_more` is true, call again with `offset=next_offset` and exactly the
same filters. Keep the dataset unchanged during traversal; if posts are added,
deleted, or their lifecycle dates change, restart the traversal. Each response
reads its totals, posts, and targets from one database snapshot. Separate pages
are separate snapshots. Equal lifecycle dates are ordered by descending ID.

`profile_id` or `profile` filtering occurs **before** the page limit. A positive
`profile_id` takes precedence over the slug. Without a profile filter, the
result covers the current project, so other brands can fill the recent window.
Unknown profiles are rejected. Authenticated project context remains authoritative.

Optional `status` is an exact post-status filter. Optional `from` and `to` are
RFC3339 timestamps, inclusive at the start and exclusive at the end. They apply
to the effective lifecycle date: nonempty `published_at` for published/partial
posts, otherwise nonempty `schedule_at`, otherwise `created_at`. Totals use the
same filters as the page. For reconciliation with profile counts, omit status
and date filters and sum all pages' post and target entries.

Authenticated `GET /posts` supports the same filters and pagination through
query parameters. Existing clients can continue reading only `posts`.

## Coverage

These counts describe history currently stored in Social. They exclude deleted
records and platform history that was never imported. A complete local listing
does not prove that content or an account was never used over its lifetime.
Missing data and database failures must not be interpreted as zero use.
