-- Per-transport delivery health for a contact channel. Messaging remains the
-- suppression source of truth; CRM stores the effective state needed for
-- contact reads, address selection, and messageable audience filtering.
CREATE TABLE contact_channel_delivery_state (
  project_id                 TEXT    NOT NULL,
  channel_id                 INTEGER NOT NULL REFERENCES contact_channels(id) ON DELETE CASCADE,
  transport                  TEXT    NOT NULL CHECK (transport IN ('email', 'sms', 'whatsapp')),
  status                     TEXT    NOT NULL DEFAULT 'active'
    CHECK (status IN ('active', 'soft_bounced', 'hard_bounced', 'complained', 'unsubscribed')),
  consecutive_soft_bounces   INTEGER NOT NULL DEFAULT 0,
  last_bounce_at             TIMESTAMP,
  last_delivered_at          TIMESTAMP,
  status_reason              TEXT,
  status_updated_at          TIMESTAMP,
  quarantined                INTEGER NOT NULL DEFAULT 0,
  quarantined_at             TIMESTAMP,
  suppressed                 INTEGER NOT NULL DEFAULT 0,
  suppression_kind           TEXT,
  suppression_match          TEXT,
  suppression_reason         TEXT,
  suppression_source         TEXT,
  suppressed_at              TIMESTAMP,
  suppression_checked_at     TIMESTAMP,
  updated_at                 TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, channel_id, transport)
);
CREATE INDEX ix_channel_delivery_messageable
  ON contact_channel_delivery_state(project_id, transport, suppressed, quarantined, status);
CREATE INDEX ix_channel_delivery_retry
  ON contact_channel_delivery_state(project_id, quarantined, suppressed, suppression_source, suppression_reason);

-- Stable provider event IDs make message.event processing idempotent. The
-- source install is part of the key because different Messaging installs can
-- legitimately use the same provider identifier.
CREATE TABLE crm_processed_messaging_events (
  project_id        TEXT    NOT NULL,
  source_install_id INTEGER NOT NULL DEFAULT 0,
  event_id          TEXT    NOT NULL,
  processed_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, source_install_id, event_id)
);

-- Backfill routes that predate this migration. A phone can independently be
-- messageable over SMS and WhatsApp, so it owns one row for each transport.
INSERT OR IGNORE INTO contact_channel_delivery_state (project_id, channel_id, transport)
SELECT project_id, id, 'email' FROM contact_channels WHERE kind = 'email';
INSERT OR IGNORE INTO contact_channel_delivery_state (project_id, channel_id, transport)
SELECT project_id, id, 'sms' FROM contact_channels WHERE kind = 'phone';
INSERT OR IGNORE INTO contact_channel_delivery_state (project_id, channel_id, transport)
SELECT project_id, id, 'whatsapp' FROM contact_channels WHERE kind = 'phone';

CREATE TRIGGER contact_channel_delivery_email_insert
AFTER INSERT ON contact_channels WHEN NEW.kind = 'email'
BEGIN
  INSERT OR IGNORE INTO contact_channel_delivery_state (project_id, channel_id, transport)
  VALUES (NEW.project_id, NEW.id, 'email');
END;

CREATE TRIGGER contact_channel_delivery_phone_insert
AFTER INSERT ON contact_channels WHEN NEW.kind = 'phone'
BEGIN
  INSERT OR IGNORE INTO contact_channel_delivery_state (project_id, channel_id, transport)
  VALUES (NEW.project_id, NEW.id, 'sms');
  INSERT OR IGNORE INTO contact_channel_delivery_state (project_id, channel_id, transport)
  VALUES (NEW.project_id, NEW.id, 'whatsapp');
END;

-- Keep cleanup deterministic even on SQLite connections where foreign-key
-- enforcement was not enabled before this migration ran.
CREATE TRIGGER contact_channel_delivery_delete
AFTER DELETE ON contact_channels
BEGIN
  DELETE FROM contact_channel_delivery_state
  WHERE project_id = OLD.project_id AND channel_id = OLD.id;
END;
