CREATE TABLE trading_observation_failures (
  project_id   TEXT    NOT NULL,
  portfolio_id INTEGER NOT NULL,
  request_key  TEXT    NOT NULL,
  failures     INTEGER NOT NULL CHECK (failures > 0),
  failed_at    INTEGER NOT NULL, -- Unix milliseconds.
  retry_at     INTEGER, -- Unix milliseconds. NULL blocks this snapshot request.
  last_error   TEXT    NOT NULL,
  PRIMARY KEY (project_id, portfolio_id)
);
