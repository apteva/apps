-- A reader selection survives pause/resume and is independent from build state.
ALTER TABLE projection_definitions ADD COLUMN is_current INTEGER NOT NULL DEFAULT 0;
UPDATE projection_definitions SET is_current=1 WHERE status IN ('active','paused');
CREATE UNIQUE INDEX projection_current_name_idx ON projection_definitions(project_id,name) WHERE is_current=1;
ALTER TABLE projection_definitions ADD COLUMN options TEXT NOT NULL DEFAULT '{}';
ALTER TABLE projection_definitions ADD COLUMN storage_format INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_definitions ADD COLUMN latest_relevant_change INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_definitions ADD COLUMN published_change INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_definitions ADD COLUMN built INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_definitions ADD COLUMN published_at_ms INTEGER;
ALTER TABLE projection_definitions ADD COLUMN last_failure TEXT;
UPDATE projection_definitions SET latest_relevant_change=MAX(COALESCE((SELECT MAX(change_id) FROM projection_changes c JOIN projection_sources s ON s.table_id=c.table_id WHERE s.projection_id=projection_definitions.id),0),COALESCE((SELECT MAX(pending_change_id) FROM projection_queue q WHERE q.projection_id=projection_definitions.id),0),COALESCE((SELECT MAX(processed_change_id) FROM projection_scopes s WHERE s.projection_id=projection_definitions.id),0));
ALTER TABLE projection_queue ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE projection_queue ADD COLUMN due_at_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projection_queue ADD COLUMN forced INTEGER NOT NULL DEFAULT 0;
CREATE TABLE projection_indexes (
 projection_id INTEGER NOT NULL REFERENCES projection_definitions(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 columns_json TEXT NOT NULL,
 unique_index INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(projection_id,name)
);
-- Records invisible staging generations, including abandoned work after a restart.
CREATE TABLE projection_generations (
 projection_id INTEGER NOT NULL REFERENCES projection_definitions(id) ON DELETE CASCADE,
 generation TEXT NOT NULL,
 lease_token TEXT NOT NULL,
 created_at_ms INTEGER NOT NULL,
 PRIMARY KEY(projection_id,generation)
);

-- Keep per-event dependency capture and eligible-scope claiming indexed.
CREATE INDEX projection_sources_table_idx ON projection_sources(table_id,projection_id);
CREATE INDEX projection_queue_due_idx ON projection_queue(project_id,due_at_ms,projection_id);
