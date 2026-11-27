-- SaaS v0.14.0 -- per-unit meter prices for quantity meters (e.g. a Catalog
-- metered price per minute), used for usage beyond a plan's included
-- allowance. Included allowances themselves live in plan metadata and in
-- Credits lots, so no other table is needed.
ALTER TABLE saas_plan_meters ADD COLUMN unit_price_id INTEGER;
