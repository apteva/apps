-- Durable bus events. A row whose state change must reach subscribers
-- carries the pending event itself, written by the same statement that
-- changes the state: event_topic + event_payload stay set until the
-- platform acknowledges the event, then are cleared. event_seq numbers
-- the row's events, so a retry resends the same event ID and the
-- platform drops the duplicate.
ALTER TABLE inbox_items ADD COLUMN event_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE inbox_items ADD COLUMN event_topic TEXT;
ALTER TABLE inbox_items ADD COLUMN event_payload TEXT;
ALTER TABLE inbox_items ADD COLUMN event_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE inbox_items ADD COLUMN event_next_at TEXT;
CREATE INDEX IF NOT EXISTS idx_inbox_items_event ON inbox_items(event_next_at) WHERE event_topic IS NOT NULL;

ALTER TABLE inbox_send_intents ADD COLUMN event_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE inbox_send_intents ADD COLUMN event_topic TEXT;
ALTER TABLE inbox_send_intents ADD COLUMN event_payload TEXT;
ALTER TABLE inbox_send_intents ADD COLUMN event_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE inbox_send_intents ADD COLUMN event_next_at TEXT;
CREATE INDEX IF NOT EXISTS idx_inbox_send_intents_event ON inbox_send_intents(event_next_at) WHERE event_topic IS NOT NULL;

ALTER TABLE post_targets ADD COLUMN event_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE post_targets ADD COLUMN event_topic TEXT;
ALTER TABLE post_targets ADD COLUMN event_payload TEXT;
ALTER TABLE post_targets ADD COLUMN event_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE post_targets ADD COLUMN event_next_at TEXT;
CREATE INDEX IF NOT EXISTS idx_post_targets_event ON post_targets(event_next_at) WHERE event_topic IS NOT NULL;
