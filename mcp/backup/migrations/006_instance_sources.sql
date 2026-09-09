ALTER TABLE policies ADD COLUMN source_config TEXT NOT NULL DEFAULT '{}';
ALTER TABLE runs ADD COLUMN source_config TEXT NOT NULL DEFAULT '{}';
CREATE TABLE instance_operations (
  id TEXT PRIMARY KEY,
  run_id INTEGER NOT NULL,
  kind TEXT NOT NULL,
  instance_id INTEGER NOT NULL,
  config TEXT NOT NULL,
  token TEXT NOT NULL,
  target_port INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'pending',
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX instance_operation_run ON instance_operations(run_id,kind);
