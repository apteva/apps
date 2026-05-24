-- v0.2.2: persist the managed DNS apex/name used to create a zone.
--
-- Hostname remains the canonical public host, but managed-domain UI
-- creation passes the exact domains-app inventory row plus subdomain.
-- Persisting those fields lets delete call domains with the same
-- apex/name instead of re-splitting the hostname.
ALTER TABLE zones ADD COLUMN dns_domain TEXT NOT NULL DEFAULT '';
ALTER TABLE zones ADD COLUMN dns_name TEXT NOT NULL DEFAULT '';
