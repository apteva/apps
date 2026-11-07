-- Recovery index for SES automatic responses. Only Transient/General bounces
-- carrying a feedback ID can ever be reinterpreted, so recovery indexes those
-- once and then only revisits a candidate when its inbound copy arrives.
CREATE TABLE auto_response_candidates (
 event_id INTEGER PRIMARY KEY REFERENCES delivery_events(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 message_id INTEGER NOT NULL,
 feedback_id TEXT NOT NULL,
 ready INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX ix_auto_response_feedback ON auto_response_candidates(project_id, feedback_id);
CREATE INDEX ix_auto_response_ready ON auto_response_candidates(message_id) WHERE ready=1;
-- Monotonic cursors. Existing inbound mail is covered by the bounce backfill,
-- which checks for an already-stored inbound copy when it indexes a candidate.
CREATE TABLE auto_response_scan_state (name TEXT PRIMARY KEY, last_id INTEGER NOT NULL);
INSERT INTO auto_response_scan_state(name,last_id) VALUES ('bounces',0);
INSERT INTO auto_response_scan_state(name,last_id) SELECT 'inbound',COALESCE(MAX(id),0) FROM messages;
