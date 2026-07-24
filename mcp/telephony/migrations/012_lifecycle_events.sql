ALTER TABLE calls ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN provider_occurred_at TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN duration_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE calls ADD COLUMN talk_duration_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE calls ADD COLUMN termination_cause TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN termination_code TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN termination_initiator TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN provider_sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE calls ADD COLUMN provider_event_id TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN lifecycle_revision INTEGER NOT NULL DEFAULT 0;

UPDATE calls
SET updated_at = COALESCE(NULLIF(ended_at, ''), NULLIF(answered_at, ''), placed_at)
WHERE updated_at = '';

CREATE INDEX IF NOT EXISTS idx_calls_project_updated
    ON calls(project_id, updated_at, id);

CREATE INDEX IF NOT EXISTS idx_calls_provider_identity
    ON calls(project_id, carrier_slug, carrier_sid);

CREATE TABLE IF NOT EXISTS call_events (
    event_id       TEXT PRIMARY KEY,
    call_id        TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    project_id     TEXT NOT NULL,
    topic          TEXT NOT NULL,
    revision       INTEGER NOT NULL,
    occurred_at    TEXT NOT NULL,
    payload_json   TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    published_at   TEXT NOT NULL DEFAULT '',
    UNIQUE(call_id, topic)
);

CREATE INDEX IF NOT EXISTS idx_call_events_publish
    ON call_events(project_id, published_at, created_at);

CREATE INDEX IF NOT EXISTS idx_call_events_call_revision
    ON call_events(call_id, revision);
