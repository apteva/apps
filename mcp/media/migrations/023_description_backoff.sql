-- Shared provider/model cooldown survives restarts and protects later files in
-- the same batch. Switching models or connections starts an independent budget.
CREATE TABLE description_backoff (
  connection_id INTEGER NOT NULL,
  tool TEXT NOT NULL,
  model TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL,
  retry_info TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (connection_id, tool, model)
);
