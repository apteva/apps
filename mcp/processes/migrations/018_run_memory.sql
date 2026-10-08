CREATE TABLE process_run_summaries (
 run_id TEXT NOT NULL REFERENCES process_runs(id),
 revision INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 author TEXT NOT NULL,
 request_key TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(run_id, revision),
 UNIQUE(run_id, request_key)
);
CREATE TABLE process_memory (
 process_id TEXT NOT NULL REFERENCES processes(id),
 assignment_id TEXT NOT NULL,
 scope TEXT NOT NULL,
 entry_key TEXT NOT NULL,
 revision INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 run_id TEXT NOT NULL REFERENCES process_runs(id),
 step_id TEXT NOT NULL DEFAULT '',
 author TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(process_id, assignment_id, scope, entry_key)
);
CREATE INDEX process_memory_page ON process_memory(process_id,assignment_id,scope,entry_key);
-- RFC3339Nano omits trailing fractional zeros. Normalize a generated key so
-- lexical order, date filters and keyset cursors agree at subsecond boundaries.
ALTER TABLE process_runs ADD COLUMN history_at TEXT GENERATED ALWAYS AS (
 substr(created_at,1,19)||'.'||substr((CASE WHEN substr(created_at,20,1)='.' THEN replace(substr(created_at,21),'Z','') ELSE '' END)||'000000000',1,9)||'Z'
) VIRTUAL;
CREATE INDEX process_history_page ON process_runs(process_id,history_at DESC,id DESC);
CREATE INDEX project_history_page ON process_runs(history_at DESC,id DESC);

CREATE TRIGGER bus_run_summary_saved AFTER INSERT ON process_run_summaries
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 SELECT p.project_id,'run.summary_updated',json_object('process_id',r.process_id,'assignment_id',r.assignment_id,'run_id',r.id,'summary_revision',NEW.revision)
 FROM process_runs r JOIN processes p ON p.id=r.process_id WHERE r.id=NEW.run_id;
END;
CREATE TRIGGER bus_run_memory_created AFTER INSERT ON process_memory
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 SELECT project_id,'run.memory_updated',json_object('process_id',NEW.process_id,'assignment_id',NEW.assignment_id,'run_id',NEW.run_id,'scope',NEW.scope,'key',NEW.entry_key,'memory_revision',NEW.revision)
 FROM processes WHERE id=NEW.process_id;
END;
CREATE TRIGGER bus_run_memory_changed AFTER UPDATE ON process_memory
WHEN OLD.revision IS NOT NEW.revision
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 SELECT project_id,'run.memory_updated',json_object('process_id',NEW.process_id,'assignment_id',NEW.assignment_id,'run_id',NEW.run_id,'scope',NEW.scope,'key',NEW.entry_key,'memory_revision',NEW.revision)
 FROM processes WHERE id=NEW.process_id;
END;
