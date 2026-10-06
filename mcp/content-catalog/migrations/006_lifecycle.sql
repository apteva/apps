-- Preserve every external reference. Lifecycle changes never move Storage bytes.
ALTER TABLE assets ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active' CHECK(lifecycle IN ('active','archived'));
ALTER TABLE assets ADD COLUMN archive_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE assets ADD COLUMN archived_at TEXT NOT NULL DEFAULT '';
ALTER TABLE assets ADD COLUMN original_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE assets ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE sessions ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active' CHECK(lifecycle IN ('active','archived'));
ALTER TABLE sessions ADD COLUMN archive_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN archived_at TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
UPDATE sessions SET lifecycle='archived',archive_reason='Legacy archived session',archived_at=updated_at WHERE status='archived';
CREATE INDEX ix_assets_lifecycle ON assets(project_id,lifecycle,session_id);
CREATE INDEX ix_sessions_lifecycle ON sessions(project_id,lifecycle,brand_id);
CREATE TABLE lifecycle_operations (
 project_id TEXT NOT NULL, operation_id TEXT NOT NULL, request_hash TEXT NOT NULL,
 result TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(project_id,operation_id)
);
CREATE TABLE lifecycle_events (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL, operation_id TEXT NOT NULL,
 entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL,
 before_json TEXT NOT NULL, after_json TEXT NOT NULL, reason TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX ix_lifecycle_history ON lifecycle_events(project_id,entity_type,entity_id,created_at);
