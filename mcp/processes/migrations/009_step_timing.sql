ALTER TABLE process_step_runs ADD COLUMN start_at TEXT NOT NULL DEFAULT '';
ALTER TABLE process_step_runs ADD COLUMN completed_at TEXT NOT NULL DEFAULT '';
UPDATE process_step_runs SET completed_at=updated_at WHERE state='completed';
CREATE TRIGGER bus_task_timing AFTER UPDATE ON process_step_runs
WHEN OLD.start_at IS NOT NEW.start_at
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 VALUES (NEW.project_id,'task.updated',json_object('task_id',NEW.id,'run_id',COALESCE(NEW.run_id,''),'entity_revision',NEW.revision,'start_at',NEW.start_at,'due_at',NEW.due_at));
END;
