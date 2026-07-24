ALTER TABLE computer_sessions ADD COLUMN final_screenshot BLOB;
ALTER TABLE computer_sessions ADD COLUMN final_screenshot_mime TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS computer_session_navigation (
    session_id  TEXT NOT NULL,
    position    INTEGER NOT NULL,
    url         TEXT NOT NULL,
    title       TEXT NOT NULL DEFAULT '',
    visited_at  TEXT NOT NULL,
    PRIMARY KEY (session_id, position),
    FOREIGN KEY (session_id) REFERENCES computer_sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_computer_session_navigation_session
    ON computer_session_navigation(session_id, position);
