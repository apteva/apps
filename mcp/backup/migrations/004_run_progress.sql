-- Durable progress makes long-running snapshots observable and lets startup
-- reconciliation distinguish an interrupted operation from an idle row.
ALTER TABLE runs ADD COLUMN stage TEXT NOT NULL DEFAULT '';

CREATE INDEX ix_runs_failed_history ON runs(status, started_at);
