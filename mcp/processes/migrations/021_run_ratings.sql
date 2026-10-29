-- Ratings are revisioned judgements on terminal runs and steps. The latest
-- revision per rater/run/step is active; earlier revisions remain history.
CREATE TABLE process_run_ratings (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES process_runs(id),
 step_id TEXT NOT NULL DEFAULT '',
 process_id TEXT NOT NULL,
 version INTEGER NOT NULL,
 executor_agent_id INTEGER NOT NULL DEFAULT 0,
 rater TEXT NOT NULL,
 source TEXT NOT NULL CHECK(source IN ('operator','reviewer','agent')),
 revision INTEGER NOT NULL,
 score INTEGER NOT NULL CHECK(score BETWEEN 1 AND 5),
 criteria_json TEXT NOT NULL DEFAULT '{}',
 tags_json TEXT NOT NULL DEFAULT '[]',
 comment TEXT NOT NULL DEFAULT '',
 corrected_output TEXT NOT NULL DEFAULT '',
 request_key TEXT NOT NULL,
 superseded INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL,
 UNIQUE(run_id, step_id, rater, revision),
 UNIQUE(run_id, rater, request_key)
);
CREATE INDEX run_ratings_run ON process_run_ratings(run_id, superseded);
CREATE INDEX run_ratings_quality ON process_run_ratings(process_id, version, superseded);

CREATE TABLE process_review_items (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES process_runs(id),
 process_id TEXT NOT NULL,
 version INTEGER NOT NULL,
 reason TEXT NOT NULL CHECK(reason IN ('low_score','failed','sample')),
 rating_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','claimed','resolved','dismissed')),
 assignee TEXT NOT NULL DEFAULT '',
 resolution TEXT NOT NULL DEFAULT '',
 revision INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(run_id, reason)
);
CREATE INDEX review_items_queue ON process_review_items(process_id, state, created_at DESC);

-- Review rules live in the frozen procedure version, so a run is judged by the
-- policy it ran under. Run-state rules fire in SQL with the state transition.
CREATE TRIGGER process_review_failed_run AFTER UPDATE OF state ON process_runs
 WHEN NEW.state='failed' AND OLD.state IS NOT 'failed'
 BEGIN
 INSERT INTO process_review_items(id,run_id,process_id,version,reason,created_at,updated_at)
 SELECT 'rvw_'||lower(hex(randomblob(16))),NEW.id,NEW.process_id,NEW.version,'failed',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM process_versions v WHERE v.process_id=NEW.process_id AND v.version=NEW.version
 AND coalesce(json_extract(v.body_json,'$.quality.review.failed_runs'),0)=1
 ON CONFLICT DO NOTHING;
 END;
CREATE TRIGGER process_review_sampled_run AFTER UPDATE OF state ON process_runs
 WHEN NEW.state='completed' AND OLD.state IS NOT 'completed'
 BEGIN
 INSERT INTO process_review_items(id,run_id,process_id,version,reason,created_at,updated_at)
 SELECT 'rvw_'||lower(hex(randomblob(16))),NEW.id,NEW.process_id,NEW.version,'sample',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM process_versions v WHERE v.process_id=NEW.process_id AND v.version=NEW.version
 AND abs(random()%100)<coalesce(json_extract(v.body_json,'$.quality.review.sample_percent'),0)
 ON CONFLICT DO NOTHING;
 END;

CREATE TRIGGER bus_run_rated AFTER INSERT ON process_run_ratings
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 SELECT p.project_id,CASE WHEN NEW.revision=1 THEN 'run.rated' ELSE 'run.rating_revised' END,json_object('process_id',NEW.process_id,'assignment_id',r.assignment_id,'run_id',NEW.run_id,'step_id',NEW.step_id,'rating_id',NEW.id,'rating_revision',NEW.revision,'source',NEW.source,'score',NEW.score,'version',NEW.version)
 FROM process_runs r JOIN processes p ON p.id=r.process_id WHERE r.id=NEW.run_id;
END;
CREATE TRIGGER bus_review_created AFTER INSERT ON process_review_items
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 SELECT project_id,'review.item_created',json_object('process_id',NEW.process_id,'run_id',NEW.run_id,'review_id',NEW.id,'reason',NEW.reason,'to_state',NEW.state)
 FROM processes WHERE id=NEW.process_id;
END;
CREATE TRIGGER bus_review_updated AFTER UPDATE ON process_review_items
WHEN OLD.revision IS NOT NEW.revision
BEGIN
 INSERT INTO process_event_outbox(project_id,topic,payload_json)
 SELECT project_id,'review.item_updated',json_object('process_id',NEW.process_id,'run_id',NEW.run_id,'review_id',NEW.id,'reason',NEW.reason,'from_state',OLD.state,'to_state',NEW.state,'review_revision',NEW.revision)
 FROM processes WHERE id=NEW.process_id;
END;
