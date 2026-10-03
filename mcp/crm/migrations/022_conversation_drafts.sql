-- Saved replies are deliberately separate from the append-only activity log.
-- No historical rows are rewritten or backfilled by this migration.
CREATE TABLE conversation_drafts (
 id INTEGER PRIMARY KEY,
 project_id TEXT NOT NULL,
 contact_id INTEGER NOT NULL REFERENCES contacts(id),
 conversation_id INTEGER NOT NULL REFERENCES contact_conversations(id),
 reply_to_activity_id INTEGER NOT NULL,
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
 UNIQUE(project_id,client_key)
);
CREATE INDEX ix_crm_drafts_conversation ON conversation_drafts(project_id,conversation_id,status,updated_at DESC,id DESC);
CREATE INDEX ix_crm_drafts_contact ON conversation_drafts(project_id,contact_id);
CREATE TRIGGER crm_draft_owner_insert BEFORE INSERT ON conversation_drafts BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM contact_conversations WHERE project_id=NEW.project_id AND id=NEW.conversation_id AND contact_id=NEW.contact_id) THEN RAISE(ABORT,'draft conversation does not belong to contact/project') END;
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM contact_activities WHERE project_id=NEW.project_id AND id=NEW.reply_to_activity_id AND contact_id=NEW.contact_id AND conversation_id=NEW.conversation_id AND kind IN ('email_received','sms_received','whatsapp_received')) THEN RAISE(ABORT,'draft reply anchor not found in conversation') END;
END;
