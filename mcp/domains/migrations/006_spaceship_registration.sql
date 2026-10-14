-- Preserve exactly the options approved for a Spaceship purchase and its async handle.
ALTER TABLE registration_intents ADD COLUMN request_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE registration_intents ADD COLUMN billing_mode TEXT NOT NULL DEFAULT 'quoted';
ALTER TABLE registration_intents ADD COLUMN operation_id TEXT NOT NULL DEFAULT '';
