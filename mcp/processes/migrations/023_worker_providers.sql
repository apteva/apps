-- The provider a run's worker thread was pinned to, as last reported by the
-- platform. Only pinned workers have a row; everything else follows its agent.
CREATE TABLE process_worker_providers (
  run_id TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  pin TEXT NOT NULL,
  active TEXT NOT NULL DEFAULT '',
  missing INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (run_id, thread_id)
);
