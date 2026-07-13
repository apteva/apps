-- Durable per-rule UTC daily hit counters. The redirects.hits column remains
-- the all-time total; this table provides reconciliation without storing one
-- row per request.
CREATE TABLE redirect_daily_stats (
  rule_id     INTEGER  NOT NULL,
  project_id  TEXT     NOT NULL DEFAULT '',
  date        TEXT     NOT NULL, -- YYYY-MM-DD in UTC
  hits        INTEGER  NOT NULL DEFAULT 0 CHECK (hits >= 0),
  updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (rule_id, date),
  FOREIGN KEY (rule_id) REFERENCES redirects(id) ON DELETE CASCADE
);

CREATE INDEX ix_redirect_daily_stats_project_date
  ON redirect_daily_stats(project_id, date DESC);
