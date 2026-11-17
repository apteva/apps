CREATE TABLE IF NOT EXISTS build_pins (
    build_id INTEGER NOT NULL,
    reason TEXT NOT NULL,
    expires_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (build_id, reason)
);
CREATE INDEX IF NOT EXISTS build_pins_expires ON build_pins(expires_at);
