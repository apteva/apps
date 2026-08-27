# seo

Generic SEO research workbench for Apteva. Track domains, keywords, rankings,
and backlinks; pull metrics from any provider behind one pluggable role.

## Schema (v0.7.0)

Twenty tables, grounded in the convergent shape across DataForSEO / Ahrefs / Moz and extended with generic search-engine entities:

- `seo_locations` — provider/search-engine/language/location catalog used to
  make every paid refresh locale-explicit
- `domains` — hostname identity (one row per host)
- `pages` — optional path under a domain (URL-level tracking)
- `domain_metrics` — `(domain, provider, ts)` snapshot
- `page_metrics` — `(page, provider, ts)` snapshot
- `keywords` — `(text, country_iso, language_iso)` identity
- `keyword_metrics` — `(keyword, provider, ts)` snapshot
- `keyword_volume_history` — monthly volume series, all three providers expose
  ~24 months of this inline so it gets its own table
- `keyword_metric_jobs` — resumable bulk refresh progress grouped by provider,
  search engine, and locale
- `keyword_metric_job_items` — per-keyword volume/difficulty checkpoints and
  retry state for each bulk job
- `rankings` — `(domain, keyword, ts) → rank, rank_url, device, serp_features`
- `ranking_observations` — successful domain ranking refreshes, including
  empty observations, used to distinguish current rows from retained history
- `backlinks` — `(domain, source_url, target_url) → anchor, follow flags,
  first_seen, last_seen, is_lost`
- `search_entities` — generic Google/YouTube entities such as domains, pages,
  channels, and videos
- `search_serp_snapshots` — cached paid SERP searches by search engine,
  keyword, locale, provider, and timestamp
- `search_serp_results` — ranked result rows linked to cached SERP snapshots
- `serp_tracking_settings` — project-level scheduler and monthly budget controls
- `serp_trackers` — opt-in keyword/target/device tracking configuration
- `serp_refresh_jobs` — deduplicated DataForSEO Standard Queue task lifecycle
- `serp_rank_observations` — durable scheduled rank or explicit not-found history

Every snapshot table carries a `raw_json` column that stores the unflattened
provider response, so provider-specific fields survive without schema churn.

## Status

v0.7.0 supports DataForSEO, YepAPI, or both through one provider-neutral adapter.
An installation may bind multiple providers and designate a default; paid MCP
tools and panel actions can select a specific provider. Provider locations,
metrics, rankings, backlinks, and SERP snapshots remain separately tagged.
Provider-sensitive cached reads use the configured default unless the caller
selects another provider; `provider: all` explicitly combines cached providers.
YepAPI keyword refreshes conditionally use its dedicated difficulty endpoint
when the general metrics response omits difficulty or reports zero.

Generic `search_engine` support covers Google and YouTube. Both engines
use the shared locale, keyword, SERP result, entity, and opportunity tooling;
Google additionally keeps domain metrics, tracked-domain ranking history, and
backlinks.
Keyword creation infers the engine from an explicit location. YepAPI YouTube
SERP requests fall back to its video-filtered YouTube search endpoint on
retryable provider failures while retaining the same stored result shape.
Location sync uses each provider's catalog strategy and always creates explicit
Google and YouTube country/language rows. Domain, keyword-metric, and backlink
refreshes remain UI/HTTP-driven; `serp_search` and refreshed keyword ideas are
explicit paid MCP actions.

The panel opens on a project overview and separates the primary workspaces into
Domains, Keywords, and Explorer. Domains are searchable and distinguish owned
sites from discovered competitors; rankings and backlinks live in dedicated
domain tabs. Provider selection, location catalogs, and activity are kept in
Settings because they configure refreshes rather than represent SEO data.

Cached backlink analytics use the provider-supplied `first_seen`, `last_seen`,
and current `is_lost` values already stored on each backlink. The
`backlink_movement` tool and domain panel derive daily gained/lost counts, net
movement, current active/lost totals, and timestamp coverage without creating
snapshots or calling the provider. For a lost link, `last_seen` is used as its
loss marker; the coverage fields make incomplete provider timestamps explicit.

The domain panel links to a paginated backlink detail view powered by the
cached-only `backlinks_browse` tool. It supports URL/anchor search and
active/lost and follow/nofollow filters without making provider requests.

Google keyword metric refreshes are HTTP/UI-only bulk jobs. DataForSEO requests
are grouped by locale and sent in batches of up to 1,000 keywords, with separate
volume and difficulty phases. The app checks account credit before starting,
retries rate limits with backoff, and resumes only missing fields after a
partial or interrupted run. SERP/ranking refreshes stay separate because they
have different provider costs.

Automatic Google rank tracking is opt-in and uses DataForSEO's asynchronous
Standard Queue. Each tracker can run daily, weekly, or monthly. The daily
policy checks the top 20 each day and top 100 on Sunday; weekly and monthly
trackers use the regular top-20 depth on each run. A configurable $5 monthly
cap applies across all schedules. Identical keyword/locale/device checks share
one provider task across targets. Full SERP snapshots remain bounded, while
compact rank and not-found observations are retained for long-term history and
charts.
