-- LLM Gateway v0.4.0
--
-- Column-aware repair and index creation run in ensureGatewaySchema. SQLite
-- does not support the conditional ALTER TABLE statements needed to recover
-- every released and partially applied v0.3 schema in static SQL.

SELECT 1;
