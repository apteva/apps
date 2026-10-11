-- A resumable setup journal. It is separate from the active admission settings:
-- incomplete provider setup never changes the transport used by new calls.
CREATE TABLE outbound_sip_setup (
 project_id TEXT NOT NULL,
 connection_id INTEGER NOT NULL,
 phone_number TEXT NOT NULL DEFAULT '',
 provider TEXT NOT NULL,
 state_json TEXT NOT NULL,
 credentials_cipher BLOB NOT NULL,
 PRIMARY KEY(project_id, connection_id, phone_number)
);
