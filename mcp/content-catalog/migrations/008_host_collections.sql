-- Reserve before creating a remote collection. An uncertain result must be
-- reconciled by observation, never by sending another create request.
CREATE TABLE host_collections (
 project_id TEXT NOT NULL,
 session_id TEXT NOT NULL REFERENCES sessions(id),
 provider TEXT NOT NULL,
 connection_id INTEGER NOT NULL,
 library_id TEXT NOT NULL,
 name TEXT NOT NULL,
 remote_id TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,session_id,provider,connection_id,library_id)
);
