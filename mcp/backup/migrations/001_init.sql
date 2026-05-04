-- Backup app v0.1 — destinations, policies, runs.
--
-- Single tenant: this app installs as scope:global, one per server.
-- No project_id partitioning because backups span the whole instance.

CREATE TABLE destinations (
  id              INTEGER PRIMARY KEY,
  name            TEXT    NOT NULL,
  kind            TEXT    NOT NULL,                       -- 'local' | 's3' | 'storage_app'
  config_json     TEXT    NOT NULL DEFAULT '{}',          -- shape depends on kind
  -- For S3-compatible destinations the actual credentials live in the
  -- platform's connections table (encrypted at rest, OAuth-aware).
  -- We just keep the id here; the runner fetches secrets at upload time
  -- via the SDK's GetConnection — bytes never touch backup.db.
  connection_id   INTEGER,
  enabled         INTEGER NOT NULL DEFAULT 1,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_destinations_name ON destinations(name);

CREATE TABLE policies (
  id               INTEGER PRIMARY KEY,
  name             TEXT    NOT NULL,
  schedule         TEXT    NOT NULL,                      -- cron expression, e.g. "0 3 * * *"
  destination_id   INTEGER NOT NULL REFERENCES destinations(id) ON DELETE CASCADE,
  retention_keep   INTEGER NOT NULL DEFAULT 14,           -- prune older runs after each success; 0 = keep forever
  enabled          INTEGER NOT NULL DEFAULT 1,
  -- jobs_id is the id returned by the jobs app when we registered this
  -- policy's cron. Cleared on policy delete + re-set on update.
  jobs_id          TEXT    NOT NULL DEFAULT '',
  created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_policies_dest ON policies(destination_id);

-- One row per backup run. Started rows whose finished_at is NULL after
-- a long time are presumed dead — the dashboard surfaces them as such.
CREATE TABLE runs (
  id                INTEGER PRIMARY KEY,
  policy_id         INTEGER REFERENCES policies(id) ON DELETE SET NULL,
  destination_id    INTEGER NOT NULL,
  destination_name  TEXT    NOT NULL,                     -- snapshotted in case the dest is later renamed/deleted
  started_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  finished_at       TIMESTAMP,
  status            TEXT    NOT NULL DEFAULT 'running',   -- 'running' | 'success' | 'failed'
  bytes_compressed  INTEGER NOT NULL DEFAULT 0,
  sha256            TEXT    NOT NULL DEFAULT '',
  remote_key        TEXT    NOT NULL DEFAULT '',          -- bucket key / file path / file id
  manifest_json     TEXT    NOT NULL DEFAULT '',          -- copy of the snapshot's manifest.json for forensic use
  error             TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX ix_runs_policy ON runs(policy_id, started_at DESC);
CREATE INDEX ix_runs_dest   ON runs(destination_id, started_at DESC);
CREATE INDEX ix_runs_status ON runs(status, started_at DESC);
