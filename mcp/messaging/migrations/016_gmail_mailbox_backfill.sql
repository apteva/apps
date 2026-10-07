-- Catch up existing connections as well as new ones. Progress survives worker
-- ticks/restarts; the fixed seven-day window does not move during pagination.
ALTER TABLE gmail_sync_state ADD COLUMN backfill_complete INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gmail_sync_state ADD COLUMN backfill_until INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gmail_sync_state ADD COLUMN backfill_page_token TEXT NOT NULL DEFAULT '';

-- A Gmail message already recorded by Apteva's send path must not be imported
-- again when it appears in SENT. Include both directions in the delivery key.
CREATE UNIQUE INDEX ux_gmail_message_delivery
  ON messages(project_id, provider_connection_id, provider_message_id)
  WHERE provider_slug='gmail' AND provider_message_id IS NOT NULL;
