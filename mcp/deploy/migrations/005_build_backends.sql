-- Provider-neutral build execution.
--
-- Environments select where builds run. Each build snapshots that
-- configuration so an in-flight cloud job is not affected by later edits.
-- Existing deployments remain local without any operator action.

ALTER TABLE deployments ADD COLUMN build_backend TEXT NOT NULL DEFAULT 'local';
ALTER TABLE deployments ADD COLUMN build_backend_config_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE deployment_environments ADD COLUMN build_backend TEXT NOT NULL DEFAULT 'local';
ALTER TABLE deployment_environments ADD COLUMN build_backend_config_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE builds ADD COLUMN build_backend TEXT NOT NULL DEFAULT 'local';
ALTER TABLE builds ADD COLUMN build_backend_config_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE builds ADD COLUMN external_job_id TEXT NOT NULL DEFAULT '';
ALTER TABLE builds ADD COLUMN external_status TEXT NOT NULL DEFAULT '';
ALTER TABLE builds ADD COLUMN external_meta_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE builds ADD COLUMN release_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE builds ADD COLUMN release_options_json TEXT NOT NULL DEFAULT '{}';

CREATE INDEX ix_builds_external_pending
    ON builds(build_backend, status, id)
    WHERE build_backend != 'local' AND status IN ('pending', 'running');
