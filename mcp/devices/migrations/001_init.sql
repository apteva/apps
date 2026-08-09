PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS devices (
    id                 TEXT PRIMARY KEY,
    project_id         TEXT NOT NULL,
    name               TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    protocol           TEXT NOT NULL DEFAULT 'arest-mqtt/v1',
    model              TEXT NOT NULL DEFAULT '',
    manufacturer       TEXT NOT NULL DEFAULT '',
    firmware           TEXT NOT NULL DEFAULT '',
    mqtt_username      TEXT NOT NULL UNIQUE,
    mqtt_client_id     TEXT NOT NULL DEFAULT '',
    enabled            INTEGER NOT NULL DEFAULT 1,
    status             TEXT NOT NULL DEFAULT 'provisioned',
    availability       TEXT NOT NULL DEFAULT 'unknown',
    manifest_json      TEXT NOT NULL DEFAULT '{}',
    metadata_json      TEXT NOT NULL DEFAULT '{}',
    credential_version INTEGER NOT NULL DEFAULT 1,
    last_seen          TEXT,
    connected_at       TEXT,
    disconnected_at    TEXT,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX IF NOT EXISTS idx_devices_project_status ON devices(project_id, status);
CREATE INDEX IF NOT EXISTS idx_devices_username ON devices(mqtt_username);

CREATE TABLE IF NOT EXISTS device_state (
    device_id   TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    key         TEXT NOT NULL,
    value_json  TEXT NOT NULL,
    value_type  TEXT NOT NULL DEFAULT 'unknown',
    unit        TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT 'state',
    updated_at  TEXT NOT NULL,
    PRIMARY KEY(device_id, key)
);

CREATE TABLE IF NOT EXISTS device_commands (
    id               TEXT PRIMARY KEY,
    device_id        TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    operation        TEXT NOT NULL,
    target           TEXT NOT NULL DEFAULT '',
    arguments_json   TEXT NOT NULL DEFAULT '{}',
    request_json     TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'queued',
    result_json      TEXT,
    error            TEXT NOT NULL DEFAULT '',
    idempotency_key  TEXT,
    timeout_ms       INTEGER NOT NULL,
    created_at       TEXT NOT NULL,
    sent_at          TEXT,
    deadline_at      TEXT NOT NULL,
    completed_at     TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_device_commands_idempotency
    ON device_commands(device_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_device_commands_recent ON device_commands(device_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_device_commands_status_deadline ON device_commands(status, deadline_at);

CREATE TABLE IF NOT EXISTS device_telemetry (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id     TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    payload_json  TEXT NOT NULL,
    received_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_device_telemetry_recent ON device_telemetry(device_id, received_at DESC);

CREATE TABLE IF NOT EXISTS device_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id   TEXT REFERENCES devices(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,
    data_json   TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_device_events_recent ON device_events(device_id, created_at DESC);
