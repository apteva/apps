-- Mobile deployment targets reuse the existing environment -> build -> release
-- pipeline. Runtime-only columns (pid/port) remain zero for store releases.

ALTER TABLE deployments ADD COLUMN target_kind TEXT NOT NULL DEFAULT 'service';
ALTER TABLE deployments ADD COLUMN target_config_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE deployment_environments ADD COLUMN target_config_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE builds ADD COLUMN artifact_manifest_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE releases ADD COLUMN channel TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN provider TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN external_id TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN external_status TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN release_meta_json TEXT NOT NULL DEFAULT '{}';

CREATE INDEX ix_releases_mobile_status
    ON releases(provider, status, id)
    WHERE provider != '';
