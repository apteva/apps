# Performance widget

The existing Performance widget ranks matching accounts at both half and full
width. Its Profile, Platform, Date range and Accounts controls share one row
(scrolling horizontally on narrow widgets); Accounts opens a separate selection
panel. Column headings change the sort for the mounted widget.

Use the widget's **Configure** action for persisted SDK settings. Display lets
you choose the ranking metric, account limit (10 by default, configurable from
1 to 100), totals, graph, account table and filter bar independently. Turn off
Show totals and Show graph for an accounts-only view. Columns lets you choose
profile, platform, followers, views, impressions and interactions separately.
Filters saves the default platform, dates and graph metric; explicit profile
and account IDs and custom dates are advanced settings.

Accounts start sorted by the chosen ranking metric, descending. If that column
is hidden, ranking uses the first visible metric; with no metric columns, it
uses account names. The row limit applies **after** filters and sorting, and
never truncates the all-account totals or graph. The table shows how many rows
are displayed out of the matching set.

Views, impressions and interactions cover the selected complete UTC dates.
Custom dates are inclusive and may span 1–366 days, ending no later than
yesterday UTC. A small percentage next to the sorted metric compares it with
the previous equal period: green for gains, red for losses, neutral for zero.
There is no separate Change column. The badge moves when sorting another metric
and disappears for text sorting or missing/incomplete comparison evidence.
Followers are a current snapshot regardless of the selected dates. Missing
metrics stay unavailable, partial daily coverage is labeled, and a Pinterest
board selected with its parent remains excluded from aggregate totals.

Inactive accounts remain visible with a status label and their cached metrics.
The widget reads cached Social metrics; opening or filtering it never refreshes
a provider or publishes content. Its authenticated HTTP endpoints are:

- `GET /widgets/performance/options`: project-scoped profile, platform and
  account choices.
- `GET /widgets/performance`: `profile_id`, `account_ids` (comma separated),
  `platforms` (comma separated), and either `days=7|28|90` or both `start_date`
  and `end_date` in `YYYY-MM-DD` format.

The widget always reads the database; it has no demo toggle or generated-data
endpoint. For local development, `scripts/seed-local-performance.py` inserts
four ordinary profiles with Facebook, Instagram, TikTok and YouTube account
rows, current followers and 180 days of daily metrics. For example:

```sh
python3 scripts/seed-local-performance.py \
  --database ~/.apteva/apps/social/data/34/app.db \
  --project-id YOUR_LOCAL_PROJECT_ID
```

The command accepts only an existing local dev database below
`~/.apteva/apps/social/data`. Accounts have no credentials, connection ID 0 and
status `disconnected`, so workers skip them and they cannot publish. Their
cached metrics use the regular read path, including the disconnected status
label. The script returns the created account/profile IDs, preserves existing
rows, and safely refreshes only its own sample metrics when rerun. It is never
run automatically on installation or upgrade.
