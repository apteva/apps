-- Transport selection is admission-only; existing calls retain ingress_path.
CREATE TABLE outbound_transports (
 project_id TEXT NOT NULL,
 connection_id INTEGER NOT NULL,
 phone_number TEXT NOT NULL DEFAULT '',
 provider TEXT NOT NULL,
 transport TEXT NOT NULL CHECK(transport IN ('programmable_websocket','sip_direct')),
 config_json TEXT NOT NULL DEFAULT '{}',
 credentials_cipher BLOB,
 verified_at TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(project_id,connection_id,phone_number)
);
CREATE TABLE outbound_sip_calls (
 call_id TEXT PRIMARY KEY REFERENCES calls(id),
 resource_id TEXT NOT NULL,
 host TEXT NOT NULL,
 codecs_json TEXT NOT NULL
);
