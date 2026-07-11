-- v0.13.37: provider webhooks are retried, so every stored event needs a
-- stable provider id. Existing rows remain valid and are not retroactively
-- deduplicated because older payloads did not preserve that id.

ALTER TABLE delivery_events ADD COLUMN provider_event_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS ux_delivery_event_provider_id
  ON delivery_events(provider_event_id)
  WHERE provider_event_id IS NOT NULL;
