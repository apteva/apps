-- mqtt — schema v0.1.
--
-- Six tables:
--   mqtt_users          username/password + topic ACL (publish + subscribe glob lists)
--   mqtt_acl_log        last N ACL decisions for debugging
--   mqtt_retained       broker-side retained-message store (across restarts)
--   mqtt_devices        HA-discovery device registry
--   mqtt_subscriptions  per-pattern bus-bridge subs (re-emit as mqtt.<bus_topic>)
--   mqtt_message_log    rolling last-N feed for the panel's Live tab
--
-- No project_id on mqtt_users / retained / acl_log: the broker is
-- single-tenant per install. mqtt_devices and mqtt_subscriptions
-- carry project_id for sanity (the broker scope follows install scope).

CREATE TABLE IF NOT EXISTS mqtt_users (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    username                 TEXT    NOT NULL UNIQUE,
    password_hash            TEXT    NOT NULL,                 -- bcrypt
    allow_publish_topics_json   TEXT NOT NULL DEFAULT '["#"]', -- glob list; "#" = anywhere
    allow_subscribe_topics_json TEXT NOT NULL DEFAULT '["#"]',
    enabled                  INTEGER NOT NULL DEFAULT 1,
    created_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS mqtt_acl_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    username   TEXT    NOT NULL,
    action     TEXT    NOT NULL CHECK(action IN ('connect','publish','subscribe')),
    topic      TEXT    NOT NULL DEFAULT '',
    allowed    INTEGER NOT NULL,
    reason     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_acl_log_ts ON mqtt_acl_log(ts);

CREATE TABLE IF NOT EXISTS mqtt_retained (
    topic        TEXT    PRIMARY KEY,
    payload      BLOB    NOT NULL,
    qos          INTEGER NOT NULL DEFAULT 0,
    properties   TEXT    NOT NULL DEFAULT '',
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS mqtt_devices (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT    NOT NULL,
    slug          TEXT    NOT NULL,         -- "homeassistant/<comp>/<obj>" key
    component     TEXT    NOT NULL,         -- light | sensor | binary_sensor | switch | climate | …
    object_id     TEXT    NOT NULL,
    display_name  TEXT    NOT NULL DEFAULT '',
    manufacturer  TEXT    NOT NULL DEFAULT '',
    model         TEXT    NOT NULL DEFAULT '',
    state_topic   TEXT    NOT NULL DEFAULT '',
    command_topic TEXT    NOT NULL DEFAULT '',
    ha_config_json TEXT   NOT NULL DEFAULT '{}',
    last_seen     TIMESTAMP,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, slug)
);

CREATE TABLE IF NOT EXISTS mqtt_subscriptions (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id     TEXT    NOT NULL,
    topic_pattern  TEXT    NOT NULL,         -- MQTT-style: foo/+/bar, foo/#
    bus_topic      TEXT    NOT NULL,         -- emitted as mqtt.<bus_topic>
    created_by     TEXT    NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, topic_pattern, bus_topic)
);

-- mqtt_message_log — bounded ring of recent broker traffic for the
-- panel's "Live" tab. Pruned by retention-sweep worker.
CREATE TABLE IF NOT EXISTS mqtt_message_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    topic       TEXT    NOT NULL,
    payload     BLOB    NOT NULL,
    qos         INTEGER NOT NULL DEFAULT 0,
    retain      INTEGER NOT NULL DEFAULT 0,
    client_id   TEXT    NOT NULL DEFAULT '',
    is_printable INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_msg_log_ts ON mqtt_message_log(ts);
CREATE INDEX IF NOT EXISTS idx_msg_log_topic ON mqtt_message_log(topic);
