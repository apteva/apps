# seo

Generic SEO research workbench for Apteva. Track domains, keywords, rankings,
and backlinks; pull metrics from any provider behind one pluggable role.

## Schema (v0.5)

Sixteen tables, grounded in the convergent shape across DataForSEO / Ahrefs / Moz and extended with generic search-engine entities:

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

Every snapshot table carries a `raw_json` column that stores the unflattened
provider response, so provider-specific fields survive without schema churn.

## Status

v0.5.1 supports DataForSEO, YepAPI, or both through one provider-neutral adapter.
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

Google keyword metric refreshes are HTTP/UI-only bulk jobs. DataForSEO requests
are grouped by locale and sent in batches of up to 1,000 keywords, with separate
volume and difficulty phases. The app checks account credit before starting,
retries rate limits with backoff, and resumes only missing fields after a
partial or interrupted run. SERP/ranking refreshes stay separate because they
have different provider costs.
