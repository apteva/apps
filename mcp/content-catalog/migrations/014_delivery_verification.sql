-- Delivery verification is separate from provider readiness. Historical rows
-- stay pending/not_checked with no scheduled check; only an explicit
-- hosting_check or a new hosting result verifies them.
ALTER TABLE hostings ADD COLUMN delivery_status TEXT NOT NULL DEFAULT 'pending' CHECK(delivery_status IN ('pending','verified','failed'));
ALTER TABLE hostings ADD COLUMN delivery_reason TEXT NOT NULL DEFAULT 'not_checked';
ALTER TABLE hostings ADD COLUMN delivery_receipt TEXT NOT NULL DEFAULT '{}';
ALTER TABLE hostings ADD COLUMN delivery_verified_at TEXT NOT NULL DEFAULT '';
ALTER TABLE hostings ADD COLUMN delivery_attempts INTEGER NOT NULL DEFAULT 0;
CREATE INDEX ix_hostings_delivery_pending ON hostings(delivery_status,next_check_at);
