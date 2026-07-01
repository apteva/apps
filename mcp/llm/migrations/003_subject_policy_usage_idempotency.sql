-- LLM Gateway v0.3.0

PRAGMA foreign_keys=off;

ALTER TABLE policies RENAME TO policies_v02;

CREATE TABLE policies (
  id                     INTEGER PRIMARY KEY,
  project_id             TEXT NOT NULL DEFAULT '',
  subject_type           TEXT NOT NULL DEFAULT '',
  subject_id             TEXT NOT NULL DEFAULT '',
  allowed_models_json    TEXT NOT NULL DEFAULT '[]',
  blocked_models_json    TEXT NOT NULL DEFAULT '[]',
  allowed_providers_json TEXT NOT NULL DEFAULT '[]',
  limits_json            TEXT NOT NULL DEFAULT '{}',
  disabled               INTEGER NOT NULL DEFAULT 0,
  fallback_policy_json   TEXT NOT NULL DEFAULT '{}',
  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, subject_type, subject_id)
);

INSERT INTO policies (
  id, project_id, subject_type, subject_id, allowed_models_json, blocked_models_json,
  allowed_providers_json, limits_json, disabled, fallback_policy_json, created_at, updated_at
)
SELECT
  id, project_id, '', '', allowed_models_json, blocked_models_json,
  allowed_providers_json, limits_json, 0, fallback_policy_json, created_at, updated_at
FROM policies_v02;

DROP TABLE policies_v02;

ALTER TABLE usage_events ADD COLUMN provider_request_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS ux_usage_request_id
  ON usage_events(project_id, request_id)
  WHERE request_id != '';

PRAGMA foreign_keys=on;
