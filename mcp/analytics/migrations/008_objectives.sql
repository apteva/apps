-- Analytics v0.9.0 - measurable objectives over stored analytics events.

CREATE TABLE objectives (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT    NOT NULL,
  name         TEXT    NOT NULL,
  description  TEXT    NOT NULL DEFAULT '',
  owner_type   TEXT    NOT NULL DEFAULT ''
    CHECK (owner_type IN ('', 'user', 'agent', 'team')),
  owner_id     TEXT    NOT NULL DEFAULT '',
  status       TEXT    NOT NULL DEFAULT 'active'
    CHECK (status IN ('draft', 'active', 'paused', 'archived')),
  created_by   TEXT    NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  archived_at  INTEGER
);

CREATE INDEX ix_objectives_project_status
  ON objectives(project_id, status, updated_at DESC, id DESC);

CREATE TABLE objective_targets (
  id            INTEGER PRIMARY KEY,
  objective_id  INTEGER NOT NULL REFERENCES objectives(id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  metric_key    TEXT    NOT NULL DEFAULT 'custom',
  target_value  REAL    NOT NULL,
  unit          TEXT    NOT NULL
    CHECK (unit IN ('money', 'count', 'percent', 'number')),
  currency      TEXT    NOT NULL DEFAULT '',
  direction     TEXT    NOT NULL
    CHECK (direction IN ('at_least', 'at_most')),
  period_start  INTEGER NOT NULL,
  period_end    INTEGER NOT NULL,
  timezone      TEXT    NOT NULL DEFAULT 'UTC',
  query_json    TEXT    NOT NULL
    CHECK (json_valid(query_json)),
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  CHECK (period_end > period_start),
  CHECK (unit != 'money' OR length(currency) = 3)
);

CREATE INDEX ix_objective_targets_objective
  ON objective_targets(objective_id, period_start, period_end, id);
CREATE INDEX ix_objective_targets_metric
  ON objective_targets(metric_key, period_start, period_end);

-- This is a cache of the last Analytics query result. Query failures update
-- status/error while retaining the last good actual and measured_at so the UI
-- can distinguish stale data from zero.
CREATE TABLE objective_progress (
  target_id      INTEGER PRIMARY KEY REFERENCES objective_targets(id) ON DELETE CASCADE,
  actual_value   REAL,
  measured_at    INTEGER,
  status         TEXT    NOT NULL
    CHECK (status IN ('ok', 'error')),
  error          TEXT    NOT NULL DEFAULT '',
  details_json   TEXT    NOT NULL DEFAULT '{}'
    CHECK (json_valid(details_json)),
  updated_at     INTEGER NOT NULL
);
