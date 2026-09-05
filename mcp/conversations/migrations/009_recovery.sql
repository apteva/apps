-- Each content mutation has a durable replay position, independent of message id.
CREATE TABLE message_changes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_message_changes_conversation ON message_changes(conversation_id,id);
INSERT INTO message_changes(message_id,conversation_id) SELECT id,conversation_id FROM messages ORDER BY id;
ALTER TABLE messages ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
UPDATE messages SET revision=(SELECT MAX(id) FROM message_changes WHERE message_id=messages.id);
ALTER TABLE messages ADD COLUMN dismissed INTEGER NOT NULL DEFAULT 0;
UPDATE messages SET dismissed=EXISTS(SELECT 1 FROM json_each(messages.components_json) WHERE json_extract(value,'$.props.dismissed')=1);
CREATE TRIGGER messages_created AFTER INSERT ON messages BEGIN
    INSERT INTO message_changes(message_id,conversation_id) VALUES(NEW.id,NEW.conversation_id);
    UPDATE messages SET revision=last_insert_rowid() WHERE id=NEW.id;
END;
CREATE TRIGGER messages_changed AFTER UPDATE OF components_json,action_status,content ON messages BEGIN
    INSERT INTO message_changes(message_id,conversation_id) VALUES(NEW.id,NEW.conversation_id);
    UPDATE messages SET revision=last_insert_rowid(),dismissed=EXISTS(SELECT 1 FROM json_each(NEW.components_json) WHERE json_extract(value,'$.props.dismissed')=1) WHERE id=NEW.id;
    UPDATE deliveries SET status=CASE WHEN status IN ('processing','ambiguous','cancelled') THEN status ELSE 'pending' END,
        generation=generation+1, attempts=0, next_attempt_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
        WHERE message_id=NEW.id AND (target LIKE 'web:%' OR target LIKE 'telegram:%');
END;
ALTER TABLE deliveries ADD COLUMN lease_token TEXT NOT NULL DEFAULT '';
ALTER TABLE deliveries ADD COLUMN lease_until INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deliveries ADD COLUMN generation INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_deliveries_route_order ON deliveries(target,status,message_id);
-- A claimed inbound update is not a completed update. No unapproved content is retained.
ALTER TABLE telegram_updates ADD COLUMN completed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE telegram_updates ADD COLUMN lease_until INTEGER NOT NULL DEFAULT 0;
ALTER TABLE telegram_updates ADD COLUMN lease_token TEXT NOT NULL DEFAULT '';
-- Archiving cancels queued work atomically. An already accepted provider request cannot be recalled.
CREATE TRIGGER conversations_archived AFTER UPDATE OF archived_at ON conversations
WHEN NEW.archived_at IS NOT NULL BEGIN
    UPDATE conversations SET conversation_key='' WHERE id=NEW.id AND conversation_key LIKE 'topic:%';
    UPDATE deliveries SET status='cancelled',lease_token='',last_error='Conversation archived',updated_at=CURRENT_TIMESTAMP
        WHERE message_id IN (SELECT id FROM messages WHERE conversation_id=NEW.id) AND status IN ('pending','processing');
END;
CREATE TABLE retired_conversation_threads (
    project_id TEXT NOT NULL, agent_id INTEGER NOT NULL, thread_id TEXT NOT NULL,
    PRIMARY KEY(project_id,agent_id,thread_id)
);
CREATE TRIGGER conversations_retired BEFORE DELETE ON conversations BEGIN
    INSERT OR IGNORE INTO retired_conversation_threads SELECT OLD.project_id,agent_id,thread_id FROM conversation_agent_threads WHERE conversation_id=OLD.id;
    INSERT OR IGNORE INTO retired_conversation_threads SELECT OLD.project_id,OLD.lead_agent_id,OLD.thread_id WHERE OLD.thread_id!='';
END;
ALTER TABLE messages ADD COLUMN request_hash TEXT NOT NULL DEFAULT '';
CREATE TABLE telegram_message_parts (
 binding_id TEXT NOT NULL REFERENCES telegram_bindings(id) ON DELETE CASCADE,
 message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 part INTEGER NOT NULL,
 telegram_message_id INTEGER NOT NULL,
 content TEXT NOT NULL,
 PRIMARY KEY(binding_id,message_id,part)
);
CREATE TABLE telegram_command_receipts (
 connection_id INTEGER NOT NULL,update_id INTEGER NOT NULL,
 conversation_id TEXT NOT NULL,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(connection_id,update_id)
);
CREATE TABLE approval_lifecycle (
 message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
 delivery_id TEXT NOT NULL UNIQUE,
 execution_id TEXT NOT NULL,
 sequence INTEGER NOT NULL,
 state TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
