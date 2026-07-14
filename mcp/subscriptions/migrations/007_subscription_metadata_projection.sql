-- Subscriptions v0.6.0 -- atomically project source metadata with item changes.

ALTER TABLE subscription_changes
  ADD COLUMN subscription_metadata_patch_json TEXT NOT NULL DEFAULT '{}';
