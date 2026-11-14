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
Followers remain the latest cached count regardless of the selected dates.
Their percentage represents balance growth **during** the selected dates, not
a comparison of two period totals. Social reads the most recent unsegmented
`followers` snapshot at or before each UTC boundary, at most 24 hours old, and
calculates `(end - baseline) / baseline * 100`. The ending boundary is midnight
after the inclusive end date; later snapshots cannot affect historical growth.
Daily follower series are excluded because providers may use them for new
followers rather than total balances. No interpolation or provider refresh is
performed. Missing/stale boundaries, one reused observation, and a zero
baseline leave the percentage unavailable, with a reason in the API and a
tooltip beside the sorted follower count. A confirmed unchanged positive
balance returns 0%; a fall to zero returns -100%.

Each follower metric includes `observed_at` for the latest count and a `growth`
object with status/reason, selected dates, boundary values and observation
timestamps, net change and comparable-account coverage. Its `change_percent`
is populated only when the comparison is valid. Aggregate growth divides
summed boundary balances for the same current-count contributors; it never
averages percentages or drops a contributor lacking history. A zero-baseline
member can participate when the aggregate baseline is positive. Current totals
remain available when aggregate growth is incomplete.

Missing
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
rows, current followers, 180 days of daily metrics and follower balance
snapshots at UTC boundaries. Follower examples include gains, losses and zero
growth. For example:

```sh
python3 scripts/seed-local-performance.py \
  --database ~/.apteva/apps/social/data/34/app.db \
  --project-id YOUR_LOCAL_PROJECT_ID
```

Add `--followers-only` to add/refresh just follower history on existing sample
accounts, preserving all other sample metric rows.

The command accepts only an existing local dev database below
`~/.apteva/apps/social/data`. Accounts have no credentials, connection ID 0 and
status `disconnected`, so workers skip them and they cannot publish. Their
cached metrics use the regular read path, including the disconnected status
label. The script returns the created account/profile IDs, preserves existing
rows, and safely refreshes only its own sample metrics when rerun. It is never
run automatically on installation or upgrade.
