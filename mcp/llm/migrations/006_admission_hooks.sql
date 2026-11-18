-- LLM Gateway v0.6.0
--
-- Admission hooks, settlement outbox, public model names, token metadata,
-- and request-log fields. Tables are created and columns added by
-- ensureGatewayV06Schema during OnMount, because SQLite cannot add columns
-- conditionally in static SQL and earlier releases left schemas in several
-- shapes. Keeping this tracked migration harmless lets every released schema
-- reach the runtime repair safely.

SELECT 1;
