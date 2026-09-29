-- DIDWW can quote either an individual available_did or a did_group/SKU.
-- Keep the provider resource IDs with the short-lived confirmation intent so
-- retries cannot accidentally order a different number or group.
ALTER TABLE number_purchase_intents ADD COLUMN provider_resource_id TEXT NOT NULL DEFAULT '';
ALTER TABLE number_purchase_intents ADD COLUMN provider_group_id TEXT NOT NULL DEFAULT '';
ALTER TABLE number_purchase_intents ADD COLUMN provider_sku_id TEXT NOT NULL DEFAULT '';
ALTER TABLE number_purchase_intents ADD COLUMN inventory_mode TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_number_purchase_intents_provider_resource
    ON number_purchase_intents(carrier_connection_id, provider_resource_id, status);
