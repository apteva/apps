# Changelog

## 0.7.1

- Preserve paid rank-tracking costs through queue submission, failed tasks,
  and collection so monthly budget accounting retains committed spending.
- Keep empty latest SERPs authoritative and select current snapshots separately
  for each provider and locale.
- Validate bulk keyword-refresh provider bindings and account credit before
  creating jobs, avoiding permanently pending jobs after rejected requests.
- Cancel obsolete panel reads when selections change and defer hidden detail
  requests until their workspace is opened.
- Preserve configured domain refresh locales and split page ranking reads
  into batches of at most 200 keyword IDs.
- Aggregate cached backlink summaries through a covering SQLite index; a local
  100,000-link benchmark improved from 67.7 ms to 22.4 ms per summary.
- Update app-sdk to v0.76.0 and add backend and panel regression coverage.

## 0.7.0

- Replaced the seed-first panel with a project overview showing tracked sites,
  competitors, keywords, opportunities, quick workflows, and recent activity.
- Added a searchable domain explorer with separate Sites, Competitors, and All
  views plus focused Overview, Rankings, and Backlinks tabs for each domain.
- Consolidated research and tracked SERP results under Explorer, with search
  engine controls shown only inside workflows where they apply.
- Moved DataForSEO, YepAPI, locations, and activity into Settings so refresh
  infrastructure no longer competes with the application's primary navigation.
- Kept paid refresh actions explicit while making cached data browsing and
  backlink history easier to discover.

## 0.6.3

- Added a dedicated backlink detail view from each domain's movement panel.
- Added cached pagination, source/target/anchor search, active/lost filters, and
  follow/nofollow filters through the read-only `backlinks_browse` tool.
- Shows complete source and destination links, link attributes, authority,
  provider, status, and first/last-seen dates without making provider requests.

## 0.6.2

- Added the read-only `backlink_movement` tool with configurable 1-730 day
  gained/lost buckets, active/lost totals, net movement, and timestamp coverage.
- Added cached backlink movement cards, a 30-day trend chart, and recent active
  and lost link lists to the domain panel.
- Derived all movement from the existing provider-supplied `first_seen`,
  `last_seen`, and `is_lost` fields. No snapshots, migrations, or additional
  provider calls are introduced.

## 0.6.1

- Added per-tracker daily, weekly, and monthly automatic rank refresh choices.
- Kept the existing daily policy of top-20 checks with a top-100 Sunday scan;
  weekly and monthly trackers use the regular top-20 depth on each run.
- Made cadence changes reschedule immediately without deleting rank history.
- Updated the monthly budget estimator for DataForSEO's discounted additional
  SERP pages.

## 0.6.0

- Added opt-in daily Google rank tracking through app-sdk workers and
  DataForSEO's lower-cost Standard Queue.
- Added a configurable monthly spend cap, top-20 daily checks, and top-100
  Sunday checks with deterministic scheduling jitter.
- Deduplicated provider work by keyword, locale, device, provider, and day so
  multiple tracked targets share one paid SERP request.
- Added durable compact observations for both ranked and explicit not-found
  results while retaining the existing bounded full-SERP snapshot policy.
- Added rank tracking, budget, status, and historical views to the SEO panel
  plus read-only MCP tools for tracker status and history.
- Preserved v0.5.1 provider isolation and v0.4.11 resumable bulk keyword metric
  jobs alongside the new rank-tracking workers.

## 0.5.1

- Made every provider-sensitive cached read use the configured default provider;
  callers can request `provider: all` for an intentional cross-provider view.
- Made keyword creation infer Google or YouTube from an explicit location while
  returning HTTP 400 for an explicitly conflicting search engine.
- Preserved provider 5xx status codes and retry hints instead of collapsing them
  into generic HTTP 500 responses.
- Enriched missing or zero YepAPI keyword difficulty through its dedicated
  difficulty endpoint while avoiding the extra paid call for nonzero scores.
- Added a video-only `youtube_search` fallback when YepAPI's YouTube SERP route
  returns a retryable server error, including normalization of its camel-case
  video metadata.
- Kept the panel's provider selector usable by explicitly loading all provider
  locations while provider-specific views remain isolated.

## 0.5.0

- Added a provider-neutral execution adapter with complete DataForSEO and
  YepAPI implementations for locations, keyword metrics, keyword ideas,
  Google and YouTube SERPs, domains, ranked keywords, and backlinks.
- Enabled multiple SEO provider bindings with a designated default and an
  optional `provider` argument on provider-sensitive MCP tools.
- Added provider selection to the panel and provider-scoped locale, keyword,
  metric, ranking, backlink, and history reads.
- Added YepAPI locale seeding for Google and YouTube and response-contract
  tests for metrics, trends, SERPs, locations, and ranked keywords.
- Preserved resumable DataForSEO bulk keyword metric jobs, account preflight,
  rate-limit retries, and explicit HTTP 402/429 responses.

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
