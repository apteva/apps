-- Operator alerts: a presale promise in days on pool profiles, the date each
-- waiting tenant was promised, and which stuck alert was last sent (so each
-- one fires once).
ALTER TABLE hosting_profiles ADD COLUMN presale_ready_days INTEGER;
ALTER TABLE hosting_tenants ADD COLUMN due_at TIMESTAMP;
ALTER TABLE hosting_tenants ADD COLUMN stuck_alert TEXT NOT NULL DEFAULT '';
