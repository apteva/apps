-- fleet 011: hosted tenants can terminate public HTTP/HTTPS directly on
-- their Instances host instead of routing through the parent Apteva server.
-- Existing tenants remain on parent ingress until an explicit staged cutover.

ALTER TABLE fleet_tenants ADD COLUMN ingress_mode TEXT NOT NULL DEFAULT 'parent';
ALTER TABLE fleet_tenants ADD COLUMN ingress_error TEXT;

CREATE INDEX IF NOT EXISTS idx_fleet_tenants_ingress_mode
    ON fleet_tenants(ingress_mode, instance_id);
