-- Revisions also advance for existing operator destination writers.
ALTER TABLE routing_destinations ADD COLUMN revision TEXT NOT NULL DEFAULT '';
UPDATE routing_destinations SET revision=lower(hex(randomblob(16)));
CREATE TRIGGER destination_revision_insert AFTER INSERT ON routing_destinations BEGIN
 UPDATE routing_destinations SET revision=lower(hex(randomblob(16))) WHERE id=NEW.id;
END;
CREATE TRIGGER destination_revision_update AFTER UPDATE OF name,kind,config_json,enabled ON routing_destinations
 WHEN NEW.name IS NOT OLD.name OR NEW.kind IS NOT OLD.kind OR NEW.config_json IS NOT OLD.config_json OR NEW.enabled IS NOT OLD.enabled BEGIN
 UPDATE routing_destinations SET revision=lower(hex(randomblob(16))) WHERE id=NEW.id;
 INSERT INTO telephony_policy_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TABLE telephony_resource_grants (
 project_id TEXT NOT NULL, id TEXT NOT NULL, principal_json TEXT NOT NULL,
 resource_kind TEXT NOT NULL, resource_id TEXT NOT NULL, actions_json TEXT NOT NULL,
 enabled INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, revision TEXT NOT NULL,
 administrator_install_id INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,id)
);
CREATE INDEX resource_grants_principal ON telephony_resource_grants(project_id,principal_json,enabled,deleted);
CREATE INDEX resource_grants_resource ON telephony_resource_grants(project_id,resource_kind,resource_id);
CREATE TRIGGER resource_grant_insert AFTER INSERT ON telephony_resource_grants BEGIN
 INSERT INTO telephony_policy_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER resource_grant_update AFTER UPDATE ON telephony_resource_grants BEGIN
 INSERT INTO telephony_policy_versions VALUES(NEW.project_id,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TABLE telephony_admin_receipts (
 project_id TEXT NOT NULL, administrator_install_id INTEGER NOT NULL, idempotency_key TEXT NOT NULL,
 request_hash TEXT NOT NULL, result_json TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,administrator_install_id,idempotency_key)
);
CREATE TABLE telephony_admin_audit (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL, administrator_install_id INTEGER NOT NULL,
 administrator_app TEXT NOT NULL, operation TEXT NOT NULL, resource_id TEXT NOT NULL,
 revision TEXT NOT NULL, idempotency_key TEXT NOT NULL, occurred_at TEXT NOT NULL
);
CREATE INDEX resource_admin_audit_project ON telephony_admin_audit(project_id,seq);
CREATE TABLE telephony_admin_state(project_id TEXT PRIMARY KEY, cursor_key TEXT NOT NULL);
