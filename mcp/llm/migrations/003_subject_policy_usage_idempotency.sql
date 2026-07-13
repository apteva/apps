-- LLM Gateway v0.3.0
--
-- Schema repair is intentionally performed by ensureGatewaySchema during
-- OnMount. The original v0.3 migration was not transactional and could leave
-- a database half-migrated when legacy request ids were duplicated. Keeping
-- this tracked migration harmless lets both untouched v0.2 databases and
-- partially upgraded v0.3 databases reach the runtime repair safely.

SELECT 1;
