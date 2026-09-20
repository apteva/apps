-- Processes 0.14: step runs are the public execution primitive. Replace the
-- historical task.* outbox triggers without removing existing audit records.
-- Normalize configuration written by pre-0.14 releases. Historical run rows
-- remain readable; only future assignments and procedure revisions are native.
UPDATE process_assignments
SET body_json=json_set(json_remove(body_json,'$.execution_mode'),'$.execution_mode','agent');
UPDATE process_versions
SET body_json=json_remove(body_json,'$.execution_mode');

DROP TRIGGER IF EXISTS bus_task_created;
DROP TRIGGER IF EXISTS bus_task_changed;
DROP TRIGGER IF EXISTS bus_approval_created;
DROP TRIGGER IF EXISTS bus_approval_ready;
DROP TRIGGER IF EXISTS bus_approval_resolved;
DROP TRIGGER IF EXISTS bus_task_delivery;
DROP TRIGGER IF EXISTS bus_task_timing;

CREATE TRIGGER bus_step_created AFTER INSERT ON process_step_runs
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'step.created',json_object('step_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state, 'to_state', NEW.state));
END;

CREATE TRIGGER bus_step_changed AFTER UPDATE ON process_step_runs
WHEN OLD.state IS NOT NEW.state OR OLD.progress IS NOT NEW.progress OR OLD.output IS NOT NEW.output OR OLD.error IS NOT NEW.error OR OLD.decision IS NOT NEW.decision OR OLD.definition_json IS NOT NEW.definition_json OR OLD.executor_json IS NOT NEW.executor_json OR OLD.required IS NOT NEW.required OR OLD.due_at IS NOT NEW.due_at OR OLD.execution_state IS NOT NEW.execution_state OR OLD.task_id IS NOT NEW.task_id
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,CASE WHEN OLD.state IS NOT NEW.state THEN 'step.state_changed' ELSE 'step.updated' END,json_object('step_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state, 'from_state', OLD.state, 'to_state', NEW.state));
END;

CREATE TRIGGER bus_step_approval_created AFTER INSERT ON process_step_runs
WHEN json_extract(NEW.definition_json,'$.kind')='approval' AND NEW.state IN ('ready','waiting') AND NEW.decision=''
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'step.approval_requested',json_object('step_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state));
END;

CREATE TRIGGER bus_step_approval_ready AFTER UPDATE ON process_step_runs
WHEN json_extract(NEW.definition_json,'$.kind')='approval' AND NEW.state IN ('ready','waiting') AND NEW.decision='' AND OLD.state NOT IN ('ready','waiting','running','blocked')
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'step.approval_requested',json_object('step_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state));
END;

CREATE TRIGGER bus_step_approval_resolved AFTER UPDATE ON process_step_runs
WHEN json_extract(NEW.definition_json,'$.kind')='approval' AND NEW.decision<>'' AND OLD.decision IS NOT NEW.decision
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'step.approval_resolved',json_object('step_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state));
END;

CREATE TRIGGER bus_step_delivery AFTER UPDATE ON process_step_runs
WHEN (CASE WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END) IS NOT (CASE WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END)
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'delivery.state_changed',json_object('step_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state, 'entity_kind', 'step', 'from_state', CASE WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END, 'to_state', CASE WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END));
END;

CREATE TRIGGER bus_step_timing AFTER UPDATE ON process_step_runs
WHEN OLD.start_at IS NOT NEW.start_at
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'step.updated',json_object('step_id',NEW.id,'run_id',COALESCE(NEW.run_id,''),'entity_revision',NEW.revision,'start_at',NEW.start_at,'due_at',NEW.due_at));
END;
