CREATE TABLE IF NOT EXISTS computer_sessions (
    id                  TEXT PRIMARY KEY,
    backend             TEXT NOT NULL,
    backend_session_id  TEXT NOT NULL,
    app_context_id      TEXT,
    context_name        TEXT,
    initial_url         TEXT,
    current_url         TEXT,
    width               INTEGER,
    height              INTEGER,
    status              TEXT NOT NULL,
    close_reason        TEXT,
    recording_status    TEXT NOT NULL,
    opened_at           TEXT NOT NULL,
    closed_at           TEXT,
    updated_at          TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_computer_sessions_status_updated
    ON computer_sessions(status, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_computer_sessions_recording_updated
    ON computer_sessions(recording_status, updated_at DESC);
