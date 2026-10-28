CREATE TABLE IF NOT EXISTS google_play_observations (
    connection_id INTEGER NOT NULL,
    package_name TEXT NOT NULL,
    observation_key TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    checked_at TEXT NOT NULL,
    PRIMARY KEY (connection_id, package_name, observation_key)
);
CREATE TABLE IF NOT EXISTS google_play_backoff (
    connection_id INTEGER PRIMARY KEY,
    failures INTEGER NOT NULL DEFAULT 0,
    next_retry_at TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT ''
);
