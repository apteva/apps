# Changelog

## 0.4.3

- Added a DataForSEO YouTube location fallback that seeds YouTube locales from
  active Google DataForSEO locations when the full YouTube location catalog is
  unavailable through the integration runner.
- Added a YouTube keyword `Refresh SERP` panel action that calls the generic
  `serp_search` tool and displays cached YouTube rankings.
- Guarded Google Ads keyword metric refresh so it does not run against
  YouTube keywords.

## 0.4.2

- Fixed DataForSEO Labs location parsing for the current
  `available_languages` response shape so country/language locales populate
  correctly.

## 0.4.1

- Fixed DataForSEO location sync so Google locations are committed even if a
  secondary search engine sync, such as YouTube, fails.
- Fixed generic SERP result saves by always reading back the canonical
  `search_entities` ID after upsert conflicts before inserting result rows.

## 0.4.0

- Added `search_engine`-based support for Google and YouTube, with Google as
  the default.
- Added generic search entities for Google domains/pages and YouTube
  channels/videos.
- Added cached SERP snapshots and generic entity rankings.
- Added YouTube discovery tools for SERP search, keyword ideas, and content
  opportunities.
- Added a dashboard search-engine selector with engine-specific views,
  filtered locales, discovery, entities, keywords, and locations.
- Extended DataForSEO location sync to import YouTube locations and languages
  into the shared `seo_locations` catalog.
- Preserved the v0.3.7 DataForSEO domain refresh and ranking-history fixes.

## 0.3.1

- Fixed DataForSEO live refresh calls by sending a top-level task array through
  the integration runner.
- DataForSEO catalog now uses Basic auth derived from login/password and marks
  the SEO app's paid live endpoints as task-array bodies.

## 0.3.0

- Added first-class SEO locations with explicit provider, search engine,
  location, country, and language metadata.
- Added DataForSEO location sync through the bound integration connection.
- Removed implicit US/en fallback from paid refresh paths.
- Stored `location_id` on domain, page, keyword, volume-history, and ranking
  metric rows.
- Added a dashboard SEO panel with seed, domains, keywords, and locations views.
- Kept paid refresh actions UI/HTTP-driven only; no MCP refresh tools are
  exposed.
- Updated the DataForSEO integration catalog with `locations_and_languages`.
- Bumped `github.com/apteva/app-sdk` to `v0.35.0`.
