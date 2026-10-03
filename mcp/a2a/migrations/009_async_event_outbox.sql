-- Durable publications for server-managed async A2A notifications.
-- The task ledger remains authoritative; this outbox only records the
-- publication intent so a transient app-event gateway failure cannot lose a
-- progress or terminal notification.
CREATE TABLE IF NOT EXISTS a2a_event_outbox (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT    NOT NULL,
    event_id      TEXT    NOT NULL UNIQUE,
    topic         TEXT    NOT NULL,
    payload_json  TEXT    NOT NULL,
    published     INTEGER NOT NULL DEFAULT 0,
    attempts      INTEGER NOT NULL DEFAULT 0,
    next_attempt  TEXT    NOT NULL DEFAULT '',
    last_error    TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_a2a_event_outbox_due
    ON a2a_event_outbox(project_id, published, next_attempt, id);
