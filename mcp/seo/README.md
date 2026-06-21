# seo

Generic SEO research workbench for Apteva. Track domains, keywords, rankings,
and backlinks; pull metrics from any provider behind one pluggable role.

## Schema (v0.3)

Ten tables, grounded in the convergent shape across DataForSEO / Ahrefs / Moz:

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

Every snapshot table carries a `raw_json` column that stores the unflattened
provider response, so provider-specific fields (Ahrefs distribution buckets,
DataForSEO `pos_*` counts, Moz link-count forest) survive without schema churn.

## Status

v0.3 is the first usable release: locale-aware schema, DataForSEO location
sync, domain/keyword/backlink refresh via UI/HTTP only, cached read-only MCP
tools, and a dashboard panel with an interactive seed flow. Refresh actions are
not exposed as MCP tools.
