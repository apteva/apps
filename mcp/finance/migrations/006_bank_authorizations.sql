CREATE TABLE bank_authorizations (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 connection_id INTEGER NOT NULL,
 bank_name TEXT NOT NULL,
 country TEXT NOT NULL,
 state TEXT NOT NULL UNIQUE,
 status TEXT NOT NULL,
 session_id TEXT NOT NULL DEFAULT '',
 expires_at INTEGER NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX bank_authorizations_connection ON bank_authorizations(project_id, connection_id, created_at);
