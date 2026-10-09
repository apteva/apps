-- Freeze coalesced delivery snapshots before sending. Retries retain both the
-- event identity and payload, even when new publications arrive meanwhile.
ALTER TABLE projection_event_outbox ADD COLUMN delivery_sealed INTEGER NOT NULL DEFAULT 0;
