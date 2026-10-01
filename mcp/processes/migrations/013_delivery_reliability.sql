-- Freeze tracked-event content before any network call. Keep old identities
-- for audit/duplicate protection, including after an explicit assignment.
CREATE TABLE process_delivery_envelopes (
 event_id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 request_json TEXT NOT NULL,
 spawn_json TEXT NOT NULL DEFAULT '',
 spawned INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL
);
ALTER TABLE process_step_runs ADD COLUMN delivery_suspended INTEGER NOT NULL DEFAULT 0;
ALTER TABLE process_runs ADD COLUMN delivery_suspended INTEGER NOT NULL DEFAULT 0;

-- Existing conflicting events cannot be safely reconstructed or assigned a
-- new id automatically: the original may already have executed remotely.
UPDATE process_step_runs SET delivery_suspended=1,next_attempt_at=''
WHERE delivered_at='' AND lower(delivery_warning) LIKE '%source event id already exists with different content%';
UPDATE process_runs SET delivery_suspended=1,next_attempt_at=''
WHERE workflow=0 AND delivered_at='' AND lower(delivery_warning) LIKE '%source event id already exists with different content%';
UPDATE process_runs SET delivery_suspended=1
WHERE workflow=1 AND EXISTS(SELECT 1 FROM process_step_runs s WHERE s.run_id=process_runs.id AND s.delivery_suspended=1 AND s.state NOT IN ('completed','failed','cancelled'));

-- A suspended delivery is not an automatic retry. Publish stable, truthful
-- state transitions; changes to attempt counts/backoff alone emit nothing.
DROP TRIGGER bus_run_delivery;
CREATE TRIGGER bus_run_delivery AFTER UPDATE ON process_runs
WHEN (CASE WHEN OLD.delivery_suspended=1 THEN 'suspended' WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END) IS NOT (CASE WHEN NEW.delivery_suspended=1 THEN 'suspended' WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END)
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES ((SELECT project_id FROM processes WHERE id=NEW.process_id),'delivery.state_changed',json_object('process_id', NEW.process_id, 'assignment_id', NEW.assignment_id, 'run_id', NEW.id, 'progress', NEW.progress, 'backend', NEW.backend, 'execution_state', NEW.execution_state, 'procedure_version', NEW.version, 'entity_kind', 'run', 'from_state', CASE WHEN OLD.delivery_suspended=1 THEN 'suspended' WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END, 'to_state', CASE WHEN NEW.delivery_suspended=1 THEN 'suspended' WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END));
END;

DROP TRIGGER bus_step_delivery;
CREATE TRIGGER bus_step_delivery AFTER UPDATE ON process_step_runs
WHEN (CASE WHEN OLD.delivery_suspended=1 THEN 'suspended' WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END) IS NOT (CASE WHEN NEW.delivery_suspended=1 THEN 'suspended' WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END)
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'delivery.state_changed',json_object('step_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'execution_state', NEW.execution_state, 'entity_kind', 'step', 'from_state', CASE WHEN OLD.delivery_suspended=1 THEN 'suspended' WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END, 'to_state', CASE WHEN NEW.delivery_suspended=1 THEN 'suspended' WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END));
END;

CREATE INDEX process_worker_projects ON processes(project_id,id);
CREATE INDEX process_active_runs ON process_runs(process_id,state);
CREATE INDEX process_outbox_project ON process_event_outbox(project_id,sequence,next_attempt_at);
CREATE INDEX process_trigger_project ON process_triggers(project_id);
