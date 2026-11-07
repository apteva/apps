-- Provider evidence stays immutable; only the derived interpretation changes.
CREATE TABLE delivery_event_interpretations (
 event_id INTEGER PRIMARY KEY REFERENCES delivery_events(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind='auto_replied'),
 inbound_message_id INTEGER NOT NULL REFERENCES messages(id),
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE VIEW effective_delivery_events AS
 SELECT d.*, COALESCE(i.kind,d.kind) AS effective_kind, i.inbound_message_id
 FROM delivery_events d LEFT JOIN delivery_event_interpretations i ON i.event_id=d.id;
CREATE TABLE delivery_correction_outbox (
 event_id TEXT PRIMARY KEY, project_id TEXT NOT NULL, payload TEXT NOT NULL,
 delivered_at TEXT
);
