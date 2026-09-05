-- Recover historical archives and merged identities without deleting history.
UPDATE contacts SET deleted_at=NULL WHERE status='archived';
UPDATE contacts SET primary_email=NULL, primary_phone=NULL WHERE status='merged';
UPDATE contact_channels SET is_primary=0 WHERE is_primary=1 AND id NOT IN
 (SELECT MIN(id) FROM contact_channels WHERE is_primary=1 GROUP BY project_id,contact_id,kind);
CREATE UNIQUE INDEX ux_channel_primary ON contact_channels(project_id,contact_id,kind) WHERE is_primary=1;
CREATE INDEX ix_activity_preview ON contact_activities(project_id,conversation_id,occurred_at DESC,id DESC);
CREATE TRIGGER crm_activity_owner BEFORE INSERT ON contact_activities BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM contacts WHERE project_id=NEW.project_id AND id=NEW.contact_id AND deleted_at IS NULL AND status!='merged') THEN RAISE(ABORT,'contact not found in this project') END;
 SELECT CASE WHEN NEW.conversation_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM contact_conversations WHERE project_id=NEW.project_id AND id=NEW.conversation_id AND contact_id=NEW.contact_id) THEN RAISE(ABORT,'conversation does not belong to contact') END;
END;
CREATE TRIGGER crm_attribute_touch_insert AFTER INSERT ON contact_attributes BEGIN
 UPDATE contacts SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ',MAX(julianday('now'),COALESCE(julianday(updated_at),0)+1.0/86400000.0)) WHERE id=NEW.contact_id AND project_id=NEW.project_id;
END;
CREATE TRIGGER crm_attribute_touch_update AFTER UPDATE ON contact_attributes BEGIN
 UPDATE contacts SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ',MAX(julianday('now'),COALESCE(julianday(updated_at),0)+1.0/86400000.0)) WHERE id=NEW.contact_id AND project_id=NEW.project_id;
END;
INSERT OR IGNORE INTO contact_attribute_defs(project_id,key,label,type,enum_values,sort_order,is_system)
 SELECT DISTINCT c.project_id,t.key,t.label,t.type,t.enum_values,t.sort_order,1 FROM contacts c CROSS JOIN _system_attribute_templates t;
