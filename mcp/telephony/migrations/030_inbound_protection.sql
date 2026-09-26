ALTER TABLE calls ADD COLUMN handling_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN announcement_state TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN announcement_text TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS inbound_burst_attempts (
    project_id TEXT NOT NULL,
    carrier_slug TEXT NOT NULL,
    carrier_connection_id INTEGER NOT NULL,
    carrier_sid TEXT NOT NULL,
    to_number TEXT NOT NULL,
    from_number TEXT NOT NULL,
    received_at INTEGER NOT NULL,
    suppression_reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (project_id, carrier_slug, carrier_connection_id, carrier_sid)
);
CREATE INDEX IF NOT EXISTS idx_inbound_burst_destination ON inbound_burst_attempts(project_id, to_number, received_at);
CREATE INDEX IF NOT EXISTS idx_inbound_burst_caller ON inbound_burst_attempts(project_id, to_number, from_number, received_at);

CREATE TABLE IF NOT EXISTS inbound_burst_cooldowns (
    project_id TEXT NOT NULL,
    to_number TEXT NOT NULL,
    from_number TEXT NOT NULL,
    reason TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (project_id, to_number, from_number)
);
