-- An epoch for definition fields used by background workers. Runtime watermarks,
-- metrics and publication timestamps deliberately do not invalidate definitions.
CREATE TABLE projection_definition_epoch (
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  epoch INTEGER NOT NULL DEFAULT 0
);
INSERT INTO projection_definition_epoch(singleton) VALUES(1);
CREATE TRIGGER projection_definition_insert AFTER INSERT ON projection_definitions BEGIN
  UPDATE projection_definition_epoch SET epoch=epoch+1 WHERE singleton=1;
END;
CREATE TRIGGER projection_definition_delete AFTER DELETE ON projection_definitions BEGIN
  UPDATE projection_definition_epoch SET epoch=epoch+1 WHERE singleton=1;
END;
CREATE TRIGGER projection_definition_update AFTER UPDATE OF project_id,name,version,status,sql_text,source_tables,result_columns,scope_columns,result_table,options,is_current,storage_format ON projection_definitions
WHEN OLD.project_id IS NOT NEW.project_id OR OLD.name IS NOT NEW.name OR OLD.version IS NOT NEW.version OR OLD.status IS NOT NEW.status OR OLD.sql_text IS NOT NEW.sql_text OR OLD.source_tables IS NOT NEW.source_tables OR OLD.result_columns IS NOT NEW.result_columns OR OLD.scope_columns IS NOT NEW.scope_columns OR OLD.result_table IS NOT NEW.result_table OR OLD.options IS NOT NEW.options OR OLD.is_current IS NOT NEW.is_current OR OLD.storage_format IS NOT NEW.storage_format
BEGIN
  UPDATE projection_definition_epoch SET epoch=epoch+1 WHERE singleton=1;
END;
-- Retiring a version fences publication immediately, but an older sidecar can
-- still be finishing a staging batch. Preserve its lease until expiry for GC.
CREATE TABLE projection_retired_leases (
 projection_id INTEGER NOT NULL REFERENCES projection_definitions(id) ON DELETE CASCADE,
 lease_token TEXT NOT NULL,
 claimed_until TEXT NOT NULL,
 PRIMARY KEY(projection_id,lease_token)
);
INSERT INTO projection_retired_leases
 SELECT q.projection_id,q.lease_token,q.claimed_until FROM projection_queue q
 JOIN projection_definitions p ON p.id=q.projection_id
 WHERE p.status='retired' AND q.claimed_until>CURRENT_TIMESTAMP;
-- Published historical results remain readable. Retirement cancels queued work;
-- publication fencing prevents an in-flight retired worker from publishing.
DELETE FROM projection_queue WHERE projection_id IN (SELECT id FROM projection_definitions WHERE status='retired');
DELETE FROM projection_event_outbox WHERE projection_id IN (SELECT id FROM projection_definitions WHERE status='retired');
CREATE TRIGGER projection_retire_queue AFTER UPDATE OF status ON projection_definitions
WHEN NEW.status='retired' BEGIN
  INSERT OR REPLACE INTO projection_retired_leases SELECT projection_id,lease_token,claimed_until FROM projection_queue WHERE projection_id=NEW.id AND claimed_until>CURRENT_TIMESTAMP;
  DELETE FROM projection_queue WHERE projection_id=NEW.id;
  DELETE FROM projection_event_outbox WHERE projection_id=NEW.id;
END;
CREATE INDEX projection_worker_status_idx ON projection_definitions(status,project_id,id);
-- A previous sidecar may have computed invalidations before retirement. Ignore
-- its stale insertion without recreating work or failing unrelated consumption.
CREATE TRIGGER projection_queue_retired_guard BEFORE INSERT ON projection_queue
WHEN EXISTS(SELECT 1 FROM projection_definitions WHERE id=NEW.projection_id AND status='retired')
BEGIN
 SELECT RAISE(IGNORE);
END;
