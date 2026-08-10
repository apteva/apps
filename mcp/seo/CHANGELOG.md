# Changelog

## 0.4.11

- Added bulk Google keyword metric jobs grouped by provider locale, with up to
  1,000 keywords per DataForSEO request for volume and difficulty.
- Persisted per-keyword progress so interrupted, partial, and failed jobs can
  resume only missing volume or difficulty fields.
- Added account balance preflight, rate-limit retry with backoff, and explicit
  HTTP 402 and 429 responses for provider payment and throttling failures.
- Updated seed-and-refresh and keyword-list UI workflows to use bulk jobs and
  show progress, errors, and resume controls.

## 0.4.10

- Added the adaptive SEO app icon used by the registry.

## 0.4.9

- Fixed duplicate `keywords_add` and `domains_add` calls returning stale SQLite
  insert IDs instead of the canonical existing records.

## 0.4.8

- Unified `rankings_for_keyword` on the generic SERP result model for Google
  and YouTube, and added `rankings_for_keywords` for efficient batch reads.
- Replaced same-day domain ranking observations atomically so disappeared
  rankings no longer remain current.
- Limited opportunity and YouTube idea analysis to the latest snapshot per
  keyword and locale, with metric-aware Google opportunity scores.
- Added search-engine/location validation, strict YouTube video detection,
  residual legacy-row cleanup, supporting indexes, and 30-snapshot retention.

## 0.4.7

- Added an install migration that backfills keyword search-engine metadata,
  links existing YouTube SERP snapshots to keyword rows, and cleans legacy
  YouTube channel/playlist SERP rows.

## 0.4.6

- Changed YouTube SERP ingestion to store and return video results only.
  Channel and playlist rows from DataForSEO are skipped for normal SEO data.
- Simplified YouTube content opportunities back to the standard interface while
  preserving the video-only default.

## 0.4.5

- Classified YouTube SERP rows as videos, channels, or playlists instead of
  treating every YouTube result as a video.
- Defaulted YouTube keyword ideas and content opportunities to video results so
  channel names and playlists do not appear as video topics.
- Added an explicit `result_type` filter to `content_opportunities`; use
  `result_type: "all"` to inspect the complete mixed YouTube SERP.

## 0.4.4

- Fixed DataForSEO YouTube SERP refreshes by translating the app's generic
  `depth` argument to YouTube Organic's provider-specific `block_depth`
  parameter.

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
