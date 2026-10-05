-- Release authorization is separate from execution state and survives restart.
ALTER TABLE process_step_runs ADD COLUMN released_at TEXT NOT NULL DEFAULT '';
CREATE TABLE process_run_advances (
 run_id TEXT NOT NULL REFERENCES process_runs(id),
 request_key TEXT NOT NULL,
 step_id TEXT NOT NULL REFERENCES process_step_runs(id),
 actor TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY (run_id, request_key),
 UNIQUE (step_id)
);

-- Notify other open run views when MCP releases queued work, even if the
-- executor is still busy and delivery has not changed yet.
DROP TRIGGER bus_step_changed;
CREATE TRIGGER bus_step_changed AFTER UPDATE ON process_step_runs
WHEN OLD.released_at IS NOT NEW.released_at OR OLD.state IS NOT NEW.state OR OLD.progress IS NOT NEW.progress OR OLD.output IS NOT NEW.output OR OLD.error IS NOT NEW.error OR OLD.definition_json IS NOT NEW.definition_json OR OLD.executor_json IS NOT NEW.executor_json OR OLD.required IS NOT NEW.required OR OLD.due_at IS NOT NEW.due_at OR OLD.execution_state IS NOT NEW.execution_state OR OLD.task_id IS NOT NEW.task_id
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,CASE WHEN OLD.state IS NOT NEW.state THEN 'step.state_changed' ELSE 'step.updated' END,json_object('step_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'released_at', NEW.released_at, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'execution_state', NEW.execution_state, 'from_state', OLD.state, 'to_state', NEW.state));
END;
