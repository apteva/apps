CREATE TRIGGER crm_opportunity_insert_guard BEFORE INSERT ON crm_opportunities BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM contacts WHERE id=NEW.contact_id AND project_id=NEW.project_id AND deleted_at IS NULL AND status!='merged') THEN RAISE(ABORT,'contact not found') END;
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM crm_pipeline_stages s JOIN crm_pipelines p ON p.id=s.pipeline_id AND p.project_id=s.project_id WHERE s.id=NEW.stage_id AND s.project_id=NEW.project_id AND s.pipeline_id=NEW.pipeline_id AND s.archived_at IS NULL AND p.archived_at IS NULL AND s.category=NEW.status) THEN RAISE(ABORT,'stage/status mismatch or archived pipeline') END;
END;
CREATE TRIGGER crm_opportunity_update_guard BEFORE UPDATE OF stage_id,status,pipeline_id ON crm_opportunities BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM crm_pipeline_stages WHERE id=NEW.stage_id AND project_id=NEW.project_id AND pipeline_id=NEW.pipeline_id AND archived_at IS NULL AND category=NEW.status) THEN RAISE(ABORT,'stage/status mismatch') END;
END;
CREATE TRIGGER crm_stage_update_guard BEFORE UPDATE OF category,archived_at ON crm_pipeline_stages WHEN NEW.category!=OLD.category OR (NEW.archived_at IS NOT NULL AND OLD.archived_at IS NULL) BEGIN
 SELECT CASE WHEN EXISTS(SELECT 1 FROM crm_opportunities WHERE stage_id=OLD.id AND project_id=OLD.project_id AND archived_at IS NULL) THEN RAISE(ABORT,'stage has active opportunities') END;
END;
