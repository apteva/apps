-- The parent owns durable renderer attempt IDs and results. A lost RPC never
-- creates a new render execution; its exact submitted plan is reconciled by
-- job ID, and a resumed render re-attaches through its owner key.
CREATE TABLE renderer_calls (
  job_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  owner_key TEXT,
  request_json TEXT NOT NULL,
  receipt_json TEXT,
  grant_hash TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  UNIQUE(project_id, owner_key)
);
-- Job-scoped Storage transfer capabilities. Only the SHA-256 of the grant is
-- stored; the grant itself exists only in the submitted plan.
CREATE TABLE renderer_grants (
  token_hash TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  max_bytes INTEGER NOT NULL,
  used_bytes INTEGER NOT NULL DEFAULT 0,
  reserved_bytes INTEGER NOT NULL DEFAULT 0,
  requests INTEGER NOT NULL DEFAULT 0,
  folders TEXT NOT NULL
);
CREATE TABLE renderer_uploads (
  token_hash TEXT NOT NULL,
  kind TEXT NOT NULL,
  upload_id TEXT NOT NULL,
  PRIMARY KEY(token_hash, kind, upload_id)
);

-- A render running on a removed Instances host has no component execution
-- identity. Never replay it on the renderer and mistake a different machine's
-- empty workdir for proof that the old process stopped.
UPDATE renders SET status='failed', phase='failed',
  error='legacy_executor_cleanup_required: verify the previous Instances render stopped before retrying',
  finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
WHERE status IN ('preparing','rendering') AND executor='remote';
