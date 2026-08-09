ALTER TABLE mqtt_message_log ADD COLUMN username TEXT NOT NULL DEFAULT '';
ALTER TABLE mqtt_message_log ADD COLUMN payload_size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mqtt_message_log ADD COLUMN payload_truncated INTEGER NOT NULL DEFAULT 0;
UPDATE mqtt_message_log SET payload_size = length(payload) WHERE payload_size = 0;
CREATE INDEX IF NOT EXISTS idx_msg_log_username ON mqtt_message_log(username);
