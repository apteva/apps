-- Existing policies retain their prefix; new policies receive random namespaces.
ALTER TABLE policies ADD COLUMN storage_id TEXT NOT NULL DEFAULT '';
UPDATE policies SET storage_id = 'policy-' || id;
CREATE UNIQUE INDEX ux_policies_storage_id ON policies(storage_id);
CREATE TABLE backup_queue (
 run_id INTEGER PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 policy_json TEXT NOT NULL,
 destination_json TEXT NOT NULL
);
CREATE INDEX ix_runs_history ON runs(started_at DESC, id DESC);
CREATE INDEX ix_runs_scope_status_history ON runs(scope_kind, scope_id, status, started_at DESC, id DESC);
CREATE INDEX ix_runs_scope_history ON runs(scope_kind, scope_id, started_at DESC, id DESC);
-- Normalize historical SQLite UTC timestamps for browser consumers.
UPDATE runs SET finished_at = strftime('%Y-%m-%dT%H:%M:%fZ', finished_at)
 WHERE finished_at IS NOT NULL AND instr(finished_at, 'T') = 0;
