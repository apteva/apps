-- Domains v0.3 — per-domain DNS connection.
--
-- v0.2 routed every record op through the install's role binding
-- ("dns_provider"), so operators could only have one provider per
-- install. v0.3 lets each domain row pin its own connection — so a
-- single install can manage acme.com on Porkbun account A and
-- shop.example on Namecheap, mix and match.
--
-- Backward compatibility: rows with connection_id IS NULL fall back
-- to the role binding at call time, so pre-v0.3 rows keep working
-- without a backfill.

ALTER TABLE domains ADD COLUMN connection_id INTEGER;

CREATE INDEX ix_domains_connection
  ON domains(connection_id)
  WHERE deleted_at IS NULL AND connection_id IS NOT NULL;
