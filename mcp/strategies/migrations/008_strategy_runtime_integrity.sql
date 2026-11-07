-- Pin executable strategy definitions for assignments and backtests.

CREATE TABLE strategy_versions (
  strategy_id     INTEGER NOT NULL REFERENCES strategies(id) ON DELETE CASCADE,
  version         INTEGER NOT NULL,
  definition_json TEXT    NOT NULL,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (strategy_id, version)
);

INSERT INTO strategy_versions (strategy_id, version, definition_json, created_at)
SELECT id, version, definition_json, updated_at
  FROM strategies;

ALTER TABLE portfolio_strategy_assignments
  ADD COLUMN strategy_version INTEGER NOT NULL DEFAULT 1;

UPDATE portfolio_strategy_assignments
   SET strategy_version = COALESCE(
     (SELECT version FROM strategies WHERE strategies.id = portfolio_strategy_assignments.strategy_id),
     1
   );

CREATE INDEX ix_strategy_assignments_version
  ON portfolio_strategy_assignments(strategy_id, strategy_version, status);
