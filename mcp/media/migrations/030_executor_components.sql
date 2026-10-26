-- The parent owns durable attempt IDs and results. A lost RPC never creates a
-- new render execution; its exact submitted plan is reconciled by job ID.
CREATE TABLE media_executor_calls (
  job_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  owner_key TEXT,
  request_json TEXT NOT NULL,
  receipt_json TEXT,
  grant_hash TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  UNIQUE(project_id, owner_key)
);
CREATE TABLE media_executor_grants (
  token_hash TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  max_bytes INTEGER NOT NULL,
  used_bytes INTEGER NOT NULL DEFAULT 0,
  reserved_bytes INTEGER NOT NULL DEFAULT 0,
  requests INTEGER NOT NULL DEFAULT 0,
  folders TEXT NOT NULL
);
CREATE TABLE media_executor_uploads (
  token_hash TEXT NOT NULL,
  kind TEXT NOT NULL,
  upload_id TEXT NOT NULL,
  PRIMARY KEY(token_hash, kind, upload_id)
);

-- An old externally running attempt has no component execution identity. Never
-- replay it on a new worker and mistake a different machine's empty workdir for
-- proof that the old process stopped. Drain old offloading before upgrading.
UPDATE renders SET status='failed',
  error='legacy_executor_cleanup_required: verify the previous offloaded process stopped before retrying',
  completed_at=CURRENT_TIMESTAMP
WHERE status='running' AND COALESCE(json_extract(metrics,'$.remote_host_id'),0)>0;
