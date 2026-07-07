# seo

Generic SEO research workbench for Apteva. Track domains, keywords, rankings,
and backlinks; pull metrics from any provider behind one pluggable role.

## Schema (v0.4)

Thirteen tables, grounded in the convergent shape across DataForSEO / Ahrefs / Moz and extended with generic search-engine entities:

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
- `rankings` — `(domain, keyword, ts) → rank, rank_url, device, serp_features`
- `backlinks` — `(domain, source_url, target_url) → anchor, follow flags,
  first_seen, last_seen, is_lost`
- `search_entities` — generic Google/YouTube entities such as domains, pages,
  channels, and videos
- `search_serp_snapshots` — cached paid SERP searches by search engine,
  keyword, locale, provider, and timestamp
- `search_serp_results` — ranked result rows linked to cached SERP snapshots

Every snapshot table carries a `raw_json` column that stores the unflattened
provider response, so provider-specific fields (Ahrefs distribution buckets,
DataForSEO `pos_*` counts, Moz link-count forest) survive without schema churn.

## Status

v0.4 adds generic `search_engine` support for Google and YouTube. Google keeps
the existing domain/keyword workflow and v0.3.7 ranking-history fixes, while
YouTube uses the shared locale, keyword, SERP, entity, and opportunity tooling.
If DataForSEO's full YouTube location catalog is unavailable, sync seeds
YouTube locales from active DataForSEO Google locations so YouTube SERP refresh
still has explicit country/language rows. Refresh actions for expensive
provider calls remain UI/HTTP-driven.
