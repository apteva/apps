-- fleet 004: hosted tenants — apteva-servers spawned on a remote VPS
-- via the optional Instances integration, instead of locally on the
-- parent host.
--
--   instance_id        0 (default) = LOCAL (existing behavior — process
--                      on the parent host). >0 = a row in the Instances
--                      app's instances table; fleet drives provisioning
--                      / start / stop / version updates via
--                      instance_run_command over SSH.
--
-- Plain ADD COLUMN — no CHECK changes, no rebuild.

ALTER TABLE fleet_tenants ADD COLUMN instance_id INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_fleet_tenants_instance
    ON fleet_tenants(instance_id);
