-- Associate email records with the connection that owns them. Existing email
-- messages came from SES; their connection is resolved from the current SES
-- binding until the sender is next reconciled.
ALTER TABLE senders ADD COLUMN provider_connection_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE identities ADD COLUMN provider_connection_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN provider_slug TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN provider_connection_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN provider_thread_id TEXT NOT NULL DEFAULT '';
UPDATE messages SET provider_slug='aws-ses' WHERE channel='email';

DROP INDEX IF EXISTS ux_msg_inbound_provider_id;
CREATE UNIQUE INDEX ux_msg_inbound_provider_id
  ON messages(project_id, provider_slug, provider_connection_id, provider_message_id)
  WHERE direction='in' AND provider_message_id IS NOT NULL;

CREATE TABLE gmail_sync_state (
  project_id TEXT NOT NULL,
  connection_id INTEGER NOT NULL,
  mailbox TEXT NOT NULL,
  history_id TEXT NOT NULL,
  last_synced_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(project_id, connection_id)
);
