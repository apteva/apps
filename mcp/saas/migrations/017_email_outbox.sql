-- SaaS v0.13.1 -- email outbox.

-- Every account email is stored here before it is sent, so a failed send can
-- be retried by the email-outbox worker with the same rendered message.
-- idempotency_key is the email's base key (one row per email). send_key is the
-- key passed to Messaging on the next attempt: it stays the same after an
-- error (Messaging stored nothing, or returns the stored row), and gets a
-- ":retry:N" suffix after Messaging stored a failed message under it.
CREATE TABLE saas_email_outbox (
  id               INTEGER PRIMARY KEY,
  project_id       TEXT NOT NULL,
  account_id       TEXT NOT NULL,
  kind             TEXT NOT NULL,
  recipient        TEXT NOT NULL,
  subject          TEXT NOT NULL DEFAULT '',
  body             TEXT NOT NULL DEFAULT '',
  body_html        TEXT NOT NULL DEFAULT '',
  detail_json      TEXT NOT NULL DEFAULT '{}',
  idempotency_key  TEXT NOT NULL,
  send_key         TEXT NOT NULL,
  status           TEXT NOT NULL DEFAULT 'pending',
  attempts         INTEGER NOT NULL DEFAULT 0,
  last_error       TEXT NOT NULL DEFAULT '',
  message_id       INTEGER,
  message_status   TEXT NOT NULL DEFAULT '',
  next_attempt_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  sent_at          TIMESTAMP,
  created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, idempotency_key)
);
CREATE INDEX ix_saas_email_outbox_due ON saas_email_outbox(status, next_attempt_at);
