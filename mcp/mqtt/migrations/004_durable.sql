-- Full packet bodies are separate from diagnostic previews. Journal positions
-- are monotonically increasing and never reused; acknowledgement cursors persist.
CREATE TABLE mqtt_journal (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 message_id TEXT NOT NULL UNIQUE,
 scope_id TEXT NOT NULL,
 topic TEXT NOT NULL,
 packet BLOB NOT NULL,
 username TEXT NOT NULL DEFAULT '',
 accepted_at INTEGER NOT NULL
);
CREATE INDEX mqtt_journal_scope_seq ON mqtt_journal(scope_id, seq);
CREATE TABLE mqtt_sessions (
 client_id TEXT PRIMARY KEY,
 scope_id TEXT NOT NULL,
 credential_id INTEGER NOT NULL,
 revision INTEGER NOT NULL,
 snapshot BLOB NOT NULL,
 last_seen INTEGER NOT NULL
);
CREATE INDEX mqtt_sessions_scope ON mqtt_sessions(scope_id);
CREATE TABLE mqtt_consumers (
 scope_id TEXT NOT NULL,
 owner TEXT NOT NULL,
 name TEXT NOT NULL,
 topic_filter TEXT NOT NULL,
 cursor INTEGER NOT NULL DEFAULT 0,
 lease_end INTEGER NOT NULL DEFAULT 0,
 lease_token TEXT NOT NULL DEFAULT '',
 lease_until INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(scope_id, owner, name)
);
ALTER TABLE mqtt_retained ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0;
CREATE TABLE mqtt_journal_floor(scope_id TEXT PRIMARY KEY, seq INTEGER NOT NULL);
-- Capacity accounting shares the acceptance/pruning transaction.
CREATE TABLE mqtt_journal_usage(scope_id TEXT PRIMARY KEY, messages INTEGER NOT NULL, bytes INTEGER NOT NULL);
CREATE TRIGGER mqtt_journal_usage_insert AFTER INSERT ON mqtt_journal BEGIN
 INSERT INTO mqtt_journal_usage(scope_id,messages,bytes) VALUES(new.scope_id,1,length(new.packet))
 ON CONFLICT(scope_id) DO UPDATE SET messages=messages+1,bytes=bytes+length(new.packet);
END;
CREATE TRIGGER mqtt_journal_usage_delete AFTER DELETE ON mqtt_journal BEGIN
 UPDATE mqtt_journal_usage SET messages=messages-1,bytes=bytes-length(old.packet) WHERE scope_id=old.scope_id;
END;
ALTER TABLE mqtt_sessions ADD COLUMN connected INTEGER NOT NULL DEFAULT 0;
