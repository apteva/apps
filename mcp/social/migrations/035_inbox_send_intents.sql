-- One row per inbox reply attempt, written before the provider call. A
-- caller idempotency key replays the stored result instead of sending
-- again; uncertain sends are settled when sync finds our reply upstream.
CREATE TABLE IF NOT EXISTS inbox_send_intents (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id         TEXT    NOT NULL,
  inbox_item_id      INTEGER NOT NULL,
  social_account_id  INTEGER NOT NULL,
  platform           TEXT    NOT NULL,
  kind               TEXT    NOT NULL,
  target_external_id TEXT    NOT NULL,
  target_root_id     TEXT    NOT NULL DEFAULT '',
  target_thread_id   TEXT    NOT NULL DEFAULT '',
  idempotency_key    TEXT,
  request_hash       TEXT    NOT NULL,
  body_text          TEXT    NOT NULL DEFAULT '',
  state              TEXT    NOT NULL DEFAULT 'pending', -- pending | sent | failed | uncertain
  external_id        TEXT    NOT NULL DEFAULT '',
  outcome_json       TEXT,
  error              TEXT,
  created_at         TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_inbox_send_intents_key
  ON inbox_send_intents(project_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_inbox_send_intents_open
  ON inbox_send_intents(social_account_id, state, created_at);
CREATE INDEX IF NOT EXISTS idx_inbox_send_intents_item
  ON inbox_send_intents(inbox_item_id, request_hash, created_at);
