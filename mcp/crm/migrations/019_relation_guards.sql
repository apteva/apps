CREATE TRIGGER crm_channel_owner_insert BEFORE INSERT ON contact_channels BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM contacts WHERE project_id=NEW.project_id AND id=NEW.contact_id AND deleted_at IS NULL AND status!='merged') THEN RAISE(ABORT,'channel contact not found in this project') END;
END;
CREATE TRIGGER crm_membership_owner_insert BEFORE INSERT ON contact_list_members BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM contacts WHERE project_id=NEW.project_id AND id=NEW.contact_id AND deleted_at IS NULL AND status!='merged') THEN RAISE(ABORT,'membership contact not found in this project') END;
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM contact_lists WHERE project_id=NEW.project_id AND id=NEW.list_id AND archived_at IS NULL) THEN RAISE(ABORT,'membership list not found in this project') END;
END;
CREATE TRIGGER crm_routing_list_insert BEFORE INSERT ON routing_rules WHEN NEW.add_list_id IS NOT NULL BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM contact_lists WHERE project_id=NEW.project_id AND id=NEW.add_list_id AND archived_at IS NULL) THEN RAISE(ABORT,'routing list not found in this project') END;
END;
CREATE TRIGGER crm_routing_list_update BEFORE UPDATE OF add_list_id,project_id ON routing_rules WHEN NEW.add_list_id IS NOT NULL BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM contact_lists WHERE project_id=NEW.project_id AND id=NEW.add_list_id AND archived_at IS NULL) THEN RAISE(ABORT,'routing list not found in this project') END;
END;
CREATE INDEX ix_crm_contact_chronological ON contacts(project_id,julianday(updated_at) DESC,id DESC);
CREATE INDEX ix_crm_activity_chronological ON contact_activities(project_id,conversation_id,julianday(occurred_at) DESC,id DESC);
CREATE INDEX ix_crm_timeline_chronological ON contact_activities(project_id,contact_id,julianday(occurred_at) DESC,id DESC);
CREATE INDEX ix_crm_inbox_chronological ON contact_conversations(project_id,julianday(last_activity_at) DESC,id DESC);
CREATE TABLE crm_delivery_recoveries(id INTEGER PRIMARY KEY,project_id TEXT NOT NULL,channel_id INTEGER NOT NULL,transport TEXT NOT NULL,reason TEXT NOT NULL,previous_evidence TEXT,created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')));
