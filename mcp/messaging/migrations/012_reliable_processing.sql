-- Provider delivery IDs, rather than sender-controlled RFC Message-ID, deduplicate mail.
DROP INDEX IF EXISTS ux_msg_inbound_msgid;
ALTER TABLE messages ADD COLUMN envelope_recipients TEXT NOT NULL DEFAULT '[]';
ALTER TABLE messages ADD COLUMN from_canonical TEXT NOT NULL DEFAULT '';
UPDATE messages SET from_canonical = lower(trim(CASE WHEN instr(from_addr,'<') > 0 AND instr(from_addr,'>') > instr(from_addr,'<') THEN substr(from_addr,instr(from_addr,'<')+1,instr(from_addr,'>')-instr(from_addr,'<')-1) ELSE from_addr END));
CREATE INDEX ix_messages_sender ON messages(project_id, from_canonical, created_at, id);
CREATE TRIGGER messages_canonical_insert AFTER INSERT ON messages BEGIN
 UPDATE messages SET from_canonical=lower(trim(CASE WHEN instr(NEW.from_addr,'<') > 0 AND instr(NEW.from_addr,'>') > instr(NEW.from_addr,'<') THEN substr(NEW.from_addr,instr(NEW.from_addr,'<')+1,instr(NEW.from_addr,'>')-instr(NEW.from_addr,'<')-1) ELSE NEW.from_addr END)) WHERE id=NEW.id;
END;
CREATE TABLE inbound_jobs (
 message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL, source_kind TEXT NOT NULL, source TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
 lease_until INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '', lease_token TEXT NOT NULL DEFAULT ''
);
CREATE INDEX ix_inbound_jobs_due ON inbound_jobs(status,next_attempt,lease_until);
-- Legacy unfinished work can still resume routing; raw source was not retained previously.
INSERT INTO inbound_jobs(message_id,project_id,source_kind,source)
 SELECT id,project_id,'legacy','{}' FROM messages WHERE direction='in' AND route_status IN ('pending','target_failed');
CREATE TABLE unmatched_provider_events (
 id INTEGER PRIMARY KEY, project_id TEXT NOT NULL, provider_id TEXT NOT NULL,
 event_id TEXT NOT NULL, topic_arn TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(project_id,event_id)
);
CREATE INDEX ix_unmatched_provider_id ON unmatched_provider_events(provider_id);
CREATE TABLE recipient_delivery_status (
 message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 recipient TEXT NOT NULL, status TEXT NOT NULL, occurred_at TEXT NOT NULL,
 PRIMARY KEY(message_id,recipient)
);
CREATE TABLE messaging_settings (name TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TRIGGER messages_canonical_update AFTER UPDATE OF from_addr ON messages BEGIN
 UPDATE messages SET from_canonical=lower(trim(CASE WHEN instr(NEW.from_addr,'<') > 0 AND instr(NEW.from_addr,'>') > instr(NEW.from_addr,'<') THEN substr(NEW.from_addr,instr(NEW.from_addr,'<')+1,instr(NEW.from_addr,'>')-instr(NEW.from_addr,'<')-1) ELSE NEW.from_addr END)) WHERE id=NEW.id;
END;
-- Backfill deterministic per-recipient outcomes from historical delivery events.
INSERT INTO recipient_delivery_status(message_id,recipient,status,occurred_at)
 SELECT message_id,recipient,status,occurred_at FROM (
 SELECT message_id,lower(recipient) AS recipient,CASE kind WHEN 'rejected' THEN 'failed' ELSE kind END AS status,occurred_at,
 row_number() OVER (PARTITION BY message_id,lower(recipient) ORDER BY CASE kind WHEN 'complained' THEN 120 WHEN 'bounced' THEN 110 WHEN 'failed' THEN 100 WHEN 'rejected' THEN 100 WHEN 'clicked' THEN 50 WHEN 'opened' THEN 40 WHEN 'delivered' THEN 30 WHEN 'sent' THEN 20 ELSE 0 END DESC,julianday(occurred_at) DESC,id DESC) AS n
 FROM delivery_events WHERE recipient IS NOT NULL AND recipient!='' AND kind IN ('sent','delivered','opened','clicked','failed','rejected','bounced','complained')
 ) WHERE n=1;
