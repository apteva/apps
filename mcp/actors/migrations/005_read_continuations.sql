CREATE TABLE actors_read_continuations (
 ref TEXT PRIMARY KEY, project_id TEXT NOT NULL, actor_id INTEGER NOT NULL,
 actor_revision INTEGER NOT NULL, operation TEXT NOT NULL, config_hash TEXT NOT NULL,
 payload_json TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'available',
 expires_at INTEGER NOT NULL, claimed_by INTEGER, source_run_id INTEGER NOT NULL
);
CREATE INDEX ix_actors_read_continuations_expiry ON actors_read_continuations(project_id,state,expires_at);
