-- Operational admission controls never change permissions or live media leases.
CREATE TABLE outbound_admission_rules (
 project_id TEXT NOT NULL,
 scope TEXT NOT NULL CHECK(scope IN ('provider','connection','number')),
 value TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,scope,value)
);
CREATE TABLE carrier_binding_drains (
 connection_id INTEGER PRIMARY KEY,
 change_id TEXT NOT NULL,
 removed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_calls_carrier_drain ON calls(carrier_connection_id,status);
CREATE INDEX idx_recordings_carrier_drain ON recordings(carrier_connection_id,storage_status);
CREATE TABLE runtime_binding_updates (
 id INTEGER PRIMARY KEY CHECK(id=1),
 change_id TEXT NOT NULL,
 request_json TEXT NOT NULL,
 status TEXT NOT NULL
);
-- Draining polls active work and a bounded callback grace window, not history.
CREATE INDEX idx_calls_carrier_active_drain ON calls(carrier_connection_id,id)
 WHERE status NOT IN ('completed','failed','no-answer','busy','canceled') OR media_active=1;
CREATE INDEX idx_calls_carrier_ended_drain ON calls(carrier_connection_id,ended_at);
