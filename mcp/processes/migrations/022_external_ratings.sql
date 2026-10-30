-- Ratings are an external reviewer's judgement. A run's owner and executors
-- never receive rating requests; the procedure names its reviewer instead.
-- Requests are durable and retried independently of worker cleanup.
CREATE TABLE process_rating_requests (
 run_id TEXT PRIMARY KEY REFERENCES process_runs(id),
 agent_id INTEGER NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '',
 delivered_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX process_rating_requests_pending ON process_rating_requests(next_attempt_at) WHERE delivered_at='';
CREATE TRIGGER process_rating_request_terminal AFTER UPDATE OF state ON process_runs
 WHEN NEW.state IN ('completed','failed','cancelled') AND OLD.state NOT IN ('completed','failed','cancelled')
 BEGIN
 INSERT INTO process_rating_requests(run_id,agent_id)
 SELECT NEW.id,CAST(json_extract(v.body_json,'$.quality.reviewer.agent_id') AS INTEGER)
 FROM process_versions v WHERE v.process_id=NEW.process_id AND v.version=NEW.version
 AND coalesce(json_extract(v.body_json,'$.quality.request_rating'),0)=1
 AND json_extract(v.body_json,'$.quality.reviewer.kind')='agent'
 AND coalesce(json_extract(v.body_json,'$.quality.reviewer.agent_id'),0)>0
 ON CONFLICT DO NOTHING;
 END;
