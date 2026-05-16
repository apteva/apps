CREATE TABLE IF NOT EXISTS calls (
    id              TEXT PRIMARY KEY,
    thread_id       TEXT NOT NULL UNIQUE,
    carrier_sid     TEXT,                       -- Twilio CallSid; nullable until make_call returns
    to_number       TEXT NOT NULL,
    from_number     TEXT NOT NULL,
    directive       TEXT NOT NULL,
    voice           TEXT NOT NULL,
    audio_bridge_url TEXT NOT NULL,             -- core's /realtime/audio?thread=...&token=...
    status          TEXT NOT NULL,              -- initiated|ringing|in-progress|completed|failed|canceled|no-answer
    placed_at       TEXT NOT NULL,              -- RFC3339
    answered_at     TEXT,
    ended_at        TEXT,
    project_id      TEXT NOT NULL DEFAULT '',
    error_message   TEXT
);

CREATE INDEX IF NOT EXISTS idx_calls_status ON calls(status);
CREATE INDEX IF NOT EXISTS idx_calls_project ON calls(project_id, placed_at DESC);
