ALTER TABLE actors_runs ADD COLUMN queue_key TEXT NOT NULL DEFAULT '';
ALTER TABLE actors_runs ADD COLUMN queued_at TIMESTAMP;
ALTER TABLE actors_runs ADD COLUMN started_at TIMESTAMP;
CREATE INDEX ix_actors_runs_active_context ON actors_runs(queue_key)
  WHERE status='running' AND queue_key<>'';
ALTER TABLE actors_context_locks ADD COLUMN cleanup_pending INTEGER NOT NULL DEFAULT 0;
CREATE INDEX ix_actors_context_locks_context ON actors_context_locks(context_id, expires_at);
