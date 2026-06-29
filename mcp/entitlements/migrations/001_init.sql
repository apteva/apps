-- Entitlements v0.1.0 — shared access grants and usage metering.

CREATE TABLE entitlement_grants (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,

  subject_type           TEXT    NOT NULL DEFAULT 'customer',
  subject_id             TEXT    NOT NULL,
  feature_key            TEXT    NOT NULL,

  status                TEXT    NOT NULL DEFAULT 'active',
  source_type            TEXT    NOT NULL DEFAULT 'manual',
  source_id              TEXT,

  starts_at              TIMESTAMP,
  expires_at             TIMESTAMP,
  revoked_at             TIMESTAMP,
  revoked_reason         TEXT,

  metadata               TEXT    NOT NULL DEFAULT '{}',
  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_grants_subject ON entitlement_grants(project_id, subject_type, subject_id, status);
CREATE INDEX ix_grants_feature ON entitlement_grants(project_id, feature_key, status);
CREATE INDEX ix_grants_source ON entitlement_grants(project_id, source_type, source_id);

CREATE TABLE entitlement_limits (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  subject_type           TEXT    NOT NULL DEFAULT 'customer',
  subject_id             TEXT    NOT NULL,
  feature_key            TEXT    NOT NULL,

  limit_type             TEXT    NOT NULL DEFAULT 'quota', -- boolean | count | quota | seats
  limit_value            INTEGER NOT NULL DEFAULT 0,
  reset_interval         TEXT,                             -- day | week | month | year | lifetime

  metadata               TEXT    NOT NULL DEFAULT '{}',
  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_limits_subject_feature ON entitlement_limits(project_id, subject_type, subject_id, feature_key);

CREATE TABLE usage_events (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  subject_type           TEXT    NOT NULL DEFAULT 'customer',
  subject_id             TEXT    NOT NULL,
  feature_key            TEXT    NOT NULL,
  quantity               INTEGER NOT NULL DEFAULT 1,
  idempotency_key        TEXT,
  metadata               TEXT    NOT NULL DEFAULT '{}',
  occurred_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_usage_idempotency ON usage_events(project_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL AND idempotency_key != '';
CREATE INDEX ix_usage_subject_feature ON usage_events(project_id, subject_type, subject_id, feature_key, occurred_at DESC);
