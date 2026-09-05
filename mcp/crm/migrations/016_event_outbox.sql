CREATE TABLE crm_event_outbox (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 topic TEXT NOT NULL,
 payload TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 delivered_at TEXT,
 attempts INTEGER NOT NULL DEFAULT 0,
 last_error TEXT
);
CREATE INDEX ix_crm_outbox_pending ON crm_event_outbox(id) WHERE delivered_at IS NULL;
CREATE TRIGGER crm_membership_added AFTER INSERT ON contact_list_members BEGIN
 INSERT INTO crm_event_outbox(project_id,topic,payload) VALUES(NEW.project_id,'list.member.added',json_object('list_id',NEW.list_id,'contact_id',NEW.contact_id));
END;
CREATE TRIGGER crm_membership_removed AFTER DELETE ON contact_list_members BEGIN
 INSERT INTO crm_event_outbox(project_id,topic,payload) VALUES(OLD.project_id,'list.member.removed',json_object('list_id',OLD.list_id,'contact_id',OLD.contact_id));
END;
