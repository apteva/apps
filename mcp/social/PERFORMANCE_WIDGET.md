# Performance widget

The existing Performance widget lists each matching account at both half and
full width. Use its Profile, Platform, Accounts and Date range controls to
compare accounts. Column headings sort the account table. Configure the widget
to save default profile/account IDs, platform, date preset or custom dates;
changes made in the visible filter bar last for that mounted widget.

Views, impressions and interactions cover the selected complete UTC dates.
Custom dates are inclusive and may span 1–366 days, ending no later than
yesterday UTC. Change compares the selected visibility metric with the previous
equal period, and is unavailable when either period lacks complete coverage.
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
