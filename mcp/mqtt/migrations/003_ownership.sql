-- Empty ownership is quarantined. OnMount assigns legacy rows only when the
-- install scope is known (or legacy_scope_id was explicitly configured).
ALTER TABLE mqtt_users ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mqtt_users ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE mqtt_retained ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mqtt_message_log ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mqtt_acl_log ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mqtt_subscriptions ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mqtt_devices ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_mqtt_users_scope ON mqtt_users(scope_id);
CREATE INDEX idx_mqtt_retained_scope ON mqtt_retained(scope_id, topic);
CREATE INDEX idx_mqtt_message_scope ON mqtt_message_log(scope_id, id);
CREATE INDEX idx_mqtt_acl_scope ON mqtt_acl_log(scope_id, id);
CREATE INDEX idx_mqtt_subscriptions_scope ON mqtt_subscriptions(scope_id, id);
CREATE INDEX idx_mqtt_devices_scope ON mqtt_devices(scope_id, id);

-- Unknown identities continue to produce unowned failed-auth audit records.
-- Only records that existed before this migration may be assigned as legacy
-- install data; a later restart must not claim new unknown identities.
CREATE TABLE mqtt_security_state (name TEXT PRIMARY KEY, value INTEGER NOT NULL);
INSERT INTO mqtt_security_state(name, value)
    SELECT 'legacy_acl_max_id', COALESCE(MAX(id), 0) FROM mqtt_acl_log;

-- Bindings intentionally survive credential deletion. Recreating the same
-- username gets a different AUTOINCREMENT credential id and cannot inherit it.
CREATE TABLE mqtt_client_owners (
    client_id TEXT PRIMARY KEY,
    scope_id TEXT NOT NULL,
    credential_id INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
