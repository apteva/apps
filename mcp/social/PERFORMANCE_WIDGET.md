# Performance widget

The existing Performance widget lists each matching account at both half and
full width. Use its Profile, Platform, Accounts and Date range controls to
compare accounts. Column headings sort the account table. Configure the widget
to save default profile/account IDs, platform, date preset or custom dates, and
demo mode; changes made in the visible filter bar last for that mounted widget.

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

Both endpoints accept `demo_data=true`. This explicitly selects a separate
synthetic dataset with four labeled profiles and four accounts per profile:
Facebook, Instagram, TikTok and YouTube. All filters and date comparisons work
against that dataset. It creates no Social profiles, accounts, metric rows or
OAuth connections and cannot enter inbox, polling or publication workflows.
Demo rows do not link to real account pages. Demo mode is off by default and
can be enabled per widget for local demonstrations.
