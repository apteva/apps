# Changelog

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
