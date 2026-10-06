-- Additive ingress protections. Historical messages are retained unchanged;
-- unfinished work must pass the ownership checks before it can be routed.
ALTER TABLE messages ADD COLUMN receiving_identity TEXT NOT NULL DEFAULT '';
CREATE TABLE inbound_delivery_keys (
  provider TEXT NOT NULL,
  delivery_key TEXT NOT NULL,
  project_id TEXT NOT NULL,
  message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  PRIMARY KEY(provider, delivery_key)
);
CREATE INDEX ix_inbound_delivery_message ON inbound_delivery_keys(message_id);
CREATE INDEX ix_msg_global_inbound_s3 ON messages(s3_key)
  WHERE direction='in' AND channel='email' AND provider_slug IN ('','aws-ses');
