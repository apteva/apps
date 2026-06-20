ALTER TABLE calls ADD COLUMN carrier_slug TEXT NOT NULL DEFAULT 'twilio';
ALTER TABLE calls ADD COLUMN carrier_connection_id INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_calls_carrier ON calls(carrier_slug, carrier_connection_id);
