CREATE TABLE IF NOT EXISTS provider_orders (
 id TEXT PRIMARY KEY,
 provider TEXT NOT NULL,
 connection_id INTEGER NOT NULL,
 request_key TEXT NOT NULL,
 instance_id INTEGER,
 input_json TEXT NOT NULL,
 state_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(provider,connection_id,request_key)
);
CREATE INDEX IF NOT EXISTS provider_orders_instance ON provider_orders(instance_id);
CREATE INDEX IF NOT EXISTS provider_orders_unclaimed ON provider_orders(created_at) WHERE instance_id IS NULL;
