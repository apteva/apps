-- Capture committed domain changes from every writer (HTTP, MCP, workers and
-- lifecycle callbacks). SQLite triggers share the writer's transaction: rolled
-- back changes never escape, and publishing never holds an execution lock.
CREATE TABLE process_event_outbox (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 event_id TEXT NOT NULL UNIQUE DEFAULT (lower(hex(randomblob(16)))),
 project_id TEXT NOT NULL,
 topic TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 occurred_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX process_event_pending ON process_event_outbox(next_attempt_at,sequence);

CREATE TRIGGER bus_process_created AFTER INSERT ON processes
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'process.created',json_object('process_id', NEW.id, 'procedure_version', NEW.current_version, 'to_state', NEW.status));
END;

CREATE TRIGGER bus_process_changed AFTER UPDATE ON processes
WHEN OLD.status IS NOT NEW.status OR OLD.current_version IS NOT NEW.current_version OR OLD.sync_pending IS NOT NEW.sync_pending OR OLD.sync_error IS NOT NEW.sync_error
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,CASE WHEN OLD.status IS NOT NEW.status THEN 'process.state_changed' ELSE 'process.updated' END,json_object('process_id', NEW.id, 'procedure_version', NEW.current_version, 'from_state', OLD.status, 'to_state', NEW.status));
END;

CREATE TRIGGER bus_assignment_created AFTER INSERT ON process_assignments
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES ((SELECT project_id FROM processes WHERE id=NEW.process_id),'assignment.created',json_object('process_id', NEW.process_id, 'assignment_id', NEW.id, 'entity_revision', NEW.revision, 'to_state', NEW.status));
END;

CREATE TRIGGER bus_assignment_changed AFTER UPDATE ON process_assignments
WHEN OLD.body_json IS NOT NEW.body_json OR OLD.status IS NOT NEW.status OR OLD.sync_pending IS NOT NEW.sync_pending OR OLD.sync_error IS NOT NEW.sync_error OR OLD.next_run_at IS NOT NEW.next_run_at OR OLD.last_schedule_note IS NOT NEW.last_schedule_note
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES ((SELECT project_id FROM processes WHERE id=NEW.process_id),CASE WHEN OLD.status IS NOT NEW.status THEN 'assignment.state_changed' ELSE 'assignment.updated' END,json_object('process_id', NEW.process_id, 'assignment_id', NEW.id, 'entity_revision', NEW.revision, 'from_state', OLD.status, 'to_state', NEW.status));
END;

CREATE TRIGGER bus_run_created AFTER INSERT ON process_runs
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES ((SELECT project_id FROM processes WHERE id=NEW.process_id),'run.created',json_object('process_id', NEW.process_id, 'assignment_id', NEW.assignment_id, 'run_id', NEW.id, 'progress', NEW.progress, 'backend', NEW.backend, 'execution_state', NEW.execution_state, 'procedure_version', NEW.version, 'to_state', NEW.state));
END;

CREATE TRIGGER bus_run_changed AFTER UPDATE ON process_runs
WHEN OLD.state IS NOT NEW.state OR OLD.progress IS NOT NEW.progress OR OLD.current_step IS NOT NEW.current_step OR OLD.result IS NOT NEW.result OR OLD.error IS NOT NEW.error OR OLD.execution_state IS NOT NEW.execution_state OR OLD.schedule_paused IS NOT NEW.schedule_paused OR OLD.task_id IS NOT NEW.task_id
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES ((SELECT project_id FROM processes WHERE id=NEW.process_id),CASE WHEN OLD.state IS NOT NEW.state THEN 'run.state_changed' ELSE 'run.updated' END,json_object('process_id', NEW.process_id, 'assignment_id', NEW.assignment_id, 'run_id', NEW.id, 'progress', NEW.progress, 'backend', NEW.backend, 'execution_state', NEW.execution_state, 'procedure_version', NEW.version, 'from_state', OLD.state, 'to_state', NEW.state));
END;

CREATE TRIGGER bus_task_created AFTER INSERT ON process_step_runs
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'task.created',json_object('task_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state, 'to_state', NEW.state));
END;

CREATE TRIGGER bus_task_changed AFTER UPDATE ON process_step_runs
WHEN OLD.state IS NOT NEW.state OR OLD.progress IS NOT NEW.progress OR OLD.output IS NOT NEW.output OR OLD.error IS NOT NEW.error OR OLD.decision IS NOT NEW.decision OR OLD.definition_json IS NOT NEW.definition_json OR OLD.executor_json IS NOT NEW.executor_json OR OLD.required IS NOT NEW.required OR OLD.due_at IS NOT NEW.due_at OR OLD.execution_state IS NOT NEW.execution_state OR OLD.task_id IS NOT NEW.task_id
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,CASE WHEN OLD.state IS NOT NEW.state THEN 'task.state_changed' ELSE 'task.updated' END,json_object('task_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state, 'from_state', OLD.state, 'to_state', NEW.state));
END;

CREATE TRIGGER bus_approval_created AFTER INSERT ON process_step_runs
WHEN json_extract(NEW.definition_json,'$.kind')='approval' AND NEW.state IN ('ready','waiting') AND NEW.decision=''
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'task.approval_requested',json_object('task_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state));
END;

CREATE TRIGGER bus_approval_ready AFTER UPDATE ON process_step_runs
WHEN json_extract(NEW.definition_json,'$.kind')='approval' AND NEW.state IN ('ready','waiting') AND NEW.decision='' AND OLD.state NOT IN ('ready','waiting','running','blocked')
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'task.approval_requested',json_object('task_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state));
END;

CREATE TRIGGER bus_approval_resolved AFTER UPDATE ON process_step_runs
WHEN json_extract(NEW.definition_json,'$.kind')='approval' AND NEW.decision<>'' AND OLD.decision IS NOT NEW.decision
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'task.approval_resolved',json_object('task_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state));
END;

CREATE TRIGGER bus_run_delivery AFTER UPDATE ON process_runs
WHEN (CASE WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END) IS NOT (CASE WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END)
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES ((SELECT project_id FROM processes WHERE id=NEW.process_id),'delivery.state_changed',json_object('process_id', NEW.process_id, 'assignment_id', NEW.assignment_id, 'run_id', NEW.id, 'progress', NEW.progress, 'backend', NEW.backend, 'execution_state', NEW.execution_state, 'procedure_version', NEW.version, 'entity_kind', 'run', 'from_state', CASE WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END, 'to_state', CASE WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END));
END;

CREATE TRIGGER bus_task_delivery AFTER UPDATE ON process_step_runs
WHEN (CASE WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END) IS NOT (CASE WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END)
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'delivery.state_changed',json_object('task_id', NEW.id, 'run_id', COALESCE(NEW.run_id,''), 'process_id', COALESCE((SELECT process_id FROM process_runs WHERE id=NEW.run_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_runs WHERE id=NEW.run_id),''), 'step_key', NEW.step_key, 'origin', NEW.origin, 'entity_revision', NEW.revision, 'progress', NEW.progress, 'executor', json(NEW.executor_json), 'decision', NEW.decision, 'execution_state', NEW.execution_state, 'entity_kind', 'task', 'from_state', CASE WHEN OLD.delivery_warning<>'' THEN 'retrying' WHEN OLD.delivered_at<>'' THEN 'delivered' ELSE 'pending' END, 'to_state', CASE WHEN NEW.delivery_warning<>'' THEN 'retrying' WHEN NEW.delivered_at<>'' THEN 'delivered' ELSE 'pending' END));
END;

CREATE TRIGGER bus_trigger_created AFTER INSERT ON process_triggers
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'trigger.updated',json_object('process_id', NEW.process_id, 'assignment_id', NEW.assignment_id, 'trigger_id', NEW.id, 'entity_revision', NEW.revision, 'to_state', NEW.status, 'sync_pending', NEW.sync_pending));
END;

CREATE TRIGGER bus_trigger_changed AFTER UPDATE ON process_triggers
WHEN OLD.config_json IS NOT NEW.config_json OR OLD.status IS NOT NEW.status OR OLD.subscription_enabled IS NOT NEW.subscription_enabled OR OLD.sync_pending IS NOT NEW.sync_pending OR OLD.sync_error IS NOT NEW.sync_error
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'trigger.updated',json_object('process_id', NEW.process_id, 'assignment_id', NEW.assignment_id, 'trigger_id', NEW.id, 'entity_revision', NEW.revision, 'to_state', NEW.status, 'sync_pending', NEW.sync_pending));
END;

CREATE TRIGGER bus_trigger_event_created AFTER INSERT ON process_trigger_events
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'trigger.event_processed',json_object('trigger_id', NEW.trigger_id, 'process_id', COALESCE((SELECT process_id FROM process_triggers WHERE id=NEW.trigger_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_triggers WHERE id=NEW.trigger_id),''), 'source_event_id', NEW.event_id, 'trigger_event_id', NEW.id, 'run_id', NEW.run_id, 'to_state', NEW.status));
END;

CREATE TRIGGER bus_trigger_event_changed AFTER UPDATE ON process_trigger_events
WHEN OLD.status IS NOT NEW.status OR OLD.run_id IS NOT NEW.run_id OR OLD.reason IS NOT NEW.reason
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'trigger.event_processed',json_object('trigger_id', NEW.trigger_id, 'process_id', COALESCE((SELECT process_id FROM process_triggers WHERE id=NEW.trigger_id),''), 'assignment_id', COALESCE((SELECT assignment_id FROM process_triggers WHERE id=NEW.trigger_id),''), 'source_event_id', NEW.event_id, 'trigger_event_id', NEW.id, 'run_id', NEW.run_id, 'to_state', NEW.status));
END;
