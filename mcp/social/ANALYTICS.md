# Social analytics

`account_metrics` and `post_metrics` share reporting metadata. All reporting options are optional. The publishing destination model is
described in [DESTINATIONS.md](DESTINATIONS.md).

## Scopes

- `provider_account`: the upstream account. For Pinterest this is the entire
  Pinterest account, regardless of which board was selected in Social.
- `destination`: the connected publishing destination. For Pinterest this is
  one board. Providers whose destination is their account use the same identity
  for both scopes.
- `post`: one published target (Pin, video, Reel, etc.).

Existing Pinterest calls without `scope` retain whole-account behavior and now
label it explicitly. The dashboard starts with account scope; select destination scope for board reports.
Multi-account results deduplicate whole-account identities and include
`account_aliases` mapping additional Social account IDs to the returned account.

## Board report example

```json
{
  "social_account_id": 42,
  "scope": "destination",
  "range": "28d",
  "group_by": ["post"],
  "sort_by": "outbound_clicks",
  "limit": 20,
  "compare": true
}
```

Use `start_date` and `end_date` together for custom inclusive UTC dates.
`published_in_range=true` limits the inventory to posts created during the
selected window. Without it, the report measures activity during the window
across all discovered current posts, including older posts.

Pinterest lists the board's Pins with bookmarks and deduplicates IDs, then
requests analytics in batches of up to 100. The provider currently marks batch
analytics as restricted beta. A 403/404 switches to individual Pin analytics,
with a maximum of 50 fallback requests per period. Inventory is bounded to
1,000 Pins by default (`max_posts`, maximum 1,000) and 20 listing pages. Provider
429 and 5xx errors stop subsequent requests for that period; Social does not
retry them in a tight loop. A stopped current period defers comparison.

Pinterest requires the start date to be within the past 90 days. Unsupported
windows and preceding comparison periods are reported explicitly, never
silently shortened. Comparisons reuse the same current inventory. Publication
cohort comparisons use separate cohorts and omit change percentages.

`post_rows` contains the ranked posts, their metric availability, and individual
report windows. Other adapters can also rank up to 20 locally known published
posts; this is explicitly labelled as an incomplete provider inventory.

## Accuracy and coverage

Every report identifies `scope`, `entity_id`, a deduplication `identity`,
`source`, `window` and `metric_info`:

- `range`: values for the reported date range.
- `lifetime`: lifetime totals; a date filter did not change these values.
- `snapshot`: current profile totals or the latest observation.

`period_metrics` and `period_window` expose additive period performance separately
from compatibility profile fields. For example, YouTube channel `views` stays
lifetime while `period_metrics.views` comes from the date-filtered daily report.
Supported adapters compare these totals against the preceding period.

Each metric declares its time semantics and whether it is additive. Board
aggregation sums additive counts, never unique reach, followers, rates or
averages. `impressions`, `video_views`, `pin_clicks` and `outbound_clicks` remain
separate. Pinterest's legacy `clicks` field aliases outbound clicks and is not
silently replaced with Pin clicks. It is hidden from new displays to avoid
counting it twice. Pinterest impressions are no longer labelled as views.

`available` lists returned metrics, including genuine zero values. Omitted
metrics mean unavailable, not zero. Calculated reports include `coverage` with
inventory type, discovered/included/failed counts, per-metric post counts,
completeness, truncation and limitations. Daily points include coverage counts
and flag incomplete or estimated data. Processing/unavailable days are omitted.
Partial series never enter the home widget's daily totals.

Board reports describe **current** membership. Deleted or moved Pins, historical
membership, and lifetime completeness cannot be reconstructed from this API.
They are calculated reports, not a native Pinterest board analytics endpoint.

Known video Pins can contribute optional video view totals when batch analytics
is available. Missing video metrics do not become zero or invalidate image
metrics. Provider capability metadata describes scopes, labels, date support,
metrics and batch limits, so shared consumers need no Pinterest-only routes.

## Caching and compatibility

Results are cached for up to four hours; provider failures are cached for five
minutes to avoid repeated background requests. `refresh=true` bypasses the cache.
Cache keys include project, account or target, provider binding/identity, scope,
dates, filters, inventory limit, cohort and comparison options. Sort and display
limit operate on a copy of the cached inventory and do not fetch it again.
Reconnecting to another destination invalidates the cached binding.

`post_metrics` accepts the existing `post_id`, or optional `post_ids` (maximum
20), plus date options and `refresh`. Existing single-post responses are retained.
Without dates, the existing post window remains 30 days. Adapters that only
return lifetime post totals say so explicitly. Account tools retain their
existing fields; new report metadata disambiguates their meaning.

Migration 025 creates the post report cache and relabels existing Pinterest
whole-account history as `provider_account`, preventing old totals from being
used as board or home-widget performance. No server, SDK implementation or
integration catalog changes are required.

## Account connections and child destinations

Pinterest is connected once per native account. Board reports can select an
explicit `destination_id` independently of the publishing default. Changing a
default cannot reuse another board's cached report or mix its time series. See
[DESTINATIONS.md](DESTINATIONS.md) for the shared native/provider contract and
legacy board preset compatibility.
