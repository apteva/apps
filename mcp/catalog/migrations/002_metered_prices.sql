-- Catalog v0.1.2 — generic metered price metadata.

ALTER TABLE prices ADD COLUMN billing_scheme TEXT NOT NULL DEFAULT 'flat';
ALTER TABLE prices ADD COLUMN meter_key TEXT NOT NULL DEFAULT '';
ALTER TABLE prices ADD COLUMN unit_label TEXT NOT NULL DEFAULT '';
ALTER TABLE prices ADD COLUMN unit_size INTEGER NOT NULL DEFAULT 1;

CREATE INDEX ix_prices_meter ON prices(project_id, billing_scheme, meter_key, archived_at);
