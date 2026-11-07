-- The SDK commits this rebuild atomically. Copy every existing field verbatim;
-- existing reply drafts keep their anchors, revisions and delivery retry keys.
CREATE TABLE conversation_drafts_next (
 id INTEGER PRIMARY KEY,
 project_id TEXT NOT NULL,
 contact_id INTEGER NOT NULL REFERENCES contacts(id),
 conversation_id INTEGER REFERENCES contact_conversations(id),
 reply_to_activity_id INTEGER,
 mode TEXT NOT NULL DEFAULT 'reply' CHECK(mode IN ('reply','message')),
 content TEXT NOT NULL CHECK(json_valid(content)),
 status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','sending','send_failed','sent','discarded')),
 revision INTEGER NOT NULL DEFAULT 1,
 client_key TEXT NOT NULL,
 created_by TEXT NOT NULL,
 updated_by TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 messaging_install_id INTEGER NOT NULL DEFAULT 0,
 attempt_key TEXT NOT NULL DEFAULT '',
 lease_until TEXT NOT NULL DEFAULT '',
 dispatched INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '',
 sent_result TEXT,
 CHECK((mode='reply' AND conversation_id IS NOT NULL AND reply_to_activity_id IS NOT NULL) OR (mode='message' AND reply_to_activity_id IS NULL)),
 UNIQUE(project_id,client_key)
);
INSERT INTO conversation_drafts_next(id,project_id,contact_id,conversation_id,reply_to_activity_id,content,status,revision,client_key,created_by,updated_by,created_at,updated_at,messaging_install_id,attempt_key,lease_until,dispatched,last_error,sent_result)
 SELECT id,project_id,contact_id,conversation_id,reply_to_activity_id,content,status,revision,client_key,created_by,updated_by,created_at,updated_at,messaging_install_id,attempt_key,lease_until,dispatched,last_error,sent_result FROM conversation_drafts;
DROP TABLE conversation_drafts;
ALTER TABLE conversation_drafts_next RENAME TO conversation_drafts;
CREATE INDEX ix_crm_drafts_conversation ON conversation_drafts(project_id,conversation_id,status,updated_at DESC,id DESC);
CREATE INDEX ix_crm_drafts_contact ON conversation_drafts(project_id,contact_id);
CREATE TRIGGER crm_draft_owner_insert BEFORE INSERT ON conversation_drafts BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM contacts WHERE project_id=NEW.project_id AND id=NEW.contact_id) THEN RAISE(ABORT,'draft contact not found in project') END;
 SELECT CASE WHEN NEW.conversation_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM contact_conversations WHERE project_id=NEW.project_id AND id=NEW.conversation_id AND contact_id=NEW.contact_id) THEN RAISE(ABORT,'draft conversation does not belong to contact/project') END;
 SELECT CASE WHEN NEW.mode='reply' AND NOT EXISTS(SELECT 1 FROM contact_activities WHERE project_id=NEW.project_id AND id=NEW.reply_to_activity_id AND contact_id=NEW.contact_id AND conversation_id=NEW.conversation_id AND kind IN ('email_received','sms_received','whatsapp_received')) THEN RAISE(ABORT,'draft reply anchor not found in conversation') END;
END;
