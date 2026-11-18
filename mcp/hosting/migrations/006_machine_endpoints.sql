-- How Instances reaches each machine, and where customers reach it.
-- endpoint_source: 'instances' (worked out from Instances on every refresh)
-- or 'manual' (typed by an operator; the refresh leaves it alone).
-- endpoint_issue explains why a machine has no customer endpoint (for
-- example a reverse-tunnel Mac without public SSH); such a machine is not
-- for sale.
ALTER TABLE hosting_machines ADD COLUMN connect_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE hosting_machines ADD COLUMN endpoint_source TEXT NOT NULL DEFAULT 'instances';
ALTER TABLE hosting_machines ADD COLUMN endpoint_issue TEXT NOT NULL DEFAULT '';
