ALTER TABLE calls ADD COLUMN callback_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN carrier_request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN state_expires_at TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN deadline_at TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN media_active INTEGER NOT NULL DEFAULT 0;
ALTER TABLE inbound_routes ADD COLUMN previous_voice_url TEXT NOT NULL DEFAULT '';

UPDATE calls
SET carrier_sid = '', status = 'canceled', error_message = 'duplicate inbound carrier callback'
WHERE direction = 'inbound'
  AND carrier_sid <> ''
  AND rowid NOT IN (
      SELECT MIN(rowid)
      FROM calls
      WHERE direction = 'inbound' AND carrier_sid <> ''
      GROUP BY carrier_slug, carrier_connection_id, carrier_sid
  );

CREATE UNIQUE INDEX IF NOT EXISTS idx_calls_inbound_carrier_sid
    ON calls(carrier_slug, carrier_connection_id, carrier_sid)
    WHERE direction = 'inbound' AND carrier_sid <> '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_calls_outbound_idempotency
    ON calls(project_id, agent_id, idempotency_key)
    WHERE direction = 'outbound' AND idempotency_key <> '';

UPDATE inbound_routes
SET enabled = 0,
    updated_at = CURRENT_TIMESTAMP
WHERE enabled = 1
  AND rowid NOT IN (
      SELECT MAX(rowid)
      FROM inbound_routes
      WHERE enabled = 1
      GROUP BY carrier_connection_id, phone_number
  );

CREATE UNIQUE INDEX IF NOT EXISTS idx_inbound_routes_active_number
    ON inbound_routes(carrier_connection_id, phone_number)
    WHERE enabled = 1;

CREATE INDEX IF NOT EXISTS idx_calls_state_expiry
    ON calls(status, state_expires_at);

CREATE INDEX IF NOT EXISTS idx_calls_deadline
    ON calls(status, deadline_at);

CREATE TABLE IF NOT EXISTS inbound_event_outbox (
    call_id          TEXT PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
    project_id       TEXT NOT NULL DEFAULT '',
    agent_id         INTEGER NOT NULL,
    message          TEXT NOT NULL,
    attempts         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at  TEXT NOT NULL,
    delivered_at     TEXT NOT NULL DEFAULT '',
    last_error       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_inbound_event_outbox_pending
    ON inbound_event_outbox(delivered_at, next_attempt_at, project_id);
