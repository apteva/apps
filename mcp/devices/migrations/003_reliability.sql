-- Preserve v1 data. Old credentials require MQTT 0.4 migration and client review.
UPDATE devices SET enabled=0, status='requires_enrollment', availability='offline';
ALTER TABLE devices ADD COLUMN topic_root TEXT NOT NULL DEFAULT '';
ALTER TABLE device_state ADD COLUMN observed_at TEXT NOT NULL DEFAULT '';
ALTER TABLE device_state ADD COLUMN message_id TEXT NOT NULL DEFAULT '';
ALTER TABLE device_state ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE device_state ADD COLUMN sequence INTEGER;
ALTER TABLE device_commands ADD COLUMN fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE device_commands ADD COLUMN next_attempt_at TEXT;
ALTER TABLE device_commands ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
CREATE TABLE device_operations (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL, device_id TEXT NOT NULL,
 kind TEXT NOT NULL, idempotency_key TEXT NOT NULL, fingerprint TEXT NOT NULL,
 arguments_json TEXT NOT NULL, secret_cipher TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'pending', error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, completed_at TEXT, secret_expires_at TEXT,
 UNIQUE(project_id,kind,idempotency_key)
);
CREATE UNIQUE INDEX device_operation_active ON device_operations(project_id,device_id) WHERE status='pending';
CREATE TABLE device_messages (
 id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL,
 device_id TEXT NOT NULL, message_id TEXT NOT NULL, fingerprint TEXT NOT NULL,
 kind TEXT NOT NULL, data_json TEXT NOT NULL, metadata_json TEXT NOT NULL,
 observed_at TEXT NOT NULL, received_at TEXT NOT NULL,
 session_id TEXT NOT NULL DEFAULT '', sequence INTEGER,
 UNIQUE(project_id,device_id,message_id)
);
CREATE INDEX device_message_history ON device_messages(project_id,device_id,id);
CREATE TABLE device_outbox (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE,
 project_id TEXT NOT NULL, device_id TEXT NOT NULL, topic TEXT NOT NULL,
 data_json TEXT NOT NULL, created_at TEXT NOT NULL, delivered_at TEXT
);
CREATE INDEX device_outbox_pending ON device_outbox(delivered_at,seq);
CREATE TABLE device_usage (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL,
 device_id TEXT NOT NULL, measurement TEXT NOT NULL, quantity INTEGER NOT NULL,
 source_id TEXT NOT NULL, created_at TEXT NOT NULL,
 UNIQUE(project_id,device_id,measurement,source_id)
);
CREATE INDEX device_usage_scope ON device_usage(project_id,seq);
CREATE TABLE device_tombstones (
 project_id TEXT NOT NULL, device_id TEXT NOT NULL, deleted_at TEXT NOT NULL,
 PRIMARY KEY(project_id,device_id)
);
CREATE TABLE device_streams (
 device_id TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
 observed_at TEXT NOT NULL, message_id TEXT NOT NULL,
 session_id TEXT NOT NULL DEFAULT '', sequence INTEGER
);
CREATE TABLE device_responses (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL,
 device_id TEXT NOT NULL, command_id TEXT NOT NULL,
 fingerprint TEXT NOT NULL, response_json TEXT NOT NULL,
 disposition TEXT NOT NULL, created_at TEXT NOT NULL,
 UNIQUE(project_id,command_id,fingerprint)
);
CREATE INDEX device_responses_scope ON device_responses(project_id,seq);
-- Keep pre-v0.2 telemetry queryable without inventing historical usage charges.
INSERT INTO device_messages(project_id,device_id,message_id,fingerprint,kind,data_json,metadata_json,observed_at,received_at)
 SELECT d.project_id,t.device_id,'legacy-import-'||t.id,'','telemetry',t.payload_json,'{"imported_from":"v0.1"}',t.received_at,t.received_at
 FROM device_telemetry t JOIN devices d ON d.id=t.device_id;
UPDATE device_state SET observed_at=updated_at WHERE observed_at='';
CREATE TABLE device_retention (
 project_id TEXT NOT NULL, kind TEXT NOT NULL, through_seq INTEGER NOT NULL,
 PRIMARY KEY(project_id,kind)
);
ALTER TABLE device_commands ADD COLUMN output_schema_json TEXT NOT NULL DEFAULT '{}';
CREATE TABLE device_installation (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), project_id TEXT NOT NULL
);
