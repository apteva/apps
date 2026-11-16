-- fleet 016: track the tenant control key by ID so it can be rotated and
-- the previous key revoked, with enough durable state to resume a rotation
-- that crashed between steps.
--
--   api_key_id / api_key_prefix   the live control key as the tenant knows it
--   pending_key_enc / _id / _prefix
--                                 a freshly minted key that is not yet the
--                                 live key (minted, not yet verified/swapped)
--   revoke_key_prefix             the previous key, swapped out but not yet
--                                 confirmed revoked on the tenant

ALTER TABLE fleet_tenants ADD COLUMN api_key_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE fleet_tenants ADD COLUMN api_key_prefix TEXT NOT NULL DEFAULT '';
ALTER TABLE fleet_tenants ADD COLUMN pending_key_enc BLOB;
ALTER TABLE fleet_tenants ADD COLUMN pending_key_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE fleet_tenants ADD COLUMN pending_key_prefix TEXT NOT NULL DEFAULT '';
ALTER TABLE fleet_tenants ADD COLUMN revoke_key_prefix TEXT NOT NULL DEFAULT '';
ALTER TABLE fleet_tenants ADD COLUMN key_rotated_at DATETIME;

CREATE INDEX IF NOT EXISTS idx_fleet_events_kind_time
    ON fleet_events(kind, created_at);
