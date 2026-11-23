-- Workflows v0.5.0 — frozen definition versions + named stages.
--
--   workflow_versions — immutable snapshot of every definition the
--                       workflow has had (resolved source bytes +
--                       denormalised trigger). Written on create and
--                       on every source change; never updated.
--   workflow_stages   — named pointers (staging, production, …) at a
--                       version, with per-stage vars, an optional
--                       extra event filter and active/paused status.
--
-- A workflow with zero stage rows behaves exactly as before: events
-- and manual runs execute the live (head) definition. Once it has at
-- least one stage, events fan out to the active stages instead.

CREATE TABLE workflow_versions (
  id            INTEGER PRIMARY KEY,
  workflow_id   INTEGER NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  version       INTEGER NOT NULL,
  source        TEXT    NOT NULL,                 -- resolved source bytes (repo sources are frozen too)
  source_hash   TEXT    NOT NULL,
  trigger_kind  TEXT    NOT NULL DEFAULT 'manual',
  trigger_json  TEXT,
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(workflow_id, version)
);

-- Backfill: the current head of every existing workflow becomes its
-- first frozen version. Repo-backed rows have no inline bytes here;
-- they are skipped and get a snapshot on their next source update.
INSERT INTO workflow_versions (workflow_id, version, source, source_hash, trigger_kind, trigger_json, created_at)
SELECT id, version, source, source_hash, trigger_kind, trigger_json, COALESCE(updated_at, CURRENT_TIMESTAMP)
  FROM workflows
 WHERE source IS NOT NULL AND source <> '';

CREATE TABLE workflow_stages (
  id               INTEGER PRIMARY KEY,
  project_id       TEXT    NOT NULL,
  workflow_id      INTEGER NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  name             TEXT    NOT NULL,              -- ^[a-z0-9][a-z0-9-]{0,39}$
  version          INTEGER NOT NULL,
  previous_version INTEGER,
  vars_json        TEXT    NOT NULL DEFAULT '{}',
  filter_json      TEXT,                          -- {"when": "...", "source_install_id": N}
  status           TEXT    NOT NULL DEFAULT 'active', -- active | paused
  rev              INTEGER NOT NULL DEFAULT 1,
  created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(workflow_id, name)
);
CREATE INDEX ix_stages_proj_name ON workflow_stages(project_id, name);

-- Runs remember which stage (if any) they ran for, plus the stage
-- vars they saw so replay is deterministic.
ALTER TABLE workflow_runs ADD COLUMN stage TEXT;
ALTER TABLE workflow_runs ADD COLUMN stage_vars_json TEXT;
CREATE INDEX ix_runs_workflow_stage ON workflow_runs(workflow_id, stage, started_at DESC);
