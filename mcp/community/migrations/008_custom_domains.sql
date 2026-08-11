-- Community v0.11: one managed public hostname per community.
-- portal_host remains the canonical public URL. The additional fields record
-- only DNS ownership so detach can remove the exact record Community created.

ALTER TABLE communities ADD COLUMN portal_dns_managed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE communities ADD COLUMN portal_dns_domain TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN portal_dns_name TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN portal_dns_type TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN portal_dns_value TEXT NOT NULL DEFAULT '';
ALTER TABLE communities ADD COLUMN portal_domain_error TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_communities_portal_host
    ON communities(lower(portal_host))
    WHERE portal_dns_domain <> '' AND portal_host <> '';
