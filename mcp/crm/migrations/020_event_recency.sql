ALTER TABLE contact_channel_delivery_state ADD COLUMN suppression_event_at TEXT;
CREATE TRIGGER crm_tag_touch_insert AFTER INSERT ON contact_tags BEGIN
 UPDATE contacts SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ',MAX(julianday('now'),COALESCE(julianday(updated_at),0)+1.0/86400000.0)) WHERE id=NEW.contact_id AND project_id=NEW.project_id;
END;
CREATE TRIGGER crm_tag_touch_delete AFTER DELETE ON contact_tags BEGIN
 UPDATE contacts SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ',MAX(julianday('now'),COALESCE(julianday(updated_at),0)+1.0/86400000.0)) WHERE id=OLD.contact_id AND project_id=OLD.project_id;
END;
CREATE TRIGGER crm_list_touch_insert AFTER INSERT ON contact_list_members BEGIN
 UPDATE contacts SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ',MAX(julianday('now'),COALESCE(julianday(updated_at),0)+1.0/86400000.0)) WHERE id=NEW.contact_id AND project_id=NEW.project_id;
END;
CREATE TRIGGER crm_list_touch_delete AFTER DELETE ON contact_list_members BEGIN
 UPDATE contacts SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ',MAX(julianday('now'),COALESCE(julianday(updated_at),0)+1.0/86400000.0)) WHERE id=OLD.contact_id AND project_id=OLD.project_id;
END;
