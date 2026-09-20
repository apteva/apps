CREATE TABLE IF NOT EXISTS graphql_releases (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  api_slug TEXT NOT NULL,
  environment TEXT NOT NULL,
  version INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'published',
  schema_version INTEGER NOT NULL,
  schema_hash TEXT NOT NULL,
  schema_sdl TEXT NOT NULL,
  sources_json TEXT NOT NULL,
  resolvers_json TEXT NOT NULL,
  security_json TEXT NOT NULL,
  limits_json TEXT NOT NULL,
  modules_json TEXT NOT NULL,
  checksum TEXT NOT NULL,
  created_at TEXT NOT NULL,
  published_at TEXT NOT NULL,
  UNIQUE(project_id, api_slug, environment, version)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_graphql_release_active
  ON graphql_releases(project_id, api_slug, environment)
  WHERE status='published';

CREATE INDEX IF NOT EXISTS idx_graphql_release_history
  ON graphql_releases(project_id, api_slug, environment, version DESC);

ALTER TABLE graphql_request_logs ADD COLUMN operation_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE graphql_request_logs ADD COLUMN api_release INTEGER NOT NULL DEFAULT 0;
ALTER TABLE graphql_request_logs ADD COLUMN response_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE graphql_request_logs ADD COLUMN row_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE graphql_request_logs ADD COLUMN resolver_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE graphql_request_logs ADD COLUMN source_timings_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE graphql_request_logs ADD COLUMN error_codes_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE graphql_request_logs ADD COLUMN authorization_scope TEXT NOT NULL DEFAULT '';
ALTER TABLE graphql_request_logs ADD COLUMN request_id TEXT NOT NULL DEFAULT '';

ALTER TABLE graphql_resolver_modules ADD COLUMN null_behavior TEXT NOT NULL DEFAULT 'propagate';
ALTER TABLE graphql_resolver_modules ADD COLUMN decimal_precision INTEGER NOT NULL DEFAULT 34;
ALTER TABLE graphql_resolver_modules ADD COLUMN decimal_scale INTEGER NOT NULL DEFAULT 12;
ALTER TABLE graphql_resolver_modules ADD COLUMN rounding_mode TEXT NOT NULL DEFAULT 'half_even';
ALTER TABLE graphql_resolver_modules ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC';
ALTER TABLE graphql_resolver_modules ADD COLUMN completeness TEXT NOT NULL DEFAULT 'complete';
