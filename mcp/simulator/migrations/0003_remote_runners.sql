-- Optional remote execution through the Instances app.
-- Existing sims remain local and keep their public ids. Remote sims use an
-- opaque public id while backend_id carries the host-native AVD name / UDID.

ALTER TABLE sims ADD COLUMN runner_kind TEXT NOT NULL DEFAULT 'local';
ALTER TABLE sims ADD COLUMN instance_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sims ADD COLUMN backend_id TEXT NOT NULL DEFAULT '';

UPDATE sims SET backend_id = id WHERE backend_id = '';

CREATE INDEX ix_sims_runner ON sims(runner_kind, instance_id, platform);
CREATE UNIQUE INDEX ux_sims_backend
  ON sims(project_id, runner_kind, instance_id, platform, backend_id);

ALTER TABLE sim_runs ADD COLUMN artifact_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sim_runs ADD COLUMN runner_kind TEXT NOT NULL DEFAULT 'local';
ALTER TABLE sim_runs ADD COLUMN instance_id INTEGER NOT NULL DEFAULT 0;

CREATE TABLE simulator_hosts (
  instance_id       INTEGER PRIMARY KEY,
  instance_name     TEXT NOT NULL DEFAULT '',
  worker_version    TEXT NOT NULL DEFAULT '',
  worker_port       INTEGER NOT NULL,
  worker_token      TEXT NOT NULL,
  capabilities_json TEXT NOT NULL DEFAULT '{}',
  status            TEXT NOT NULL DEFAULT 'unknown',
  last_seen_at      TIMESTAMP,
  error             TEXT NOT NULL DEFAULT ''
);
