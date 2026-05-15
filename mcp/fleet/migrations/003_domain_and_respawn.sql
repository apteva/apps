-- fleet 003: per-tenant domain attachment + respawn bookkeeping.
--
-- Columns added are nullable / defaulted so existing rows keep working
-- and no backfill is needed. We use ADD COLUMN (not rebuild) because we
-- are not changing any CHECK constraints — unlike migration 002 which
-- had to broaden the status CHECK.
--
--   domain               canonical FQDN the tenant is served at (lowercased)
--   domain_record_id     "<apex>|<type>" so detach can target the same record
--   domain_attached_at   when attach succeeded
--   respawn_attempts     consecutive auto-respawn tries since last healthy boot
--                        — capped in code, reset on successful health check
--   last_respawn_at      timestamp of the most recent auto-respawn

ALTER TABLE fleet_tenants ADD COLUMN domain             TEXT;
ALTER TABLE fleet_tenants ADD COLUMN domain_record_id   TEXT;
ALTER TABLE fleet_tenants ADD COLUMN domain_attached_at DATETIME;
ALTER TABLE fleet_tenants ADD COLUMN respawn_attempts   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE fleet_tenants ADD COLUMN last_respawn_at    DATETIME;

CREATE INDEX IF NOT EXISTS idx_fleet_tenants_domain
    ON fleet_tenants(domain);
