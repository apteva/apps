-- Processes 0.16: procedure steps are generic. Review or approval behavior is
-- expressed in ordinary step instructions and executed through agent tools;
-- Processes no longer owns a step type or decision state.
DROP TRIGGER IF EXISTS bus_step_created;
DROP TRIGGER IF EXISTS bus_step_changed;
DROP TRIGGER IF EXISTS bus_step_approval_created;
DROP TRIGGER IF EXISTS bus_step_approval_ready;
DROP TRIGGER IF EXISTS bus_step_approval_resolved;
DROP TRIGGER IF EXISTS bus_step_delivery;

DELETE FROM process_event_outbox
WHERE topic IN ('step.approval_requested','step.approval_resolved','task.approval_requested','task.approval_resolved');

UPDATE process_versions SET body_json=json_remove(
 body_json,
 '$.steps[0].kind','$.steps[1].kind','$.steps[2].kind','$.steps[3].kind','$.steps[4].kind',
 '$.steps[5].kind','$.steps[6].kind','$.steps[7].kind','$.steps[8].kind','$.steps[9].kind',
 '$.steps[10].kind','$.steps[11].kind','$.steps[12].kind','$.steps[13].kind','$.steps[14].kind',
 '$.steps[15].kind','$.steps[16].kind','$.steps[17].kind','$.steps[18].kind','$.steps[19].kind',
 '$.steps[20].kind','$.steps[21].kind','$.steps[22].kind','$.steps[23].kind','$.steps[24].kind',
 '$.steps[25].kind','$.steps[26].kind','$.steps[27].kind','$.steps[28].kind','$.steps[29].kind'
);
UPDATE process_step_runs SET definition_json=json_remove(definition_json,'$.kind'),decision='';
UPDATE process_step_events SET decision='';

CREATE TRIGGER bus_step_created AFTER INSERT ON process_step_runs
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'step.created',json_object('step_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'execution_state', NEW.execution_state, 'to_state', NEW.state));
END;

CREATE TRIGGER bus_step_changed AFTER UPDATE ON process_step_runs
WHEN OLD.state IS NOT NEW.state OR OLD.progress IS NOT NEW.progress OR OLD.output IS NOT NEW.output OR OLD.error IS NOT NEW.error OR OLD.definition_json IS NOT NEW.definition_json OR OLD.executor_json IS NOT NEW.executor_json OR OLD.required IS NOT NEW.required OR OLD.due_at IS NOT NEW.due_at OR OLD.execution_state IS NOT NEW.execution_state OR OLD.task_id IS NOT NEW.task_id
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,CASE WHEN OLD.state IS NOT NEW.state THEN 'step.state_changed' ELSE 'step.updated' END,json_object('step_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'execution_state', NEW.execution_state, 'from_state', OLD.state, 'to_state', NEW.state));
END;

CREATE TRIGGER bus_step_delivery AFTER UPDATE ON process_step_runs
WHEN (CASE WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END) IS NOT (CASE WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END)
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'delivery.state_changed',json_object('step_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'execution_state', NEW.execution_state, 'entity_kind', 'step', 'from_state', CASE WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END, 'to_state', CASE WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END));
END;
