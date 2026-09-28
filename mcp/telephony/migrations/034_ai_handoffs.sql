-- One durable startup budget per inbound call, shared by all entry points.
CREATE TABLE ai_handoffs (
    call_id TEXT PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'retry',
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL,
    deadline_at TEXT NOT NULL,
    next_attempt_at TEXT NOT NULL,
    owner TEXT NOT NULL DEFAULT '',
    agent_id INTEGER NOT NULL DEFAULT 0,
    node_id TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    fallback_applied INTEGER NOT NULL DEFAULT 0,
    directive TEXT NOT NULL,
    voice TEXT NOT NULL,
    greeting TEXT NOT NULL
);
CREATE INDEX idx_ai_handoffs_due ON ai_handoffs(project_id,status,next_attempt_at);
CREATE TABLE ai_handoff_attempts (
    call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    attempt INTEGER NOT NULL,
    thread_id TEXT NOT NULL,
    outcome TEXT NOT NULL DEFAULT 'started',
    error_code TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    completed_at TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(call_id,attempt)
);

-- Cancellation fences late startup results in the same transaction as hangup.
CREATE TRIGGER ai_handoff_call_ended AFTER UPDATE OF status ON calls
WHEN NEW.status IN ('completed','failed','busy','no-answer','canceled')
BEGIN
    UPDATE ai_handoffs SET status='canceled'
      WHERE call_id=NEW.id AND status IN ('running','retry');
    UPDATE ai_handoff_attempts SET outcome='canceled',error_code='call_ended',
      completed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
      WHERE call_id=NEW.id AND outcome='started';
END;
